-- 0065 ai_token_usage(MySQL):按协议 + 自然月累计的 token 用量,与 sqlite 版同义。
-- 一行 = (协议, 月份),写入靠 INSERT ... ON DUPLICATE KEY UPDATE 累加;月份按 UTC。
-- 不建外键到 ai_config:配置改了/停用了,它已经用掉的 tokens 是历史事实,不该随之消失。
-- MySQL 的 DDL 隐式提交,按语句逐条执行(store.go 已如此处理)。

CREATE TABLE IF NOT EXISTS ai_token_usage (
    provider          VARCHAR(16) NOT NULL,
    usage_month       VARCHAR(7)  NOT NULL,
    prompt_tokens     BIGINT NOT NULL DEFAULT 0,
    completion_tokens BIGINT NOT NULL DEFAULT 0,
    updated_at        VARCHAR(32) NOT NULL,
    PRIMARY KEY (provider, usage_month),
    CONSTRAINT chk_ai_token_usage_provider CHECK (provider IN ('claude', 'openai', 'ollama'))
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
