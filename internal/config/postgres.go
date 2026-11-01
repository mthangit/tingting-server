package config

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// PGStore lưu config trong bảng configs.
type PGStore struct {
	DB *pgxpool.Pool
}

func (s PGStore) Active(ctx context.Context) (Config, error) {
	var c Config
	err := s.DB.QueryRow(ctx,
		`SELECT version, schema_version, payload FROM configs WHERE is_active`).
		Scan(&c.Version, &c.SchemaVersion, &c.Payload)
	if errors.Is(err, pgx.ErrNoRows) {
		return Config{}, ErrNoActiveConfig
	}
	if err != nil {
		return Config{}, fmt.Errorf("load active config: %w", err)
	}
	return c, nil
}

// Publish lưu một version mới (chưa bật). Gọi Validate trước khi lưu.
func (s PGStore) Publish(ctx context.Context, payload json.RawMessage, note string) (int64, error) {
	var version int64
	err := s.DB.QueryRow(ctx,
		`INSERT INTO configs (schema_version, payload, note) VALUES ($1, $2, $3) RETURNING version`,
		SchemaVersion, payload, note).Scan(&version)
	if err != nil {
		return 0, fmt.Errorf("insert config: %w", err)
	}
	return version, nil
}

// Activate bật một version và tắt version đang bật, trong cùng một transaction.
func (s PGStore) Activate(ctx context.Context, version int64) error {
	return pgx.BeginFunc(ctx, s.DB, func(tx pgx.Tx) error {
		var exists bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM configs WHERE version = $1)`, version).Scan(&exists); err != nil {
			return err
		}
		if !exists {
			return fmt.Errorf("không có config version %d", version)
		}
		if _, err := tx.Exec(ctx, `UPDATE configs SET is_active = false WHERE is_active`); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `UPDATE configs SET is_active = true WHERE version = $1`, version)
		return err
	})
}

// Version là một dòng trong lịch sử config.
type Version struct {
	Version   int64
	Note      string
	CreatedAt time.Time
	IsActive  bool
}

func (s PGStore) List(ctx context.Context, limit int) ([]Version, error) {
	rows, err := s.DB.Query(ctx,
		`SELECT version, note, created_at, is_active FROM configs ORDER BY version DESC LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (Version, error) {
		var v Version
		err := row.Scan(&v.Version, &v.Note, &v.CreatedAt, &v.IsActive)
		return v, err
	})
}
