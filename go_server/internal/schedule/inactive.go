package schedule

import (
	"context"
	"fmt"
	"sync"
	"time"

	"traccar-server_go/go_server/internal/model"
)

// InactiveStore is the part of the storage the inactive device task needs.
type InactiveStore interface {
	Devices(ctx context.Context, filter model.DeviceFilter) ([]model.Device, error)
	LatestPositions(ctx context.Context, deviceIDs []int64) ([]model.Position, error)
	InsertEvents(ctx context.Context, events []*model.Event) error
}

// DeviceInactive raises one deviceInactive event when a device has not reported
// within the timeout. The Java server raises it from a scheduled task too; a
// device that starts reporting again is forgotten, so the next silence raises a
// new event.
type DeviceInactive struct {
	Store InactiveStore
	// Timeout is how long a device may stay silent. Zero disables the task.
	Timeout time.Duration
	Now     func() time.Time
	// OnEvent reports a raised event, so the caller can publish it on the live
	// socket as well.
	OnEvent func(event *model.Event)

	mu       sync.Mutex
	notified map[int64]time.Time
}

// Name implements Task.
func (d *DeviceInactive) Name() string { return "deviceInactive" }

// Run implements Task.
func (d *DeviceInactive) Run(ctx context.Context) error {
	if d.Store == nil || d.Timeout <= 0 {
		return nil
	}
	now := time.Now()
	if d.Now != nil {
		now = d.Now()
	}

	devices, err := listAllDevices(ctx, d.Store)
	if err != nil {
		return err
	}
	ids := make([]int64, 0, len(devices))
	for _, device := range devices {
		ids = append(ids, device.ID)
	}
	positions, err := d.Store.LatestPositions(ctx, ids)
	if err != nil {
		return err
	}
	lastSeen := make(map[int64]time.Time, len(devices))
	for _, device := range devices {
		lastSeen[device.ID] = device.LastUpdate
	}
	for _, position := range positions {
		last := position.Time
		if last.IsZero() {
			last = position.ServerTime
		}
		if last.After(lastSeen[position.DeviceID]) {
			lastSeen[position.DeviceID] = last
		}
	}

	var events []*model.Event
	d.mu.Lock()
	if d.notified == nil {
		d.notified = make(map[int64]time.Time)
	}
	for _, device := range devices {
		last := lastSeen[device.ID]
		if last.IsZero() {
			// A device that never reported is unknown, not inactive.
			continue
		}
		if now.Sub(last) <= d.Timeout {
			delete(d.notified, device.ID)
			continue
		}
		if previous, ok := d.notified[device.ID]; ok && previous.Equal(last) {
			continue
		}
		d.notified[device.ID] = last
		event := &model.Event{
			DeviceID: device.ID,
			Type:     model.EventDeviceInactive,
			Time:     now,
		}
		event.Set("lastUpdate", last.UTC().Format(time.RFC3339))
		events = append(events, event)
	}
	d.mu.Unlock()

	if len(events) == 0 {
		return nil
	}
	if err := d.Store.InsertEvents(ctx, events); err != nil {
		return fmt.Errorf("schedule: insert inactive events: %w", err)
	}
	if d.OnEvent != nil {
		for _, event := range events {
			d.OnEvent(event)
		}
	}
	return nil
}

// listAllDevices reads every device in pages; the device listing is capped.
func listAllDevices(ctx context.Context, store InactiveStore) ([]model.Device, error) {
	const page = 1000
	var devices []model.Device
	for offset := 0; ; offset += page {
		batch, err := store.Devices(ctx, model.DeviceFilter{Limit: page, Offset: offset})
		if err != nil {
			return nil, err
		}
		devices = append(devices, batch...)
		if len(batch) < page {
			return devices, nil
		}
	}
}
