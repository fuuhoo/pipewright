-- 0071 acme_accounts(MySQL):ACME 账户,每 CA 一行,详见 sqlite 同序注释。

CREATE TABLE IF NOT EXISTS acme_accounts (
    id                 VARCHAR(64) PRIMARY KEY,
    ca                 VARCHAR(64) NOT NULL,
    directory_url      VARCHAR(512) NOT NULL,
    contact            VARCHAR(255) NOT NULL DEFAULT '',
    account_url        VARCHAR(512) NOT NULL DEFAULT '',
    eab_kid            VARCHAR(255) NOT NULL DEFAULT '',
    eab_key_sealed     BLOB,
    account_key_sealed BLOB NOT NULL,
    registered_at      VARCHAR(32) NOT NULL DEFAULT '',
    created_at         VARCHAR(32) NOT NULL,
    updated_at         VARCHAR(32) NOT NULL
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
CREATE UNIQUE INDEX idx_acme_accounts_ca ON acme_accounts (ca);
