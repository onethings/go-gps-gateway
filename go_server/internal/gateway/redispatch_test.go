package gateway

import (
	"bufio"
	"context"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"

	"traccar-server_go/go_server/internal/model"
)

// dispatchPipeDial hands out a fixed client-side connection.
func dispatchPipeDial(conn net.Conn) func(context.Context, string, string) (net.Conn, error) {
	return func(context.Context, string, string) (net.Conn, error) { return conn, nil }
}

// serveRESP runs the fake server side. It reads commands one at a time (a
// RESP array header plus its bulk-string elements), replying after each with
// the next reply line. replies must have one entry per command. It closes done
// when finished.
func serveRESP(t *testing.T, server net.Conn, replies ...string) <-chan struct{} {
	t.Helper()
	done := make(chan struct{})
	go func() {
		defer close(done)
		reader := bufio.NewReader(server)
		for _, reply := range replies {
			line, err := reader.ReadString('\n')
			if err != nil {
				return
			}
			if !strings.HasPrefix(line, "*") {
				return
			}
			count, err := strconv.Atoi(strings.TrimSpace(line[1:]))
			if err != nil {
				return
			}
			for i := 0; i < count; i++ {
				length := 0
				if e, err := reader.ReadString('\n'); err != nil || !strings.HasPrefix(e, "$") {
					return
				} else if length, err = strconv.Atoi(strings.TrimSpace(e[1:])); err != nil {
					return
				}
				if length > 0 {
					if _, err := ioReadFull(reader, make([]byte, length)); err != nil {
						return
					}
					if _, err := reader.ReadString('\n'); err != nil {
						return
					}
				}
			}
			if _, err := server.Write([]byte(reply)); err != nil {
				return
			}
		}
		_ = server.Close()
	}()
	return done
}

func TestRedisDispatchStoreEnqueue(t *testing.T) {
	client, server := net.Pipe()
	// HSET, EXPIRE, SADD: three commands, three replies.
	done := serveRESP(t, server, "+OK\r\n", ":1\r\n", ":1\r\n")

	store, err := NewRedisDispatchStore(RedisDispatchConfig{URL: "redis://localhost:6379", Dial: dispatchPipeDial(client)})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	store.Enqueue(7, 99, dispatchEntry{
		protocol: "t55",
		command:  modelCommand(99, 7, 5),
		attempts: 2,
		next:     time.Unix(1_700_000_000, 0),
		expires:  time.Unix(1_700_000_100, 0),
	})
	_ = store.Close()
	<-done
}

func TestRedisDispatchStoreRemove(t *testing.T) {
	client, server := net.Pipe()
	// DEL, SREM: two commands, two replies.
	done := serveRESP(t, server, ":1\r\n", ":1\r\n")

	store, err := NewRedisDispatchStore(RedisDispatchConfig{URL: "redis://localhost:6379", Dial: dispatchPipeDial(client)})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	store.Remove(7, 99)
	_ = store.Close()
	<-done
}

func TestRedisDispatchStoreLoad(t *testing.T) {
	client, server := net.Pipe()
	body := `{"protocol":"t55","attempts":2,"next":1700000000,"expires":1700000100}`
	// SMEMBERS reply: *1, one member; HGET reply: one bulk string.
	done := serveRESP(t, server,
		"*1\r\n$2\r\n99\r\n",
		"$"+strconv.Itoa(len(body))+"\r\n"+body+"\r\n",
	)

	store, err := NewRedisDispatchStore(RedisDispatchConfig{URL: "redis://localhost:6379", Dial: dispatchPipeDial(client)})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	entries := store.Load(7)
	_ = store.Close()
	<-done

	if len(entries) != 1 {
		t.Fatalf("load: got %d entries, want 1", len(entries))
	}
	e := entries[0]
	if e.protocol != "t55" || e.attempts != 2 {
		t.Errorf("load: entry = %+v", e)
	}
}

// modelCommand builds the command row that entryToRedis reads.
func modelCommand(id, deviceID, serial int64) model.Command {
	return model.Command{ID: id, DeviceID: deviceID, Serial: int(serial)}
}