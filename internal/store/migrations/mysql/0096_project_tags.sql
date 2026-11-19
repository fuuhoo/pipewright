-- 0096 project_tags(MySQL):项目标签(0..N 个,纯展示 / 筛选属性)。
-- 语义与 sqlite 版一致;详情见 sqlite/0096_project_tags.sql 的头注释。

ALTER TABLE projects ADD COLUMN tags_json LONGTEXT NOT NULL DEFAULT ('[]');