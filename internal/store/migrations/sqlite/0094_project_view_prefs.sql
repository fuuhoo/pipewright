-- 0094 project_view_prefs:项目页「项目卡片」的每用户手动顺序。
--
-- 和 0093 server_view_prefs 同一个理由:卡片先后是**看的人**的习惯,不是项目的属性。
-- 项目有分组、有归属,但「我想先看到哪几个」因人而异,做成共享列会让别人一拖把我的屏幕也改了。
--
--   user_id    : 这份顺序属于谁(会话里的本人 id,只有本人能读写)
--   order_json : 拖出来的项目 id 完整顺序;不在其中的(新建 / 别人建的项目)按默认顺序垫在后面
--
-- 只存 id:项目改名不影响;项目被删后残留的 id 在读取时忽略,下次保存自然消失,不需要清理任务。
-- 不设外键(与 server_view_prefs / api_tokens 同口径):删号由 users.Delete 的事务显式清。

CREATE TABLE IF NOT EXISTS project_view_prefs (
    user_id    TEXT PRIMARY KEY,
    order_json TEXT NOT NULL DEFAULT '[]',
    updated_at TEXT NOT NULL
);
