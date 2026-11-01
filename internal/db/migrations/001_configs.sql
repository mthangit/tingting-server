-- Config cho app (API bootstrap). Mỗi lần đổi config là một version mới, không sửa bản cũ.
-- Rollback = bật lại version trước. Chỉ một version được bật tại một thời điểm.
CREATE TABLE configs (
    version        BIGSERIAL PRIMARY KEY,
    schema_version INT         NOT NULL,
    payload        JSONB       NOT NULL,
    note           TEXT        NOT NULL DEFAULT '',
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    is_active      BOOLEAN     NOT NULL DEFAULT false
);

CREATE UNIQUE INDEX configs_one_active ON configs (is_active) WHERE is_active;
