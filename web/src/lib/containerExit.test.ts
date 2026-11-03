import { describe, it, expect } from 'vitest'
import { exitMeaning, lastExitView } from './containerExit'

// 「上次为什么死的」全靠 inspect 里那几个字段,而 docker 会在容器重新拉起时把死因现场抹掉。
// 这些用例锁的是本地 Docker 29.4 实测出来的行为,不是设想:
//   · exited / restarting(退避等下次拉起)→ ExitCode / OOMKilled / Error / FinishedAt 都在
//   · running → 前三项被清零,只有 FinishedAt 与 RestartCount 留着
//   · created → 时间全是 0001-01-01(后端已折成空串)
type InspectState = Parameters<typeof lastExitView>[0]

function state(over: Partial<InspectState> = {}): InspectState {
  return { state: 'running', exitCode: 0, finishedAt: '', restartCount: 0, ...over }
}

describe('lastExitView 这一栏该不该出现', () => {
  it('exited / dead / restarting 就是死因现场', () => {
    for (const s of ['exited', 'dead', 'restarting']) {
      expect(lastExitView(state({ state: s, exitCode: 137, finishedAt: '2026-11-02T13:07:30Z' }))).toBe('evidence')
    }
  })

  it('重新拉起后只剩时间和次数,按「现场已清」摆', () => {
    expect(
      lastExitView(state({ state: 'running', exitCode: 0, finishedAt: '2026-11-02T13:07:36Z', restartCount: 8 })),
    ).toBe('cleared')
    // 只看过重启次数、时间已被抹掉,也算死过
    expect(lastExitView(state({ state: 'running', restartCount: 1 }))).toBe('cleared')
    expect(lastExitView(state({ state: 'paused', finishedAt: '2026-11-02T13:07:36Z' }))).toBe('cleared')
  })

  it('从没死过就别占一栏', () => {
    expect(lastExitView(state())).toBe('none')
    expect(lastExitView(state({ state: 'created' }))).toBe('none')
    expect(lastExitView(state({ state: undefined }))).toBe('none')
  })
})

describe('exitMeaning 退出码翻译', () => {
  it('137 看 OOMKilled 分内存超限还是被强杀', () => {
    expect(exitMeaning(137, true)).toEqual({ key: 'oom' })
    expect(exitMeaning(137, false)).toEqual({ key: 'sigkill' })
  })

  it('143 = SIGTERM,128+n = 信号 n', () => {
    expect(exitMeaning(143, false)).toEqual({ key: 'sigterm', code: 143 })
    expect(exitMeaning(139, false)).toEqual({ key: 'signal', signal: 11 })
  })

  it('1..127 归应用自己退出,0 归正常', () => {
    expect(exitMeaning(1, false)).toEqual({ key: 'app', code: 1 })
    expect(exitMeaning(7, false)).toEqual({ key: 'app', code: 7 })
    expect(exitMeaning(0, false)).toEqual({ key: 'normal', code: 0 })
  })

  it('超出信号范围的怪数字不乱猜信号', () => {
    expect(exitMeaning(322, false)).toEqual({ key: 'app', code: 322 })
  })
})
