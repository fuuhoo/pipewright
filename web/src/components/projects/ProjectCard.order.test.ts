import { describe, expect, it } from 'vitest'
import { mount } from '@vue/test-utils'

import type { Project } from '../../api/projects'
import ProjectCard from './ProjectCard.vue'

// 项目卡片的拖动排序:握把拖动 + 方向键挪格。jsdom 没有 DataTransfer,用例自己造一个,
// 顺便把「认哪个 MIME」这条契约钉住(和 ServerCard.hostOrder.test.ts 同一手法)。
const MIME = 'application/x-pipewright-project'

function project(over: Partial<Project> = {}): Project {
  return {
    id: 'p-1',
    name: 'go图形报告',
    repoUrl: 'https://gitlab.example.com/team/report.git',
    defaultBranch: 'main',
    credentialId: 'c-1',
    credentialName: '演示凭据名',
    pacEnabled: false,
    prStatusEnabled: false,
    groupId: 'g-pay',
    serviceId: '',
    serviceName: '',
    lastRunStatus: null,
    lastRunAt: null,
    lastRunId: null,
    targetServers: [],
    updatedAt: new Date().toISOString(),
    ...over,
  } as Project
}

function mountCard(over: Partial<Project> = {}) {
  return mount(ProjectCard, {
    props: { project: project(over), groupLabel: '支付平台', canAssignGroup: false, canViewRuns: true },
  })
}

function transfer(types: string[] = [], store: Record<string, string> = {}) {
  return {
    types,
    effectAllowed: '',
    setData: (k: string, v: string) => {
      store[k] = v
      if (!types.includes(k)) types.push(k)
    },
    getData: (k: string) => store[k] ?? '',
    setDragImage: () => undefined,
  }
}

/** 卡片高 200:上半 = before,下半 = after(jsdom 的 getBoundingClientRect 全是 0,必须自己钉)。 */
function stubHeight(wrapper: ReturnType<typeof mountCard>, height: number): void {
  const el = wrapper.find('li').element as Element
  el.getBoundingClientRect = () =>
    ({ top: 0, left: 0, bottom: height, right: 0, height, width: 0, x: 0, y: 0, toJSON: () => ({}) }) as DOMRect
}

function grip(wrapper: ReturnType<typeof mountCard>) {
  return wrapper.find('.card-grip')
}

describe('ProjectCard 拖动排序', () => {
  it('握把带项目 id 落到 dataTransfer,并用自己的 MIME 标记', async () => {
    const wrapper = mountCard()
    const store: Record<string, string> = {}
    const dt = transfer([], store)
    await grip(wrapper).trigger('dragstart', { dataTransfer: dt, clientX: 4, clientY: 4 })
    expect(dt.types).toContain(MIME)
    expect(store[MIME]).toBe('p-1')
    expect(wrapper.find('li').classes()).toContain('project-card--dragging')
  })

  it('落在上半 → reorder-project(被拖那个, before);下半 → after', async () => {
    const before = mountCard()
    stubHeight(before, 200)
    await before
      .find('li')
      .trigger('drop', { dataTransfer: transfer([MIME], { [MIME]: 'other' }), clientY: 30 })
    expect(before.emitted('reorder-project')).toEqual([['other', 'before']])

    const after = mountCard()
    stubHeight(after, 200)
    await after
      .find('li')
      .trigger('drop', { dataTransfer: transfer([MIME], { [MIME]: 'other' }), clientY: 170 })
    expect(after.emitted('reorder-project')).toEqual([['other', 'after']])
  })

  it('拖回自己那张 / 不带项目 MIME 的拖动都不上报', async () => {
    const self = mountCard()
    stubHeight(self, 200)
    await self.find('li').trigger('drop', { dataTransfer: transfer([MIME], { [MIME]: 'p-1' }), clientY: 30 })
    expect(self.emitted('reorder-project')).toBeUndefined()

    const text = mountCard()
    stubHeight(text, 200)
    await text.find('li').trigger('dragover', { dataTransfer: transfer(['text/plain']), clientY: 30 })
    expect(text.find('li').classes()).not.toContain('project-card--drop-before')
    await text.find('li').trigger('drop', { dataTransfer: transfer(['text/plain']), clientY: 30 })
    expect(text.emitted('reorder-project')).toBeUndefined()
  })

  it('dragover 时按上下半显示插入线', async () => {
    const wrapper = mountCard()
    stubHeight(wrapper, 200)
    const li = wrapper.find('li')
    await li.trigger('dragover', { dataTransfer: transfer([MIME]), clientY: 30 })
    expect(li.classes()).toContain('project-card--drop-before')
    await li.trigger('dragover', { dataTransfer: transfer([MIME]), clientY: 180 })
    expect(li.classes()).toContain('project-card--drop-after')
  })

  it('握把可聚焦,上下方向键各上报一格', async () => {
    const wrapper = mountCard()
    expect(grip(wrapper).attributes('tabindex')).toBe('0')
    await grip(wrapper).trigger('keydown', { key: 'ArrowUp' })
    await grip(wrapper).trigger('keydown', { key: 'ArrowDown' })
    expect(wrapper.emitted('nudge-project')).toEqual([[-1], [1]])
  })
})
