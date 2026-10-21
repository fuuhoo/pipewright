-- 0075 api_tokens(MySQL):API 令牌(Bearer),详见 sqlite 同序注释。
-- 主键与引用列用 VARCHAR(TEXT 在 MySQL 不能直接做主键/唯一索引),时间戳列同样 VARCHAR(32) 存 RFC3339。

CREATE TABLE IF NOT EXISTS api_tokens (
    id           VARCHAR(64) PRIMARY KEY,
    name         VARCHAR(128) NOT NULL,
    token_hash   CHAR(64) NOT NULL,
    user_id      VARCHAR(64) NOT NULL,
    created_by   VARCHAR(128) NOT NULL DEFAULT '',
    last_used_at VARCHAR(32) NOT NULL DEFAULT '',
    created_at   VARCHAR(32) NOT NULL
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
CREATE UNIQUE INDEX idx_api_tokens_hash ON api_tokens (token_hash);
CREATE INDEX idx_api_tokens_user ON api_tokens (user_id);
