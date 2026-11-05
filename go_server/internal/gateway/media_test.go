package gateway

import (
	"context"
	"encoding/binary"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"traccar-server_go/go_server/internal/codec"
	"traccar-server_go/go_server/internal/hls"
	"traccar-server_go/go_server/internal/protocol/jt1078"
)

type mediaDirectory struct {
	deviceID int64
	uniqueID string
}

func (d mediaDirectory) Resolve(uniqueID string) (*codec.DeviceSession, error) {
	if uniqueID != d.uniqueID {
		return nil, nil
	}
	return &codec.DeviceSession{DeviceID: d.deviceID, UniqueID: uniqueID}, nil
}

func (d mediaDirectory) UniqueID(int64) (string, error) { return d.uniqueID, nil }

// jt1078Packet builds one atomic JT1078 packet for the integration test.
func jt1078Packet(keyFrame bool, at time.Time, payload string) []byte {
	out := append([]byte(nil), jt1078.Magic...)
	out = append(out, 0x80, 0x80|98, 0x00, 0x01)
	out = append(out, 0x01, 0x35, 0x12, 0x34, 0x50, 0x06)
	out = append(out, 1)
	dataType := byte(1)
	if keyFrame {
		dataType = 0
	}
	out = append(out, dataType<<4)
	var stamp [8]byte
	binary.BigEndian.PutUint64(stamp[:], uint64(at.UnixMilli()))
	out = append(out, stamp[:]...)
	out = append(out, 0x00, 0x00, 0x00, 0x00)
	var length [2]byte
	binary.BigEndian.PutUint16(length[:], uint16(len(payload)))
	out = append(out, length[:]...)
	return append(out, payload...)
}

func TestJT1078ServerWritesPlayablePlaylist(t *testing.T) {
	root := t.TempDir()
	manager := hls.NewManager(root)

	server, err := NewServer(
		DefaultRegistry(), jt1078.ProtocolName,
		mediaDirectory{deviceID: 7, uniqueID: "013512345006"}, nil,
		WithMediaSink(manager),
	)
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	client, device := net.Pipe()
	done := make(chan struct{})
	go func() {
		defer close(done)
		server.ServeConn(ctx, device)
	}()

	base := time.UnixMilli(1700000000000)
	for index, packet := range [][]byte{
		jt1078Packet(true, base, "\x00\x00\x00\x01\x65idr-a"),
		jt1078Packet(false, base.Add(500*time.Millisecond), "\x00\x00\x00\x01\x41p-a"),
		jt1078Packet(true, base.Add(2*time.Second), "\x00\x00\x00\x01\x65idr-b"),
		jt1078Packet(true, base.Add(4*time.Second), "\x00\x00\x00\x01\x65idr-c"),
	} {
		if _, err := client.Write(packet); err != nil {
			t.Fatalf("write %d: %v", index, err)
		}
	}
	client.Close()
	<-done

	playlist := filepath.Join(root, "7", "index.m3u8")
	data, err := os.ReadFile(playlist)
	if err != nil {
		t.Fatalf("device playlist: %v", err)
	}
	if !strings.Contains(string(data), "1/0.ts") {
		t.Fatalf("playlist does not name the first segment:\n%s", data)
	}

	segment, err := os.ReadFile(filepath.Join(root, "7", "1", "0.ts"))
	if err != nil {
		t.Fatalf("segment: %v", err)
	}
	if len(segment) == 0 || len(segment)%188 != 0 {
		t.Fatalf("segment length = %d", len(segment))
	}
	for offset := 0; offset < len(segment); offset += 188 {
		if segment[offset] != 0x47 {
			t.Fatalf("segment packet at %d does not start with the sync byte", offset)
		}
	}
}
