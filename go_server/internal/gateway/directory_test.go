package gateway_test

import (
	"errors"
	"testing"

	"traccar-server_go/go_server/internal/gateway"
)

// TestMemoryDirectoryModel pins that the model a decoder reads per model comes
// from the directory, and that an unknown identifier cannot carry one.
func TestMemoryDirectoryModel(t *testing.T) {
	directory := gateway.NewMemoryDirectory()

	const uniqueID = "864717003283581"
	if _, err := directory.Register(uniqueID, "gt06"); err != nil {
		t.Fatalf("register: %v", err)
	}

	session, err := directory.Resolve(uniqueID)
	if err != nil || session == nil {
		t.Fatalf("resolve = %v, %v", session, err)
	}
	if session.Model != "" {
		t.Errorf("model = %q, want empty", session.Model)
	}

	if err := directory.SetModel(uniqueID, "VL103"); err != nil {
		t.Fatalf("set model: %v", err)
	}
	session, err = directory.Resolve(uniqueID)
	if err != nil || session == nil {
		t.Fatalf("resolve after set = %v, %v", session, err)
	}
	if session.Model != "VL103" {
		t.Errorf("model = %q, want VL103", session.Model)
	}

	if err := directory.SetModel("no-such-device", "VL103"); !errors.Is(err, gateway.ErrUnknownDevice) {
		t.Errorf("set model on unknown id = %v, want ErrUnknownDevice", err)
	}
}
