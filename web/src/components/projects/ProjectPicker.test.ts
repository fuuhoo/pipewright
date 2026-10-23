import { describe, expect, it } from 'vitest'
import { mount } from '@vue/test-utils'

import ProjectPicker from './ProjectPicker.vue'
import { GROUP_ALL, GROUP_NONE } from '../../lib/groupFilter'
import type { ProjectOptionSource } from '../../lib/runProjectFilter'

const PROJECTS: ProjectOptionSource[] = [
  { id: 'p-1', name: '官网前端', groupId: '', repoUrl: 'https://example.com/web.git', defaultBranch: 'master' },
  { id: 'p-2', name: '中台服务', groupId: 'g-a', repoUrl: 'git@example.com:mid.git', defaultBranch: 'release/2026' },
  { id: 'p-3', name: 'Report API', groupId: 'g-b', repoUrl: 'https://example.com/report', defaultBranch: 'main' },
]

const GROUP_NAMES: Record<string, string> = { '': '未归组', 'g-a': '业务组', 'g-b': '数据组' }

const LABELS = {
  placeholder: '搜索项目',
  ariaLabel: '按项目筛选运行记录',
  allLabel: '全部项目',
  loadingLabel: '加载项目中…',
  emptyLabel: '没有匹配的项目',
}

function mountPicker(props: Record<string, unknown> = {}) {
  return mount(ProjectPicker, {
    props: {
      inputId: 'runs-project',
      modelValue: '',
      projects: PROJECTS,
      groupFilter: GROUP_ALL,
      groupNames: GROUP_NAMES,
      ...LABELS,
      ...props,
    },
  })
}

function optionTexts(wrapper: ReturnType<typeof mountPicker>): string[] {
  return wrapper.findAll('[role="option"]').map((o) => o.text())
}

describe('ProjectPicker', () => {
  it('聚焦即出候选,首项是「全部项目」,点选后回 id 并收起', async () => {
    const wrapper = mountPicker()
    await wrapper.get('[role="combobox"]').trigger('focus')

    expect(wrapper.find('[role="listbox"]').exists()).toBe(true)
    expect(optionTexts(wrapper)).toEqual([
      '全部项目',
      '官网前端未归组',
      '中台服务业务组',
      'Report API数据组',
    ])

    await wrapper.findAll('[role="option"]')[1].trigger('click')
    expect(wrapper.emitted('update:modelValue')?.[0]).toEqual(['p-1'])
    expect(wrapper.find('[role="listbox"]').exists()).toBe(false)
  })

  it('打关键词按大小写不敏感子串收窄,仓库地址也命中', async () => {
    const wrapper = mountPicker()
    const input = wrapper.get('[role="combobox"]')
    await input.trigger('focus')

    await input.setValue('report')
    expect(optionTexts(wrapper)).toEqual(['Report API数据组'])

    await input.setValue('MID.GIT')
    expect(optionTexts(wrapper)).toEqual(['中台服务业务组'])
  })

  it('无命中时说空态,而不是静默只剩「全部项目」', async () => {
    const wrapper = mountPicker()
    await wrapper.get('[role="combobox"]').trigger('focus')
    await wrapper.get('[role="combobox"]').setValue('zzz')

    expect(wrapper.find('[role="option"]').exists()).toBe(false)
    expect(wrapper.get('[role="listbox"]').text()).toBe('没有匹配的项目')
  })

  it('分组只收窄候选;已选但不属于该组的项目补回首位,控件不会显示着一个列表里找不到的值', async () => {
    const wrapper = mountPicker({ groupFilter: 'g-a', modelValue: 'p-3' })
    await wrapper.get('[role="combobox"]').trigger('focus')

    // 「全部项目」是不受分组影响的清空项;p-3(数据组)被补到业务组的 p-2 前面。
    expect(optionTexts(wrapper)).toEqual(['全部项目', 'Report API数据组', '中台服务业务组'])
  })

  it('未归组那一档用哨兵,不与空串混;组名标签跟着 groupId 走', async () => {
    const wrapper = mountPicker({ groupFilter: GROUP_NONE })
    await wrapper.get('[role="combobox"]').trigger('focus')

    expect(optionTexts(wrapper)).toEqual(['全部项目', '官网前端未归组'])
  })

  it('关着的时候显示选中项目的名字', () => {
    const wrapper = mountPicker({ modelValue: 'p-2' })
    expect((wrapper.get('[role="combobox"]').element as HTMLInputElement).value).toBe('中台服务')
  })

  it('地址栏带来的野 id 显示前 8 位,不冒充「全部项目」', () => {
    const wrapper = mountPicker({ modelValue: '0123456789abcdef' })
    expect((wrapper.get('[role="combobox"]').element as HTMLInputElement).value).toBe('01234567')
  })

  it('方向键加回车能选中,首项「全部项目」也在导航里', async () => {
    const wrapper = mountPicker()
    const input = wrapper.get('[role="combobox"]')

    await input.trigger('keydown', { key: 'ArrowDown' })
    await input.trigger('keydown', { key: 'Enter' })
    expect(wrapper.emitted('update:modelValue')?.[0]).toEqual([''])

    await input.trigger('keydown', { key: 'ArrowDown' })
    await input.trigger('keydown', { key: 'ArrowDown' })
    await input.trigger('keydown', { key: 'Enter' })
    expect(wrapper.emitted('update:modelValue')?.[1]).toEqual(['p-1'])
  })

  it('Escape 收起面板且不改动已选值', async () => {
    const wrapper = mountPicker({ modelValue: 'p-2' })
    const input = wrapper.get('[role="combobox"]')
    await input.trigger('focus')
    await input.trigger('keydown', { key: 'Escape' })

    expect(wrapper.find('[role="listbox"]').exists()).toBe(false)
    expect(wrapper.emitted('update:modelValue')).toBeUndefined()
    expect((input.element as HTMLInputElement).value).toBe('中台服务')
  })

  it('Escape 后焦点还留在输入框上,再点它要能重新打开', async () => {
    const wrapper = mountPicker()
    const input = wrapper.get('[role="combobox"]')
    await input.trigger('focus')
    await input.trigger('keydown', { key: 'Escape' })
    expect(wrapper.find('[role="listbox"]').exists()).toBe(false)

    // 这一步浏览器里只发 click、不会再发 focus,所以打开得靠 click 自己。
    await input.trigger('click')
    expect(wrapper.find('[role="listbox"]').exists()).toBe(true)
    expect(optionTexts(wrapper)[0]).toBe('全部项目')
  })

  it('加载中文案占位,空清单也不炸', async () => {
    const wrapper = mountPicker({ projects: [], loading: true })
    await wrapper.get('[role="combobox"]').trigger('focus')
    expect(wrapper.get('[role="listbox"]').text()).toBe('加载项目中…')
  })
})
