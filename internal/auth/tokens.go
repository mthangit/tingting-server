package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
)

const (
	tokenIssuer   = "ting"
	tokenAudience = "ting-app"
)

// ErrInvalidAccessToken: access token sai, hết hạn hoặc không do server này cấp.
var ErrInvalidAccessToken = errors.New("invalid access token")

// AccessTokens cấp và kiểm tra access token (JWT ký HS256 bằng khoá bí mật của server).
// Token ngắn hạn, không lưu trong database; muốn chặn một máy thì thu hồi refresh token của máy đó.
type AccessTokens struct {
	Secret []byte
	TTL    time.Duration
	Now    func() time.Time
}

// Session là người dùng và máy gắn với một access token.
type Session struct {
	UserID   uuid.UUID
	DeviceID uuid.UUID
}

type accessClaims struct {
	DeviceID string `json:"did"`
	jwt.RegisteredClaims
}

func (a AccessTokens) Issue(s Session) (string, time.Time, error) {
	now := a.now()
	expires := now.Add(a.TTL)
	claims := accessClaims{
		DeviceID: s.DeviceID.String(),
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    tokenIssuer,
			Audience:  jwt.ClaimStrings{tokenAudience},
			Subject:   s.UserID.String(),
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(expires),
		},
	}
	token, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(a.Secret)
	return token, expires, err
}

func (a AccessTokens) Parse(raw string) (Session, error) {
	var claims accessClaims
	_, err := jwt.ParseWithClaims(raw, &claims, func(*jwt.Token) (any, error) { return a.Secret, nil },
		jwt.WithValidMethods([]string{"HS256"}),
		jwt.WithIssuer(tokenIssuer),
		jwt.WithAudience(tokenAudience),
		jwt.WithExpirationRequired(),
		jwt.WithLeeway(10*time.Second),
		jwt.WithTimeFunc(a.now),
	)
	if err != nil {
		return Session{}, fmt.Errorf("%w: %v", ErrInvalidAccessToken, err)
	}
	userID, errU := uuid.Parse(claims.Subject)
	deviceID, errD := uuid.Parse(claims.DeviceID)
	if errU != nil || errD != nil {
		return Session{}, fmt.Errorf("%w: bad subject", ErrInvalidAccessToken)
	}
	return Session{UserID: userID, DeviceID: deviceID}, nil
}

func (a AccessTokens) now() time.Time {
	if a.Now != nil {
		return a.Now()
	}
	return time.Now()
}

// NewRefreshToken tạo refresh token ngẫu nhiên (trả cho app) và mã băm của nó (lưu trong database).
func NewRefreshToken() (token, hash string, err error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", "", err
	}
	token = base64.RawURLEncoding.EncodeToString(b)
	return token, HashRefreshToken(token), nil
}

func HashRefreshToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}
