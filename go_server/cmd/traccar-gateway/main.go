// Command traccar-gateway runs the Go protocol gateway for the H02, GT06,
// JT808 and Teltonika device families.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"traccar-server_go/go_server/internal/alert"
	"traccar-server_go/go_server/internal/api"
	"traccar-server_go/go_server/internal/auth"
	"traccar-server_go/go_server/internal/config"
	"traccar-server_go/go_server/internal/gateway"
	"traccar-server_go/go_server/internal/handler"
	"traccar-server_go/go_server/internal/metrics"
	"traccar-server_go/go_server/internal/model"
	"traccar-server_go/go_server/internal/notify"
	"traccar-server_go/go_server/internal/session"
	"traccar-server_go/go_server/internal/storage/postgres"
)

func main() {
	flags := flag.NewFlagSet("traccar-gateway", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)

	// The configuration file has to be read before the flags are registered:
	// every flag defaults to the value the file and the environment resolved,
	// so passing a flag is the only way to override them.
	argument, _ := config.PathFromArgs(os.Args[1:])
	tree, err := config.Load(argument, os.Environ())
	if err != nil {
		log.Fatal(err)
	}
	binder := config.Bind(flags, tree, tree.Root())
	if err := flags.Parse(os.Args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return
		}
		log.Fatalf("%v", err)
	}
	settings, err := binder.Settings()
	if err != nil {
		log.Fatalf("config: %v", err)
	}
	log.Printf("config: %s", tree.Summary())

	if settings.IssueToken != "" && settings.DatabaseURL == "" {
		log.Fatal("-issue-token needs -dsn")
	}

	deliveredTypes, err := notifyTypeFilter(settings.NotifyTypes)
	if err != nil {
		log.Fatalf("-notify-types: %v", err)
	}
	channels, mailer, err := notifyChannels(notifyChannelConfig{
		WebhookURL:           settings.WebhookURL,
		WebhookAuthorization: settings.WebhookAuthorization,
		SMTPAddress:          settings.EmailSMTP,
		EmailFrom:            settings.EmailFrom,
		EmailTo:              settings.EmailTo,
		EmailUsername:        settings.EmailUsername,
		EmailPassword:        settings.EmailPassword,
		EmailTLS:             settings.EmailTLS,
	})
	if err != nil {
		log.Fatalf("notify: %v", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	registry := gateway.DefaultRegistry()
	meters := metrics.New()

	var (
		directory  gateway.DeviceDirectory
		sink       gateway.Sink
		batcher    *postgres.BatchSink
		store      *postgres.Store
		pipeline   *alert.Pipeline
		dispatcher *notify.Dispatcher
		motion     *session.Motion
		sessions   *session.Manager
	)

	if settings.DatabaseURL != "" {
		store, err = postgres.New(ctx, settings.DatabaseURL)
		if err != nil {
			log.Fatalf("postgres: %v", err)
		}
		defer store.Close()

		if settings.IssueToken != "" {
			if err := issueAPIToken(ctx, store, settings.IssueToken,
				strings.Join(settings.TokenScopes, ","), settings.TokenTTL); err != nil {
				log.Fatalf("issue token: %v", err)
			}
			return
		}

		batcher = postgres.NewBatchSink(store, settings.BatchSize, settings.BatchInterval,
			postgres.WithBatchLogger(log.Printf),
			postgres.WithBatchMetrics(meters))
		directory = store

		engine := alert.New(alert.Config{
			OverspeedKnots: settings.OverspeedKnots,
			PowerCutVolts:  settings.PowerCutVolts,
			NotRepeat:      settings.AlertRepeat,
			PreferLowest:   settings.OverspeedLowest,
		}, nil)

		var hook func(*model.Event)
		if len(channels) > 0 {
			dispatcher = notify.NewDispatcher(store, channels,
				notify.WithLogger(log.Printf),
				notify.WithMetrics(meters),
				notify.WithTimeout(settings.NotifyTimeout),
				notify.WithQueueSize(settings.NotifyQueue),
				notify.WithSubscriptions(store),
				notify.WithTypes(deliveredTypes))
			hook = dispatcher.Enqueue
			log.Printf("notify: %s enabled", notifyChannelNames(channels))
		}

		pipeline = alert.NewPipeline(engine, nil, store,
			alert.WithLogger(log.Printf),
			alert.WithMetrics(meters),
			alert.WithAlertHook(hook))

		// The motion state machine records deviceMoving / deviceStopped, so it
		// needs somewhere to write them.
		motion = session.NewMotion(session.MotionConfig{
			Events: store,
			Logger: log.Printf,
		})

		// The chain is the ProcessingHandler: the alert pipeline is one stage of
		// it and the batch sink closes it.
		sink, err = handler.New(handler.Config{
			Alerts: pipeline,
			Store:  batcher,
			Stages: map[string]handler.PositionHandler{handler.StageMotion: motion},
		})
		if err != nil {
			log.Fatalf("handler: %v", err)
		}

		if err := reloadGeofences(ctx, store, engine); err != nil {
			log.Printf("alerts: load geofences: %v", err)
		}
		if settings.GeofenceRefresh > 0 {
			go refreshGeofences(ctx, store, engine, settings.GeofenceRefresh)
		}
		if err := reloadDeviceAttributes(ctx, store, engine); err != nil {
			log.Printf("alerts: load device attributes: %v", err)
		}
		if settings.DeviceRefresh > 0 {
			go refreshDeviceAttributes(ctx, store, engine, settings.DeviceRefresh)
		}
	} else {
		log.Print("no -dsn given, using the in-memory directory and a log sink")
		if settings.OverspeedKnots != 0 || settings.PowerCutVolts != 0 {
			log.Print("alert rules need -dsn, ignoring -overspeed-knots and -power-cut-volts")
		}
		if len(channels) > 0 {
			log.Print("alert notifications need -dsn, ignoring -notify-* flags")
		}
		directory = gateway.NewMemoryDirectory()

		logSink := gateway.SinkFunc(func(position *model.Position) error {
			encoded, err := json.Marshal(position)
			if err != nil {
				return err
			}
			log.Printf("position %s", encoded)
			return nil
		})
		sink, err = handler.New(handler.Config{Store: logSink})
		if err != nil {
			log.Fatalf("handler: %v", err)
		}
	}

	router := gateway.NewRouter(registry, directory)

	// The session manager owns devices.status and the online / offline events.
	// It is created after the router because the timeout has to close the
	// connection of the protocol the device connected with.
	var sessionsDone chan struct{}
	if store != nil {
		sessions = session.New(session.Config{
			Store:   store,
			Events:  store,
			Timeout: settings.ServerTimeout,
			Close: func(protocol string, deviceID int64) {
				if err := router.Disconnect(protocol, deviceID); err != nil {
					log.Printf("session: close device %d: %v", deviceID, err)
				}
			},
			Logger: log.Printf,
		})
		sessionsDone = make(chan struct{})
		go func() {
			defer close(sessionsDone)
			sessions.Run(ctx)
		}()
	}

	// One dispatcher serves the REST layer and the protocol servers: the REST
	// side queues a command, the answer path closes it, and the reconnect of a
	// device flushes what was waiting for it.
	var commands *gateway.Dispatcher
	if store != nil {
		commands = gateway.NewDispatcher(router, store,
			gateway.WithCommandRetry(settings.CommandRetry),
			gateway.WithCommandAttempts(settings.CommandAttempts),
			gateway.WithCommandQueue(settings.CommandQueue),
			gateway.WithCommandQueueTTL(settings.CommandQueueTTL),
			gateway.WithDispatcherLogger(log.Printf),
			gateway.WithDispatcherMetrics(meters),
		)
		go commands.Run(ctx)

		if settings.CommandRetention > 0 {
			go purgeCommandHistory(ctx, store, settings.CommandRetention)
		}
	}

	var servers sync.WaitGroup
	for _, protocol := range settings.Protocols {
		name, address := protocol.Name, protocol.Address
		if address == "" {
			continue
		}

		listener, err := net.Listen("tcp", address)
		if err != nil {
			log.Fatalf("%s: listen %s: %v", name, address, err)
		}

		options := []gateway.Option{
			gateway.WithReadTimeout(protocol.ReadTimeout),
			gateway.WithMetrics(meters),
		}
		if protocol.AutoRegister {
			options = append(options, gateway.WithAutoRegister())
		}
		if store != nil {
			// Device acknowledgements close the command rows the REST layer
			// wrote, and a device that reconnects gets what was queued for it,
			// so the gateway needs the same dispatcher.
			options = append(options, gateway.WithCommandTracker(commands))
			options = append(options, gateway.WithSessionTracker(sessions))
		}

		server, err := gateway.NewServer(registry, name, directory, sink, options...)
		if err != nil {
			log.Fatalf("%s: %v", name, err)
		}

		// Commands are routed by protocol, so every listener has to be
		// reachable from the REST layer.
		router.Register(name, server)

		log.Printf("%s: listening on %s", name, listener.Addr())

		servers.Add(1)
		go func(name string, server *gateway.Server, listener net.Listener) {
			defer servers.Done()
			if err := server.Serve(ctx, listener); err != nil {
				log.Printf("%s: %v", name, err)
			}
		}(name, server, listener)
	}

	// The API is created after the listeners so that every protocol server is
	// already registered in the router when the first request arrives.
	var apiServer *http.Server
	if settings.WebAddress != "" {
		if store == nil {
			log.Print("no -dsn given, REST API disabled")
		} else {
			options := []api.Option{
				api.WithPositions(store),
				api.WithGeofenceLookup(store),
				api.WithDevices(store),
				api.WithUsers(store),
				api.WithNotifications(store),
				api.WithCommands(router, store),
				api.WithCommandStore(store),
				api.WithCommandDeliverer(commands),
				api.WithMetrics(meters),
				api.WithHealthPinger(store),
				api.WithLogger(log.Printf),
				api.WithLoginLockout(settings.LoginMaxFailures, settings.LoginLockDuration),
				api.WithPermissions(store),
				api.WithResources(store),
				api.WithShareDisabled(settings.DisableShare),
			}
			if mailer != nil {
				options = append(options, api.WithPasswordReset(notify.NewPasswordResetMail(mailer)))
			} else {
				log.Print("api: no SMTP relay configured; password reset email is unavailable")
			}
			switch {
			case settings.AllowAnonymous:
				log.Print("api: -allow-anonymous set, REST is unauthenticated")
			case settings.APIKey == "":
				log.Print("api: no -api-key set; REST accepts only database tokens")
				options = append(options, api.WithTokens(store))
			default:
				options = append(options, api.WithAPIKey(settings.APIKey), api.WithTokens(store))
			}
			apiServer = &http.Server{
				Addr:              settings.WebAddress,
				Handler:           api.NewServer(store, options...).Handler(),
				ReadHeaderTimeout: 5 * time.Second,
				ReadTimeout:       15 * time.Second,
				WriteTimeout:      30 * time.Second,
				IdleTimeout:       60 * time.Second,
			}
			log.Printf("api: listening on %s", settings.WebAddress)
			go func() {
				if err := apiServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
					log.Printf("api: %v", err)
				}
			}()
		}
	}

	<-ctx.Done()
	log.Print("shutting down")

	servers.Wait()

	// The session manager writes the offline transitions of the connections
	// that just ended, so it drains after the servers are done.
	if sessionsDone != nil {
		<-sessionsDone
		if dropped := sessions.Dropped(); dropped > 0 {
			log.Printf("session: dropped %d status changes because the queue was full", dropped)
		}
	}

	if apiServer != nil {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		if err := apiServer.Shutdown(shutdownCtx); err != nil {
			log.Printf("api: shutdown: %v", err)
		}
		cancel()
	}

	// The alert pipeline sits in front of the position batcher, so it has to
	// stop first.
	if pipeline != nil {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		if err := pipeline.Close(shutdownCtx); err != nil {
			log.Printf("flush alerts: %v", err)
		}
		cancel()
		if dropped := pipeline.Dropped(); dropped > 0 {
			log.Printf("dropped %d alerts because the queue was full", dropped)
		}
	}

	// The pipeline feeds the notification queue, so it has to stop first.
	if dispatcher != nil {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		if err := dispatcher.Close(shutdownCtx); err != nil {
			log.Printf("flush notifications: %v", err)
		}
		cancel()
		if dropped := dispatcher.Dropped(); dropped > 0 {
			log.Printf("dropped %d notifications because the queue was full", dropped)
		}
	}

	if batcher != nil {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if err := batcher.Close(shutdownCtx); err != nil {
			log.Printf("flush positions: %v", err)
		}
		if dropped := batcher.Dropped(); dropped > 0 {
			log.Printf("dropped %d positions because the queue was full", dropped)
		}
	}
}

// issueAPIToken prints a fresh token for an existing user. Only the SHA-256
// hash is stored, so the value printed here cannot be recovered later.
func issueAPIToken(ctx context.Context, store *postgres.Store, email, rawScopes string, ttl time.Duration) error {
	if ttl < 0 {
		return errors.New("token ttl must not be negative")
	}
	scopes, err := auth.ParseScopes(rawScopes)
	if err != nil {
		return err
	}

	var expiresAt *time.Time
	if ttl > 0 {
		deadline := time.Now().Add(ttl)
		expiresAt = &deadline
	}

	token, err := store.IssueToken(ctx, email, scopes, expiresAt)
	if err != nil {
		return err
	}
	fmt.Println(token)
	return nil
}

// reloadGeofences replaces the engine's geofence set. A row with an
// unparseable area is reported but does not drop the geofences that parsed.
func reloadGeofences(ctx context.Context, store *postgres.Store, engine *alert.Engine) error {
	geofences, err := store.Geofences(ctx)
	engine.SetGeofences(geofences)
	return err
}

func refreshGeofences(ctx context.Context, store *postgres.Store, engine *alert.Engine, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := reloadGeofences(ctx, store, engine); err != nil {
				log.Printf("alerts: reload geofences: %v", err)
			}
		}
	}
}

// reloadDeviceAttributes refreshes the per-device threshold snapshot. Devices
// are created by the gateway itself, so the attributes set later with SQL are
// picked up on the next reload. A failed query keeps the previous snapshot.
func reloadDeviceAttributes(ctx context.Context, store *postgres.Store, engine *alert.Engine) error {
	attributes, err := store.DeviceAttributes(ctx)
	if err != nil {
		return err
	}
	engine.SetDeviceAttributes(attributes)
	return nil
}

func refreshDeviceAttributes(ctx context.Context, store *postgres.Store, engine *alert.Engine, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := reloadDeviceAttributes(ctx, store, engine); err != nil {
				log.Printf("alerts: reload device attributes: %v", err)
			}
		}
	}
}

// commandPurgeInterval is how often the retention sweep runs. It is fixed
// because a command history that grows for an hour longer than asked is not
// worth another flag.
const commandPurgeInterval = time.Hour

// purgeCommandHistory deletes finished commands older than retention, so the
// table stays bounded without an operator remembering to run a query. The first
// sweep runs after one interval: a gateway that restarts often never spends its
// startup deleting rows.
func purgeCommandHistory(ctx context.Context, store *postgres.Store, retention time.Duration) {
	ticker := time.NewTicker(commandPurgeInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			deleted, err := store.DeleteCommandsBefore(ctx, retention)
			if err != nil {
				log.Printf("commands: purge history: %v", err)
				continue
			}
			if deleted > 0 {
				log.Printf("commands: purged %d rows older than %s", deleted, retention)
			}
		}
	}
}

// notifyChannelConfig collects the -notify-* flags that configure the channels.
type notifyChannelConfig struct {
	WebhookURL           string
	WebhookAuthorization string
	SMTPAddress          string
	EmailFrom            string
	EmailTo              string
	EmailUsername        string
	EmailPassword        string
	EmailTLS             string
}

// notifyChannels builds the channels the flags enable. A channel with no flag
// is skipped; a partial configuration is an error so that a typo does not
// silently turn notifications off. The mail channel's SMTP client is returned
// too, because the password reset email reuses the same relay.
func notifyChannels(config notifyChannelConfig) ([]notify.Channel, *notify.Mail, error) {
	var (
		channels []notify.Channel
		mailer   *notify.Mail
	)

	if config.WebhookURL != "" {
		parsed, err := url.Parse(config.WebhookURL)
		if err != nil {
			return nil, nil, fmt.Errorf("webhook %q: %w", config.WebhookURL, err)
		}
		if parsed.Scheme != "http" && parsed.Scheme != "https" {
			return nil, nil, fmt.Errorf("webhook %q: want an http or https URL", config.WebhookURL)
		}
		channels = append(channels, notify.Channel{
			Notifier:  notify.NewWebhook(notify.WebhookConfig{Authorization: config.WebhookAuthorization}),
			Defaults:  []string{config.WebhookURL},
			Attribute: notify.AttrNotifyWebhook,
		})
	}

	if config.SMTPAddress != "" || config.EmailFrom != "" || config.EmailTo != "" {
		built, err := notify.NewMail(notify.MailConfig{
			Address:  config.SMTPAddress,
			From:     config.EmailFrom,
			Username: config.EmailUsername,
			Password: config.EmailPassword,
			TLS:      config.EmailTLS,
		})
		if err != nil {
			return nil, nil, err
		}
		mailer = built
		channels = append(channels, notify.Channel{
			Notifier:  mailer,
			Defaults:  notify.SplitList(config.EmailTo),
			Attribute: notify.AttrNotifyEmail,
		})
	}

	return channels, mailer, nil
}

// notifyTypeFilter validates -notify-types so that a misspelled type fails at
// startup instead of silently never matching. An empty value keeps every type.
func notifyTypeFilter(types []string) ([]string, error) {
	for _, alertType := range types {
		switch alertType {
		case "none", "all", "*":
		default:
			if !model.IsEventType(alertType) {
				return nil, fmt.Errorf("unknown alert type %q", alertType)
			}
		}
	}
	return types, nil
}

func notifyChannelNames(channels []notify.Channel) string {
	names := make([]string, 0, len(channels))
	for _, channel := range channels {
		names = append(names, channel.Notifier.Name())
	}
	return strings.Join(names, ", ")
}
