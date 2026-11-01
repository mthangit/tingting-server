package datasync

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/google/uuid"

	"github.com/mthangit/tingting-server/internal/auth"
	"github.com/mthangit/tingting-server/internal/httpx"
)

// Store lưu và đọc dữ liệu đồng bộ.
type Store interface {
	Push(ctx context.Context, userID uuid.UUID, c Changes) (accepted, ignored int, err error)
	Pull(ctx context.Context, userID uuid.UUID, cursor int64, limit int) (Changes, int64, bool, error)
}

const (
	// MaxPushRecords: số bản ghi tối đa mỗi lần đẩy. App chia lô theo sync.pushBatchSize trong config.
	MaxPushRecords = 500
	// MaxPullLimit: số bản ghi tối đa mỗi trang kéo về.
	MaxPullLimit = 1000
	defaultPull  = 500
)

type Service struct {
	Store Store
	Log   *slog.Logger
	Now   func() time.Time
}

// Register gắn API đồng bộ; require là middleware bắt buộc đăng nhập.
func (s *Service) Register(mux *http.ServeMux, require func(http.Handler) http.Handler) {
	mux.Handle("POST /v1/sync/push", require(http.HandlerFunc(s.push)))
	mux.Handle("GET /v1/sync/pull", require(http.HandlerFunc(s.pull)))
}

// PushResponse: accepted = đã ghi, ignored = server đã có bản mới hơn (hoặc bằng), rejected = dữ liệu sai.
type PushResponse struct {
	Accepted int        `json:"accepted"`
	Ignored  int        `json:"ignored"`
	Rejected []Rejected `json:"rejected"`
}

// POST /v1/sync/push  {"changes": {"transactions": [...], "tags": [...], "sources": [...]}}
func (s *Service) push(w http.ResponseWriter, r *http.Request) {
	session, _ := auth.SessionFrom(r.Context())
	var body struct {
		Changes Changes `json:"changes"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 4<<20)
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			httpx.Error(w, http.StatusRequestEntityTooLarge, "tooManyChanges")
			return
		}
		httpx.Error(w, http.StatusBadRequest, "invalidRequest")
		return
	}
	if body.Changes.Len() > MaxPushRecords {
		httpx.Error(w, http.StatusRequestEntityTooLarge, "tooManyChanges")
		return
	}

	valid, rejected := body.Changes.Validate(s.now())
	accepted, ignored, err := s.Store.Push(r.Context(), session.UserID, valid)
	if err != nil {
		s.Log.ErrorContext(r.Context(), "sync push", "err", err)
		httpx.Error(w, http.StatusInternalServerError, "internal")
		return
	}
	if rejected == nil {
		rejected = []Rejected{}
	}
	s.Log.InfoContext(r.Context(), "sync push", "accepted", accepted, "ignored", ignored, "rejected", len(rejected))
	httpx.JSON(w, http.StatusOK, PushResponse{Accepted: accepted, Ignored: ignored, Rejected: rejected})
}

// PullResponse: gọi tiếp với cursor = nextCursor khi hasMore.
type PullResponse struct {
	Changes    Changes `json:"changes"`
	NextCursor int64   `json:"nextCursor"`
	HasMore    bool    `json:"hasMore"`
}

// GET /v1/sync/pull?cursor=<rev>&limit=<n>
func (s *Service) pull(w http.ResponseWriter, r *http.Request) {
	session, _ := auth.SessionFrom(r.Context())
	query := r.URL.Query()

	cursor := int64(0)
	if raw := query.Get("cursor"); raw != "" {
		v, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || v < 0 {
			httpx.Error(w, http.StatusBadRequest, "invalidCursor")
			return
		}
		cursor = v
	}
	limit := defaultPull
	if raw := query.Get("limit"); raw != "" {
		v, err := strconv.Atoi(raw)
		if err != nil || v < 1 {
			httpx.Error(w, http.StatusBadRequest, "invalidLimit")
			return
		}
		limit = min(v, MaxPullLimit)
	}

	changes, next, hasMore, err := s.Store.Pull(r.Context(), session.UserID, cursor, limit)
	if err != nil {
		s.Log.ErrorContext(r.Context(), "sync pull", "err", err)
		httpx.Error(w, http.StatusInternalServerError, "internal")
		return
	}
	httpx.JSON(w, http.StatusOK, PullResponse{Changes: changes, NextCursor: next, HasMore: hasMore})
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}
