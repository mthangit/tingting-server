// Server API của Ting.
//
// Biến môi trường:
//
//	PORT                   cổng lắng nghe (Cloud Run tự đặt), mặc định 8080
//	DATABASE_URL           Postgres (Neon). Bắt buộc, trừ khi dùng TING_CONFIG_FILE
//	TING_CONFIG_FILE       đọc config từ file thay vì database, dùng khi chạy trên máy
//	TING_ALLOWED_API_HOSTS domain mà config được trỏ tới, phân cách bằng dấu phẩy. Mặc định "*.run.app,localhost,127.0.0.1"
//	TING_ALLOWED_LOGO_HOSTS domain chứa logo nguồn tiền (bucket R2 public). Mặc định "*.r2.dev"
//	TING_GOOGLE_CLIENT_IDS OAuth client ID của app (Google Sign-In), phân cách bằng dấu phẩy. Có thì bật đăng nhập
//	TING_JWT_SECRET        khoá ký access token, ít nhất 32 ký tự. Bắt buộc khi bật đăng nhập
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/mthangit/tingting-server/internal/auth"
	"github.com/mthangit/tingting-server/internal/bootstrap"
	"github.com/mthangit/tingting-server/internal/config"
	"github.com/mthangit/tingting-server/internal/datasync"
	"github.com/mthangit/tingting-server/internal/db"
	"github.com/mthangit/tingting-server/internal/httpx"
)

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	if err := run(log); err != nil {
		log.Error("server stopped", "err", err)
		os.Exit(1)
	}
}

func run(log *slog.Logger) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	allowedHosts := splitList(env("TING_ALLOWED_API_HOSTS", "*.run.app,localhost,127.0.0.1"))
	logoHosts := splitList(env("TING_ALLOWED_LOGO_HOSTS", "*.r2.dev"))

	mux := http.NewServeMux()

	var configs config.Store
	if path := os.Getenv("TING_CONFIG_FILE"); path != "" {
		log.Info("using config file", "path", path)
		configs = config.FileStore{Path: path, AllowedHosts: allowedHosts, LogoHosts: logoHosts}
	} else {
		url := os.Getenv("DATABASE_URL")
		if url == "" {
			return errors.New("cần DATABASE_URL hoặc TING_CONFIG_FILE")
		}
		pool, err := db.Open(ctx, url)
		if err != nil {
			return err
		}
		defer pool.Close()
		configs = config.PGStore{DB: pool}

		authService, err := newAuth(pool, log)
		if err != nil {
			return err
		}
		if authService != nil {
			authService.Register(mux)
			// Đồng bộ cần biết dữ liệu của ai, nên chỉ bật khi có đăng nhập.
			(&datasync.Service{Store: datasync.PGStore{DB: pool}, Log: log}).Register(mux, authService.Require)
			log.Info("sign in with google and sync enabled")
		}
	}

	mux.Handle("GET /v1/bootstrap", bootstrap.Handler{
		Configs: &config.Cached{Store: configs, TTL: 30 * time.Second},
		Log:     log,
	})
	// Không dùng /healthz: Cloud Run giữ riêng các đường dẫn kết thúc bằng "z".
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})

	limiter := &httpx.RateLimiter{Policy: rateLimitPolicy}
	server := &http.Server{
		Addr:              ":" + env("PORT", "8080"),
		Handler:           httpx.Logging(log, limiter.Middleware(mux)),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	errs := make(chan error, 1)
	go func() {
		log.Info("listening", "addr", server.Addr)
		errs <- server.ListenAndServe()
	}()

	select {
	case err := <-errs:
		return err
	case <-ctx.Done():
	}

	// Cloud Run cho khoảng 10 giây sau SIGTERM để xử lý nốt request.
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	return server.Shutdown(shutdownCtx)
}

// rateLimitPolicy: giới hạn số request mỗi phút theo IP. Vượt thì trả 429 kèm Retry-After.
func rateLimitPolicy(r *http.Request) (string, httpx.Limit, bool) {
	switch path := r.URL.Path; {
	case path == "/health":
		return "", httpx.Limit{}, false
	case path == "/v1/bootstrap":
		return "bootstrap", httpx.Limit{PerMinute: 30, Burst: 10}, true
	case strings.HasPrefix(path, "/v1/auth/"):
		return "auth", httpx.Limit{PerMinute: 10, Burst: 5}, true
	default:
		return "api", httpx.Limit{PerMinute: 120, Burst: 60}, true
	}
}

// newAuth bật đăng nhập khi có TING_GOOGLE_CLIENT_IDS. Đăng nhập cần database nên không có ở chế độ config file.
func newAuth(pool *pgxpool.Pool, log *slog.Logger) (*auth.Service, error) {
	clientIDs := splitList(os.Getenv("TING_GOOGLE_CLIENT_IDS"))
	if len(clientIDs) == 0 {
		log.Info("sign in disabled: TING_GOOGLE_CLIENT_IDS not set")
		return nil, nil
	}
	secret := os.Getenv("TING_JWT_SECRET")
	if len(secret) < 32 {
		return nil, errors.New("TING_JWT_SECRET phải có ít nhất 32 ký tự")
	}
	return &auth.Service{
		Google:     &auth.GoogleVerifier{ClientIDs: clientIDs},
		Store:      auth.PGStore{DB: pool},
		Access:     auth.AccessTokens{Secret: []byte(secret), TTL: 15 * time.Minute},
		RefreshTTL: 30 * 24 * time.Hour,
		Log:        log,
	}, nil
}

func splitList(s string) []string {
	var out []string
	for _, part := range strings.Split(s, ",") {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, part)
		}
	}
	return out
}

func env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
