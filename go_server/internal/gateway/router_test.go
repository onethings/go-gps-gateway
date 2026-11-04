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

	_, err := router.EncodeCommand("gt06", model.NewCommand(1, model.CommandAlarmArm))
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

// TestRouterEncodesEveryAdvertisedGT06Command is the same pin for GT06: every
// type the REST layer advertises must be buildable by the encoder.
func TestRouterEncodesEveryAdvertisedGT06Command(t *testing.T) {
	directory := gateway.NewMemoryDirectory()
	if _, err := directory.Register("864717003283581", "gt06"); err != nil {
		t.Fatalf("register: %v", err)
	}
	router := gateway.NewRouter(gateway.DefaultRegistry(), directory, gateway.WithConfig(func(_, key string) string {
		switch key {
		case "alternative":
			return "true"
		case "language":
			return "true"
		case "devicePassword":
			return "secret"
		}
		return ""
	}))

	// The parameters each command needs to be buildable at all.
	attributes := map[string]map[string]any{
		model.CommandPositionPeriodic: {"frequency": 60},
		model.CommandSOSNumber:        {"index": 0, "phone": "10086"},
		model.CommandSetPhonebook:     {"index": 1, "phone": "10086"},
		model.CommandVoiceMonitoring:  {"phone": "10086"},
		model.CommandSetConnection:    {"server": "gps.example.com", "port": 5023},
		model.CommandCustom:           {"data": "WHERE#"},
	}

	advertised, ok := router.SupportedCommands("gt06", true)
	if !ok {
		t.Fatal("gt06 is not registered")
	}
	if len(advertised) != 16 {
		t.Fatalf("advertised %d commands, want 16: %v", len(advertised), advertised)
	}
	for _, commandType := range advertised {
		command := model.NewCommand(1, commandType)
		for key, value := range attributes[commandType] {
			command.SetAttribute(key, value)
		}
		if _, err := router.EncodeCommand("gt06", command); err != nil {
			t.Errorf("%s: %v", commandType, err)
		}
	}
}

// TestRouterEncodesEveryAdvertisedH02Command is the same pin for H02: every
// type the REST layer advertises must be buildable by the encoder.
func TestRouterEncodesEveryAdvertisedH02Command(t *testing.T) {
	directory := gateway.NewMemoryDirectory()
	if _, err := directory.Register("123456789012345", "h02"); err != nil {
		t.Fatalf("register: %v", err)
	}
	router := gateway.NewRouter(gateway.DefaultRegistry(), directory, gateway.WithConfig(func(_, key string) string {
		if key == "alternative" {
			return "true"
		}
		return ""
	}))

	// The parameters each command needs to be buildable at all.
	attributes := map[string]map[string]any{
		model.CommandPositionPeriodic: {"frequency": 60},
		model.CommandCustom:           {"data": "*HQ,123456789012345,S18,120000,60#"},
	}

	advertised, ok := router.SupportedCommands("h02", true)
	if !ok {
		t.Fatal("h02 is not registered")
	}
	if len(advertised) != 6 {
		t.Fatalf("advertised %d commands, want 6: %v", len(advertised), advertised)
	}
	for _, commandType := range advertised {
		command := model.NewCommand(1, commandType)
		for key, value := range attributes[commandType] {
			command.SetAttribute(key, value)
		}
		if _, err := router.EncodeCommand("h02", command); err != nil {
			t.Errorf("%s: %v", commandType, err)
		}
	}
}

// TestRouterEncodesEveryAdvertisedTeltonikaCommand is the same pin for
// Teltonika: every advertised type must be buildable by the encoder.
func TestRouterEncodesEveryAdvertisedTeltonikaCommand(t *testing.T) {
	directory := gateway.NewMemoryDirectory()
	if _, err := directory.Register("123456789012345", "teltonika"); err != nil {
		t.Fatalf("register: %v", err)
	}
	router := gateway.NewRouter(gateway.DefaultRegistry(), directory)

	attributes := map[string]map[string]any{
		model.CommandCustom: {model.CmdKeyData: "setdigout 1"},
	}

	advertised, ok := router.SupportedCommands("teltonika", false)
	if !ok {
		t.Fatal("teltonika is not registered")
	}
	if len(advertised) != 3 {
		t.Fatalf("advertised %d commands, want 3: %v", len(advertised), advertised)
	}
	for _, commandType := range advertised {
		command := model.NewCommand(1, commandType)
		for key, value := range attributes[commandType] {
			command.SetAttribute(key, value)
		}
		if _, err := router.EncodeCommand("teltonika", command); err != nil {
			t.Errorf("%s: %v", commandType, err)
		}
	}
}

// TestRouterEncodesEveryAdvertisedGps103Command is the same pin for GPS103:
// every type the REST layer advertises must be buildable by the encoder.
func TestRouterEncodesEveryAdvertisedGps103Command(t *testing.T) {
	directory := gateway.NewMemoryDirectory()
	if _, err := directory.Register("123456789012345", "gps103"); err != nil {
		t.Fatalf("register: %v", err)
	}
	router := gateway.NewRouter(gateway.DefaultRegistry(), directory)

	// The parameters each command needs to be buildable at all.
	attributes := map[string]map[string]any{
		model.CommandPositionPeriodic: {model.CmdKeyFrequency: 300},
		model.CommandCustom:           {model.CmdKeyData: "H,080"},
	}

	advertised, ok := router.SupportedCommands("gps103", true)
	if !ok {
		t.Fatal("gps103 is not registered")
	}
	if len(advertised) != 9 {
		t.Fatalf("advertised %d commands, want 9: %v", len(advertised), advertised)
	}
	for _, commandType := range advertised {
		command := model.NewCommand(1, commandType)
		for key, value := range attributes[commandType] {
			command.SetAttribute(key, value)
		}
		if _, err := router.EncodeCommand("gps103", command); err != nil {
			t.Errorf("%s: %v", commandType, err)
		}
	}
}

// TestRouterEncodesEveryAdvertisedTk103Command is the same pin for TK103.
// The encoder has two layouts selected by protocol.tk103.alternative, so each
// advertised command is built by the layout that can express it.
func TestRouterEncodesEveryAdvertisedTk103Command(t *testing.T) {
	directory := gateway.NewMemoryDirectory()
	if _, err := directory.Register("123456789012345", "tk103"); err != nil {
		t.Fatalf("register: %v", err)
	}
	legacy := gateway.NewRouter(gateway.DefaultRegistry(), directory)
	alternative := gateway.NewRouter(gateway.DefaultRegistry(), directory, gateway.WithConfig(func(_, key string) string {
		if key == "alternative" {
			return "true"
		}
		return ""
	}))

	// The parameters each command needs to be buildable at all.
	attributes := map[string]map[string]any{
		model.CommandCustom:           {model.CmdKeyData: "AA00"},
		model.CommandPositionPeriodic: {model.CmdKeyFrequency: 60},
		model.CommandSetConnection:    {model.CmdKeyServer: "1.2.3.4", model.CmdKeyPort: "5555"},
		model.CommandSOSNumber:        {model.CmdKeyPhone: "+55555555555"},
		model.CommandOutputControl:    {model.CmdKeyData: "1"},
		model.CommandModeDeepSleep:    {model.CmdKeyEnable: true},
		model.CommandModePowerSaving:  {model.CmdKeyEnable: true},
		model.CommandAlarmSOS:         {model.CmdKeyEnable: true},
	}
	// These four exist only in the legacy parenthesised layout.
	legacyOnly := map[string]bool{
		model.CommandSetOdometer:   true,
		model.CommandEngineStop:    true,
		model.CommandEngineResume:  true,
		model.CommandOutputControl: true,
	}

	advertised, ok := alternative.SupportedCommands("tk103", true)
	if !ok {
		t.Fatal("tk103 is not registered")
	}
	if len(advertised) != 18 {
		t.Fatalf("advertised %d commands, want 18: %v", len(advertised), advertised)
	}
	for _, commandType := range advertised {
		command := model.NewCommand(1, commandType)
		for key, value := range attributes[commandType] {
			command.SetAttribute(key, value)
		}
		router := alternative
		if legacyOnly[commandType] {
			router = legacy
		}
		if _, err := router.EncodeCommand("tk103", command); err != nil {
			t.Errorf("%s: %v", commandType, err)
		}
	}
}

// TestRouterEncodesEveryAdvertisedMeitrackCommand is the same pin for
// Meitrack: every advertised type must be buildable by the encoder.
func TestRouterEncodesEveryAdvertisedMeitrackCommand(t *testing.T) {
	directory := gateway.NewMemoryDirectory()
	if _, err := directory.Register("123456789012345", "meitrack"); err != nil {
		t.Fatalf("register: %v", err)
	}
	router := gateway.NewRouter(gateway.DefaultRegistry(), directory, gateway.WithConfig(func(_, key string) string {
		if key == "alternative" {
			return "true"
		}
		return ""
	}))

	// The parameters each command needs to be buildable at all.
	attributes := map[string]map[string]any{
		model.CommandCustom:       {model.CmdKeyData: "H,080"},
		model.CommandRequestPhoto: {model.CmdKeyIndex: 1},
		model.CommandSendSMS: {
			model.CmdKeyPhone:   "15360853789",
			model.CmdKeyMessage: "Meitrack",
		},
	}

	advertised, ok := router.SupportedCommands("meitrack", true)
	if !ok {
		t.Fatal("meitrack is not registered")
	}
	if len(advertised) != 8 {
		t.Fatalf("advertised %d commands, want 8: %v", len(advertised), advertised)
	}
	for _, commandType := range advertised {
		command := model.NewCommand(1, commandType)
		for key, value := range attributes[commandType] {
			command.SetAttribute(key, value)
		}
		if _, err := router.EncodeCommand("meitrack", command); err != nil {
			t.Errorf("%s: %v", commandType, err)
		}
	}
}

// TestRouterEncodesEveryAdvertisedWondexCommand is the same pin for Wondex:
// every advertised type must be buildable by the encoder.
func TestRouterEncodesEveryAdvertisedWondexCommand(t *testing.T) {
	directory := gateway.NewMemoryDirectory()
	if _, err := directory.Register("123456789012345", "wondex"); err != nil {
		t.Fatalf("register: %v", err)
	}
	router := gateway.NewRouter(gateway.DefaultRegistry(), directory)

	advertised, ok := router.SupportedCommands("wondex", true)
	if !ok {
		t.Fatal("wondex is not registered")
	}
	if len(advertised) != 6 {
		t.Fatalf("advertised %d commands, want 6: %v", len(advertised), advertised)
	}
	for _, commandType := range advertised {
		if _, err := router.EncodeCommand("wondex", model.NewCommand(1, commandType)); err != nil {
			t.Errorf("%s: %v", commandType, err)
		}
	}
}

// TestRouterGT06EngineStopLayouts covers the two engine layouts the
// configuration and the device model select.
func TestRouterGT06EngineStopLayouts(t *testing.T) {
	directory := gateway.NewMemoryDirectory()
	if _, err := directory.Register("864717003283581", "gt06"); err != nil {
		t.Fatalf("register: %v", err)
	}
	router := gateway.NewRouter(gateway.DefaultRegistry(), directory, gateway.WithConfig(func(_, key string) string {
		switch key {
		case "alternative":
			return "true"
		case "devicePassword":
			return "secret"
		}
		return ""
	}))

	encode := func() string {
		t.Helper()
		payload, err := router.EncodeCommand("gt06", model.NewCommand(1, model.CommandEngineStop))
		if err != nil {
			t.Fatalf("encode: %v", err)
		}
		if len(payload) < 15 {
			t.Fatalf("payload = %x", payload)
		}
		return string(payload[9 : 5+int(payload[4])])
	}

	if got := encode(); got != "DYD,secret#" {
		t.Fatalf("content = %q, want %q", got, "DYD,secret#")
	}

	if err := directory.SetModel("864717003283581", "G109"); err != nil {
		t.Fatalf("set model: %v", err)
	}
	if got := encode(); got != "DYD#" {
		t.Fatalf("content = %q, want %q", got, "DYD#")
	}
}
