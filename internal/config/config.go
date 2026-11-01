// Package config chứa config mà app tải về qua API bootstrap: kiểm tra hợp lệ, lưu theo version và trả cho app.
package config

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"
)

// SchemaVersion là phiên bản cấu trúc config mà server này hiểu.
// Đổi cấu trúc theo kiểu app cũ không đọc được thì tăng số này.
const SchemaVersion = 1

// ErrNoActiveConfig: chưa có config nào được bật.
var ErrNoActiveConfig = errors.New("no active config")

// Config là một version config đã lưu. Payload giữ nguyên JSON gốc, kể cả trường server không biết,
// để thêm trường mới cho app mà không cần sửa server.
type Config struct {
	Version       int64
	SchemaVersion int
	Payload       json.RawMessage
}

// Body là JSON trả cho app: payload kèm schemaVersion và configVersion do server điền.
func (c Config) Body() ([]byte, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(c.Payload, &fields); err != nil {
		return nil, fmt.Errorf("decode payload: %w", err)
	}
	fields["schemaVersion"] = json.RawMessage(fmt.Sprint(c.SchemaVersion))
	fields["configVersion"] = json.RawMessage(fmt.Sprint(c.Version))
	return json.Marshal(fields)
}

// ETag theo version và nội dung, để app chỉ tải lại khi config thật sự đổi.
func ETag(version int64, body []byte) string {
	sum := sha256.Sum256(body)
	return fmt.Sprintf(`"cfg-%d-%s"`, version, hex.EncodeToString(sum[:])[:12])
}

// Store lưu config theo version. Chỉ một version được bật tại một thời điểm.
type Store interface {
	Active(ctx context.Context) (Config, error)
}

// Phần của config mà server kiểm tra. Các trường khác được giữ nguyên, không kiểm tra.
type payload struct {
	SchemaVersion *int `json:"schemaVersion"`
	TTLSeconds    int  `json:"ttlSeconds"`
	App           struct {
		MinVersion    string `json:"minVersion"`
		LatestVersion string `json:"latestVersion"`
	} `json:"app"`
	API struct {
		BaseURL   string              `json:"baseUrl"`
		Endpoints map[string]endpoint `json:"endpoints"`
	} `json:"api"`
	Features map[string]bool `json:"features"`
	Sync     *struct {
		IntervalSeconds int `json:"intervalSeconds"`
		DebounceSeconds int `json:"debounceSeconds"`
		PushBatchSize   int `json:"pushBatchSize"`
		PullPageSize    int `json:"pullPageSize"`
	} `json:"sync"`
	Catalog *struct {
		Sources *struct {
			Version int             `json:"version"`
			Items   []catalogSource `json:"items"`
		} `json:"sources"`
	} `json:"catalog"`
}

// catalogSource là một nguồn tiền mặc định app cài sẵn. ID cố định để hai máy không bị nhân đôi khi đồng bộ.
type catalogSource struct {
	ID        string   `json:"id"`
	Name      string   `json:"name"`
	Kind      string   `json:"kind"`
	SortOrder int      `json:"sortOrder"`
	LogoURL   string   `json:"logoUrl"`
	Aliases   []string `json:"aliases"`
}

type endpoint struct {
	Method    string `json:"method"`
	Path      string `json:"path"`
	TimeoutMs int    `json:"timeoutMs"`
}

var (
	semverPattern       = regexp.MustCompile(`^\d+\.\d+\.\d+$`)
	endpointNamePattern = regexp.MustCompile(`^[a-z][a-zA-Z0-9]*(\.[a-z][a-zA-Z0-9]*)*$`)
	allowedMethods      = map[string]bool{"GET": true, "POST": true, "PUT": true, "PATCH": true, "DELETE": true}
	uuidPattern         = regexp.MustCompile(`^[0-9A-Fa-f]{8}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{12}$`)
	aliasPattern        = regexp.MustCompile(`^[a-z0-9]+$`)
	sourceKinds         = map[string]bool{"cash": true, "wallet": true, "bank": true, "other": true}
)

// Validate kiểm tra config trước khi lưu. Config sai có thể làm hỏng app trên mọi máy, nên chặn từ server.
// allowedHosts là các domain app được phép gọi, ví dụ "*.run.app"; logoHosts là nơi chứa logo (bucket R2 public),
// ví dụ "*.r2.dev". App cũng tự kiểm tra lại cả hai.
func Validate(raw []byte, allowedHosts, logoHosts []string) error {
	var p payload
	if err := json.Unmarshal(raw, &p); err != nil {
		return fmt.Errorf("config không phải JSON object hợp lệ: %w", err)
	}

	var problems []string
	add := func(format string, args ...any) { problems = append(problems, fmt.Sprintf(format, args...)) }

	if p.SchemaVersion != nil && *p.SchemaVersion != SchemaVersion {
		add("schemaVersion phải là %d", SchemaVersion)
	}
	if p.TTLSeconds < 60 {
		add("ttlSeconds phải >= 60")
	}
	if !semverPattern.MatchString(p.App.MinVersion) {
		add("app.minVersion phải có dạng 1.2.3")
	}
	if p.App.LatestVersion != "" && !semverPattern.MatchString(p.App.LatestVersion) {
		add("app.latestVersion phải có dạng 1.2.3")
	}

	if err := checkBaseURL(p.API.BaseURL, allowedHosts); err != nil {
		add("api.baseUrl: %v", err)
	}
	if len(p.API.Endpoints) == 0 {
		add("api.endpoints không được rỗng")
	}
	for name, e := range p.API.Endpoints {
		if !endpointNamePattern.MatchString(name) {
			add("api.endpoints: tên %q phải có dạng nhom.tenApi", name)
		}
		if !allowedMethods[e.Method] {
			add("api.endpoints.%s.method %q không hợp lệ", name, e.Method)
		}
		if !strings.HasPrefix(e.Path, "/") || strings.Contains(e.Path, "://") {
			add("api.endpoints.%s.path phải bắt đầu bằng /", name)
		}
		if e.TimeoutMs < 0 {
			add("api.endpoints.%s.timeoutMs không được âm", name)
		}
	}

	if s := p.Sync; s != nil {
		if s.IntervalSeconds < 60 {
			add("sync.intervalSeconds phải >= 60")
		}
		if s.DebounceSeconds < 0 || s.PushBatchSize < 1 || s.PullPageSize < 1 {
			add("sync: debounceSeconds >= 0, pushBatchSize và pullPageSize >= 1")
		}
	}

	if p.Catalog != nil && p.Catalog.Sources != nil {
		sources := p.Catalog.Sources
		if sources.Version < 1 {
			add("catalog.sources.version phải >= 1")
		}
		ids, names := map[string]bool{}, map[string]bool{}
		for i, s := range sources.Items {
			at := fmt.Sprintf("catalog.sources.items[%d]", i)
			id, name := strings.ToUpper(s.ID), strings.ToLower(strings.TrimSpace(s.Name))
			if !uuidPattern.MatchString(s.ID) {
				add("%s.id phải là UUID", at)
			} else if ids[id] {
				add("%s.id %s bị trùng", at, s.ID)
			}
			if name == "" {
				add("%s.name không được rỗng", at)
			} else if names[name] {
				add("%s.name %q bị trùng", at, s.Name)
			}
			ids[id], names[name] = true, true
			if !sourceKinds[s.Kind] {
				add("%s.kind %q phải là cash, wallet, bank hoặc other", at, s.Kind)
			}
			if s.SortOrder < 0 {
				add("%s.sortOrder không được âm", at)
			}
			if s.LogoURL != "" {
				if err := checkLogoURL(s.LogoURL, logoHosts); err != nil {
					add("%s.logoUrl: %v", at, err)
				}
			}
			for _, alias := range s.Aliases {
				if !aliasPattern.MatchString(alias) {
					add("%s.aliases: %q chỉ gồm chữ thường không dấu và số", at, alias)
				}
			}
		}
	}

	if len(problems) > 0 {
		return fmt.Errorf("config không hợp lệ:\n- %s", strings.Join(problems, "\n- "))
	}
	return nil
}

// checkBaseURL: chỉ cho https (http chỉ cho localhost khi phát triển) và domain nằm trong danh sách cho phép.
func checkBaseURL(raw string, allowedHosts []string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return fmt.Errorf("%q không phải URL", raw)
	}
	if u.Path != "" && u.Path != "/" || u.RawQuery != "" {
		return fmt.Errorf("chỉ gồm scheme và host, đường dẫn đặt trong từng endpoint")
	}
	host := u.Hostname()
	local := host == "localhost" || host == "127.0.0.1"
	if u.Scheme != "https" && !(u.Scheme == "http" && local) {
		return fmt.Errorf("phải dùng https")
	}
	if !HostAllowed(host, allowedHosts) {
		return fmt.Errorf("domain %q không nằm trong danh sách cho phép %v", host, allowedHosts)
	}
	return nil
}

// checkLogoURL: logo phải là URL https đầy đủ trên domain chứa logo (bucket R2), để app không tải ảnh từ nơi lạ.
func checkLogoURL(raw string, logoHosts []string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return fmt.Errorf("%q không phải URL đầy đủ", raw)
	}
	if u.Scheme != "https" {
		return fmt.Errorf("phải dùng https")
	}
	if !HostAllowed(u.Hostname(), logoHosts) {
		return fmt.Errorf("domain %q không nằm trong danh sách logo cho phép %v", u.Hostname(), logoHosts)
	}
	return nil
}

// HostAllowed so host với danh sách như "api.ting.app", "*.run.app".
func HostAllowed(host string, allowed []string) bool {
	host = strings.ToLower(host)
	for _, pattern := range allowed {
		pattern = strings.ToLower(strings.TrimSpace(pattern))
		if suffix, ok := strings.CutPrefix(pattern, "*."); ok {
			if strings.HasSuffix(host, "."+suffix) {
				return true
			}
		} else if host == pattern {
			return true
		}
	}
	return false
}
