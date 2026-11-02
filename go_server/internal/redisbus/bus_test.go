package redisbus

import (
	"bufio"
	"context"
	"net"
	"testing"
	"time"
)

func TestPublishWritesTheCommand(t *testing.T) {
	server, client := net.Pipe()
	defer server.Close()

	bus, err := New(Config{
		URL:     "redis://localhost:6379",
		Channel: "traccar",
		Dial: func(context.Context, string, string) (net.Conn, error) {
			return client, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	received := make(chan []any, 1)
	go func() {
		reply, err := readReply(bufio.NewReader(server))
		if err != nil {
			t.Errorf("read command: %v", err)
			return
		}
		values, _ := reply.([]any)
		received <- values
		_, _ = server.Write([]byte(":1\r\n"))
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := bus.Publish(ctx, []byte("hello")); err != nil {
		t.Fatal(err)
	}

	values := <-received
	if len(values) != 3 || values[0] != "PUBLISH" || values[1] != "traccar" || values[2] != "hello" {
		t.Fatalf("command = %v", values)
	}
}

func TestSubscribeDeliversMessages(t *testing.T) {
	server, client := net.Pipe()
	bus, err := New(Config{
		URL:     "redis://localhost:6379",
		Channel: "traccar",
		Dial: func(context.Context, string, string) (net.Conn, error) {
			return client, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	messages := make(chan string, 1)
	go func() {
		_ = bus.Subscribe(ctx, func(payload []byte) {
			messages <- string(payload)
		})
	}()

	go func() {
		reader := bufio.NewReader(server)
		if _, err := readReply(reader); err != nil {
			t.Errorf("read subscribe: %v", err)
			return
		}
		_, _ = server.Write([]byte("*3\r\n$9\r\nsubscribe\r\n$7\r\ntraccar\r\n:1\r\n"))
		_, _ = server.Write([]byte("*3\r\n$7\r\nmessage\r\n$7\r\ntraccar\r\n$5\r\nhello\r\n"))
	}()

	select {
	case message := <-messages:
		if message != "hello" {
			t.Fatalf("message = %q", message)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for the message")
	}
	cancel()
	_ = server.Close()
}

func TestNewRejectsInvalidConfiguration(t *testing.T) {
	if _, err := New(Config{}); err == nil {
		t.Fatal("an empty URL must be rejected")
	}
	if _, err := New(Config{URL: "http://localhost"}); err == nil {
		t.Fatal("a non redis scheme must be rejected")
	}
}
