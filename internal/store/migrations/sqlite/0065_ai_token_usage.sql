-- 0065 ai_token_usage:按协议 + 自然月累计的 token 用量,给「月 Token 上限」配一个能看见的已用量。
-- 业务含义:
--   - ai_config.budget_json 里的 monthlyTokenLimit 一直是纯声明(存了但没人累计、也没人强制),
--     页面上填了上限却看不到用掉多少。本表就是那份缺失的账:每次 chat 生成把进/出 tokens 累加进来。
--   - 一行 = (协议, 月份),写入走 upsert 累加,读当月只要一次主键命中查询 —— 不做 SUM,
--     跨方言不用担心聚合函数返回类型差异,行数也恒等于「协议数 × 用过的月份数」,不会无限长。
--   - 月份按 UTC(usage_month = '2026-09'),与库里 created_at/updated_at 一律 RFC3339 UTC 同口径;
--     跨月自然归零(新月份是新的一行),上月数据仍在表里可查。
--   - 刻意不建逐调用台账:本期只需要月度口径,台账等真要按天/按用途统计时再加(加表不返工)。
--   - 无外键到 ai_config:用量是历史事实,某一档配置被改掉/停用之后,它用过的 tokens 不该跟着一起消失。
-- 迁移注意:由 internal/store/applyMigration 在事务里整段执行,SQL 里不再写 BEGIN/COMMIT 或 PRAGMA。

CREATE TABLE IF NOT EXISTS ai_token_usage (
    provider          TEXT NOT NULL CHECK (provider IN ('claude', 'openai', 'ollama')),
    usage_month       TEXT NOT NULL,
    prompt_tokens     INTEGER NOT NULL DEFAULT 0,
    completion_tokens INTEGER NOT NULL DEFAULT 0,
    updated_at        TEXT NOT NULL,
    PRIMARY KEY (provider, usage_month)
);
