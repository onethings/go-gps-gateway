package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"traccar-server_go/go_server/internal/model"
)

// serverColumns is the SELECT and RETURNING list of server_settings. The order
// is the scan order in scanServerSettings.
const serverColumns = `id, registration, readonly, device_readonly, limit_commands,
	disable_reports, fixed_email, force_settings, announcement, map, map_url,
	overlay_url, bing_key, poi_layer, coordinate_format, latitude, longitude,
	zoom, attributes`

// ServerSettings returns the single settings row, creating it when the
// migration left the table empty.
func (s *Store) ServerSettings(ctx context.Context) (*model.Server, error) {
	settings, err := scanServerSettings(s.db.QueryRowContext(ctx,
		"SELECT "+serverColumns+" FROM server_settings ORDER BY id LIMIT 1"))
	if errors.Is(err, sql.ErrNoRows) {
		if _, insertErr := s.db.ExecContext(ctx,
			`INSERT INTO server_settings DEFAULT VALUES`); insertErr != nil {
			return nil, fmt.Errorf("postgres: create server settings: %w", insertErr)
		}
		return scanServerSettings(s.db.QueryRowContext(ctx,
			"SELECT "+serverColumns+" FROM server_settings ORDER BY id LIMIT 1"))
	}
	if err != nil {
		return nil, fmt.Errorf("postgres: query server settings: %w", err)
	}
	return settings, nil
}

// UpdateServerSettings replaces every editable column of the settings row.
func (s *Store) UpdateServerSettings(ctx context.Context, settings *model.Server) (*model.Server, error) {
	if settings == nil {
		return nil, errors.New("postgres: nil server settings")
	}
	attributes, err := encodeAttributes(settings.Attributes)
	if err != nil {
		return nil, err
	}
	id := settings.ID
	if id == 0 {
		id = 1
	}
	updated, err := scanServerSettings(s.db.QueryRowContext(ctx,
		`UPDATE server_settings SET
		     registration = $2, readonly = $3, device_readonly = $4,
		     limit_commands = $5, disable_reports = $6, fixed_email = $7,
		     force_settings = $8, announcement = $9, map = $10, map_url = $11,
		     overlay_url = $12, bing_key = $13, poi_layer = $14,
		     coordinate_format = $15, latitude = $16, longitude = $17,
		     zoom = $18, attributes = $19
		 WHERE id = $1
		 RETURNING `+serverColumns,
		id, settings.Registration, settings.Readonly, settings.DeviceReadonly,
		settings.LimitCommands, settings.DisableReports, settings.FixedEmail,
		settings.ForceSettings, settings.Announcement, settings.Map, settings.MapURL,
		settings.OverlayURL, settings.BingKey, settings.PoiLayer,
		settings.CoordinateFormat, settings.Latitude, settings.Longitude,
		settings.Zoom, attributes,
	))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, model.ErrResourceNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("postgres: update server settings: %w", err)
	}
	return updated, nil
}

func scanServerSettings(row interface{ Scan(dest ...any) error }) (*model.Server, error) {
	var (
		settings   model.Server
		attributes []byte
	)
	if err := row.Scan(
		&settings.ID, &settings.Registration, &settings.Readonly, &settings.DeviceReadonly,
		&settings.LimitCommands, &settings.DisableReports, &settings.FixedEmail,
		&settings.ForceSettings, &settings.Announcement, &settings.Map, &settings.MapURL,
		&settings.OverlayURL, &settings.BingKey, &settings.PoiLayer,
		&settings.CoordinateFormat, &settings.Latitude, &settings.Longitude,
		&settings.Zoom, &attributes,
	); err != nil {
		return nil, err
	}
	decoded, err := decodeAttributes(attributes)
	if err != nil {
		return nil, fmt.Errorf("postgres: server settings: %w", err)
	}
	settings.Attributes = decoded
	return &settings, nil
}
