-- 0078 api_token_scopes(MySQL):API 令牌补「工具范围 / 分组范围」两列,语义见 sqlite 同序注释。
-- 用 VARCHAR 而非 TEXT:MySQL 的 TEXT 列不允许 DEFAULT,而这两列都靠默认值 '[]' 表达「不限」。
-- 长度对齐 certificates.sans 的量级:八个工具名远用不满 512,分组是 UUID,2048 约可容 50 组。

ALTER TABLE api_tokens ADD COLUMN allowed_tools VARCHAR(512) NOT NULL DEFAULT '[]';
ALTER TABLE api_tokens ADD COLUMN group_scope VARCHAR(2048) NOT NULL DEFAULT '[]';
