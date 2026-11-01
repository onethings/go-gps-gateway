package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"traccar-server_go/go_server/internal/config"
	"traccar-server_go/go_server/internal/forward"
	"traccar-server_go/go_server/internal/model"
)

// loadConfig writes a traccar.xml with the given entries and loads it.
func loadConfig(t *testing.T, entries string) *config.Config {
	t.Helper()
	path := filepath.Join(t.TempDir(), "traccar.xml")
	content := "<properties>\n" + entries + "\n</properties>"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	cfg, err := config.Load(path, nil)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	return cfg
}

func TestBuildForwardersReadsEveryDestination(t *testing.T) {
	cfg := loadConfig(t, `
	<entry key='forward.url'>http://example.test/positions</entry>
	<entry key='event.forward.url'>http://example.test/events</entry>
	<entry key='forward.mqtt.broker'>broker:1883</entry>
	<entry key='forward.mqtt.topic'>traccar</entry>
	<entry key='forward.redis.url'>redis://cache:6379</entry>
	<entry key='forward.wialon.address'>wialon:20332</entry>`)

	var names []string
	for _, transport := range buildForwarders(cfg, readForwardConfig(cfg)) {
		names = append(names, transport.Name())
	}
	want := "json, json, mqtt, redis, wialon"
	if got := strings.Join(names, ", "); got != want {
		t.Fatalf("transports = %s, want %s", got, want)
	}
}

func TestBuildForwardersPicksTheURLType(t *testing.T) {
	cfg := loadConfig(t, `
	<entry key='forward.url'>http://example.test/{uniqueId}</entry>
	<entry key='forward.type'>url</entry>`)

	var names []string
	for _, transport := range buildForwarders(cfg, readForwardConfig(cfg)) {
		names = append(names, transport.Name())
	}
	if got := strings.Join(names, ", "); got != "url" {
		t.Fatalf("transports = %s", got)
	}
}

func TestOnlyForKindKeepsOneFeed(t *testing.T) {
	probe := &probeTransport{}
	events := onlyForKind(probe, "event")
	if err := events.Send(context.Background(), forward.Message{Position: testForwardPosition()}); err != nil {
		t.Fatalf("send: %v", err)
	}
	if probe.sends != 0 {
		t.Fatalf("sends = %d, want the position dropped", probe.sends)
	}

	positions := onlyForKind(probe, "position")
	if err := positions.Send(context.Background(), forward.Message{Position: testForwardPosition()}); err != nil {
		t.Fatalf("send: %v", err)
	}
	if probe.sends != 1 {
		t.Fatalf("sends = %d, want the position delivered", probe.sends)
	}

	all := onlyForKind(probe, "all")
	if err := all.Send(context.Background(), forward.Message{Position: testForwardPosition()}); err != nil {
		t.Fatalf("send: %v", err)
	}
	if probe.sends != 2 {
		t.Fatalf("sends = %d, want both feeds delivered", probe.sends)
	}
}

type probeTransport struct {
	sends int
}

func (p *probeTransport) Name() string { return "probe" }

func (p *probeTransport) Send(_ context.Context, _ forward.Message) error {
	p.sends++
	return nil
}

func (p *probeTransport) Close() error { return nil }

func testForwardPosition() *model.Position {
	position := model.NewPosition("gt06")
	position.DeviceID = 7
	return position
}

func TestReadForwardConfigDefaults(t *testing.T) {
	settings := readForwardConfig(nil)
	if settings.queue != forward.DefaultQueueSize ||
		settings.attempts != forward.DefaultMaxAttempts ||
		settings.backoff != forward.DefaultMaxBackoff ||
		settings.timeout != forward.DefaultTimeout ||
		settings.kind != "all" {
		t.Fatalf("settings = %+v", settings)
	}
}

func TestReadForwardConfigOverrides(t *testing.T) {
	cfg := loadConfig(t, `
	<entry key='forward.queue'>8</entry>
	<entry key='forward.attempts'>2</entry>
	<entry key='forward.backoff'>5s</entry>
	<entry key='forward.timeout'>3s</entry>
	<entry key='forward.kind'>position</entry>`)

	settings := readForwardConfig(cfg)
	if settings.queue != 8 || settings.attempts != 2 {
		t.Fatalf("settings = %+v", settings)
	}
	if settings.backoff.String() != "5s" || settings.timeout.String() != "3s" {
		t.Fatalf("settings = %+v", settings)
	}
	if settings.kind != "position" {
		t.Fatalf("kind = %q", settings.kind)
	}
}
