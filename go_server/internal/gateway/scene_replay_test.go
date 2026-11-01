package gateway_test

import (
	"context"
	"io"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"traccar-server_go/go_server/internal/devicescene"
	"traccar-server_go/go_server/internal/gateway"
	"traccar-server_go/go_server/internal/model"
)

// replayScene pushes every frame through a real gateway over net.Pipe. It
// returns the positions that reached the sink and the directory the gateway
// registered into, so a caller can check both ends.
func replayScene(t *testing.T, scene devicescene.Scene, preRegister, autoRegister bool) (int64, *gateway.MemoryDirectory) {
	t.Helper()

	frames, err := scene.Frames()
	if err != nil {
		t.Fatalf("frames: %v", err)
	}

	directory := gateway.NewMemoryDirectory()
	if preRegister && scene.UniqueID != "" {
		if _, err := directory.Register(scene.UniqueID, scene.Protocol); err != nil {
			t.Fatalf("register %q: %v", scene.UniqueID, err)
		}
	}

	var delivered atomic.Int64
	options := []gateway.Option{gateway.WithLogger(func(string, ...any) {})}
	if autoRegister {
		options = append(options, gateway.WithAutoRegister())
	}

	server, err := gateway.NewServer(gateway.DefaultRegistry(), scene.Protocol, directory,
		gateway.SinkFunc(func(*model.Position) error {
			delivered.Add(1)
			return nil
		}),
		options...,
	)
	if err != nil {
		t.Fatalf("new server: %v", err)
	}

	client, peer := net.Pipe()
	defer client.Close()
	go server.ServeConn(context.Background(), peer)
	go func() { _, _ = io.Copy(io.Discard, client) }()

	for _, frame := range frames {
		if _, err := client.Write(frame); err != nil {
			t.Fatalf("write: %v", err)
		}
	}

	deadline := time.Now().Add(3 * time.Second)
	for delivered.Load() < int64(scene.Positions) && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	// A frame that must be dropped needs the same window to prove it was.
	time.Sleep(20 * time.Millisecond)

	return delivered.Load(), directory
}

// TestScenesReachTheSink covers nine of the ten live lane predicates without a
// socket, so a broken scene or a protocol regression fails in the suite.
func TestScenesReachTheSink(t *testing.T) {
	scenes, err := devicescene.All()
	if err != nil {
		t.Fatalf("scenes: %v", err)
	}
	if len(scenes) < 4 {
		t.Fatalf("bundled scenes = %d, want at least 4", len(scenes))
	}

	for _, scene := range scenes {
		t.Run(scene.Name, func(t *testing.T) {
			autoRegister := scene.Name == "gt06-unknown"

			delivered, directory := replayScene(t, scene, !autoRegister, autoRegister)
			if delivered != int64(scene.Positions) {
				t.Errorf("positions = %d, want %d", delivered, scene.Positions)
			}

			session, err := directory.Resolve(scene.UniqueID)
			if err != nil {
				t.Fatalf("resolve: %v", err)
			}
			if autoRegister && session == nil {
				t.Errorf("unknown identifier %q was not registered", scene.UniqueID)
			}
			if !autoRegister && session == nil {
				t.Errorf("identifier %q missing from the directory", scene.UniqueID)
			}
		})
	}
}

// TestAutoRegisterOffRejectsUnknownDevice pins the flag the gateway gained, so
// the lane that turns it off does not depend on the shell script alone.
func TestAutoRegisterOffRejectsUnknownDevice(t *testing.T) {
	var unknown devicescene.Scene
	scenes, err := devicescene.All()
	if err != nil {
		t.Fatalf("scenes: %v", err)
	}
	for _, scene := range scenes {
		if scene.Name == "gt06-unknown" {
			unknown = scene
		}
	}
	if unknown.Name == "" {
		t.Fatal("gt06-unknown scene missing")
	}

	delivered, directory := replayScene(t, unknown, false, false)
	if delivered != 0 {
		t.Errorf("positions = %d, want 0", delivered)
	}
	if session, err := directory.Resolve(unknown.UniqueID); err != nil || session != nil {
		t.Errorf("resolve = %v, %v, want nil, nil", session, err)
	}
}

// TestAutoRegisterOnCreatesUnknownDevice is the other half of the flag, so both
// settings have a test rather than a note in the plan.
func TestAutoRegisterOnCreatesUnknownDevice(t *testing.T) {
	scenes, err := devicescene.All()
	if err != nil {
		t.Fatalf("scenes: %v", err)
	}

	var unknown devicescene.Scene
	for _, scene := range scenes {
		if scene.Name == "gt06-unknown" {
			unknown = scene
		}
	}
	if unknown.Name == "" {
		t.Fatal("gt06-unknown scene missing")
	}

	_, directory := replayScene(t, unknown, false, true)
	if session, err := directory.Resolve(unknown.UniqueID); err != nil || session == nil {
		t.Errorf("resolve = %v, %v, want a session", session, err)
	}
}

// TestScenesRegisterOnFirstContact replays every scene against an empty
// directory with -auto-register, which is what a fresh install does when the
// first real device connects. H02 and JT808 resolve the identifier inside the
// decoder instead of reporting it on the result, so before the resolver could
// register there was nothing to create the device row and both protocols
// dropped every frame.
func TestScenesRegisterOnFirstContact(t *testing.T) {
	scenes, err := devicescene.All()
	if err != nil {
		t.Fatalf("scenes: %v", err)
	}

	for _, scene := range scenes {
		t.Run(scene.Name, func(t *testing.T) {
			delivered, directory := replayScene(t, scene, false, true)
			if delivered != int64(scene.Positions) {
				t.Errorf("positions = %d, want %d", delivered, scene.Positions)
			}
			session, err := directory.Resolve(scene.UniqueID)
			if err != nil || session == nil {
				t.Fatalf("resolve %q = %v, %v, want a session", scene.UniqueID, session, err)
			}
		})
	}
}
