// Package bootstrap phục vụ GET /v1/bootstrap: API duy nhất app biết sẵn, trả config cho app.
package bootstrap

import (
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/mthangit/tingting-server/internal/config"
	"github.com/mthangit/tingting-server/internal/httpx"
)

type Handler struct {
	Configs config.Store
	Log     *slog.Logger
}

func (h Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	cfg, err := h.Configs.Active(r.Context())
	if err != nil {
		if !errors.Is(err, config.ErrNoActiveConfig) {
			h.Log.ErrorContext(r.Context(), "load config", "err", err)
		}
		// App giữ config đang có và thử lại sau.
		httpx.Error(w, http.StatusServiceUnavailable, "configUnavailable")
		return
	}

	body, err := cfg.Body()
	if err != nil {
		h.Log.ErrorContext(r.Context(), "encode config", "version", cfg.Version, "err", err)
		httpx.Error(w, http.StatusInternalServerError, "internal")
		return
	}

	etag := config.ETag(cfg.Version, body)
	w.Header().Set("ETag", etag)
	// App tự quyết khi nào hỏi lại theo ttlSeconds; proxy không được cache vì sau này config có thể theo user.
	w.Header().Set("Cache-Control", "private, no-cache")
	w.Header().Set("Vary", "Authorization, X-App-Version, X-Platform")

	if matches(r.Header.Get("If-None-Match"), etag) {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_, _ = w.Write(body)
}

// matches so If-None-Match (có thể nhiều giá trị, có thể có W/) với ETag hiện tại.
func matches(header, etag string) bool {
	for _, candidate := range strings.Split(header, ",") {
		candidate = strings.TrimPrefix(strings.TrimSpace(candidate), "W/")
		if candidate == etag || candidate == "*" {
			return true
		}
	}
	return false
}
