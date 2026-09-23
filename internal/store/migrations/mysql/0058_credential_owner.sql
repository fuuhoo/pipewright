-- 0058 credential_owner(MySQL):凭据归属与开关列,见 sqlite 同名注释。
-- description 用 VARCHAR 而非 TEXT:MySQL 的 TEXT 列不允许 DEFAULT。

ALTER TABLE credentials ADD COLUMN owner_id    VARCHAR(64)  NOT NULL DEFAULT '';
ALTER TABLE credentials ADD COLUMN description VARCHAR(512) NOT NULL DEFAULT '';
ALTER TABLE credentials ADD COLUMN enabled     TINYINT      NOT NULL DEFAULT 1;
ALTER TABLE credentials ADD COLUMN disabled_by VARCHAR(64)  NOT NULL DEFAULT '';
ALTER TABLE credentials ADD COLUMN disabled_at VARCHAR(32)  NOT NULL DEFAULT '';
ALTER TABLE credentials ADD COLUMN created_by  VARCHAR(64)  NOT NULL DEFAULT '';

CREATE INDEX idx_credentials_owner_id ON credentials(owner_id);
