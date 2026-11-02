package gateway

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Command queue dispatch-state externalisation. The commands table in
// PostgreSQL is already the durable authority for which rows exist and whether
// they were written; what this store adds is a shared copy of the live retry
// bookkeeping (attempts, next, expires) so a node restart restores exact timing
// instead of resetting the clock, and so every node sees the same queue. The
// dispatcher keeps its in-memory queue as a fast local mirror; Redis is a
// write-through shadow of the fields the delivery policy depends on.

// dispatchKeyPrefix namespaces the dispatch-state keys. Per device we keep a
// set of queued command ids and one hash per command holding the live fields.
const dispatchKeyPrefix = "traccar:dispatch:"

// dispatchState is the live-queue shadow the Dispatcher consults when it is
// configured. A nil implementation is the disabled path: the dispatcher then
// runs purely in memory, exactly as before externalisation.
type dispatchState interface {
	// Enqueue records that command commandID is waiting for device deviceID,
	// carrying the live delivery fields. It must not block the delivery path
	// for long; implementations give up rather than stall a write.
	Enqueue(deviceID, commandID int64, entry dispatchEntry)
	// Remove forgets a command that left the queue (answered, failed, dropped).
	Remove(deviceID, commandID int64)
	// Load returns the live fields of every command still queued for a device,
	// so a reconnect can restore exact retry timing. Unknown commands are
	// omitted; the caller keeps the timing it already has for those.
	Load(deviceID int64) []dispatchEntry
}

// noDispatchState is the default: nothing is externalised.
type noDispatchState struct{}

func (noDispatchState) Enqueue(int64, int64, dispatchEntry) {}
func (noDispatchState) Remove(int64, int64)                 {}
func (noDispatchState) Load(int64) []dispatchEntry          { return nil }

// redisEntry is the JSON body of one live entry in Redis.
type redisEntry struct {
	Protocol  string  `json:"protocol"`
	Attempts  int     `json:"attempts"`
	NextUnix  float64 `json:"next"`
	ExpiresAt float64 `json:"expires"`
	Command   struct {
		ID       int64  `json:"id"`
		DeviceID int64  `json:"deviceId"`
		Serial   int    `json:"serial"`
	} `json:"command"`
}

// RedisDispatchConfig configures the dispatch-state store.
type RedisDispatchConfig struct {
	URL string
	// Dial defaults to net.Dialer.DialContext; tests pass net.Pipe.
	Dial func(ctx context.Context, network, address string) (net.Conn, error)
}

// RedisDispatchStore mirrors the live command queue to Redis. All operations
// are best-effort: when Redis is down or slow the store drops the write and
// logs, so a delivery is never held hostage by the mirror.
type RedisDispatchStore struct {
	url  string
	dial func(ctx context.Context, network, address string) (net.Conn, error)

	mu     sync.Mutex
	conn   net.Conn
	reader *bufio.Reader
	logf   func(format string, args ...any)
}

// NewRedisDispatchStore builds the store. url is redis://[:password@]host:port.
func NewRedisDispatchStore(config RedisDispatchConfig) (*RedisDispatchStore, error) {
	if config.URL == "" {
		return nil, fmt.Errorf("gateway: redis dispatch: empty URL")
	}
	dial := config.Dial
	if dial == nil {
		dialer := &net.Dialer{Timeout: 10 * time.Second}
		dial = dialer.DialContext
	}
	return &RedisDispatchStore{
		url:  config.URL,
		dial: dial,
		logf: func(string, ...any) {},
	}, nil
}

// SetLogger overrides the error sink.
func (s *RedisDispatchStore) SetLogger(logf func(format string, args ...any)) {
	if logf != nil {
		s.logf = logf
	}
}

func (s *RedisDispatchStore) Enqueue(deviceID, commandID int64, entry dispatchEntry) {
	body, err := json.Marshal(entryToRedis(entry, commandID))
	if err != nil {
		return
	}
	s.exec(
		buildRESP("HSET", dispatchKey(deviceID, commandID), "state", string(body)),
		buildRESP("EXPIRE", dispatchKey(deviceID, commandID), "3600"),
		buildRESP("SADD", dispatchSetKey(deviceID), strconv.FormatInt(commandID, 10)),
	)
}

func (s *RedisDispatchStore) Remove(deviceID, commandID int64) {
	s.exec(
		buildRESP("DEL", dispatchKey(deviceID, commandID)),
		buildRESP("SREM", dispatchSetKey(deviceID), strconv.FormatInt(commandID, 10)),
	)
}

func (s *RedisDispatchStore) Load(deviceID int64) []dispatchEntry {
	s.mu.Lock()
	defer s.mu.Unlock()

	if err := s.connect(context.Background()); err != nil {
		s.logf("gateway: redis dispatch load: %v", err)
		return nil
	}
	ids, err := s.replySMEMBERS(dispatchSetKey(deviceID))
	if err != nil {
		s.reset()
		s.logf("gateway: redis dispatch load ids: %v", err)
		return nil
	}

	entries := make([]dispatchEntry, 0, len(ids))
	for _, idStr := range ids {
		id, err := strconv.ParseInt(idStr, 10, 64)
		if err != nil {
			continue
		}
		if entry, ok := s.hgetEntry(deviceID, id); ok {
			entries = append(entries, entry)
		}
	}
	return entries
}

// exec writes a batch of commands and drains their replies normalising errors
// to a log line. It is the single choke point where a Redis fault stops being
// fatal.
func (s *RedisDispatchStore) exec(commands ...[]byte) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if err := s.connect(context.Background()); err != nil {
		s.logf("gateway: redis dispatch connect: %v", err)
		return
	}
	for _, command := range commands {
		if _, err := s.conn.Write(command); err != nil {
			s.reset()
			s.logf("gateway: redis dispatch write: %v", err)
			return
		}
		if _, err := s.reader.ReadString('\n'); err != nil {
			s.reset()
			s.logf("gateway: redis dispatch read: %v", err)
			return
		}
	}
}

// hgetEntry reads one entry hash. Returns false when it is gone or unreadable.
func (s *RedisDispatchStore) hgetEntry(deviceID, commandID int64) (dispatchEntry, bool) {
	if _, err := s.conn.Write(buildRESP("HGET", dispatchKey(deviceID, commandID), "state")); err != nil {
		s.reset()
		return dispatchEntry{}, false
	}
	line, err := s.reader.ReadString('\n')
	if err != nil {
		s.reset()
		return dispatchEntry{}, false
	}
	if !strings.HasPrefix(line, "$") {
		return dispatchEntry{}, false
	}
	length, err := strconv.Atoi(strings.TrimSpace(line[1:]))
	if err != nil || length <= 0 {
		return dispatchEntry{}, false
	}
	body := make([]byte, length)
	if _, err := ioReadFull(s.reader, body); err != nil {
		s.reset()
		return dispatchEntry{}, false
	}
	// trailing \r\n
	if _, err := s.reader.ReadString('\n'); err != nil {
		s.reset()
		return dispatchEntry{}, false
	}
	var r redisEntry
	if err := json.Unmarshal(body, &r); err != nil {
		return dispatchEntry{}, false
	}
	return dispatchEntry{
		protocol: r.Protocol,
		attempts: r.Attempts,
		next:     time.Unix(0, int64(r.NextUnix*float64(time.Second))),
		expires:  time.Unix(0, int64(r.ExpiresAt*float64(time.Second))),
	}, true
}

// replySMEMBERS reads a SMEMBERS reply: *n followed by n bulk strings.
func (s *RedisDispatchStore) replySMEMBERS(key string) ([]string, error) {
	if _, err := s.conn.Write(buildRESP("SMEMBERS", key)); err != nil {
		return nil, err
	}
	line, err := s.reader.ReadString('\n')
	if err != nil {
		return nil, err
	}
	if !strings.HasPrefix(line, "*") {
		return nil, fmt.Errorf("gateway: redis: unexpected SMEMBERS reply %q", strings.TrimSpace(line))
	}
	count, err := strconv.Atoi(strings.TrimSpace(line[1:]))
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, count)
	for i := 0; i < count; i++ {
		bulk, err := s.reader.ReadString('\n')
		if err != nil {
			return nil, err
		}
		if !strings.HasPrefix(bulk, "$") {
			return nil, fmt.Errorf("gateway: redis: unexpected SMEMBERS element %q", strings.TrimSpace(bulk))
		}
		length, err := strconv.Atoi(strings.TrimSpace(bulk[1:]))
		if err != nil {
			return nil, err
		}
		if length <= 0 {
			ids = append(ids, "")
			continue
		}
		body := make([]byte, length)
		if _, err := ioReadFull(s.reader, body); err != nil {
			return nil, err
		}
		if _, err := s.reader.ReadString('\n'); err != nil {
			return nil, err
		}
		ids = append(ids, string(body))
	}
	return ids, nil
}

func (s *RedisDispatchStore) connect(ctx context.Context) error {
	if s.conn != nil {
		return nil
	}
	target, err := url.Parse(s.url)
	if err != nil {
		return fmt.Errorf("gateway: redis dispatch url %q: %w", s.url, err)
	}
	address := target.Host
	if !strings.Contains(address, ":") {
		address += ":6379"
	}
	conn, err := s.dial(ctx, "tcp", address)
	if err != nil {
		return fmt.Errorf("gateway: redis dispatch dial %s: %w", address, err)
	}
	reader := bufio.NewReader(conn)
	if password, ok := target.User.Password(); ok && password != "" {
		if _, err := conn.Write(buildRESP("AUTH", password)); err != nil {
			_ = conn.Close()
			return fmt.Errorf("gateway: redis dispatch auth: %w", err)
		}
		reply, err := reader.ReadString('\n')
		if err != nil || strings.HasPrefix(reply, "-") {
			_ = conn.Close()
			return fmt.Errorf("gateway: redis dispatch auth rejected: %s", strings.TrimSpace(reply))
		}
	}
	s.conn = conn
	s.reader = reader
	return nil
}

func (s *RedisDispatchStore) reset() {
	if s.conn != nil {
		_ = s.conn.Close()
	}
	s.conn = nil
	s.reader = nil
}

// Close drops the connection.
func (s *RedisDispatchStore) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reset()
	return nil
}

func dispatchSetKey(deviceID int64) string {
	return dispatchKeyPrefix + "q:" + strconv.FormatInt(deviceID, 10)
}

func dispatchKey(deviceID, commandID int64) string {
	return dispatchKeyPrefix + "e:" + strconv.FormatInt(deviceID, 10) + ":" + strconv.FormatInt(commandID, 10)
}

func entryToRedis(entry dispatchEntry, commandID int64) redisEntry {
	var r redisEntry
	r.Protocol = entry.protocol
	r.Attempts = entry.attempts
	r.NextUnix = float64(entry.next.UnixNano()) / float64(time.Second)
	r.ExpiresAt = float64(entry.expires.UnixNano()) / float64(time.Second)
	r.Command.ID = commandID
	r.Command.DeviceID = entry.command.DeviceID
	r.Command.Serial = entry.command.Serial
	return r
}

// buildRESP encodes one command as a RESP array of bulk strings.
func buildRESP(values ...string) []byte {
	var builder strings.Builder
	builder.WriteString("*" + strconv.Itoa(len(values)) + "\r\n")
	for _, value := range values {
		builder.WriteString("$" + strconv.Itoa(len(value)) + "\r\n")
		builder.WriteString(value)
		builder.WriteString("\r\n")
	}
	return []byte(builder.String())
}

// ioReadFull reads exactly len(buf) bytes.
func ioReadFull(r *bufio.Reader, buf []byte) (int, error) {
	total := 0
	for total < len(buf) {
		n, err := r.Read(buf[total:])
		total += n
		if err != nil {
			return total, err
		}
		if n == 0 {
			return total, fmt.Errorf("gateway: redis: short read")
		}
	}
	return total, nil
}