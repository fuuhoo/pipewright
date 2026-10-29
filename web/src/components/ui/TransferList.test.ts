import { describe, it, expect } from 'vitest'
import { mount } from '@vue/test-utils'
import TransferList from './TransferList.vue'

const options = [
  { id: 'admin', label: 'admin' },
  { id: 'user', label: 'user' },
  { id: 'ops', label: 'ops' },
]

function targetNames(wrapper: ReturnType<typeof mount>) {
  return wrapper.findAll('.transfer__panel--target .transfer__name').map((n) => n.text())
}

async function addFirstAvailable(wrapper: ReturnType<typeof mount>) {
  await wrapper.get('.transfer__panel--source input[type="checkbox"]').setValue(true)
  await wrapper.findAll('.transfer__btn')[0].trigger('click')
}

describe('TransferList — 已选顺序', () => {
  it('preserveOrder:保留 v-model 原顺序,新加入的追加在末尾', async () => {
    const wrapper = mount(TransferList, {
      props: { modelValue: ['ops', 'user'], options, preserveOrder: true },
    })
    expect(targetNames(wrapper)).toEqual(['ops', 'user'])

    await addFirstAvailable(wrapper)

    expect(wrapper.emitted('update:modelValue')?.at(-1)).toEqual([['ops', 'user', 'admin']])
  })

  it('disabledIds:置灰不可移入,meta 作为行尾次要信息展示', async () => {
    const wrapper = mount(TransferList, {
      props: {
        modelValue: [],
        options: [
          { id: 'a', label: 'A 组', meta: '3 名成员' },
          { id: 'b', label: 'B 组' },
        ],
        disabledIds: ['a'],
      },
    })
    expect(wrapper.get('.transfer__panel--source .transfer__meta').text()).toBe('3 名成员')
    const boxes = wrapper.findAll('.transfer__panel--source input[type="checkbox"]')
    expect((boxes[0].element as HTMLInputElement).disabled).toBe(true)
    expect((boxes[1].element as HTMLInputElement).disabled).toBe(false)
    expect(wrapper.findAll('.transfer__btn')[0].attributes('disabled')).toBeDefined()
  })

  it('readonly:不渲染勾选框与箭头,两栏仍可查看', () => {
    const wrapper = mount(TransferList, {
      props: { modelValue: ['user'], options, readonly: true },
    })
    expect(wrapper.findAll('input[type="checkbox"]')).toHaveLength(0)
    expect(wrapper.findAll('.transfer__btn')).toHaveLength(0)
    expect(wrapper.find('.transfer--readonly').exists()).toBe(true)
    expect(targetNames(wrapper)).toEqual(['user'])
  })

  it('pageSize:每页截断,翻页只看当前页,搜索回到第 1 页', async () => {
    const many = Array.from({ length: 13 }, (_, i) => ({ id: `u${i}`, label: `user-${String(i).padStart(2, '0')}` }))
    const wrapper = mount(TransferList, {
      props: { modelValue: [], options: many, pageSize: 8, readonly: true },
    })
    const names = () => wrapper.findAll('.transfer__panel--source .transfer__name').map((n) => n.text())
    expect(names()).toHaveLength(8)
    expect(wrapper.get('.transfer__page-label').text()).toBe('第 1 / 2 页')

    await wrapper.findAll('.transfer__page-btn')[1].trigger('click')
    expect(names()).toHaveLength(5)
    expect(wrapper.get('.transfer__page-label').text()).toBe('第 2 / 2 页')

    await wrapper.get('.transfer__panel--source input[type="search"]').setValue('user-0')
    expect(wrapper.get('.transfer__page-label').text()).toBe('第 1 / 2 页')
    expect(names()).toEqual(['user-00', 'user-01', 'user-02', 'user-03', 'user-04', 'user-05', 'user-06', 'user-07'])
  })

  it('createdAt:两栏都按最新的在前,缺时间的排在后面', () => {
    const wrapper = mount(TransferList, {
      props: {
        modelValue: ['old'],
        readonly: true,
        options: [
          { id: 'old', label: 'old', createdAt: '2024-01-01T00:00:00Z' },
          { id: 'new', label: 'new', createdAt: '2026-01-01T00:00:00Z' },
          { id: 'none', label: 'none' },
          { id: 'mid', label: 'mid', createdAt: '2025-01-01T00:00:00Z' },
        ],
      },
    })
    const names = (panel: string) =>
      wrapper.findAll(`.transfer__panel--${panel} .transfer__name`).map((n) => n.text())
    expect(names('source')).toEqual(['new', 'mid', 'none'])
    expect(names('target')).toEqual(['old'])
  })

  it('默认每页 8 条,超过即分页', () => {
    const many = Array.from({ length: 9 }, (_, i) => ({ id: `u${i}`, label: `u${i}` }))
    const wrapper = mount(TransferList, {
      props: { modelValue: [], options: many, readonly: true },
    })
    expect(wrapper.findAll('.transfer__panel--source .transfer__name')).toHaveLength(8)
    expect(wrapper.get('.transfer__page-label').text()).toBe('第 1 / 2 页')
  })

  it('默认按 options 顺序重排,不因加入先后跳位', async () => {
    const wrapper = mount(TransferList, {
      props: { modelValue: ['ops', 'user'], options },
    })
    expect(targetNames(wrapper)).toEqual(['user', 'ops'])

    await addFirstAvailable(wrapper)

    expect(wrapper.emitted('update:modelValue')?.at(-1)).toEqual([['admin', 'user', 'ops']])
  })
})
