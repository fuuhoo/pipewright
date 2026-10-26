-- 0083 notification_deliveries:通知「发出去了没有」的台账。
--
-- 为什么需要:渠道/路由/模板三张表(notification_channels / notification_routes /
-- notification_templates)+ notify_config 全是**配置**,库里没有一处记下「某个事件当时发给了
-- 哪个渠道、成功还是失败」。于是发送结果只活在进程的 log.Printf 里(见 internal/notify/router.go
-- 那几个分支),页面上问「昨晚那封失败通知到底发没发出去」根本无据可查 —— 左栏那个「通知」入口
-- 因此只能指向设置页,等于没有内容。
--
-- 本表把每次投递(事件 × 渠道)钉成一行:status 三档 ok / failed / skipped(skipped = 路由配了
-- 但渠道被停用,平台按既有语义优雅跳过)。error_text 只存渠道返回的人读错误,绝无凭据明文 ——
-- 正文早在构造 vars 时就过双层脱敏(见 internal/httpapi/notify_hook.go 的 Masker + notify 的
-- SummarizeFailure),所以 title/body 存的是「当时那句被洗过之后的话」,而不是任何密钥。
-- body 存渲染后的正文(按字符截断到上限),点开一行就能看到那条通知的原样内容;
-- 即便结论是 failed/skipped 也照样存 —— 「本来该说什么」正是排查时要看的那一半。
--
-- 两条生长边界:
--  1. run_id 带 ON DELETE CASCADE —— 裁掉某条运行记录时,它名下的台账跟着走(与 0081
--     run_workspace_snapshots 同一口径)。
--  2. 表整体再由保留策略按「全局条数 + 天数」裁一遍(internal/retention 的 PruneDeliveries),
--     因为 project_id / run_id 为空的全局事件(证书到期、指标异常)不挂在任何运行上,级联管不到它。
--
-- project_id 可为空:全局事件本就没有项目;它不属于任何分组,所以列表按分组收敛时这类行不参与
-- 项目收敛(见 internal/notify/deliveries.go 的可见性口径)。
--
-- additive only:新建独立表,不改任何既有表的列或语义。

CREATE TABLE IF NOT EXISTS notification_deliveries (
    id            TEXT PRIMARY KEY,
    event         TEXT NOT NULL,
    project_id    TEXT,                                -- NULL = 全局事件(证书/异常)
    run_id        TEXT,                                -- NULL = 不挂在某次运行上
    channel_id    TEXT NOT NULL,                       -- 不加外键:渠道删了,历史要留下
    channel_name  TEXT NOT NULL DEFAULT '',            -- 冗余当时解析到的渠道名(渠道改名/删除后仍可辨)
    status        TEXT NOT NULL,                       -- ok | failed | skipped
    title         TEXT NOT NULL DEFAULT '',            -- 渲染后的通知标题
    body          TEXT NOT NULL DEFAULT '',            -- 渲染后的正文(按字符截断;脱敏同 title)
    error_text    TEXT NOT NULL DEFAULT '',            -- 人读失败原因(绝无凭据明文);成功为空
    created_at    TEXT NOT NULL,
    FOREIGN KEY (run_id) REFERENCES pipeline_runs (id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_notify_deliv_created ON notification_deliveries (created_at);
CREATE INDEX IF NOT EXISTS idx_notify_deliv_event   ON notification_deliveries (event, created_at);
CREATE INDEX IF NOT EXISTS idx_notify_deliv_project ON notification_deliveries (project_id);
CREATE INDEX IF NOT EXISTS idx_notify_deliv_run     ON notification_deliveries (run_id);
