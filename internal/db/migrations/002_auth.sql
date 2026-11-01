-- Đăng nhập. Một user gắn được nhiều cách đăng nhập (Google trước, Apple sau) qua user_identities.
CREATE TABLE users (
    id         UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    deleted_at TIMESTAMPTZ
);

CREATE TABLE user_identities (
    provider   TEXT        NOT NULL,           -- 'google' | 'apple'
    subject    TEXT        NOT NULL,           -- ID tài khoản do Google/Apple cấp (trường "sub")
    user_id    UUID        NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    email      TEXT        NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (provider, subject)
);
CREATE INDEX user_identities_user ON user_identities (user_id);

-- ID máy do app tạo lần đầu cài (header X-Device-Id).
CREATE TABLE devices (
    user_id      UUID        NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    id           UUID        NOT NULL,
    platform     TEXT        NOT NULL DEFAULT '',
    app_version  TEXT        NOT NULL DEFAULT '',
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_seen_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (user_id, id)
);

-- Chỉ lưu SHA-256 của refresh token. Mỗi lần refresh, token cũ bị thu hồi và thay bằng token mới;
-- dùng lại token đã thu hồi nghĩa là token bị lộ, thu hồi hết token của máy đó.
CREATE TABLE refresh_tokens (
    token_hash TEXT        PRIMARY KEY,
    user_id    UUID        NOT NULL,
    device_id  UUID        NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at TIMESTAMPTZ NOT NULL,
    revoked_at TIMESTAMPTZ,
    FOREIGN KEY (user_id, device_id) REFERENCES devices (user_id, id) ON DELETE CASCADE
);
CREATE INDEX refresh_tokens_device ON refresh_tokens (user_id, device_id);
