package auth

import (
	"context"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"slices"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// GoogleCertsURL là nơi Google công bố khoá công khai để kiểm tra idToken.
const GoogleCertsURL = "https://www.googleapis.com/oauth2/v3/certs"

var googleIssuers = []string{"accounts.google.com", "https://accounts.google.com"}

// Identity là người dùng đã được Google (hoặc Apple) xác nhận.
type Identity struct {
	Provider string
	Subject  string
	Email    string
}

// Verifier kiểm tra token đăng nhập do Google/Apple cấp cho app.
type Verifier interface {
	Verify(ctx context.Context, token string) (Identity, error)
}

// ErrInvalidIDToken: token sai chữ ký, hết hạn, sai app hoặc sai nơi cấp.
var ErrInvalidIDToken = errors.New("invalid id token")

// GoogleVerifier kiểm tra idToken từ Google Sign-In.
// ClientIDs là OAuth client ID của app (iOS, sau này web); token cấp cho app khác bị từ chối.
type GoogleVerifier struct {
	ClientIDs []string
	CertsURL  string
	Client    *http.Client
	Now       func() time.Time

	mu        sync.Mutex
	keys      map[string]*rsa.PublicKey
	fetchedAt time.Time
}

type googleClaims struct {
	Email         string `json:"email"`
	EmailVerified bool   `json:"email_verified"`
	jwt.RegisteredClaims
}

func (v *GoogleVerifier) Verify(ctx context.Context, raw string) (Identity, error) {
	var claims googleClaims
	_, err := jwt.ParseWithClaims(raw, &claims, func(t *jwt.Token) (any, error) {
		kid, _ := t.Header["kid"].(string)
		return v.key(ctx, kid)
	},
		jwt.WithValidMethods([]string{"RS256"}),
		jwt.WithExpirationRequired(),
		jwt.WithIssuedAt(),
		jwt.WithLeeway(30*time.Second),
		jwt.WithTimeFunc(v.now),
	)
	if err != nil {
		return Identity{}, fmt.Errorf("%w: %v", ErrInvalidIDToken, err)
	}
	if !slices.Contains(googleIssuers, claims.Issuer) {
		return Identity{}, fmt.Errorf("%w: issuer %q", ErrInvalidIDToken, claims.Issuer)
	}
	if !slices.ContainsFunc(claims.Audience, func(aud string) bool { return slices.Contains(v.ClientIDs, aud) }) {
		return Identity{}, fmt.Errorf("%w: audience %v", ErrInvalidIDToken, claims.Audience)
	}
	if claims.Subject == "" {
		return Identity{}, fmt.Errorf("%w: missing sub", ErrInvalidIDToken)
	}
	email := ""
	if claims.EmailVerified {
		email = claims.Email
	}
	return Identity{Provider: "google", Subject: claims.Subject, Email: email}, nil
}

// key trả khoá công khai theo kid. Khoá được cache 1 giờ; gặp kid lạ (Google vừa đổi khoá) thì tải lại,
// nhưng không quá 1 lần mỗi phút để token giả không làm server gọi Google liên tục.
func (v *GoogleVerifier) key(ctx context.Context, kid string) (*rsa.PublicKey, error) {
	v.mu.Lock()
	defer v.mu.Unlock()

	age := v.now().Sub(v.fetchedAt)
	if key, ok := v.keys[kid]; ok && age < time.Hour {
		return key, nil
	}
	if v.keys == nil || age >= time.Minute {
		keys, err := v.fetch(ctx)
		if err != nil {
			if key, ok := v.keys[kid]; ok {
				return key, nil // Google tạm lỗi: dùng khoá cũ
			}
			return nil, err
		}
		v.keys, v.fetchedAt = keys, v.now()
	}
	if key, ok := v.keys[kid]; ok {
		return key, nil
	}
	return nil, fmt.Errorf("unknown key id %q", kid)
}

func (v *GoogleVerifier) fetch(ctx context.Context) (map[string]*rsa.PublicKey, error) {
	url := v.CertsURL
	if url == "" {
		url = GoogleCertsURL
	}
	client := v.Client
	if client == nil {
		client = &http.Client{Timeout: 5 * time.Second}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch google certs: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("fetch google certs: status %d", resp.StatusCode)
	}

	var set struct {
		Keys []struct {
			Kid string `json:"kid"`
			Kty string `json:"kty"`
			N   string `json:"n"`
			E   string `json:"e"`
		} `json:"keys"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&set); err != nil {
		return nil, fmt.Errorf("decode google certs: %w", err)
	}
	keys := make(map[string]*rsa.PublicKey, len(set.Keys))
	for _, k := range set.Keys {
		if k.Kty != "RSA" {
			continue
		}
		n, errN := base64.RawURLEncoding.DecodeString(k.N)
		e, errE := base64.RawURLEncoding.DecodeString(k.E)
		if errN != nil || errE != nil {
			continue
		}
		keys[k.Kid] = &rsa.PublicKey{N: new(big.Int).SetBytes(n), E: int(new(big.Int).SetBytes(e).Int64())}
	}
	if len(keys) == 0 {
		return nil, errors.New("google certs: no usable keys")
	}
	return keys, nil
}

func (v *GoogleVerifier) now() time.Time {
	if v.Now != nil {
		return v.Now()
	}
	return time.Now()
}
