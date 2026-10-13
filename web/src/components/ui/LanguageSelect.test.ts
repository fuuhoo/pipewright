/**
 * LanguageSelect 交互回归:下拉候选、自定义新增的展开与回收、历史值不被改写。
 * locale 已由 src/test/setup.ts 钉到 zh-CN,断言直接写中文词条。
 */
import { describe, it, expect } from 'vitest'
import { mount } from '@vue/test-utils'
import { nextTick } from 'vue'
import LanguageSelect from './LanguageSelect.vue'
import { CUSTOM_SENTINEL, OTHER_LANGUAGE } from '../../lib/languageOptions'

function factory(props: Record<string, unknown>) {
  return mount(LanguageSelect, { props: { modelValue: '', known: [], ...props } })
}

describe('LanguageSelect', () => {
  it('候选是「已添加语言 + 其他 + 自定义新增」,初始显示占位', () => {
    const w = factory({ known: ['node', 'java'] })
    const values = w.findAll('option').map((o) => o.attributes('value'))
    expect(values).toEqual(['', 'java', 'node', OTHER_LANGUAGE, CUSTOM_SENTINEL])
    expect(w.findAll('option')[0].text()).toBe('选择语言…')
    expect(w.findAll('option')[3].text()).toBe('其他(不绑定语言)')
    expect(w.find('input[type=text]').exists()).toBe(false)
  })

  it('选已有语言直接带出值,不出现文本框', async () => {
    const w = factory({ known: ['java'] })
    await w.find('select').setValue('java')
    expect(w.emitted('update:modelValue')?.at(-1)).toEqual(['java'])
    expect(w.find('input[type=text]').exists()).toBe(false)
  })

  it('选「自定义新增」把值清空并展开文本框,哨兵值不会外泄', async () => {
    const w = factory({ modelValue: 'java', known: ['java'] })
    await w.find('select').setValue(CUSTOM_SENTINEL)
    const events = w.emitted('update:modelValue') ?? []
    expect(events.at(-1)).toEqual([''])
    expect(events.flat()).not.toContain(CUSTOM_SENTINEL)
    expect(w.find('input[type=text]').exists()).toBe(true)
    expect(w.find('.lang-select__hint').exists()).toBe(true)
  })

  it('手输值按归一化口径外传(大小写与首尾空白不算新语言)', async () => {
    const w = factory({ known: [] })
    await w.find('select').setValue(CUSTOM_SENTINEL)
    const draft = w.find('input[type=text]')
    await draft.setValue('  DotNet ')
    expect(w.emitted('update:modelValue')?.at(-1)).toEqual(['dotnet'])
  })

  it('失焦时填的其实就是已有语言 → 收回下拉,不再攒同义项', async () => {
    const w = factory({ known: ['java', 'node'] })
    await w.find('select').setValue(CUSTOM_SENTINEL)
    const draft = w.find('input[type=text]')
    await draft.setValue('Java')
    await draft.trigger('blur')
    await nextTick()
    expect(w.emitted('update:modelValue')?.at(-1)).toEqual(['java'])
    expect(w.find('input[type=text]').exists()).toBe(false)
  })

  it('库里遗留的下拉外值置顶并可选中,编辑不会偷改语言', async () => {
    const w = factory({ modelValue: 'golang', known: ['go', 'node'] })
    expect(w.findAll('option').map((o) => o.attributes('value'))).toEqual([
      '',
      'golang',
      'go',
      'node',
      OTHER_LANGUAGE,
      CUSTOM_SENTINEL,
    ])
    expect((w.find('select').element as HTMLSelectElement).value).toBe('golang')
    expect(w.emitted('update:modelValue')).toBeUndefined()
  })

  it('禁用态透传给控件(内置行的语言不可改)', () => {
    const w = factory({ known: ['java'], disabled: true })
    expect((w.find('select').element as HTMLSelectElement).disabled).toBe(true)
  })

  it('选「其他」把不绑定语言的 token 交出去', async () => {
    const w = factory({ known: ['java'] })
    await w.find('select').setValue(OTHER_LANGUAGE)
    expect(w.emitted('update:modelValue')?.at(-1)).toEqual([OTHER_LANGUAGE])
  })
})
