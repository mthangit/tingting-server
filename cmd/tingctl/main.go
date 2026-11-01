// tingctl quản lý config của app trong database.
//
//	tingctl validate <file.json>                 kiểm tra file config, không cần database
//	tingctl publish  <file.json> [ghi chú]       lưu thành version mới (chưa bật)
//	tingctl activate <version>                   bật một version (dùng để rollback)
//	tingctl list                                 xem các version gần đây
//
// Cần DATABASE_URL (trừ validate). TING_ALLOWED_API_HOSTS, TING_ALLOWED_LOGO_HOSTS giống server.
package main

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/mthangit/tingting-server/internal/config"
	"github.com/mthangit/tingting-server/internal/db"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		return usage()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	allowedHosts := strings.Split(env("TING_ALLOWED_API_HOSTS", "*.run.app,localhost,127.0.0.1"), ",")
	logoHosts := strings.Split(env("TING_ALLOWED_LOGO_HOSTS", "*.r2.dev"), ",")

	switch cmd := args[0]; cmd {
	case "validate":
		if len(args) != 2 {
			return usage()
		}
		raw, err := readValid(args[1], allowedHosts, logoHosts)
		if err != nil {
			return err
		}
		fmt.Printf("OK (%d byte)\n", len(raw))
		return nil

	case "publish":
		if len(args) < 2 {
			return usage()
		}
		raw, err := readValid(args[1], allowedHosts, logoHosts)
		if err != nil {
			return err
		}
		store, closeDB, err := openStore(ctx)
		if err != nil {
			return err
		}
		defer closeDB()
		version, err := store.Publish(ctx, raw, strings.Join(args[2:], " "))
		if err != nil {
			return err
		}
		fmt.Printf("Đã lưu version %d (chưa bật). Bật bằng: tingctl activate %d\n", version, version)
		return nil

	case "activate":
		if len(args) != 2 {
			return usage()
		}
		version, err := strconv.ParseInt(args[1], 10, 64)
		if err != nil {
			return fmt.Errorf("version phải là số: %w", err)
		}
		store, closeDB, err := openStore(ctx)
		if err != nil {
			return err
		}
		defer closeDB()
		if err := store.Activate(ctx, version); err != nil {
			return err
		}
		fmt.Printf("Đã bật version %d. Server áp dụng trong khoảng 30 giây.\n", version)
		return nil

	case "list":
		store, closeDB, err := openStore(ctx)
		if err != nil {
			return err
		}
		defer closeDB()
		versions, err := store.List(ctx, 20)
		if err != nil {
			return err
		}
		for _, v := range versions {
			mark := " "
			if v.IsActive {
				mark = "*"
			}
			fmt.Printf("%s %4d  %s  %s\n", mark, v.Version, v.CreatedAt.Local().Format("2006-01-02 15:04"), v.Note)
		}
		return nil

	default:
		return usage()
	}
}

func readValid(path string, allowedHosts, logoHosts []string) ([]byte, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if err := config.Validate(raw, allowedHosts, logoHosts); err != nil {
		return nil, err
	}
	return raw, nil
}

func openStore(ctx context.Context) (config.PGStore, func(), error) {
	url := os.Getenv("DATABASE_URL")
	if url == "" {
		return config.PGStore{}, nil, fmt.Errorf("cần DATABASE_URL")
	}
	pool, err := db.Open(ctx, url)
	if err != nil {
		return config.PGStore{}, nil, err
	}
	return config.PGStore{DB: pool}, pool.Close, nil
}

func usage() error {
	return fmt.Errorf(`cách dùng:
  tingctl validate <file.json>
  tingctl publish  <file.json> [ghi chú]
  tingctl activate <version>
  tingctl list`)
}

func env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
