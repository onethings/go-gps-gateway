package gateway_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"traccar-server_go/go_server/internal/codec"
	"traccar-server_go/go_server/internal/gateway"
	"traccar-server_go/go_server/internal/model"
)

// dispatchSender stands in for the router: it records every payload and can be
// told that the device is away.
type dispatchSender struct {
	mu        sync.Mutex
	sends     []send
	offline   bool
	unknown   bool
	encodeErr error
	encoded   []*model.Command
	// nextSerial is what the fake encoder hands to each command it encodes, so
	// a test can tell a fresh serial from a recovered one.
	nextSerial int
}

type send struct {
	protocol string
	deviceID int64
	payload  []byte
}

func (s *dispatchSender) EncodeCommand(protocol string, command *model.Command) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.encodeErr != nil {
		return nil, s.encodeErr
	}
	if command.Serial == 0 {
		s.nextSerial++
		command.Serial = 100 + s.nextSerial
	}
	s.encoded = append(s.encoded, command)
	return []byte{byte(command.Serial)}, nil
}

func (s *dispatchSender) Send(protocol string, deviceID int64, payload []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	switch {
	case s.unknown:
		return gateway.ErrUnknownProtocol
	case s.offline:
		return gateway.ErrDeviceOffline
	}
	s.sends = append(s.sends, send{protocol: protocol, deviceID: deviceID, payload: payload})
	return nil
}

func (s *dispatchSender) setOffline(offline bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.offline = offline
}

func (s *dispatchSender) written() []send {
	s.mu.Lock()
	defer s.mu.Unlock()

	return append([]send(nil), s.sends...)
}

// dispatchLog is the storage the dispatcher writes through.
type dispatchLog struct {
	mu      sync.Mutex
	sent    []int64
	failed  []int64
	acks    []codec.CommandAck
	pending []model.Command
}

func (l *dispatchLog) MarkCommandSent(_ context.Context, commandID int64) error {
	l.mu.Lock()
	defer l.mu.Unlock()

	l.sent = append(l.sent, commandID)
	return nil
}

func (l *dispatchLog) MarkCommandFailed(_ context.Context, commandID int64) error {
	l.mu.Lock()
	defer l.mu.Unlock()

	l.failed = append(l.failed, commandID)
	return nil
}

func (l *dispatchLog) AcknowledgeCommand(_ context.Context, _ int64, ack codec.CommandAck) error {
	l.mu.Lock()
	defer l.mu.Unlock()

	l.acks = append(l.acks, ack)
	return nil
}

func (l *dispatchLog) PendingCommands(context.Context, int64, int) ([]model.Command, error) {
	l.mu.Lock()
	defer l.mu.Unlock()

	return append([]model.Command(nil), l.pending...), nil
}

func (l *dispatchLog) sentIDs() []int64 {
	l.mu.Lock()
	defer l.mu.Unlock()

	return append([]int64(nil), l.sent...)
}

func (l *dispatchLog) failedIDs() []int64 {
	l.mu.Lock()
	defer l.mu.Unlock()

	return append([]int64(nil), l.failed...)
}

// newQueuedCommand records a command the way the REST layer does: the row
// exists, so the dispatcher is the only writer of its status.
func newQueuedCommand(id, deviceID int64, commandType string) *model.Command {
	command := model.NewCommand(deviceID, commandType)
	command.ID = id
	command.Status = model.CommandStatusPending
	return command
}

func TestDispatchDeliversToOnlineDevice(t *testing.T) {
	sender := &dispatchSender{}
	log := &dispatchLog{}
	dispatcher := gateway.NewDispatcher(sender, log)

	command := newQueuedCommand(1, 7, model.CommandEngineStop)
	delivered, err := dispatcher.Deliver(context.Background(), "gt06", command, []byte{0x01})
	if err != nil {
		t.Fatalf("deliver: %v", err)
	}
	if !delivered {
		t.Error("delivered = false for a device that is connected")
	}
	if queued := dispatcher.Queued(); queued != 1 {
		t.Errorf("queued = %d, want 1: the row waits for its answer", queued)
	}

	writes := sender.written()
	if len(writes) != 1 || writes[0].deviceID != 7 || writes[0].protocol != "gt06" {
		t.Fatalf("writes = %+v", writes)
	}
	if sent := log.sentIDs(); len(sent) != 1 || sent[0] != 1 {
		t.Fatalf("sent = %v, want [1]", sent)
	}
}

func TestDispatchQueuesWhileTheDeviceIsAway(t *testing.T) {
	sender := &dispatchSender{offline: true}
	log := &dispatchLog{}
	dispatcher := gateway.NewDispatcher(sender, log)

	command := newQueuedCommand(1, 7, model.CommandEngineStop)
	delivered, err := dispatcher.Deliver(context.Background(), "gt06", command, []byte{0x01})
	if err != nil {
		t.Fatalf("deliver: %v", err)
	}
	if delivered {
		t.Error("delivered = true for a device that is offline")
	}
	if queued := dispatcher.Queued(); queued != 1 {
		t.Fatalf("queued = %d, want 1", queued)
	}
	if sent := log.sentIDs(); len(sent) != 0 {
		t.Fatalf("sent = %v, want none: nothing reached the socket", sent)
	}
	if failed := log.failedIDs(); len(failed) != 0 {
		t.Fatalf("failed = %v: an offline device is a wait, not a failure", failed)
	}

	// The device comes back: the queued command goes out before anything else.
	sender.setOffline(false)
	dispatcher.DeviceConnected("gt06", 7)

	if writes := sender.written(); len(writes) != 1 || writes[0].deviceID != 7 {
		t.Fatalf("writes after reconnect = %+v", writes)
	}
	if sent := log.sentIDs(); len(sent) != 1 || sent[0] != 1 {
		t.Fatalf("sent after reconnect = %v, want [1]", sent)
	}
}

// TestDispatchRetransmitsUntilTheAttemptsRunOut covers the retransmission rule:
// every retry period writes the same payload again, and the command fails once
// it may not be written any more.
func TestDispatchRetransmitsUntilTheAttemptsRunOut(t *testing.T) {
	sender := &dispatchSender{}
	log := &dispatchLog{}
	dispatcher := gateway.NewDispatcher(sender, log,
		gateway.WithCommandRetry(20*time.Millisecond),
		gateway.WithCommandAttempts(2),
	)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		dispatcher.Run(ctx)
	}()

	command := newQueuedCommand(1, 7, model.CommandEngineStop)
	if _, err := dispatcher.Deliver(ctx, "gt06", command, []byte{0x01}); err != nil {
		t.Fatalf("deliver: %v", err)
	}

	waitFor(t, func() bool { return len(log.failedIDs()) == 1 })

	cancel()
	<-done

	if writes := sender.written(); len(writes) != 2 {
		t.Fatalf("writes = %d, want 2: one send and one retransmission", len(writes))
	}
	if queued := dispatcher.Queued(); queued != 0 {
		t.Errorf("queued = %d, want 0 after giving up", queued)
	}
}

// TestDispatchGivesUpOnADeviceThatNeverComesBack covers the queue lifetime: a
// command that was never written cannot wait forever.
func TestDispatchGivesUpOnADeviceThatNeverComesBack(t *testing.T) {
	sender := &dispatchSender{offline: true}
	log := &dispatchLog{}
	dispatcher := gateway.NewDispatcher(sender, log,
		gateway.WithCommandRetry(20*time.Millisecond),
		gateway.WithCommandQueueTTL(40*time.Millisecond),
	)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		dispatcher.Run(ctx)
	}()

	command := newQueuedCommand(1, 7, model.CommandEngineStop)
	if _, err := dispatcher.Deliver(ctx, "gt06", command, []byte{0x01}); err != nil {
		t.Fatalf("deliver: %v", err)
	}

	waitFor(t, func() bool { return len(log.failedIDs()) == 1 })

	cancel()
	<-done

	if sent := log.sentIDs(); len(sent) != 0 {
		t.Errorf("sent = %v, want none: the device never took the payload", sent)
	}
	if queued := dispatcher.Queued(); queued != 0 {
		t.Errorf("queued = %d, want 0 after the lifetime ran out", queued)
	}
}

// TestDispatchAcknowledgeBySerialPairsTheAnswer pins the exact pairing: an
// answer that names a serial closes that command, not the newest one.
func TestDispatchAcknowledgeBySerialPairsTheAnswer(t *testing.T) {
	sender := &dispatchSender{}
	log := &dispatchLog{}
	dispatcher := gateway.NewDispatcher(sender, log)

	older := newQueuedCommand(1, 7, model.CommandPositionSingle)
	older.Serial = 41
	newer := newQueuedCommand(2, 7, model.CommandPositionStop)
	newer.Serial = 42
	for _, command := range []*model.Command{older, newer} {
		if _, err := dispatcher.Deliver(context.Background(), "jt808", command, []byte{0x01}); err != nil {
			t.Fatalf("deliver %d: %v", command.ID, err)
		}
	}
	if queued := dispatcher.Queued(); queued != 2 {
		t.Fatalf("queued = %d, want 2", queued)
	}

	serial := 41
	if err := dispatcher.AcknowledgeCommand(
		context.Background(), 7, codec.CommandAck{Code: 0, Serial: &serial},
	); err != nil {
		t.Fatalf("acknowledge: %v", err)
	}

	queue := dispatcher.QueueFor(7)
	if len(queue) != 1 || queue[0] != 2 {
		t.Fatalf("queue after the answer = %v, want only command 2", queue)
	}
	if acks := log.ackIDs(); len(acks) != 1 || acks[0] != 41 {
		t.Fatalf("stored acks = %v, want the serial 41 one", acks)
	}
}

// TestDispatchWithoutSerialClosesTheNewest covers the protocols that carry no
// serial: the answer still closes one command, the newest that waits.
func TestDispatchWithoutSerialClosesTheNewest(t *testing.T) {
	sender := &dispatchSender{}
	log := &dispatchLog{}
	dispatcher := gateway.NewDispatcher(sender, log)

	for id := int64(1); id <= 2; id++ {
		if _, err := dispatcher.Deliver(
			context.Background(), "gt06", newQueuedCommand(id, 7, model.CommandEngineStop), []byte{0x01},
		); err != nil {
			t.Fatalf("deliver %d: %v", id, err)
		}
	}

	if err := dispatcher.AcknowledgeCommand(
		context.Background(), 7, codec.CommandAck{Code: 0},
	); err != nil {
		t.Fatalf("acknowledge: %v", err)
	}

	queue := dispatcher.QueueFor(7)
	if len(queue) != 1 || queue[0] != 1 {
		t.Fatalf("queue after the answer = %v, want only command 1", queue)
	}
}

// TestDispatchRecoversWhatTheRestartLost covers the reconnect path: rows that
// were recorded but never written are encoded again and delivered, and the
// serial the row already carries survives so the answer still pairs.
func TestDispatchRecoversWhatTheRestartLost(t *testing.T) {
	sender := &dispatchSender{}
	log := &dispatchLog{}
	log.pending = []model.Command{
		{ID: 9, DeviceID: 7, Type: model.CommandEngineStop, Serial: 41,
			Status: model.CommandStatusPending},
	}
	dispatcher := gateway.NewDispatcher(sender, log)

	dispatcher.DeviceConnected("jt808", 7)

	writes := sender.written()
	if len(writes) != 1 || writes[0].protocol != "jt808" || writes[0].deviceID != 7 {
		t.Fatalf("writes = %+v", writes)
	}
	if len(sender.encoded) != 1 || sender.encoded[0].Serial != 41 {
		t.Fatalf("encoded = %+v, want the stored serial 41", sender.encoded)
	}
	if sent := log.sentIDs(); len(sent) != 1 || sent[0] != 9 {
		t.Fatalf("sent = %v, want [9]", sent)
	}

	// The recovery runs once: a second connect does not encode the row again.
	dispatcher.DeviceConnected("jt808", 7)
	if len(sender.encoded) != 1 {
		t.Errorf("encoded = %d, want 1: the pending rows were loaded twice", len(sender.encoded))
	}
}

// TestDispatchRefusesAFullQueue keeps one device from growing the server state
// without bound.
func TestDispatchRefusesAFullQueue(t *testing.T) {
	sender := &dispatchSender{offline: true}
	log := &dispatchLog{}
	dispatcher := gateway.NewDispatcher(sender, log, gateway.WithCommandQueue(1))

	if _, err := dispatcher.Deliver(
		context.Background(), "gt06", newQueuedCommand(1, 7, model.CommandEngineStop), []byte{0x01},
	); err != nil {
		t.Fatalf("first deliver: %v", err)
	}

	delivered, err := dispatcher.Deliver(
		context.Background(), "gt06", newQueuedCommand(2, 7, model.CommandEngineStop), []byte{0x01},
	)
	if !errors.Is(err, gateway.ErrCommandQueueFull) {
		t.Fatalf("second deliver err = %v, want ErrCommandQueueFull", err)
	}
	if delivered {
		t.Error("delivered = true for a command that was refused")
	}
	if failed := log.failedIDs(); len(failed) != 1 || failed[0] != 2 {
		t.Fatalf("failed = %v, want [2]: a refused command cannot wait", failed)
	}
	if queued := dispatcher.Queued(); queued != 1 {
		t.Errorf("queued = %d, want 1", queued)
	}
}

// TestDispatchFailsACommandWithoutAProtocol covers the one send error that no
// reconnect can fix.
func TestDispatchFailsACommandWithoutAProtocol(t *testing.T) {
	sender := &dispatchSender{unknown: true}
	log := &dispatchLog{}
	dispatcher := gateway.NewDispatcher(sender, log)

	delivered, err := dispatcher.Deliver(
		context.Background(), "gt06", newQueuedCommand(1, 7, model.CommandEngineStop), []byte{0x01},
	)
	if !errors.Is(err, gateway.ErrUnknownProtocol) {
		t.Fatalf("err = %v, want ErrUnknownProtocol", err)
	}
	if delivered {
		t.Error("delivered = true for a protocol that is not listening")
	}
	if failed := log.failedIDs(); len(failed) != 1 || failed[0] != 1 {
		t.Fatalf("failed = %v, want [1]", failed)
	}
	if queued := dispatcher.Queued(); queued != 0 {
		t.Errorf("queued = %d, want 0", queued)
	}
}

// TestDispatchKeepsOneQueuePerDevice checks that a device that is away does not
// hold up a device that is connected.
func TestDispatchKeepsOneQueuePerDevice(t *testing.T) {
	sender := &dispatchSender{offline: true}
	log := &dispatchLog{}
	dispatcher := gateway.NewDispatcher(sender, log)

	if _, err := dispatcher.Deliver(
		context.Background(), "gt06", newQueuedCommand(1, 7, model.CommandEngineStop), []byte{0x01},
	); err != nil {
		t.Fatalf("deliver: %v", err)
	}

	// Device 8 connects while device 7 is still away.
	dispatcher.DeviceConnected("gt06", 8)
	if queued := dispatcher.Queued(); queued != 1 {
		t.Errorf("queued = %d, want 1: only device 7 has a command", queued)
	}

	sender.setOffline(false)
	if _, err := dispatcher.Deliver(
		context.Background(), "gt06", newQueuedCommand(2, 8, model.CommandEngineStop), []byte{0x01},
	); err != nil {
		t.Fatalf("deliver to device 8: %v", err)
	}
	if queued := dispatcher.Queued(); queued != 2 {
		t.Errorf("queued = %d, want 2", queued)
	}
	if writes := sender.written(); len(writes) != 1 || writes[0].deviceID != 8 {
		t.Errorf("writes = %+v, want only device 8", writes)
	}
}

func (l *dispatchLog) ackIDs() []int {
	l.mu.Lock()
	defer l.mu.Unlock()

	ids := make([]int, 0, len(l.acks))
	for _, ack := range l.acks {
		if ack.Serial == nil {
			ids = append(ids, 0)
			continue
		}
		ids = append(ids, *ack.Serial)
	}
	return ids
}
