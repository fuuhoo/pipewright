-- 0056 project_group:项目归属分组。
--   '' = 未归组,全员可见可操作(存量项目全部落在这一态,不迁移)。
--   非空 = resource_groups.id;**不建外键**:删组时由领域层先把组内项目移出或整组清理,
--          避免 ON DELETE SET NULL 与 NOT NULL 冲突、也避免悬挂引用静默放行。
--   部分索引只在有分组时占空间,列表按分组过滤走它。

ALTER TABLE projects ADD COLUMN group_id TEXT NOT NULL DEFAULT '';

CREATE INDEX IF NOT EXISTS idx_projects_group_id ON projects(group_id) WHERE group_id != '';
