-- 0058 credential_owner:凭据归属与开关列落地。
-- internal/vault 的 Actor-aware 方法(ListWithActor / GetWithActor / DisableWithActor)
-- 早已按 owner_id / enabled 写判定,但 credentials 表一直缺这些列,故其 SQL 退化成
-- 「只按 scope 过滤」的兼容版本。本迁移补齐列,让既有判定真正生效。
--
--   owner_id     : personal 凭据的归属 users.id;'' = 全局凭据(存量行即此态,全员可引用)
--   description  : 管理员备注(轮换提醒、用途等)
--   enabled      : 软禁用开关;0 = 禁用(不可被新引用使用,已有引用不静默改指)
--   disabled_by  : 禁用操作者 users.id
--   disabled_at  : 禁用时间
--   created_by   : 创建者 users.id(个人凭据与 owner_id 同值;全局凭据为管理员行 id)
--
-- 所有列默认值都让存量行为「全局 + 启用 + 无备注」,即完全保持改造前的语义。

ALTER TABLE credentials ADD COLUMN owner_id    TEXT NOT NULL DEFAULT '';
ALTER TABLE credentials ADD COLUMN description TEXT NOT NULL DEFAULT '';
ALTER TABLE credentials ADD COLUMN enabled     INTEGER NOT NULL DEFAULT 1;
ALTER TABLE credentials ADD COLUMN disabled_by TEXT NOT NULL DEFAULT '';
ALTER TABLE credentials ADD COLUMN disabled_at TEXT NOT NULL DEFAULT '';
ALTER TABLE credentials ADD COLUMN created_by  TEXT NOT NULL DEFAULT '';

CREATE INDEX IF NOT EXISTS idx_credentials_owner_id ON credentials(owner_id) WHERE owner_id != '';
