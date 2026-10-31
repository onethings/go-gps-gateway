package main

import (
	"bytes"
	"encoding/hex"
	"io"
	"net"
	"testing"
	"time"

	"traccar-server_go/go_server/internal/devicescene"
)

func bundledScene(t *testing.T, name string) *devicescene.Scene {
	t.Helper()

	scene, err := load(name)
	if err != nil {
		t.Fatalf("load %s: %v", name, err)
	}
	return scene
}

func TestLoadAcceptsBundledName(t *testing.T) {
	scene := bundledScene(t, "gt06")
	if scene.Protocol != "gt06" {
		t.Errorf("protocol = %q, want gt06", scene.Protocol)
	}

	if _, err := load("no-such-scene"); err == nil {
		t.Error("load accepted an unknown name")
	}
}

// TestWriteModesReplaySameBytes pins the three write patterns to the same
// stream, because the lanes rely on split and batch differing only in framing.
func TestWriteModesReplaySameBytes(t *testing.T) {
	frames, err := bundledScene(t, "gt06").Frames()
	if err != nil {
		t.Fatalf("frames: %v", err)
	}

	var want bytes.Buffer
	for _, frame := range frames {
		want.Write(frame)
	}

	for _, mode := range []string{modeSingle, modeSplit, modeBatch} {
		t.Run(mode, func(t *testing.T) {
			client, server := net.Pipe()
			defer server.Close()

			received := make(chan []byte, 1)
			go func() {
				data, _ := io.ReadAll(server)
				received <- data
			}()

			if err := writeFrames(client, frames, mode); err != nil {
				t.Fatalf("write: %v", err)
			}
			client.Close()

			select {
			case got := <-received:
				if !bytes.Equal(got, want.Bytes()) {
					t.Errorf("wrote %s, want %s", hex.EncodeToString(got), hex.EncodeToString(want.Bytes()))
				}
			case <-time.After(3 * time.Second):
				t.Fatal("peer received nothing")
			}
		})
	}
}

func TestReadRepliesCollectsUntilClose(t *testing.T) {
	client, server := net.Pipe()

	go func() {
		_, _ = server.Write([]byte{0x01, 0x02})
		time.Sleep(10 * time.Millisecond)
		_, _ = server.Write([]byte{0x03})
		server.Close()
	}()

	replies, err := readReplies(client, 200*time.Millisecond)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(replies) != 2 {
		t.Fatalf("replies = %d, want 2", len(replies))
	}

	joined := append(append([]byte(nil), replies[0]...), replies[1]...)
	if !bytes.Equal(joined, []byte{0x01, 0x02, 0x03}) {
		t.Errorf("replies = %x, want 010203", joined)
	}
}
