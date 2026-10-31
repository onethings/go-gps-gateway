package gateway_test

import (
	"context"
	"encoding/hex"
	"io"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"traccar-server_go/go_server/internal/codec"
	"traccar-server_go/go_server/internal/gateway"
	"traccar-server_go/go_server/internal/model"
)

var (
	gt06LoginFrame    = mustBenchHex("78780d01086471700328358100093f040d0a")
	gt06PositionFrame = mustBenchHex("78781f120f0a140e150bc505e51e780293a9e800540000f601006e0055da00035f240d0a")
)

func mustBenchHex(value string) []byte {
	data, err := hex.DecodeString(value)
	if err != nil {
		panic(err)
	}
	return data
}

type benchResolver struct{}

func (benchResolver) Resolve(uniqueID string) (*codec.DeviceSession, error) {
	return &codec.DeviceSession{DeviceID: 1, UniqueID: uniqueID}, nil
}

func (benchResolver) UniqueID(int64) (string, error) { return "864717003283581", nil }

// BenchmarkGatewayIngest measures one position from the socket write to the
// sink. It runs over net.Pipe because the suite never opens a port.
func BenchmarkGatewayIngest(b *testing.B) {
	directory := gateway.NewMemoryDirectory()
	if _, err := directory.Register("864717003283581", "gt06"); err != nil {
		b.Fatalf("register: %v", err)
	}

	var ingested atomic.Int64
	server, err := gateway.NewServer(gateway.DefaultRegistry(), "gt06", directory,
		gateway.SinkFunc(func(*model.Position) error {
			ingested.Add(1)
			return nil
		}),
		gateway.WithLogger(func(string, ...any) {}),
	)
	if err != nil {
		b.Fatalf("new server: %v", err)
	}

	client, peer := net.Pipe()
	defer client.Close()
	go server.ServeConn(context.Background(), peer)
	// The server answers the login and every position, and a blocked write
	// would stall its read loop, so the client side has to keep draining.
	go func() { _, _ = io.Copy(io.Discard, client) }()

	if _, err := client.Write(gt06LoginFrame); err != nil {
		b.Fatalf("login: %v", err)
	}

	b.SetBytes(int64(len(gt06PositionFrame)))
	b.ReportAllocs()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		if _, err := client.Write(gt06PositionFrame); err != nil {
			b.Fatalf("write %d: %v", i, err)
		}
	}
	b.StopTimer()

	deadline := time.Now().Add(10 * time.Second)
	for ingested.Load() < int64(b.N) && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if got := ingested.Load(); got != int64(b.N) {
		b.Fatalf("sink saw %d of %d positions", got, b.N)
	}

	positions := float64(ingested.Load())
	b.ReportMetric(positions/b.Elapsed().Seconds(), "positions/s")
}

// BenchmarkDecodeGT06 isolates the decode step so a model branch added later
// can be compared against this baseline without the socket in the way.
func BenchmarkDecodeGT06(b *testing.B) {
	factory, err := gateway.DefaultRegistry().Get("gt06")
	if err != nil {
		b.Fatalf("registry: %v", err)
	}

	decoder := factory(codec.Base{Resolver: benchResolver{}, UniqueIDs: benchResolver{}}).NewDecoder()
	withSession, ok := decoder.(codec.SessionDecoder)
	if !ok {
		b.Fatal("gt06 decoder does not accept a session")
	}

	session := &codec.DeviceSession{DeviceID: 1, UniqueID: "864717003283581"}
	b.ReportAllocs()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		result, err := withSession.DecodeWithSession(gt06PositionFrame, session)
		if err != nil {
			b.Fatalf("decode: %v", err)
		}
		if len(result.Positions) != 1 {
			b.Fatalf("positions = %d, want 1", len(result.Positions))
		}
	}
}
