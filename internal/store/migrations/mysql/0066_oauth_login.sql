-- 0066 oauth_login_apps / user_identities(MySQL):OAuth 登录平台。列义见 sqlite 同序注释。
-- description/域名串等用 VARCHAR 而非 TEXT:MySQL 的 TEXT 列不允许 DEFAULT。
-- user_id 与 users.id 同为 VARCHAR(64):MySQL 要求外键两侧类型逐字一致。

CREATE TABLE IF NOT EXISTS oauth_login_apps (
    provider                 VARCHAR(64) PRIMARY KEY,
    client_id                VARCHAR(255) NOT NULL DEFAULT '',
    client_secret_ciphertext BLOB,
    base_url                 VARCHAR(1024) NOT NULL DEFAULT '',
    allowed_domains          VARCHAR(1024) NOT NULL DEFAULT '',
    enabled                  TINYINT NOT NULL DEFAULT 0,
    created_at               VARCHAR(32) NOT NULL,
    updated_at               VARCHAR(32) NOT NULL
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE IF NOT EXISTS user_identities (
    id          VARCHAR(64) PRIMARY KEY,
    user_id     VARCHAR(64) NOT NULL,
    provider    VARCHAR(64) NOT NULL,
    external_id VARCHAR(191) NOT NULL,
    username    VARCHAR(255) NOT NULL DEFAULT '',
    email       VARCHAR(255) NOT NULL DEFAULT '',
    created_at  VARCHAR(32) NOT NULL,
    updated_at  VARCHAR(32) NOT NULL,
    UNIQUE KEY uk_user_identities_provider_external (provider, external_id),
    FOREIGN KEY (user_id) REFERENCES users (id) ON DELETE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE INDEX idx_user_identities_user_id ON user_identities (user_id);
