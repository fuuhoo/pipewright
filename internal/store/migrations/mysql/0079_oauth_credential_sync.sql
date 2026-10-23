-- 0079 oauth_credential_sync(MySQL):登录时同步 git 凭据,列义见 sqlite 同序注释。
-- 只用 ADD COLUMN,不重建表。BLOB 在 MySQL 里不允许 DEFAULT,git_refresh_ciphertext 因此留 NULL
-- (= 该平台没发续期凭证);其余四列都靠默认值表达「从未同步过」,存量行为一字不变。

ALTER TABLE oauth_login_apps ADD COLUMN sync_credentials TINYINT NOT NULL DEFAULT 0;

ALTER TABLE user_identities ADD COLUMN credential_id VARCHAR(64) NOT NULL DEFAULT '';
ALTER TABLE user_identities ADD COLUMN git_refresh_ciphertext BLOB NULL;
ALTER TABLE user_identities ADD COLUMN git_expires_at VARCHAR(32) NOT NULL DEFAULT '';
ALTER TABLE user_identities ADD COLUMN git_scope VARCHAR(512) NOT NULL DEFAULT '';
