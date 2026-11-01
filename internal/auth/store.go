package auth

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
)

var (
	// ErrRefreshInvalid: refresh token không tồn tại hoặc đã hết hạn.
	ErrRefreshInvalid = errors.New("refresh token invalid")
	// ErrRefreshReused: refresh token đã bị thu hồi mà vẫn được gửi lên, coi như bị lộ.
	// Mọi refresh token của máy đó bị thu hồi, máy phải đăng nhập lại.
	ErrRefreshReused = errors.New("refresh token reused")
)

// Device là thông tin máy gửi lên khi đăng nhập.
type Device struct {
	ID         uuid.UUID
	Platform   string
	AppVersion string
}

// User là thông tin trả cho app sau khi đăng nhập.
type User struct {
	ID    uuid.UUID `json:"id"`
	Email string    `json:"email"`
}

// Store lưu user, cách đăng nhập, máy và refresh token.
type Store interface {
	// SignIn tìm user theo identity (tạo mới nếu chưa có), ghi nhận máy, lưu refresh token mới.
	SignIn(ctx context.Context, id Identity, device Device, refreshHash string, refreshExpires time.Time) (User, error)
	// Rotate thu hồi refresh token cũ và lưu token mới cho cùng user, cùng máy.
	Rotate(ctx context.Context, oldHash, newHash string, newExpires, now time.Time) (Session, error)
	// Revoke thu hồi một refresh token (đăng xuất). Token không tồn tại thì bỏ qua.
	Revoke(ctx context.Context, hash string, now time.Time) error
	// User trả thông tin user, kèm email của cách đăng nhập đầu tiên.
	User(ctx context.Context, id uuid.UUID) (User, error)
}

// ErrUserNotFound: user đã bị xoá.
var ErrUserNotFound = errors.New("user not found")
