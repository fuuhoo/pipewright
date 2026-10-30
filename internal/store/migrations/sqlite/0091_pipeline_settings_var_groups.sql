-- 0091 pipeline_settings.var_group_ids:流水线引用的复用库变量组。
--
-- 语义:
--   - 存一份 ID 数组(JSON),按引用顺序解析;引用的组由「变量与缓存」页多选产生。
--   - 执行期把组内变量并入本流水线变量表,同名以项目自己的变量优先(组的顺序靠前者先到、
--     靠后者覆盖前一组同名项)。
--   - 加密变量照旧只存 credentialId 引用;发布路径 / 命令里引用加密变量会在运行时明确报错
--     (这些正文会落到目标机与部署记录里,不能把机密抄进去)。
--
-- 存量行默认 '[]'(不引用任何组),行为与加这列之前一字不差。

ALTER TABLE pipeline_settings ADD COLUMN var_group_ids TEXT NOT NULL DEFAULT '[]';
