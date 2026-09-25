-- 0061 project_repo_optional:项目可以绑 0 个 git 仓库(纯发布用)。
-- 与 sqlite 版同义:credential_id 用 NULL(而非 '')表示「无凭据」——'' 会撞外键;
-- repo_url 留空即「不绑仓库」,给个空串默认值让直接插 NULL 之外的写法也成立。

ALTER TABLE projects MODIFY credential_id VARCHAR(64) NULL;
ALTER TABLE projects MODIFY repo_url VARCHAR(1024) NOT NULL DEFAULT '';
