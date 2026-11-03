import type { ContainerInspect } from '../api/containers'

/**
 * 「上次为什么退」这一栏的解读规则。
 *
 * 背景(本地 Docker 29.4 实测,不是推测):
 *   · 容器处于 exited / dead / restarting(docker 退避等下一次拉起)时,`.State` 里
 *     ExitCode / OOMKilled / Error / FinishedAt 完整留着死因现场。
 *   · 一旦被重新拉起进入 running,前三项被 docker 清零,只有 FinishedAt(上次退出时刻)
 *     和顶层 RestartCount(累计重启次数)还在。
 *   · 从没启动过的 created 容器:两个时间都是 0001-01-01,后端已折成空串。
 * 所以崩溃重启的容器要抓死因,得趁它处于 restarting/exited 的那段时间读一次详情。
 */

/** 这一栏该怎么摆:不出 / 现场还在 / 现场已被清零。 */
export type LastExitView = 'none' | 'evidence' | 'cleared'

/** 退出码归类,交给 i18n 出文案(后端只给数字,不猜语义)。 */
export type ExitMeaningKey = 'oom' | 'sigkill' | 'sigterm' | 'signal' | 'app' | 'normal'

export function lastExitView(data: Pick<ContainerInspect, 'state' | 'exitCode' | 'finishedAt' | 'restartCount'>): LastExitView {
  const state = data.state ?? ''
  if (state === 'exited' || state === 'dead' || state === 'restarting') return 'evidence'
  // 死过至少一次才值得说;从没退出过的运行中/暂停容器不必占一栏。
  const died = data.restartCount > 0 || (data.finishedAt ?? '') !== ''
  if (!died) return 'none'
  // running / paused 只剩时间和次数;created 之类读不出可靠含义,同样按「现场没了」摆,由 UI 说明。
  return 'cleared'
}

export function exitMeaning(exitCode: number, oomKilled: boolean): { key: ExitMeaningKey; signal?: number; code?: number } {
  if (exitCode === 0) return { key: 'normal', code: 0 }
  if (exitCode === 137) return { key: oomKilled ? 'oom' : 'sigkill' }
  if (exitCode === 143) return { key: 'sigterm', code: 143 }
  // shell 惯例:被信号杀死时退出码 = 128 + 信号号。
  if (exitCode > 128 && exitCode < 165) return { key: 'signal', signal: exitCode - 128 }
  return { key: 'app', code: exitCode }
}
