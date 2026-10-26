/**
 * buildEnvRsync — 构建环境镜像「有没有装 rsync」的已实测清单。
 *
 * 「复制文件」步骤编译成 `rsync …; else cp -a …`,排除项只在 rsync 分支生效,而用哪个分支
 * 要等命令真在容器里跑起来才知道 —— 编辑态问不了实时(问一次就得 `docker run` 进镜像,
 * 先把镜像拉下来不说,还只有本机 daemon 准,远程 runner 上是另一台)。
 *
 * 所以这里只放**能负责地断言**的事实:内置目录那 21 条镜像逐条实测过(见下表注释),
 * 一条都没装 rsync。目录外的镜像串(自建 / 私有仓库)一律 'unknown',UI 就说「运行时决定」,
 * 绝不替用户猜一个镜像里有什么。
 */

export type RsyncSupport = 'yes' | 'no' | 'unknown'

/**
 * 实测口径(2026-10-26,本机 OrbStack / arm64;temurin 17 的 alpine 档只出 amd64,用 --platform 跑):
 *   docker run --rm --entrypoint sh <镜像> -c 'command -v rsync'   → 21 条全部空输出
 * 覆盖的是 internal/buildenv/seed.go 里 builtinBuildEnvs 的全部条目。
 * 新增内置项时要一起实测;镜像串**精确匹配**,所以管理员把某条换成装了 rsync 的自建镜像时
 * 会落到 'unknown'(说「运行时决定」),不会留下一句过期的「这个镜像里没有」。
 */
const MEASURED: Record<string, RsyncSupport> = {
  'node:26-alpine': 'no',
  'node:24-alpine': 'no',
  'node:22-alpine': 'no',
  'node:20-alpine': 'no',
  'node:18-alpine': 'no',
  'node:16-alpine': 'no',
  'python:3.12-alpine': 'no',
  'python:3.11-alpine': 'no',
  'eclipse-temurin:21-jdk-alpine': 'no',
  'eclipse-temurin:17-jdk-alpine': 'no',
  'eclipse-temurin:11-jdk': 'no',
  'eclipse-temurin:8-jdk': 'no',
  'maven:3.9-eclipse-temurin-21': 'no',
  'golang:1.22-alpine': 'no',
  'ruby:3.3-alpine': 'no',
  'php:8.3-cli-alpine': 'no',
  'rust:1.79-alpine': 'no',
  'alpine:latest': 'no',
  'registry.cn-qingdao.aliyuncs.com/fubin/vfox-full:1.0.14': 'no',
  'registry.cn-qingdao.aliyuncs.com/fubin/vfox-full:1.0.13': 'no',
  'registry.cn-qingdao.aliyuncs.com/fubin/vfox-full:1.0.12': 'no',
}

/** 该镜像里 rsync 在不在;'unknown' = 没实测过(含没选构建环境)。 */
export function rsyncSupportFor(image: string | undefined): RsyncSupport {
  const key = (image ?? '').trim()
  if (!key) return 'unknown'
  return MEASURED[key] ?? 'unknown'
}
