package forward

import (
	"bufio"
	"context"
	"encoding/json"
	"net"
	"testing"
	"time"

	"traccar-server_go/go_server/internal/model"
)

func TestNetworkForwarderWritesOneJSONLine(t *testing.T) {
	server, client := net.Pipe()
	defer server.Close()

	transport, err := NewNetwork(NetworkConfig{
		Address:  "collector.test:9000",
		Protocol: "tcp",
		Dial: func(context.Context, string, string) (net.Conn, error) {
			return client, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer transport.Close()

	position := model.NewPosition("gt06")
	position.DeviceID = 7
	position.Latitude = 1.5
	position.Longitude = 2.5
	position.Time = time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)

	lines := make(chan string, 1)
	go func() {
		line, _ := bufio.NewReader(server).ReadString('\n')
		lines <- line
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := transport.Send(ctx, Message{Position: position}); err != nil {
		t.Fatal(err)
	}

	line := <-lines
	var document map[string]any
	if err := json.Unmarshal([]byte(line), &document); err != nil {
		t.Fatalf("payload %q: %v", line, err)
	}
	payload, ok := document["position"].(map[string]any)
	if !ok {
		t.Fatalf("payload = %v", document)
	}
	if payload["deviceId"].(float64) != 7 {
		t.Fatalf("deviceId = %v", payload["deviceId"])
	}
}

func TestNetworkForwarderRejectsUnknownProtocol(t *testing.T) {
	if _, err := NewNetwork(NetworkConfig{Address: "x:1", Protocol: "sctp"}); err == nil {
		t.Fatal("expected an error for an unknown protocol")
	}
}
