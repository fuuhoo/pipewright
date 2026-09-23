-- 0054 resource_groups:资源分组(项目 / 服务器的权限边界)。
-- 业务含义:
--   - 分组是「谁能看、谁能操作」的单位;资源和分组多对一(资源带 group_id)。
--   - visibility='public'  → 全员可见可操作(等同未归组)。
--   - visibility='private' → 仅组长 + 成员 + 管理员可见可操作;其余 403。
--   - group_id='' (空) 表示未归组:全员可见可操作(存量数据默认态,不做迁移)。
--
-- 表名用 resource_groups 而非 groups:groups 是 MySQL 8 保留字(8.0 引入 GROUPS 窗口帧),
--   裸写 `CREATE TABLE groups` 在 MySQL 直接语法错误。
--
--   id           : uuid v4
--   name         : 分组展示名,唯一
--   owner_id     : 组长 users.id;组长可管成员 + 管组内资源,但不因此获得全局权限
--   created_by   : 创建者 users.id(管理员或组长)
--   updated_by   : 最后一次改动者 users.id,仅审计用途

CREATE TABLE IF NOT EXISTS resource_groups (
    id          TEXT PRIMARY KEY,
    name        TEXT NOT NULL UNIQUE,
    description TEXT NOT NULL DEFAULT '',
    visibility  TEXT NOT NULL DEFAULT 'private',
    owner_id    TEXT NOT NULL,
    created_by  TEXT NOT NULL,
    created_at  TEXT NOT NULL,
    updated_at  TEXT NOT NULL,

    CHECK (visibility IN ('public', 'private'))
);

CREATE INDEX IF NOT EXISTS idx_resource_groups_visibility ON resource_groups(visibility);
CREATE INDEX IF NOT EXISTS idx_resource_groups_owner      ON resource_groups(owner_id);
