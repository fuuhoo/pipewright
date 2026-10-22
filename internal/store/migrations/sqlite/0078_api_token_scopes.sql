-- 0078 api_token_scopes:API 令牌补两列「这把令牌被允许做什么」的范围。
--
-- 为什么要这两列:令牌的能力原本完全等于归属人的能力,而机器凭据一旦发出去就是长期、
-- 没人盯着的通道 —— 给 CI 签一把,它不该能改流水线,也不该看得到别组的项目。
-- 归属人那条线答的是「这个人能干什么」,这两列答的是「这把令牌被允许干什么」,
-- 判定取交集:**只能收窄,永远不能放大**(见 internal/access 的 Decide / VisibleGroups
-- 与 internal/mcpsrv 的按端点注册)。
--
-- 为什么存 JSON 数组而不是关联表:两列都是「一小串稳定 ID」,读时整读整写,
-- 从不按单个元素查询(没有「列出所有能跑构建的令牌」这类需求),
-- 关联表要多养一次迁移和一处 join,换不来好处。同表先例:certificates.sans。
--
-- 空数组 = 不限,所以 0075 发出去的存量令牌行为一字不变。
-- 「一个都不给」不由这两列表达:签发时至少各选一项,由 HTTP 层校验拦下。
ALTER TABLE api_tokens ADD COLUMN allowed_tools TEXT NOT NULL DEFAULT '[]';
ALTER TABLE api_tokens ADD COLUMN group_scope TEXT NOT NULL DEFAULT '[]';
