-- 0070 certificates(MySQL):平台自持的 SSL 证书仓库,详见 sqlite 同序注释。
-- 主键与引用列用 VARCHAR 而非 TEXT(MySQL 的 TEXT 不能当主键/索引前缀以外的老限制),
-- 时间戳列同样 VARCHAR(32) 存 RFC3339。

CREATE TABLE IF NOT EXISTS certificates (
    id              VARCHAR(64) PRIMARY KEY,
    domain          VARCHAR(255) NOT NULL,
    sans            VARCHAR(2048) NOT NULL DEFAULT '[]',
    source          VARCHAR(32) NOT NULL DEFAULT 'acme',
    status          VARCHAR(32) NOT NULL DEFAULT 'pending',
    ca              VARCHAR(64) NOT NULL DEFAULT '',
    dns_provider_id VARCHAR(64) NOT NULL DEFAULT '',
    auto_renew      TINYINT NOT NULL DEFAULT 1,
    key_algo        VARCHAR(32) NOT NULL DEFAULT '',
    not_before      VARCHAR(32) NOT NULL DEFAULT '',
    not_after       VARCHAR(32) NOT NULL DEFAULT '',
    issuer          VARCHAR(255) NOT NULL DEFAULT '',
    serial          VARCHAR(128) NOT NULL DEFAULT '',
    cert_pem        TEXT NOT NULL,
    chain_pem       TEXT NOT NULL,
    key_sealed      BLOB,
    last_error      VARCHAR(1024) NOT NULL DEFAULT '',
    issued_at       VARCHAR(32) NOT NULL DEFAULT '',
    checked_at      VARCHAR(32) NOT NULL DEFAULT '',
    created_at      VARCHAR(32) NOT NULL,
    updated_at      VARCHAR(32) NOT NULL
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
CREATE UNIQUE INDEX idx_certificates_domain ON certificates (domain);
CREATE INDEX idx_certificates_not_after ON certificates (not_after);
