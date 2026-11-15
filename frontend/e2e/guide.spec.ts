import { expect, test, type Page, type Route } from '@playwright/test'
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'

const publicSettings = {
  registration_enabled: true,
  email_verify_enabled: false,
  force_email_on_third_party_signup: false,
  registration_email_suffix_whitelist: [],
  password_reset_enabled: true,
  invitation_code_enabled: false,
  turnstile_enabled: false,
  turnstile_site_key: '',
  aliyun_captcha_enabled: false,
  aliyun_captcha_scene_id: '',
  aliyun_captcha_prefix: '',
  aliyun_captcha_region: 'cn',
  site_name: 'AIFXO',
  site_logo: '',
  site_subtitle: '',
  api_base_url: 'https://api.aifxo.com',
  contact_info: '',
  doc_url: '',
  home_content: '',
  compact_home_enabled: false,
  hide_ccs_import_button: false,
  payment_enabled: false,
  risk_control_enabled: false,
  table_default_page_size: 20,
  table_page_size_options: [10, 20, 50],
  custom_menu_items: [],
  custom_endpoints: [],
  linuxdo_oauth_enabled: false,
  wechat_oauth_enabled: false,
  oidc_oauth_enabled: false,
  oidc_oauth_provider_name: 'OIDC',
  github_oauth_enabled: false,
  google_oauth_enabled: false,
  backend_mode_enabled: true,
  version: 'e2e',
  balance_low_notify_enabled: false,
  account_quota_notify_enabled: false,
  balance_low_notify_threshold: 0,
  channel_monitor_enabled: false,
  channel_monitor_default_interval_seconds: 60,
  available_channels_enabled: false,
  model_plaza_enabled: false,
  model_plaza_require_auth: false,
  plugin_management_enabled: false,
  service_quota_enabled: false,
  affiliate_enabled: false,
}

function json(route: Route, body: unknown) {
  return route.fulfill({
    status: 200,
    contentType: 'application/json',
    body: JSON.stringify(body),
  })
}

async function preparePublicGuide(page: Page) {
  await page.addInitScript((settings) => {
    localStorage.clear()
    localStorage.setItem('theme', 'light')
    Object.defineProperty(window, '__APP_CONFIG__', {
      configurable: true,
      writable: true,
      value: settings,
    })
  }, publicSettings)

  await page.route('**/setup/status**', (route) => json(route, {
    code: 0,
    message: 'ok',
    data: { needs_setup: false, step: 'complete' },
  }))
  await page.route('**/api/v1/settings/guide**', (route) => json(route, {
    code: 0,
    message: 'ok',
    data: {
      content: '',
      chapters: [],
      version: 0,
      updated_at: '',
      has_custom_content: false,
      display_mode: 'bundled',
    },
  }))
}

// Exercise the built entry using the server's production script policy, with
// the exact same-origin embedding exception used for this document only.
const configSource = readFileSync(resolve(process.cwd(), '../backend/internal/config/config.go'), 'utf8')
const defaultPolicy = configSource.match(/const DefaultCSPPolicy = "([^"]+)"/)?.[1]
if (!defaultPolicy) throw new Error('DefaultCSPPolicy was not found')
const guidePolicy = defaultPolicy.replace('__CSP_NONCE__', "'nonce-guide-browser-check'")
  .replace("frame-ancestors 'none'", "frame-ancestors 'self'")

async function applyGuideCsp(page: Page) {
  await page.route(/\/guide-content\/(?:index\.html)?(?:\?.*)?$/, async (route) => {
    const response = await route.fetch()
    await route.fulfill({
      response,
      headers: {
        ...response.headers(),
        'content-security-policy': guidePolicy,
        'x-frame-options': 'SAMEORIGIN',
      },
    })
  })
}

test('public guide workflow under production CSP', async ({ context, page }, testInfo) => {
  const errors: string[] = []
  page.on('pageerror', (error) => errors.push(error.message))
  page.on('console', (message) => {
    if (message.type() === 'error' && /content security policy|violates.*directive/i.test(message.text())) errors.push(message.text())
  })
  await context.grantPermissions(['clipboard-read', 'clipboard-write'])
  await preparePublicGuide(page)
  await applyGuideCsp(page)
  await page.goto('/guide')

  const frame = page.frameLocator('[data-test="aifxo-guide-frame"]')
  await expect(frame.locator('#overview.active')).toBeVisible()
  await expect(frame.locator('.page')).toHaveCount(15)
  await expect(frame.locator('script:not([src])')).toHaveCount(0)
  await expect(frame.locator('body')).not.toContainText('本地预览')
  await expect(frame.locator('body')).not.toContainText('HTML 预览版')
  await expect(frame.locator('a[href*="127.0.0.1"], a[href*="localhost"]')).toHaveCount(0)

  // Search works within the isolated page and updates the shareable /guide hash.
  await frame.locator('#search').fill('流式输出')
  await frame.locator('#search').press('Enter')
  await expect(frame.locator('#streaming.active')).toBeVisible()
  await expect(page).toHaveURL(/#streaming$/)
  await expect(frame.locator('#streaming')).toContainText('response.completed')
  await expect(frame.locator('#streaming')).toContainText('[DONE]')

  // Deep links from the parent page reveal hidden panels and preserve file guidance.
  await page.evaluate(() => { window.location.hash = '#codex-auth-file' })
  await expect(frame.locator('#codex.active')).toBeVisible()
  await expect(frame.locator('#legacy-mode')).toBeVisible()
  await expect(frame.locator('#desktop-install')).toBeVisible()
  const auth = frame.locator('[data-code="codex-auth"]')
  await expect(auth).toContainText('"OPENAI_API_KEY": "YOUR-API-KEY"')
  await expect(auth).not.toContainText('auth_mode')
  await auth.getByRole('button', { name: '复制代码' }).click()
  await expect.poll(() => page.evaluate(() => navigator.clipboard.readText()))
    .toBe('{\n  "OPENAI_API_KEY": "YOUR-API-KEY"\n}')

  // The gallery loads one selected image only after expansion.
  const gallery = frame.locator('#desktop-install .gallery').first()
  await expect(gallery.locator('img')).not.toHaveAttribute('src')
  await gallery.locator('summary').click()
  await expect.poll(() => gallery.locator('img').evaluate((image) => (image as HTMLImageElement).naturalWidth)).toBeGreaterThan(0)
  const firstImage = await gallery.locator('img').getAttribute('src')
  await gallery.getByRole('button', { name: '下一张截图' }).click()
  await expect(gallery.locator('img')).not.toHaveAttribute('src', firstImage || '')
  await gallery.getByRole('button', { name: '放大当前截图' }).click()
  await expect(frame.locator('dialog')).toBeVisible()
  await frame.getByRole('button', { name: '关闭预览' }).click()
  await expect(frame.locator('dialog')).not.toBeVisible()

  await page.evaluate(() => { window.location.hash = '#tools' })
  await expect(frame.locator('#tools.active')).toBeVisible()
  await expect(frame.locator('.download-row a')).toHaveAttribute('href',
    'https://github.com/Gnsvip/sub2api-czc/releases/download/czc-v2026.11.15.4/AIFXO-Config-1.2.0-Windows-x64.zip')
  await frame.getByRole('tab', { name: 'CC Switch', exact: true }).click()
  await expect(frame.locator('#tools-switch-panel')).toBeVisible()
  await expect(frame.locator('#tools-aifxo-panel')).not.toBeVisible()

  if (testInfo.project.name === 'mobile-390x844') {
    await frame.getByRole('button', { name: '展开目录' }).click()
    await expect(frame.locator('.menu')).toHaveAttribute('aria-expanded', 'true')
    await frame.locator('.sidebar a[href="#usage"]').click()
    await expect(frame.locator('.menu')).toHaveAttribute('aria-expanded', 'false')
    await expect(page).toHaveURL(/#usage$/)
  } else {
    await expect(frame.locator('.sidebar')).toBeVisible()
  }

  // Every page fits a narrow viewport, and only one article is active.
  const ids = await frame.locator('.page').evaluateAll((pages) => pages.map((item) => item.id))
  for (const id of ids) {
    await page.evaluate((hash) => { window.location.hash = hash }, `#${id}`)
    await expect(frame.locator(`#${id}.active`)).toBeVisible()
    await expect(frame.locator('.page.active')).toHaveCount(1)
    const layout = await frame.locator('html').evaluate((html) => ({
      documentWidth: html.scrollWidth, viewportWidth: html.clientWidth,
    }))
    expect(layout.documentWidth, id).toBeLessThanOrEqual(layout.viewportWidth + 1)
  }
  const links = await frame.locator('a[href^="https://"]').evaluateAll((anchors) => anchors.map((anchor) => ({
    target: anchor.getAttribute('target'), rel: anchor.getAttribute('rel'),
  })))
  for (const link of links) {
    expect(link.target).toBe('_blank')
    expect(link.rel).toContain('noopener')
  }
  expect(errors).toEqual([])
  await page.evaluate(() => { window.location.hash = '#overview' })
  await expect(frame.locator('#overview.active')).toBeVisible()
  await page.screenshot({ path: testInfo.outputPath(`guide-${testInfo.project.name}-viewport.png`) })
})

test('legacy chapter links still open the corresponding modern section', async ({ page }) => {
  await preparePublicGuide(page)
  await applyGuideCsp(page)
  await page.goto('/guide#recharge')
  const frame = page.frameLocator('[data-test="aifxo-guide-frame"]')
  await expect(frame.locator('#start.active')).toBeVisible()
})
