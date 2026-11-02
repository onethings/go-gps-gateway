package gateway

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"traccar-server_go/go_server/internal/codec"
	"traccar-server_go/go_server/internal/metrics"
	"traccar-server_go/go_server/internal/model"
)

// ErrCommandQueueFull is returned when one device already holds as many
// undelivered commands as the dispatcher allows.
var ErrCommandQueueFull = errors.New("gateway: command queue full")

// Delivery policy defaults. A command is retransmitted every Retry period until
// it has been written Attempts times or the queue entry ran out of time.
const (
	DefaultCommandRetry    = 30 * time.Second
	DefaultCommandAttempts = 3
	DefaultCommandQueue    = 32
	DefaultCommandQueueTTL = 5 * time.Minute
)

// CommandLog is the persistence the dispatcher needs: the state of a row before
// it is written, and the record of how it ended.
type CommandLog interface {
	// MarkCommandSent records one write of the payload to the device socket.
	MarkCommandSent(ctx context.Context, commandID int64) error
	// MarkCommandFailed ends a command that will never be executed.
	MarkCommandFailed(ctx context.Context, commandID int64) error
	// AcknowledgeCommand closes the row the device answered.
	AcknowledgeCommand(ctx context.Context, deviceID int64, ack codec.CommandAck) error
	// PendingCommands returns the commands of device that were recorded but
	// never written, oldest first, so a reconnect can still deliver them.
	PendingCommands(ctx context.Context, deviceID int64, limit int) ([]model.Command, error)
}

// CommandSender turns a command into bytes and writes them to the device.
type CommandSender interface {
	EncodeCommand(protocol string, command *model.Command) ([]byte, error)
	Send(protocol string, deviceID int64, payload []byte) error
}

// DispatchOption customises a Dispatcher.
type DispatchOption func(*Dispatcher)

// WithCommandRetry sets how long a written command waits for its answer before
// the dispatcher writes it again.
func WithCommandRetry(retry time.Duration) DispatchOption {
	return func(d *Dispatcher) {
		if retry > 0 {
			d.retry = retry
		}
	}
}

// WithCommandAttempts sets how often a command may be written before the
// dispatcher gives up on it.
func WithCommandAttempts(attempts int) DispatchOption {
	return func(d *Dispatcher) {
		if attempts > 0 {
			d.attempts = attempts
		}
	}
}

// WithCommandQueue sets how many undelivered commands one device may hold, so a
// device that stays away cannot make the server hold unbounded state.
func WithCommandQueue(depth int) DispatchOption {
	return func(d *Dispatcher) {
		if depth > 0 {
			d.depth = depth
		}
	}
}

// WithCommandQueueTTL sets how long a command may wait for the device to come
// back before it is given up on.
func WithCommandQueueTTL(ttl time.Duration) DispatchOption {
	return func(d *Dispatcher) {
		if ttl > 0 {
			d.ttl = ttl
		}
	}
}

// WithDispatcherLogger overrides the log sink.
func WithDispatcherLogger(logf func(format string, args ...any)) DispatchOption {
	return func(d *Dispatcher) {
		if logf != nil {
			d.logf = logf
		}
	}
}

// WithDispatcherMetrics publishes the command counters.
func WithDispatcherMetrics(set *metrics.Set) DispatchOption {
	return func(d *Dispatcher) {
		if set == nil {
			return
		}
		d.sent = set.CommandsSent
		d.retried = set.CommandRetries
		d.dropped = set.CommandsDropped
		d.queued = set.CommandsQueued
	}
}

// WithQueuedSentHook reports a command that waited in the queue and was only
// written after its device came back. It is how the queuedCommandSent event
// gets raised: the dispatcher is the only place that knows a command had been
// waiting.
func WithQueuedSentHook(hook func(deviceID, commandID int64)) DispatchOption {
	return func(d *Dispatcher) {
		if hook != nil {
			d.onQueuedSent = hook
		}
	}
}

// WithDispatchState attaches an external mirror of the live retry bookkeeping.
// Enqueue/Remove/Load are best-effort; a nil or default store is a no-op so the
// dispatcher behaves exactly as it did before externalisation.
func WithDispatchState(state dispatchState) DispatchOption {
	return func(d *Dispatcher) {
		if state != nil {
			d.state = state
		}
	}
}

// Dispatcher delivers commands to devices and keeps the ones that cannot be
// delivered yet. It owns every status change of a command once the row exists:
// sent when the payload reached the socket, done or failed when the device
// answered, failed when the command ran out of attempts or of time.
//
// The queue is per device, so a command waiting for one device never delays
// another, and a write is serialised with the writes the decoding thread does on
// the same connection.
type Dispatcher struct {
	sender CommandSender
	store  CommandLog
	logf   func(format string, args ...any)

	retry    time.Duration
	attempts int
	depth    int
	ttl      time.Duration

	// mu guards devices and ctx; a device queue takes its own lock, always
	// after this one, and no code path takes them the other way round.
	mu      sync.Mutex
	devices map[int64]*deviceQueue
	ctx     context.Context

	// onQueuedSent reports a command written after it had waited in the queue.
	onQueuedSent func(deviceID, commandID int64)

	// state mirrors the live retry bookkeeping to a shared store (Redis) so a
	// restart restores exact timing and other nodes share the queue view. A
	// noDispatchState is the default: pure in-memory, as before externalisation.
	state dispatchState

	// countMu guards the size of the whole queue, which the gauge needs and
	// which is read without holding a device lock.
	countMu sync.Mutex
	count   int

	now func() time.Time

	sent    *metrics.Counter
	retried *metrics.Counter
	dropped *metrics.Counter
	queued  *metrics.Gauge
}

// dispatchEntry is one command the dispatcher still owns. It waits for a device
// (payload not written yet), then for an answer, and leaves the queue only when
// the device answers or the policy gives up.
type dispatchEntry struct {
	protocol string
	command  model.Command
	payload  []byte
	// attempts counts writes that reached the socket.
	attempts int
	// next is the earliest retransmission.
	next time.Time
	// expires is when an undelivered command is given up on.
	expires time.Time
}

// deviceQueue is the queue of one device. Its mutex serialises the writes of
// this device, which is the same granularity the connection itself uses.
type deviceQueue struct {
	// recovered reports whether the pending rows of the device were loaded from
	// the store, which happens once per process lifetime.
	recovered bool
	entries   []*dispatchEntry
	mu        sync.Mutex
}

// NewDispatcher builds a dispatcher. sender writes to the devices and store
// keeps the rows the dispatcher cannot finish by itself.
func NewDispatcher(sender CommandSender, store CommandLog, options ...DispatchOption) *Dispatcher {
	d := &Dispatcher{
		sender:   sender,
		store:    store,
		logf:     func(string, ...any) {},
		retry:    DefaultCommandRetry,
		attempts: DefaultCommandAttempts,
		depth:    DefaultCommandQueue,
		ttl:      DefaultCommandQueueTTL,
		devices:  make(map[int64]*deviceQueue),
		ctx:      context.Background(),
		now:      time.Now,
		state:    noDispatchState{},
	}
	for _, option := range options {
		option(d)
	}
	return d
}

// Deliver queues a command whose row already exists and writes it at once when
// the device is connected. It reports whether the payload reached the socket;
// false means the command is queued and will be written when the device
// reconnects. A full queue is the only way to be refused: the row is marked
// failed and ErrCommandQueueFull is returned.
func (d *Dispatcher) Deliver(
	ctx context.Context, protocol string, command *model.Command, payload []byte,
) (bool, error) {

	entry := &dispatchEntry{
		protocol: protocol,
		command:  *command,
		payload:  payload,
		next:     d.now(),
		expires:  d.now().Add(d.ttl),
	}

	queue := d.queue(command.DeviceID)
	queue.mu.Lock()
	defer queue.mu.Unlock()

	if err := d.enqueue(queue, entry); err != nil {
		d.fail(ctx, command.ID)
		return false, err
	}
	return d.write(ctx, command.DeviceID, queue, entry)
}

// AcknowledgeCommand closes the command the device answered and forgets it in
// the queue. The store stays the authority on which row ends, so an answer for a
// command this process never queued still lands.
func (d *Dispatcher) AcknowledgeCommand(ctx context.Context, deviceID int64, ack codec.CommandAck) error {
	d.retire(deviceID, ack.Serial)
	return d.store.AcknowledgeCommand(ctx, deviceID, ack)
}

// DeviceConnected loads the commands that were recorded for the device but never
// written, so a command queued before a restart is not lost, and then writes
// everything the device is waiting for.
func (d *Dispatcher) DeviceConnected(protocol string, deviceID int64) {
	ctx := d.context()

	queue := d.queue(deviceID)
	queue.mu.Lock()
	defer queue.mu.Unlock()

	if !queue.recovered {
		queue.recovered = true
		d.recover(ctx, protocol, deviceID, queue)
		d.restore(ctx, deviceID, queue)
	}

	for _, entry := range queue.entries {
		if entry.attempts == 0 {
			if _, err := d.write(ctx, deviceID, queue, entry); err != nil {
				d.logf("dispatch: command %d of device %d: %v", entry.command.ID, deviceID, err)
			}
		}
	}
	d.publishQueued()
}

// restore overlays the shared bookkeeping (Redis) onto the entries that were
// just re-created from the store, so exact retry timing survives a restart. It
// must be called with the queue locked. A command the shared store holds that is
// not in the queue was already answered or given up on, so it is ignored.
func (d *Dispatcher) restore(ctx context.Context, deviceID int64, queue *deviceQueue) {
	for _, remote := range d.state.Load(deviceID) {
		for _, entry := range queue.entries {
			if entry.command.ID == remote.command.ID {
				entry.protocol = remote.protocol
				entry.attempts = remote.attempts
				entry.next = remote.next
				entry.expires = remote.expires
				break
			}
		}
	}
}

// recover re-encodes the rows the device never received and queues them. It
// must be called with the queue locked.
func (d *Dispatcher) recover(ctx context.Context, protocol string, deviceID int64, queue *deviceQueue) {
	commands, err := d.store.PendingCommands(ctx, deviceID, d.depth)
	if err != nil {
		d.logf("dispatch: load pending commands of device %d: %v", deviceID, err)
		return
	}

	for i := range commands {
		command := commands[i]
		// A command that already carries a serial keeps it through the
		// re-encode, so the row still matches the answer the device sends.
		payload, err := d.sender.EncodeCommand(protocol, &command)
		if err != nil {
			d.logf("dispatch: re-encode command %d of device %d: %v", command.ID, deviceID, err)
			d.fail(ctx, command.ID)
			continue
		}
		if err := d.enqueue(queue, &dispatchEntry{
			protocol: protocol,
			command:  command,
			payload:  payload,
			next:     d.now(),
			expires:  d.now().Add(d.ttl),
		}); err != nil {
			d.logf("dispatch: recover command %d of device %d: %v", command.ID, deviceID, err)
			d.fail(ctx, command.ID)
		}
	}
}

// Run retransmits commands that are still waiting for an answer and gives up on
// the ones that waited too long, until ctx is cancelled.
func (d *Dispatcher) Run(ctx context.Context) {
	d.setContext(ctx)

	interval := d.retry / 4
	if interval < 10*time.Millisecond {
		interval = 10 * time.Millisecond
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			d.sweep(ctx)
		}
	}
}

// sweep applies the delivery policy to every queue once.
func (d *Dispatcher) sweep(ctx context.Context) {
	now := d.now()

	for _, deviceID := range d.deviceIDs() {
		queue := d.queue(deviceID)

		queue.mu.Lock()
		var due []*dispatchEntry
		for _, entry := range queue.entries {
			if !now.Before(entry.next) {
				due = append(due, entry)
			}
		}

		for _, entry := range due {
			// A command that was never written is given up on when its wait is
			// over; one the device already received is given up on when it ran
			// out of attempts, because an answer may still arrive.
			if entry.attempts >= d.attempts ||
				(entry.attempts == 0 && !now.Before(entry.expires)) {
				d.logf("dispatch: give up on command %d of device %d after %d attempts",
					entry.command.ID, deviceID, entry.attempts)
				queue.remove(entry.command.ID)
				d.counted(-1)
				d.state.Remove(entry.command.DeviceID, entry.command.ID)
				d.fail(ctx, entry.command.ID)
				continue
			}
			if _, err := d.write(ctx, deviceID, queue, entry); err != nil {
				d.logf("dispatch: command %d of device %d: %v", entry.command.ID, deviceID, err)
			}
		}
		queue.mu.Unlock()
	}
	d.publishQueued()
}

// Queued reports how many commands are waiting for a device.
func (d *Dispatcher) Queued() int {
	d.countMu.Lock()
	defer d.countMu.Unlock()

	return d.count
}

// QueueFor reports the command ids still queued for one device, oldest first.
// It is what an operator looks at to see what a device is waiting for.
func (d *Dispatcher) QueueFor(deviceID int64) []int64 {
	queue := d.queue(deviceID)

	queue.mu.Lock()
	defer queue.mu.Unlock()

	ids := make([]int64, 0, len(queue.entries))
	for _, entry := range queue.entries {
		ids = append(ids, entry.command.ID)
	}
	return ids
}

// write sends the payload of one entry and moves it to waiting for an answer.
// It must be called with the queue locked. The error is only set when the
// command can never be delivered: an offline device is not a failure yet.
func (d *Dispatcher) write(
	ctx context.Context, deviceID int64, queue *deviceQueue, entry *dispatchEntry,
) (bool, error) {

	err := d.sender.Send(entry.protocol, deviceID, entry.payload)
	if err != nil && errors.Is(err, ErrUnknownProtocol) {
		// No server listens for this protocol, so no reconnect can help.
		queue.remove(entry.command.ID)
		d.counted(-1)
		d.state.Remove(entry.command.DeviceID, entry.command.ID)
		d.fail(ctx, entry.command.ID)
		return false, err
	}

	if err != nil {
		// Offline, or a write that failed on the way out: the device will come
		// back, so the entry stays queued and costs no attempt.
		entry.next = d.now().Add(d.retry)
		return false, nil
	}

	entry.attempts++
	entry.next = d.now().Add(d.retry)
	if entry.attempts == 1 {
		increment(d.sent)
	} else {
		increment(d.retried)
	}

	// The entry changed (attempts, next): refresh the mirror so a restart
	// restores the retry schedule rather than re-writing an answered command.
	d.state.Enqueue(entry.command.DeviceID, entry.command.ID, *entry)

	if entry.attempts == 1 && d.onQueuedSent != nil && !entry.command.CreatedAt.IsZero() &&
		d.now().Sub(entry.command.CreatedAt) > d.retry {
		d.onQueuedSent(deviceID, entry.command.ID)
	}
	if err := d.store.MarkCommandSent(ctx, entry.command.ID); err != nil {
		// The row stays pending and will be written again after a restart. That
		// is a duplicate the device may see, the price of not losing it.
		d.logf("dispatch: mark command %d sent: %v", entry.command.ID, err)
	}
	return true, nil
}

// fail ends a command that will not be executed. The caller has already logged
// why, so this only writes the row and counts the command.
func (d *Dispatcher) fail(ctx context.Context, commandID int64) {
	if err := d.store.MarkCommandFailed(ctx, commandID); err != nil {
		d.logf("dispatch: mark command %d failed: %v", commandID, err)
	}
	increment(d.dropped)
}

// enqueue appends an entry and refuses when the device already holds a full
// queue. An entry for a command that is already queued is dropped, which is what
// keeps a reconnect from queueing a command twice. It must be called with the
// queue locked.
func (d *Dispatcher) enqueue(queue *deviceQueue, entry *dispatchEntry) error {
	if entry.command.ID != 0 {
		for _, existing := range queue.entries {
			if existing.command.ID == entry.command.ID {
				return nil
			}
		}
	}
	if len(queue.entries) >= d.depth {
		return fmt.Errorf("%w: %d commands", ErrCommandQueueFull, len(queue.entries))
	}

	queue.entries = append(queue.entries, entry)
	d.counted(1)

	// Mirror the live bookkeeping so a restart (or another node) can restore
	// the exact timing and queue membership. Best-effort: a store failure never
	// blocks delivery.
	d.state.Enqueue(entry.command.DeviceID, entry.command.ID, *entry)
	return nil
}

// retire drops the entry an answer belongs to: the one with that serial, or the
// newest one that was written and is still waiting.
func (d *Dispatcher) retire(deviceID int64, serial *int) {
	queue := d.queue(deviceID)

	queue.mu.Lock()
	defer queue.mu.Unlock()

	var match *dispatchEntry
	if serial != nil {
		for _, entry := range queue.entries {
			if entry.command.Serial == *serial && entry.attempts > 0 {
				match = entry
			}
		}
	}
	if match == nil {
		for _, entry := range queue.entries {
			if entry.attempts > 0 {
				match = entry
			}
		}
	}
	if match == nil {
		return
	}

	queue.remove(match.command.ID)
	d.counted(-1)
	d.state.Remove(deviceID, match.command.ID)
}

// queue returns the queue of one device, creating an empty one on first use.
// Queues are never dropped: there is at most one per device, and a gateway
// already keeps a session for every device it has seen.
func (d *Dispatcher) queue(deviceID int64) *deviceQueue {
	d.mu.Lock()
	defer d.mu.Unlock()

	queue, ok := d.devices[deviceID]
	if !ok {
		queue = &deviceQueue{}
		d.devices[deviceID] = queue
	}
	return queue
}

func (d *Dispatcher) deviceIDs() []int64 {
	d.mu.Lock()
	defer d.mu.Unlock()

	ids := make([]int64, 0, len(d.devices))
	for deviceID := range d.devices {
		ids = append(ids, deviceID)
	}
	return ids
}

// counted moves the queue size by delta and refreshes the gauge.
func (d *Dispatcher) counted(delta int) {
	d.countMu.Lock()
	d.count += delta
	count := d.count
	d.countMu.Unlock()

	if d.queued != nil {
		d.queued.Set(int64(count))
	}
}

func (d *Dispatcher) publishQueued() {
	if d.queued != nil {
		d.queued.Set(int64(d.Queued()))
	}
}

func (d *Dispatcher) setContext(ctx context.Context) {
	d.mu.Lock()
	defer d.mu.Unlock()

	if ctx != nil {
		d.ctx = ctx
	}
}

// context is the one Run was started with, so the work a reconnect triggers
// stops with the gateway.
func (d *Dispatcher) context() context.Context {
	d.mu.Lock()
	defer d.mu.Unlock()

	return d.ctx
}

func increment(counter *metrics.Counter) {
	if counter != nil {
		counter.Inc()
	}
}

// remove drops the entry of a command. It must be called with the queue locked.
func (q *deviceQueue) remove(commandID int64) {
	for i, entry := range q.entries {
		if entry.command.ID == commandID {
			q.entries = append(q.entries[:i], q.entries[i+1:]...)
			return
		}
	}
}

var _ CommandTracker = (*Dispatcher)(nil)
