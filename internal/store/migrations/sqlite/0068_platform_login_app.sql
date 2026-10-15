-- 0068 platform_login_app:登录身份源再加三家国内平台(钉钉 / 飞书 / 企业微信)。
--
-- 为什么只多一列:三家的端点都写死在代码预设里(各自只有一个官方主机),scope 复用 0067
-- 已有的 scopes 列,client_id / client_secret 也够用 —— 唯独企业微信的自建应用登录要
-- corpid + corpsecret + **agentid** 三件(扫码授权 URL 与「应用可见范围」判定都要 agentid),
-- 前两件已有归宿,第三件没有,故补此列。
--
--   agent_id : 企业微信自建应用的 AgentId;其余身份源恒为空串。
--
-- 刻意只 ADD COLUMN,不重建表 —— user_identities 以 (provider, external_id) 唯一并外键引用
-- users,重建表的 DROP 会级联真删已绑定的身份。默认空串即「未使用」,既有行不受影响。

ALTER TABLE oauth_login_apps ADD COLUMN agent_id TEXT NOT NULL DEFAULT '';
