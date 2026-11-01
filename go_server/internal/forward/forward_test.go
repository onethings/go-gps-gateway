package forward

import (
	"bufio"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"traccar-server_go/go_server/internal/helper"
	"traccar-server_go/go_server/internal/model"
)

func testDevice() *model.Device {
	return &model.Device{ID: 7, UniqueID: "864717003283581", Name: "Van 7", Status: "online"}
}

func testPosition() *model.Position {
	position := model.NewPosition("gt06")
	position.ID = 42
	position.DeviceID = 7
	position.Time = time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	position.Valid = true
	position.Latitude = 54.94535
	position.Longitude = 24.01762
	position.Speed = 27
	position.Course = 180
	position.Altitude = 100
	position.Attributes["sat"] = 9
	return position
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func okResponse() *http.Response {
	return &http.Response{
		StatusCode: http.StatusOK,
		Status:     "200 OK",
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader("")),
	}
}

// recordingTransport is a Transport for the queue and retry tests.
type recordingTransport struct {
	mu       sync.Mutex
	sends    int
	failures int
	block    chan struct{}
	started  chan struct{}
}

func (t *recordingTransport) Name() string { return "recording" }

func (t *recordingTransport) Send(ctx context.Context, _ Message) error {
	if t.started != nil {
		select {
		case t.started <- struct{}{}:
		default:
		}
	}
	if t.block != nil {
		select {
		case <-t.block:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.sends++
	if t.failures > 0 {
		t.failures--
		return io.ErrUnexpectedEOF
	}
	return nil
}

func (t *recordingTransport) Close() error { return nil }

func closeService(t *testing.T, service *Service) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := service.Close(ctx); err != nil {
		t.Fatalf("close: %v", err)
	}
}

func TestServiceRetriesAndForwards(t *testing.T) {
	transport := &recordingTransport{failures: 2}
	service := New(Config{MaxAttempts: 5, MaxBackoff: 10 * time.Millisecond}, []Transport{transport})

	service.ForwardPosition(testPosition(), testDevice())
	// Two failures and then the successful send.
	deadline := time.Now().Add(2 * time.Second)
	for {
		transport.mu.Lock()
		sends := transport.sends
		transport.mu.Unlock()
		if sends >= 3 || time.Now().After(deadline) {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	closeService(t, service)

	transport.mu.Lock()
	defer transport.mu.Unlock()
	if transport.sends != 3 {
		t.Fatalf("sends = %d, want two retries and one success", transport.sends)
	}
	if service.Failed() != 0 {
		t.Errorf("failed = %d", service.Failed())
	}
}

func TestServiceQueueFullDrops(t *testing.T) {
	unblock := make(chan struct{})
	transport := &recordingTransport{block: unblock, started: make(chan struct{}, 1)}
	service := New(Config{QueueSize: 1, MaxAttempts: 1}, []Transport{transport})

	// The first message keeps the worker busy; the queue holds one more and the
	// third is dropped.
	service.ForwardPosition(testPosition(), testDevice())
	select {
	case <-transport.started:
	case <-time.After(2 * time.Second):
		t.Fatal("worker never picked up the first message")
	}
	service.ForwardPosition(testPosition(), testDevice())
	service.ForwardPosition(testPosition(), testDevice())
	if got := service.Dropped(); got != 1 {
		t.Fatalf("dropped = %d, want 1", got)
	}
	close(unblock)
	closeService(t, service)
}

func TestJSONTransportPostsPayload(t *testing.T) {
	var (
		method      string
		contentType string
		body        map[string]any
	)
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		method = request.Method
		contentType = request.Header.Get("Content-Type")
		_ = json.NewDecoder(request.Body).Decode(&body)
		return okResponse(), nil
	})}
	transport, err := NewJSON(HTTPConfig{URL: "http://example.test/hook", Client: client})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	if err := transport.Send(context.Background(), Message{Position: testPosition(), Device: testDevice()}); err != nil {
		t.Fatalf("send: %v", err)
	}
	if method != http.MethodPost || contentType != "application/json" {
		t.Errorf("method = %s, content type = %s", method, contentType)
	}
	position, ok := body["position"].(map[string]any)
	if !ok || position["deviceId"] != float64(7) {
		t.Errorf("body = %v", body)
	}
	device, _ := body["device"].(map[string]any)
	if device["uniqueId"] != "864717003283581" {
		t.Errorf("device = %v", device)
	}
}

func TestURLTransportSubstitutes(t *testing.T) {
	var target string
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		target = request.URL.String()
		return okResponse(), nil
	})}
	transport, err := NewURL(HTTPConfig{
		URL:    "http://example.test/track?id={uniqueId}&lat={latitude}&name={name}&status={statusCode}",
		Header: "X-Token: abc",
		Client: client,
	})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	if err := transport.Send(context.Background(), Message{Position: testPosition(), Device: testDevice()}); err != nil {
		t.Fatalf("send: %v", err)
	}
	for _, part := range []string{"id=864717003283581", "lat=54.94535", "name=Van+7", "status=0xF11C"} {
		if !strings.Contains(target, part) {
			t.Errorf("target %q misses %q", target, part)
		}
	}
}

// pipeDial returns a dial function that hands out the connected pair.
func pipeDial(conn net.Conn) func(context.Context, string, string) (net.Conn, error) {
	return func(context.Context, string, string) (net.Conn, error) { return conn, nil }
}

func TestRedisTransportPush(t *testing.T) {
	client, server := net.Pipe()
	done := make(chan struct{})
	go func() {
		defer close(done)
		reader := bufio.NewReader(server)
		// *3, $5, LPUSH, $20, key, $len, json: seven lines for one LPUSH.
		var payload string
		for index := 0; index < 7; index++ {
			line, err := reader.ReadString('\n')
			if err != nil {
				return
			}
			payload = line
		}
		if !strings.Contains(payload, `"deviceId":7`) {
			t.Errorf("payload = %q", payload)
		}
		_, _ = server.Write([]byte(":1\r\n"))
	}()

	transport, err := NewRedis(RedisConfig{
		URL:  "redis://localhost:6379",
		Dial: pipeDial(client),
	})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	if err := transport.Send(context.Background(), Message{Position: testPosition(), Device: testDevice()}); err != nil {
		t.Fatalf("send: %v", err)
	}
	_ = transport.Close()
	<-done
}

func TestMQTTTransportPublish(t *testing.T) {
	client, server := net.Pipe()
	done := make(chan struct{})
	var (
		mu    sync.Mutex
		topic string
		body  []byte
	)
	go func() {
		defer close(done)
		reader := bufio.NewReader(server)
		if _, err := reader.ReadByte(); err != nil { // CONNECT header
			return
		}
		length, err := readRemainingLength(reader)
		if err != nil {
			return
		}
		connect := make([]byte, length)
		if _, err := io.ReadFull(reader, connect); err != nil {
			return
		}
		if !strings.Contains(string(connect), "MQTT") {
			t.Errorf("connect = %q", connect)
		}
		_, _ = server.Write([]byte{0x20, 0x02, 0x00, 0x00})

		header, err := reader.ReadByte()
		if err != nil || header != 0x30 {
			return
		}
		length, err = readRemainingLength(reader)
		if err != nil {
			return
		}
		packet := make([]byte, length)
		if _, err := io.ReadFull(reader, packet); err != nil {
			return
		}
		topicLength := int(binary.BigEndian.Uint16(packet[:2]))
		mu.Lock()
		topic = string(packet[2 : 2+topicLength])
		body = packet[2+topicLength:]
		mu.Unlock()

		// Drain the DISCONNECT so Close does not block on the pipe.
		if _, err := io.ReadFull(reader, make([]byte, 2)); err != nil {
			return
		}
	}()

	transport, err := NewMQTT(MQTTConfig{
		Broker:   "broker:1883",
		Topic:    "traccar",
		ClientID: "test",
		Dial:     pipeDial(client),
	})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	if err := transport.Send(context.Background(), Message{Position: testPosition(), Device: testDevice()}); err != nil {
		t.Fatalf("send: %v", err)
	}
	_ = transport.Close()
	<-done

	mu.Lock()
	defer mu.Unlock()
	if topic != "traccar" {
		t.Errorf("topic = %q", topic)
	}
	if !strings.Contains(string(body), `"deviceId":7`) {
		t.Errorf("body = %q", body)
	}
}

// readRemainingLength reads the MQTT varint length.
func readRemainingLength(reader *bufio.Reader) (int, error) {
	multiplier := 1
	value := 0
	for {
		digit, err := reader.ReadByte()
		if err != nil {
			return 0, err
		}
		value += int(digit&0x7f) * multiplier
		if digit&0x80 == 0 {
			return value, nil
		}
		multiplier *= 128
	}
}

func TestWialonTransportDatagram(t *testing.T) {
	client, server := net.Pipe()
	received := make(chan string, 1)
	go func() {
		reader := bufio.NewReader(server)
		line, _ := reader.ReadString('\n')
		received <- line
	}()

	transport, err := NewWialon(WialonConfig{Address: "wialon:20332", Dial: pipeDial(client)})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	if err := transport.Send(context.Background(), Message{Position: testPosition(), Device: testDevice()}); err != nil {
		t.Fatalf("send: %v", err)
	}
	line := <-received
	if !strings.HasPrefix(line, "864717003283581#D#") {
		t.Fatalf("datagram = %q", line)
	}
	if !strings.Contains(line, "N") || !strings.Contains(line, "E") {
		t.Errorf("datagram = %q", line)
	}
	if !strings.Contains(line, "5456.72100;N") {
		t.Errorf("latitude minutes = %q", line)
	}
}

func isHex(value string) bool {
	for _, r := range value {
		if !strings.ContainsRune("0123456789abcdef", r) {
			return false
		}
	}
	return value != ""
}

func TestWialonTransportV2Checksum(t *testing.T) {
	client, server := net.Pipe()
	received := make(chan string, 1)
	go func() {
		reader := bufio.NewReader(server)
		line, _ := reader.ReadString('\n')
		received <- line
	}()

	transport, err := NewWialon(WialonConfig{Address: "wialon:20332", Version: "2", Dial: pipeDial(client)})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	if err := transport.Send(context.Background(), Message{Position: testPosition(), Device: testDevice()}); err != nil {
		t.Fatalf("send: %v", err)
	}
	line := <-received
	if !strings.HasPrefix(line, "864717003283581#D#") {
		t.Fatalf("datagram = %q", line)
	}
	if !strings.HasSuffix(line, "\r\n") {
		t.Fatalf("datagram = %q", line)
	}
	trimmed := strings.TrimSuffix(line, "\r\n")
	index := strings.LastIndexByte(trimmed, ';')
	if index < 0 {
		t.Fatalf("datagram = %q", line)
	}
	// The checksum covers the data that follows the #D# header.
	dataStart := strings.Index(trimmed, "#D#")
	if dataStart < 0 {
		t.Fatalf("datagram = %q", line)
	}
	body, checksum := trimmed[dataStart+len("#D#"):index], trimmed[index+1:]
	if !isHex(checksum) || len(checksum) != 4 {
		t.Fatalf("checksum = %q", line)
	}
	if want := fmt.Sprintf("%04x", helper.CRC16IBM([]byte(body))); want != checksum {
		t.Errorf("checksum = %s, want %s", checksum, want)
	}
}

func TestOnlyKeepsOneFeed(t *testing.T) {
	positions := &recordingTransport{}
	only := Only(positions, false)
	if err := only.Send(context.Background(), Message{Event: &model.Event{ID: 1}}); err != nil {
		t.Fatalf("send event: %v", err)
	}
	if positions.sends != 0 {
		t.Fatalf("sends = %d, want the event dropped", positions.sends)
	}
	if err := only.Send(context.Background(), Message{Position: testPosition()}); err != nil {
		t.Fatalf("send position: %v", err)
	}
	if positions.sends != 1 {
		t.Fatalf("sends = %d, want the position delivered", positions.sends)
	}

	events := &recordingTransport{}
	only = Only(events, true)
	if err := only.Send(context.Background(), Message{Position: testPosition()}); err != nil {
		t.Fatalf("send position: %v", err)
	}
	if events.sends != 0 {
		t.Fatalf("sends = %d, want the position dropped", events.sends)
	}
	if err := only.Send(context.Background(), Message{Event: &model.Event{ID: 1}}); err != nil {
		t.Fatalf("send event: %v", err)
	}
	if events.sends != 1 {
		t.Fatalf("sends = %d, want the event delivered", events.sends)
	}
}
