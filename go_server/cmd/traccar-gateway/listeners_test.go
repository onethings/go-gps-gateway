package main

import (
	"context"
	"testing"

	"traccar-server_go/go_server/internal/config"
	"traccar-server_go/go_server/internal/gateway"
	"traccar-server_go/go_server/internal/metrics"
	"traccar-server_go/go_server/internal/model"
)

// newTestListenerSet builds a listener set without any listener: the transport
// tests only exercise the decision logic, because the sandbox does not allow
// binding sockets.
func newTestListenerSet(t *testing.T, cfg *config.Config) *listenerSet {
	t.Helper()
	registry := gateway.DefaultRegistry()
	return newListenerSet(context.Background(), registry, gateway.NewMemoryDirectory(),
		gateway.SinkFunc(func(*model.Position) error { return nil }),
		gateway.NewRouter(registry, gateway.NewMemoryDirectory()),
		metrics.New(), nil, nil, cfg, func(string, ...any) {})
}

func TestTLSWithoutCertificateIsAnError(t *testing.T) {
	listeners := newTestListenerSet(t, loadConfig(t, `
	<entry key='protocol.gt06.address'>127.0.0.1:5023</entry>
	<entry key='protocol.gt06.transport'>tls</entry>`))

	_, err := listeners.tlsConfig()
	if err == nil {
		t.Fatal("tlsConfig succeeded without a certificate")
	}
}

func TestTLSWithMissingFilesIsAnError(t *testing.T) {
	listeners := newTestListenerSet(t, loadConfig(t, `
	<entry key='server.tls.certificate'>/nonexistent/cert.pem</entry>
	<entry key='server.tls.key'>/nonexistent/key.pem</entry>`))

	if _, err := listeners.tlsConfig(); err == nil {
		t.Fatal("tlsConfig succeeded with missing files")
	}
}

func TestPollDeviceIDFromConfiguration(t *testing.T) {
	listeners := newTestListenerSet(t, loadConfig(t, `
	<entry key='protocol.testline.poll.deviceId'>42</entry>`))
	section := listeners.config.Protocol("testline")

	deviceID, err := listeners.pollDeviceID(section.String(pollDeviceIDKey), "")
	if err != nil {
		t.Fatalf("pollDeviceID: %v", err)
	}
	if deviceID != 42 {
		t.Fatalf("device id = %d", deviceID)
	}
}

func TestPollDeviceIDUnknownUniqueIDIsZero(t *testing.T) {
	listeners := newTestListenerSet(t, loadConfig(t, ``))

	deviceID, err := listeners.pollDeviceID("", "nobody")
	if err != nil {
		t.Fatalf("pollDeviceID: %v", err)
	}
	if deviceID != 0 {
		t.Fatalf("device id = %d, want 0", deviceID)
	}
}
