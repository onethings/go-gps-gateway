package gateway_test

import (
	"bytes"
	"errors"
	"strings"
	"testing"
	"time"

	"traccar-server_go/go_server/internal/gateway"
	"traccar-server_go/go_server/internal/model"
)

// gt06Login is the login frame used by the other gateway tests: it binds IMEI
// 864717003283581 (device id 1) to the connection.
const gt06Login = "78780d01086471700328358100093f040d0a"

func newRouter(t *testing.T, protocolName string, directory gateway.DeviceDirectory, server *gateway.Server) *gateway.Router {
	t.Helper()
	router := gateway.NewRouter(gateway.DefaultRegistry(), directory)
	router.Register(protocolName, server)
	return router
}

func TestRouterEncodeCommand(t *testing.T) {
	directory := gateway.NewMemoryDirectory()
	if _, err := directory.Register("135790246811220", "h02"); err != nil {
		t.Fatalf("register: %v", err)
	}
	router := gateway.NewRouter(gateway.DefaultRegistry(), directory)

	payload, err := router.EncodeCommand("h02", model.NewCommand(1, model.CommandAlarmArm))
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	// The encoder needs the unique id from the directory, so this also covers
	// the Base.UniqueIDs wiring.
	text := string(payload)
	if !strings.HasPrefix(text, "*HQ,135790246811220,SCF,") || !strings.HasSuffix(text, ",0,0#") {
		t.Fatalf("payload = %q", text)
	}
}

func TestRouterEncodeUnsupported(t *testing.T) {
	directory := gateway.NewMemoryDirectory()
	if _, err := directory.Register("864717003283581", "gt06"); err != nil {
		t.Fatalf("register: %v", err)
	}
	router := gateway.NewRouter(gateway.DefaultRegistry(), directory)

	_, err := router.EncodeCommand("gt06", model.NewCommand(1, model.CommandPositionSingle))
	if !errors.Is(err, gateway.ErrCommandUnsupported) {
		t.Fatalf("err = %v, want ErrCommandUnsupported", err)
	}
}

func TestRouterUnknownProtocol(t *testing.T) {
	directory := gateway.NewMemoryDirectory()
	router := gateway.NewRouter(gateway.DefaultRegistry(), directory)

	if _, err := router.EncodeCommand("h02x", model.NewCommand(1, model.CommandAlarmArm)); !errors.Is(err, gateway.ErrUnknownProtocol) {
		t.Fatalf("encode err = %v, want ErrUnknownProtocol", err)
	}
	if err := router.Send("h02x", 1, []byte("x")); !errors.Is(err, gateway.ErrUnknownProtocol) {
		t.Fatalf("send err = %v, want ErrUnknownProtocol", err)
	}
}

func TestRouterSendOffline(t *testing.T) {
	directory := gateway.NewMemoryDirectory()
	server := newServer(t, "gt06", directory, nil)
	router := newRouter(t, "gt06", directory, server)

	if err := router.Send("gt06", 1, []byte("x")); !errors.Is(err, gateway.ErrDeviceOffline) {
		t.Fatalf("err = %v, want ErrDeviceOffline", err)
	}
	if router.Online("gt06", 1) {
		t.Error("online = true, want false")
	}
}

func TestRouterSendGT06(t *testing.T) {
	directory := gateway.NewMemoryDirectory()
	if _, err := directory.Register("864717003283581", "gt06"); err != nil {
		t.Fatalf("register: %v", err)
	}
	server := newServer(t, "gt06", directory, nil)
	router := newRouter(t, "gt06", directory, server)

	conn := startConn(t, server)
	write(t, conn, mustHex(t, gt06Login))
	if ack := readN(t, conn, 10); ack[3] != 0x01 {
		t.Fatalf("login ack = %x", ack)
	}

	if !router.Online("gt06", 1) {
		t.Fatal("online = false after login")
	}

	payload, err := router.EncodeCommand("gt06", model.NewCommand(1, model.CommandEngineStop))
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	if !bytes.Contains(payload, []byte("Relay,1#")) {
		t.Fatalf("payload = %x", payload)
	}

	// net.Pipe has no buffer, so the write has to happen on another goroutine.
	errc := make(chan error, 1)
	go func() { errc <- router.Send("gt06", 1, payload) }()

	if got := readChunk(t, conn); !bytes.Equal(got, payload) {
		t.Fatalf("command = %x, want %x", got, payload)
	}
	if err := <-errc; err != nil {
		t.Fatalf("send: %v", err)
	}
}

// TestRouterSendH02 covers a protocol without a handshake: the first frame
// that resolves to a device is what binds the connection, so the command can
// still be delivered.
func TestRouterSendH02(t *testing.T) {
	directory := gateway.NewMemoryDirectory()
	if _, err := directory.Register("135790246811220", "h02"); err != nil {
		t.Fatalf("register: %v", err)
	}

	positions := make(chan *model.Position, 4)
	server := newServer(t, "h02", directory, gateway.SinkFunc(func(position *model.Position) error {
		positions <- position
		return nil
	}))
	router := newRouter(t, "h02", directory, server)

	conn := startConn(t, server)

	write(t, conn, []byte("*HQ,135790246811220,HTBT,100#"))
	nextPosition(t, positions)
	if ack := readChunk(t, conn); len(ack) == 0 {
		t.Fatal("no heartbeat response")
	}

	if !router.Online("h02", 1) {
		t.Fatal("online = false after the first frame")
	}

	payload, err := router.EncodeCommand("h02", model.NewCommand(1, model.CommandAlarmArm))
	if err != nil {
		t.Fatalf("encode: %v", err)
	}

	errc := make(chan error, 1)
	go func() { errc <- router.Send("h02", 1, payload) }()

	if got := readChunk(t, conn); !bytes.Contains(got, []byte(",SCF,")) {
		t.Fatalf("command = %q, want an SCF sentence", got)
	}
	if err := <-errc; err != nil {
		t.Fatalf("send: %v", err)
	}
}

func TestServerReconnectClosesPrevious(t *testing.T) {
	directory := gateway.NewMemoryDirectory()
	if _, err := directory.Register("864717003283581", "gt06"); err != nil {
		t.Fatalf("register: %v", err)
	}
	server := newServer(t, "gt06", directory, nil)

	first := startConn(t, server)
	write(t, first, mustHex(t, gt06Login))
	if ack := readN(t, first, 10); ack[3] != 0x01 {
		t.Fatalf("first login ack = %x", ack)
	}

	second := startConn(t, server)
	write(t, second, mustHex(t, gt06Login))
	if ack := readN(t, second, 10); ack[3] != 0x01 {
		t.Fatalf("second login ack = %x", ack)
	}

	_ = first.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, err := first.Read(make([]byte, 1)); err == nil {
		t.Fatal("previous connection is still open")
	}

	errc := make(chan error, 1)
	go func() { errc <- server.Send(1, []byte("cmd")) }()
	if got := readChunk(t, second); string(got) != "cmd" {
		t.Fatalf("command = %q, want %q", got, "cmd")
	}
	if err := <-errc; err != nil {
		t.Fatalf("send: %v", err)
	}

	// The old connection returning must not unhook the new one.
	if !server.Online(1) {
		t.Fatal("online = false after the previous connection closed")
	}
}

// TestRouterEncodesEveryAdvertisedJT808Command pins that the command list the
// REST layer advertises is exactly the list the encoder can build. A command
// that /api/commands/types offers but the encoder refuses is a dead entry in
// the web interface.
func TestRouterEncodesEveryAdvertisedJT808Command(t *testing.T) {
	directory := gateway.NewMemoryDirectory()
	if _, err := directory.Register("013800138000", "jt808"); err != nil {
		t.Fatalf("register: %v", err)
	}
	router := gateway.NewRouter(gateway.DefaultRegistry(), directory, gateway.WithConfig(func(_, key string) string {
		switch key {
		case "web.url":
			return "http://video.example.com:8082"
		case "jt1078.port":
			return "1078"
		}
		return ""
	}))

	// The parameters each command needs to be buildable at all.
	attributes := map[string]map[string]any{
		model.CommandPositionPeriodic: {"frequency": 60},
		model.CommandSetConnection:    {"server": "example.com", "port": 5027},
		model.CommandMessage:          {"message": "text"},
		model.CommandVoiceMessage:     {"message": "text"},
		model.CommandSetSpeedLimit:    {"data": 80},
		model.CommandCustom:           {"data": "7e0200000000"},
	}

	advertised, ok := router.SupportedCommands("jt808", false)
	if !ok {
		t.Fatal("jt808 is not registered")
	}
	if len(advertised) != 21 {
		t.Fatalf("advertised %d commands, want 21: %v", len(advertised), advertised)
	}
	for _, commandType := range advertised {
		command := model.NewCommand(1, commandType)
		for key, value := range attributes[commandType] {
			command.SetAttribute(key, value)
		}
		if _, err := router.EncodeCommand("jt808", command); err != nil {
			t.Errorf("%s: %v", commandType, err)
		}
	}
}
