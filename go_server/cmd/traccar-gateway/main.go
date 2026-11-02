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
	"strconv"
	"strings"
	"syscall"
	"time"

	"traccar-server_go/go_server/internal/alert"
	"traccar-server_go/go_server/internal/api"
	"traccar-server_go/go_server/internal/auth"
	"traccar-server_go/go_server/internal/config"
	"traccar-server_go/go_server/internal/eventctx"
	"traccar-server_go/go_server/internal/forward"
	"traccar-server_go/go_server/internal/gateway"
	"traccar-server_go/go_server/internal/geo"
	"traccar-server_go/go_server/internal/handler"
	"traccar-server_go/go_server/internal/media"
	"traccar-server_go/go_server/internal/metrics"
	"traccar-server_go/go_server/internal/model"
	"traccar-server_go/go_server/internal/notify"
	"traccar-server_go/go_server/internal/report"
	"traccar-server_go/go_server/internal/schedule"
	"traccar-server_go/go_server/internal/session"
	"traccar-server_go/go_server/internal/stats"
	"traccar-server_go/go_server/internal/storage/postgres"
)

// version is what /api/server reports; -ldflags can override it at build time.
var version = "dev"

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
		Config:               settings.Config,
	})
	if err != nil {
		log.Fatalf("notify: %v", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	registry := gateway.DefaultRegistry()
	meters := metrics.New()
	// The socket hub exists before the sinks so the pipeline can publish while
	// the API server is still being assembled.
	socketHub := api.NewSocketHub()

	var (
		directory     gateway.DeviceDirectory
		sink          gateway.Sink
		batcher       *postgres.BatchSink
		store         *postgres.Store
		pipeline      *alert.Pipeline
		dispatcher    *notify.Dispatcher
		motion        *session.Motion
		sessions      *session.Manager
		state         *eventState
		forwarder     *forward.Service
		speedLimit    *geo.SpeedLimits
		mapMatcher    *geo.Matcher
		locator       *geo.Locator
		scheduler     *schedule.Scheduler
		geoSettings   = geo.ReadSettings(settings.Config)
		mediaSettings = readMediaSettings(settings.Config)
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
			OverspeedKnots:          settings.OverspeedKnots,
			PowerCutVolts:           settings.PowerCutVolts,
			NotRepeat:               settings.AlertRepeat,
			PreferLowest:            settings.OverspeedLowest,
			GeofenceSegmentCrossing: settings.GeofenceSegmentCrossing,
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

		// Every event reaches the sockets; the notification queue only when an
		// operator configured a channel.
		alertHook := socketHub.PublishEvent
		if hook != nil {
			alertHook = func(event *model.Event) {
				socketHub.PublishEvent(event)
				hook(event)
			}
		}

		// The event rules and the proximity check need the links and
		// maintenance intervals, refreshed in the background like the alert
		// thresholds.
		state = newEventState()
		if err := state.reload(ctx, store); err != nil {
			log.Printf("events: load state: %v", err)
		}
		// The distance and filter stages compare a position against the last
		// stored one, so seed it for every device; otherwise every restart
		// would start each trip and its running total over.
		if err := state.seedLastPositions(ctx, store); err != nil {
			log.Printf("events: seed last positions: %v", err)
		}
		if settings.DeviceRefresh > 0 {
			go runEventStateRefresh(ctx, state, store, settings.DeviceRefresh)
		}

		// The forwarders read the device row from the same snapshot, so a
		// destination sees the position exactly as it was ingested.
		forwardSettings := readForwardConfig(settings.Config)
		if transports := buildForwarders(settings.Config, forwardSettings); len(transports) > 0 {
			forwarder = forward.New(forward.Config{
				QueueSize:   forwardSettings.queue,
				MaxBackoff:  forwardSettings.backoff,
				MaxAttempts: forwardSettings.attempts,
			}, transports, forward.WithLogger(log.Printf))
			log.Printf("forward: %s enabled", transportNames(transports))

			previous := alertHook
			alertHook = func(event *model.Event) {
				previous(event)
				forwarder.ForwardEvent(event,
					state.Device(event.DeviceID), state.LastPosition(event.DeviceID))
			}
		}

		positionHook := func(position *model.Position) {
			state.setPosition(position)
			socketHub.PublishPosition(position)
			forwarder.ForwardPosition(position, state.Device(position.DeviceID))
		}

		pipeline = alert.NewPipeline(engine, nil, store,
			alert.WithLogger(log.Printf),
			alert.WithMetrics(meters),
			alert.WithAlertHook(alertHook),
			alert.WithPositionHook(positionHook))

		// The motion state machine records deviceMoving / deviceStopped, so it
		// needs somewhere to write them.
		motion = session.NewMotion(session.MotionConfig{
			Events: store,
			Logger: log.Printf,
		})

		eventConfig := handler.EventConfig{
			IgnoreDuplicateAlarms:  settings.IgnoreDuplicateAlerts,
			FuelIncreaseThreshold:  settings.FuelIncreaseThreshold,
			FuelDropThreshold:      settings.FuelDropThreshold,
			AccelerationThreshold:  settings.BehaviorAcceleration,
			BrakingThreshold:       settings.BehaviorBraking,
			ProximityEnterDistance: settings.ProximityEnter,
			ProximityExitDistance:  settings.ProximityExit,
			UnaccompaniedDistance:  settings.Unaccompanied,
		}
		providers := handler.EventProviders{
			Attributes:    engine.DeviceAttributes,
			Maintenances:  state.Maintenances,
			LinkedDevices: state.LinkedDevices,
			LastPosition:  state.LastPosition,
		}

		// The speed limit service only ever answers from its cache here; a miss
		// schedules a background request, so a slow third party never delays the
		// ingest.
		stages := map[string]handler.PositionHandler{
			// The distance slot runs the outdated completion first, exactly like
			// the Java ProcessingHandler orders OutdatedHandler before
			// DistanceHandler and both before FilterHandler: a frame without a
			// fix borrows the last known location before it is measured.
			handler.StageDistance: handler.Pipeline{
				Stage: handler.StageDistance,
				Handlers: []handler.PositionHandler{
					handler.OutdatedHandler{
						Stage:        handler.StageDistance,
						LastPosition: state.LastPosition,
					},
					handler.DistanceHandler{
						Stage:        handler.StageDistance,
						LastPosition: state.LastPosition,
					},
				},
			},
			handler.StageFilter: handler.FilterHandler{
				Stage:        handler.StageFilter,
				Attributes:   engine.DeviceAttributes,
				LastPosition: state.LastPosition,
			},
			handler.StageGeofence: handler.GeofenceHandler{
				Stage:     handler.StageGeofence,
				Geofences: engine.Geofences,
			},
		}
		if geoSettings.SpeedLimit.Enabled {
			speedLimit = geo.NewSpeedLimits(geoSettings, nil, log.Printf)
			if err := speedLimit.Load(); err != nil {
				log.Printf("geo: load speed limit cache: %v", err)
			}
			stages[handler.StageSpeedLimit] = geo.Stage{Limits: speedLimit}
			log.Printf("geo: speed limits enabled via %s", geoSettings.SpeedLimit.URL)
		}
		if geoSettings.MapMatcher.Enabled {
			mapMatcher = geo.NewMatcher(geoSettings, nil)
			if err := mapMatcher.Load(); err != nil {
				log.Printf("geo: load map matcher cache: %v", err)
			}
			stages[handler.StageMapMatcher] = geo.MapMatcherStage{Matcher: mapMatcher}
			log.Printf("geo: map matching enabled via %s", geoSettings.MapMatcher.URL)
		}

		// The chain is the ProcessingHandler: the alert pipeline is the business
		// stage, the M4-1 rules sit on their own stages and the terminal writes
		// the position once, together with every event raised for it.
		sink, err = handler.New(handler.Config{
			Alerts: pipeline,
			Terminal: func(ctx context.Context, position *model.Position) error {
				events := eventctx.Events(ctx)
				if len(events) == 0 {
					return batcher.Handle(position)
				}
				pipeline.EnqueueReport(position, events)
				return nil
			},
			Stages: mergeStages(stages, map[string]handler.PositionHandler{
				handler.StageMotion: motion,
				handler.StageAlertEvent: handler.RuleStage{
					Stage: handler.StageAlertEvent,
					Rules: []handler.EventRule{
						&handler.AlarmRule{IgnoreDuplicates: eventConfig.IgnoreDuplicateAlarms},
						&handler.IgnitionRule{},
						&handler.BehaviorRule{Config: eventConfig, Providers: providers},
					},
				},
				handler.StageFuel: handler.RuleStage{
					Stage: handler.StageFuel,
					Rules: []handler.EventRule{
						&handler.FuelRule{Config: eventConfig, Providers: providers},
					},
				},
				handler.StageDriver: handler.RuleStage{
					Stage: handler.StageDriver,
					Rules: []handler.EventRule{&handler.DriverRule{}},
				},
				handler.StageMaintenance: handler.RuleStage{
					Stage: handler.StageMaintenance,
					Rules: []handler.EventRule{
						&handler.MaintenanceRule{Maintenances: state.Maintenances},
					},
				},
				handler.StageMedia: handler.RuleStage{
					Stage: handler.StageMedia,
					Rules: []handler.EventRule{&handler.MediaRule{}, &handler.CommandResultRule{}},
				},
			}),
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
			// The socket shows the new state, so it needs the full row.
			OnStatus: func(ctx context.Context, deviceID int64, status string) {
				device, err := store.Device(ctx, deviceID)
				if err != nil {
					return
				}
				socketHub.PublishDevice(device)
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
		options := []gateway.DispatchOption{
			gateway.WithCommandRetry(settings.CommandRetry),
			gateway.WithCommandAttempts(settings.CommandAttempts),
			gateway.WithCommandQueue(settings.CommandQueue),
			gateway.WithCommandQueueTTL(settings.CommandQueueTTL),
			gateway.WithDispatcherLogger(log.Printf),
			gateway.WithDispatcherMetrics(meters),
		}
		// Externalise the live retry bookkeeping to Redis when configured, so a
		// restart restores exact timing and multi-node deployments share the
		// queue view. The store is best-effort: if Redis is unreachable the
		// dispatcher still delivers from its in-memory queue.
		if url := settings.Config.String(config.DispatchRedisURL); url != "" {
			dispatchState, err := gateway.NewRedisDispatchStore(gateway.RedisDispatchConfig{
				URL: url,
			})
			if err != nil {
				log.Printf("dispatch: redis state disabled: %v", err)
			} else {
				dispatchState.SetLogger(log.Printf)
				options = append(options, gateway.WithDispatchState(dispatchState))
			}
		}
		commands = gateway.NewDispatcher(router, store, options...)
		go commands.Run(ctx)

		if settings.CommandRetention > 0 {
			go purgeCommandHistory(ctx, store, settings.CommandRetention)
		}
	}

	// The scheduled maintenance runs whether or not the REST layer is enabled: it
	// is about the data, not about serving requests.
	if store != nil {
		schedulerSettings := readSchedulerSettings(settings.Config)
		generator := schedule.Generator(reportGenerator{service: report.New(store, reportConfig(settings.Config))})
		var reporter schedule.Mailer
		if mailer != nil {
			reporter = scheduledReportMailer{mail: mailer}
		}
		scheduler = buildScheduler(schedulerSettings, store, generator, reporter, log.Printf)
		if names := scheduler.Tasks(); len(names) > 0 {
			log.Printf("schedule: %v", names)
			go scheduler.Run(ctx)
		}
	}

	// The statistics writer turns the process counters into the rows the
	// /api/statistics resource reads back.
	if store != nil && settings.StatisticsInterval > 0 {
		writer := &stats.Writer{
			Store:    store,
			Metrics:  meters,
			Interval: settings.StatisticsInterval,
		}
		go writer.Run(ctx)
		log.Printf("statistics: writing a row every %s", settings.StatisticsInterval)
	}

	// The protocol settings table starts from the configured listeners, so an
	// administrator sees one editable row per shipped protocol, and a row edited
	// earlier wins over the configuration file.
	if store != nil {
		defaults := make([]model.ProtocolSettings, 0, len(settings.Protocols))
		for _, protocol := range settings.Protocols {
			transport := protocol.Transport
			if transport == "" {
				transport = model.TransportTCP
			}
			defaults = append(defaults, model.ProtocolSettings{
				Protocol:  protocol.Name,
				Port:      portOf(protocol.Address),
				Transport: transport,
			})
		}
		if err := store.EnsureProtocolSettings(ctx, defaults); err != nil {
			log.Printf("api: seed protocol settings: %v", err)
		}
		if stored, err := store.ProtocolSettings(ctx); err != nil {
			log.Printf("api: load protocol settings: %v", err)
		} else {
			applyStoredProtocolSettings(settings.Protocols, stored)
		}
	}

	listeners := newListenerSet(ctx, registry, directory, sink, router, meters, commands, sessions,
		settings.Config, log.Printf)
	for _, protocol := range settings.Protocols {
		if err := listeners.start(protocol); err != nil {
			log.Fatal(err)
		}
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
				api.WithSocket(socketHub, store),
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
				api.WithProtocolSettings(store, listeners),
				api.WithServerSettings(store),
				api.WithStatistics(store),
				api.WithAudit(store),
				api.WithNotificators(notifyChannelList(channels)),
				api.WithNotifierSender(notifierService{channels: channels}),
				api.WithReports(report.New(store, reportConfig(settings.Config))),
				api.WithVersion(version),
				api.WithWebPath(settings.WebPath),
				api.WithReboot(stop),
			}
			if geoSettings.SpeedLimit.Enabled {
				options = append(options, api.WithSpeedLimits(speedLimitGetter{speedLimit: speedLimit}))
			}
			if geoSettings.Geolocation.Enabled {
				locator = geo.NewLocator(geoSettings, nil)
				locator.OnRequest = meters.GeolocationRequests.Inc
				if err := locator.Load(); err != nil {
					log.Printf("geo: load geolocation cache: %v", err)
				}
				options = append(options, api.WithGeolocation(locator))
				log.Print("geo: cell tower geolocation enabled")
			}
			options = append(options, api.WithRequestCounter(meters.Requests))
			if mediaSettings.Enabled {
				manager := &media.Manager{Store: store, Directory: mediaSettings.Directory}
				options = append(options, api.WithMedia(store, manager))
				log.Printf("media: storing uploads in %s", mediaSettings.Directory)
			}
			if geoSettings.Geocoder.Enabled {
				geocoder := geo.NewGeocoder(geoSettings, nil)
				geocoder.OnRequest = meters.GeocoderRequests.Inc
				if err := geocoder.Load(); err != nil {
					log.Printf("geo: load geocoder cache: %v", err)
				}
				options = append(options, api.WithGeocoder(geocoder))
				log.Printf("geo: reverse geocoding enabled (%s)", geoSettings.Geocoder.Type)
			}
			if mailer != nil {
				options = append(options, api.WithPasswordReset(notify.NewPasswordResetMail(mailer)))
				options = append(options, api.WithReportMailer(reportMailer{mail: mailer}))
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

	listeners.waitAll()

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

	if scheduler != nil {
		// Run returns when ctx is cancelled, which the shutdown path already did.
		scheduler.Close()
	}

	// The speed limit stage sits in the chain the pipeline closes, so its
	// background requests are drained next.
	if speedLimit != nil {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		if err := speedLimit.Close(shutdownCtx); err != nil {
			log.Printf("geo: speed limits: %v", err)
		}
		cancel()
		if err := speedLimit.Save(); err != nil {
			log.Printf("geo: save speed limit cache: %v", err)
		}
	}

	// The map matcher writes its snapped-coordinate cache to the same file, so
	// it is persisted together with the speed limits.
	if mapMatcher != nil {
		if err := mapMatcher.Save(); err != nil {
			log.Printf("geo: save map matcher cache: %v", err)
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

	// The forwarders are fed by the pipeline and dispatcher hooks, so they drain
	// after both.
	if forwarder != nil {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		if err := forwarder.Close(shutdownCtx); err != nil {
			log.Printf("flush forwards: %v", err)
		}
		cancel()
		if dropped := forwarder.Dropped(); dropped > 0 {
			log.Printf("dropped %d forwards because the queue was full", dropped)
		}
		if failed := forwarder.Failed(); failed > 0 {
			log.Printf("gave up on %d forwards after the retries", failed)
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

// mergeStages combines the stages the pipeline always installs with the ones a
// configured service adds, so enabling a service never silently replaces a rule.
func mergeStages(base, extra map[string]handler.PositionHandler) map[string]handler.PositionHandler {
	merged := make(map[string]handler.PositionHandler, len(base)+len(extra))
	for name, stage := range extra {
		merged[name] = stage
	}
	for name, stage := range base {
		merged[name] = stage
	}
	return merged
}

// speedLimitGetter adapts the cached limit service to the REST route, which
// wants to wait for the answer while the ingest path does not.
type speedLimitGetter struct {
	speedLimit *geo.SpeedLimits
}

// Limit implements api.SpeedLimits.
func (g speedLimitGetter) Limit(ctx context.Context, latitude, longitude float64) (float64, bool, error) {
	if g.speedLimit == nil {
		return 0, false, nil
	}
	return g.speedLimit.Fetch(ctx, latitude, longitude)
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
	// Config supplies the provider settings that have no flags.
	Config *config.Config
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
			UserTarget: func(subscription model.NotificationSubscription) []string {
				if subscription.Email == "" {
					return nil
				}
				return []string{subscription.Email}
			},
		})
	}

	extra, err := extraNotifyChannels(config.Config)
	if err != nil {
		return nil, nil, err
	}
	channels = append(channels, extra...)

	return channels, mailer, nil
}

// extraNotifyChannels builds the providers that have no command line flags. An
// unset provider is skipped; a provider with a partial configuration is an
// error, so a missing token cannot silently disable alerts.
func extraNotifyChannels(cfg *config.Config) ([]notify.Channel, error) {
	if cfg == nil {
		return nil, nil
	}
	value := func(key string) string { return strings.TrimSpace(cfg.String(key)) }
	var channels []notify.Channel

	if token := value(config.NotifyTelegramToken); token != "" {
		built, err := notify.NewTelegram(notify.TelegramConfig{
			Token:   token,
			BaseURL: value(config.NotifyTelegramBaseURL),
		})
		if err != nil {
			return nil, err
		}
		channels = append(channels, notify.Channel{
			Notifier: built,
			Defaults: notify.SplitList(value(config.NotifyTelegramChatID)),
			// A chat id has no per-device override in the Java server; the
			// account attribute is the per-user target.
			Attribute:  "notifyTelegram",
			UserTarget: userAttributeTargets("telegramChatId"),
		})
	}

	if token := value(config.NotifyPushoverToken); token != "" {
		built, err := notify.NewPushover(notify.PushoverConfig{
			Token: token,
			User:  value(config.NotifyPushoverUser),
			URL:   value(config.NotifyPushoverURL),
		})
		if err != nil {
			return nil, err
		}
		channels = append(channels, notify.Channel{
			Notifier:   built,
			Defaults:   notify.SplitList(value(config.NotifyPushoverUser)),
			Attribute:  "notifyPushover",
			UserTarget: userAttributeTargets("pushoverUserKey"),
		})
	}

	if account := value(config.NotifyFirebaseAccount); account != "" {
		built, err := notify.NewFirebase(notify.FirebaseConfig{
			ServiceAccount: account,
			Mode:           value(config.NotifyFirebaseMode),
			TokenURL:       value(config.NotifyFirebaseTokenURL),
			BaseURL:        value(config.NotifyFirebaseURL),
		})
		if err != nil {
			return nil, err
		}
		channels = append(channels, notify.Channel{
			Notifier:   built,
			Attribute:  "notifyFirebase",
			UserTarget: userAttributeTargets("notificationTokens"),
		})
	}

	if key := value(config.NotifyTraccarKey); key != "" {
		built, err := notify.NewTraccarPush(notify.TraccarPushConfig{
			Key: key,
			URL: value(config.NotifyTraccarURL),
		})
		if err != nil {
			return nil, err
		}
		channels = append(channels, notify.Channel{
			Notifier:   built,
			Attribute:  "notifyTraccar",
			UserTarget: userAttributeTargets("notificationTokens"),
		})
	}

	if token := value(config.NotifyWhatsappToken); token != "" {
		built, err := notify.NewWhatsapp(notify.WhatsappConfig{
			Token:            token,
			PhoneNumberID:    value(config.NotifyWhatsappPhoneID),
			TemplateName:     value(config.NotifyWhatsappTemplate),
			TemplateLanguage: value(config.NotifyWhatsappLanguage),
			URL:              value(config.NotifyWhatsappURL),
		})
		if err != nil {
			return nil, err
		}
		channels = append(channels, notify.Channel{
			Notifier:   built,
			Attribute:  "notifyWhatsapp",
			UserTarget: userAttributeTargets("phone"),
		})
	}

	if value(config.SMSHTTPURL) != "" || value(config.SMSAWSAccess) != "" {
		built, err := notify.NewSms(notify.SmsConfig{
			HTTPURL:             value(config.SMSHTTPURL),
			HTTPTemplate:        value(config.SMSHTTPTemplate),
			AuthorizationHeader: value(config.SMSHTTPAuthorizationHdr),
			Authorization:       value(config.SMSHTTPAuthorization),
			User:                value(config.SMSHTTPUser),
			Password:            value(config.SMSHTTPPassword),
			AWSAccess:           value(config.SMSAWSAccess),
			AWSSecret:           value(config.SMSAWSSecret),
			AWSRegion:           value(config.SMSAWSRegion),
			AWSEndpoint:         value(config.SMSAWSEndpoint),
		})
		if err != nil {
			return nil, err
		}
		channels = append(channels, notify.Channel{
			Notifier:   built,
			Attribute:  "notifySms",
			UserTarget: userAttributeTargets("phone"),
		})
	}

	return channels, nil
}

// userAttributeTargets resolves a per-user channel target from the users
// attributes row, splitting a list so notificationTokens can hold several.
func userAttributeTargets(key string) func(model.NotificationSubscription) []string {
	return func(subscription model.NotificationSubscription) []string {
		if subscription.Attributes == nil {
			return nil
		}
		return notify.SplitList(subscription.Attributes.GetString(key, ""))
	}
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
	return strings.Join(notifyChannelList(channels), ", ")
}

// notifyChannelList lists the delivery channels, which is what
// /api/notificators reports to the web interface.
func notifyChannelList(channels []notify.Channel) []string {
	names := make([]string, 0, len(channels))
	for _, channel := range channels {
		names = append(names, channel.Notifier.Name())
	}
	return names
}

// portOf extracts the port of a listen address; a malformed address reports
// zero, which disables the protocol row in the settings table.
func portOf(address string) int {
	_, port, err := net.SplitHostPort(address)
	if err != nil {
		return 0
	}
	value, err := strconv.Atoi(port)
	if err != nil {
		return 0
	}
	return value
}
