package config

import (
	"context"
	"errors"
	"sync"
	"time"
)

// Cached giữ config đang bật trong bộ nhớ, để mỗi lần gọi bootstrap không phải chạm database.
// Config mới được bật sẽ có hiệu lực sau tối đa TTL.
type Cached struct {
	Store Store
	TTL   time.Duration
	Now   func() time.Time

	mu       sync.Mutex
	value    Config
	err      error
	loadedAt time.Time
}

func (c *Cached) Active(ctx context.Context) (Config, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	now := c.now()
	if !c.loadedAt.IsZero() && now.Sub(c.loadedAt) < c.TTL {
		return c.value, c.err
	}

	value, err := c.Store.Active(ctx)
	if err != nil && !errors.Is(err, ErrNoActiveConfig) {
		// Database tạm lỗi: trả bản tốt gần nhất nếu có, không lưu lỗi lại để lần sau thử tiếp.
		if !c.loadedAt.IsZero() && c.err == nil {
			return c.value, nil
		}
		return Config{}, err
	}
	c.value, c.err, c.loadedAt = value, err, now
	return value, err
}

func (c *Cached) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}
