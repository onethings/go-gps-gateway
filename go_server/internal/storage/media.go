package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"traccar-server_go/go_server/internal/model"
)

const mediaColumns = `id, device_id, file_name, type, size, media_time, attributes`

// CreateMediaFile inserts one metadata row.
func (s *Store) CreateMediaFile(ctx context.Context, file *model.MediaFile) error {
	if file == nil {
		return nil
	}
	attributes, err := encodeAttributes(file.Attributes)
	if err != nil {
		return err
	}
	mediaTime := file.MediaTime
	if mediaTime.IsZero() {
		mediaTime = time.Now()
	}
	id, err := insertReturningID(ctx, s.db,
		`INSERT INTO media (device_id, file_name, type, size, media_time, attributes)
		 VALUES ($1, $2, $3, $4, $5, $6) RETURNING id`,
		file.DeviceID, file.FileName, file.Type, file.Size, mediaTime.UTC(), attributes)
	if err != nil {
		return fmt.Errorf("storage: create media file: %w", err)
	}
	file.ID = id
	file.MediaTime = mediaTime.UTC()
	return nil
}

// MediaFile reads one row by id.
func (s *Store) MediaFile(ctx context.Context, id int64) (*model.MediaFile, error) {
	row := s.db.QueryRowContext(ctx,
		"SELECT "+mediaColumns+" FROM media WHERE id = $1", id)
	file, err := scanMediaFile(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, model.ErrMediaFileNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("storage: query media file %d: %w", id, err)
	}
	return file, nil
}

// MediaFiles lists the files of one device inside a period, newest first.
func (s *Store) MediaFiles(
	ctx context.Context, deviceID int64, from, to time.Time,
) ([]model.MediaFile, error) {
	rows, err := s.db.QueryContext(ctx,
		"SELECT "+mediaColumns+` FROM media
		 WHERE device_id = $1 AND media_time >= $2 AND media_time <= $3
		 ORDER BY media_time DESC, id DESC`,
		deviceID, from.UTC(), to.UTC())
	if err != nil {
		return nil, fmt.Errorf("storage: query media: %w", err)
	}
	defer rows.Close()

	var files []model.MediaFile
	for rows.Next() {
		file, err := scanMediaFile(rows)
		if err != nil {
			return nil, err
		}
		files = append(files, *file)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("storage: query media: %w", err)
	}
	return files, nil
}

// DeleteMediaFile removes one metadata row.
func (s *Store) DeleteMediaFile(ctx context.Context, id int64) error {
	result, err := s.db.ExecContext(ctx, `DELETE FROM media WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("storage: delete media file %d: %w", id, err)
	}
	deleted, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("storage: delete media file %d: %w", id, err)
	}
	if deleted == 0 {
		return model.ErrMediaFileNotFound
	}
	return nil
}

func scanMediaFile(row interface{ Scan(dest ...any) error }) (*model.MediaFile, error) {
	var (
		file       model.MediaFile
		attributes []byte
	)
	if err := row.Scan(&file.ID, &file.DeviceID, &file.FileName, &file.Type,
		&file.Size, &file.MediaTime, &attributes); err != nil {
		return nil, err
	}
	decoded, err := decodeAttributes(attributes)
	if err != nil {
		return nil, err
	}
	file.Attributes = decoded
	return &file, nil
}
