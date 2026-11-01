package bootstrap

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/mthangit/tingting-server/internal/config"
)

type stubStore struct {
	cfg config.Config
	err error
}

func (s stubStore) Active(context.Context) (config.Config, error) { return s.cfg, s.err }

func newHandler(store config.Store) Handler {
	return Handler{Configs: store, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
}

var cfg = config.Config{Version: 42, SchemaVersion: 1, Payload: json.RawMessage(`{"ttlSeconds":3600,"features":{"sync":true}}`)}

func get(h http.Handler, etag string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, "/v1/bootstrap", nil)
	if etag != "" {
		req.Header.Set("If-None-Match", etag)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestReturnsConfigWithETag(t *testing.T) {
	rec := get(newHandler(stubStore{cfg: cfg}), "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	if rec.Header().Get("ETag") == "" {
		t.Fatal("missing ETag")
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["configVersion"] != float64(42) {
		t.Fatalf("configVersion: %v", body["configVersion"])
	}
}

func TestReturns304WhenETagMatches(t *testing.T) {
	h := newHandler(stubStore{cfg: cfg})
	etag := get(h, "").Header().Get("ETag")

	for _, header := range []string{etag, "W/" + etag, `"old", ` + etag} {
		rec := get(h, header)
		if rec.Code != http.StatusNotModified || rec.Body.Len() != 0 {
			t.Fatalf("If-None-Match %s: status %d, body %q", header, rec.Code, rec.Body.String())
		}
	}
	if rec := get(h, `"cfg-41-abc"`); rec.Code != http.StatusOK {
		t.Fatalf("stale etag should get 200, got %d", rec.Code)
	}
}

func TestReturns503WithoutActiveConfig(t *testing.T) {
	rec := get(newHandler(stubStore{err: config.ErrNoActiveConfig}), "")
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status %d", rec.Code)
	}
	if rec.Body.String() != "{\"error\":\"configUnavailable\"}\n" {
		t.Fatalf("body %q", rec.Body.String())
	}
}
