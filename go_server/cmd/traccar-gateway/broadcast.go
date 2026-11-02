package main

import (
	"context"
	"encoding/json"

	"traccar-server_go/go_server/internal/redisbus"
)

// socketEnvelope wraps one socket payload for the cross node channel. The
// channel name and the device id stay outside the payload so a receiving node
// can filter it for its own clients without decoding the payload itself.
type socketEnvelope struct {
	Channel  string          `json:"channel"`
	DeviceID int64           `json:"deviceId"`
	Payload  json.RawMessage `json:"payload"`
}

// redisBridge adapts the Redis bus to api.SocketRemote. Publishing happens on
// its own goroutine through a bounded queue: the ingest path must never wait
// for Redis, and a full queue loses a live update instead of a position.
type redisBridge struct {
	bus   *redisbus.Bus
	queue chan []byte
}

// newRedisBridge starts the publisher. ctx stops it during shutdown.
func newRedisBridge(ctx context.Context, bus *redisbus.Bus, depth int) *redisBridge {
	if depth <= 0 {
		depth = 256
	}
	bridge := &redisBridge{bus: bus, queue: make(chan []byte, depth)}
	go bridge.run(ctx)
	return bridge
}

// Publish implements api.SocketRemote.
func (b *redisBridge) Publish(channel string, deviceID int64, payload []byte) {
	if b == nil || b.bus == nil {
		return
	}
	encoded, err := json.Marshal(socketEnvelope{
		Channel:  channel,
		DeviceID: deviceID,
		Payload:  payload,
	})
	if err != nil {
		return
	}
	select {
	case b.queue <- encoded:
	default:
		// A node that cannot keep up with the live feed loses an update, not
		// the position it describes.
	}
}

func (b *redisBridge) run(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case payload := <-b.queue:
			_ = b.bus.Publish(ctx, payload)
		}
	}
}

// decodeSocketEnvelope parses a message another node published.
func decodeSocketEnvelope(payload []byte) (socketEnvelope, bool) {
	var envelope socketEnvelope
	if err := json.Unmarshal(payload, &envelope); err != nil {
		return envelope, false
	}
	if envelope.Channel == "" || len(envelope.Payload) == 0 {
		return envelope, false
	}
	return envelope, true
}
