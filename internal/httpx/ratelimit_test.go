package httpx

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestRateLimiterBlocksAfterBurstPerIPAndRecovers(t *testing.T) {
	now := time.Unix(0, 0)
	limiter := &RateLimiter{
		Policy: func(r *http.Request) (string, Limit, bool) {
			return "test", Limit{PerMinute: 60, Burst: 2}, r.URL.Path != "/health"
		},
		Now: func() time.Time { return now },
	}
	handler := limiter.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) }))
	call := func(path, ip string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("GET", path, nil)
		req.Header.Set("X-Forwarded-For", ip)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		return rec
	}

	call("/x", "1.1.1.1")
	call("/x", "1.1.1.1")
	rec := call("/x", "1.1.1.1")
	if rec.Code != http.StatusTooManyRequests || rec.Header().Get("Retry-After") != "1" {
		t.Fatalf("third request should be limited: %d %q", rec.Code, rec.Header().Get("Retry-After"))
	}
	if rec := call("/x", "2.2.2.2"); rec.Code != 204 {
		t.Fatal("another IP should not be limited")
	}
	if rec := call("/health", "1.1.1.1"); rec.Code != 204 {
		t.Fatal("unlimited path should pass")
	}
	now = now.Add(time.Second)
	if rec := call("/x", "1.1.1.1"); rec.Code != 204 {
		t.Fatalf("should recover after a token refills, got %d", rec.Code)
	}
}

func TestClientIPUsesLastForwardedHop(t *testing.T) {
	req := httptest.NewRequest("GET", "/", nil)
	req.RemoteAddr = "10.0.0.1:1234"
	if got := ClientIP(req); got != "10.0.0.1" {
		t.Fatalf("remote addr: %s", got)
	}
	// Phần tử đầu do client tự gửi (có thể giả); phần tử cuối do Google Front End thêm vào.
	req.Header.Set("X-Forwarded-For", "6.6.6.6, 203.0.113.9")
	if got := ClientIP(req); got != "203.0.113.9" {
		t.Fatalf("forwarded: %s", got)
	}
}
