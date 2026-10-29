import { describe, it, expect, beforeEach, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'

const listGroups = vi.fn()
const listAssignableUsers = vi.fn()
const createGroup = vi.fn()
const updateGroup = vi.fn()
const addGroupMember = vi.fn()
const removeGroupMember = vi.fn()

vi.mock('../api/groups', () => ({
  listGroups: (...a: unknown[]) => listGroups(...a),
  listAssignableUsers: (...a: unknown[]) => listAssignableUsers(...a),
  createGroup: (...a: unknown[]) => createGroup(...a),
  updateGroup: (...a: unknown[]) => updateGroup(...a),
  deleteGroup: vi.fn(),
  addGroupMember: (...a: unknown[]) => addGroupMember(...a),
  removeGroupMember: (...a: unknown[]) => removeGroupMember(...a),
}))

import type { Group } from '../api/groups'
import { useSessionStore } from '../stores/session'
import Groups from './Groups.vue'

const SOURCE = '.transfer__panel--source'
const TARGET = '.transfer__panel--target'

function member(id: string, username: string) {
  return { id, username }
}

function group(over: Partial<Group> = {}): Group {
  return {
    id: 'g1',
    name: '支付平台',
    description: '',
    visibility: 'private',
    ownerId: 'u1',
    ownerName: 'admin',
    members: [member('u2', 'alice')],
    projectCount: 0,
    serverCount: 0,
    variableGroupCount: 0,
    canManage: true,
    createdAt: '2026-10-01T00:00:00Z',
    updatedAt: '2026-10-01T00:00:00Z',
    ...over,
  }
}

type Wrapper = ReturnType<typeof mount>

async function openModal(clickText: string) {
  const wrapper = mount(Groups, { global: { stubs: { teleport: true } } })
  await flushPromises()
  const btn = wrapper.findAll('button').find((b) => b.text().includes(clickText))
  expect(btn).toBeTruthy()
  await btn!.trigger('click')
  await flushPromises()
  return wrapper
}

async function openCreate() {
  return openModal('新建分组')
}

function sourceBoxes(wrapper: Wrapper) {
  return wrapper.findAll(`${SOURCE} .transfer__item input[type="checkbox"]`)
}

function targetBoxes(wrapper: Wrapper) {
  return wrapper.findAll(`${TARGET} .transfer__item input[type="checkbox"]`)
}

function sourceNames(wrapper: Wrapper) {
  return wrapper.findAll(`${SOURCE} .transfer__name`).map((n) => n.text())
}

function targetNames(wrapper: Wrapper) {
  return wrapper.findAll(`${TARGET} .transfer__name`).map((n) => n.text())
}

async function move(wrapper: Wrapper, dir: 'right' | 'left') {
  const btns = wrapper.findAll('.transfer__btn')
  await btns[dir === 'right' ? 0 : 1].trigger('click')
}

function baseMocks() {
  listAssignableUsers
    .mockReset()
    .mockResolvedValue([member('u1', 'admin'), member('u2', 'alice'), member('u3', 'bob')])
  createGroup.mockReset().mockResolvedValue(group())
  updateGroup.mockReset().mockResolvedValue(group())
  addGroupMember.mockReset().mockResolvedValue(group())
  removeGroupMember.mockReset().mockResolvedValue(group())
}

describe('Groups — 新建分组的成员穿梭框', () => {
  beforeEach(() => {
    listGroups.mockReset().mockResolvedValue([])
    baseMocks()
    // 创建者就是 admin:新建时 ownerId 为空,靠用户名认出自己并锁进右栏
    const store = useSessionStore()
    if (store.user) store.setUser({ ...store.user, username: 'admin' })
  })

  it('左栏勾选后移到右栏,离开的人从左栏消失,再移一个不冲掉前一个', async () => {
    const wrapper = await openCreate()
    // 创建者是组长 → 锁在右栏;左栏只剩另外两人
    expect(sourceNames(wrapper)).toEqual(['alice', 'bob'])
    expect(targetNames(wrapper)).toEqual(['admin'])
    const owner = targetBoxes(wrapper)[0].element as HTMLInputElement
    expect(owner.checked).toBe(true)
    expect(owner.disabled).toBe(true)

    await sourceBoxes(wrapper)[0].setValue(true)
    await move(wrapper, 'right')
    expect(sourceNames(wrapper)).toEqual(['bob'])
    expect(targetNames(wrapper)).toEqual(['admin', 'alice'])

    await sourceBoxes(wrapper)[0].setValue(true)
    await move(wrapper, 'right')
    expect(sourceNames(wrapper)).toEqual([])
    expect(targetNames(wrapper)).toEqual(['admin', 'alice', 'bob'])

    await wrapper.get('#group-name').setValue('测试组')
    await wrapper.get('form.app-modal__form').trigger('submit')
    await flushPromises()

    expect(createGroup).toHaveBeenCalledTimes(1)
    // 组长不入名册:只提交真正加入的两个人
    expect(createGroup.mock.calls[0][0].memberIds).toEqual(['u2', 'u3'])
  })

  it('两栏都能按名字搜索,搜索只影响显示,不会误移走看不见的人', async () => {
    const wrapper = await openCreate()
    await wrapper.get(`${SOURCE} input[type="search"]`).setValue('bo')
    expect(sourceNames(wrapper)).toEqual(['bob'])

    await wrapper.get(`${TARGET} input[type="search"]`).setValue('ad')
    expect(targetNames(wrapper)).toEqual(['admin'])

    await wrapper.get(`${SOURCE} input[type="search"]`).setValue('')
    expect(sourceNames(wrapper)).toEqual(['alice', 'bob'])
  })
})

describe('Groups — 编辑分组成员', () => {
  beforeEach(() => {
    listGroups.mockReset().mockResolvedValue([group()])
    baseMocks()
  })

  it('现有成员在右栏、组长锁定;搜索后加入新成员、移出旧成员,保存时折算成增删', async () => {
    const wrapper = await openModal('管理')
    expect(targetNames(wrapper)).toEqual(['admin', 'alice'])
    expect(sourceNames(wrapper)).toEqual(['bob'])

    // 搜索 bob 并加入
    await wrapper.get(`${SOURCE} input[type="search"]`).setValue('bo')
    expect(sourceNames(wrapper)).toEqual(['bob'])
    await sourceBoxes(wrapper)[0].setValue(true)
    await move(wrapper, 'right')
    await wrapper.get(`${SOURCE} input[type="search"]`).setValue('')
    expect(targetNames(wrapper)).toEqual(['admin', 'alice', 'bob'])

    // 已选里勾 alice 移出
    await targetBoxes(wrapper)[1].setValue(true)
    await move(wrapper, 'left')
    expect(targetNames(wrapper)).toEqual(['admin', 'bob'])

    await wrapper.get('form.app-modal__form').trigger('submit')
    await flushPromises()

    expect(updateGroup).toHaveBeenCalledTimes(1)
    expect(addGroupMember).toHaveBeenCalledTimes(1)
    expect(addGroupMember).toHaveBeenCalledWith('g1', 'u3')
    expect(removeGroupMember).toHaveBeenCalledTimes(1)
    expect(removeGroupMember).toHaveBeenCalledWith('g1', 'u2')
  })
})
