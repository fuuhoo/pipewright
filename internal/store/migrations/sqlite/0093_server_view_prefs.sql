-- 0093 server_view_prefs:容器页「主机卡片」的每用户视图偏好(置顶 + 手动顺序)。
--
-- 为什么按用户存,而不是在 servers 表上加 pinned / sort_order 两列:
--   顺序和置顶是**看的人**的习惯,不是主机的属性。一台机器不该因为某个人把它拖到后面,
--   对管理员也跟着靠后;而一旦落成共享列,每次增删改主机都得顺带维护它,
--   把「我的界面习惯」和「谁都能改的资产数据」搅在同一行里。
--
--   user_id     : 这份偏好属于谁(会话里的本人 id,只有本人能读写)
--   pinned_json : 置顶的主机 id 数组,数组顺序就是置顶区的展示顺序;'[]' = 一个都没置顶
--   order_json  : 手动拖出来的完整顺序;不在其中的(新建主机 / 别人加的主机)按后端默认顺序垫在后面
--
-- 两份都只存 id:主机改名或换地址不影响;主机被删后残留的 id 在读取时被忽略,
-- 下次保存自然消失,所以不需要额外的清理任务。
--
-- 不设外键(与 api_tokens 同口径):删号由 users.Delete 的事务显式清(见该方法)。

CREATE TABLE IF NOT EXISTS server_view_prefs (
    user_id     TEXT PRIMARY KEY,
    pinned_json TEXT NOT NULL DEFAULT '[]',
    order_json  TEXT NOT NULL DEFAULT '[]',
    updated_at  TEXT NOT NULL
);
