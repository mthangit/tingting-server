package datasync

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

var now = time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)

func at(t time.Time) Time { return Time{t} }

func validTransaction() Transaction {
	return Transaction{
		ID: uuid.New(), Amount: 89000, Kind: "expense", Note: "GRAB", OccurredAt: at(now),
		Source: "MB Bank", Origin: "notification", CreatedAt: at(now), UpdatedAt: at(now),
	}
}

func TestDecodesAppJSON(t *testing.T) {
	// Đúng định dạng SpendJSON của app: UUID viết hoa, ngày ISO 8601 không có phần lẻ giây, trường nil bị bỏ.
	raw := `{"amount":89000,"balanceAfter":4210000,"createdAt":"2026-10-24T05:10:00Z","excludedFromTotals":false,
	"id":"3F2504E0-4F89-11D3-9A0C-0305E82C3301","kind":"expense","needsReview":false,"note":"GRAB cơm trưa",
	"occurredAt":"2026-10-24T05:10:00Z","origin":"notification","source":"MB Bank",
	"tagID":"6E0C7A90-0000-4000-8000-000000000001","updatedAt":"2026-10-24T05:10:00.75Z"}`
	var tx Transaction
	if err := json.Unmarshal([]byte(raw), &tx); err != nil {
		t.Fatal(err)
	}
	if tx.TagID == nil || tx.BalanceAfter == nil || *tx.BalanceAfter != 4210000 || tx.DeletedAt != nil || tx.RawText != nil {
		t.Fatalf("decoded: %+v", tx)
	}
	if tx.UpdatedAt.Nanosecond() != 0 {
		t.Fatal("timestamps should be truncated to seconds like the app")
	}
	if p := tx.problem(now); p != "" {
		t.Fatalf("expected valid, got %s", p)
	}

	out, _ := json.Marshal(tx)
	if !strings.Contains(string(out), `"updatedAt":"2026-10-24T05:10:00Z"`) || !strings.Contains(string(out), `"deletedAt":null`) {
		t.Fatalf("encoded: %s", out)
	}
}

func TestValidateRejectsBadRecords(t *testing.T) {
	future := at(now.Add(48 * time.Hour))
	cases := map[string]func(*Transaction){
		"invalidAmount":     func(tx *Transaction) { tx.Amount = 0 },
		"invalidKind":       func(tx *Transaction) { tx.Kind = "transfer" },
		"invalidOrigin":     func(tx *Transaction) { tx.Origin = "sms" },
		"missingId":         func(tx *Transaction) { tx.ID = uuid.Nil },
		"textTooLong":       func(tx *Transaction) { tx.Note = strings.Repeat("a", 1001) },
		"missingTimestamp":  func(tx *Transaction) { tx.UpdatedAt = Time{} },
		"updatedAtInFuture": func(tx *Transaction) { tx.UpdatedAt = future },
	}
	for reason, edit := range cases {
		tx := validTransaction()
		edit(&tx)
		ok, rejected := Changes{Transactions: []Transaction{tx, validTransaction()}}.Validate(now)
		if len(ok.Transactions) != 1 || len(rejected) != 1 || rejected[0].Reason != reason {
			t.Errorf("%s: ok=%d rejected=%+v", reason, len(ok.Transactions), rejected)
		}
	}

	tag := Tag{ID: uuid.New(), Name: "Ăn uống", Kind: "expense", Color: "neon", CreatedAt: at(now), UpdatedAt: at(now)}
	source := Source{ID: uuid.New(), Name: "", Kind: "bank", CreatedAt: at(now), UpdatedAt: at(now)}
	_, rejected := Changes{Tags: []Tag{tag}, Sources: []Source{source}}.Validate(now)
	if len(rejected) != 2 || rejected[0].Reason != "invalidColor" || rejected[1].Reason != "invalidName" {
		t.Fatalf("rejected: %+v", rejected)
	}
}
