import { describe, expect, it } from 'vitest'
import { mount } from '@vue/test-utils'

import type { Project } from '../../api/projects'
import ProjectCard from './ProjectCard.vue'

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
    lastRunStatus: null,
    targetServers: [],
    updatedAt: new Date().toISOString(),
    ...over,
  } as Project
}

function mountCard(props: Partial<{ project: Project; groupLabel: string; canAssignGroup: boolean }> = {}) {
  return mount(ProjectCard, {
    props: { project: project(), groupLabel: '支付平台', canAssignGroup: false, ...props },
  })
}

/** 明细行依次是:上次运行 / 目标服务器 / 仓库凭据 / 分组。 */
function metaRow(wrapper: ReturnType<typeof mountCard>, index: number) {
  return wrapper.findAll('.card-meta-row')[index]
}

describe('ProjectCard', () => {
  it('仓库与分支照实展示,动作按钮各自报出是哪一张卡', async () => {
    const p = project({ targetServers: ['35号机', '36号机'] })
    const wrapper = mountCard({ project: p })

    expect(wrapper.get('.project-name').text()).toBe('go图形报告')
    expect(wrapper.get('.repo-url').text()).toBe('gitlab.example.com/team/report.git')
    expect(wrapper.get('.branch-name').text()).toBe('main')
    expect(metaRow(wrapper, 1).text()).toContain('35号机, 36号机')

    await wrapper.get('button[aria-label="重命名项目 go图形报告"]').trigger('click')
    expect(wrapper.emitted('rename')?.[0]).toEqual([p])

    await wrapper.get('button[aria-label="设置项目 go图形报告 的仓库绑定"]').trigger('click')
    expect(wrapper.emitted('repo')?.[0]).toEqual([p])

    await wrapper.get('button[aria-label="配置项目 go图形报告 的流水线"]').trigger('click')
    expect(wrapper.emitted('pipeline')?.[0]).toEqual([p])

    await wrapper.get('button[aria-label="浏览项目 go图形报告 的代码"]').trigger('click')
    expect(wrapper.emitted('code')?.[0]).toEqual([p])

    await wrapper.get('button[aria-label="删除项目 go图形报告"]').trigger('click')
    expect(wrapper.emitted('remove')?.[0]).toEqual([p])
  })

  it('纯发布项目没有仓库:说清用途,也不给一个点了没内容的代码浏览按钮', () => {
    const wrapper = mountCard({ project: project({ repoUrl: '', defaultBranch: '' }) })

    expect(wrapper.get('.repo-url--unbound').text()).toContain('仅用于发布')
    expect(wrapper.find('.branch-row').exists()).toBe(false)
    expect(wrapper.find('button[aria-label="浏览项目 go图形报告 的代码"]').exists()).toBe(false)
  })

  it('分组行:能改归属才是按钮,否则只是带原因的一行字', async () => {
    const p = project()
    const locked = mountCard({ project: p, canAssignGroup: false })
    expect(locked.find('button.group-link').exists()).toBe(false)
    expect(metaRow(locked, 3).get('.meta-value').text()).toBe('支付平台')
    expect(metaRow(locked, 3).get('.meta-value').attributes('title')).toContain('无法修改归属')

    const editable = mountCard({ project: p, canAssignGroup: true })
    await editable.get('button.group-link').trigger('click')
    expect(editable.emitted('assign')?.[0]).toEqual([p])
  })

  it('没有运行记录时直说尚无运行,有则胶囊与明细行同一个说法', () => {
    const idle = mountCard()
    expect(metaRow(idle, 0).text()).toContain('尚无运行')
    expect(idle.find('.status-pill').exists()).toBe(false)

    const ok = mountCard({ project: project({ lastRunStatus: '成功' }) })
    expect(ok.get('.status-pill').text()).toContain('成功')
    expect(metaRow(ok, 0).text()).toContain('上次运行')

    const running = mountCard({ project: project({ lastRunStatus: '进行中' }) })
    expect(running.find('.status-dot--pulse').exists()).toBe(true)
  })
})
