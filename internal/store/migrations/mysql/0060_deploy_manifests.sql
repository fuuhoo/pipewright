-- 0060 deploy_manifests(MySQL):每次「应用清单」发布真正交到集群手里的声明正文。
-- 为什么需要这张表、以及为什么「哪一版算成功」不在这儿重复存一遍,详见 sqlite 同序注释。
-- 正文只存规范化 YAML,且 kind: Secret 在解析期就被拒 —— 这张表不会是集群机密的副本。

CREATE TABLE IF NOT EXISTS deploy_manifests (
    id          VARCHAR(64) PRIMARY KEY,
    run_id      VARCHAR(64) NOT NULL,
    cluster_id  VARCHAR(64) NOT NULL,
    kind        VARCHAR(64) NOT NULL,
    namespace   VARCHAR(255) NOT NULL,
    name        VARCHAR(255) NOT NULL,
    doc_ordinal INT NOT NULL DEFAULT 1,
    body        TEXT NOT NULL,
    created_at  VARCHAR(32) NOT NULL,
    FOREIGN KEY (run_id) REFERENCES pipeline_runs (id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE INDEX idx_deploy_manifests_object ON deploy_manifests (cluster_id, kind, namespace, name, created_at);
CREATE INDEX idx_deploy_manifests_run ON deploy_manifests (run_id);
