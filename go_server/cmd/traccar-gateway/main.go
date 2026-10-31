// Command traccar-gateway runs the Go protocol gateway for the H02, GT06,
// JT808 and Teltonika device families.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"traccar-server_go/go_server/internal/alert"
	"traccar-server_go/go_server/internal/api"
	"traccar-server_go/go_server/internal/gateway"
	"traccar-server_go/go_server/internal/metrics"
	"traccar-server_go/go_server/internal/model"
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
		"REST API key (defaults to TRACCAR_API_KEY); empty disables authentication")
	overspeedKnots := flag.Float64("overspeed-knots", 0, "overspeed alert threshold in knots; 0 disables the rule")
	powerCutVolts := flag.Float64("power-cut-volts", 0, "power cut alert threshold in volts; 0 disables the rule")
	alertRepeat := flag.Duration("alert-repeat", 0,
		"suppress repeat overspeed/powerCut alerts per device within this window; 0 disables")
	geofenceRefresh := flag.Duration("geofence-refresh", 5*time.Minute, "geofence reload interval; 0 loads them once")
	flag.Parse()

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	registry := gateway.DefaultRegistry()
	meters := metrics.New()

	var (
		directory gateway.DeviceDirectory
		sink      gateway.Sink
		batcher   *postgres.BatchSink
		store     *postgres.Store
		pipeline  *alert.Pipeline
	)

	if *dsn != "" {
		var err error
		store, err = postgres.New(ctx, *dsn)
		if err != nil {
			log.Fatalf("postgres: %v", err)
		}
		defer store.Close()

		batcher = postgres.NewBatchSink(store, *batchSize, *batchInterval,
			postgres.WithBatchLogger(log.Printf),
			postgres.WithBatchMetrics(meters))
		directory = store

		engine := alert.New(alert.Config{
			OverspeedKnots: *overspeedKnots,
			PowerCutVolts:  *powerCutVolts,
			NotRepeat:      *alertRepeat,
		}, nil)
		pipeline = alert.NewPipeline(engine, batcher, store,
			alert.WithLogger(log.Printf),
			alert.WithMetrics(meters))
		sink = pipeline

		if err := reloadGeofences(ctx, store, engine); err != nil {
			log.Printf("alerts: load geofences: %v", err)
		}
		if *geofenceRefresh > 0 {
			go refreshGeofences(ctx, store, engine, *geofenceRefresh)
		}
	} else {
		log.Print("no -dsn given, using the in-memory directory and a log sink")
		if *overspeedKnots != 0 || *powerCutVolts != 0 {
			log.Print("alert rules need -dsn, ignoring -overspeed-knots and -power-cut-volts")
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
			if *apiKey == "" {
				log.Print("api: no -api-key set, REST is unauthenticated")
			}
			apiServer = &http.Server{
				Addr: *httpAddress,
				Handler: api.NewServer(store,
					api.WithPositions(store),
					api.WithDevices(store),
					api.WithCommands(router, store),
					api.WithAPIKey(*apiKey),
					api.WithMetrics(meters),
					api.WithLogger(log.Printf),
				).Handler(),
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
