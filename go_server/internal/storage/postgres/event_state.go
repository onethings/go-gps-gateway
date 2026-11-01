package postgres

import (
	"context"
	"fmt"

	"traccar-server_go/go_server/internal/model"
)

// DeviceMaintenances returns the maintenance intervals linked to each device,
// which is what the maintenance event rule needs. The pairs live in the generic
// permissions table, the same way the Java server stores them in its device /
// object link tables.
func (s *Store) DeviceMaintenances(ctx context.Context) (map[int64][]*model.Maintenance, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT p.owner_id, m.id, m.name, m.type, m.start, m.period, m.attributes
		 FROM permissions p
		 JOIN maintenances m ON m.id = p.property_id
		 WHERE p.owner_type = 'device' AND p.property_type = 'maintenance'
		 ORDER BY p.owner_id, m.id`)
	if err != nil {
		return nil, fmt.Errorf("postgres: query device maintenances: %w", err)
	}
	defer rows.Close()

	result := make(map[int64][]*model.Maintenance)
	for rows.Next() {
		var (
			deviceID    int64
			maintenance model.Maintenance
			attributes  []byte
		)
		if err := rows.Scan(&deviceID, &maintenance.ID, &maintenance.Name, &maintenance.Type,
			&maintenance.Start, &maintenance.Period, &attributes); err != nil {
			return nil, fmt.Errorf("postgres: scan device maintenance: %w", err)
		}
		decoded, err := decodeAttributes(attributes)
		if err != nil {
			return nil, fmt.Errorf("postgres: maintenance %d: %w", maintenance.ID, err)
		}
		maintenance.Attributes = decoded
		result[deviceID] = append(result[deviceID], &maintenance)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("postgres: query device maintenances: %w", err)
	}
	return result, nil
}

// DeviceLinks returns the devices linked to each device, which the proximity
// rule turns into enter / exit events. Links are stored as a device to device
// permission pair.
func (s *Store) DeviceLinks(ctx context.Context) (map[int64][]int64, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT owner_id, property_id FROM permissions
		 WHERE owner_type = 'device' AND property_type = 'device'
		 ORDER BY owner_id, property_id`)
	if err != nil {
		return nil, fmt.Errorf("postgres: query device links: %w", err)
	}
	defer rows.Close()

	result := make(map[int64][]int64)
	for rows.Next() {
		var ownerID, propertyID int64
		if err := rows.Scan(&ownerID, &propertyID); err != nil {
			return nil, fmt.Errorf("postgres: scan device link: %w", err)
		}
		result[ownerID] = append(result[ownerID], propertyID)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("postgres: query device links: %w", err)
	}
	return result, nil
}
