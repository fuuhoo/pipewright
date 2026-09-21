-- 0053 session_user_binding:v6.2 阶段 6 — 会话表绑定到 users。
--
-- 背景:
--   单管理员时代 sessions 表只有 token/csrf_token/timestamps 四个字段,认证操作者
--   隐含为 admin_user.id=1。v6.2 引入 users 表后,需让会话能区分 admin 与 user
--   普通用户,以便 RBAC 中间件(RequireAdmin/RequireUser)按角色放行/拒绝。
--
-- 列设计:
--   - user_id TEXT:会话绑定的 users.id(UUID v4)。空字符串表示「未绑定」(向后兼容
--     旧 admin_user.id=1 时代签发的会话;requireAuth 后由中间件按 Role 判定)。
--   - role   TEXT:会话角色(admin/user)。空表示「未知」(旧会话)。与 vault.Actor.Role
--     保持同一取值集合,便于 audit actor 注入时直接用。
--
-- 兼容性:
--   - 旧会话行 user_id/role 均为 ''(NULL → '' 由会话 Create 默认填空)。
--   - 这种会话在 RequireAdmin 中按"通过"(若 role='admin')或"拒绝"(其它)区分。
--     旧部署升级后,管理员旧会话的 role 仍为空——为不锁死管理员,中间件层额外校验
--     「有任意有效会话即放行 /api/auth/login 与 /api/auth/session 等入口」,并要求
--     旧会话需登出重登一次才能激活 RBAC。详见 auth.RequireAdmin 文档。
--
-- 重要约束(同 0049):
--   本迁移由 internal/store/store.go 的 applyMigration 在 SQLite 路径下用
--   tx.Begin() / tx.Exec(sqlText) / tx.Commit() 包裹执行(见 store.go)。
--   故 sqlText 本身**不能再嵌套 BEGIN TRANSACTION / ROLLBACK**。
--   ALTER TABLE ADD COLUMN 是单条 DDL,事务内合法。
--
-- 注意:users 表与 admin_user 表的迁移在 0049_user_split.sql 已完成,本迁移只
-- 修改 sessions 表。

-- 1. 加 user_id / role 列。允许为空(向后兼容旧会话行)。
ALTER TABLE sessions ADD COLUMN user_id TEXT NOT NULL DEFAULT '';
ALTER TABLE sessions ADD COLUMN role    TEXT NOT NULL DEFAULT '';

-- 2. 为审计/管理端查询用的索引:按 user_id 找某用户的所有会话。
CREATE INDEX IF NOT EXISTS idx_sessions_user_id ON sessions(user_id);
