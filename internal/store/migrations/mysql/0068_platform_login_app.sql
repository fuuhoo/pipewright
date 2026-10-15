-- 0068 platform_login_app(MySQL):登录身份源补企业微信的 AgentId,语义见 sqlite 同序注释。
-- 用 VARCHAR 而非 TEXT:MySQL 的 TEXT 列不允许 DEFAULT。只 ADD COLUMN,不重建表。

ALTER TABLE oauth_login_apps
    ADD COLUMN agent_id VARCHAR(64) NOT NULL DEFAULT '';
