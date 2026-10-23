-- 0079 oauth_credential_sync:「用 git 账号登录时顺手把仓库凭据也同步一份」。
--
-- 为什么要这个能力:此前登录只认身份,克隆私有仓还得用户再去「连接 git 凭据」走一遍授权
-- —— 两次授权、两套回调,而多数用户根本不知道第二遍的存在。开了同步以后,登录这一次授权
-- 就足够让流水线拉到代码。
--
-- 为什么开关放在登录应用上(oauth_login_apps)而不是全局:这一开一关改变了「登录换来什么权限」——
-- 关掉时只申请身份 scope(read_user 一类),打开时按 DefaultScopes 申请到能读仓库。
-- 让管理员逐个身份源决定,才不至于给一个只用来「认人」的 OIDC 应用白要一遍仓库读权限。
--   sync_credentials:0 = 只登录不发凭据(所有存量部署的默认,行为一字不变)
--
-- 下面四列挂在 user_identities 上,而不是 credentials 表上:被同步的凭据**归人**,
-- 但「这张令牌还能不能续、什么时候到期」是「某个人在某平台的一次授权」的属性 ——
-- 同一个人同一个平台只该有一条(由 (provider, external_id) 唯一键保证),
-- 于是这条映射行天生就是幂等键:重复登录只会轮换那一条凭据,不会每次都新建一条把人淹掉。
-- 手工录入的凭据、以及另一条「连接 git 凭据」流建出来的凭据都没有这几列,续期扫描器天然看不见它们。
--   credential_id          : 回指 credentials.id(空 = 还没同步过)。删掉凭据后下次登录会重新建一条
--   git_refresh_ciphertext : refresh_token 的 vault secretbox 密文;明文绝不入库。
--                            NULL = 该平台不发续期凭证(到期只能重新登录)
--   git_expires_at         : 访问令牌的到期时刻(RFC3339 UTC,'')。空串 = provider 没告知时效,
--                            按长期令牌处理:不进续期队列、也不报「已过期」
--   git_scope              : provider **实际**授予的权限串(申请到 ≠ 拿到,平台可以收窄),
--                            仅用于排查「为什么这个令牌拉不到那个仓库」
--
-- 全部只 ADD COLUMN,不重建表:重建时 DROP 会沿外键级联真删数据(见 SQLite 迁移外键纪律)。

ALTER TABLE oauth_login_apps ADD COLUMN sync_credentials INTEGER NOT NULL DEFAULT 0;

ALTER TABLE user_identities ADD COLUMN credential_id TEXT NOT NULL DEFAULT '';
ALTER TABLE user_identities ADD COLUMN git_refresh_ciphertext BLOB;
ALTER TABLE user_identities ADD COLUMN git_expires_at TEXT NOT NULL DEFAULT '';
ALTER TABLE user_identities ADD COLUMN git_scope TEXT NOT NULL DEFAULT '';
