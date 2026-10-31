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
	"traccar-server_go/go_server/internal/gateway"
	"traccar-server_go/go_server/internal/metrics"
	"traccar-server_go/go_server/internal/model"
	"traccar-server_go/go_server/internal/notify"
	"traccar-server_go/go_server/internal/storage/postgres"
)

func main() {
	addresses := map[string]*string{
		"h02":       flag.String("h02", "0.0.0.0:5013", "H02 listen address"),
		"gt06":      flag.String("gt06", "0.0.0.0:5023", "GT06 listen address"),
		"jt808":     flag.String("jt808", "0.0.0.0:8610", "JT808 listen address"),
		"teltonika": flag.String("teltonika", "0.0.0.0:5027", "Teltonika listen address"),
	}
	dsn := flag.String("dsn", "", "PostgreSQL DSN; empty uses an in-memory directory and a log sink")
	batchSize := flag.Int("batch-size", 500, "positions per database insert")
	batchInterval := flag.Duration("batch-interval", time.Second, "maximum delay before a batch is written")
	httpAddress := flag.String("http", "0.0.0.0:8082", "REST listen address; empty disables the API")
	apiKey := flag.String("api-key", os.Getenv("TRACCAR_API_KEY"),
		"REST API key (defaults to TRACCAR_API_KEY); empty means database tokens only")
	allowAnonymous := flag.Bool("allow-anonymous", false,
		"disable REST authentication entirely; local development only")
	issueToken := flag.String("issue-token", "",
		"issue an API token for this user email and exit (needs -dsn)")
	tokenScopes := flag.String("token-scopes", "read:positions,read:devices,read:alerts",
		"comma separated scopes for -issue-token")
	tokenTTL := flag.Duration("token-ttl", 0, "lifetime of an issued token; 0 never expires")
	overspeedKnots := flag.Float64("overspeed-knots", 0, "overspeed alert threshold in knots; 0 disables the rule")
	powerCutVolts := flag.Float64("power-cut-volts", 0, "power cut alert threshold in volts; 0 disables the rule")
	preferLowest := flag.Bool("overspeed-prefer-lowest", false,
		"use the lowest geofence speedLimit when several geofences contain a position (default: highest)")
	alertRepeat := flag.Duration("alert-repeat", 0,
		"suppress repeat overspeed/powerCut alerts per device within this window; 0 disables")
	geofenceRefresh := flag.Duration("geofence-refresh", 5*time.Minute, "geofence reload interval; 0 loads them once")
	deviceRefresh := flag.Duration("device-refresh", 5*time.Minute,
		"device attribute reload interval for per-device thresholds; 0 loads them once")
	notifyWebhook := flag.String("notify-webhook", "",
		"webhook URL that receives alert JSON; empty disables the webhook channel")
	notifyWebhookAuthorization := flag.String("notify-webhook-authorization", "",
		"Authorization header sent with every webhook request")
	notifySMTP := flag.String("notify-email-smtp", "",
		"SMTP host:port for alert email; empty disables the mail channel")
	notifyEmailFrom := flag.String("notify-email-from", "", "sender address of alert email")
	notifyEmailTo := flag.String("notify-email-to", "",
		"default alert email recipients, comma separated; empty uses only per-device notifyEmail")
	notifyEmailUsername := flag.String("notify-email-username", "", "SMTP username; empty disables AUTH")
	notifyEmailPassword := flag.String("notify-email-password", os.Getenv("TRACCAR_SMTP_PASSWORD"),
		"SMTP password (defaults to TRACCAR_SMTP_PASSWORD)")
	notifyEmailTLS := flag.String("notify-email-tls", notify.MailTLSAuto,
		"SMTP TLS mode: auto, starttls, implicit or none")
	notifyTypes := flag.String("notify-types", "",
		"comma separated alert types to deliver; empty delivers every type")
	notifyTimeout := flag.Duration("notify-timeout", 10*time.Second,
		"timeout of one device lookup or notification send")
	notifyQueue := flag.Int("notify-queue", 256, "alerts that may wait for notification delivery")
	flag.Parse()

	if *issueToken != "" && *dsn == "" {
		log.Fatal("-issue-token needs -dsn")
	}

	deliveredTypes, err := notifyTypeFilter(*notifyTypes)
	if err != nil {
		log.Fatalf("-notify-types: %v", err)
	}
	channels, err := notifyChannels(notifyChannelConfig{
		WebhookURL:           *notifyWebhook,
		WebhookAuthorization: *notifyWebhookAuthorization,
		SMTPAddress:          *notifySMTP,
		EmailFrom:            *notifyEmailFrom,
		EmailTo:              *notifyEmailTo,
		EmailUsername:        *notifyEmailUsername,
		EmailPassword:        *notifyEmailPassword,
		EmailTLS:             *notifyEmailTLS,
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
	)

	if *dsn != "" {
		store, err = postgres.New(ctx, *dsn)
		if err != nil {
			log.Fatalf("postgres: %v", err)
		}
		defer store.Close()

		if *issueToken != "" {
			if err := issueAPIToken(ctx, store, *issueToken, *tokenScopes, *tokenTTL); err != nil {
				log.Fatalf("issue token: %v", err)
			}
			return
		}

		batcher = postgres.NewBatchSink(store, *batchSize, *batchInterval,
			postgres.WithBatchLogger(log.Printf),
			postgres.WithBatchMetrics(meters))
		directory = store

		engine := alert.New(alert.Config{
			OverspeedKnots: *overspeedKnots,
			PowerCutVolts:  *powerCutVolts,
			NotRepeat:      *alertRepeat,
			PreferLowest:   *preferLowest,
		}, nil)

		var hook func(*model.Alert)
		if len(channels) > 0 {
			dispatcher = notify.NewDispatcher(store, channels,
				notify.WithLogger(log.Printf),
				notify.WithMetrics(meters),
				notify.WithTimeout(*notifyTimeout),
				notify.WithQueueSize(*notifyQueue),
				notify.WithTypes(deliveredTypes))
			hook = dispatcher.Enqueue
			log.Printf("notify: %s enabled", notifyChannelNames(channels))
		}

		pipeline = alert.NewPipeline(engine, batcher, store,
			alert.WithLogger(log.Printf),
			alert.WithMetrics(meters),
			alert.WithAlertHook(hook))
		sink = pipeline

		if err := reloadGeofences(ctx, store, engine); err != nil {
			log.Printf("alerts: load geofences: %v", err)
		}
		if *geofenceRefresh > 0 {
			go refreshGeofences(ctx, store, engine, *geofenceRefresh)
		}
		if err := reloadDeviceAttributes(ctx, store, engine); err != nil {
			log.Printf("alerts: load device attributes: %v", err)
		}
		if *deviceRefresh > 0 {
			go refreshDeviceAttributes(ctx, store, engine, *deviceRefresh)
		}
	} else {
		log.Print("no -dsn given, using the in-memory directory and a log sink")
		if *overspeedKnots != 0 || *powerCutVolts != 0 {
			log.Print("alert rules need -dsn, ignoring -overspeed-knots and -power-cut-volts")
		}
		if len(channels) > 0 {
			log.Print("alert notifications need -dsn, ignoring -notify-* flags")
		}
		directory = gateway.NewMemoryDirectory()
		sink = gateway.SinkFunc(func(position *model.Position) error {
			encoded, err := json.Marshal(position)
			if err != nil {
				return err
			}
			log.Printf("position %s", encoded)
			return nil
		})
	}

	router := gateway.NewRouter(registry, directory)

	var servers sync.WaitGroup
	for _, name := range registry.Names() {
		address := addresses[name]
		if address == nil || *address == "" {
			continue
		}

		listener, err := net.Listen("tcp", *address)
		if err != nil {
			log.Fatalf("%s: listen %s: %v", name, *address, err)
		}

		server, err := gateway.NewServer(registry, name, directory, sink,
			gateway.WithReadTimeout(10*time.Minute),
			gateway.WithAutoRegister(),
			gateway.WithMetrics(meters),
		)
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
	if *httpAddress != "" {
		if store == nil {
			log.Print("no -dsn given, REST API disabled")
		} else {
			options := []api.Option{
				api.WithPositions(store),
				api.WithDevices(store),
				api.WithUsers(store),
				api.WithCommands(router, store),
				api.WithMetrics(meters),
				api.WithHealthPinger(store),
				api.WithLogger(log.Printf),
			}
			switch {
			case *allowAnonymous:
				log.Print("api: -allow-anonymous set, REST is unauthenticated")
			case *apiKey == "":
				log.Print("api: no -api-key set; REST accepts only database tokens")
				options = append(options, api.WithTokens(store))
			default:
				options = append(options, api.WithAPIKey(*apiKey), api.WithTokens(store))
			}
			apiServer = &http.Server{
				Addr:              *httpAddress,
				Handler:           api.NewServer(store, options...).Handler(),
				ReadHeaderTimeout: 5 * time.Second,
				ReadTimeout:       15 * time.Second,
				WriteTimeout:      30 * time.Second,
				IdleTimeout:       60 * time.Second,
			}
			log.Printf("api: listening on %s", *httpAddress)
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
// silently turn notifications off.
func notifyChannels(config notifyChannelConfig) ([]notify.Channel, error) {
	var channels []notify.Channel

	if config.WebhookURL != "" {
		parsed, err := url.Parse(config.WebhookURL)
		if err != nil {
			return nil, fmt.Errorf("webhook %q: %w", config.WebhookURL, err)
		}
		if parsed.Scheme != "http" && parsed.Scheme != "https" {
			return nil, fmt.Errorf("webhook %q: want an http or https URL", config.WebhookURL)
		}
		channels = append(channels, notify.Channel{
			Notifier:  notify.NewWebhook(notify.WebhookConfig{Authorization: config.WebhookAuthorization}),
			Defaults:  []string{config.WebhookURL},
			Attribute: notify.AttrNotifyWebhook,
		})
	}

	if config.SMTPAddress != "" || config.EmailFrom != "" || config.EmailTo != "" {
		mailer, err := notify.NewMail(notify.MailConfig{
			Address:  config.SMTPAddress,
			From:     config.EmailFrom,
			Username: config.EmailUsername,
			Password: config.EmailPassword,
			TLS:      config.EmailTLS,
		})
		if err != nil {
			return nil, err
		}
		channels = append(channels, notify.Channel{
			Notifier:  mailer,
			Defaults:  notify.SplitList(config.EmailTo),
			Attribute: notify.AttrNotifyEmail,
		})
	}

	return channels, nil
}

// notifyTypeFilter validates -notify-types so that a misspelled type fails at
// startup instead of silently never matching. An empty value keeps every type.
func notifyTypeFilter(value string) ([]string, error) {
	types := notify.SplitList(value)
	for _, alertType := range types {
		switch alertType {
		case "none", "all", "*",
			model.AlertOverspeed, model.AlertPowerCut, model.AlertPowerRestored,
			model.AlertGeofenceEnter, model.AlertGeofenceExit:
		default:
			return nil, fmt.Errorf("unknown alert type %q", alertType)
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
