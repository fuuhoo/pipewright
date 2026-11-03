-- 0093 server_view_prefs(MySQL):容器页主机卡片的每用户置顶与顺序,见 sqlite/0093 注释。
-- MySQL 逐条执行(无事务),故不含条件式 DDL。

CREATE TABLE IF NOT EXISTS server_view_prefs (
    user_id     VARCHAR(64) PRIMARY KEY,
    pinned_json LONGTEXT NOT NULL DEFAULT ('[]'),
    order_json  LONGTEXT NOT NULL DEFAULT ('[]'),
    updated_at  VARCHAR(32) NOT NULL
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
