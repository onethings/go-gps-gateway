package forward

import (
	"bufio"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"hash/crc32"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Kafka protocol constants. The versions are the oldest ones every broker from
// 0.11 on still accepts, which is also where the v2 record batch appeared.
const (
	kafkaClientID        = "traccar-gateway"
	kafkaProduceKey      = 0
	kafkaProduceVersion  = 3
	kafkaMetadataKey     = 3
	kafkaMetadataVersion = 4
	kafkaAcksAll         = -1
	kafkaTimeoutMillis   = 5000
	kafkaMaxResponse     = 32 << 20
	kafkaMaxArray        = 1 << 20
)

// kafkaCastagnoli is the table of the CRC-32C a record batch carries.
var kafkaCastagnoli = crc32.MakeTable(crc32.Castagnoli)

// KafkaConfig configures the Kafka forwarder. Brokers is the comma separated
// bootstrap list the Java client calls bootstrap.servers; Topic is the
// destination topic.
type KafkaConfig struct {
	Brokers string
	Topic   string
	// Dial defaults to net.Dialer.DialContext; tests pass net.Pipe.
	Dial func(ctx context.Context, network, address string) (net.Conn, error)
}

// KafkaTransport produces the position or event JSON, keyed by device id so all
// messages of one device land on the same partition, like the Java producer.
type KafkaTransport struct {
	config    KafkaConfig
	bootstrap []string
	dial      func(ctx context.Context, network, address string) (net.Conn, error)

	mu          sync.Mutex
	conn        net.Conn
	reader      *bufio.Reader
	address     string
	next        int // bootstrap address the last metadata request used
	correlation int32
	partitions  int32
	leaders     map[int32]string
}

// NewKafka creates the forwarder.
func NewKafka(config KafkaConfig) (*KafkaTransport, error) {
	if config.Topic == "" {
		return nil, fmt.Errorf("forward kafka: empty topic")
	}
	var bootstrap []string
	for _, address := range strings.Split(config.Brokers, ",") {
		if address = strings.TrimSpace(address); address != "" {
			if !strings.Contains(address, ":") {
				address += ":9092"
			}
			bootstrap = append(bootstrap, address)
		}
	}
	if len(bootstrap) == 0 {
		return nil, fmt.Errorf("forward kafka: empty brokers")
	}
	dial := config.Dial
	if dial == nil {
		dialer := &net.Dialer{Timeout: 10 * time.Second}
		dial = dialer.DialContext
	}
	return &KafkaTransport{config: config, bootstrap: bootstrap, dial: dial}, nil
}

// Name implements Transport.
func (t *KafkaTransport) Name() string { return "kafka" }

// Send implements Transport.
func (t *KafkaTransport) Send(ctx context.Context, message Message) error {
	encoded, err := json.Marshal(payload(message))
	if err != nil {
		return fmt.Errorf("forward kafka: encode: %w", err)
	}
	key := ""
	if message.Device != nil {
		key = strconv.FormatInt(message.Device.ID, 10)
	}

	t.mu.Lock()
	defer t.mu.Unlock()
	if t.leaders == nil {
		if err := t.loadMetadata(ctx); err != nil {
			t.reset()
			return err
		}
	}
	partition := kafkaPartition(key, t.partitions)
	if err := t.connect(ctx, partition); err != nil {
		t.reset()
		return err
	}
	if deadline, ok := ctx.Deadline(); ok {
		_ = t.conn.SetDeadline(deadline)
	}
	if err := t.produce(partition, key, encoded); err != nil {
		t.reset()
		return err
	}
	_ = t.conn.SetDeadline(time.Time{})
	return nil
}

// loadMetadata asks the bootstrap servers for the partition leaders. It tries
// every address once, so one dead broker does not take the transport down.
func (t *KafkaTransport) loadMetadata(ctx context.Context) error {
	var lastErr error
	for attempt := 0; attempt < len(t.bootstrap); attempt++ {
		index := (t.next + attempt) % len(t.bootstrap)
		address := t.bootstrap[index]
		conn, err := t.dial(ctx, "tcp", address)
		if err != nil {
			lastErr = err
			continue
		}
		if deadline, ok := ctx.Deadline(); ok {
			_ = conn.SetDeadline(deadline)
		}
		t.conn, t.reader, t.address = conn, bufio.NewReader(conn), address
		leaders, partitions, err := t.metadata()
		if err == nil {
			t.next = (index + 1) % len(t.bootstrap)
			t.leaders, t.partitions = leaders, partitions
			_ = conn.SetDeadline(time.Time{})
			return nil
		}
		lastErr = err
		t.disconnect()
	}
	return fmt.Errorf("forward kafka: metadata from %s: %w", t.config.Brokers, lastErr)
}

// metadata sends a Metadata request and returns the leader of every partition.
func (t *KafkaTransport) metadata() (map[int32]string, int32, error) {
	correlation := t.nextCorrelation()

	var request kafkaWriter
	request.int16(kafkaMetadataKey)
	request.int16(kafkaMetadataVersion)
	request.int32(correlation)
	request.str(kafkaClientID)
	request.int32(1) // one topic
	request.str(t.config.Topic)
	request.octet(0) // do not create the topic

	body, err := t.roundTrip(request.data)
	if err != nil {
		return nil, 0, err
	}

	reply := kafkaReader{data: body}
	if got := reply.int32(); got != correlation {
		return nil, 0, fmt.Errorf("forward kafka: correlation %d, want %d", got, correlation)
	}
	reply.int32() // throttle time
	brokers := map[int32]string{}
	for count := reply.arrayLen(); count > 0 && reply.err == nil; count-- {
		broker := reply.int32()
		host := reply.str()
		port := reply.int32()
		reply.nullableString() // rack
		brokers[broker] = net.JoinHostPort(host, strconv.Itoa(int(port)))
	}
	reply.nullableString() // cluster id
	reply.int32()          // controller id

	leaders := map[int32]string{}
	var partitions int32
	for topics := reply.arrayLen(); topics > 0 && reply.err == nil; topics-- {
		topicError := reply.int16()
		name := reply.str()
		reply.octet() // is internal
		count := reply.arrayLen()
		if topicError == 0 && name == t.config.Topic {
			partitions = int32(count)
		}
		for parts := count; parts > 0 && reply.err == nil; parts-- {
			partitionError := reply.int16()
			partition := reply.int32()
			leader := reply.int32()
			reply.int32Array() // replicas
			reply.int32Array() // in-sync replicas
			if topicError != 0 || partitionError != 0 || name != t.config.Topic {
				continue
			}
			if address, ok := brokers[leader]; ok {
				leaders[partition] = address
			}
		}
		if topicError != 0 && name == t.config.Topic {
			return nil, 0, fmt.Errorf("forward kafka: topic %s: %s", name, kafkaErrorText(topicError))
		}
	}
	if reply.err != nil {
		return nil, 0, fmt.Errorf("forward kafka: metadata reply: %w", reply.err)
	}
	if partitions == 0 {
		return nil, 0, fmt.Errorf("forward kafka: no partition for topic %s", t.config.Topic)
	}
	return leaders, partitions, nil
}

// connect points the connection at the leader of the partition.
func (t *KafkaTransport) connect(ctx context.Context, partition int32) error {
	leader, ok := t.leaders[partition]
	if !ok {
		return fmt.Errorf("forward kafka: no leader for partition %d", partition)
	}
	if t.conn != nil && t.address == leader {
		return nil
	}
	t.disconnect()
	conn, err := t.dial(ctx, "tcp", leader)
	if err != nil {
		return fmt.Errorf("forward kafka: dial %s: %w", leader, err)
	}
	t.conn, t.reader, t.address = conn, bufio.NewReader(conn), leader
	return nil
}

// produce sends one record batch with acks=all and checks the partition error.
func (t *KafkaTransport) produce(partition int32, key string, value []byte) error {
	correlation := t.nextCorrelation()

	var request kafkaWriter
	request.int16(kafkaProduceKey)
	request.int16(kafkaProduceVersion)
	request.int32(correlation)
	request.str(kafkaClientID)
	request.nullableString() // transactional id: none
	request.int16(kafkaAcksAll)
	request.int32(kafkaTimeoutMillis)
	request.int32(1) // one topic
	request.str(t.config.Topic)
	request.int32(1) // one partition
	request.int32(partition)
	request.bytes(kafkaRecordBatch(key, value, time.Now()))

	body, err := t.roundTrip(request.data)
	if err != nil {
		return err
	}

	reply := kafkaReader{data: body}
	if got := reply.int32(); got != correlation {
		return fmt.Errorf("forward kafka: correlation %d, want %d", got, correlation)
	}
	for topics := reply.arrayLen(); topics > 0 && reply.err == nil; topics-- {
		reply.str() // topic
		for parts := reply.arrayLen(); parts > 0 && reply.err == nil; parts-- {
			reply.int32() // partition
			if code := reply.int16(); code != 0 {
				return fmt.Errorf("forward kafka: produce to partition %d: %s",
					partition, kafkaErrorText(code))
			}
			reply.int64() // base offset
			reply.int64() // log append time
		}
	}
	reply.int32() // throttle time
	if reply.err != nil {
		return fmt.Errorf("forward kafka: produce reply: %w", reply.err)
	}
	return nil
}

// roundTrip writes one length prefixed request and reads the reply.
func (t *KafkaTransport) roundTrip(request []byte) ([]byte, error) {
	frame := make([]byte, 4, 4+len(request))
	binary.BigEndian.PutUint32(frame, uint32(len(request)))
	if _, err := t.conn.Write(append(frame, request...)); err != nil {
		return nil, fmt.Errorf("forward kafka: write: %w", err)
	}
	var header [4]byte
	if _, err := io.ReadFull(t.reader, header[:]); err != nil {
		return nil, fmt.Errorf("forward kafka: read: %w", err)
	}
	size := binary.BigEndian.Uint32(header[:])
	if size > kafkaMaxResponse {
		return nil, fmt.Errorf("forward kafka: response too large (%d bytes)", size)
	}
	body := make([]byte, size)
	if _, err := io.ReadFull(t.reader, body); err != nil {
		return nil, fmt.Errorf("forward kafka: read: %w", err)
	}
	return body, nil
}

func (t *KafkaTransport) nextCorrelation() int32 {
	t.correlation++
	return t.correlation
}

// disconnect drops the broker connection but keeps the partition metadata.
func (t *KafkaTransport) disconnect() {
	if t.conn != nil {
		_ = t.conn.Close()
	}
	t.conn = nil
	t.reader = nil
	t.address = ""
}

// reset drops the connection and the metadata, so the next send refreshes both.
func (t *KafkaTransport) reset() {
	t.disconnect()
	t.leaders = nil
	t.partitions = 0
}

// Close implements Transport.
func (t *KafkaTransport) Close() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.reset()
	return nil
}

// kafkaPartition is the Java client's default partitioner: the positive murmur2
// hash of the key modulo the partition count.
func kafkaPartition(key string, partitions int32) int32 {
	if partitions <= 1 || key == "" {
		return 0
	}
	return int32(uint32(kafkaMurmur2([]byte(key)))&0x7fffffff) % partitions
}

// kafkaMurmur2 is Utils.murmur2 from the Java client, seed included.
func kafkaMurmur2(data []byte) uint32 {
	const (
		seed uint32 = 0x9747b28c
		m    uint32 = 0x5bd1e995
		r           = 24
	)
	hash := seed ^ uint32(len(data))
	index := 0
	for ; index+4 <= len(data); index += 4 {
		value := binary.LittleEndian.Uint32(data[index:])
		value *= m
		value ^= value >> r
		value *= m
		hash *= m
		hash ^= value
	}
	switch len(data) - index {
	case 3:
		hash ^= uint32(data[index+2]) << 16
		fallthrough
	case 2:
		hash ^= uint32(data[index+1]) << 8
		fallthrough
	case 1:
		hash ^= uint32(data[index])
		hash *= m
	}
	hash ^= hash >> 13
	hash *= m
	hash ^= hash >> 15
	return hash
}

// kafkaRecordBatch builds the v2 batch that carries one record.
func kafkaRecordBatch(key string, value []byte, sent time.Time) []byte {
	timestamp := sent.UnixMilli()

	var body kafkaWriter
	body.octet(0) // attributes
	body.varint(0)
	body.varint(0) // offset delta
	body.varint(int32(len(key)))
	body.data = append(body.data, key...)
	body.varint(int32(len(value)))
	body.data = append(body.data, value...)
	body.varint(0) // headers

	var record kafkaWriter
	record.varint(int32(len(body.data)))
	record.data = append(record.data, body.data...)

	var batch kafkaWriter
	batch.int64(0)  // base offset: the broker assigns it
	batch.int32(0)  // batch length, filled in below
	batch.int32(-1) // partition leader epoch
	batch.octet(2)  // magic, record batch v2
	batch.int32(0)  // crc, filled in below
	batch.int16(0)  // attributes
	batch.int32(0)  // last offset delta
	batch.int64(timestamp)
	batch.int64(timestamp)
	batch.int64(-1) // producer id: no idempotence
	batch.int16(-1) // producer epoch
	batch.int32(-1) // base sequence
	batch.int32(1)  // record count
	batch.data = append(batch.data, record.data...)

	binary.BigEndian.PutUint32(batch.data[8:12], uint32(len(batch.data)-12))
	binary.BigEndian.PutUint32(batch.data[17:21],
		crc32.Checksum(batch.data[21:], kafkaCastagnoli))
	return batch.data
}

// kafkaErrorText names the error codes a producer can see.
func kafkaErrorText(code int16) string {
	switch code {
	case 0:
		return "no error"
	case 1:
		return "offset out of range"
	case 2:
		return "corrupt message"
	case 3:
		return "unknown topic or partition"
	case 5:
		return "leader not available"
	case 6:
		return "not leader for partition"
	case 7:
		return "request timed out"
	case 10:
		return "message too large"
	case 17:
		return "invalid topic"
	case 19:
		return "not enough replicas"
	case 20:
		return "not enough replicas after append"
	case 29:
		return "topic authorization failed"
	}
	return "error code " + strconv.Itoa(int(code))
}

// kafkaWriter builds the Kafka protocol primitives.
type kafkaWriter struct{ data []byte }

func (w *kafkaWriter) octet(value byte) {
	w.data = append(w.data, value)
}

func (w *kafkaWriter) int16(value int16) {
	w.data = binary.BigEndian.AppendUint16(w.data, uint16(value))
}

func (w *kafkaWriter) int32(value int32) {
	w.data = binary.BigEndian.AppendUint32(w.data, uint32(value))
}

func (w *kafkaWriter) int64(value int64) {
	w.data = binary.BigEndian.AppendUint64(w.data, uint64(value))
}

func (w *kafkaWriter) str(value string) {
	w.int16(int16(len(value)))
	w.data = append(w.data, value...)
}

func (w *kafkaWriter) nullableString() {
	w.int16(-1)
}

func (w *kafkaWriter) bytes(value []byte) {
	w.int32(int32(len(value)))
	w.data = append(w.data, value...)
}

// varint writes a signed 32 bit integer in the protocol's zigzag varint.
func (w *kafkaWriter) varint(value int32) {
	encoded := uint64(uint32(value<<1) ^ uint32(value>>31))
	for encoded >= 0x80 {
		w.data = append(w.data, byte(encoded)|0x80)
		encoded >>= 7
	}
	w.data = append(w.data, byte(encoded))
}

// kafkaReader reads the Kafka protocol primitives and records the first error,
// so a parse only has to check err once.
type kafkaReader struct {
	data []byte
	pos  int
	err  error
}

func (r *kafkaReader) take(size int) []byte {
	if r.err != nil {
		return nil
	}
	if size < 0 || r.pos+size > len(r.data) {
		r.err = io.ErrUnexpectedEOF
		return nil
	}
	value := r.data[r.pos : r.pos+size]
	r.pos += size
	return value
}

func (r *kafkaReader) octet() byte {
	if value := r.take(1); value != nil {
		return value[0]
	}
	return 0
}

func (r *kafkaReader) int16() int16 {
	if value := r.take(2); value != nil {
		return int16(binary.BigEndian.Uint16(value))
	}
	return 0
}

func (r *kafkaReader) int32() int32 {
	if value := r.take(4); value != nil {
		return int32(binary.BigEndian.Uint32(value))
	}
	return 0
}

func (r *kafkaReader) int64() int64 {
	if value := r.take(8); value != nil {
		return int64(binary.BigEndian.Uint64(value))
	}
	return 0
}

func (r *kafkaReader) str() string {
	size := r.int16()
	if size <= 0 {
		return ""
	}
	if value := r.take(int(size)); value != nil {
		return string(value)
	}
	return ""
}

func (r *kafkaReader) nullableString() string {
	return r.str()
}

// arrayLen reads an array length; the protocol uses -1 for a null array.
func (r *kafkaReader) arrayLen() int {
	size := r.int32()
	if size < 0 {
		return 0
	}
	if size > kafkaMaxArray {
		r.err = fmt.Errorf("array of %d elements is too long", size)
		return 0
	}
	return int(size)
}

func (r *kafkaReader) int32Array() []int32 {
	count := r.arrayLen()
	values := make([]int32, 0, count)
	for index := 0; index < count && r.err == nil; index++ {
		values = append(values, r.int32())
	}
	return values
}
