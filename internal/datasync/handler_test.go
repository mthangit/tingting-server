package datasync

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/mthangit/tingting-server/internal/auth"
)

type fakeStore struct {
	pushedFor uuid.UUID
	pushed    Changes
	pulledFor uuid.UUID
	cursor    int64
	limit     int
}

func (f *fakeStore) Push(_ context.Context, user uuid.UUID, c Changes) (int, int, error) {
	f.pushedFor, f.pushed = user, c
	return c.Len(), 0, nil
}

func (f *fakeStore) Pull(_ context.Context, user uuid.UUID, cursor int64, limit int) (Changes, int64, bool, error) {
	f.pulledFor, f.cursor, f.limit = user, cursor, limit
	return Changes{Transactions: []Transaction{}, Tags: []Tag{}, Sources: []Source{}}, cursor, false, nil
}

func setup(t *testing.T) (*http.ServeMux, *fakeStore, string, uuid.UUID) {
	t.Helper()
	tokens := auth.AccessTokens{Secret: []byte(strings.Repeat("s", 32)), TTL: time.Hour}
	authService := &auth.Service{Access: tokens}
	store := &fakeStore{}
	mux := http.NewServeMux()
	(&Service{Store: store, Log: slog.New(slog.NewTextHandler(io.Discard, nil)), Now: func() time.Time { return now }}).
		Register(mux, authService.Require)
	user := uuid.New()
	token, _, _ := tokens.Issue(auth.Session{UserID: user, DeviceID: uuid.New()})
	return mux, store, token, user
}

func call(mux http.Handler, method, path, token string, body any) *httptest.ResponseRecorder {
	var reader io.Reader
	if body != nil {
		raw, _ := json.Marshal(body)
		reader = bytes.NewReader(raw)
	}
	req := httptest.NewRequest(method, path, reader)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func TestSyncRequiresSignIn(t *testing.T) {
	mux, _, _, _ := setup(t)
	if rec := call(mux, "GET", "/v1/sync/pull", "", nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("pull without token: %d", rec.Code)
	}
	if rec := call(mux, "POST", "/v1/sync/push", "", map[string]any{}); rec.Code != http.StatusUnauthorized {
		t.Fatalf("push without token: %d", rec.Code)
	}
}

func TestPushStoresValidRecordsForTokenUserAndReportsRejected(t *testing.T) {
	mux, store, token, user := setup(t)
	bad := validTransaction()
	bad.Amount = -5
	rec := call(mux, "POST", "/v1/sync/push", token, map[string]any{"changes": Changes{Transactions: []Transaction{validTransaction(), bad}}})
	if rec.Code != http.StatusOK {
		t.Fatalf("push: %d %s", rec.Code, rec.Body)
	}
	var resp PushResponse
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	if resp.Accepted != 1 || len(resp.Rejected) != 1 || resp.Rejected[0].Reason != "invalidAmount" {
		t.Fatalf("response: %+v", resp)
	}
	if store.pushedFor != user || len(store.pushed.Transactions) != 1 {
		t.Fatal("push should go to the token's user with only valid records")
	}
}

func TestPushRejectsTooManyChanges(t *testing.T) {
	mux, _, token, _ := setup(t)
	many := make([]Transaction, MaxPushRecords+1)
	for i := range many {
		many[i] = validTransaction()
	}
	if rec := call(mux, "POST", "/v1/sync/push", token, map[string]any{"changes": Changes{Transactions: many}}); rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("got %d", rec.Code)
	}
}

func TestPullParsesCursorAndCapsLimit(t *testing.T) {
	mux, store, token, user := setup(t)
	rec := call(mux, "GET", "/v1/sync/pull?cursor=42&limit=99999", token, nil)
	if rec.Code != http.StatusOK || store.pulledFor != user || store.cursor != 42 || store.limit != MaxPullLimit {
		t.Fatalf("pull: %d %+v", rec.Code, store)
	}
	if !strings.Contains(rec.Body.String(), `"transactions":[]`) {
		t.Fatalf("empty lists should be [] not null: %s", rec.Body)
	}
	for _, q := range []string{"cursor=-1", "cursor=abc", "limit=0"} {
		if rec := call(mux, "GET", "/v1/sync/pull?"+q, token, nil); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: %d", q, rec.Code)
		}
	}
}
