-- 0053 session_user_binding:v6.2 阶段 6 — 会话表绑定到 users(MySQL 版)。
--
-- 同 sqlite/0053_session_user_binding.sql 业务含义。MySQL 路径下迁移由
-- internal/store/store.go 的 applyMigrationMySQL 用 tx.Begin/Commit 包裹。
-- ALTER TABLE ADD COLUMN 在 MySQL 事务内合法。

ALTER TABLE sessions
    ADD COLUMN user_id VARCHAR(64) NOT NULL DEFAULT '',
    ADD COLUMN role    VARCHAR(16) NOT NULL DEFAULT '';

CREATE INDEX idx_sessions_user_id ON sessions(user_id);
