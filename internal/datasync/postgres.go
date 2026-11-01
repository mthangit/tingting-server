package datasync

import (
	"context"
	"fmt"
	"sort"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// PGStore lưu dữ liệu đồng bộ trong Postgres.
type PGStore struct {
	DB *pgxpool.Pool
}

// Chỉ ghi đè khi bản gửi lên mới hơn; ngược lại câu lệnh không đụng dòng nào (RowsAffected = 0).
const (
	upsertTransaction = `
INSERT INTO transactions (user_id, id, amount, kind, note, tag_id, occurred_at, source, origin, raw_title, raw_text,
                          balance_after, needs_review, excluded_from_totals, created_at, updated_at, deleted_at, rev)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18)
ON CONFLICT (user_id, id) DO UPDATE SET
    amount = EXCLUDED.amount, kind = EXCLUDED.kind, note = EXCLUDED.note, tag_id = EXCLUDED.tag_id,
    occurred_at = EXCLUDED.occurred_at, source = EXCLUDED.source, origin = EXCLUDED.origin,
    raw_title = EXCLUDED.raw_title, raw_text = EXCLUDED.raw_text, balance_after = EXCLUDED.balance_after,
    needs_review = EXCLUDED.needs_review, excluded_from_totals = EXCLUDED.excluded_from_totals,
    created_at = EXCLUDED.created_at, updated_at = EXCLUDED.updated_at, deleted_at = EXCLUDED.deleted_at,
    rev = EXCLUDED.rev
WHERE transactions.updated_at < EXCLUDED.updated_at`

	upsertTag = `
INSERT INTO tags (user_id, id, name, kind, color, sort_order, is_hidden, created_at, updated_at, deleted_at, rev)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
ON CONFLICT (user_id, id) DO UPDATE SET
    name = EXCLUDED.name, kind = EXCLUDED.kind, color = EXCLUDED.color, sort_order = EXCLUDED.sort_order,
    is_hidden = EXCLUDED.is_hidden, created_at = EXCLUDED.created_at, updated_at = EXCLUDED.updated_at,
    deleted_at = EXCLUDED.deleted_at, rev = EXCLUDED.rev
WHERE tags.updated_at < EXCLUDED.updated_at`

	upsertSource = `
INSERT INTO payment_sources (user_id, id, name, kind, sort_order, is_hidden, created_at, updated_at, deleted_at, rev)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
ON CONFLICT (user_id, id) DO UPDATE SET
    name = EXCLUDED.name, kind = EXCLUDED.kind, sort_order = EXCLUDED.sort_order, is_hidden = EXCLUDED.is_hidden,
    created_at = EXCLUDED.created_at, updated_at = EXCLUDED.updated_at, deleted_at = EXCLUDED.deleted_at,
    rev = EXCLUDED.rev
WHERE payment_sources.updated_at < EXCLUDED.updated_at`
)

// Push ghi các bản ghi (đã kiểm tra hợp lệ) và trả số bản được ghi / bị bỏ qua vì cũ hơn bản trên server.
// Mỗi bản ghi được cấp một rev; các lần push của cùng user chạy lần lượt (khoá dòng user_rev),
// nên rev được commit theo đúng thứ tự tăng dần và pull không bỏ sót.
func (s PGStore) Push(ctx context.Context, userID uuid.UUID, c Changes) (accepted, ignored int, err error) {
	if c.Len() == 0 {
		return 0, 0, nil
	}
	err = pgx.BeginFunc(ctx, s.DB, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `INSERT INTO user_rev (user_id) VALUES ($1) ON CONFLICT DO NOTHING`, userID); err != nil {
			return fmt.Errorf("init rev: %w", err)
		}
		var rev int64
		if err := tx.QueryRow(ctx, `SELECT rev FROM user_rev WHERE user_id = $1 FOR UPDATE`, userID).Scan(&rev); err != nil {
			return fmt.Errorf("lock rev: %w", err)
		}

		// rev cấp cho cả bản bị bỏ qua cũng không sao: cursor chỉ cần tăng dần, có khoảng trống không ảnh hưởng.
		batch := &pgx.Batch{}
		for _, t := range c.Transactions {
			rev++
			batch.Queue(upsertTransaction, userID, t.ID, t.Amount, t.Kind, t.Note, t.TagID, t.OccurredAt, t.Source,
				t.Origin, t.RawTitle, t.RawText, t.BalanceAfter, t.NeedsReview, t.ExcludedFromTotals,
				t.CreatedAt, t.UpdatedAt, t.DeletedAt, rev)
		}
		for _, t := range c.Tags {
			rev++
			batch.Queue(upsertTag, userID, t.ID, t.Name, t.Kind, t.Color, t.SortOrder, t.IsHidden,
				t.CreatedAt, t.UpdatedAt, t.DeletedAt, rev)
		}
		for _, src := range c.Sources {
			rev++
			batch.Queue(upsertSource, userID, src.ID, src.Name, src.Kind, src.SortOrder, src.IsHidden,
				src.CreatedAt, src.UpdatedAt, src.DeletedAt, rev)
		}

		results := tx.SendBatch(ctx, batch)
		for range batch.Len() {
			tag, err := results.Exec()
			if err != nil {
				results.Close()
				return fmt.Errorf("upsert: %w", err)
			}
			if tag.RowsAffected() == 1 {
				accepted++
			} else {
				ignored++
			}
		}
		if err := results.Close(); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `UPDATE user_rev SET rev = $2 WHERE user_id = $1`, userID, rev)
		return err
	})
	if err != nil {
		return 0, 0, err
	}
	return accepted, ignored, nil
}

// Pull trả tối đa limit bản ghi có rev > cursor, theo thứ tự rev, cùng cursor cho lần gọi sau.
func (s PGStore) Pull(ctx context.Context, userID uuid.UUID, cursor int64, limit int) (Changes, int64, bool, error) {
	type item struct {
		rev   int64
		apply func(*Changes)
	}
	var items []item

	rows, err := s.DB.Query(ctx, `
SELECT id, amount, kind, note, tag_id, occurred_at, source, origin, raw_title, raw_text, balance_after,
       needs_review, excluded_from_totals, created_at, updated_at, deleted_at, rev
FROM transactions WHERE user_id = $1 AND rev > $2 ORDER BY rev LIMIT $3`, userID, cursor, limit+1)
	if err != nil {
		return Changes{}, 0, false, err
	}
	for rows.Next() {
		var t Transaction
		var deleted *Time
		var rev int64
		if err := rows.Scan(&t.ID, &t.Amount, &t.Kind, &t.Note, &t.TagID, &t.OccurredAt, &t.Source, &t.Origin,
			&t.RawTitle, &t.RawText, &t.BalanceAfter, &t.NeedsReview, &t.ExcludedFromTotals,
			&t.CreatedAt, &t.UpdatedAt, &deleted, &rev); err != nil {
			rows.Close()
			return Changes{}, 0, false, err
		}
		t.DeletedAt = deleted
		items = append(items, item{rev, func(c *Changes) { c.Transactions = append(c.Transactions, t) }})
	}
	if err := rows.Err(); err != nil {
		return Changes{}, 0, false, err
	}

	rows, err = s.DB.Query(ctx, `
SELECT id, name, kind, color, sort_order, is_hidden, created_at, updated_at, deleted_at, rev
FROM tags WHERE user_id = $1 AND rev > $2 ORDER BY rev LIMIT $3`, userID, cursor, limit+1)
	if err != nil {
		return Changes{}, 0, false, err
	}
	for rows.Next() {
		var t Tag
		var deleted *Time
		var rev int64
		if err := rows.Scan(&t.ID, &t.Name, &t.Kind, &t.Color, &t.SortOrder, &t.IsHidden,
			&t.CreatedAt, &t.UpdatedAt, &deleted, &rev); err != nil {
			rows.Close()
			return Changes{}, 0, false, err
		}
		t.DeletedAt = deleted
		items = append(items, item{rev, func(c *Changes) { c.Tags = append(c.Tags, t) }})
	}
	if err := rows.Err(); err != nil {
		return Changes{}, 0, false, err
	}

	rows, err = s.DB.Query(ctx, `
SELECT id, name, kind, sort_order, is_hidden, created_at, updated_at, deleted_at, rev
FROM payment_sources WHERE user_id = $1 AND rev > $2 ORDER BY rev LIMIT $3`, userID, cursor, limit+1)
	if err != nil {
		return Changes{}, 0, false, err
	}
	for rows.Next() {
		var src Source
		var deleted *Time
		var rev int64
		if err := rows.Scan(&src.ID, &src.Name, &src.Kind, &src.SortOrder, &src.IsHidden,
			&src.CreatedAt, &src.UpdatedAt, &deleted, &rev); err != nil {
			rows.Close()
			return Changes{}, 0, false, err
		}
		src.DeletedAt = deleted
		items = append(items, item{rev, func(c *Changes) { c.Sources = append(c.Sources, src) }})
	}
	if err := rows.Err(); err != nil {
		return Changes{}, 0, false, err
	}

	// Ba bảng dùng chung bộ đếm rev nên gộp lại theo rev. Mỗi bảng lấy dư 1 dòng; bản nào chưa lấy
	// đều có rev lớn hơn cursor trả về, nên lần gọi sau không bỏ sót.
	sort.Slice(items, func(i, j int) bool { return items[i].rev < items[j].rev })
	hasMore := len(items) > limit
	if hasMore {
		items = items[:limit]
	}
	changes := Changes{Transactions: []Transaction{}, Tags: []Tag{}, Sources: []Source{}}
	next := cursor
	for _, it := range items {
		it.apply(&changes)
		next = it.rev
	}
	return changes, next, hasMore, nil
}
