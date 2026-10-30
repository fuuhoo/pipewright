-- 0089 deploy target backup:把「这次部署用的部署路径 / 重启命令 / 备份方式」随每机结果落库。
--
-- 为什么要有:备份是部署前打在目标机上的,恢复入口在运行详情(按目标)。恢复时必须知道
-- 部署到了哪个目录、恢复完要不要重启 —— 这些此前只存在于发起部署的前端表单里,页面一刷新
-- 就没了,历史运行更是无从得知。随结果落库后,恢复不再依赖客户端回传路径 / 命令。
--
--   deploy_path     : 解析后的部署目录(deployPath 别名与兜底根目录都已展开),非文件部署为空。
--   restart_command : 该次部署的重启 / 切换命令(空 = 部署时不重启,恢复也不重启)。
--   backup_mode     : dir | suffix(空 = 该次部署没有开启备份,不提供恢复入口)。
--
-- 只 ADD COLUMN,不重建表;存量行三列为空 = 「当时没有记录」,不提供恢复。
ALTER TABLE deploy_targets ADD COLUMN deploy_path TEXT NOT NULL DEFAULT '';
ALTER TABLE deploy_targets ADD COLUMN restart_command TEXT NOT NULL DEFAULT '';
ALTER TABLE deploy_targets ADD COLUMN backup_mode TEXT NOT NULL DEFAULT '';
