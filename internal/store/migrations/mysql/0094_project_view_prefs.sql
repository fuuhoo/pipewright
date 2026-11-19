-- 0094 project_view_prefs(MySQL):项目卡片的每用户手动顺序,见 sqlite/0094 注释。
-- MySQL 逐条执行(无事务),故不含条件式 DDL。

CREATE TABLE IF NOT EXISTS project_view_prefs (
    user_id    VARCHAR(64) PRIMARY KEY,
    order_json LONGTEXT NOT NULL DEFAULT ('[]'),
    updated_at VARCHAR(32) NOT NULL
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
