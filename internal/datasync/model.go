// Package datasync đồng bộ giao dịch, tag, nguồn tiền giữa app và server.
//
// Đẩy lên: bản ghi có updatedAt mới hơn bản trên server thì ghi đè, cũ hơn hoặc bằng thì bỏ qua
// (gửi lại nhiều lần vẫn an toàn). Mỗi lần ghi, bản ghi nhận rev mới tăng dần theo user.
// Kéo về: mọi bản ghi có rev > cursor, kể cả bản đã xoá mềm.
package datasync

import (
	"database/sql/driver"
	"encoding/json"
	"fmt"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
)

// Time là thời điểm dạng ISO 8601 đến giây, khớp với SpendJSON trên app (.iso8601 không có phần lẻ giây).
type Time struct{ time.Time }

func (t Time) MarshalJSON() ([]byte, error) {
	return json.Marshal(t.UTC().Format(time.RFC3339))
}

func (t *Time) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return err
	}
	parsed, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return err
	}
	t.Time = parsed.UTC().Truncate(time.Second)
	return nil
}

func (t Time) Value() (driver.Value, error) { return t.Time, nil }

func (t *Time) Scan(src any) error {
	v, ok := src.(time.Time)
	if !ok {
		return fmt.Errorf("cannot scan %T into Time", src)
	}
	t.Time = v.UTC()
	return nil
}

// Transaction giống SpendCore.Transaction.
type Transaction struct {
	ID                 uuid.UUID  `json:"id"`
	Amount             int64      `json:"amount"`
	Kind               string     `json:"kind"`
	Note               string     `json:"note"`
	TagID              *uuid.UUID `json:"tagID"`
	OccurredAt         Time       `json:"occurredAt"`
	Source             string     `json:"source"`
	Origin             string     `json:"origin"`
	RawText            *string    `json:"rawText"`
	RawTitle           *string    `json:"rawTitle"`
	BalanceAfter       *int64     `json:"balanceAfter"`
	NeedsReview        bool       `json:"needsReview"`
	ExcludedFromTotals bool       `json:"excludedFromTotals"`
	CreatedAt          Time       `json:"createdAt"`
	UpdatedAt          Time       `json:"updatedAt"`
	DeletedAt          *Time      `json:"deletedAt"`
}

// Tag giống SpendCore.Tag.
type Tag struct {
	ID        uuid.UUID `json:"id"`
	Name      string    `json:"name"`
	Kind      string    `json:"kind"`
	Color     string    `json:"color"`
	SortOrder int       `json:"sortOrder"`
	IsHidden  bool      `json:"isHidden"`
	CreatedAt Time      `json:"createdAt"`
	UpdatedAt Time      `json:"updatedAt"`
	DeletedAt *Time     `json:"deletedAt"`
}

// Source giống SpendCore.PaymentSource.
type Source struct {
	ID        uuid.UUID `json:"id"`
	Name      string    `json:"name"`
	Kind      string    `json:"kind"`
	SortOrder int       `json:"sortOrder"`
	IsHidden  bool      `json:"isHidden"`
	CreatedAt Time      `json:"createdAt"`
	UpdatedAt Time      `json:"updatedAt"`
	DeletedAt *Time     `json:"deletedAt"`
}

// Changes là một nhóm thay đổi gửi lên hoặc trả về.
type Changes struct {
	Transactions []Transaction `json:"transactions"`
	Tags         []Tag         `json:"tags"`
	Sources      []Source      `json:"sources"`
}

func (c Changes) Len() int { return len(c.Transactions) + len(c.Tags) + len(c.Sources) }

// Rejected là bản ghi bị từ chối vì dữ liệu sai; app không cần gửi lại bản đó.
type Rejected struct {
	Type   string    `json:"type"`
	ID     uuid.UUID `json:"id"`
	Reason string    `json:"reason"`
}

var (
	transactionKinds = map[string]bool{"expense": true, "income": true}
	origins          = map[string]bool{"manual": true, "notification": true}
	sourceKinds      = map[string]bool{"cash": true, "wallet": true, "bank": true, "other": true}
	tagColors        = map[string]bool{
		"orange": true, "blue": true, "pink": true, "purple": true, "teal": true, "amber": true,
		"slate": true, "red": true, "gray": true, "green": true, "lime": true, "cyan": true,
	}
)

// maxFuture: updatedAt ở tương lai xa (đồng hồ máy sai) sẽ thắng mãi mọi bản sửa sau, nên từ chối.
const maxFuture = 24 * time.Hour

func validTimes(created, updated Time, now time.Time) string {
	switch {
	case created.IsZero() || updated.IsZero():
		return "missingTimestamp"
	case updated.After(now.Add(maxFuture)):
		return "updatedAtInFuture"
	}
	return ""
}

func (t Transaction) problem(now time.Time) string {
	switch {
	case t.ID == uuid.Nil:
		return "missingId"
	case t.Amount <= 0 || t.Amount > 1_000_000_000_000_000:
		return "invalidAmount"
	case !transactionKinds[t.Kind]:
		return "invalidKind"
	case !origins[t.Origin]:
		return "invalidOrigin"
	case t.OccurredAt.IsZero():
		return "missingOccurredAt"
	case tooLong(t.Note, 1000) || tooLong(t.Source, 200) || tooLongPtr(t.RawText, 5000) || tooLongPtr(t.RawTitle, 500):
		return "textTooLong"
	}
	return validTimes(t.CreatedAt, t.UpdatedAt, now)
}

func (t Tag) problem(now time.Time) string {
	switch {
	case t.ID == uuid.Nil:
		return "missingId"
	case t.Name == "" || tooLong(t.Name, 100):
		return "invalidName"
	case !transactionKinds[t.Kind]:
		return "invalidKind"
	case !tagColors[t.Color]:
		return "invalidColor"
	}
	return validTimes(t.CreatedAt, t.UpdatedAt, now)
}

func (s Source) problem(now time.Time) string {
	switch {
	case s.ID == uuid.Nil:
		return "missingId"
	case s.Name == "" || tooLong(s.Name, 100):
		return "invalidName"
	case !sourceKinds[s.Kind]:
		return "invalidKind"
	}
	return validTimes(s.CreatedAt, s.UpdatedAt, now)
}

// Validate tách bản ghi hợp lệ khỏi bản ghi sai.
func (c Changes) Validate(now time.Time) (Changes, []Rejected) {
	var ok Changes
	var rejected []Rejected
	for _, t := range c.Transactions {
		if p := t.problem(now); p != "" {
			rejected = append(rejected, Rejected{"transaction", t.ID, p})
		} else {
			ok.Transactions = append(ok.Transactions, t)
		}
	}
	for _, t := range c.Tags {
		if p := t.problem(now); p != "" {
			rejected = append(rejected, Rejected{"tag", t.ID, p})
		} else {
			ok.Tags = append(ok.Tags, t)
		}
	}
	for _, s := range c.Sources {
		if p := s.problem(now); p != "" {
			rejected = append(rejected, Rejected{"source", s.ID, p})
		} else {
			ok.Sources = append(ok.Sources, s)
		}
	}
	return ok, rejected
}

func tooLong(s string, max int) bool { return utf8.RuneCountInString(s) > max }

func tooLongPtr(s *string, max int) bool { return s != nil && tooLong(*s, max) }
