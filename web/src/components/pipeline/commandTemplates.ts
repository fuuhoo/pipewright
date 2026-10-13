/**
 * commandTemplates — 「执行命令」框上方的一键语言模板(纯数据 + 纯函数,便于脱页面断言)。
 *
 * 为什么不做成任务挑选画廊里的模板:画廊那层(`JOB_TYPE_SPECS[*].templates`)是**整节点预填**,
 * 选中即覆盖已有 config,且只有「构建」类型有;这里要解决的是命令本身怎么写(npm 还是 pnpm、
 * mvn 还是 gradlew),script / 历史 build_frontend / 构建任务的产物档都用得上,所以只做
 * 「往命令文本里插一段」,其余字段(环境、产物路径、缓存)一律不动。
 *
 * 文案故意不进 i18n:芯片标签是语言与包管理器的技术名,命令正文是 shell —— 两种口径下
 * 各语言本来就写作同一个串(与字段的 placeholder 同一处理)。
 */

export interface CommandTemplate {
  id: string
  /** 归类语言:与「构建环境」目录里的 language 取值一致(node/java/…/vfox)。 */
  language: string
  /** 芯片文案(技术名,不翻译)。 */
  label: string
  /** 插入的命令块:每行一条,按顺序在同一个 `set -e` 脚本里执行。 */
  commands: string
}

export const COMMAND_TEMPLATES: CommandTemplate[] = [
  {
    id: 'node-npm',
    language: 'node',
    label: 'Node.js · npm',
    commands: 'npm ci --no-audit --no-fund\nnpm run build',
  },
  {
    id: 'node-pnpm',
    language: 'node',
    label: 'Node.js · pnpm',
    commands: 'corepack enable\npnpm install --frozen-lockfile\npnpm run build',
  },
  {
    id: 'node-yarn',
    language: 'node',
    label: 'Node.js · yarn',
    commands: 'corepack enable\nyarn install --frozen-lockfile\nyarn build',
  },
  {
    id: 'java-maven',
    language: 'java',
    label: 'Java · Maven',
    commands: 'mvn -B -DskipTests clean package',
  },
  {
    id: 'java-gradle',
    language: 'java',
    label: 'Java · Gradle',
    commands: './gradlew --no-daemon -x test clean build',
  },
  {
    id: 'python-pip',
    language: 'python',
    label: 'Python · pip',
    commands: 'pip install -r requirements.txt',
  },
  {
    id: 'python-poetry',
    language: 'python',
    label: 'Python · Poetry',
    commands: 'pip install poetry\npoetry install --no-root',
  },
  {
    id: 'go',
    language: 'go',
    label: 'Go',
    commands: 'go mod download\nCGO_ENABLED=0 go build -trimpath -o dist/app .',
  },
  {
    id: 'ruby',
    language: 'ruby',
    label: 'Ruby · Bundler',
    commands: 'bundle config set --local path vendor/bundle\nbundle install',
  },
  {
    id: 'php',
    language: 'php',
    label: 'PHP · Composer',
    commands: 'composer install --no-interaction --prefer-dist --optimize-autoloader',
  },
  {
    id: 'rust',
    language: 'rust',
    label: 'Rust · Cargo',
    commands: 'cargo build --release',
  },
  // vfox-full ≥1.0.13 里 `vfox use` 由镜像自带的包装层接管,直接在平台的 sh 脚本里生效:
  // 不需要 eval activate,也不需要把整段包进 bash heredoc。1.0.12 仍要自己 activate(会显式报错,不会静默)。
  {
    id: 'vfox',
    language: 'vfox',
    label: 'vfox → nodejs@22',
    commands: 'vfox use nodejs@22\nnode -v\nnpm ci --no-audit --no-fund\nnpm run build',
  },
  {
    id: 'vfox-java',
    language: 'vfox',
    label: 'vfox → java@17',
    commands: 'vfox use java@17\njava -version\nmvn -B -DskipTests clean package',
  },
]

/**
 * 芯片展示顺序:命中当前环境语言的模板排前面(选了 Node.js 20 就先看见 npm/pnpm/yarn),
 * 其余语言照旧顺序跟在后面 —— 环境没选、或选的是 alpine 这类没有模板的语言时给全量。
 */
export function templatesForLanguage(language: string): CommandTemplate[] {
  const lang = (language ?? '').trim().toLowerCase()
  const hit = COMMAND_TEMPLATES.filter((x) => x.language === lang)
  if (hit.length === 0) return [...COMMAND_TEMPLATES]
  return [...hit, ...COMMAND_TEMPLATES.filter((x) => x.language !== lang)]
}

/**
 * 把模板并进现有命令:空框直接填入,非空**追加到末尾**(绝不覆盖 —— 框里可能是用户调了
 * 半天的那条 `cd frontend`)。多余尾换行压掉,两块命令之间不留空行:后端按行拼成单个
 * `set -e` 脚本,顺序执行,追加在后面就是「装完依赖再跑我的活」。
 */
export function mergeCommands(existing: string, template: string): string {
  const cur = (existing ?? '').replace(/\r/g, '')
  if (cur.trim() === '') return template
  return `${cur.replace(/\n+$/, '')}\n${template}`
}
