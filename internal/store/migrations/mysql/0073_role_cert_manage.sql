-- 0073 role_cert_manage:把新功能点 cert.manage 播给「普通用户」与「运维」两档预置角色。
--
-- cert.manage(internal/access/perms.go)管的是证书这条线上的写动作:登记待签、粘贴导入、
-- 就地替换、签发/续期、配置发布落点、发布到目标机、取证书包(含私钥明文)。
-- 字典里多一个点而库里不给任何角色,页面上就会出现「管理员看得到、别人全默认没有」的静默缺口,
-- 所以按每个落点操作点的既有口径补种子:
--   - user:四类资源都能动(与 server.exec / container.operate / preview.recycle 同批给的),
--     这是 0063 定下的「普通用户档位不变」承诺,少给一个点就是当场改写了这一档的行为。
--   - ops:落点全能动、编排只读 —— 证书发布就是落点动作,不给等于让运维看得到却按不动。
--   - developer:只有 cert.view(与其余落点操作点一致:看得见,不许往别人机器上写)。
--   - viewer:一个 operate 都不给(0063 的只读承诺)。
-- 管理员(内置档)永远拿全字典,不需要种子;自定义角色由管理员在角色页自己勾。
--
-- 顺序:紧跟该角色 0063 里 cert.view 之后没有意义(role_perms 无顺序语义),统一追加在文件末尾,
-- 便于回看「这一版新给了谁」。
INSERT INTO role_perms (role_id, perm_id)
  VALUES
  ('user', 'cert.manage'),
  ('ops', 'cert.manage');
