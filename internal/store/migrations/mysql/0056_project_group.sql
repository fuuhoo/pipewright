-- 0056 project_group(MySQL):项目归属分组,见 sqlite 同名注释。
-- MySQL 不支持部分索引,故建普通索引;'' 值集中在同一叶子页,过滤仍走索引。

ALTER TABLE projects ADD COLUMN group_id VARCHAR(64) NOT NULL DEFAULT '';

CREATE INDEX idx_projects_group_id ON projects(group_id);
