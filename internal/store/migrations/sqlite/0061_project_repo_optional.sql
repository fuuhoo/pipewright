-- 0061 project_repo_optional:项目可以绑 0 个 git 仓库(纯发布用)。
-- 业务含义:
--   - repo_url 留空 = 这项目只用来发布(产物/镜像从别处来,不进 CI 工作区)。
--   - credential_id 改为可空:没仓库就没东西要鉴权。原来它是 NOT NULL + 外键,
--     空字符串会直接撞 FK(而不是表示「无凭据」),所以必须用 NULL 表达。
--
-- 重要约束:本迁移由 internal/store/store.go 的 applyMigration 在事务里整段执行,
-- 故 SQL 里不能再嵌套 BEGIN/COMMIT;外键的关闭与恢复由 harness 负责(它必须在事务**外**
-- 切 PRAGMA foreign_keys——连接级开关在事务内是空操作)。这里不写任何 PRAGMA:
-- 若外键处于开启态,下面的 DROP TABLE projects 会顺著 14 张子表的 ON DELETE CASCADE
-- 真删数据(含全部 pipeline_runs),而不是简单报错。

CREATE TABLE projects_v2 (
    id                TEXT PRIMARY KEY,
    name              TEXT NOT NULL,
    repo_url          TEXT NOT NULL DEFAULT '',
    default_branch    TEXT NOT NULL DEFAULT '',
    credential_id     TEXT,
    created_at        TEXT NOT NULL,
    updated_at        TEXT NOT NULL,
    pac_enabled       INTEGER NOT NULL DEFAULT 0,
    pr_status_enabled INTEGER NOT NULL DEFAULT 0,
    group_id          TEXT NOT NULL DEFAULT '',
    FOREIGN KEY (credential_id) REFERENCES credentials (id) ON DELETE RESTRICT
);

-- 空串的凭据引用(理论上不该存在)一并归一成 NULL,新库只认 NULL 表示「无凭据」。
INSERT INTO projects_v2 (id, name, repo_url, default_branch, credential_id, created_at,
                         updated_at, pac_enabled, pr_status_enabled, group_id)
SELECT id, name, repo_url, default_branch,
       CASE WHEN credential_id = '' THEN NULL ELSE credential_id END,
       created_at, updated_at, pac_enabled, pr_status_enabled, group_id
FROM projects;

DROP TABLE projects;
ALTER TABLE projects_v2 RENAME TO projects;

CREATE INDEX IF NOT EXISTS idx_projects_credential_id ON projects (credential_id);
CREATE INDEX IF NOT EXISTS idx_projects_group_id ON projects (group_id) WHERE group_id != '';
