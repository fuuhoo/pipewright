-- 0067 oidc_login_app(MySQL):登录应用补「通用 OIDC 身份源」列,语义见 sqlite 同序注释。
-- 用 VARCHAR 而非 TEXT:MySQL 的 TEXT 列不允许 DEFAULT。
-- 全部 ADD COLUMN 且默认值即「未使用」,git 平台的既有行不受影响;刻意不重建表。

ALTER TABLE oauth_login_apps
    ADD COLUMN discovery_url VARCHAR(512) NOT NULL DEFAULT '',
    ADD COLUMN authorize_url VARCHAR(512) NOT NULL DEFAULT '',
    ADD COLUMN token_url     VARCHAR(512) NOT NULL DEFAULT '',
    ADD COLUMN userinfo_url  VARCHAR(512) NOT NULL DEFAULT '',
    ADD COLUMN id_claim      VARCHAR(64)  NOT NULL DEFAULT 'sub',
    ADD COLUMN scopes        VARCHAR(255) NOT NULL DEFAULT '';
