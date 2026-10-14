/**
 * 配置资源「容器内目标路径」的可点选常用值。
 *
 * 背景:这一格原来只有一句灰色 placeholder(`如 /root/.m2/settings.xml`),知道该填什么的人照样得
 * 手敲一遍;而填错的代价是静默的 —— 路径不在工具的默认读取位置,文件照样挂进容器却没人去读。
 *
 * 两条与后端对齐的硬约束:
 *   - 必须是绝对路径且不含 `..`(internal/pipeline/buildenv_runtime.go 的 safeMountPath),否则保存期就报「不可注入」;
 *   - 构建容器以 root 运行,工具默认读取位置都在 /root 下(缓存挂载表 internal/projectcache/layout.go
 *     用的就是 /root/.m2/repository、/root/.npm 这一套),所以预置值全走 /root。
 *
 * 芯片正文不进 i18n:路径翻译了就不是能用的值。
 */

/** 类型关键词 → 常用路径。顺序即匹配顺序(先 npmrc 再 npm,避免被宽词吃掉)。 */
const typeKeywords: Array<[string, string[]]> = [
  ['maven', ['/root/.m2/settings.xml', '/root/.m2/settings-security.xml']],
  ['npmrc', ['/root/.npmrc']],
  ['yarn', ['/root/.yarnrc']],
  ['pnpm', ['/root/.npmrc']],
  ['npm', ['/root/.npmrc']],
  ['pip', ['/root/.pip/pip.conf', '/etc/pip.conf']],
  ['pypi', ['/root/.pip/pip.conf']],
  ['twine', ['/root/.pypirc']],
  ['goproxy', ['/root/.netrc']],
  ['gradle', ['/root/.gradle/init.gradle', '/root/.gradle/gradle.properties']],
  ['docker', ['/root/.docker/config.json']],
  ['cargo', ['/root/.cargo/config.toml']],
  ['netrc', ['/root/.netrc']],
  ['go', ['/root/.netrc', '/root/.config/go/env']],
  ['env', ['/root/.env']],
]

/** 类型没命中时按语言兜底(内置种子的四条就是这四个语言的权威值)。 */
const languageDefaults: Record<string, string[]> = {
  java: ['/root/.m2/settings.xml', '/root/.gradle/init.gradle'],
  node: ['/root/.npmrc'],
  python: ['/root/.pip/pip.conf'],
  go: ['/root/.netrc', '/root/.config/go/env'],
}

/** 芯片行最多几颗:再多就把弹窗撑长,而这几条已覆盖绝大多数场景。 */
export const maxPresetChips = 4

/** 语言和类型都还没选(刚打开新建弹窗)时给的四条 —— 就是内置种子的目标路径,不猜具体工具。 */
const blankFormDefaults = ['/root/.m2/settings.xml', '/root/.npmrc', '/root/.pip/pip.conf', '/root/.netrc']

function normalize(raw: string): string {
  return (raw ?? '').toLowerCase().replace(/[^a-z0-9]/g, '')
}

/**
 * 该「语言 + 配置类型」组合下值得点的目标路径。
 * 类型命中优先(它更具体);类型是自由文本、没命中时退回语言;两个都空给内置四条;
 * 选了但都不认识(自定义类型 / 「其他」)则不给 —— 硬猜一条错路径比不填更坏。
 */
export function targetPathPresets(language: string, configType: string): string[] {
  const type = normalize(configType)
  const lang = normalize(language)
  const out: string[] = []
  if (type) {
    for (const [kw, paths] of typeKeywords) {
      if (type.includes(kw)) out.push(...paths)
    }
  }
  if (!out.length && lang) out.push(...(languageDefaults[lang] ?? []))
  if (!out.length && !type && !lang) out.push(...blankFormDefaults)
  return [...new Set(out)].slice(0, maxPresetChips)
}
