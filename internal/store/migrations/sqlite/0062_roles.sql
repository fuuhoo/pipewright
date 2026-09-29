-- 0062 roles / role_perms:可配置角色(自定义角色)。
-- 内置五档(admin / user / developer / ops / viewer)**不进库**:它们的点集是代码表
-- internal/access/perms.go 的 rolePerms,页面上当「模板」呈现(只读、可复制成自定义角色)。
-- 这么切是为了两件事:①升级时新加功能点能自动跟着内置档位走,不会被各实例库里的历史手改拖住;
-- ②管理员永远不会把自己锁在门外 —— 内置 admin 不可改不可删。
-- 库里只存自定义角色:users.role 存 roles.id(uuid v4),access 的档位判定按「先查代码表,
-- 再查本表」两级走(见 internal/access/catalog.go)。
--   id          : uuid v4;与内置角色名(admin/user/...)天然不撞,故不另设前缀
--   name        : 展示名,任意文本(不走 i18n —— 它是管理员起的业务称呼)
--   description : 用途说明(列表副标题)
--   base_role   : 复制来源的内置角色名('' = 手建);仅用于页面「基于 X 模板」提示,无判定语义
--   created_by  : 建它的 users.id,仅审计展示(与 resource_groups.created_by 同理)
-- perm_id 不加外键到「点字典」:点的权威在代码里(21 条常量),库里存的是它的字符串 ID。
-- 代码删了某个点,残留引用不会命中任何点 → 档位算不出 → fail closed,由启动装载时清理。

CREATE TABLE IF NOT EXISTS roles (
    id          TEXT PRIMARY KEY,
    name        TEXT NOT NULL UNIQUE,
    description TEXT NOT NULL DEFAULT '',
    base_role   TEXT NOT NULL DEFAULT '',
    created_by  TEXT NOT NULL DEFAULT '',
    created_at  TEXT NOT NULL,
    updated_at  TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS role_perms (
    role_id TEXT NOT NULL,
    perm_id TEXT NOT NULL,
    PRIMARY KEY (role_id, perm_id),
    FOREIGN KEY (role_id) REFERENCES roles (id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_role_perms_role_id ON role_perms (role_id);
