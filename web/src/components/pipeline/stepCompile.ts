/**
 * stepCompile — 可视化「步骤构建器」与现有可执行 config 的双向纯函数转换。
 *
 * 设计契约(绝不改后端执行语义):
 * 脚本类节点(script/custom/build_frontend/build_backend/templated)在后端经
 * `scriptStepFromJob` 读取这些 config 键执行:
 *   - `image`        运行镜像(节点级,非步骤)
 *   - `commands`     多行命令字符串,按 `\n` 拆成逐行命令,在容器内拼成一个
 *                    `set -e` 单脚本经 `sh -c` 执行(见 internal/build/dag_stage_exec.go:25)
 *   - `workDir`      步骤起始工作目录(节点级)
 *   - `artifactPath` 多行产物路径(每行一条 glob,节点级固定属性,不由步骤产出)
 * 后端**不读** `env` 这类键 —— 所以「设环境变量」步骤被编译成 `export K=V` 命令行;
 * 「切目录」步骤被编译成 `cd DIR` 命令行。因为所有命令同处一个 shell 脚本,
 * `export` / `cd` 对后续命令生效,顺序语义被忠实保留,且零后端改动。
 *
 * 「条件守卫」(condition)步骤编译成**单行** `if ! ( <cond> ); then echo …; exit 0; fi`:
 * 条件不成立则整段脚本提前**成功退出**,后续步骤全部跳过。这是 `set -e` **安全**的 ——
 * 条件 `<cond>` 跑在 `if` 测试上下文里(set -e 对其豁免,即便条件命令非零退出也不会中断脚本),
 * 命中则 `exit 0` 干净结束;且整条是单行,逐行反解析仍能原样还原条件,round-trip 不丢数据。
 *
 * 「复制文件」(copy)步骤编译成**单行** `if command -v rsync …; then rsync -a --exclude=…;
 * else cp -a …; fi`:优先 rsync(支持排除目录/通配、增量覆盖),容器里没装则退回 `cp -a`。
 * `command -v` 探针跑在 `if` 测试上下文里,对 `set -e` 安全;整条单行仍能逐行反解析还原。
 * 填了排除项时,else 分支前额外挂一句 `echo '…排除项未生效'`:cp 没有排除选项,静默整份复制
 * 是最难发现的失败,所以让运行日志自己说清「这镜像没 rsync,排除项被忽略了」。
 *
 * 两种复制方式(2026-10 起):
 *   - contents(默认,老行只有这一种):把「源目录的内容」铺进目标目录 ——
 *     rsync 靠源尾斜杠 `src/`、cp 靠 `src/.`。源写 `./` 时没有目录名可保留,即工作区根内容。
 *   - entry:把「源文件夹/文件本身」放进目标目录并保留名称 —— rsync 源不带尾斜杠、cp 直接
 *     `cp -a src dest/`,于是 `frontend/dist → out` 得到 `out/dist`,单个文件也能复制。
 * 两种方式的行骨架只差源 token(带不带 `/`、`/.`),反解析据此区分;老配置一律读回 contents,
 * 行为不变。
 *
 * 编译(steps → config):把有序步骤列表展开成 `commands` 多行串。产物目录不属于步骤,
 * 它是节点的固定属性(`artifactPath`,档位=产物时表单里始终有这一格),由调用方单独持有;
 * `image`/`workDir` 同样是节点级字段(由调用方合并)。
 *
 * 反解析(config → steps):只吃 `commands`,按行识别成「设环境变量」/「切目录」/
 * 「复制文件」/「条件守卫」/「运行命令」几类步骤。
 * 反解析允许有损(注释、奇异语法仍归类为「运行命令」原样保留),绝不丢用户数据。
 */

// ─── 步骤模型 ──────────────────────────────────────────────────────────────────

import { t } from '../../i18n'

export type StepKind = 'command' | 'env' | 'workDir' | 'copy' | 'condition'

/**
 * 复制方式:contents=复制目录内容(老语义,默认);entry=把文件夹/文件本身放进目标目录(保留名称)。
 */
export type CopyMode = 'contents' | 'entry'

export interface StepBlock {
  /** 稳定的本地标识(列表 key / 拖拽用),不进 config */
  id: string
  kind: StepKind
  /** command:整段命令(可多行) */
  command?: string
  /** env:环境变量名 */
  envKey?: string
  /** env:环境变量值 */
  envValue?: string
  /** workDir:目标目录 */
  dir?: string
  /** copy:复制源目录 */
  copySrc?: string
  /** copy:复制目标目录 */
  copyDest?: string
  /** copy:排除项(空格/逗号分隔,编译成若干 `--exclude=`) */
  copyExcludes?: string
  /** copy:复制方式;缺省(含老配置)=复制目录内容 */
  copyMode?: CopyMode
  /** condition:shell 条件表达式(不成立则跳过后续步骤) */
  condition?: string
}

/** 编译产出:展开后的 config 片段(只含步骤拥有的 commands 键;产物目录是节点级字段)。 */
export interface CompiledSteps {
  commands: string
}

let _seq = 0
/** 生成稳定的步骤 id(仅前端,不进 config)。 */
export function nextStepId(): string {
  _seq += 1
  return `step-${_seq}-${Math.random().toString(36).slice(2, 7)}`
}

// ─── 编译:steps → config ───────────────────────────────────────────────────────

/** POSIX shell 单引号转义:把值安全包进 '...'(内部单引号 → '\''). */
export function shellQuote(value: string): string {
  return `'${value.replace(/'/g, `'\\''`)}'`
}

// 条件守卫单行模板:`if ! ( <cond> ); then echo '…'; exit 0; fi`(set -e 安全;cond 原样,不转义)。
const COND_GUARD_PREFIX = 'if ! ( '
const COND_GUARD_SUFFIX = " ); then echo '条件不成立,跳过后续步骤'; exit 0; fi"

/** 把条件表达式包成单行 set -e 安全的守卫(不成立→提前成功退出)。 */
export function conditionGuardLine(cond: string): string {
  return `${COND_GUARD_PREFIX}${cond}${COND_GUARD_SUFFIX}`
}

// 复制步骤的单行骨架(逐段拼,反解析按同样字面切回去;改动须两侧同步)。
const COPY_HEAD = 'if command -v rsync >/dev/null 2>&1; then mkdir -p '
const COPY_RSYNC = ' && rsync -a '
const COPY_ELSE = '; else '
const COPY_MKDIR = 'mkdir -p '
const COPY_CP = ' && cp -a '
const COPY_TAIL = '; fi'
const COPY_EXCLUDE = '--exclude='
// cp 没有排除选项 → 填了排除项却没 rsync 时,那几项会被静默丢掉。光靠 UI 猜不出镜像里
// 到底有没有 rsync(只有容器里知道),所以把告警下到这行命令里:真走了兜底,运行日志就说实话。
// 必须是常量 —— 反解析按字面吃这一段,带变量的文案切不回来。
const COPY_WARN = "echo '没有 rsync,复制退回 cp:排除项未生效'; "

/** 排除项文本 → 列表:空格与逗号都是分隔符(rsync 的 --exclude 本身不含空格)。 */
export function splitExcludes(text: string): string[] {
  return text.split(/[\s,]+/).map((s) => s.trim()).filter(Boolean)
}

/** 去掉路径尾部斜杠(只剩斜杠的路径会归一成空串,由调用方当「没填」丢掉)。 */
function trimTrailingSlash(p: string): string {
  return p.replace(/\/+$/, '')
}

/**
 * 把复制步骤编译成单行。两种方式的骨架相同,只差源 token:
 *   - contents:`rsync 'src/' 'dest/'` / `cp -a 'src/.' 'dest/'`(复制目录内容)
 *   - entry:   `rsync 'src' 'dest/'` / `cp -a 'src' 'dest/'`(复制文件夹/文件本身,保留名称)
 * 两条分支都先 `mkdir -p 'dest/'`,语义一致。有排除项时兜底分支先 echo 一句告警
 * (cp 不吃排除,日志必须说清那几项没生效)。源/目标尾斜杠会被归一;任一空 → 返回空串。
 */
export function copyStepLine(
  src: string,
  dest: string,
  excludes: readonly string[],
  mode: CopyMode = 'contents',
): string {
  const s = trimTrailingSlash(src.trim())
  const d = trimTrailingSlash(dest.trim())
  if (!s || !d) return ''
  const destTok = shellQuote(`${d}/`)
  const excludeTok = excludes.map((e) => `${COPY_EXCLUDE}${shellQuote(e)} `).join('')
  const warn = excludes.length ? COPY_WARN : ''
  const rsyncSrc = mode === 'entry' ? s : `${s}/`
  const cpSrc = mode === 'entry' ? s : `${s}/.`
  return (
    `${COPY_HEAD}${destTok}${COPY_RSYNC}${excludeTok}${shellQuote(rsyncSrc)} ${destTok}` +
    `${COPY_ELSE}${warn}${COPY_MKDIR}${destTok}${COPY_CP}${shellQuote(cpSrc)} ${destTok}${COPY_TAIL}`
  )
}

/**
 * 把一个步骤编译成 0 条或多条命令行。
 * - command:原样多行(空步骤跳过)
 * - env:`export K=V`(K 合法才发;值做单引号转义)
 * - workDir:`cd DIR`(DIR 做单引号转义)
 * - copy:单行 rsync/cp 兜底(源与目标齐备才发;排除项逐条 `--exclude=`)
 * - condition:`if ! ( <cond> ); then echo …; exit 0; fi`(单行守卫;cond 原样,空步骤跳过)
 */
export function stepToCommandLines(step: StepBlock): string[] {
  switch (step.kind) {
    case 'command': {
      const raw = (step.command ?? '').replace(/\r/g, '')
      return raw.split('\n').filter((l) => l.trim() !== '')
    }
    case 'env': {
      const key = (step.envKey ?? '').trim()
      if (!key) return []
      return [`export ${key}=${shellQuote(step.envValue ?? '')}`]
    }
    case 'workDir': {
      const dir = (step.dir ?? '').trim()
      if (!dir) return []
      return [`cd ${shellQuote(dir)}`]
    }
    case 'copy': {
      const line = copyStepLine(
        step.copySrc ?? '',
        step.copyDest ?? '',
        splitExcludes(step.copyExcludes ?? ''),
        step.copyMode ?? 'contents',
      )
      return line ? [line] : []
    }
    case 'condition': {
      const cond = (step.condition ?? '').replace(/\r?\n/g, ' ').trim()
      if (!cond) return []
      return [conditionGuardLine(cond)]
    }
    default:
      return []
  }
}

/** 把有序步骤列表编译成可执行 config 片段(commands 多行)。 */
export function compileSteps(steps: readonly StepBlock[]): CompiledSteps {
  const cmdLines: string[] = []
  for (const step of steps) cmdLines.push(...stepToCommandLines(step))
  return { commands: cmdLines.join('\n') }
}

// ─── 反解析:config → steps ─────────────────────────────────────────────────────

const ENV_LINE = /^export\s+([A-Za-z_][A-Za-z0-9_]*)=(.*)$/
const CD_LINE = /^cd\s+(.+)$/
// 条件守卫:`if ! ( <cond> ); then echo …; exit 0; fi` → 抽回 <cond>(贪婪到最后一个 `); then echo`)。
const COND_LINE = /^if ! \( (.+) \); then echo .*; exit 0; fi$/

/**
 * 反单引号转义:把 shellQuote 产出的 '...'(内含 '\'' 续接)还原为原始字符串。
 * 逐字符状态机:引号内原样吃,引号外只认 `\'`(转义单引号)= 字面 `'`,其余在引号外的
 * 字符按字面吃(容错)。非单引号开头的 token 原样返回(有损但不丢字符)。
 */
export function shellUnquote(token: string): string {
  const t = token.trim()
  if (!t.startsWith("'")) return t
  let out = ''
  let i = 0
  let inQuote = false
  while (i < t.length) {
    const ch = t[i]
    if (inQuote) {
      if (ch === "'") {
        inQuote = false
        i += 1
      } else {
        out += ch
        i += 1
      }
      continue
    }
    // 引号外
    if (ch === "'") {
      inQuote = true
      i += 1
    } else if (ch === '\\' && i + 1 < t.length && t[i + 1] === "'") {
      // shellQuote 用 '\'' 表达一个字面单引号
      out += "'"
      i += 2
    } else {
      out += ch
      i += 1
    }
  }
  return out
}

/**
 * 从 s[i] 起读一个 shellQuote 产出的 `'…'` token(内部 `'\''` 续接还原成字面单引号)。
 * 返回还原值与其后的下标;不是引号开头 / 引号未闭合 → null(交给上层归类兜底)。
 */
function readQuotedToken(s: string, i: number): { value: string; next: number } | null {
  if (s[i] !== "'") return null
  let out = ''
  let inQuote = true
  let k = i + 1
  while (k < s.length) {
    const ch = s[k]
    if (inQuote) {
      if (ch === "'") {
        inQuote = false
      } else {
        out += ch
      }
      k += 1
      continue
    }
    // 引号外:相邻引号段继续吃,`\'` 是一个字面单引号,其余字符即 token 结束。
    if (ch === "'") {
      inQuote = true
      k += 1
    } else if (ch === '\\' && s[k + 1] === "'") {
      out += "'"
      k += 2
    } else {
      break
    }
  }
  if (inQuote) return null
  return { value: out, next: k }
}

/**
 * 反解析复制步骤:严格按 copyStepLine 的字面骨架逐段吃,并校验两条分支路径一致
 * (mkdir / rsync / cp 三处目标同值;contents 的 `src/` 对 `src/.`,entry 两边同为 `src`)。
 * 源 token 是否带尾斜杠就是两种方式的区别 —— 老行永远带,读回 contents,行为不变。
 * 用户手改过任何一个字面段 → 返回 null,整行退回「运行命令」原样保留(绝不丢内容)。
 */
export function parseCopyLine(line: string): {
  src: string
  dest: string
  excludes: string[]
  mode: CopyMode
} | null {
  const t = line.trim()
  if (!t.startsWith(COPY_HEAD)) return null
  let i = COPY_HEAD.length
  const skip = (lit: string): boolean => {
    if (!t.startsWith(lit, i)) return false
    i += lit.length
    return true
  }
  const take = (): { value: string } | null => {
    const tok = readQuotedToken(t, i)
    if (!tok) return null
    i = tok.next
    return tok
  }
  const destMkdir = take()
  if (!destMkdir || !skip(COPY_RSYNC)) return null
  const excludes: string[] = []
  while (skip(COPY_EXCLUDE)) {
    const e = take()
    if (!e) return null
    excludes.push(e.value)
    if (!skip(' ')) return null
  }
  const rsyncSrc = take()
  if (!rsyncSrc || !skip(' ')) return null
  // 尾斜杠 = 「复制目录内容」;没有 = 「复制文件夹/文件本身」。两种方式的 cp 源据此校验。
  const mode: CopyMode = rsyncSrc.value.endsWith('/') ? 'contents' : 'entry'
  const srcName = mode === 'contents' ? rsyncSrc.value.slice(0, -1) : rsyncSrc.value
  if (!srcName) return null
  const rsyncDest = take()
  if (!rsyncDest || rsyncDest.value !== destMkdir.value || !skip(COPY_ELSE)) return null
  // 兜底告警段:生成器只在有排除项时才写它。老配置里没有这一段(可选吃掉),
  // 而「没有排除项却有告警」只可能是手插的 —— 归成 copy 会把它重生成丢掉,所以退回 command。
  const warned = skip(COPY_WARN)
  if (warned && excludes.length === 0) return null
  if (!skip(COPY_MKDIR)) return null
  const destAgain = take()
  if (!destAgain || destAgain.value !== destMkdir.value || !skip(COPY_CP)) return null
  const cpSrc = take()
  const expectCpSrc = mode === 'contents' ? `${srcName}/.` : srcName
  if (!cpSrc || cpSrc.value !== expectCpSrc || !skip(' ')) return null
  const cpDest = take()
  if (!cpDest || cpDest.value !== destMkdir.value || !t.startsWith(COPY_TAIL, i)) return null
  return { src: srcName, dest: destMkdir.value.replace(/\/+$/, ''), excludes, mode }
}

/**
 * 把一行命令分类成步骤:
 * - rsync/cp 兜底单行  → copy(源/目标/排除逐项还原)
 * - `export K=V`  → env(值反转义)
 * - `cd DIR`      → workDir(DIR 反转义)
 * - 其它          → command(原样,有损归类不丢内容)
 */
export function lineToStep(line: string): StepBlock {
  const trimmed = line.trim()
  const copy = parseCopyLine(trimmed)
  if (copy) {
    return {
      id: nextStepId(),
      kind: 'copy',
      copySrc: copy.src,
      copyDest: copy.dest,
      copyExcludes: copy.excludes.join(' '),
      // contents 是缺省值,不写进对象(老配置读出的步骤形状保持不变)。
      ...(copy.mode === 'entry' ? { copyMode: 'entry' as const } : {}),
    }
  }
  const cond = COND_LINE.exec(trimmed)
  if (cond) {
    return { id: nextStepId(), kind: 'condition', condition: cond[1].trim() }
  }
  const env = ENV_LINE.exec(trimmed)
  if (env) {
    return { id: nextStepId(), kind: 'env', envKey: env[1], envValue: shellUnquote(env[2]) }
  }
  const cd = CD_LINE.exec(trimmed)
  if (cd) {
    return { id: nextStepId(), kind: 'workDir', dir: shellUnquote(cd[1]) }
  }
  return { id: nextStepId(), kind: 'command', command: line }
}

/**
 * 从 config 反解析出步骤列表:只吃 `commands`,逐行分类。
 * 产物目录(`artifactPath`)不是步骤,由表单的固定字段直接编辑,这里不碰。
 * 空 → 空列表(由调用方决定是否兜底)。
 */
export function parseSteps(config: Record<string, string>): StepBlock[] {
  const steps: StepBlock[] = []
  const commands = config.commands ?? ''
  for (const line of commands.replace(/\r/g, '').split('\n')) {
    if (line.trim() === '') continue
    steps.push(lineToStep(line))
  }
  return steps
}

/**
 * 判断该类型的 config 能否被步骤构建器无歧义地处理(模板节点用 commandTemplate/params,
 * 步骤构建器不覆盖那套渲染语义 → 让它们走原始视图,避免误编译丢数据)。
 */
export function configUsesTemplate(config: Record<string, string>): boolean {
  return Boolean((config.commandTemplate ?? '').trim() || (config.params ?? '').trim())
}

/** 步骤类型的展示元信息(标签 + accent token 名),供 UI 复用。 */
export const STEP_KIND_META: Record<StepKind, { label: string; accent: string }> = {
  command: { get label() { return t('pipelineJob.stepCommandLabel') }, accent: 'primary' },
  env: { get label() { return t('pipelineJob.stepEnvLabel') }, accent: 'cyan' },
  workDir: { get label() { return t('pipelineJob.stepWorkDirLabel') }, accent: 'amber' },
  copy: { get label() { return t('pipelineJob.stepCopyLabel') }, accent: 'green' },
  condition: { get label() { return t('pipelineJob.stepConditionLabel') }, accent: 'red' },
}
