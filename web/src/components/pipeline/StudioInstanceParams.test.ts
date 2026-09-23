import { describe, it, expect } from 'vitest'
import { mount } from '@vue/test-utils'
import StudioInstanceParams from './StudioInstanceParams.vue'
import type { PromotedParam } from './studioCompile'

const params: PromotedParam[] = [
  { key: 'image', label: '构建镜像', type: 'text', default: 'node:20-alpine' },
  { key: 'dir', label: '目录', type: 'text', default: 'frontend' },
]

const imageOptions = [
  { value: 'node:20-alpine', label: 'Node.js 20 · node:20-alpine' },
  { value: 'node:18-alpine', label: 'Node.js 18 · node:18-alpine' },
]

describe('StudioInstanceParams 镜像参数 → 预置下拉', () => {
  it('image 参数在传入预置选项时渲染为下拉,其他参数不受影响', () => {
    const wrapper = mount(StudioInstanceParams, {
      props: { params, modelValue: {}, imageOptions },
    })
    const imageSelect = wrapper.find('select#si-image')
    expect(imageSelect.exists()).toBe(true)
    expect(imageSelect.text()).toContain('node:20-alpine')
    expect(wrapper.find('input#si-dir').exists()).toBe(true)
  })

  it('切换预置镜像 → emit 更新后的值表', async () => {
    const wrapper = mount(StudioInstanceParams, {
      props: { params, modelValue: {}, imageOptions },
    })
    await wrapper.find('select#si-image').setValue('node:18-alpine')
    const last = wrapper.emitted('update:modelValue')!.at(-1)![0] as Record<string, string>
    expect(last.image).toBe('node:18-alpine')
    expect(last.dir).toBe('frontend')
  })

  it('当前值不在预置内 → 保留为带告警的选项,不静默丢值', async () => {
    const wrapper = mount(StudioInstanceParams, {
      props: { params, modelValue: { image: 'evil.io/x:1' }, imageOptions },
    })
    const select = wrapper.find('select#si-image')
    expect((select.element as HTMLSelectElement).value).toBe('evil.io/x:1')
    expect(select.text()).toContain('evil.io/x:1 ⚠')
  })

  it('未传预置选项(接口失败兜底)→ image 仍为文本框', () => {
    const wrapper = mount(StudioInstanceParams, { props: { params, modelValue: {} } })
    expect(wrapper.find('select#si-image').exists()).toBe(false)
    expect(wrapper.find('input#si-image').exists()).toBe(true)
  })
})
