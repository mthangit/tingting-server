package auth

import (
	"context"
	"sync"
	"time"

	"github.com/google/uuid"
)

// MemoryStore giữ dữ liệu đăng nhập trong bộ nhớ, dùng cho test. Hành vi giống PGStore.
type MemoryStore struct {
	mu         sync.Mutex
	identities map[string]uuid.UUID // provider + "|" + subject → user
	emails     map[uuid.UUID]string
	refresh    map[string]*memoryRefresh
}

type memoryRefresh struct {
	session   Session
	expiresAt time.Time
	revokedAt *time.Time
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{
		identities: map[string]uuid.UUID{},
		emails:     map[uuid.UUID]string{},
		refresh:    map[string]*memoryRefresh{},
	}
}

func (m *MemoryStore) SignIn(_ context.Context, id Identity, device Device, refreshHash string, refreshExpires time.Time) (User, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	key := id.Provider + "|" + id.Subject
	userID, ok := m.identities[key]
	if !ok {
		userID = uuid.New()
		m.identities[key] = userID
	}
	if id.Email != "" {
		m.emails[userID] = id.Email
	}
	m.refresh[refreshHash] = &memoryRefresh{session: Session{UserID: userID, DeviceID: device.ID}, expiresAt: refreshExpires}
	return User{ID: userID, Email: id.Email}, nil
}

func (m *MemoryStore) Rotate(_ context.Context, oldHash, newHash string, newExpires, now time.Time) (Session, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	old, ok := m.refresh[oldHash]
	if !ok {
		return Session{}, ErrRefreshInvalid
	}
	if old.revokedAt != nil {
		for _, r := range m.refresh {
			if r.session == old.session && r.revokedAt == nil {
				r.revokedAt = &now
			}
		}
		return Session{}, ErrRefreshReused
	}
	if !old.expiresAt.After(now) {
		return Session{}, ErrRefreshInvalid
	}
	old.revokedAt = &now
	m.refresh[newHash] = &memoryRefresh{session: old.session, expiresAt: newExpires}
	return old.session, nil
}

func (m *MemoryStore) Revoke(_ context.Context, hash string, now time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if r, ok := m.refresh[hash]; ok && r.revokedAt == nil {
		r.revokedAt = &now
	}
	return nil
}

func (m *MemoryStore) User(_ context.Context, id uuid.UUID) (User, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, userID := range m.identities {
		if userID == id {
			return User{ID: id, Email: m.emails[id]}, nil
		}
	}
	return User{}, ErrUserNotFound
}
