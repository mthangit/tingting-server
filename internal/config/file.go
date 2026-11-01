package config

import (
	"context"
	"fmt"
	"os"
)

// FileStore đọc config từ một file JSON, dùng khi chạy trên máy không có Postgres.
// Version luôn là 0; ETag vẫn đổi theo nội dung file.
type FileStore struct {
	Path         string
	AllowedHosts []string
	LogoHosts    []string
}

func (s FileStore) Active(ctx context.Context) (Config, error) {
	raw, err := os.ReadFile(s.Path)
	if err != nil {
		return Config{}, fmt.Errorf("read %s: %w", s.Path, err)
	}
	if err := Validate(raw, s.AllowedHosts, s.LogoHosts); err != nil {
		return Config{}, err
	}
	return Config{Version: 0, SchemaVersion: SchemaVersion, Payload: raw}, nil
}
