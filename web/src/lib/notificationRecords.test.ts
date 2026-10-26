import { describe, expect, it } from 'vitest'

import { DEFAULT_FILTERS, eventLabelKey, hasActiveFilters, resolveFilters, statusLabelKey, toListParams, totalPages, withFilters } from './notificationRecords'

describe('resolveFilters', () => {
  it('空地址栏 = 默认状态(全部事件 / 全部结论 / 全部项目 / 第 1 页)', () => {
    expect(resolveFilters({})).toEqual(DEFAULT_FILTERS)
  })

  it('认下三个筛选与页码', () => {
    expect(
      resolveFilters({ event: 'build_failed', status: 'skipped', projectId: 'p-1', page: '3' }),
    ).toEqual({ event: 'build_failed', status: 'skipped', projectId: 'p-1', page: 3 })
  })

  it('脏页码一律回第 1 页:0、负数、非数字、数组、数字都算没给', () => {
    for (const bad of ['0', '-2', 'abc', '1.5x', ['2'], 4, undefined, '']) {
      expect(resolveFilters({ page: bad }).page).toBe(1)
    }
  })

  it('页码前导零与空格仍按数字理解,不退回第 1 页', () => {
    expect(resolveFilters({ page: '007' }).page).toBe(7)
  })

  it('同名重复的筛选当作没筛(与运行页项目筛选同一口径)', () => {
    expect(resolveFilters({ event: ['a', 'b'], projectId: ['p1', 'p2'] })).toEqual(DEFAULT_FILTERS)
  })
})

describe('withFilters', () => {
  it('默认值整个删掉,不留空串参数', () => {
    expect(
      withFilters({ event: 'build_failed' }, { event: '', status: '', projectId: '', page: 1 }),
    ).toEqual({})
  })

  it('退回第 1 页时把 page 也从 URL 抹掉', () => {
    expect(
      withFilters({ page: '3' }, { event: '', status: '', projectId: '', page: 1 }),
    ).toEqual({})
  })

  it('非默认值写回,且不动无关参数', () => {
    expect(
      withFilters({ tab: 'channels' }, { event: 'cert_expiring', status: 'failed', projectId: 'p-9', page: 2 }),
    ).toEqual({ tab: 'channels', event: 'cert_expiring', status: 'failed', projectId: 'p-9', page: '2' })
  })
})

describe('toListParams', () => {
  it('空筛选发 undefined,而不是空串(后端会把空串当没筛,但请求里多三个噪音参数)', () => {
    expect(toListParams(DEFAULT_FILTERS, 20)).toEqual({
      event: undefined,
      status: undefined,
      projectId: undefined,
      page: 1,
      pageSize: 20,
    })
  })

  it('runId 不由这页产生(本页没有运行筛选),事件与结论原样带上', () => {
    expect(toListParams({ event: 'build_failed', status: 'ok', projectId: '', page: 4 }, 20)).toEqual({
      event: 'build_failed',
      status: 'ok',
      projectId: undefined,
      page: 4,
      pageSize: 20,
    })
  })
})

describe('hasActiveFilters', () => {
  it('只有页码变了不算筛选(空列表仍该说「还没有记录」)', () => {
    expect(hasActiveFilters({ ...DEFAULT_FILTERS, page: 3 })).toBe(false)
  })

  it('任一维度筛了就算筛选中', () => {
    expect(hasActiveFilters({ ...DEFAULT_FILTERS, status: 'failed' })).toBe(true)
  })
})

describe('eventLabelKey', () => {
  // 这 11 个是后端 Events() 当前的全集;键名规则漂了,整列事件名会退成 build_failed 这种原始 ID。
  it('逐一对上设置页已翻过 8 语种的那批键', () => {
    expect(eventLabelKey('build_succeeded')).toBe('settingsNotifications.evBuildSucceeded')
    expect(eventLabelKey('build_failed')).toBe('settingsNotifications.evBuildFailed')
    expect(eventLabelKey('deploy_succeeded')).toBe('settingsNotifications.evDeploySucceeded')
    expect(eventLabelKey('deploy_failed')).toBe('settingsNotifications.evDeployFailed')
    expect(eventLabelKey('rollback')).toBe('settingsNotifications.evRollback')
    expect(eventLabelKey('health_check_failed')).toBe('settingsNotifications.evHealthCheckFailed')
    expect(eventLabelKey('approval_required')).toBe('settingsNotifications.evApprovalRequired')
    expect(eventLabelKey('anomaly_detected')).toBe('settingsNotifications.evAnomalyDetected')
    expect(eventLabelKey('cert_expiring')).toBe('settingsNotifications.evCertExpiring')
    expect(eventLabelKey('cert_renewed')).toBe('settingsNotifications.evCertRenewed')
    expect(eventLabelKey('cert_renew_failed')).toBe('settingsNotifications.evCertRenewFailed')
  })

  it('大小写混乱的 ID 也归一,空 ID 不给键(让调用方退成原值)', () => {
    expect(eventLabelKey('BUILD_FAILED')).toBe('settingsNotifications.evBuildFailed')
    expect(eventLabelKey('_')).toBe('')
    expect(eventLabelKey('')).toBe('')
  })
})

describe('statusLabelKey', () => {
  it('三档结论各有键', () => {
    expect(statusLabelKey('ok')).toBe('notificationRecords.statusOk')
    expect(statusLabelKey('failed')).toBe('notificationRecords.statusFailed')
    expect(statusLabelKey('skipped')).toBe('notificationRecords.statusSkipped')
  })

  it('后端将来加档不给假键', () => {
    expect(statusLabelKey('queued')).toBe('')
  })
})

describe('totalPages', () => {
  it('0 条也是 1 页(页脚不该出现「第 1 / 0 页」)', () => {
    expect(totalPages(0, 20)).toBe(1)
  })

  it('向上取整', () => {
    expect(totalPages(21, 20)).toBe(2)
    expect(totalPages(40, 20)).toBe(2)
  })

  it('每页条数坏了不算页数', () => {
    expect(totalPages(50, 0)).toBe(1)
  })
})
