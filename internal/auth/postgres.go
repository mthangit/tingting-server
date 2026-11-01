package auth

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// PGStore lưu dữ liệu đăng nhập trong Postgres.
type PGStore struct {
	DB *pgxpool.Pool
}

func (s PGStore) SignIn(ctx context.Context, id Identity, device Device, refreshHash string, refreshExpires time.Time) (User, error) {
	var user User
	err := pgx.BeginFunc(ctx, s.DB, func(tx pgx.Tx) error {
		// Hai lần đăng nhập đầu tiên chạy cùng lúc: ON CONFLICT để chỉ một user được tạo.
		err := tx.QueryRow(ctx,
			`SELECT user_id FROM user_identities WHERE provider = $1 AND subject = $2`,
			id.Provider, id.Subject).Scan(&user.ID)
		if errors.Is(err, pgx.ErrNoRows) {
			var created uuid.UUID
			if err := tx.QueryRow(ctx, `INSERT INTO users DEFAULT VALUES RETURNING id`).Scan(&created); err != nil {
				return fmt.Errorf("create user: %w", err)
			}
			err = tx.QueryRow(ctx,
				`INSERT INTO user_identities (provider, subject, user_id, email) VALUES ($1, $2, $3, $4)
				 ON CONFLICT (provider, subject) DO UPDATE SET email = user_identities.email
				 RETURNING user_id`,
				id.Provider, id.Subject, created, id.Email).Scan(&user.ID)
			if err != nil {
				return fmt.Errorf("create identity: %w", err)
			}
			if user.ID != created {
				if _, err := tx.Exec(ctx, `DELETE FROM users WHERE id = $1`, created); err != nil {
					return err
				}
			}
		} else if err != nil {
			return fmt.Errorf("find identity: %w", err)
		} else if id.Email != "" {
			if _, err := tx.Exec(ctx, `UPDATE user_identities SET email = $3 WHERE provider = $1 AND subject = $2`,
				id.Provider, id.Subject, id.Email); err != nil {
				return err
			}
		}

		var deleted *time.Time
		if err := tx.QueryRow(ctx, `SELECT deleted_at FROM users WHERE id = $1`, user.ID).Scan(&deleted); err != nil {
			return err
		}
		if deleted != nil {
			return ErrUserNotFound
		}
		user.Email = id.Email

		if _, err := tx.Exec(ctx,
			`INSERT INTO devices (user_id, id, platform, app_version) VALUES ($1, $2, $3, $4)
			 ON CONFLICT (user_id, id) DO UPDATE
			 SET platform = EXCLUDED.platform, app_version = EXCLUDED.app_version, last_seen_at = now()`,
			user.ID, device.ID, device.Platform, device.AppVersion); err != nil {
			return fmt.Errorf("upsert device: %w", err)
		}
		_, err = tx.Exec(ctx,
			`INSERT INTO refresh_tokens (token_hash, user_id, device_id, expires_at) VALUES ($1, $2, $3, $4)`,
			refreshHash, user.ID, device.ID, refreshExpires)
		return err
	})
	return user, err
}

func (s PGStore) Rotate(ctx context.Context, oldHash, newHash string, newExpires, now time.Time) (Session, error) {
	var session Session
	var reused bool
	err := pgx.BeginFunc(ctx, s.DB, func(tx pgx.Tx) error {
		var revokedAt *time.Time
		var expiresAt time.Time
		err := tx.QueryRow(ctx,
			`SELECT user_id, device_id, revoked_at, expires_at FROM refresh_tokens WHERE token_hash = $1 FOR UPDATE`,
			oldHash).Scan(&session.UserID, &session.DeviceID, &revokedAt, &expiresAt)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrRefreshInvalid
		}
		if err != nil {
			return err
		}
		if revokedAt != nil {
			reused = true
			_, err := tx.Exec(ctx,
				`UPDATE refresh_tokens SET revoked_at = $3 WHERE user_id = $1 AND device_id = $2 AND revoked_at IS NULL`,
				session.UserID, session.DeviceID, now)
			return err
		}
		if !expiresAt.After(now) {
			return ErrRefreshInvalid
		}
		if _, err := tx.Exec(ctx, `UPDATE refresh_tokens SET revoked_at = $2 WHERE token_hash = $1`, oldHash, now); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx,
			`INSERT INTO refresh_tokens (token_hash, user_id, device_id, expires_at) VALUES ($1, $2, $3, $4)`,
			newHash, session.UserID, session.DeviceID, newExpires); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `UPDATE devices SET last_seen_at = $3 WHERE user_id = $1 AND id = $2`,
			session.UserID, session.DeviceID, now)
		return err
	})
	if err != nil {
		return Session{}, err
	}
	if reused {
		return Session{}, ErrRefreshReused
	}
	return session, nil
}

func (s PGStore) Revoke(ctx context.Context, hash string, now time.Time) error {
	_, err := s.DB.Exec(ctx,
		`UPDATE refresh_tokens SET revoked_at = $2 WHERE token_hash = $1 AND revoked_at IS NULL`, hash, now)
	return err
}

func (s PGStore) User(ctx context.Context, id uuid.UUID) (User, error) {
	user := User{ID: id}
	err := s.DB.QueryRow(ctx,
		`SELECT COALESCE((SELECT email FROM user_identities WHERE user_id = u.id AND email <> ''
		                  ORDER BY created_at LIMIT 1), '')
		 FROM users u WHERE u.id = $1 AND u.deleted_at IS NULL`, id).Scan(&user.Email)
	if errors.Is(err, pgx.ErrNoRows) {
		return User{}, ErrUserNotFound
	}
	return user, err
}
