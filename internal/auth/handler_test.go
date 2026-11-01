package auth

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

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
)

type harness struct {
	t      *testing.T
	google *fakeGoogle
	mux    *http.ServeMux
	now    time.Time
	device uuid.UUID
}

func newHarness(t *testing.T) *harness {
	h := &harness{t: t, google: newFakeGoogle(t), mux: http.NewServeMux(), now: time.Now(), device: uuid.New()}
	clock := func() time.Time { return h.now }
	service := &Service{
		Google:     h.google.verifier(),
		Store:      NewMemoryStore(),
		Access:     AccessTokens{Secret: []byte(strings.Repeat("s", 32)), TTL: 15 * time.Minute, Now: clock},
		RefreshTTL: 30 * 24 * time.Hour,
		Log:        slog.New(slog.NewTextHandler(io.Discard, nil)),
		Now:        clock,
	}
	service.Register(h.mux)
	return h
}

func (h *harness) do(method, path string, body any, headers map[string]string) *httptest.ResponseRecorder {
	var reader io.Reader
	if body != nil {
		raw, _ := json.Marshal(body)
		reader = bytes.NewReader(raw)
	}
	req := httptest.NewRequest(method, path, reader)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.mux.ServeHTTP(rec, req)
	return rec
}

func (h *harness) signIn(edit func(c jwt.MapClaims)) TokenResponse {
	h.t.Helper()
	rec := h.do("POST", "/v1/auth/google", map[string]string{"idToken": h.google.sign(h.t, edit)},
		map[string]string{"X-Device-Id": h.device.String(), "X-Platform": "ios", "X-App-Version": "0.2.0"})
	if rec.Code != http.StatusOK {
		h.t.Fatalf("sign in: %d %s", rec.Code, rec.Body)
	}
	return decodeTokens(h.t, rec)
}

func (h *harness) refresh(token string) *httptest.ResponseRecorder {
	return h.do("POST", "/v1/auth/refresh", map[string]string{"refreshToken": token}, nil)
}

func (h *harness) me(access string) *httptest.ResponseRecorder {
	return h.do("GET", "/v1/me", nil, map[string]string{"Authorization": "Bearer " + access})
}

func decodeTokens(t *testing.T, rec *httptest.ResponseRecorder) TokenResponse {
	t.Helper()
	var tokens TokenResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &tokens); err != nil {
		t.Fatal(err)
	}
	return tokens
}

func TestSignInReturnsTokensAndSameUserNextTime(t *testing.T) {
	h := newHarness(t)
	first := h.signIn(nil)
	if first.AccessToken == "" || first.RefreshToken == "" || first.User == nil || first.User.Email != "me@example.com" {
		t.Fatalf("unexpected response: %+v", first)
	}
	second := h.signIn(nil)
	if second.User.ID != first.User.ID {
		t.Fatal("same Google account should map to the same user")
	}
	other := h.signIn(func(c jwt.MapClaims) { c["sub"] = "google-user-2" })
	if other.User.ID == first.User.ID {
		t.Fatal("different Google account should be a different user")
	}

	rec := h.me(first.AccessToken)
	var user User
	_ = json.Unmarshal(rec.Body.Bytes(), &user)
	if rec.Code != http.StatusOK || user.ID != first.User.ID {
		t.Fatalf("me: %d %s", rec.Code, rec.Body)
	}
}

func TestSignInRejectsBadInput(t *testing.T) {
	h := newHarness(t)
	valid := h.google.sign(t, nil)
	device := map[string]string{"X-Device-Id": h.device.String()}
	cases := []struct {
		name    string
		body    any
		headers map[string]string
		status  int
		code    string
	}{
		{"missing token", map[string]string{}, device, 400, "invalidRequest"},
		{"missing device id", map[string]string{"idToken": valid}, nil, 400, "invalidDeviceId"},
		{"token for other app", map[string]string{"idToken": h.google.sign(t, func(c jwt.MapClaims) { c["aud"] = "x" })}, device, 401, "invalidIdToken"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := h.do("POST", "/v1/auth/google", tc.body, tc.headers)
			if rec.Code != tc.status || !strings.Contains(rec.Body.String(), tc.code) {
				t.Fatalf("got %d %s", rec.Code, rec.Body)
			}
		})
	}
}

func TestRefreshRotatesAndDetectsReuse(t *testing.T) {
	h := newHarness(t)
	login := h.signIn(nil)

	rec := h.refresh(login.RefreshToken)
	if rec.Code != http.StatusOK {
		t.Fatalf("refresh: %d %s", rec.Code, rec.Body)
	}
	rotated := decodeTokens(t, rec)
	if rotated.RefreshToken == login.RefreshToken {
		t.Fatal("refresh token should rotate")
	}
	if h.me(rotated.AccessToken).Code != http.StatusOK {
		t.Fatal("new access token should work")
	}

	// Token cũ bị dùng lại: từ chối, và token mới của máy đó cũng bị thu hồi.
	if rec := h.refresh(login.RefreshToken); rec.Code != http.StatusUnauthorized || !strings.Contains(rec.Body.String(), "refreshTokenReused") {
		t.Fatalf("reuse: %d %s", rec.Code, rec.Body)
	}
	if rec := h.refresh(rotated.RefreshToken); rec.Code != http.StatusUnauthorized {
		t.Fatalf("rotated token should be revoked after reuse, got %d", rec.Code)
	}
}

func TestRefreshRejectsUnknownAndExpired(t *testing.T) {
	h := newHarness(t)
	if rec := h.refresh("nope"); rec.Code != http.StatusUnauthorized {
		t.Fatalf("unknown: %d", rec.Code)
	}
	login := h.signIn(nil)
	h.now = h.now.Add(31 * 24 * time.Hour)
	if rec := h.refresh(login.RefreshToken); rec.Code != http.StatusUnauthorized || !strings.Contains(rec.Body.String(), "invalidRefreshToken") {
		t.Fatalf("expired: %d %s", rec.Code, rec.Body)
	}
}

func TestLogoutRevokesRefreshToken(t *testing.T) {
	h := newHarness(t)
	login := h.signIn(nil)
	if rec := h.do("POST", "/v1/auth/logout", map[string]string{"refreshToken": login.RefreshToken}, nil); rec.Code != http.StatusNoContent {
		t.Fatalf("logout: %d", rec.Code)
	}
	if rec := h.refresh(login.RefreshToken); rec.Code != http.StatusUnauthorized {
		t.Fatalf("refresh after logout: %d", rec.Code)
	}
}

func TestRequireRejectsMissingWrongAndExpiredAccessTokens(t *testing.T) {
	h := newHarness(t)
	login := h.signIn(nil)

	if rec := h.do("GET", "/v1/me", nil, nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("missing: %d", rec.Code)
	}
	forged, _, _ := AccessTokens{Secret: []byte(strings.Repeat("x", 32)), TTL: time.Hour}.Issue(Session{UserID: login.User.ID, DeviceID: h.device})
	if rec := h.me(forged); rec.Code != http.StatusUnauthorized {
		t.Fatalf("forged: %d", rec.Code)
	}
	h.now = h.now.Add(16 * time.Minute)
	if rec := h.me(login.AccessToken); rec.Code != http.StatusUnauthorized {
		t.Fatalf("expired: %d", rec.Code)
	}
}

func TestAccessTokenRoundTrip(t *testing.T) {
	tokens := AccessTokens{Secret: []byte(strings.Repeat("k", 32)), TTL: time.Minute}
	want := Session{UserID: uuid.New(), DeviceID: uuid.New()}
	raw, _, err := tokens.Issue(want)
	if err != nil {
		t.Fatal(err)
	}
	got, err := tokens.Parse(raw)
	if err != nil || got != want {
		t.Fatalf("got %+v %v", got, err)
	}
	if _, err := tokens.Parse(raw + "x"); err == nil {
		t.Fatal("tampered token accepted")
	}
}

func TestSessionFromContext(t *testing.T) {
	if _, ok := SessionFrom(context.Background()); ok {
		t.Fatal("empty context should have no session")
	}
}
