package httpx

import (
	"fmt"
	"math"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"golang.org/x/time/rate"
)

// Limit là số request tối đa mỗi phút cho một IP, cho phép dồn tối đa Burst request cùng lúc.
type Limit struct {
	PerMinute int
	Burst     int
}

// RateLimiter giới hạn số request theo IP, đếm trong bộ nhớ của từng instance.
// Đủ để chặn một nguồn spam; tấn công từ rất nhiều IP thì cần chặn ở tầng trước (Cloudflare, Cloud Armor).
type RateLimiter struct {
	// Policy chọn giới hạn theo request; trả ok = false thì không giới hạn.
	Policy func(r *http.Request) (name string, limit Limit, ok bool)
	Now    func() time.Time

	mu       sync.Mutex
	visitors map[string]*visitor
	swept    time.Time
}

type visitor struct {
	limiter  *rate.Limiter
	lastSeen time.Time
}

func (l *RateLimiter) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name, limit, ok := l.Policy(r)
		if !ok {
			next.ServeHTTP(w, r)
			return
		}
		limiter := l.limiter(name+"|"+ClientIP(r), limit)
		reservation := limiter.ReserveN(l.now(), 1)
		if delay := reservation.DelayFrom(l.now()); delay > 0 {
			reservation.CancelAt(l.now())
			w.Header().Set("Retry-After", fmt.Sprint(int(math.Ceil(delay.Seconds()))))
			Error(w, http.StatusTooManyRequests, "rateLimited")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (l *RateLimiter) limiter(key string, limit Limit) *rate.Limiter {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	if l.visitors == nil {
		l.visitors = map[string]*visitor{}
	}
	// Dọn IP lâu không gọi để bộ nhớ không phình ra khi bị scan từ nhiều IP.
	if now.Sub(l.swept) > 5*time.Minute {
		for k, v := range l.visitors {
			if now.Sub(v.lastSeen) > 10*time.Minute {
				delete(l.visitors, k)
			}
		}
		l.swept = now
	}
	v, ok := l.visitors[key]
	if !ok {
		v = &visitor{limiter: rate.NewLimiter(rate.Limit(float64(limit.PerMinute)/60), limit.Burst)}
		l.visitors[key] = v
	}
	v.lastSeen = now
	return v.limiter
}

func (l *RateLimiter) now() time.Time {
	if l.Now != nil {
		return l.Now()
	}
	return time.Now()
}

// ClientIP lấy IP người gọi. Sau Google Front End (Cloud Run), IP thật là phần tử cuối của X-Forwarded-For
// (các phần tử trước do client tự gửi nên có thể giả). Chạy trực tiếp thì dùng địa chỉ kết nối.
func ClientIP(r *http.Request) string {
	if forwarded := r.Header.Get("X-Forwarded-For"); forwarded != "" {
		parts := strings.Split(forwarded, ",")
		if ip := strings.TrimSpace(parts[len(parts)-1]); ip != "" {
			return ip
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
