import { describe, it, expect, afterEach } from 'vitest'
import { mount, VueWrapper } from '@vue/test-utils'
import { h, nextTick } from 'vue'
import AppModal from './AppModal.vue'

// AppModal 通过 Teleport 挂到 body → 直接查 document,别在 wrapper 里找。
function modalEl(): HTMLElement {
  const el = document.querySelector('.app-modal')
  if (!el) throw new Error('弹窗未渲染')
  return el as HTMLElement
}

function overlayEl(): HTMLElement {
  const el = document.querySelector('.app-modal-overlay')
  if (!el) throw new Error('遮罩未渲染')
  return el as HTMLElement
}

function mountModal(props: Record<string, unknown> = {}): VueWrapper {
  return mount(AppModal, {
    attachTo: document.body,
    props: { title: '新建分组', ...props },
    slots: {
      default: () => h('input', { class: 'ui-input' }),
      actions: () => h('button', { type: 'submit' }, '保存'),
    },
  })
}

function closeOn(mounted: VueWrapper): void {
  mounted.unmount()
  document.body.innerHTML = ''
}

describe('AppModal', () => {
  afterEach(() => {
    document.body.innerHTML = ''
  })

  it('渲染 dialog 语义,标题用 aria-labelledby 关联', () => {
    const wrapper = mountModal()
    const el = modalEl()
    expect(el.getAttribute('role')).toBe('dialog')
    expect(el.getAttribute('aria-modal')).toBe('true')
    const title = el.querySelector('.app-modal__title')!
    expect(title.textContent).toBe('新建分组')
    expect(el.getAttribute('aria-labelledby')).toBe(title.id)
    closeOn(wrapper)
  })

  it('字段与操作区同在 form 里,所以回车等于提交主操作', () => {
    const wrapper = mountModal()
    const form = modalEl().querySelector('form')!
    expect(form.contains(modalEl().querySelector('.ui-input')!)).toBe(true)
    expect(form.contains(modalEl().querySelector('.app-modal__actions button')!)).toBe(true)

    form.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true }))
    expect(wrapper.emitted('submit')).toHaveLength(1)
    closeOn(wrapper)
  })

  it('点背景请求关闭', async () => {
    const wrapper = mountModal()
    overlayEl().click()
    await nextTick()
    expect(wrapper.emitted('close')).toHaveLength(1)
    closeOn(wrapper)
  })

  it('busy 时背景点击与 Esc 都不关(提交中不许误关)', async () => {
    const wrapper = mountModal({ busy: true })
    overlayEl().click()
    modalEl().dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true }))
    await nextTick()
    expect(wrapper.emitted('close')).toBeUndefined()
    closeOn(wrapper)
  })

  it('打开时把焦点放进第一个字段,关闭后还给触发元素', async () => {
    const trigger = document.createElement('button')
    document.body.appendChild(trigger)
    trigger.focus()

    const wrapper = mountModal()
    await nextTick()
    expect(document.activeElement).toBe(modalEl().querySelector('.ui-input'))

    closeOn(wrapper)
    trigger.remove()
  })
})
