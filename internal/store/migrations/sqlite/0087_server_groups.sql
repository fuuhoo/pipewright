-- 0087 server_groups:一台服务器可以归属多个资源分组。
--
-- 在此之前 servers.group_id 只存一个分组。把机器按「谁能碰」切开时,一台机器常常同时属于
-- 两拨人(如「支付」+「生产」);单列只能二选一,选哪个都意味着另一拨人从列表里凭空消失。
-- 本表把归属拆成多行,servers.group_id 随之删除 —— 只留一份事实,不留会漂的派生列。
--
-- 语义(见 internal/access/access.go):
--   - 没有行 = 未归组:全员可见可操作(与旧 '' 同义);
--   - 命中任意一个可见分组即可见可操作(并集):分组切的是「谁能碰」,不是把资源劈成两半;
--   - 改名/删除/改组这类归属级动作要求**每一个**所在分组都管得住。
--
-- 不加外键:resource_groups 的删除在 internal/group 里按「仍被引用」拒绝(与 0056/0057 同一口径);
-- 加级联会让删组静默把机器甩成未归组 —— 那等于把它开放给全员。
-- server_id 也不建外键,删除机器时由 internal/target 在同一事务里清掉本表的行。
--
-- 回填:旧的单值复制成一行(空串 = 未归组,不落行)。幂等,重复执行不会多一行。

CREATE TABLE IF NOT EXISTS server_groups (
    server_id  TEXT NOT NULL,
    group_id   TEXT NOT NULL,
    created_at TEXT NOT NULL,
    PRIMARY KEY (server_id, group_id)
);

CREATE INDEX IF NOT EXISTS idx_server_groups_group ON server_groups (group_id);

INSERT OR IGNORE INTO server_groups (server_id, group_id, created_at)
SELECT id, group_id, updated_at FROM servers WHERE TRIM(group_id) <> '';

DROP INDEX IF EXISTS idx_servers_group_id;
ALTER TABLE servers DROP COLUMN group_id;
