-- 0069 user_profile:本地档案补「显示名」与「邮箱」。
--
-- 为什么加在 users 而不是继续用 user_identities 那两列:那两列是**平台快照**,
-- 每次登录都按平台当前值刷新(identity.accountFromBinding)。而这两个字段要「首次建号写入、
-- 之后本地可编辑」—— 写在快照列上,用户在本地改的名字下次登录就被平台覆盖回去了。
-- 所以 users 存的是本地档案,user_identities 存的仍是平台当时说了什么(域名白名单只信后者)。
--
--   display_name : 昵称/全名,页面展示用它;为空时各处回退到 username
--   email        : 档案邮箱,仅展示与联系用,不参与登录判定,也不参与账号匹配(铁律见 internal/identity)
--
-- 刻意只 ADD COLUMN,不重建表:users 被 user_identities / credentials / 分组名册等外键引用,
-- 重建表的 DROP 会级联真删数据。

ALTER TABLE users ADD COLUMN display_name TEXT NOT NULL DEFAULT '';
ALTER TABLE users ADD COLUMN email        TEXT NOT NULL DEFAULT '';
