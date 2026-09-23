/**
 * jobConfigSchema — declarative, per-type parameter forms for pipeline canvas nodes.
 *
 * The 2-2 pipeline contract freezes the node shape as `config: Record<string,string>`
 * (a flat string map). Rather than widen that contract, every known job *type* declares
 * a typed field schema here; JobDrawer renders those fields and reads/writes specific
 * keys in the same flat map. Anything not covered by a type's schema stays editable via
 * the drawer's "raw parameters" fallback, so custom/unknown types and power users lose
 * nothing. This is the Jenkins/云效-style "each step type has its own form" behaviour,
 * implemented without a backend DTO change.
 *
 * All values are strings (config is Record<string,string>). number/toggle fields are
 * serialized to/from strings at the field layer; multiline commands are stored as a
 * single newline-joined string.
 */

import type { CredentialType } from '../../api/credentials'
import { t } from '../../i18n'

// ─── Field model ──────────────────────────────────────────────────────────────

export type FieldKind =
  | 'text'
  | 'textarea'
  | 'select'
  | 'number'
  | 'toggle'
  | 'credential'
  | 'server'
  | 'channel'
  | 'buildenv'
  | 'configprofiles'
  /** 只读回显:推送目标 = 环境绑定的镜像仓(不可编辑,也不写进 config) */
  | 'pushTarget'

export interface SelectOption {
  value: string
  label: string
}

export interface JobField {
  /** Key written into job.config */
  key: string
  /** Human label (zh) */
  label: string
  kind: FieldKind
  placeholder?: string
  /** Helper text shown under the control */
  hint?: string
  /** Options for `select` */
  options?: SelectOption[]
  /** Restrict the credential picker to one credential type */
  credentialType?: CredentialType
  /** Render with monospace font (paths, commands, image refs) */
  monospace?: boolean
  /** Conditional visibility based on the current config values */
  when?: (config: Record<string, string>) => boolean
}

/** Accent palette keys — map to --color-{accent} / --color-{accent}-soft tokens. */
export type AccentName = 'cyan' | 'primary' | 'green' | 'amber' | 'red' | 'neutral'

/** Picker category id for grouping task types (Jenkins/云效-style gallery). */
export type CategoryId = 'source' | 'build' | 'deploy' | 'quality' | 'notify' | 'custom'

/**
 * 任务模板:同一动词任务下的「预填配方」(对齐云效的任务模板语义)。
 * 它不是节点类型 —— config 键与所挂 type 完全一致,只是选中即预填,避免为一组常用取值单开一个类型。
 */
export interface JobTemplate {
  id: string
  label: string
  description: string
  prefill: Record<string, string>
}

export interface JobTypeSpec {
  type: string
  /** Friendly zh label shown in the picker, drawer, and node card */
  label: string
  /** One-line description of what this node does (shown in the picker card) */
  description: string
  /** Accent colour for the type icon */
  accent: AccentName
  /** Category the type belongs to (picker grouping) */
  category: CategoryId
  fields: JobField[]
  /** 模板节点的预填配置(选中即带上,用户只改参数);普通节点省略。 */
  defaultConfig?: Record<string, string>
  /** 该任务在挑选画廊里展出的模板配方(点模板 = 点任务 + 预填)。 */
  templates?: JobTemplate[]
  /**
   * 该类型只是另一个类型的**历史别名**(如 custom → script):不进 picker,抽屉里按目标类型渲染,
   * 保存时统一写回目标类型。后端仍认这个别名(存量流水线与 .pipewright.yml 不改),所以不做数据迁移。
   */
  aliasOf?: string
  /**
   * 该 type 下**必须消失**的遗留键:既不渲染成控件,也不落到「原始参数」里。
   * 用于假字段(表单能填、执行侧无人消费)的清理:抽屉一打开就把它们从 config 抹掉,
   * 保存时随 flush 落库,避免用户以为「填了有用」。
   */
  droppedKeys?: string[]
  /**
   * 由「构建环境」控件**托管**的桥接键:值仍写进 config(运行时按目录解析时要对齐),
   * 但不在表单里出现、也不落到「原始参数」—— 否则就等于留了个手填镜像的入口(R4 要消灭的正是这个)。
   */
  managedKeys?: string[]
}

// ─── Shared option sets ─────────────────────────────────────────────────────────

const ARTIFACT_OPTIONS: SelectOption[] = [
  { value: 'image', get label() { return t('pipelineJob.artifactImage') } },
  { value: 'jar', get label() { return t('pipelineJob.artifactJar') } },
  { value: 'dist', get label() { return t('pipelineJob.artifactDist') } },
]

// 「构建」任务的产物档位 = 第一件事:它决定这同一份配置将来跑哪条路径
// (镜像 → docker build / 工具链镜像;jar|dist → 构建环境容器里跑命令 + 收文件产物)。
// 空档位是**合法显示态**:后端保存校验会拒绝空档位,所以这里给一个明确的未选项,
// 而不是让下拉框默认落在「镜像」上骗人(select 的取值回退到第一项)。
const BUILD_TIER_OPTIONS: SelectOption[] = [
  { value: '', get label() { return t('pipelineJob.buildTierUnselected') } },
  ...ARTIFACT_OPTIONS,
]

const BUILD_MODEL_OPTIONS: SelectOption[] = [
  { value: 'dockerfile', get label() { return t('pipelineJob.buildModelDockerfile') } },
  { value: 'toolchain', get label() { return t('pipelineJob.buildModelToolchain') } },
]

const DEPLOY_STRATEGY_OPTIONS: SelectOption[] = [
  { value: 'rolling', get label() { return t('pipelineJob.deployStrategyRolling') } },
  { value: 'recreate', get label() { return t('pipelineJob.deployStrategyRecreate') } },
  { value: 'blue-green', get label() { return t('pipelineJob.deployStrategyBlueGreen') } },
]

// 部署后的健康探测方式(原「健康检查」节点的能力;探测不通 → 部署节点失败)。
// 显式给「不探测」一档:留空与选 none 同义,但下拉框要有明确的默认项,别让人以为必须选一个。
const PROBE_MODE_OPTIONS: SelectOption[] = [
  { value: 'none', get label() { return t('pipelineJob.probeModeNone') } },
  { value: 'http', get label() { return t('pipelineJob.probeModeHttp') } },
  { value: 'command', get label() { return t('pipelineJob.probeModeCommand') } },
]

// 部署节点的产物类型偏好(本 run 同时产出镜像与文件产物时,挑哪件部署)。
// 留空 = 自动:优先文件产物(dist/jar/archive),没有文件产物才用镜像。
const DEPLOY_ARTIFACT_OPTIONS: SelectOption[] = [
  { value: '', get label() { return t('pipelineJob.deployArtifactAuto') } },
  { value: 'image', get label() { return t('pipelineJob.artifactImage') } },
  { value: 'dist', get label() { return t('pipelineJob.artifactDist') } },
  { value: 'jar', get label() { return t('pipelineJob.artifactJar') } },
  { value: 'archive', get label() { return t('pipelineJob.deployArtifactArchive') } },
]

// `when` helpers
const modelIs = (v: string) => (c: Record<string, string>) =>
  (c.buildModel || 'dockerfile') === v
const probeIs = (v: string) => (c: Record<string, string>) =>
  (c.healthProbe || 'none') === v
// 构建任务的产物档位分派:镜像走 docker/工具链镜像构建,jar|dist 走脚本容器。
// 档位未选时两边都不成立 —— 表单只剩「产物档位」一项,与后端的必填校验同一口径(不猜默认档)。
const tierIsImage = (c: Record<string, string>) => c.artifactType === 'image'
const tierIsFile  = (c: Record<string, string>) => c.artifactType === 'jar' || c.artifactType === 'dist'
const imageNeedsBuildEnv = (c: Record<string, string>) => tierIsImage(c) && modelIs('toolchain')(c)
const needsBuildEnvField = (c: Record<string, string>) => tierIsFile(c) || imageNeedsBuildEnv(c)

/**
 * 给一组字段统一套上可见性条件(已有 `when` 的字段取交集)。
 * 用属性描述符复制而非展开:label/hint 是 getter(切语言即时生效),展开会把文案冻结成当下语言。
 */
function gated(fields: JobField[], when: (c: Record<string, string>) => boolean): JobField[] {
  return fields.map((f) => {
    const own = f.when
    return Object.create(Object.getPrototypeOf(f) as object, {
      ...Object.getOwnPropertyDescriptors(f),
      when: {
        value: own ? (c: Record<string, string>) => when(c) && own(c) : when,
        enumerable: true,
        configurable: true,
      },
    }) as JobField
  })
}

// ─── Per-type specs ──────────────────────────────────────────────────────────────

// 任务级执行选项(P0 引擎能力):超时 / 重试 / 资源规格。脚本类节点共用。
// 全部可选;留空 = 旧行为(不限超时、不重试、不限资源)。timeout/retry 为非负整数。
const EXEC_OPTION_FIELDS: JobField[] = [
  {
    key: 'timeoutSeconds',
    get label() { return t('pipelineJob.fieldTimeoutLabel') },
    kind: 'number',
    placeholder: '0',
    get hint() { return t('pipelineJob.fieldTimeoutHint') },
  },
  {
    key: 'retries',
    get label() { return t('pipelineJob.fieldRetriesLabel') },
    kind: 'number',
    placeholder: '0',
    get hint() { return t('pipelineJob.fieldRetriesHint') },
  },
  {
    key: 'cpu',
    get label() { return t('pipelineJob.fieldCpuLabel') },
    kind: 'text',
    monospace: true,
    placeholder: '1',
    get hint() { return t('pipelineJob.fieldCpuHint') },
  },
  {
    key: 'memory',
    get label() { return t('pipelineJob.fieldMemoryLabel') },
    kind: 'text',
    monospace: true,
    placeholder: '512m',
    get hint() { return t('pipelineJob.fieldMemoryHint') },
  },
]

const SCRIPT_FIELDS: JobField[] = [
  {
    key: 'buildEnvId',
    get label() { return t('pipelineJob.fieldBuildEnvLabel') },
    kind: 'buildenv',
    get hint() { return t('pipelineJob.fieldBuildEnvHint') },
  },
  {
    key: 'commands',
    get label() { return t('pipelineJob.fieldCommandsLabel') },
    kind: 'textarea',
    monospace: true,
    placeholder: 'npm ci\nnpm run build',
    get hint() { return t('pipelineJob.fieldCommandsHint') },
  },
  {
    key: 'configProfileIds',
    get label() { return t('pipelineJob.fieldConfigProfilesLabel') },
    kind: 'configprofiles',
    get hint() { return t('pipelineJob.fieldConfigProfilesHint') },
  },
  {
    key: 'workDir',
    get label() { return t('pipelineJob.fieldWorkDirLabel') },
    kind: 'text',
    monospace: true,
    placeholder: '.',
    get hint() { return t('pipelineJob.fieldWorkDirHint') },
  },
  {
    key: 'artifactPath',
    get label() { return t('pipelineJob.fieldArtifactPathLabel') },
    kind: 'textarea',
    monospace: true,
    placeholder: 'frontend/dist\nbackend/target/app.jar',
    get hint() { return t('pipelineJob.fieldArtifactPathHint') },
  },
  ...EXEC_OPTION_FIELDS,
  {
    key: 'cachePaths',
    get label() { return t('pipelineJob.fieldCachePathsLabel') },
    kind: 'textarea',
    monospace: true,
    placeholder: 'node_modules\n.m2/repository',
    get hint() { return t('pipelineJob.fieldCachePathsHint') },
  },
  {
    key: 'cacheKey',
    get label() { return t('pipelineJob.fieldCacheKeyLabel') },
    kind: 'text',
    monospace: true,
    get placeholder() { return t('pipelineJob.fieldCacheKeyPlaceholder') },
    get hint() { return t('pipelineJob.fieldCacheKeyHint') },
  },
]

// 部署节点的字段(deploy_ssh 与遗留 deploy_frontend 节点共用同一套表单)。
const DEPLOY_SSH_FIELDS: JobField[] = [
  {
    key: 'serverId',
    get label() { return t('pipelineJob.fieldServerIdLabel') },
    kind: 'server',
    get hint() { return t('pipelineJob.fieldServerIdHint') },
  },
  {
    key: 'artifactType',
    get label() { return t('pipelineJob.fieldArtifactTypeLabel') },
    kind: 'select',
    options: DEPLOY_ARTIFACT_OPTIONS,
    get hint() { return t('pipelineJob.fieldArtifactTypeHint') },
  },
  {
    key: 'deployPath',
    get label() { return t('pipelineJob.fieldDeployPathLabel') },
    kind: 'text',
    monospace: true,
    placeholder: '/opt/app',
    get hint() { return t('pipelineJob.fieldDeployPathHint') },
    when: (c) => c.artifactType !== 'image',
  },
  {
    key: 'containerName',
    get label() { return t('pipelineJob.fieldContainerNameLabel') },
    kind: 'text',
    monospace: true,
    placeholder: 'app',
    get hint() { return t('pipelineJob.fieldContainerNameHint') },
    when: (c) => c.artifactType === 'image',
  },
  {
    key: 'ports',
    get label() { return t('pipelineJob.fieldPortsLabel') },
    kind: 'text',
    monospace: true,
    placeholder: '8080:80, 9000:9000',
    get hint() { return t('pipelineJob.fieldPortsHint') },
    when: (c) => c.artifactType === 'image',
  },
  {
    key: 'runArgs',
    get label() { return t('pipelineJob.fieldRunArgsLabel') },
    kind: 'text',
    monospace: true,
    placeholder: '-e KEY=value --restart always',
    get hint() { return t('pipelineJob.fieldRunArgsHint') },
    when: (c) => c.artifactType === 'image',
  },
  {
    key: 'strategy',
    get label() { return t('pipelineJob.fieldStrategyLabel') },
    kind: 'select',
    options: DEPLOY_STRATEGY_OPTIONS,
  },
  {
    key: 'restartCommand',
    get label() { return t('pipelineJob.fieldRestartCommandLabel') },
    kind: 'textarea',
    monospace: true,
    placeholder: 'systemctl restart app\nnginx -s reload',
    get hint() { return t('pipelineJob.fieldRestartCommandHint') },
    when: (c) => c.artifactType !== 'image',
  },
  // 健康门控(原「健康检查」节点迁到这里):部署命令在目标机跑成功后,经**同一条 SSH 链路**
  // 再探测该机,不通即判本节点失败并阻断下游(蓝绿策略下还会回滚上一版)。
  // 没有「期望状态码」这类字段 —— 执行侧用的是 `curl -fsS`,4xx/5xx 即视为不通。
  {
    key: 'healthProbe',
    get label() { return t('pipelineJob.fieldHealthProbeLabel') },
    kind: 'select',
    options: PROBE_MODE_OPTIONS,
    get hint() { return t('pipelineJob.fieldHealthProbeHint') },
  },
  {
    key: 'healthUrl',
    get label() { return t('pipelineJob.fieldHealthUrlLabel') },
    kind: 'text',
    monospace: true,
    placeholder: 'http://localhost:8080/healthz',
    get hint() { return t('pipelineJob.fieldHealthUrlHint') },
    when: probeIs('http'),
  },
  {
    key: 'healthCommand',
    get label() { return t('pipelineJob.fieldHealthCommandLabel') },
    kind: 'text',
    monospace: true,
    placeholder: 'test -f /opt/app/current/OK',
    get hint() { return t('pipelineJob.fieldHealthCommandHint') },
    when: probeIs('command'),
  },
  {
    key: 'healthRetries',
    get label() { return t('pipelineJob.fieldHealthRetriesLabel') },
    kind: 'number',
    placeholder: '3',
    get hint() { return t('pipelineJob.fieldHealthRetriesHint') },
    when: (c) => c.healthProbe === 'http' || c.healthProbe === 'command',
  },
  {
    key: 'healthInterval',
    get label() { return t('pipelineJob.fieldHealthIntervalLabel') },
    kind: 'number',
    placeholder: '3',
    get hint() { return t('pipelineJob.fieldHealthIntervalHint') },
    when: (c) => c.healthProbe === 'http' || c.healthProbe === 'command',
  },
  {
    key: 'healthTimeout',
    get label() { return t('pipelineJob.fieldHealthTimeoutLabel') },
    kind: 'number',
    placeholder: '5',
    get hint() { return t('pipelineJob.fieldHealthTimeoutHint') },
    when: (c) => c.healthProbe === 'http' || c.healthProbe === 'command',
  },
]

export const JOB_TYPE_SPECS: Record<string, JobTypeSpec> = {
  git_source: {
    type: 'git_source',
    get label() { return t('pipelineJob.typeGitSourceLabel') },
    get description() { return t('pipelineJob.typeGitSourceDesc') },
    accent: 'cyan',
    category: 'source',
    fields: [
      {
        key: 'repoUrl',
        get label() { return t('pipelineJob.fieldRepoUrlLabel') },
        kind: 'text',
        monospace: true,
        placeholder: 'https://gitee.com/org/repo.git',
        get hint() { return t('pipelineJob.fieldRepoUrlHint') },
      },
      {
        key: 'branch',
        get label() { return t('pipelineJob.fieldBranchLabel') },
        kind: 'text',
        monospace: true,
        placeholder: 'main',
        get hint() { return t('pipelineJob.fieldBranchHint') },
      },
      {
        key: 'credentialId',
        get label() { return t('pipelineJob.fieldCredentialIdLabel') },
        kind: 'credential',
        credentialType: 'git_token',
        get hint() { return t('pipelineJob.fieldCredentialIdHint') },
      },
      {
        key: 'depth',
        get label() { return t('pipelineJob.fieldDepthLabel') },
        kind: 'number',
        placeholder: '1',
        get hint() { return t('pipelineJob.fieldDepthHint') },
      },
    ],
  },

  // ── 构建任务:唯一入口,产物档位决定它到底是「跑脚本收文件」还是「造镜像」──
  // 旧的 build_image / build_frontend / build_backend 都收敛到这里(见下方 legacy spec)。
  build: {
    type: 'build',
    get label() { return t('pipelineJob.typeBuildLabel') },
    get description() { return t('pipelineJob.typeBuildDesc') },
    accent: 'primary',
    category: 'build',
    fields: [
      {
        key: 'artifactType',
        get label() { return t('pipelineJob.fieldArtifactTypeBuildLabel') },
        kind: 'select',
        options: BUILD_TIER_OPTIONS,
        get hint() { return t('pipelineJob.fieldBuildTierHint') },
      },
      // 镜像小节:构建模型(Dockerfile 自带 / 平台工具链)+ 推送时机。
      {
        key: 'buildModel',
        get label() { return t('pipelineJob.fieldBuildModelLabel') },
        kind: 'select',
        options: BUILD_MODEL_OPTIONS,
        get hint() { return t('pipelineJob.fieldBuildModelHint') },
        when: tierIsImage,
      },
      {
        key: 'dockerfilePath',
        get label() { return t('pipelineJob.fieldDockerfilePathLabel') },
        kind: 'text',
        monospace: true,
        placeholder: 'Dockerfile',
        when: (c) => tierIsImage(c) && modelIs('dockerfile')(c),
      },
      {
        key: 'context',
        get label() { return t('pipelineJob.fieldContextLabel') },
        kind: 'text',
        monospace: true,
        placeholder: '.',
        get hint() { return t('pipelineJob.fieldContextHint') },
        when: (c) => tierIsImage(c) && modelIs('dockerfile')(c),
      },
      {
        key: 'pushImage',
        get label() { return t('pipelineJob.fieldPushImageLabel') },
        kind: 'toggle',
        get hint() { return t('pipelineJob.fieldPushImageHint') },
        when: tierIsImage,
      },
      // 工具链镜像与文件产物都要选预置构建环境(Dockerfile 档不需要:镜像由 FROM 决定)。
      {
        key: 'buildEnvId',
        get label() { return t('pipelineJob.fieldBuildEnvLabel') },
        kind: 'buildenv',
        get hint() { return t('pipelineJob.fieldBuildEnvHint') },
        when: needsBuildEnvField,
      },
      {
        key: 'buildCommand',
        get label() { return t('pipelineJob.fieldBuildCommandLabel') },
        kind: 'text',
        monospace: true,
        placeholder: 'mvn -B -DskipTests package',
        get hint() { return t('pipelineJob.fieldBuildCommandHint') },
        when: imageNeedsBuildEnv,
      },
      // 文件产物小节:与脚本任务同一套键(commands 多行 / artifactPath 逐行 glob)。
      ...gated(SCRIPT_FIELDS.slice(1), tierIsFile),
    ],
    // 档位留空(必选,无默认)+ 镜像小节先按 Dockerfile 展开 + 缺省推送(旧行为)。
    defaultConfig: { buildModel: 'dockerfile', pushImage: 'true' },
    templates: [
      {
        id: 'frontend',
        get label() { return t('pipelineJob.buildTemplateFrontendLabel') },
        get description() { return t('pipelineJob.buildTemplateFrontendDesc') },
        prefill: {
          artifactType: 'dist',
          commands: 'cd frontend\nnpm install --no-audit --no-fund\nnpm run build',
          artifactPath: 'frontend/dist',
        },
      },
      {
        id: 'backend',
        get label() { return t('pipelineJob.buildTemplateBackendLabel') },
        get description() { return t('pipelineJob.buildTemplateBackendDesc') },
        prefill: {
          artifactType: 'jar',
          commands: 'cd backend\nmvn -B -DskipTests package',
          artifactPath: 'backend/target/*.jar',
        },
      },
      {
        id: 'docker_image',
        get label() { return t('pipelineJob.buildTemplateImageLabel') },
        get description() { return t('pipelineJob.buildTemplateImageDesc') },
        prefill: { artifactType: 'image', buildModel: 'dockerfile', dockerfilePath: 'Dockerfile' },
      },
    ],
    managedKeys: ['image', 'toolchainLanguage', 'toolchainVersion'],
  },

  // build_image 是旧的独立「构建镜像」类型:字段与 build 的镜像小节一致,执行路径也相同
  // (后端 EffectiveJobType 只在档位为 image 时把它折到这条路径)。**不再作为可选类型**,
  // spec 保留只为让存量节点正常渲染;存量请改用「构建」任务后重存(不做自动迁移)。
  build_image: {
    type: 'build_image',
    get label() { return t('pipelineJob.typeBuildImageLabel') },
    get description() { return t('pipelineJob.typeBuildImageDesc') },
    accent: 'primary',
    category: 'build',
    fields: [
      { key: 'artifactType', get label() { return t('pipelineJob.fieldArtifactTypeBuildLabel') }, kind: 'select', options: ARTIFACT_OPTIONS },
      {
        key: 'buildModel',
        get label() { return t('pipelineJob.fieldBuildModelLabel') },
        kind: 'select',
        options: BUILD_MODEL_OPTIONS,
        get hint() { return t('pipelineJob.fieldBuildModelHint') },
      },
      {
        key: 'dockerfilePath',
        get label() { return t('pipelineJob.fieldDockerfilePathLabel') },
        kind: 'text',
        monospace: true,
        placeholder: 'Dockerfile',
        when: modelIs('dockerfile'),
      },
      {
        key: 'context',
        get label() { return t('pipelineJob.fieldContextLabel') },
        kind: 'text',
        monospace: true,
        placeholder: '.',
        get hint() { return t('pipelineJob.fieldContextHint') },
        when: modelIs('dockerfile'),
      },
      {
        key: 'buildEnvId',
        get label() { return t('pipelineJob.fieldBuildEnvLabel') },
        kind: 'buildenv',
        when: modelIs('toolchain'),
        get hint() { return t('pipelineJob.fieldBuildEnvHint') },
      },
      {
        key: 'buildCommand',
        get label() { return t('pipelineJob.fieldBuildCommandLabel') },
        kind: 'text',
        monospace: true,
        placeholder: 'npm run build',
        get hint() { return t('pipelineJob.fieldBuildCommandHint') },
        when: modelIs('toolchain'),
      },
    ],
    // 工具链镜像由 buildEnvId 控件写入这两个旧键(后端仍按它们查目录),不给手填入口。
    managedKeys: ['image', 'toolchainLanguage', 'toolchainVersion'],
  },

  push_image: {
    type: 'push_image',
    get label() { return t('pipelineJob.typePushImageLabel') },
    get description() { return t('pipelineJob.typePushImageDesc') },
    accent: 'amber',
    category: 'build',
    // 推送目标**不在这里填**:真实推送走「环境」绑定的镜像仓(服务端按本次运行所在环境
    // 解析 registry + 凭据,tag 用 <项目名>:<commit7>)。旧的 registry/imageName/tag/credentialId
    // 四个输入框没有任何执行侧消费者,是假字段(v6.2 R5),已删除。
    fields: [
      {
        key: 'pushTarget',
        get label() { return t('pipelineJob.fieldPushTargetLabel') },
        kind: 'pushTarget',
        get hint() { return t('pipelineJob.fieldPushTargetHint') },
      },
    ],
    droppedKeys: ['registry', 'imageName', 'tag', 'credentialId'],
  },

  deploy_ssh: {
    type: 'deploy_ssh',
    get label() { return t('pipelineJob.typeDeployLabel') },
    get description() { return t('pipelineJob.typeDeployDesc') },
    accent: 'green',
    category: 'deploy',
    fields: DEPLOY_SSH_FIELDS,
    templates: [{
      id: 'frontend_static',
      get label() { return t('pipelineJob.deployTemplateFrontendLabel') },
      get description() { return t('pipelineJob.deployTemplateFrontendDesc') },
      prefill: { artifactType: 'dist', strategy: 'rolling', restartCommand: 'nginx -s reload' },
    }],
  },

  // health_check 不是一种任务:它过去只是个占位节点(表单填的探测参数无人消费、执行侧恒放行),
  // 真正的门控在部署服务里。现按部署任务的「健康探测」配置生效,撤销该类型;存量节点保存时会被
  // 后端拒绝并给出改配指引(不做静默放行,也不自动迁移)。

  notify: {
    type: 'notify',
    get label() { return t('pipelineJob.typeNotifyLabel') },
    get description() { return t('pipelineJob.typeNotifyDesc') },
    accent: 'cyan',
    category: 'notify',
    fields: [
      {
        key: 'channel',
        get label() { return t('pipelineJob.fieldChannelLabel') },
        kind: 'channel',
        get hint() { return t('pipelineJob.fieldChannelHint') },
      },
      {
        key: 'titleTemplate',
        get label() { return t('pipelineJob.fieldTitleTemplateLabel') },
        kind: 'text',
        get placeholder() { return t('pipelineJob.fieldTitleTemplatePlaceholder') },
        get hint() { return t('pipelineJob.fieldTitleTemplateHint') },
      },
      {
        key: 'bodyTemplate',
        get label() { return t('pipelineJob.fieldBodyTemplateLabel') },
        kind: 'textarea',
        get placeholder() { return t('pipelineJob.fieldBodyTemplatePlaceholder') },
        get hint() { return t('pipelineJob.fieldBodyTemplateHint') },
      },
    ],
  },

  // ── 模板节点(旧):预填好常见步骤参数的脚本任务。**不再作为可选类型**,
  // 能力已做成「构建」任务的模板(见 JOB_TYPE_SPECS.build.templates);存量节点照常渲染执行。
  build_frontend: {
    type: 'build_frontend',
    get label() { return t('pipelineJob.typeBuildFrontendLabel') },
    get description() { return t('pipelineJob.typeBuildFrontendDesc') },
    accent: 'primary',
    category: 'build',
    fields: SCRIPT_FIELDS,
    // 预填命令/产物,但**不预填镜像**:镜像只能来自预置构建环境目录(R4),
    // 未选环境时保存校验会要求先选,写死一个默认镜像等于绕过目录。
    defaultConfig: {
      commands: 'cd frontend\nnpm install --no-audit --no-fund\nnpm run build',
      artifactPath: 'frontend/dist',
    },
    managedKeys: ['image'],
  },

  build_backend: {
    type: 'build_backend',
    get label() { return t('pipelineJob.typeBuildBackendLabel') },
    get description() { return t('pipelineJob.typeBuildBackendDesc') },
    accent: 'amber',
    category: 'build',
    fields: SCRIPT_FIELDS,
    defaultConfig: {
      commands: 'cd backend\nmvn -B -DskipTests package',
      artifactPath: 'backend/target/*.jar',
    },
    managedKeys: ['image'],
  },

  // deploy_frontend 是旧的「前端部署」类型:与 deploy_ssh 同一条执行路径、同一套字段,
  // 差别只是预填了 nginx reload。**不再作为可选类型**(前端部署已做成 deploy_ssh 的模板),
  // 但 spec 保留:存量节点要能正常渲染表单。存量请改成「部署」+ 前端模板后重存(不做自动迁移)。
  deploy_frontend: {
    type: 'deploy_frontend',
    get label() { return t('pipelineJob.typeDeployFrontendLabel') },
    get description() { return t('pipelineJob.typeDeployFrontendDesc') },
    accent: 'green',
    category: 'deploy',
    fields: DEPLOY_SSH_FIELDS,
    defaultConfig: { strategy: 'rolling', restartCommand: 'nginx -s reload' },
  },

  templated: {
    type: 'templated',
    get label() { return t('pipelineJob.typeTemplatedLabel') },
    get description() { return t('pipelineJob.typeTemplatedDesc') },
    accent: 'cyan',
    category: 'custom',
    fields: [
      {
        key: 'image',
        get label() { return t('pipelineJob.fieldImageLabel') },
        kind: 'text',
        monospace: true,
        placeholder: '{{image}}(参数域 = 预置目录)',
        get hint() { return t('pipelineJob.fieldImagePresetHint') },
      },
      {
        key: 'params',
        get label() { return t('pipelineJob.fieldParamsLabel') },
        kind: 'textarea',
        monospace: true,
        placeholder: 'dir=frontend\nbuildCmd=npm run build',
        get hint() { return t('pipelineJob.fieldParamsHint') },
      },
      {
        key: 'configProfileIds',
        get label() { return t('pipelineJob.fieldConfigProfilesLabel') },
        kind: 'configprofiles',
        get hint() { return t('pipelineJob.fieldConfigProfilesHint') },
      },
      {
        key: 'commandTemplate',
        get label() { return t('pipelineJob.fieldCommandTemplateLabel') },
        kind: 'textarea',
        monospace: true,
        placeholder: 'cd {{dir}}\nnpm install\n{{buildCmd}}',
        get hint() { return t('pipelineJob.fieldCommandTemplateHint') },
      },
      {
        key: 'artifactPath',
        get label() { return t('pipelineJob.fieldArtifactPathLabel') },
        kind: 'textarea',
        monospace: true,
        placeholder: '{{dir}}/dist',
        get hint() { return t('pipelineJob.fieldTemplatedArtifactPathHint') },
      },
      {
        key: 'workDir',
        get label() { return t('pipelineJob.fieldWorkDirLabel') },
        kind: 'text',
        monospace: true,
        placeholder: '.',
        get hint() { return t('pipelineJob.fieldTemplatedWorkDirHint') },
      },
      ...EXEC_OPTION_FIELDS,
      {
        key: 'cachePaths',
        get label() { return t('pipelineJob.fieldCachePathsLabel') },
        kind: 'textarea',
        monospace: true,
        placeholder: '{{dir}}/node_modules',
        get hint() { return t('pipelineJob.fieldTemplatedCachePathsHint') },
      },
      {
        key: 'cacheKey',
        get label() { return t('pipelineJob.fieldCacheKeyLabel') },
        kind: 'text',
        monospace: true,
        get placeholder() { return t('pipelineJob.fieldCacheKeyPlaceholder') },
        get hint() { return t('pipelineJob.fieldTemplatedCacheKeyHint') },
      },
    ],
    defaultConfig: {
      // 不预填镜像字面值:模板节点的镜像只能是 {{参数}}(参数域 = 预置目录),
      // 留空让用户自己选/写占位,写死 node:20-alpine 等于绕过目录。
      params: 'dir=frontend\nbuildCmd=npm run build',
      commandTemplate: 'cd {{dir}}\nnpm install --no-audit --no-fund\n{{buildCmd}}',
      artifactPath: '{{dir}}/dist',
    },
  },

  script: {
    type: 'script',
    get label() { return t('pipelineJob.typeScriptLabel') },
    get description() { return t('pipelineJob.typeScriptDesc') },
    accent: 'primary',
    category: 'custom',
    fields: SCRIPT_FIELDS,
    managedKeys: ['image'],
  },

  // custom 是 script 的旧别名:字段/执行路径完全相同,只是历史上多一个 type 值。
  // 不再作为可选类型,存量节点按 script 渲染,保存时由 canonicalJobType 收敛回 script。
  custom: {
    type: 'custom',
    aliasOf: 'script',
    get label() { return t('pipelineJob.typeScriptLabel') },
    get description() { return t('pipelineJob.typeScriptDesc') },
    accent: 'primary',
    category: 'custom',
    fields: SCRIPT_FIELDS,
    managedKeys: ['image'],
  },
}

// ─── Lookups ──────────────────────────────────────────────────────────────────

/**
 * Canonical, ordered list of pickable types — 一个动词一张卡:
 * 源 / 构建 / 部署 / 通知 / 脚本 / 模板节点。
 * 不在列表里的仍是有效类型(build_image、build_frontend、build_backend、push_image、
 * deploy_frontend、custom):spec 保留以便存量节点照常渲染与执行,保存时该收敛的收敛
 * (见 canonicalJobType),该按新类型重选的由后端 422 明确报错 —— 不做静默迁移。
 */
export const PICKABLE_TYPES: readonly string[] = [
  'git_source',
  'build',
  'deploy_ssh',
  'notify',
  'script',
  'templated',
]

/** Dropdown options for the job type selector (friendly label + token). */
export const JOB_TYPE_OPTIONS: SelectOption[] = PICKABLE_TYPES.map((type) => ({
  value: type,
  get label() {
    return `${JOB_TYPE_SPECS[type].label} · ${type}`
  },
}))

/** Picker categories in display order. */
export const JOB_CATEGORIES: ReadonlyArray<{ id: CategoryId; label: string }> = [
  { id: 'source', get label() { return t('pipelineJob.categorySource') } },
  { id: 'build', get label() { return t('pipelineJob.categoryBuild') } },
  { id: 'deploy', get label() { return t('pipelineJob.categoryDeploy') } },
  { id: 'quality', get label() { return t('pipelineJob.categoryQuality') } },
  { id: 'notify', get label() { return t('pipelineJob.categoryNotify') } },
  { id: 'custom', get label() { return t('pipelineJob.categoryCustom') } },
]

/** Pickable specs grouped by category, in display order; empty groups omitted. */
export function groupedJobTypes(): Array<{ id: CategoryId; label: string; specs: JobTypeSpec[] }> {
  return JOB_CATEGORIES.map((cat) => ({
    id: cat.id,
    label: cat.label,
    specs: PICKABLE_TYPES.map((t) => JOB_TYPE_SPECS[t]).filter((s) => s.category === cat.id),
  })).filter((g) => g.specs.length > 0)
}

/**
 * 别名类型 → 规范类型(custom → script)。非别名原样返回。
 * 保存时过一次,存量节点的旧类型在用户下次保存时收敛掉 —— 后端仍认别名,所以旧流水线照跑。
 */
export function canonicalJobType(type: string): string {
  const spec = JOB_TYPE_SPECS[type]
  return spec?.aliasOf ?? type
}

/** Template prefill for a task type + template id; empty map when the type/template is unknown. */
export function jobTemplatePrefill(type: string, templateId: string): Record<string, string> {
  const tpl = JOB_TYPE_SPECS[type]?.templates?.find((x) => x.id === templateId)
  return tpl ? { ...tpl.prefill } : {}
}

/** Accent colour for a type, falling back to neutral for unknown types. */
export function jobTypeAccent(type: string): AccentName {
  return JOB_TYPE_SPECS[type]?.accent ?? 'neutral'
}

/** Spec for a job type, or null when the type has no typed schema. */
export function getJobTypeSpec(type: string): JobTypeSpec | null {
  return JOB_TYPE_SPECS[type] ?? null
}

/** Friendly zh label for a type, falling back to the raw token. */
export function jobTypeLabel(type: string): string {
  return JOB_TYPE_SPECS[type]?.label ?? type
}

/** The set of config keys owned by a type's schema (used to split out raw extras). */
export function schemaKeys(type: string): Set<string> {
  const spec = JOB_TYPE_SPECS[type]
  return new Set(spec ? spec.fields.map((f) => f.key) : [])
}

/** 该 type 下要直接丢弃的遗留键(假字段):既不显示也不回写。见 JobTypeSpec.droppedKeys。 */
export function droppedKeys(type: string): Set<string> {
  return new Set(JOB_TYPE_SPECS[type]?.droppedKeys ?? [])
}

/** 该 type 下由构建环境控件托管的桥接键:留在 config 但不进表单、也不进「原始参数」。 */
export function managedKeys(type: string): Set<string> {
  return new Set(JOB_TYPE_SPECS[type]?.managedKeys ?? [])
}

/**
 * Split a config map into the keys owned by the type's schema vs. the rest
 * (rendered in the "raw parameters" advanced section). Order of extras preserved.
 * Dropped and managed keys appear in **neither** side —— 托管键的值仍由 typedConfig 原样带回。
 */
export function splitConfig(
  type: string,
  config: Record<string, string>,
): { extras: Array<[string, string]> } {
  const owned = schemaKeys(type)
  const drop = droppedKeys(type)
  const managed = managedKeys(type)
  const extras = Object.entries(config).filter(
    ([k]) => !owned.has(k) && !drop.has(k) && !managed.has(k),
  )
  return { extras }
}

/**
 * 「脚本类」节点:后端 `isScriptJob`(internal/build/dag_stage_exec.go)按 script 路径执行
 * (容器跑 + 收 artifactPath 产物)的那批 type。可视化步骤构建器只对这些 type 出现,
 * 因为它编译/反解析的就是这套 commands/artifactPath 键。与后端保持一致。
 */
const SCRIPT_CLASS_TYPES = new Set<string>([
  'script',
  'custom',
  'build_frontend',
  'build_backend',
  'templated',
])

/**
 * 「构建」任务按产物档位折算成真正执行它的类型(与后端 pipeline.EffectiveJobType 同语义):
 * 镜像 → build_image 路径,jar/dist → script 路径。派发与表单都只认折算结果。
 *
 * 与后端的唯一差别:档位**未选**时这里返回 `build`(哪条路径都不是),让表单只剩「选档位」
 * 一件事;后端那侧的空档位会被保存校验直接拒绝,不存在两条路径之外的执行。
 */
export function effectiveJobType(type: string, config: Record<string, string>): string {
  const t = type.trim()
  if (t !== 'build') return t
  const tier = (config.artifactType ?? '').trim()
  if (tier === 'image') return 'build_image'
  if (tier === 'jar' || tier === 'dist') return 'script'
  return 'build'
}

export function isScriptClassType(type: string, config: Record<string, string> = {}): boolean {
  return SCRIPT_CLASS_TYPES.has(effectiveJobType(type, config))
}
