-- 0066 oauth_login_apps / user_identities:用 git 平台 OAuth 登录平台本身。
--
-- 与 0022 oauth_apps 的分工:那张表配的是「取 git token 用」的应用(回调落
-- /api/oauth/{provider}/callback,要求已登录);本表的 oauth_login_apps 配的是「登录用」
-- 的应用(回调落 /api/auth/oauth/{provider}/callback,匿名可访问)。两套是刻意分开的:
-- 一个 OAuth 应用只登记一个回调地址(GitHub 尤严),两种用途各配一份互不牵制,
-- 也避免「能登录」与「能拿仓库 token」被同一个 client_id 绑成同一件事。
--
--   provider                : gitee|github|gitlab|custom(与 oauth_apps 同一枚举)
--   client_id               : 登录应用的 client id(非敏感,明文)
--   client_secret_ciphertext: vault secretbox 密文 BLOB;明文绝不入库/回显/日志
--   base_url                : 自建实例基址(custom 必填)。与 oauth_apps 各存一份 ——
--                             两套应用可能指向不同实例,不做隐式回退
--   allowed_domains         : 逗号分隔的小写邮箱域名白名单('' = 不限域名)。
--                             自动建号只放行命中域名的身份,见 internal/identity
--   enabled                 : 启用开关(未启用 → 登录页不出该入口,start 拒绝)

CREATE TABLE IF NOT EXISTS oauth_login_apps (
    provider                 TEXT PRIMARY KEY,
    client_id                TEXT NOT NULL DEFAULT '',
    client_secret_ciphertext BLOB,
    base_url                 TEXT NOT NULL DEFAULT '',
    allowed_domains          TEXT NOT NULL DEFAULT '',
    enabled                  INTEGER NOT NULL DEFAULT 0,
    created_at               TEXT NOT NULL,
    updated_at               TEXT NOT NULL
);

-- 外部身份 → 本地账号的映射。**唯一键是 (provider, external_id)**,即平台侧的稳定 ID,
-- 不是用户名也不是邮箱:平台允许改 username,按名字匹配会出现「改名即换人」,
-- 而按 username/email 撞库自动关联更是直接的账号接管面。
-- 一个本地账号可绑多条身份(同一 provider 至多一条,由 UNIQUE 保证)。
--   user_id     : users.id;删号级联解绑(ON DELETE CASCADE)
--   external_id : provider 侧稳定用户 ID(GitLab/GitHub/Gitee 的 id 字段,字符串化)
--   username    : 授权当时的平台登录名,仅作展示与建号取名参考,不参与判定
--   email       : 授权当时的主邮箱,仅用于域名白名单与展示,同样不参与账号匹配
CREATE TABLE IF NOT EXISTS user_identities (
    id          TEXT PRIMARY KEY,
    user_id     TEXT NOT NULL,
    provider    TEXT NOT NULL,
    external_id TEXT NOT NULL,
    username    TEXT NOT NULL DEFAULT '',
    email       TEXT NOT NULL DEFAULT '',
    created_at  TEXT NOT NULL,
    updated_at  TEXT NOT NULL,
    UNIQUE (provider, external_id),
    FOREIGN KEY (user_id) REFERENCES users (id) ON DELETE CASCADE
);

CREATE INDEX idx_user_identities_user_id ON user_identities (user_id);
