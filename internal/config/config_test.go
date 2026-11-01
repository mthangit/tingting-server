package config

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

var hosts = []string{"*.run.app", "localhost"}

var logoHosts = []string{"*.r2.dev"}

const valid = `{
  "ttlSeconds": 3600,
  "app": { "minVersion": "0.1.0" },
  "api": {
    "baseUrl": "https://ting-api-abc.a.run.app",
    "endpoints": { "sync.push": { "method": "POST", "path": "/v1/sync/push", "timeoutMs": 15000 } }
  },
  "features": { "sync": true },
  "sync": { "intervalSeconds": 900, "debounceSeconds": 5, "pushBatchSize": 200, "pullPageSize": 500 },
  "catalog": { "sources": { "version": 1, "items": [
    { "id": "6E0C7A90-0000-4000-8000-000000000110", "name": "MoMo", "kind": "wallet", "sortOrder": 0,
      "logoUrl": "https://pub-test.r2.dev/logos/momo.png", "aliases": ["momo"] }
  ] } },
  "somethingNew": { "kept": true }
}`

func TestValidateAcceptsGoodConfig(t *testing.T) {
	if err := Validate([]byte(valid), hosts, logoHosts); err != nil {
		t.Fatalf("expected valid, got %v", err)
	}
}

func TestValidateRejects(t *testing.T) {
	cases := map[string]struct {
		edit func(m map[string]any)
		want string
	}{
		"foreign domain": {func(m map[string]any) { api(m)["baseUrl"] = "https://evil.example.com" }, "không nằm trong danh sách"},
		"plain http":     {func(m map[string]any) { api(m)["baseUrl"] = "http://x.a.run.app" }, "phải dùng https"},
		"path in base":   {func(m map[string]any) { api(m)["baseUrl"] = "https://x.a.run.app/v1" }, "chỉ gồm scheme và host"},
		"bad method": {func(m map[string]any) {
			api(m)["endpoints"] = map[string]any{"sync.push": map[string]any{"method": "FETCH", "path": "/x"}}
		}, "method"},
		"absolute path": {func(m map[string]any) {
			api(m)["endpoints"] = map[string]any{"sync.push": map[string]any{"method": "GET", "path": "https://evil.example.com/x"}}
		}, "path phải bắt đầu bằng /"},
		"bad name": {func(m map[string]any) {
			api(m)["endpoints"] = map[string]any{"Sync Push": map[string]any{"method": "GET", "path": "/x"}}
		}, "nhom.tenApi"},
		"no endpoints":          {func(m map[string]any) { api(m)["endpoints"] = map[string]any{} }, "không được rỗng"},
		"short ttl":             {func(m map[string]any) { m["ttlSeconds"] = 5 }, "ttlSeconds"},
		"bad minVersion":        {func(m map[string]any) { m["app"] = map[string]any{"minVersion": "1.0"} }, "minVersion"},
		"wrong schema":          {func(m map[string]any) { m["schemaVersion"] = 2 }, "schemaVersion"},
		"catalog bad id":        {func(m map[string]any) { source(m)["id"] = "abc" }, "phải là UUID"},
		"catalog bad kind":      {func(m map[string]any) { source(m)["kind"] = "card" }, "kind"},
		"catalog empty name":    {func(m map[string]any) { source(m)["name"] = " " }, "name không được rỗng"},
		"catalog foreign logo":  {func(m map[string]any) { source(m)["logoUrl"] = "https://evil.example.com/x.png" }, "logoUrl"},
		"catalog relative logo": {func(m map[string]any) { source(m)["logoUrl"] = "/logos/x.png" }, "logoUrl"},
		"catalog http logo":     {func(m map[string]any) { source(m)["logoUrl"] = "http://pub-test.r2.dev/x.png" }, "https"},
		"catalog bad alias":     {func(m map[string]any) { source(m)["aliases"] = []any{"MB Bank"} }, "aliases"},
		"catalog duplicate id": {func(m map[string]any) {
			sources := m["catalog"].(map[string]any)["sources"].(map[string]any)
			items := sources["items"].([]any)
			dup := map[string]any{}
			for k, v := range items[0].(map[string]any) {
				dup[k] = v
			}
			dup["name"] = "Khác"
			sources["items"] = append(items, dup)
		}, "bị trùng"},
		"bad sync": {func(m map[string]any) {
			m["sync"] = map[string]any{"intervalSeconds": 10, "pushBatchSize": 1, "pullPageSize": 1}
		}, "intervalSeconds"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			var m map[string]any
			if err := json.Unmarshal([]byte(valid), &m); err != nil {
				t.Fatal(err)
			}
			tc.edit(m)
			raw, _ := json.Marshal(m)
			err := Validate(raw, hosts, logoHosts)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("expected error containing %q, got %v", tc.want, err)
			}
		})
	}
}

func TestValidateAllowsHTTPOnlyForLocalhost(t *testing.T) {
	raw := strings.Replace(valid, "https://ting-api-abc.a.run.app", "http://localhost:8080", 1)
	if err := Validate([]byte(raw), hosts, logoHosts); err != nil {
		t.Fatalf("expected localhost http allowed, got %v", err)
	}
}

func TestBodyKeepsUnknownFieldsAndSetsVersions(t *testing.T) {
	body, err := Config{Version: 7, SchemaVersion: 1, Payload: json.RawMessage(valid)}.Body()
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatal(err)
	}
	if got["configVersion"] != float64(7) || got["schemaVersion"] != float64(1) {
		t.Fatalf("versions not set: %v", got)
	}
	if _, ok := got["somethingNew"]; !ok {
		t.Fatal("unknown field dropped")
	}
}

func TestETagChangesWithContent(t *testing.T) {
	a, b := ETag(1, []byte(`{"a":1}`)), ETag(1, []byte(`{"a":2}`))
	if a == b || a != ETag(1, []byte(`{"a":1}`)) {
		t.Fatalf("etag not stable or not content-based: %s %s", a, b)
	}
}

func TestHostAllowed(t *testing.T) {
	cases := map[string]bool{"x.a.run.app": true, "run.app": false, "localhost": true, "evilrun.app": false, "X.A.RUN.APP": true}
	for host, want := range cases {
		if got := HostAllowed(host, hosts); got != want {
			t.Errorf("%s: got %v want %v", host, got, want)
		}
	}
}

type fakeStore struct {
	calls int
	cfg   Config
	err   error
}

func (f *fakeStore) Active(context.Context) (Config, error) {
	f.calls++
	return f.cfg, f.err
}

func TestCachedReloadsAfterTTLAndKeepsLastGoodOnError(t *testing.T) {
	now := time.Unix(0, 0)
	store := &fakeStore{cfg: Config{Version: 1}}
	cached := &Cached{Store: store, TTL: 30 * time.Second, Now: func() time.Time { return now }}
	ctx := context.Background()

	cached.Active(ctx)
	cached.Active(ctx)
	if store.calls != 1 {
		t.Fatalf("expected 1 load within TTL, got %d", store.calls)
	}

	now = now.Add(31 * time.Second)
	store.err = errors.New("db down")
	got, err := cached.Active(ctx)
	if err != nil || got.Version != 1 {
		t.Fatalf("expected last good config on db error, got %v %v", got, err)
	}

	store.err, store.cfg = nil, Config{Version: 2}
	if got, _ := cached.Active(ctx); got.Version != 2 {
		t.Fatalf("expected retry after error to load new config, got %d", got.Version)
	}
}

func TestCachedDoesNotCacheTransientErrorWithoutGoodValue(t *testing.T) {
	store := &fakeStore{err: errors.New("db down")}
	cached := &Cached{Store: store, TTL: time.Minute}
	if _, err := cached.Active(context.Background()); err == nil {
		t.Fatal("expected error")
	}
	store.err, store.cfg = nil, Config{Version: 3}
	if got, err := cached.Active(context.Background()); err != nil || got.Version != 3 {
		t.Fatalf("expected immediate retry, got %v %v", got, err)
	}
}

func api(m map[string]any) map[string]any { return m["api"].(map[string]any) }

func source(m map[string]any) map[string]any {
	return m["catalog"].(map[string]any)["sources"].(map[string]any)["items"].([]any)[0].(map[string]any)
}
