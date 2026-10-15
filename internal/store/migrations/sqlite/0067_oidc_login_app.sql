-- 0067 oidc_login_app:登录应用不再只有 git 平台,补「通用 OIDC 身份源」需要的列。
--
-- 为什么要这些列:oauth_login_apps.provider 原先只认 gitee|github|gitlab|custom,端点全部
-- 由代码里的预设或 base_url 推导。OIDC 服务器(Keycloak / Authentik / 企业自建 IDaaS)的
-- 端点既不在预设里也不能靠 base_url 猜,必须能填;而它给用户的稳定主键是 `sub`,
-- 不是 git 平台的 `id` —— 拿错字段等于所有人都没有身份,登录必须拒绝。
--
-- 全部 ADD COLUMN,默认值即「未使用」:git 平台的行不受任何影响,继续走预设端点。
-- 刻意不重建表 —— user_identities 以 provider+external_id 为唯一键并外键引用 users,
-- 重建表会级联真删数据。
--
--   discovery_url : OIDC 发现文档地址(.../.well-known/openid-configuration),非空则先拉它
--   authorize_url : 显式授权端点,非空则覆盖 discovery 结果(给不给发现文档的服务器留后路)
--   token_url     : 显式兑换端点,同上
--   userinfo_url  : 显式身份端点,同上;三者全空且 discovery 拉不到 → 保存期就报错
--   id_claim      : 外部主键字段名,默认 'sub';git 平台忽略此列(恒取 id)
--   scopes        : 空格分隔的 scope 串,OIDC 默认 'openid profile email';git 平台忽略

-- 逐条 ADD COLUMN:SQLite 的 ALTER TABLE 一条语句只能加一列(逗号分隔会直接语法错)。
ALTER TABLE oauth_login_apps ADD COLUMN discovery_url TEXT NOT NULL DEFAULT '';
ALTER TABLE oauth_login_apps ADD COLUMN authorize_url TEXT NOT NULL DEFAULT '';
ALTER TABLE oauth_login_apps ADD COLUMN token_url TEXT NOT NULL DEFAULT '';
ALTER TABLE oauth_login_apps ADD COLUMN userinfo_url TEXT NOT NULL DEFAULT '';
ALTER TABLE oauth_login_apps ADD COLUMN id_claim TEXT NOT NULL DEFAULT 'sub';
ALTER TABLE oauth_login_apps ADD COLUMN scopes TEXT NOT NULL DEFAULT '';
