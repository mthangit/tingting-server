// Package httpx gồm các phần HTTP dùng chung: trả lỗi, log request, bắt panic.
package httpx

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"time"
)

// Error trả lỗi dạng {"error": "<mã>"}. Mã lỗi viết camelCase, ổn định để app dựa vào.
func Error(w http.ResponseWriter, status int, code string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": code})
}

// JSON trả body dạng JSON.
func JSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// Logging ghi mỗi request một dòng: method, path, status, thời gian, phiên bản app.
// Không ghi body hay query vì có thể chứa dữ liệu giao dịch.
func Logging(log *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		defer func() {
			if v := recover(); v != nil {
				log.ErrorContext(r.Context(), "panic", "path", r.URL.Path, "panic", v)
				Error(rec, http.StatusInternalServerError, "internal")
			}
			log.InfoContext(r.Context(), "request",
				"method", r.Method,
				"path", r.URL.Path,
				"status", rec.status,
				"durationMs", time.Since(start).Milliseconds(),
				"appVersion", r.Header.Get("X-App-Version"),
				"platform", r.Header.Get("X-Platform"),
				"clientIP", ClientIP(r),
			)
		}()
		next.ServeHTTP(rec, r)
	})
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(status int) {
	r.status = status
	r.ResponseWriter.WriteHeader(status)
}
