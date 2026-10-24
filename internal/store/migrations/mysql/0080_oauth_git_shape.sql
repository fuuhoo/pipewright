-- 0080 oauth_git_shape(MySQL):自建实例的地址形态 + OIDC 的「同为 git 平台」声明,列义见 sqlite 同序注释。
-- 只用 ADD COLUMN,不重建表。默认空串 = 存量行为不变(custom 仍按 GitLab 形态,oidc 仍不可开凭据同步)。

ALTER TABLE oauth_apps ADD COLUMN git_shape VARCHAR(16) NOT NULL DEFAULT '';

ALTER TABLE oauth_login_apps ADD COLUMN git_shape VARCHAR(16) NOT NULL DEFAULT '';
