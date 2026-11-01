package db

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

// Nhiều tiến trình mở cùng một database mới (hai instance Cloud Run, các package test chạy song song)
// không được làm hỏng migration. Cần Postgres thật và quyền tạo database:
// TING_TEST_DATABASE_URL=postgres://… go test ./internal/db
func TestConcurrentOpenMigratesOnce(t *testing.T) {
	base := os.Getenv("TING_TEST_DATABASE_URL")
	if base == "" {
		t.Skip("TING_TEST_DATABASE_URL not set")
	}
	ctx := context.Background()

	admin, err := pgx.Connect(ctx, base)
	if err != nil {
		t.Fatal(err)
	}
	// Cleanup chạy sau khi hàm test trả về (và theo thứ tự ngược), nên đóng kết nối admin ở Cleanup chứ không
	// dùng defer, để lệnh DROP bên dưới còn kết nối mà chạy.
	t.Cleanup(func() { _ = admin.Close(context.Background()) })
	name := fmt.Sprintf("ting_migrate_%d", time.Now().UnixNano())
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+name); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := admin.Exec(context.Background(), "DROP DATABASE IF EXISTS "+name+" WITH (FORCE)"); err != nil {
			t.Errorf("drop %s: %v", name, err)
		}
	})

	u, err := url.Parse(base)
	if err != nil {
		t.Fatal(err)
	}
	u.Path = "/" + name

	const openers = 8
	var wg sync.WaitGroup
	errs := make(chan error, openers)
	start := make(chan struct{})
	for range openers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			pool, err := Open(ctx, u.String())
			if err != nil {
				errs <- err
				return
			}
			pool.Close()
		}()
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}

	pool, err := Open(ctx, u.String())
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	var applied int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM schema_migrations`).Scan(&applied); err != nil {
		t.Fatal(err)
	}
	files, _ := migrations.ReadDir("migrations")
	if applied != len(files) {
		t.Fatalf("applied %d migrations, want %d", applied, len(files))
	}
}
