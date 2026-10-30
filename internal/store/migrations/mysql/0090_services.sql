-- 0090 services(MySQL):归属服务,见 sqlite/0090_services.sql 注释。
-- MySQL 逐条执行(无事务),故不含条件式 DDL;部分索引不适用,普通索引即可。

CREATE TABLE IF NOT EXISTS services (
    id          VARCHAR(64)  PRIMARY KEY,
    name        VARCHAR(255) NOT NULL,
    description VARCHAR(512) NOT NULL DEFAULT '',
    created_at  VARCHAR(32)  NOT NULL,
    updated_at  VARCHAR(32)  NOT NULL,

    UNIQUE KEY uniq_services_name (name)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

ALTER TABLE projects ADD COLUMN service_id VARCHAR(64) NOT NULL DEFAULT '';

CREATE INDEX idx_projects_service_id ON projects(service_id);
