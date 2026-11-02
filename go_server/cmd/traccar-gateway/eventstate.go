package main

import (
	"context"
	"log"
	"sync"
	"time"

	"traccar-server_go/go_server/internal/model"
	"traccar-server_go/go_server/internal/storage/postgres"
)

// eventState is the per-device state the M4-1 event rules need beyond the
// position itself: the maintenance intervals linked to a device, the devices
// linked for a proximity check and the newest position of each linked device.
// It is refreshed periodically and kept current by the position hook, so a rule
// never queries the database on the ingest path.
type eventState struct {
	mu           sync.RWMutex
	maintenances map[int64][]*model.Maintenance
	links        map[int64][]int64
	last         map[int64]*model.Position
	// devices is the row each position and event is rendered with by the
	// forwarders, so forwarding never queries the database.
	devices map[int64]*model.Device
}

func newEventState() *eventState {
	return &eventState{
		maintenances: make(map[int64][]*model.Maintenance),
		links:        make(map[int64][]int64),
		last:         make(map[int64]*model.Position),
		devices:      make(map[int64]*model.Device),
	}
}

// Maintenances returns the intervals linked to a device.
func (s *eventState) Maintenances(deviceID int64) []*model.Maintenance {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.maintenances[deviceID]
}

// LinkedDevices returns the devices linked to a device.
func (s *eventState) LinkedDevices(deviceID int64) []int64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.links[deviceID]
}

// LastPosition returns the newest position of a device.
func (s *eventState) LastPosition(deviceID int64) *model.Position {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.last[deviceID]
}

// Device returns the cached device row. It is nil safe so a hook can call it
// before the first reload.
func (s *eventState) Device(deviceID int64) *model.Device {
	if s == nil {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.devices[deviceID]
}

// setPosition keeps the newest position, which the proximity rule compares the
// linked devices against and which the distance and filter stages use as the
// previous position. An older row never replaces a newer one, so a periodic
// reload cannot rewind the map the ingest path is updating.
func (s *eventState) setPosition(position *model.Position) {
	if position == nil || position.DeviceID == 0 {
		return
	}
	copied := *position
	s.mu.Lock()
	if previous, ok := s.last[copied.DeviceID]; ok && copied.Time.Before(previous.Time) {
		s.mu.Unlock()
		return
	}
	s.last[copied.DeviceID] = &copied
	s.mu.Unlock()
}

// reload refreshes the links, the maintenance intervals and the last position
// of every linked device from the database.
func (s *eventState) reload(ctx context.Context, store *postgres.Store) error {
	maintenances, err := store.DeviceMaintenances(ctx)
	if err != nil {
		return err
	}
	links, err := store.DeviceLinks(ctx)
	if err != nil {
		return err
	}
	devices, err := loadDevices(ctx, store)
	if err != nil {
		return err
	}

	s.mu.Lock()
	s.maintenances = maintenances
	s.links = links
	s.devices = devices
	s.mu.Unlock()

	// Seed the last position of every linked device, so a proximity check
	// right after a restart has something to compare with. The refresh must not
	// fail on a large fleet, so an error only skips the seeding.
	ids := make([]int64, 0, len(links))
	for _, linked := range links {
		ids = append(ids, linked...)
	}
	if len(ids) == 0 {
		return nil
	}
	positions, err := store.LatestPositions(ctx, ids)
	if err != nil {
		return err
	}
	for index := range positions {
		s.setPosition(&positions[index])
	}
	return nil
}

// loadDevices reads every device in pages, because the device listing is
// capped at a thousand rows per query.
func loadDevices(ctx context.Context, store *postgres.Store) (map[int64]*model.Device, error) {
	const page = 1000
	devices := make(map[int64]*model.Device)
	for offset := 0; ; offset += page {
		batch, err := store.Devices(ctx, model.DeviceFilter{Limit: page, Offset: offset})
		if err != nil {
			return nil, err
		}
		for index := range batch {
			devices[batch[index].ID] = &batch[index]
		}
		if len(batch) < page {
			return devices, nil
		}
	}
}

// seedLastPositions loads the newest stored position of every device, so the
// distance and filter stages have a previous position to compare against right
// after a restart. Without it the first position of a device would start a new
// trip and reset its running total.
func (s *eventState) seedLastPositions(ctx context.Context, store *postgres.Store) error {
	positions, err := store.LatestPositions(ctx, nil)
	if err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	for index := range positions {
		position := positions[index]
		if _, ok := s.last[position.DeviceID]; ok {
			continue
		}
		copied := position
		s.last[copied.DeviceID] = &copied
	}
	return nil
}

// runEventStateRefresh keeps the links and maintenance intervals current. A
// failure is logged and the previous snapshot stays in place, so a database
// blip does not disable the rules.
func runEventStateRefresh(ctx context.Context, state *eventState, store *postgres.Store, interval time.Duration) {
	if interval <= 0 {
		return
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := state.reload(ctx, store); err != nil {
				log.Printf("events: refresh state: %v", err)
			}
		}
	}
}
