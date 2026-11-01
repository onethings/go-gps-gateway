package gateway_test

import (
	"context"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"

	"traccar-server_go/go_server/internal/codec"
	"traccar-server_go/go_server/internal/gateway"
	"traccar-server_go/go_server/internal/model"
)

// lineDecoder is the smallest possible polled protocol: the gateway writes
// "POLL\n" and the device answers with one comma separated position.
type lineDecoder struct {
	base codec.Base
}

func (d *lineDecoder) Name() string { return "testline" }

func (d *lineDecoder) Decode(frame []byte) (*codec.Result, error) {
	parts := strings.Split(strings.TrimSpace(string(frame)), ",")
	if len(parts) != 4 || parts[0] != "POS" {
		return &codec.Result{}, nil
	}
	if d.base.Resolver == nil {
		return &codec.Result{}, nil
	}
	session, err := d.base.Resolver.Resolve(parts[1])
	if err != nil || session == nil {
		return &codec.Result{}, nil
	}
	latitude, _ := strconv.ParseFloat(parts[2], 64)
	longitude, _ := strconv.ParseFloat(parts[3], 64)

	position := model.NewPosition("testline")
	position.DeviceID = session.DeviceID
	position.Latitude = latitude
	position.Longitude = longitude
	position.Valid = true
	position.Time = d.base.Time()

	result := codec.NewResult()
	result.AddPosition(position)
	return result, nil
}

type lineFrameDecoder struct{}

func (lineFrameDecoder) DecodeFrame(data []byte) ([]byte, int) {
	index := strings.IndexByte(string(data), '\n')
	if index < 0 {
		return nil, 0
	}
	return data[:index], index + 1
}

type pollEncoder struct{}

func (pollEncoder) EncodeCommand(*model.Command) ([]byte, error) { return []byte("POLL\n"), nil }

// TestPollerDialsAndDecodes runs the client-mode side: the gateway dials the
// device, writes the poll request and stores the answer.
func TestPollerDialsAndDecodes(t *testing.T) {
	registry := gateway.NewRegistry()
	registry.Add("testline", func(base codec.Base) *gateway.Protocol {
		return &gateway.Protocol{
			Name:            "testline",
			NewFrameDecoder: func() codec.FrameDecoder { return lineFrameDecoder{} },
			NewDecoder:      func() codec.Decoder { return &lineDecoder{base: base} },
			NewEncoder:      func() codec.Encoder { return pollEncoder{} },
		}
	})

	directory := gateway.NewMemoryDirectory()
	if _, err := directory.Register("555000111", "testline"); err != nil {
		t.Fatalf("register: %v", err)
	}

	positions := make(chan *model.Position, 4)
	server, err := gateway.NewServer(registry, "testline", directory,
		gateway.SinkFunc(func(position *model.Position) error {
			positions <- position
			return nil
		}),
		gateway.WithLogger(newTestLogger(t)))
	if err != nil {
		t.Fatalf("new server: %v", err)
	}

	device, gatewaySide := net.Pipe()
	t.Cleanup(func() { _ = device.Close() })

	// The device answers every poll request with one position.
	go func() {
		request := make([]byte, 5)
		for {
			if _, err := device.Read(request); err != nil {
				return
			}
			if _, err := device.Write([]byte("POS,555000111,1.5,2.5\n")); err != nil {
				return
			}
		}
	}()

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		server.Poll(ctx, gateway.PollConfig{
			Protocol: "testline",
			Address:  "device:1234",
			Interval: 10 * time.Millisecond,
			Dial: func(context.Context, string, string) (net.Conn, error) {
				return gatewaySide, nil
			},
		})
	}()

	select {
	case position := <-positions:
		if position.DeviceID == 0 || position.Latitude != 1.5 || position.Longitude != 2.5 {
			t.Errorf("position = %+v", position)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no position decoded from the poll answer")
	}

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Poll did not stop")
	}
}
