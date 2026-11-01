package auth

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"errors"
	"math/big"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

const testClientID = "123-ios.apps.googleusercontent.com"

// fakeGoogle phục vụ khoá công khai giống googleapis.com và ký idToken bằng khoá riêng tương ứng.
type fakeGoogle struct {
	key     *rsa.PrivateKey
	kid     string
	server  *httptest.Server
	fetches atomic.Int32
}

func newFakeGoogle(t *testing.T) *fakeGoogle {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	g := &fakeGoogle{key: key, kid: "k1"}
	g.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		g.fetches.Add(1)
		pub := g.key.PublicKey
		_ = json.NewEncoder(w).Encode(map[string]any{"keys": []map[string]string{{
			"kid": g.kid, "kty": "RSA", "alg": "RS256",
			"n": base64.RawURLEncoding.EncodeToString(pub.N.Bytes()),
			"e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(pub.E)).Bytes()),
		}}})
	}))
	t.Cleanup(g.server.Close)
	return g
}

func (g *fakeGoogle) sign(t *testing.T, edit func(c jwt.MapClaims)) string {
	t.Helper()
	now := time.Now()
	claims := jwt.MapClaims{
		"iss": "https://accounts.google.com", "aud": testClientID, "sub": "google-user-1",
		"email": "me@example.com", "email_verified": true,
		"iat": now.Unix(), "exp": now.Add(time.Hour).Unix(),
	}
	if edit != nil {
		edit(claims)
	}
	token := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	token.Header["kid"] = g.kid
	raw, err := token.SignedString(g.key)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func (g *fakeGoogle) verifier() *GoogleVerifier {
	return &GoogleVerifier{ClientIDs: []string{testClientID}, CertsURL: g.server.URL}
}

func TestGoogleVerifierAcceptsValidToken(t *testing.T) {
	g := newFakeGoogle(t)
	id, err := g.verifier().Verify(context.Background(), g.sign(t, nil))
	if err != nil {
		t.Fatal(err)
	}
	if id != (Identity{Provider: "google", Subject: "google-user-1", Email: "me@example.com"}) {
		t.Fatalf("identity: %+v", id)
	}
}

func TestGoogleVerifierRejects(t *testing.T) {
	g := newFakeGoogle(t)
	other, _ := rsa.GenerateKey(rand.Reader, 2048)
	cases := map[string]func() string{
		"other app":    func() string { return g.sign(t, func(c jwt.MapClaims) { c["aud"] = "someone-else" }) },
		"wrong issuer": func() string { return g.sign(t, func(c jwt.MapClaims) { c["iss"] = "https://evil.example.com" }) },
		"expired": func() string {
			return g.sign(t, func(c jwt.MapClaims) { c["exp"] = time.Now().Add(-time.Hour).Unix() })
		},
		"no expiry":  func() string { return g.sign(t, func(c jwt.MapClaims) { delete(c, "exp") }) },
		"no subject": func() string { return g.sign(t, func(c jwt.MapClaims) { delete(c, "sub") }) },
		"garbage":    func() string { return "not-a-jwt" },
		"wrong key": func() string {
			token := jwt.NewWithClaims(jwt.SigningMethodRS256, jwt.MapClaims{"iss": "accounts.google.com", "aud": testClientID,
				"sub": "x", "exp": time.Now().Add(time.Hour).Unix()})
			token.Header["kid"] = g.kid
			raw, _ := token.SignedString(other)
			return raw
		},
		"hmac with public key": func() string {
			token := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{"iss": "accounts.google.com", "aud": testClientID,
				"sub": "x", "exp": time.Now().Add(time.Hour).Unix()})
			token.Header["kid"] = g.kid
			raw, _ := token.SignedString(g.key.PublicKey.N.Bytes())
			return raw
		},
	}
	v := g.verifier()
	for name, token := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := v.Verify(context.Background(), token()); !errors.Is(err, ErrInvalidIDToken) {
				t.Fatalf("expected ErrInvalidIDToken, got %v", err)
			}
		})
	}
}

func TestGoogleVerifierDropsUnverifiedEmail(t *testing.T) {
	g := newFakeGoogle(t)
	id, err := g.verifier().Verify(context.Background(), g.sign(t, func(c jwt.MapClaims) { c["email_verified"] = false }))
	if err != nil || id.Email != "" {
		t.Fatalf("expected empty email, got %q (%v)", id.Email, err)
	}
}

func TestGoogleVerifierCachesKeysAndRefetchesOnRotation(t *testing.T) {
	g := newFakeGoogle(t)
	now := time.Now()
	v := g.verifier()
	v.Now = func() time.Time { return now }
	ctx := context.Background()

	for range 3 {
		if _, err := v.Verify(ctx, g.sign(t, nil)); err != nil {
			t.Fatal(err)
		}
	}
	if got := g.fetches.Load(); got != 1 {
		t.Fatalf("expected 1 fetch, got %d", got)
	}

	// Google đổi khoá: kid lạ trong vòng 1 phút thì chưa tải lại, sau 1 phút thì tải lại.
	g.key, _ = rsa.GenerateKey(rand.Reader, 2048)
	g.kid = "k2"
	if _, err := v.Verify(ctx, g.sign(t, nil)); err == nil {
		t.Fatal("expected unknown kid to fail within refetch cooldown")
	}
	now = now.Add(2 * time.Minute)
	if _, err := v.Verify(ctx, g.sign(t, nil)); err != nil {
		t.Fatalf("expected refetch to pick up new key: %v", err)
	}
	if got := g.fetches.Load(); got != 2 {
		t.Fatalf("expected 2 fetches, got %d", got)
	}
}
