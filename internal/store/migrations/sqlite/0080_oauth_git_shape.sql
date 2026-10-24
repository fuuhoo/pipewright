-- 0080 oauth_git_shape:自建实例的「地址形态」与 OIDC 的「同为 git 平台」声明。
--
-- 为什么需要这一列:此前 custom 只会按 GitLab 的地址形态推导端点(/oauth/authorize、
-- /api/v4/user),而自建 Gitea 用的是 /login/oauth/* 与 /api/v1/user —— 于是自建 Gitea
-- 无论怎么填 base_url 都拼不出可用的授权地址,用户在授权页只会看到一个说不清的失败。
-- 光靠 base_url 推不出这两家(两家都能挂在任意路径下),所以必须由运营者显式说一家。
--
-- 同一列在 provider=oidc 上是另一种用途:**声明**这个身份源背后同时是一家 git 平台。
-- 通用 IdP 的令牌能不能拿去克隆仓库,平台无法自己判断(看 issuer 名字猜更是幻觉),
-- 而「登录即同步凭据」必须知道该往 scope 里追加哪一家的仓库读权限 —— 所以让运营者来断言。
-- 空串的两种含义刻意不同:custom 留空 = GitLab 形态(存量自建 GitLab 不用重存,行为一字不变);
-- oidc 留空 = 没声明,凭据同步不许打开。
--
-- 只 ADD COLUMN,不重建表:重建时 DROP 会沿外键级联真删数据(见 SQLite 迁移外键纪律)。
-- 两张表各加一列:「仓库凭据」侧只需要形态,「登录」侧同时承载形态与声明。

ALTER TABLE oauth_apps ADD COLUMN git_shape TEXT NOT NULL DEFAULT '';

ALTER TABLE oauth_login_apps ADD COLUMN git_shape TEXT NOT NULL DEFAULT '';
