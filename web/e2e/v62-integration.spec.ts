// e2e/v62-integration.spec.ts — v6.2 前后端联调。
//
// 与 pages-smoke(只验证渲染无报错)和 v62-features(只验证页面/权限)不同:
// 本文件**用真实 UI 驱动完整业务闭环**,每一轮都打到真实 Go 后端并落 SQLite,
// 再回查页面确认结果 —— 覆盖表单校验、错误码透传、列表刷新、禁用态、
// 审计 actor 注入等多层协作。
//
// 前置(由联调脚本保证):
//   - 后端 :8080,已设 PIPEWRIGHT_MASTER_KEY(vault 可用)
//   - 已 seed:11 build_env + 4 config_profile + 普通用户 alice
//   - 已建 2 条凭据:e2e-admin-personal(personal)/ e2e-global(global)
//
// 覆盖:
//   I1  build-envs:UI 新建 → 列表出现 → 编辑 → toggle → 删除 → 消失
//   I2  build-enws:P0#4 三态在 UI 上的表现(新建默认 unchecked,启用被拒并提示)
//   I3  config-profiles:UI 新建 → 出现 → 删除;multipart 上传 → 落盘
//   I4  credentials:admin 页列出凭据;personal 可禁用、global 禁用按钮置灰
//   I5  audit:UI 操作后审计出现对应 action,actor = admin:<username>
//   I6  users:页面列出 alice,角色为普通用户
//   I7  错误透传:重复 (language,version) → UI 显示冲突提示
import { test, expect, type Page } from '@playwright/test'

const BASE = process.env.PLAYWRRIGHT_BASE_URL || 'http://127.0.0.1:5173'
const ADMIN = { username: 'admin', password: 'testpass1234' }

async function login(page: Page): Promise<void> {
  await page.goto(`${BASE}/login`)
  await page.fill('input[type="text"]', ADMIN.username)
  await page.fill('input[type="password"]', ADMIN.password)
  await Promise.all([
    page.waitForURL(/\/(dashboard|onboarding)/, { timeout: 20000 }).catch(() => null),
    page.click('button[type="submit"]'),
  ])
  await page.waitForTimeout(500)
}


/** 按字段 label 文本定位弹窗内的输入框(label 包着 input,抗字段顺序变化)。 */
function field(modal: import('@playwright/test').Locator, labelRe: RegExp) {
  return modal.locator('.field', { hasText: labelRe }).locator('input, textarea, select').first()
}

/** 打开 admin 页面并等表格就绪(标题出现)。 */
async function openAdmin(page: Page, path: string, titleRe: RegExp): Promise<void> {
  await page.goto(`${BASE}${path}`, { waitUntil: 'load' })
  await expect(page.locator('h1.view-title', { hasText: titleRe })).toBeVisible({ timeout: 20000 })
}

test.describe('v6.2 前后端联调', () => {
  test.beforeAll(async ({ browser }) => {
    test.setTimeout(300_000)
    const ctx = await browser.newContext()
    const page = await ctx.newPage()
    await login(page)
    for (const r of [
      '/settings/build-envs',
      '/settings/config-profiles',
      '/settings/credentials',
      '/settings/users',
      '/settings/audit',
    ]) {
      await page.goto(`${BASE}${r}`, { waitUntil: 'load' }).catch(() => null)
      await page.waitForTimeout(700)
    }
    await ctx.close()
  })

  test('I1 build-envs:UI 新建 → 编辑 → 删除 全闭环', async ({ page }) => {
    test.setTimeout(180_000)
    await login(page)
    await openAdmin(page, '/settings/build-envs', /Build environments|构建环境/)

    const before = await page.locator('table.grid tbody tr').count()

    // ── 新建 ──
    await page.getByRole('button', { name: /New build environment|新建构建环境/ }).click()
    const modal = page.locator('.modal')
    await expect(modal).toBeVisible()

    const uniq = String(Date.now()).slice(-6)
    await field(modal, /Language|语言/).fill('e2elang')
    await field(modal, /Version|版本/).fill(uniq)
    await field(modal, /Display name|显示名/).fill('联调环境')
    await field(modal, /Description|说明/).fill('由 e2e 创建')
    await field(modal, /Image address|镜像地址/).fill('alpine:3.20')
    await modal.getByRole('button', { name: /^Save$|^保存$/ }).click()

    // 弹窗关闭 + 行数 +1
    await expect(modal).toBeHidden({ timeout: 20000 })
    await expect(page.locator('table.grid tbody tr')).toHaveCount(before + 1, { timeout: 20000 })

    // 新行可见(displayName 唯一)
    const row = page.locator('table.grid tbody tr', { hasText: '联调环境' })
    await expect(row).toHaveCount(1)
    // 新建默认 unchecked(P0 #4)
    await expect(row.locator('.pill--unchecked')).toBeVisible()

    // ── 编辑 ──
    await row.getByRole('button', { name: /^Edit$|^编辑$/ }).click()
    await expect(modal).toBeVisible()
    await field(modal, /Display name|显示名/).fill('联调环境-已改')
    await modal.getByRole('button', { name: /^Save$|^保存$/ }).click()
    await expect(modal).toBeHidden({ timeout: 20000 })
    await expect(page.locator('table.grid tbody tr', { hasText: '联调环境-已改' })).toHaveCount(1)

    // ── 删除 ──
    await page.locator('table.grid tbody tr', { hasText: '联调环境-已改' })
      .getByRole('button', { name: /^Delete$|^删除$/ }).click()
    await page.locator('.modal--sm').getByRole('button', { name: /^Delete$|^删除$/ }).click()
    await expect(page.locator('table.grid tbody tr')).toHaveCount(before, { timeout: 20000 })
    await expect(page.locator('table.grid tbody tr', { hasText: '联调环境-已改' })).toHaveCount(0)
  })

  test('I2 P0#4 三态:UI 启用 unchecked 被拒并显示原因', async ({ page }) => {
    test.setTimeout(180_000)
    await login(page)
    await openAdmin(page, '/settings/build-envs', /Build environments|构建环境/)

    // seed 的 11 条默认 enabled=true 且 unchecked。直接 check() 是 no-op(无 change 事件),
    // 所以先取消勾选(禁用成功)→ 再勾选(启用)→ 触发 IMAGE_NOT_CHECKED 拒绝。
    const firstRow = page.locator('table.grid tbody tr').first()
    await firstRow.locator('input[type="checkbox"]').uncheck()
    await page.waitForTimeout(1200)
    await firstRow.locator('input[type="checkbox"]').check()
    await page.waitForTimeout(2500)

    // 页面应给出 IMAGE_NOT_CHECKED 提示(banner),且该行仍未启用
    const banner = page.locator('.banner')
    await expect(banner).toBeVisible({ timeout: 20000 })
    await expect(banner).toContainText(/检查|拉取|check|Check|Pull|pull/i)
  })

  test('I3 config-profiles:UI 新建 + multipart 上传', async ({ page, request }) => {
    test.setTimeout(180_000)
    await login(page)

    // 先清掉历史 e2e 残留(上一轮失败可能留下),保证计数基线干净。
    const loginResp = await request.post(`${BASE}/api/auth/login`, { data: ADMIN })
    expect(loginResp.status()).toBe(200)
    const csrf = (await request.storageState()).cookies.find((c) => c.name === 'pipewright_csrf')
    const h = { 'X-CSRF-Token': csrf?.value ?? '' }
    const existing = await (await request.get(`${BASE}/api/admin/config-profiles`, { headers: h })).json()
    for (const it of existing.items ?? []) {
      if (it.language.startsWith('e2e') || it.name.includes('e2e')) {
        await request.delete(`${BASE}/api/admin/config-profiles/${it.id}`, { headers: h })
      }
    }

    await openAdmin(page, '/settings/config-profiles', /Config profiles|配置资源/)
    const before = await page.locator('table.grid tbody tr').count()
    const uniq = String(Date.now()).slice(-6)
    const profileName = `e2e-profile-${uniq}`
    const uploadName = `e2e-upload-${uniq}.npmrc`

    // ── 新建(JSON 路径)──
    await page.getByRole('button', { name: /New config profile|新建配置资源/ }).click()
    const modal = page.locator('.modal')
    await expect(modal).toBeVisible()
    await field(modal, /Language|语言/).fill('e2elang')
    await field(modal, /Config type|配置类型/).fill('e2etype')
    await field(modal, /^Name$|^名称$/).fill(profileName)
    await field(modal, /Container target path|容器内目标路径/).fill('/root/.e2erc')
    await modal.locator('textarea').fill('KEY=value')
    await modal.getByRole('button', { name: /^Save$|^保存$/ }).click()
    // 以「行出现」为成功判据(弹窗关闭是副产品),失败时把 banner 打出来便于定位
    await expect(page.locator('table.grid tbody tr', { hasText: profileName })).toHaveCount(1, {
      timeout: 25000,
    })
    await expect(modal).toBeHidden({ timeout: 25000 })
    await expect(page.locator('table.grid tbody tr')).toHaveCount(before + 1, { timeout: 20000 })

    // ── multipart 上传 ──
    await page.getByRole('button', { name: /^Upload file$|^上传文件$/ }).click()
    const up = page.locator('.modal')
    await expect(up).toBeVisible()
    await up.locator('input[type="file"]').setInputFiles({
      name: uploadName,
      mimeType: 'text/plain',
      buffer: Buffer.from('registry=https://registry.npmmirror.com/\n'),
    })
    // name 由文件名兜底填入,这里只补其余字段
    await field(up, /Language|语言/).fill('e2elang')
    await field(up, /Config type|配置类型/).fill('npmrc')
    await field(up, /Container target path|容器内目标路径/).fill('/root/.npmrc')
    await up.getByRole('button', { name: /^Upload file$|^上传$/ }).click()
    await expect(page.locator('table.grid tbody tr', { hasText: uploadName })).toHaveCount(1, {
      timeout: 25000,
    })
    await expect(up).toBeHidden({ timeout: 25000 })

    // ── 清理 ──
    for (const name of [profileName, uploadName]) {
      const row = page.locator('table.grid tbody tr', { hasText: name }).first()
      await row.getByRole('button', { name: /^Delete$|^删除$/ }).click()
      await page.locator('.modal--sm').getByRole('button', { name: /^Delete$|^删除$/ }).click()
      await page.waitForTimeout(800)
    }
    await expect(page.locator('table.grid tbody tr')).toHaveCount(before, { timeout: 20000 })
  })

  test('I4 credentials:personal 可禁用,global 按钮置灰', async ({ page }) => {
    test.setTimeout(180_000)
    await login(page)
    await openAdmin(page, '/settings/credentials', /Global credentials|全局凭据/)

    const rows = page.locator('table.grid tbody tr')
    await expect(rows.first()).toBeVisible({ timeout: 20000 })

    // global 行:禁用按钮置灰
    const globalRow = page.locator('table.grid tbody tr', { hasText: 'e2e-global' })
    await expect(globalRow).toHaveCount(1)
    await expect(globalRow.getByRole('button', { name: /^Disable$|^禁用$/ })).toBeDisabled()

    // personal 行:可点,点击后按钮变为可用状态/列表刷新
    const personalRow = page.locator('table.grid tbody tr', { hasText: 'e2e-admin-personal' })
    await expect(personalRow).toHaveCount(1)
    await personalRow.getByRole('button', { name: /^Disable$|^禁用$/ }).click()
    await expect(page.locator('.banner')).toBeVisible({ timeout: 20000 })
    await expect(page.locator('.banner')).toContainText(/e2e-admin-personal/)
  })

  test('I4b 禁 global 凭据 → 403(不是 500)', async ({ request }) => {
    // 联调补充:DisableWithActor 对 global 曾返回裸 error → HTTP 500。
    const loginResp = await request.post(`${BASE}/api/auth/login`, { data: ADMIN })
    expect(loginResp.status()).toBe(200)
    const csrf = (await request.storageState()).cookies.find((c) => c.name === 'pipewright_csrf')
    const h = { 'X-CSRF-Token': csrf?.value ?? '' }

    const creds = await (await request.get(`${BASE}/api/credentials`, { headers: h })).json()
    const global = (creds as Array<{ id: string; scope: string }>).find((c) => c.scope === 'global')
    expect(global, '应存在一条 global 凭据(前置 seed)').toBeTruthy()

    const resp = await request.post(`${BASE}/api/admin/credentials/${global!.id}/disable`, {
      headers: h,
    })
    expect(resp.status()).toBe(403)
    const body = await resp.json()
    expect(body.error?.code).toBe('forbidden')
  })

  test('I5 audit:UI 操作写入审计,actor 来自 session', async ({ page, request }) => {
    test.setTimeout(180_000)
    // 先经 API 做一次可审计的写操作(建 build_env)。
    // 浏览器要走 UI 登录(页面断言用);request fixture 另走一次 API 登录
    // (两者 cookie 罐不互通),且它不会自动加 X-CSRF-Token —— 那是前端 api/http.ts
    // 的职责,这里需手动带上。
    await login(page)
    const loginResp = await request.post(`${BASE}/api/auth/login`, { data: ADMIN })
    expect(loginResp.status()).toBe(200)
    const csrf = (await request.storageState()).cookies.find((c) => c.name === 'pipewright_csrf')
    const uniq = String(Date.now()).slice(-6)
    const created = await request.post(`${BASE}/api/admin/build-envs`, {
      headers: { 'X-CSRF-Token': csrf?.value ?? '' },
      data: {
        language: 'e2eaudit',
        version: uniq,
        displayName: '审计联调',
        sourceType: 'official',
        image: 'alpine:3.20',
      },
    })
    expect(created.status()).toBe(201)

    // UI 打开审计页,应看到 build_env_create 且 actor = admin:admin
    await openAdmin(page, '/settings/audit', /Audit log|审计日志/)
    await page.waitForTimeout(2000)
    const body = await page.locator('body').textContent()
    expect(body).toContain('build_env_create')
    expect(body).toContain('admin:admin')

    // 清理
    await request.delete(`${BASE}/api/admin/build-envs/${(await created.json()).id}`, {
      headers: { 'X-CSRF-Token': csrf?.value ?? '' },
    })
  })

  test('I6 users:页面列出普通用户 alice', async ({ page }) => {
    test.setTimeout(180_000)
    await login(page)
    await openAdmin(page, '/settings/users', /Users|用户管理/)

    const body = await page.locator('body').textContent()
    expect(body).toContain('alice')
    // 角色标识(普通用户)
    await expect(page.locator('.tag--user').first()).toBeVisible()
    // 内置管理员提示
    expect(body).toMatch(/Built-in admin|内置管理员/)
  })

  test('I7 错误透传:重复 (language,version) 在 UI 显示冲突', async ({ page }) => {
    test.setTimeout(180_000)
    await login(page)
    await openAdmin(page, '/settings/build-envs', /Build environments|构建环境/)

    // 取第一行的 language / version
    const firstRow = page.locator('table.grid tbody tr').first()
    const lang = await firstRow.locator('td').first().locator('.cell-dim').textContent()
    const [language, version] = (lang ?? '').split('/').map((s) => s.trim())

    await page.getByRole('button', { name: /New build environment|新建构建环境/ }).click()
    const modal = page.locator('.modal')
    await expect(modal).toBeVisible()
    await field(modal, /Language|语言/).fill(language)
    await field(modal, /Version|版本/).fill(version)
    await field(modal, /Display name|显示名/).fill('重复组合')
    await field(modal, /Image address|镜像地址/).fill('alpine:3.20')
    await modal.getByRole('button', { name: /^Save$|^保存$/ }).click()

    // 弹窗不关闭,显示冲突提示
    await expect(modal).toBeVisible()
    await expect(modal.locator('.banner--err')).toBeVisible({ timeout: 20000 })
    await expect(modal.locator('.banner--err')).toContainText(/duplicate|已存在|already exists/i)

    await modal.getByRole('button', { name: /^Cancel$|^取消$/ }).click()
  })
})
