-- 0086:标记账号是否有「本人知道的本地口令」。
-- OAuth/OIDC 首登自动建号写的是随机占位哈希,明文从未存在,这类账号无法走
-- 「输入当前口令」的改密路径,需要单独提供「首次设置口令」入口。
-- 默认 1 保住所有存量本地口令账号;OAuth 自动建号那批按建号描述回填为 0。
ALTER TABLE users ADD COLUMN has_local_password TINYINT(1) NOT NULL DEFAULT 1;

UPDATE users SET has_local_password = 0 WHERE description LIKE 'OAuth 自动建号%';
