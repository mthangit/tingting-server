package auth

import (
	"context"
	"errors"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/mthangit/tingting-server/internal/db"
)

// Chạy với Postgres thật: TING_TEST_DATABASE_URL=postgres://… go test ./internal/auth
func pgStore(t *testing.T) PGStore {
	t.Helper()
	url := os.Getenv("TING_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TING_TEST_DATABASE_URL not set")
	}
	pool, err := db.Open(context.Background(), url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	return PGStore{DB: pool}
}

func TestPGSignInRotateRevoke(t *testing.T) {
	store := pgStore(t)
	ctx := context.Background()
	now := time.Now()
	id := Identity{Provider: "google", Subject: "pg-" + uuid.NewString(), Email: "pg@example.com"}
	device := Device{ID: uuid.New(), Platform: "ios", AppVersion: "0.2.0"}

	user, err := store.SignIn(ctx, id, device, "h1-"+id.Subject, now.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	again, err := store.SignIn(ctx, id, device, "h2-"+id.Subject, now.Add(time.Hour))
	if err != nil || again.ID != user.ID {
		t.Fatalf("second sign in: %v %v", again, err)
	}
	if got, err := store.User(ctx, user.ID); err != nil || got.Email != "pg@example.com" {
		t.Fatalf("user: %+v %v", got, err)
	}

	session, err := store.Rotate(ctx, "h1-"+id.Subject, "h3-"+id.Subject, now.Add(time.Hour), now)
	if err != nil || session.UserID != user.ID || session.DeviceID != device.ID {
		t.Fatalf("rotate: %+v %v", session, err)
	}
	if _, err := store.Rotate(ctx, "h1-"+id.Subject, "h4-"+id.Subject, now.Add(time.Hour), now); !errors.Is(err, ErrRefreshReused) {
		t.Fatalf("reuse: %v", err)
	}
	if _, err := store.Rotate(ctx, "h3-"+id.Subject, "h5-"+id.Subject, now.Add(time.Hour), now); !errors.Is(err, ErrRefreshReused) {
		t.Fatalf("token issued before reuse should be revoked: %v", err)
	}
	if _, err := store.Rotate(ctx, "missing", "h6", now.Add(time.Hour), now); !errors.Is(err, ErrRefreshInvalid) {
		t.Fatalf("missing: %v", err)
	}
}

func TestPGConcurrentFirstSignInCreatesOneUser(t *testing.T) {
	store := pgStore(t)
	ctx := context.Background()
	id := Identity{Provider: "google", Subject: "race-" + uuid.NewString()}

	var wg sync.WaitGroup
	ids := make([]uuid.UUID, 5)
	errs := make([]error, 5)
	for i := range ids {
		wg.Add(1)
		go func() {
			defer wg.Done()
			user, err := store.SignIn(ctx, id, Device{ID: uuid.New()}, uuid.NewString(), time.Now().Add(time.Hour))
			ids[i], errs[i] = user.ID, err
		}()
	}
	wg.Wait()
	for i := range ids {
		if errs[i] != nil {
			t.Fatal(errs[i])
		}
		if ids[i] != ids[0] {
			t.Fatalf("concurrent sign ins created different users: %v", ids)
		}
	}
}
