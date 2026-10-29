-- 0084 user_roles:一个账号可以挂多个角色。
--
-- 在此之前 users.role / sessions.role 各存一个角色 id,「一人多岗」只能靠给角色勾更多点来凑,
-- 但那样一来「张三 = 运维 + 只读」这件事在页面上就消失了:角色数、删除挡门、按角色筛人
-- 全都只剩一个主角色。本表把成员关系拆成多行,users.role 退化为**主角色**(展示与旧代码兜底),
-- 判定侧改为对角色集合求并集(见 internal/access/perms.go 的 SplitRoles)。
--
-- 不加 role_id 外键:内置 admin 是代码表里的模板,roles 表里刻意没有这一行(见 0062/0063)。
-- 加外键会把管理员挡在门外。删角色的挡门在 internal/role 里按「仍有人使用」拒绝,不靠级联。
--
-- 回填:每个非空的 users.role 复制成一行。幂等(INSERT OR IGNORE),重复执行不会多一行。
-- 之后 users.Create / SetRoles 在同一个事务里同时写 users.role(主角色)与本表,两者不会漂。

CREATE TABLE IF NOT EXISTS user_roles (
    user_id    TEXT NOT NULL,
    role_id    TEXT NOT NULL,
    created_at TEXT NOT NULL,
    PRIMARY KEY (user_id, role_id)
);

CREATE INDEX IF NOT EXISTS idx_user_roles_role ON user_roles (role_id);

INSERT OR IGNORE INTO user_roles (user_id, role_id, created_at)
SELECT id, role, updated_at FROM users WHERE TRIM(role) <> '';
