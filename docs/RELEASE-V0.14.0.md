# v0.14.0 发布清单

> 发布日：2026-10-27  
> **健康门禁收敛三处不一致 + 保存错误回显修复**

## 功能与改动

### A: K8s 发布不再假接受健康门禁配置
- **问题**: `deploy_k8s` 节点在流水线画布上显示健康探测字段，但执行侧并不跑探针；用户填了这些键也不报错，只在运行页卡里"静默忽略"。
- **根因**: [internal/pipeline/job_types.go](file:///Users/cw/code/pipewright/internal/pipeline/job_types.go#L557) `validateDeployK8s` 对健康键没有拒止逻辑。
- **改动**:  
  - 后端 ([job_types.go:557](file:///Users/cw/code/pipewright/internal/pipeline/job_types.go#L557)): 当任务类型为 `deploy_k8s` 且配置中出现任何健康探测相关键时，返回 **422 invalid_job**,消息点名「K8s 发布不跑健康门禁 (落点是集群，由「等待滚动」与 autoRollback 把关)，请清空探测方式、探测 URL、探测命令、重试次数、重试间隔、单次超时这六个配置格」。  
  - 前端 ([jobConfigSchema.ts:1365](file:///Users/cw/code/ppipewright/web/src/components/pipeline/jobConfigSchema.ts#L1365)): `deploy_k8s.droppedKeys` 已声明健康六键；抽屉里选 `deploy_k8s` 时不再渲染任何探测输入框；原始参数里会过滤掉它们（`splitConfig`），**换类型 prune proven end-to-end**: ssh 节点配了 4 个健康键 → 切换 k8s → 只有 `serverIds` 保留，健康键彻底消失。
- **验证**: PUT /api/projects/{id}/pipeline 携带 `healthProbe:http` + `healthUrl:...` 并指定 `kind: deploy_k8s`,返回 422 `invalid_job` 并包含上述完整原因。

### B: 手动部署面板 间隔(秒) 上限 3600→60，与领域侧钳制口径一致
- **问题**: RunDetail「部署到目标服务器」面板的 `hcIntervalSeconds` 绑定为 `[0,60]`,但后端 [internal/deploy/healthcheck.go](file:///Users/cw/code/pipewright/internal/deploy/healthcheck.go) 已经对 interval 做 `<0→3s,0 合法，>60→60`;页面允许 3600 会造成用户误以为能配更长的时间，实际会被强制折到 60。
- **改动**: [RunDetail.vue:1041](file:///Users/cw/code/pipewright/web/src/views/RunDetail.vue#L1041) `<input min="0" max="60">`。
- **验证**: 打开一个带产物的运行详情页 → "部署到目标服务器" → "健康探测 = HTTP",看到三个数字输入的实际 DOM 属性为 重试 `1/20`,间隔 `0/60`,超时 `1/60`。

### C: README + docs/IMPLEMENTATION-STATUS 按档位写清探测不通之后的后果（不回滚）
- **问题**: README 与实施状态页混写了 "compose 失败会回滚" 和 "蓝绿策略会回滚上一版" 两类语义；实际执行侧文件直铺、compose 整栈都**不回滚**,只有 docker 单容器 & 蓝绿镜像才回滚；k8s 则根本不跑探测，门控是 rollout + autoRollback。
- **改动**:  
  - [README.md](file:///Users/cw/code/pipewright/README.md): 「探测不通时」小节补四行表格 (成功态、docker 单容器/蓝绿、文件直铺/compose、k8s)。  
  - [docs/v6.2-IMPLEMENTATION-STATUS.md](file:///Users/cw/code/pipewright/docs/v6.2-IMPLEMENTATION-STATUS.md): §2.9 补「探测键不该出现的档」一行、「探测参数的两处上限口径」一行。
- **验证**: 阅读最新版本文档对照四类部署形态的“检测不通之后”行为，确认与执行侧代码一致。

### D: 保存被拒时回显后端那句原因（顺带的 UI fix）
- **问题**: 上面 A 类的 422 `invalid_job` 会把**针对哪个节点、哪几个键**的具体文本发给前端，前端却用固定兜底文案覆盖，新消息永远看不见。
- **改动**: [ProjectPipeline.vue:183](file:///Users/cw/code/pipewright/web/src/views/ProjectPipeline.vue#L183) `mapSaveError` 增加 `'invalid_job': msg ?? t(...)` 分支。
- **验证**: 故意在部署节点配错组合（主机 + 集群）→ 保存 → Banner 显示「阶段「部署」任务「SSH 发布」:同时配了目标主机与集群 —— 一个节点只能发一种落点」而不是原来的错误信息。

### i18n ×8：探测提示改按档位说清楚
- **改动**: 所有语种 [web/src/i18n/locales/*/pipelineJob.ts](file:///Users/cw/code/pipewright/web/src/i18n/locales/en/pipelineJob.ts) `fieldHealthProbeHint` 从原先的笼统承诺改成每档一语："只有 docker 单容器档会退回上一镜像，文件直铺与 compose 整栈不回滚"。
- **验证**: 任意语种切一次任务类型，查看健康探测字段的 hint 是否已按新版本本表达。

---

## 提交摘要

```bash
aef41b4 fix(健康门禁): K8s 发布不再假接受探测配置
3a2842b fix(健康门禁): 手动部署的间隔秒上限改回 60
c8cf8a1 docs(健康门禁): 按档位写清探测不通之后的后果
0a21058 fix(健康门禁): 探测提示不再承诺「蓝绿会回滚上一版」
7d9eea7 fix(健康门禁): 保存被拒时回显后端的那句原因，不再盖成兜底文案
45f2599 chore(前端): 重新生成 dist 入口，指向上一条健康门禁提示的打包产物
51318cd chore(前端): 重新生成 dist 入口，指向上一条保存报错回显的打包产物
```

本地全量验证:
- 后端: `make fmt-check vet test` 全绿
- 前端: `npm run typecheck && npm run test && npm run build` 全绿
- 二进制: `/version` 自述 `v0.14.0 (commit aef41b4...), not dev`

---

## 发版流程

1. **推送 master**: `git push git@github.com:fuuhoo/pipewright.git master` ✅
2. **打 tag**: `git tag -a v0.14.0 -m "v0.14.0: health gate consistency fixes"` ✅
3. **推 tag**: `git push git@github.com:fuuhoo/pipewright.git v0.14.0` ✅
4. **盯四条 workflow**: GitHub Actions `release`, `aliyun-image`, `caddy-image`, `ci` (push & tag)
5. **终检**: Release 资产可下载、ACR 标签存在、镜像自述版本对、latest 已切

---

## 已知未修复项（超出本次约定）

1. [job_types.go:488](file:///Users/cw/code/pipewright/internal/pipeline/job_types.go#L488) 在「没选集群、也没配主机」时也会报「同时配了目标主机与集群」—— 这条文案会在空状态下触发，指向不了真正漏的那个字段。
2. [jobConfigSchema.ts:1365](file:///Users/cw/code/pipewright/web/src/components/pipeline/jobConfigSchema.ts#L1365) `deploy_k8s.droppedKeys` 只列了 6 个探测键，ssh→k8s 换类型后 `serverIds` 会以「原始参数」留着，正好撞上第 1 条那句错报。

这两条留待下一轮讨论是否要一并收口。

---

## 参考

- [internal/pipeline/job_types.go:557](file:///Users/cw/code/pipewright/internal/pipeline/job_types.go#L557) (`validateDeployK8s`)
- [internal/deploy/healthcheck.go](file:///Users/cw/code/pipewright/internal/deploy/healthcheck.go) (`clampHealthParams`)
- [web/src/views/RunDetail.vue:167/211/1041](file:///Users/cw/code/pipewright/web/src/views/RunDetail.vue#L167)
- [web/src/components/pipeline/jobConfigSchema.ts:1365](file:///Users/cw/code/pipewright/web/src/components/pipeline/jobConfigSchema.ts#L1365) (`droppedKeys`)
