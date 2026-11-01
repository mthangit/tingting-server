package auth

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/mthangit/tingting-server/internal/httpx"
)

// Service gồm các API đăng nhập và middleware kiểm tra access token.
type Service struct {
	Google     Verifier
	Store      Store
	Access     AccessTokens
	RefreshTTL time.Duration
	Log        *slog.Logger
	Now        func() time.Time
}

// TokenResponse là kết quả đăng nhập và refresh.
type TokenResponse struct {
	AccessToken           string    `json:"accessToken"`
	AccessTokenExpiresAt  time.Time `json:"accessTokenExpiresAt"`
	RefreshToken          string    `json:"refreshToken"`
	RefreshTokenExpiresAt time.Time `json:"refreshTokenExpiresAt"`
	User                  *User     `json:"user,omitempty"`
}

// Register gắn các API đăng nhập vào mux.
func (s *Service) Register(mux *http.ServeMux) {
	mux.HandleFunc("POST /v1/auth/google", s.signInWithGoogle)
	mux.HandleFunc("POST /v1/auth/refresh", s.refresh)
	mux.HandleFunc("POST /v1/auth/logout", s.logout)
	mux.Handle("GET /v1/me", s.Require(http.HandlerFunc(s.me)))
}

// POST /v1/auth/google  {"idToken": "..."}  + header X-Device-Id (UUID do app tạo), X-Platform, X-App-Version.
func (s *Service) signInWithGoogle(w http.ResponseWriter, r *http.Request) {
	var body struct {
		IDToken string `json:"idToken"`
	}
	if !decode(w, r, &body) || body.IDToken == "" {
		httpx.Error(w, http.StatusBadRequest, "invalidRequest")
		return
	}
	deviceID, err := uuid.Parse(r.Header.Get("X-Device-Id"))
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, "invalidDeviceId")
		return
	}

	identity, err := s.Google.Verify(r.Context(), body.IDToken)
	if err != nil {
		if !errors.Is(err, ErrInvalidIDToken) {
			s.Log.ErrorContext(r.Context(), "verify google token", "err", err)
			httpx.Error(w, http.StatusServiceUnavailable, "providerUnavailable")
			return
		}
		s.Log.InfoContext(r.Context(), "rejected google token", "err", err)
		httpx.Error(w, http.StatusUnauthorized, "invalidIdToken")
		return
	}

	refresh, hash, err := NewRefreshToken()
	if err != nil {
		s.fail(w, r, "new refresh token", err)
		return
	}
	refreshExpires := s.now().Add(s.RefreshTTL)
	device := Device{ID: deviceID, Platform: r.Header.Get("X-Platform"), AppVersion: r.Header.Get("X-App-Version")}
	user, err := s.Store.SignIn(r.Context(), identity, device, hash, refreshExpires)
	if errors.Is(err, ErrUserNotFound) {
		httpx.Error(w, http.StatusForbidden, "accountDeleted")
		return
	}
	if err != nil {
		s.fail(w, r, "sign in", err)
		return
	}
	s.respondTokens(w, r, Session{UserID: user.ID, DeviceID: deviceID}, refresh, refreshExpires, &user)
}

// POST /v1/auth/refresh  {"refreshToken": "..."}. Mỗi refresh token chỉ dùng được một lần;
// app phải lưu token mới và không gọi refresh song song.
func (s *Service) refresh(w http.ResponseWriter, r *http.Request) {
	var body struct {
		RefreshToken string `json:"refreshToken"`
	}
	if !decode(w, r, &body) || body.RefreshToken == "" {
		httpx.Error(w, http.StatusBadRequest, "invalidRequest")
		return
	}
	next, nextHash, err := NewRefreshToken()
	if err != nil {
		s.fail(w, r, "new refresh token", err)
		return
	}
	now := s.now()
	nextExpires := now.Add(s.RefreshTTL)
	session, err := s.Store.Rotate(r.Context(), HashRefreshToken(body.RefreshToken), nextHash, nextExpires, now)
	switch {
	case errors.Is(err, ErrRefreshReused):
		s.Log.WarnContext(r.Context(), "refresh token reused; device signed out")
		httpx.Error(w, http.StatusUnauthorized, "refreshTokenReused")
		return
	case errors.Is(err, ErrRefreshInvalid):
		httpx.Error(w, http.StatusUnauthorized, "invalidRefreshToken")
		return
	case err != nil:
		s.fail(w, r, "rotate refresh token", err)
		return
	}
	s.respondTokens(w, r, session, next, nextExpires, nil)
}

// POST /v1/auth/logout  {"refreshToken": "..."}. Luôn trả 204, kể cả token không tồn tại.
func (s *Service) logout(w http.ResponseWriter, r *http.Request) {
	var body struct {
		RefreshToken string `json:"refreshToken"`
	}
	if !decode(w, r, &body) || body.RefreshToken == "" {
		httpx.Error(w, http.StatusBadRequest, "invalidRequest")
		return
	}
	if err := s.Store.Revoke(r.Context(), HashRefreshToken(body.RefreshToken), s.now()); err != nil {
		s.fail(w, r, "revoke refresh token", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// GET /v1/me: thông tin người đang đăng nhập. App dùng để kiểm tra đăng nhập còn hiệu lực.
func (s *Service) me(w http.ResponseWriter, r *http.Request) {
	session, _ := SessionFrom(r.Context())
	user, err := s.Store.User(r.Context(), session.UserID)
	if errors.Is(err, ErrUserNotFound) {
		httpx.Error(w, http.StatusUnauthorized, "accountDeleted")
		return
	}
	if err != nil {
		s.fail(w, r, "load user", err)
		return
	}
	httpx.JSON(w, http.StatusOK, user)
}

type sessionKey struct{}

// Require chỉ cho request có access token hợp lệ đi qua, và đặt Session vào context cho handler.
func (s *Service) Require(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if !ok || raw == "" {
			w.Header().Set("WWW-Authenticate", "Bearer")
			httpx.Error(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		session, err := s.Access.Parse(raw)
		if err != nil {
			w.Header().Set("WWW-Authenticate", `Bearer error="invalid_token"`)
			httpx.Error(w, http.StatusUnauthorized, "invalidAccessToken")
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), sessionKey{}, session)))
	})
}

// SessionFrom lấy người dùng đã đăng nhập mà Require đặt vào context.
func SessionFrom(ctx context.Context) (Session, bool) {
	s, ok := ctx.Value(sessionKey{}).(Session)
	return s, ok
}

func (s *Service) respondTokens(w http.ResponseWriter, r *http.Request, session Session, refresh string, refreshExpires time.Time, user *User) {
	access, accessExpires, err := s.Access.Issue(session)
	if err != nil {
		s.fail(w, r, "issue access token", err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	httpx.JSON(w, http.StatusOK, TokenResponse{
		AccessToken:           access,
		AccessTokenExpiresAt:  accessExpires.UTC(),
		RefreshToken:          refresh,
		RefreshTokenExpiresAt: refreshExpires.UTC(),
		User:                  user,
	})
}

func (s *Service) fail(w http.ResponseWriter, r *http.Request, what string, err error) {
	s.Log.ErrorContext(r.Context(), what, "err", err)
	httpx.Error(w, http.StatusInternalServerError, "internal")
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	return json.NewDecoder(r.Body).Decode(v) == nil
}
