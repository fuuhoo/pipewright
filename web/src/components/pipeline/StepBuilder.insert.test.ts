import { describe, it, expect } from 'vitest'
import { mount } from '@vue/test-utils'
import { nextTick } from 'vue'
import StepBuilder from './StepBuilder.vue'
import { copyStepLine, plainCopyLine } from './stepCompile'

// 步骤卡片之间只有底部「加步骤」能加,想插到中间得先加到末尾再拖。
// 这里锁住分隔条的三件事:数量对(N+1)、点开是一张和「加步骤」同款的类型菜单、
// 选完插在对的位置并且焦点落进新步骤的第一个输入。
const THREE = { commands: 'echo one\necho two\necho three' }

function mountBuilder(config: Record<string, string> = THREE) {
  const host = document.createElement('div')
  document.body.appendChild(host)
  // attachTo 是必需的:focus() 只有真的挂进 document 才认,否则断言永远是 body。
  const wrapper = mount(StepBuilder, { props: { config }, attachTo: host })
  return { wrapper, host }
}

/** 点开第 index 条缝隙的「+」,返回展开出来的类型菜单。 */
async function openInsertMenu(wrapper: ReturnType<typeof mountBuilder>['wrapper'], index: number) {
  await wrapper.findAll('.sb-insert-btn')[index].trigger('click')
  await nextTick()
  return wrapper.findAll('.sb-insert-menu').at(-1)!
}

describe('StepBuilder 步骤间插入', () => {
  it('renders one strip above each step plus a trailing one', () => {
    const { wrapper, host } = mountBuilder()
    try {
      expect(wrapper.findAll('.sb-step').length).toBe(3)
      expect(wrapper.findAll('.sb-insert').length).toBe(4)
      // 第 0 条说「最前面」,其余按「第 N 步之后」报它插在哪(类型还没选,所以只说「步骤」)
      const labels = wrapper.findAll('.sb-insert-btn').map((b) => b.attributes('aria-label'))
      expect(labels[0]).toBe('在最前面插入步骤')
      expect(labels[1]).toBe('在第 1 步之后插入步骤')
      expect(labels[3]).toBe('在第 3 步之后插入步骤')
      expect(wrapper.findAll('.sb-insert-menu').length).toBe(0)
    } finally {
      host.remove()
    }
  })

  it('the strip opens a type menu with the same choices as 加步骤 (复制/高级复制 split apart)', async () => {
    const { wrapper, host } = mountBuilder()
    try {
      const menu = await openInsertMenu(wrapper, 1)
      expect(wrapper.findAll('.sb-insert-btn')[1].attributes('aria-expanded')).toBe('true')
      expect(menu.attributes('aria-label')).toBe('选择要插入的步骤类型')
      expect(menu.findAll('.sb-add-item').map((i) => [
        i.find('.sb-add-name').text(),
        i.find('.sb-add-desc').text(),
      ])).toEqual([
        ['运行命令', 'shell 命令(可多行)'],
        ['设环境变量', 'KEY=VALUE 注入环境'],
        ['切目录', '切换后续命令工作目录'],
        ['复制', '一行 cp 拷贝(不支持排除)'],
        ['高级复制', 'rsync 优先,可排除(没装 rsync 时退回 cp)'],
        ['条件守卫', '不成立则跳过后续步骤'],
      ])
      // 菜单还没点选项 → 列表没变、也没回传
      expect(wrapper.findAll('.sb-step').length).toBe(3)
      expect(wrapper.emitted('update')).toBeUndefined()
    } finally {
      host.remove()
    }
  })

  it('picking a type inserts it at that slot and focuses its first field', async () => {
    const { wrapper, host } = mountBuilder()
    try {
      const menu = await openInsertMenu(wrapper, 1)
      await menu.findAll('.sb-add-item')[1].trigger('click') // 设环境变量 → 第一个输入是 KEY
      await nextTick()

      expect(wrapper.findAll('.sb-insert-menu').length).toBe(0)
      expect(wrapper.findAll('.sb-step').length).toBe(4)
      const keyInput = wrapper.findAll('.sb-env input')[0]
      expect(document.activeElement).toBe(keyInput.element)
      // 空步骤编译成空行,所以回传的 commands 暂时和插之前一样(顺序已经变了)
      expect(wrapper.emitted('update')?.at(-1)?.[0]).toEqual({ commands: THREE.commands })
    } finally {
      host.remove()
    }
  })

  it('keeps the typed value in the slot it was inserted into', async () => {
    const { wrapper, host } = mountBuilder()
    try {
      const menu = await openInsertMenu(wrapper, 1)
      await menu.findAll('.sb-add-item')[0].trigger('click') // 运行命令
      await nextTick()
      const areas = wrapper.findAll('textarea.sb-textarea')
      expect(areas.map((a) => (a.element as HTMLTextAreaElement).value)).toEqual([
        'echo one',
        '',
        'echo two',
        'echo three',
      ])
      await areas[1].setValue('echo mid')
      await areas[1].trigger('blur')

      expect(wrapper.emitted('update')?.at(-1)?.[0]).toEqual({
        commands: 'echo one\necho mid\necho two\necho three',
      })
      // 序号重排,别让新步骤还顶着旧编号
      expect(wrapper.findAll('.sb-order').map((o) => o.text())).toEqual(['1', '2', '3', '4'])
    } finally {
      host.remove()
    }
  })

  it('inserts at the very front from the top strip and at the end from the tail strip', async () => {
    const { wrapper, host } = mountBuilder()
    try {
      const top = await openInsertMenu(wrapper, 0)
      await top.findAll('.sb-add-item')[0].trigger('click')
      await nextTick()
      await wrapper.findAll('textarea.sb-textarea')[0].setValue('echo first')

      const btns = wrapper.findAll('.sb-insert-btn')
      const tail = btns.at(-1)!
      expect(tail.attributes('aria-label')).toBe('在第 4 步之后插入步骤')
      expect(btns.length).toBe(5)
      const menu = await openInsertMenu(wrapper, 4)
      await menu.findAll('.sb-add-item')[0].trigger('click')
      await nextTick()
      const areas = wrapper.findAll('textarea.sb-textarea')
      await areas[4].setValue('echo last')
      await areas[4].trigger('blur')

      expect(wrapper.emitted('update')?.at(-1)?.[0]).toEqual({
        commands: 'echo first\necho one\necho two\necho three\necho last',
      })
    } finally {
      host.remove()
    }
  })

  it('gives a fresh node a step to start from, with a strip above and below it', () => {
    const { wrapper, host } = mountBuilder({})
    try {
      expect(wrapper.findAll('.sb-step').length).toBe(1)
      expect(wrapper.findAll('.sb-insert').length).toBe(2)
    } finally {
      host.remove()
    }
  })
})

describe('StepBuilder 复制的两种写法', () => {
  it('一行 cp 读成「复制」卡片,没有排除格,回写还是那一行 cp', async () => {
    const { wrapper, host } = mountBuilder({ commands: plainCopyLine('frontend/dist', 'build/out') })
    try {
      expect(wrapper.find('.sb-kind').text()).toBe('复制')
      expect(wrapper.find('select[aria-label="复制写法 1"]').element).toMatchObject({ value: 'plain' })
      expect(wrapper.findAll('.sb-copy-excl').length).toBe(0)
      // 空白处显形由 CSS 管,这里锁住语义:一个字都不改地写回去
      await wrapper.find('.sb-step input[type="text"]').trigger('blur')
      expect(wrapper.emitted('update')?.at(-1)?.[0]).toEqual({
        commands: "mkdir -p 'build/out/' && cp -a 'frontend/dist/.' 'build/out/'",
      })
    } finally {
      host.remove()
    }
  })

  it('rsync 骨架读成「高级复制」,排除格照旧在', () => {
    const { wrapper, host } = mountBuilder({ commands: copyStepLine('src', 'out', ['node_modules']) })
    try {
      expect(wrapper.find('.sb-kind').text()).toBe('高级复制')
      expect(wrapper.findAll('.sb-copy-excl').length).toBe(1)
      expect((wrapper.find('.sb-copy-excl input').element as HTMLInputElement).value).toBe('node_modules')
    } finally {
      host.remove()
    }
  })

  it('从「高级复制」切成「复制」时清掉排除项(cp 不吃,留着等于骗用户)', async () => {
    const { wrapper, host } = mountBuilder({ commands: copyStepLine('src', 'out', ['node_modules']) })
    try {
      await wrapper.find('select[aria-label="复制写法 1"]').setValue('plain')
      expect(wrapper.findAll('.sb-copy-excl').length).toBe(0)
      expect(wrapper.emitted('update')?.at(-1)?.[0]).toEqual({
        commands: "mkdir -p 'out/' && cp -a 'src/.' 'out/'",
      })
    } finally {
      host.remove()
    }
  })
})
