-- 0064 ai_provider_scoped:三档协议各存一份配置,不再共用同一行。
-- 业务含义:
--   - 原来 ai_config 是 id=1 的单例,provider/baseUrl/model/apiKey/预算全挤一行,
--     换协议保存就把上一档的地址和模型一起盖掉(Claude 配好了切到 Ollama,再切回来全没了)。
--   - 现在按 provider 一行:claude / openai / ollama 各自的地址、模型、密钥、预算互不干扰。
--   - enabled 语义从「这份配置启没启用」升级为「这份是当前生效的」:同一时刻只有一份生效,
--     由 internal/ai 在保存时互斥置位(跨方言一致,不用部分唯一索引——MySQL 不支持)。
--
-- 迁移注意:本迁移由 internal/store/store.go 的 applyMigration 在事务里整段执行,
-- SQL 里不能再写 BEGIN/COMMIT 或 PRAGMA。ai_config 没有被任何表外键引用,重建安全。
-- 旧表里 provider='' 的行是「从未配置」的占位,不带任何可用信息,直接丢弃。

CREATE TABLE ai_config_v2 (
    provider            TEXT PRIMARY KEY CHECK (provider IN ('claude', 'openai', 'ollama')),
    base_url            TEXT NOT NULL DEFAULT '',
    model               TEXT NOT NULL DEFAULT '',
    api_key_ciphertext  BLOB,
    budget_json         TEXT NOT NULL DEFAULT '{"monthlyTokenLimit":null}',
    enabled             INTEGER NOT NULL DEFAULT 0,
    created_at          TEXT NOT NULL,
    updated_at          TEXT NOT NULL
);

INSERT INTO ai_config_v2 (provider, base_url, model, api_key_ciphertext, budget_json,
                          enabled, created_at, updated_at)
SELECT provider, base_url, model, api_key_ciphertext, budget_json,
       enabled, created_at, updated_at
FROM ai_config
WHERE provider <> '';

DROP TABLE ai_config;
ALTER TABLE ai_config_v2 RENAME TO ai_config;
