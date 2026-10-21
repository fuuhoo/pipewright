-- 0075 api_tokens:平台自持的 API 令牌(Bearer),给 MCP 客户端与脚本这类机器调用方用。
--
-- 为什么要新造一套凭据而不是复用会话 cookie:会话绑浏览器(CSRF + SameSite),机器客户端
-- 既拿不到也守不住那套约束;而把管理员口令直接交出去更不可接受 —— 令牌要能单独撤销、
-- 单独归属,且泄露时不牵连登录口令。
--
--   id           : uuid v4,仅用于列表定位与撤销(撤销是 DELETE,状态不落列)
--   name         : 人读名称(「本机 Claude Desktop」「巡检脚本」),审计与页面都靠它认出是哪把
--   token_hash   : 明文令牌的 sha256(hex)。**库里绝不存明文**,明文只在创建那一次响应里回显。
--                  等值查找走 UNIQUE 索引,校验时算一次 sha256 比对即可,不需要慢哈希:
--                  口令能靠字典撞库,而这里是 32 字节随机数,离线跑库也逆不出可用值。
--   user_id      : 令牌**归属于哪个用户**(users.id)。权限判定与分组可见范围都按这个人算,
--                  所以「角色管功能 / 分组管数据」这条口径对机器调用同样成立,
--                  审计的 actor 也是他,而不是一律写成 admin。
--                  不设外键:删号由 users.Delete 的事务显式清(见该方法),与会话同一手法。
--   created_by   : 创建人 username(管理员可替他人签发,事后要分清「谁签的」和「谁的」)
--   last_used_at : 最近使用时间,RFC3339;'' = 从未用过(发出去没动过 = 该删的那种)
--                  写入有节流(见 internal/apitoken),不是每请求一次 UPDATE。
CREATE TABLE IF NOT EXISTS api_tokens (
    id           TEXT PRIMARY KEY,
    name         TEXT NOT NULL,
    token_hash   TEXT NOT NULL,
    user_id      TEXT NOT NULL,
    created_by   TEXT NOT NULL DEFAULT '',
    last_used_at TEXT NOT NULL DEFAULT '',
    created_at   TEXT NOT NULL
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_api_tokens_hash ON api_tokens (token_hash);
CREATE INDEX IF NOT EXISTS idx_api_tokens_user ON api_tokens (user_id);
