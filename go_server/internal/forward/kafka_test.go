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
	"strings"
	"testing"
)

func kafkaTestReadRequest(reader *bufio.Reader) ([]byte, error) {
	var header [4]byte
	if _, err := io.ReadFull(reader, header[:]); err != nil {
		return nil, err
	}
	body := make([]byte, binary.BigEndian.Uint32(header[:]))
	if _, err := io.ReadFull(reader, body); err != nil {
		return nil, err
	}
	return body, nil
}

func kafkaTestWriteResponse(writer io.Writer, body []byte) error {
	frame := make([]byte, 4, 4+len(body))
	binary.BigEndian.PutUint32(frame, uint32(len(body)))
	_, err := writer.Write(append(frame, body...))
	return err
}

// kafkaServeMetadata answers the Metadata request with one broker that leads
// the single partition of the topic.
func kafkaServeMetadata(reader *bufio.Reader, writer io.Writer, topic string) error {
	body, err := kafkaTestReadRequest(reader)
	if err != nil {
		return err
	}
	request := kafkaReader{data: body}
	if key := request.int16(); key != kafkaMetadataKey {
		return fmt.Errorf("api key = %d, want metadata", key)
	}
	if version := request.int16(); version != kafkaMetadataVersion {
		return fmt.Errorf("version = %d", version)
	}
	correlation := request.int32()
	request.str() // client id
	if topics := request.arrayLen(); topics != 1 {
		return fmt.Errorf("topics = %d, want 1", topics)
	}
	if name := request.str(); name != topic {
		return fmt.Errorf("topic = %q", name)
	}
	request.octet() // allow auto topic creation
	if request.err != nil {
		return request.err
	}

	var reply kafkaWriter
	reply.int32(correlation)
	reply.int32(0) // throttle time
	reply.int32(1) // brokers
	reply.int32(1) // node id
	reply.str("broker.test")
	reply.int32(9092)
	reply.nullableString() // rack
	reply.nullableString() // cluster id
	reply.int32(1)         // controller id
	reply.int32(1)         // topics
	reply.int16(0)         // topic error
	reply.str(topic)
	reply.octet(0) // is internal
	reply.int32(1) // partitions
	reply.int16(0) // partition error
	reply.int32(0) // partition index
	reply.int32(1) // leader
	reply.int32(1) // replicas
	reply.int32(1)
	reply.int32(1) // in-sync replicas
	reply.int32(1)
	return kafkaTestWriteResponse(writer, reply.data)
}

// kafkaServeProduce reads one Produce request and answers it with code.
func kafkaServeProduce(reader *bufio.Reader, writer io.Writer, topic string, code int16) ([]byte, error) {
	body, err := kafkaTestReadRequest(reader)
	if err != nil {
		return nil, err
	}
	request := kafkaReader{data: body}
	if key := request.int16(); key != kafkaProduceKey {
		return nil, fmt.Errorf("api key = %d, want produce", key)
	}
	if version := request.int16(); version != kafkaProduceVersion {
		return nil, fmt.Errorf("version = %d", version)
	}
	correlation := request.int32()
	request.str()            // client id
	request.nullableString() // transactional id
	if acks := request.int16(); acks != kafkaAcksAll {
		return nil, fmt.Errorf("acks = %d", acks)
	}
	request.int32() // timeout
	if topics := request.arrayLen(); topics != 1 {
		return nil, fmt.Errorf("topics = %d, want 1", topics)
	}
	if name := request.str(); name != topic {
		return nil, fmt.Errorf("topic = %q", name)
	}
	if partitions := request.arrayLen(); partitions != 1 {
		return nil, fmt.Errorf("partitions = %d, want 1", partitions)
	}
	request.int32() // partition index
	batch := request.take(int(request.int32()))
	if request.err != nil {
		return nil, request.err
	}

	var reply kafkaWriter
	reply.int32(correlation)
	reply.int32(1) // topics
	reply.str(topic)
	reply.int32(1) // partitions
	reply.int32(0) // partition index
	reply.int16(code)
	reply.int64(12) // base offset
	reply.int64(-1) // log append time
	reply.int32(0)  // throttle time
	return batch, kafkaTestWriteResponse(writer, reply.data)
}

// kafkaSend pushes one position through a fake metadata broker and leader.
func kafkaSend(t *testing.T, code int16) ([]byte, error) {
	t.Helper()
	bootstrapClient, bootstrapServer := net.Pipe()
	leaderClient, leaderServer := net.Pipe()
	t.Cleanup(func() {
		_ = bootstrapServer.Close()
		_ = leaderServer.Close()
	})

	metadata := make(chan error, 1)
	go func() {
		metadata <- kafkaServeMetadata(bufio.NewReader(bootstrapServer), bootstrapServer, "traccar")
	}()
	produced := make(chan []byte, 1)
	produceErr := make(chan error, 1)
	go func() {
		batch, err := kafkaServeProduce(bufio.NewReader(leaderServer), leaderServer, "traccar", code)
		produced <- batch
		produceErr <- err
	}()

	dials := 0
	transport, err := NewKafka(KafkaConfig{
		Brokers: "bootstrap.test:9092",
		Topic:   "traccar",
		Dial: func(context.Context, string, string) (net.Conn, error) {
			dials++
			if dials == 1 {
				return bootstrapClient, nil
			}
			return leaderClient, nil
		},
	})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	defer transport.Close()

	sendErr := transport.Send(context.Background(), Message{Position: testPosition(), Device: testDevice()})
	if err := <-metadata; err != nil {
		t.Fatalf("metadata: %v", err)
	}
	batch, err := <-produced, <-produceErr
	if err != nil {
		t.Fatalf("produce: %v", err)
	}
	return batch, sendErr
}

func TestKafkaTransportProducesRecordBatch(t *testing.T) {
	batch, err := kafkaSend(t, 0)
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	if len(batch) < 21 {
		t.Fatalf("batch is %d bytes", len(batch))
	}
	if batch[16] != 2 {
		t.Fatalf("magic = %d, want 2", batch[16])
	}
	if length := int(binary.BigEndian.Uint32(batch[8:12])); length != len(batch)-12 {
		t.Fatalf("batch length = %d, want %d", length, len(batch)-12)
	}
	checksum := binary.BigEndian.Uint32(batch[17:21])
	if want := crc32.Checksum(batch[21:], kafkaCastagnoli); checksum != want {
		t.Fatalf("crc = %#x, want %#x", checksum, want)
	}
	key, value, err := kafkaTestRecord(batch)
	if err != nil {
		t.Fatalf("record: %v", err)
	}
	if key != "7" {
		t.Fatalf("key = %q, want the device id", key)
	}
	var document map[string]any
	if err := json.Unmarshal(value, &document); err != nil {
		t.Fatalf("value %q: %v", value, err)
	}
	if position, ok := document["position"].(map[string]any); !ok || position["deviceId"].(float64) != 7 {
		t.Fatalf("value = %s", value)
	}
}

// kafkaTestRecord decodes the single record of a v2 batch.
func kafkaTestRecord(batch []byte) (string, []byte, error) {
	if len(batch) < 61 {
		return "", nil, fmt.Errorf("batch is %d bytes", len(batch))
	}
	if count := int32(binary.BigEndian.Uint32(batch[57:61])); count != 1 {
		return "", nil, fmt.Errorf("record count = %d", count)
	}
	at := 61
	length, at, err := kafkaTestVarint(batch, at)
	if err != nil {
		return "", nil, err
	}
	end := at + int(length)
	if end > len(batch) {
		return "", nil, fmt.Errorf("record length %d runs past the batch", length)
	}
	at++                                                     // attributes
	if _, at, err = kafkaTestVarint(batch, at); err != nil { // timestamp delta
		return "", nil, err
	}
	if _, at, err = kafkaTestVarint(batch, at); err != nil { // offset delta
		return "", nil, err
	}
	keyLength, at, err := kafkaTestVarint(batch, at)
	if err != nil {
		return "", nil, err
	}
	if at+int(keyLength) > end {
		return "", nil, fmt.Errorf("key runs past the record")
	}
	key := string(batch[at : at+int(keyLength)])
	at += int(keyLength)
	valueLength, at, err := kafkaTestVarint(batch, at)
	if err != nil {
		return "", nil, err
	}
	if at+int(valueLength) > end {
		return "", nil, fmt.Errorf("value runs past the record")
	}
	return key, batch[at : at+int(valueLength)], nil
}

// kafkaTestVarint decodes the zigzag varint the record uses.
func kafkaTestVarint(data []byte, at int) (int64, int, error) {
	var encoded uint64
	for shift := uint(0); ; shift += 7 {
		if at >= len(data) || shift > 63 {
			return 0, 0, io.ErrUnexpectedEOF
		}
		value := data[at]
		at++
		encoded |= uint64(value&0x7f) << shift
		if value&0x80 == 0 {
			return int64(encoded>>1) ^ -int64(encoded&1), at, nil
		}
	}
}

func TestKafkaTransportReportsPartitionError(t *testing.T) {
	_, err := kafkaSend(t, 6)
	if err == nil || !strings.Contains(err.Error(), "not leader") {
		t.Fatalf("error = %v, want a not leader error", err)
	}
}

func TestKafkaPartitionerKeepsOneKeyTogether(t *testing.T) {
	first := kafkaPartition("7", 8)
	for index := 0; index < 10; index++ {
		if got := kafkaPartition("7", 8); got != first {
			t.Fatalf("partition = %d, want %d", got, first)
		}
	}
	if first < 0 || first >= 8 {
		t.Fatalf("partition = %d is out of range", first)
	}
}

func TestKafkaConfigValidation(t *testing.T) {
	if _, err := NewKafka(KafkaConfig{Topic: "traccar"}); err == nil {
		t.Fatal("empty brokers accepted")
	}
	if _, err := NewKafka(KafkaConfig{Brokers: " , "}); err == nil {
		t.Fatal("blank brokers accepted")
	}
	if _, err := NewKafka(KafkaConfig{Brokers: "broker:9092"}); err == nil {
		t.Fatal("empty topic accepted")
	}
}
