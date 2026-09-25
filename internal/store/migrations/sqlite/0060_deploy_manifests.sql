-- 0060 deploy_manifests:每次「应用清单」发布真正交到集群手里的那几份声明。
--
-- 为什么要新表:deploy_targets 早就每发一次记一行,但它记的是**结果**(status/message),
-- 里没有清单正文。于是「回滚 = 重新应用上一版」这句话在过去没有数据来源 ——
-- 上一版是什么,发出去之后就再也找不回来了(只能现场读集群的当前值,而那是「这一版」)。
-- 这里存的是**发出去的声明本体**,一条文档一行:回滚取的是同一对象上一版正文,
-- 页面要复现「那次发了什么」也取它,不必再猜。
--
-- 只存规范化的 apply 正文(解析后重新序列化):键顺序与注释保留,服务器自己写的那些字段
-- (resourceVersion / uid / status)不留 —— 留着会把下一次 apply 撞成 409。
--
--   run_id      : FK → pipeline_runs.id(与 deploy_targets 同一挂法;按 run 删旧重写)。
--   cluster_id  : 目标集群引用 id(逻辑引用,不加外键:集群删了历史仍要可读,同 server_id 约定)。
--   kind/namespace/name : 这条声明落在哪个对象上(回滚读路径的查找键)。
--   doc_ordinal : 文件里的第几份文档(1 基;多文档清单按此顺序应用,重放也要按此顺序)。
--   body        : 实际应用的 YAML。**绝无凭据**:kind: Secret 在解析期就被拒(见 internal/kube),
--                 所以这张表不会变成集群机密的副本。
--   created_at  : 应用时刻(RFC3339 UTC)。
--
-- 「哪一版算成功」不在这张表里重复存一遍:join deploy_targets(run_id + server_id=cluster_id)
-- 的 status 就是唯一真值 —— 两处各存一份迟早会互相说不合。

CREATE TABLE IF NOT EXISTS deploy_manifests (
    id          TEXT PRIMARY KEY,
    run_id      TEXT NOT NULL,
    cluster_id  TEXT NOT NULL,
    kind        TEXT NOT NULL,
    namespace   TEXT NOT NULL,
    name        TEXT NOT NULL,
    doc_ordinal INTEGER NOT NULL DEFAULT 1,
    body        TEXT NOT NULL,
    created_at  TEXT NOT NULL,
    FOREIGN KEY (run_id) REFERENCES pipeline_runs (id)
);

-- 回滚读路径:同一集群同一对象的上一版(按时间倒序取第一条成功的)。
CREATE INDEX IF NOT EXISTS idx_deploy_manifests_object ON deploy_manifests (cluster_id, kind, namespace, name, created_at);
-- 展示/复现:这次运行发了哪几份。
CREATE INDEX IF NOT EXISTS idx_deploy_manifests_run ON deploy_manifests (run_id);
