-- Dữ liệu đồng bộ từ app. Cột giống model trong SpendCore; id do app tạo nên khoá là (user_id, id).
-- Mỗi lần ghi, bản ghi nhận rev mới từ bộ đếm user_rev; app kéo về theo "mọi thứ có rev > cursor".
CREATE TABLE user_rev (
    user_id UUID   PRIMARY KEY REFERENCES users (id) ON DELETE CASCADE,
    rev     BIGINT NOT NULL DEFAULT 0
);

CREATE TABLE transactions (
    user_id              UUID        NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    id                   UUID        NOT NULL,
    amount               BIGINT      NOT NULL,
    kind                 TEXT        NOT NULL,
    note                 TEXT        NOT NULL DEFAULT '',
    tag_id               UUID,
    occurred_at          TIMESTAMPTZ NOT NULL,
    source               TEXT        NOT NULL DEFAULT '',
    origin               TEXT        NOT NULL,
    raw_title            TEXT,
    raw_text             TEXT,
    balance_after        BIGINT,
    needs_review         BOOLEAN     NOT NULL DEFAULT false,
    excluded_from_totals BOOLEAN     NOT NULL DEFAULT false,
    created_at           TIMESTAMPTZ NOT NULL,
    updated_at           TIMESTAMPTZ NOT NULL,
    deleted_at           TIMESTAMPTZ,
    rev                  BIGINT      NOT NULL,
    PRIMARY KEY (user_id, id)
);
CREATE INDEX transactions_rev ON transactions (user_id, rev);

CREATE TABLE tags (
    user_id    UUID        NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    id         UUID        NOT NULL,
    name       TEXT        NOT NULL,
    kind       TEXT        NOT NULL,
    color      TEXT        NOT NULL,
    sort_order INT         NOT NULL DEFAULT 0,
    is_hidden  BOOLEAN     NOT NULL DEFAULT false,
    created_at TIMESTAMPTZ NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL,
    deleted_at TIMESTAMPTZ,
    rev        BIGINT      NOT NULL,
    PRIMARY KEY (user_id, id)
);
CREATE INDEX tags_rev ON tags (user_id, rev);

CREATE TABLE payment_sources (
    user_id    UUID        NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    id         UUID        NOT NULL,
    name       TEXT        NOT NULL,
    kind       TEXT        NOT NULL,
    sort_order INT         NOT NULL DEFAULT 0,
    is_hidden  BOOLEAN     NOT NULL DEFAULT false,
    created_at TIMESTAMPTZ NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL,
    deleted_at TIMESTAMPTZ,
    rev        BIGINT      NOT NULL,
    PRIMARY KEY (user_id, id)
);
CREATE INDEX payment_sources_rev ON payment_sources (user_id, rev);
