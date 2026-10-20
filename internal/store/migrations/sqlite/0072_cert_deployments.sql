-- 0072 cert_deployments:证书的发布目标(每行 = 一张证书发到一台机器的一处路径)。
-- 发布单位是「目标」而不是「一次动作」:用户配的是一份可复用的落点,续期后按 auto_deploy
-- 再铺一遍,手动发布也走同一行 —— 所以最近一次结果(last_status/last_error/deployed_at)
-- 直接挂在这行上,而不是另开一张流水表(本期不做发布历史,需要时再加,不影响这份 schema)。
--   id           : uuid v4
--   cert_id      : FK → certificates.id,ON DELETE CASCADE —— 删证书时这些落点配置跟着走:
--                  它们除了一条路径和一条指令没有别的价值,而指向一张已不存在的证书只会留下
--                  「下次自动续期该往哪发」这个永远失败的问题。子行不含任何证书材料,不会误删数据。
--   server_id    : 目标主机(引用 servers.id)。**不设外键**,与 proxy_routes 一致:删主机由上层
--                  收敛,证书发布失败时给的是「主机不存在/连不上」这种人读错误,不是级联。
--   cert_path    : 证书全文(fullchain = 叶子 + 链)在目标机上的绝对路径
--   key_path     : 私钥在目标机上的绝对路径。落盘手法:临时名 → chmod 600 → mv 原子替换
--   post_command : **发布后指令**,完全由用户填写(如 nginx reload)。**留空 = 一条命令都不跑** ——
--                  平台绝不代替用户发明要在目标机上执行的命令(见 docs 里的既有纪律),
--                  所以这里没有默认值,也没有「按服务名自动 reload」这种猜测。
--                  执行形态与部署前指令逐字同形:脚本体作 sh -c 的正文,路径走位置参数,
--                  不把用户文本拼进脚本(AC-SEC-02)。
--   auto_deploy  : 证书续期成功后是否自动重发这一份落点(默认开)
--   last_status  : '' (从未发布) | success | failed
--   last_error   : 人读失败原因,绝不含私钥与凭据
--   deployed_at  : 最近一次**成功**发布的时间
CREATE TABLE IF NOT EXISTS cert_deployments (
    id           TEXT PRIMARY KEY,
    cert_id      TEXT NOT NULL,
    server_id    TEXT NOT NULL,
    cert_path    TEXT NOT NULL,
    key_path     TEXT NOT NULL,
    post_command TEXT NOT NULL DEFAULT '',
    auto_deploy  INTEGER NOT NULL DEFAULT 1,
    last_status  TEXT NOT NULL DEFAULT '',
    last_error   TEXT NOT NULL DEFAULT '',
    deployed_at  TEXT NOT NULL DEFAULT '',
    created_at   TEXT NOT NULL,
    updated_at   TEXT NOT NULL,
    FOREIGN KEY (cert_id) REFERENCES certificates (id) ON DELETE CASCADE
);
CREATE INDEX IF NOT EXISTS idx_cert_deployments_cert ON cert_deployments (cert_id);
CREATE INDEX IF NOT EXISTS idx_cert_deployments_server ON cert_deployments (server_id);
