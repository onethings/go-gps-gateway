package forward

import (
	"bufio"
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"net/url"
	"strings"
	"testing"
	"time"
)

func writeAMQPMethod(writer io.Writer, channel uint16, method amqpMethod, args []byte) error {
	_, err := writer.Write(amqpFrame(amqpFrameMethod, channel, amqpMethodPayload(method, args)))
	return err
}

func readAMQPFrame(reader *bufio.Reader) (byte, []byte, error) {
	var header [7]byte
	if _, err := io.ReadFull(reader, header[:]); err != nil {
		return 0, nil, err
	}
	payload := make([]byte, binary.BigEndian.Uint32(header[3:7]))
	if _, err := io.ReadFull(reader, payload); err != nil {
		return 0, nil, err
	}
	var end [1]byte
	if _, err := io.ReadFull(reader, end[:]); err != nil {
		return 0, nil, err
	}
	if end[0] != amqpFrameEnd {
		return 0, nil, fmt.Errorf("frame end 0x%02x", end[0])
	}
	return header[0], payload, nil
}

func readAMQPMethod(reader *bufio.Reader, want amqpMethod) ([]byte, error) {
	kind, payload, err := readAMQPFrame(reader)
	if err != nil {
		return nil, err
	}
	if kind != amqpFrameMethod {
		return nil, fmt.Errorf("frame type %d, want a method", kind)
	}
	if got := amqpMethodOf(payload); got != want {
		return nil, fmt.Errorf("method %d.%d, want %d.%d", got.class, got.method, want.class, want.method)
	}
	return payload[4:], nil
}

func readAMQPShortstr(data []byte, at int) (string, int, error) {
	if at >= len(data) {
		return "", 0, io.ErrUnexpectedEOF
	}
	size := int(data[at])
	if at+1+size > len(data) {
		return "", 0, io.ErrUnexpectedEOF
	}
	return string(data[at+1 : at+1+size]), at + 1 + size, nil
}

// amqpServePublish is the broker side of one Send: handshake, exchange declare
// and a publish that it confirms or rejects.
func amqpServePublish(reader *bufio.Reader, writer io.Writer, nack bool) (string, error) {
	hello := make([]byte, 8)
	if _, err := io.ReadFull(reader, hello); err != nil {
		return "", err
	}
	if string(hello) != "AMQP\x00\x00\x09\x01" {
		return "", fmt.Errorf("hello = %q", hello)
	}

	var start amqpWriter
	start.octet(0)
	start.octet(9)
	start.long(0) // server properties
	start.longstr([]byte("PLAIN"))
	start.longstr([]byte("en_US"))
	if err := writeAMQPMethod(writer, 0, amqpConnectionStart, start.data); err != nil {
		return "", err
	}

	startOK, err := readAMQPMethod(reader, amqpConnectionStartOK)
	if err != nil {
		return "", err
	}
	if !strings.Contains(string(startOK), "PLAIN") || !strings.Contains(string(startOK), "\x00guest\x00secret") {
		return "", fmt.Errorf("start-ok = %q", startOK)
	}

	var tune amqpWriter
	tune.short(2047)
	tune.long(131072)
	tune.short(60)
	if err := writeAMQPMethod(writer, 0, amqpConnectionTune, tune.data); err != nil {
		return "", err
	}

	tuneOK, err := readAMQPMethod(reader, amqpConnectionTuneOK)
	if err != nil {
		return "", err
	}
	if len(tuneOK) < 8 {
		return "", fmt.Errorf("short tune-ok")
	}
	if heartbeat := binary.BigEndian.Uint16(tuneOK[6:8]); heartbeat != 0 {
		return "", fmt.Errorf("heartbeat = %d, want 0", heartbeat)
	}

	open, err := readAMQPMethod(reader, amqpConnectionOpen)
	if err != nil {
		return "", err
	}
	if vhost, _, err := readAMQPShortstr(open, 0); err != nil || vhost != "vhost" {
		return "", fmt.Errorf("vhost = %q: %v", vhost, err)
	}
	if err := writeAMQPMethod(writer, 0, amqpConnectionOpenOK, []byte{0}); err != nil {
		return "", err
	}

	if _, err := readAMQPMethod(reader, amqpChannelOpen); err != nil {
		return "", err
	}
	if err := writeAMQPMethod(writer, 1, amqpChannelOpenOK, []byte{0}); err != nil {
		return "", err
	}

	if _, err := readAMQPMethod(reader, amqpConfirmSelect); err != nil {
		return "", err
	}
	if err := writeAMQPMethod(writer, 1, amqpConfirmSelectOK, nil); err != nil {
		return "", err
	}

	declare, err := readAMQPMethod(reader, amqpExchangeDeclare)
	if err != nil {
		return "", err
	}
	exchange, at, err := readAMQPShortstr(declare, 2)
	if err != nil {
		return "", err
	}
	kind, _, err := readAMQPShortstr(declare, at)
	if err != nil {
		return "", err
	}
	if exchange != "traccar" || kind != "topic" {
		return "", fmt.Errorf("exchange %q of type %q", exchange, kind)
	}
	if err := writeAMQPMethod(writer, 1, amqpExchangeDeclareOK, nil); err != nil {
		return "", err
	}

	publish, err := readAMQPMethod(reader, amqpBasicPublish)
	if err != nil {
		return "", err
	}
	target, at, err := readAMQPShortstr(publish, 2)
	if err != nil {
		return "", err
	}
	routingKey, _, err := readAMQPShortstr(publish, at)
	if err != nil {
		return "", err
	}
	if target != "traccar" || routingKey != "positions" {
		return "", fmt.Errorf("publish to %q with key %q", target, routingKey)
	}

	frameKind, header, err := readAMQPFrame(reader)
	if err != nil {
		return "", err
	}
	if frameKind != amqpFrameHeader {
		return "", fmt.Errorf("frame type %d, want a content header", frameKind)
	}
	size := int(binary.BigEndian.Uint64(header[4:12]))
	body := make([]byte, 0, size)
	for len(body) < size {
		frameKind, chunk, err := readAMQPFrame(reader)
		if err != nil {
			return "", err
		}
		if frameKind != amqpFrameBody {
			return "", fmt.Errorf("frame type %d, want a content body", frameKind)
		}
		body = append(body, chunk...)
	}

	var confirm amqpWriter
	confirm.longlong(1) // delivery tag
	confirm.octet(0)    // not multiple
	method := amqpBasicAck
	if nack {
		method = amqpBasicNack
	}
	if err := writeAMQPMethod(writer, 1, method, confirm.data); err != nil {
		return "", err
	}
	return string(body), nil
}

func TestAMQPTransportPublishesWithConfirm(t *testing.T) {
	client, server := net.Pipe()
	defer server.Close()

	published := make(chan string, 1)
	go func() {
		body, err := amqpServePublish(bufio.NewReader(server), server, false)
		if err != nil {
			t.Errorf("server: %v", err)
		}
		published <- body
	}()

	transport, err := NewAMQP(AMQPConfig{
		URL:      "amqp://guest:secret@broker.test:5672/vhost",
		Exchange: "traccar",
		Topic:    "positions",
		Dial:     pipeDial(client),
	})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	defer transport.Close()

	if err := transport.Send(context.Background(), Message{Position: testPosition(), Device: testDevice()}); err != nil {
		t.Fatalf("send: %v", err)
	}

	select {
	case body := <-published:
		if !strings.Contains(body, `"deviceId":7`) {
			t.Fatalf("body = %q", body)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the broker never received the message")
	}
}

func TestAMQPTransportFailsOnNack(t *testing.T) {
	client, server := net.Pipe()
	defer server.Close()

	done := make(chan struct{})
	go func() {
		defer close(done)
		if _, err := amqpServePublish(bufio.NewReader(server), server, true); err != nil {
			t.Errorf("server: %v", err)
		}
	}()

	transport, err := NewAMQP(AMQPConfig{
		URL:      "amqp://guest:secret@broker.test:5672/vhost",
		Exchange: "traccar",
		Topic:    "positions",
		Dial:     pipeDial(client),
	})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	defer transport.Close()

	err = transport.Send(context.Background(), Message{Position: testPosition(), Device: testDevice()})
	if err == nil || !strings.Contains(err.Error(), "rejected") {
		t.Fatalf("send error = %v, want a rejection", err)
	}
	<-done
}

func TestAMQPConfigValidation(t *testing.T) {
	if _, err := NewAMQP(AMQPConfig{Exchange: "traccar", Topic: "positions"}); err == nil {
		t.Fatal("empty URL accepted")
	}
	if _, err := NewAMQP(AMQPConfig{URL: "amqp://broker", Topic: "positions"}); err == nil {
		t.Fatal("empty exchange accepted")
	}
	if _, err := NewAMQP(AMQPConfig{URL: "amqp://broker", Exchange: "traccar"}); err == nil {
		t.Fatal("empty topic accepted")
	}
}

func TestAMQPVhostDefaultsToRoot(t *testing.T) {
	if got := amqpVhost(mustParseURL(t, "amqp://guest:guest@broker:5672")); got != "/" {
		t.Fatalf("vhost = %q", got)
	}
	if got := amqpVhost(mustParseURL(t, "amqp://guest:guest@broker:5672/%2f")); got != "/" {
		t.Fatalf("vhost = %q", got)
	}
	if got := amqpVhost(mustParseURL(t, "amqp://guest:guest@broker:5672/tenant")); got != "tenant" {
		t.Fatalf("vhost = %q", got)
	}
}

func mustParseURL(t *testing.T, raw string) *url.URL {
	t.Helper()
	parsed, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("parse %q: %v", raw, err)
	}
	return parsed
}
