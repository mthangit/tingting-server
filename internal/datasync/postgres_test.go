package datasync

import (
	"context"
	"os"
	gosync "sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/mthangit/tingting-server/internal/db"
)

// Chạy với Postgres thật: TING_TEST_DATABASE_URL=postgres://… go test ./internal/datasync
func pgStore(t *testing.T) (PGStore, uuid.UUID) {
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
	return PGStore{DB: pool}, newUser(t, PGStore{DB: pool})
}

func newUser(t *testing.T, s PGStore) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	if err := s.DB.QueryRow(context.Background(), `INSERT INTO users DEFAULT VALUES RETURNING id`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func pullAll(t *testing.T, s PGStore, user uuid.UUID, cursor int64) (Changes, int64) {
	t.Helper()
	changes, next, _, err := s.Pull(context.Background(), user, cursor, 1000)
	if err != nil {
		t.Fatal(err)
	}
	return changes, next
}

func TestPGPushLastWriteWinsAndPullByCursor(t *testing.T) {
	store, user := pgStore(t)
	ctx := context.Background()

	tx := validTransaction()
	tag := Tag{ID: uuid.New(), Name: "Ăn uống", Kind: "expense", Color: "orange", CreatedAt: at(now), UpdatedAt: at(now)}
	source := Source{ID: uuid.New(), Name: "MB Bank", Kind: "bank", CreatedAt: at(now), UpdatedAt: at(now)}
	tx.TagID = &tag.ID

	accepted, ignored, err := store.Push(ctx, user, Changes{Transactions: []Transaction{tx}, Tags: []Tag{tag}, Sources: []Source{source}})
	if err != nil || accepted != 3 || ignored != 0 {
		t.Fatalf("first push: %d %d %v", accepted, ignored, err)
	}
	all, cursor := pullAll(t, store, user, 0)
	if len(all.Transactions) != 1 || len(all.Tags) != 1 || len(all.Sources) != 1 {
		t.Fatalf("pull: %+v", all)
	}
	if got := all.Transactions[0]; got.Amount != 89000 || *got.TagID != tag.ID || got.DeletedAt != nil {
		t.Fatalf("round trip: %+v", got)
	}

	// Gửi lại y nguyên, hoặc bản cũ hơn: bỏ qua, không sinh thay đổi mới.
	older := tx
	older.Amount = 1
	older.UpdatedAt = at(now.Add(-time.Minute))
	accepted, ignored, _ = store.Push(ctx, user, Changes{Transactions: []Transaction{tx, older}})
	if accepted != 0 || ignored != 2 {
		t.Fatalf("stale push: accepted %d ignored %d", accepted, ignored)
	}
	if changes, next := pullAll(t, store, user, cursor); changes.Len() != 0 || next != cursor {
		t.Fatalf("nothing should change after stale push: %+v next %d", changes, next)
	}

	// Bản mới hơn (xoá mềm) thắng và xuất hiện ở lần kéo sau.
	deleted := tx
	deleted.UpdatedAt = at(now.Add(time.Minute))
	deleted.DeletedAt = &deleted.UpdatedAt
	accepted, _, _ = store.Push(ctx, user, Changes{Transactions: []Transaction{deleted}})
	if accepted != 1 {
		t.Fatalf("newer push not accepted")
	}
	changes, next := pullAll(t, store, user, cursor)
	if len(changes.Transactions) != 1 || changes.Transactions[0].DeletedAt == nil || next <= cursor {
		t.Fatalf("soft delete not pulled: %+v", changes)
	}

	// Người khác không thấy dữ liệu này.
	if other, _ := pullAll(t, store, newUser(t, store), 0); other.Len() != 0 {
		t.Fatal("data leaked to another user")
	}
}

func TestPGPullPagesInRevOrder(t *testing.T) {
	store, user := pgStore(t)
	ctx := context.Background()
	var batch Changes
	for range 7 {
		batch.Transactions = append(batch.Transactions, validTransaction())
		batch.Tags = append(batch.Tags, Tag{ID: uuid.New(), Name: "t", Kind: "income", Color: "blue", CreatedAt: at(now), UpdatedAt: at(now)})
	}
	if _, _, err := store.Push(ctx, user, batch); err != nil {
		t.Fatal(err)
	}

	seen := map[uuid.UUID]bool{}
	cursor := int64(0)
	for pages := 0; ; pages++ {
		changes, next, hasMore, err := store.Pull(ctx, user, cursor, 3)
		if err != nil {
			t.Fatal(err)
		}
		if changes.Len() > 3 || pages > 10 {
			t.Fatalf("page too big or looping: %d", changes.Len())
		}
		for _, tx := range changes.Transactions {
			seen[tx.ID] = true
		}
		for _, tag := range changes.Tags {
			seen[tag.ID] = true
		}
		cursor = next
		if !hasMore {
			break
		}
	}
	if len(seen) != 14 {
		t.Fatalf("paging lost records: got %d of 14", len(seen))
	}
}

func TestPGConcurrentPushesKeepEveryChange(t *testing.T) {
	store, user := pgStore(t)
	ctx := context.Background()
	var wg gosync.WaitGroup
	for range 5 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, _, err := store.Push(ctx, user, Changes{Transactions: []Transaction{validTransaction(), validTransaction()}}); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if all, _ := pullAll(t, store, user, 0); len(all.Transactions) != 10 {
		t.Fatalf("expected 10 transactions, got %d", len(all.Transactions))
	}
}
