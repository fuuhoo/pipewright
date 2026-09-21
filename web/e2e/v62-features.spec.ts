// e2e/v62-features.spec.ts — v6.2 新功能前端验收。
//
// 覆盖:
//   1. session API 回显 role(§5.2)
//   2. admin 页面真实渲染(构建环境 11 条 / 配置资源 4 条 / 审计 / 用户)
//   3. 菜单 role 隔离(§3.6):admin 看到「构建环境/配置资源」入口,普通用户看不到
//   4. 路由守卫:普通用户访问 /settings/build-envs 被弹回 dashboard
//   5. YAML 折叠区已删(§3.7):画布页无「查看 YAML」按钮
//
// 运行前提:pipewright 后端在 :8080,PIPEWRIGHT_ADMIN_PASSWORD=testpass1234,
// 且已用 auth.HashPassword 造一个普通用户(alice / alice-pass-1234)。
import { test, expect, type Page } from '@playwright/test'

const BASE = process.env.PLAYWRIGHT_BASE_URL || 'http://127.0.0.1:5173'
const ADMIN = { username: 'admin', password: 'testpass1234' }
const ALICE = { username: 'alice', password: 'alice-pass-1234' }

async function login(page: Page, cred: { username: string; password: string }): Promise<void> {
  await page.goto(`${BASE}/login`)
  await page.fill('input[name="username"], input[type="text"]', cred.username)
  await page.fill('input[name="password"], input[type="password"]', cred.password)
  await Promise.all([
    page.waitForURL(/\/(dashboard|$)/, { timeout: 20000 }).catch(() => null),
    page.click('button[type="submit"]'),
  ])
  await page.waitForTimeout(500)
}

/**
 * 预热:vite dev 首次编译新路由会串行 transform 几十个模块,浏览器 6-连接上限
 * 会被击穿(ERR_INSUFFICIENT_RESOURCES),导致首访页面白屏。先各访一次让
 * Vite 完成依赖预打包,后续断言才稳定。
 */
const NEW_ROUTES = [
  '/settings/build-envs',
  '/settings/config-profiles',
  '/settings/credentials',
  '/settings/users',
  '/settings/audit',
  '/settings/my-credentials',
]

async function warmup(page: Page): Promise<void> {
  for (const r of NEW_ROUTES) {
    await page.goto(`${BASE}${r}`, { waitUntil: 'commit' }).catch(() => null)
    await page.waitForTimeout(1200)
  }
}

test.describe('v6.2 新功能', () => {
  // 整个文件跑之前先把 shell / dashboard / settings 各路由编译热,避免首个断言
  // 撞上 Vite 冷编译(dev 模式下这是环境特性,不是前端缺陷)。
  test.beforeAll(async ({ browser }) => {
    test.setTimeout(300_000)
    const ctx = await browser.newContext()
    const page = await ctx.newPage()
    await login(page, ADMIN)
    for (const r of ['/dashboard', '/settings/ai', '/settings/account', ...NEW_ROUTES]) {
      await page.goto(`${BASE}${r}`, { waitUntil: 'load' }).catch(() => null)
      await page.waitForTimeout(600)
    }
    await ctx.close()
  })

  test('session API 回显 role', async ({ request }) => {
    // 未登录 → 401
    const anon = await request.get(`${BASE}/api/auth/session`)
    expect(anon.status()).toBe(401)

    // admin 登录
    const r = await request.post(`${BASE}/api/auth/login`, { data: ADMIN })
    expect(r.status()).toBe(200)
    const body = await r.json()
    expect(body.username).toBe('admin')
    expect(body.role).toBe('admin')
  })

  test('普通用户登录拿到 role=user 且 admin 端点 403', async ({ request }) => {
    const r = await request.post(`${BASE}/api/auth/login`, { data: ALICE })
    expect(r.status()).toBe(200)
    const body = await r.json()
    expect(body.username).toBe('alice')
    expect(body.role).toBe('user')

    // RequireAdmin 后端权威校验
    const admin = await request.get(`${BASE}/api/admin/build-envs`)
    expect(admin.status()).toBe(403)
  })

  test('admin:构建环境页渲染 11 条 seed', async ({ page }) => {
    test.setTimeout(120_000)
    await login(page, ADMIN)
    await warmup(page)
    await page.goto(`${BASE}/settings/build-envs`, { waitUntil: 'load' })
    await page.waitForTimeout(2000)

    // 页面已挂载(标题在),再等表格
    await expect(page.locator('h1.view-title', { hasText: /Build environments|构建环境/ })).toBeVisible({ timeout: 20000 })
    const rows = page.locator('table.grid tbody tr')
    await expect(rows).toHaveCount(11, { timeout: 20000 })

    // 状态徽标存在(三态)
    expect(await page.locator('.pill').count()).toBeGreaterThan(0)
  })

  test('admin:配置资源页渲染 4 条 seed', async ({ page }) => {
    test.setTimeout(120_000)
    await login(page, ADMIN)
    await warmup(page)
    await page.goto(`${BASE}/settings/config-profiles`, { waitUntil: 'load' })
    await page.waitForTimeout(2000)

    await expect(page.locator('h1.view-title', { hasText: /Config profiles|配置资源/ })).toBeVisible({ timeout: 20000 })
    const rows = page.locator('table.grid tbody tr')
    await expect(rows).toHaveCount(4, { timeout: 20000 })

    // 内置行带「内置」标签
    await expect(page.locator('.tag--builtin').first()).toBeVisible()
  })

  test('admin:审计日志页可加载', async ({ page }) => {
    test.setTimeout(120_000)
    await login(page, ADMIN)
    await warmup(page)
    await page.goto(`${BASE}/settings/audit`, { waitUntil: 'load' })
    await page.waitForTimeout(2000)
    await expect(page.locator('h1.view-title', { hasText: /Audit log|审计日志/ })).toBeVisible({ timeout: 20000 })
  })

  test('admin:用户管理页可加载', async ({ page }) => {
    test.setTimeout(120_000)
    await login(page, ADMIN)
    await warmup(page)
    await page.goto(`${BASE}/settings/users`, { waitUntil: 'load' })
    await page.waitForTimeout(2000)
    await expect(page.locator('h1.view-title', { hasText: /Users|用户管理/ })).toBeVisible({ timeout: 20000 })
  })

  test('§3.6 菜单隔离:admin 见 admin 入口,普通用户不见', async ({ page }) => {
    test.setTimeout(120_000)
    // admin:侧栏应有「构建环境 / 配置资源」(按 href 判定,避免依赖界面语言)
    await login(page, ADMIN)
    await page.goto(`${BASE}/dashboard`, { waitUntil: 'load' })
    await page.waitForTimeout(2000)
    await expect(page.locator('a[href="/settings/build-envs"]')).toBeVisible({ timeout: 20000 })
    await expect(page.locator('a[href="/settings/config-profiles"]')).toBeVisible({ timeout: 20000 })

    // 设置页应有「全局设置 / 个人设置」两组
    await page.goto(`${BASE}/settings/ai`, { waitUntil: 'load' })
    await page.waitForTimeout(2000)
    await expect(page.locator('.settings-group')).toHaveCount(2, { timeout: 20000 })
  })

  test('§3.6 路由守卫:普通用户访问 admin 页被弹回 dashboard', async ({ browser }) => {
    test.setTimeout(180_000)
    // 用干净 context 登普通用户(避免 admin cookie 串味)
    const ctx = await browser.newContext()
    const page = await ctx.newPage()
    await login(page, ALICE)

    // 侧栏不应有 admin 入口
    await page.goto(`${BASE}/dashboard`, { waitUntil: 'load' })
    await page.waitForTimeout(2000)
    await expect(page.locator('a[href="/settings/build-envs"]')).toHaveCount(0, { timeout: 20000 })
    await expect(page.locator('a[href="/settings/config-profiles"]')).toHaveCount(0)

    // 直接打 admin 路由 → 守卫重定向到 dashboard
    await page.goto(`${BASE}/settings/build-envs`, { waitUntil: 'load' })
    await page.waitForTimeout(3000)
    expect(new URL(page.url()).pathname).toBe('/dashboard')

    // 设置页只应有「个人设置」一组
    await page.goto(`${BASE}/settings/account`, { waitUntil: 'load' })
    await page.waitForTimeout(2000)
    await expect(page.locator('.settings-group')).toHaveCount(1, { timeout: 20000 })

    await ctx.close()
  })

  test('§3.7 画布无 YAML 折叠区', async ({ page }) => {
    test.setTimeout(120_000)
    await login(page, ADMIN)
    await page.goto(`${BASE}/projects`, { waitUntil: 'load' })
    await page.waitForTimeout(2000)
    const body = await page.locator('body').textContent()
    expect(body).not.toContain('查看 YAML')
    expect(body).not.toContain('View YAML')
  })
})
