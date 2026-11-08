import { expect, test, type Page, type Route } from '@playwright/test'

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
    },
  }))
}

test('public guide workflow', async ({ context, page }, testInfo) => {
  await context.grantPermissions(['clipboard-read', 'clipboard-write'])
  await preparePublicGuide(page)
  await page.goto('/guide')

  await expect(page).toHaveURL(/\/guide$/)
  await expect(page.locator('[data-test="guide-view"]')).toBeVisible()
  await expect(page.getByRole('heading', { level: 1 })).toContainText('从充值到第一次使用')
  await expect(page.locator('#recharge')).toHaveText('充值：先买兑换码，再兑换余额')
  await expect(page.locator('#goal-workflow')).toHaveText('使用 goal-workflow 小助手')
  await expect(page.locator('#svip')).toHaveText('SVIP 能得到什么、需要注意什么')

  const sameHostPaths = ['/purchase', '/redeem', '/keys']
  for (const path of sameHostPaths) {
    const link = page.locator(`a[href="${path}"]`).first()
    await expect(link).toBeAttached()
    const resolved = new URL(await link.getAttribute('href') || '', page.url())
    expect(resolved.host).toBe(new URL(page.url()).host)
  }

  await expect(page.locator('#domains')).toHaveText('本站网址与 API 地址')
  await expect(page.getByRole('row').filter({ hasText: '网站、登录和控制台入口' }))
    .toContainText('https://www.aifxo.com/')
  await expect(page.getByRole('row').filter({ hasText: 'API 根地址（客户端要求根地址时）' }))
    .toContainText('https://api.aifxo.com')
  await expect(page.locator('a[href="/redeem-store"]')).toHaveCount(0)
  await expect(page.getByRole('link', { name: 'AIFXO', exact: true }))
    .toHaveAttribute('href', 'https://www.aifxo.com/')
  await expect(page.locator('a[href="https://api.aifxo.com"]')).toHaveCount(0)
  await expect(page.locator('#speed-script')).toHaveText('检查与 AIFXO 的连接')
  await expect(page.locator('[data-test="guide-content"]')).toContainText('https://api.aifxo.com/v1')
  await expect(page.locator('[data-test="guide-download"]')).toHaveCount(0)
  await expect(page.locator('[data-test="guide-content"]')).not.toContainText('key66.cc.cd')

  const copyButton = page.locator('[data-test="copy-goal-example"]')
  await copyButton.click()
  await expect(page.locator('[data-test="guide-view"] p[aria-live="polite"]'))
    .toContainText('使用 goal-workflow 已复制')
  await expect.poll(() => page.evaluate(() => navigator.clipboard.readText()))
    .toBe('$goal-workflow 清理内存')
  const closeToastButton = page.getByRole('button', { name: 'Close notification' })
  await closeToastButton.click()
  await expect(closeToastButton).toHaveCount(0)

  if (testInfo.project.name === 'mobile-390x844') {
    const tocToggle = page.locator('[data-test="mobile-toc-toggle"]')
    await expect(tocToggle).toBeVisible()
    await tocToggle.click()
    await expect(tocToggle).toHaveAttribute('aria-expanded', 'true')
    await page.locator('#mobile-guide-toc a[href="#recharge"]').click()
    await expect(tocToggle).toHaveAttribute('aria-expanded', 'false')
    await expect(page).toHaveURL(/#recharge$/)
  } else {
    await expect(page.locator('aside[aria-label="教程目录"]')).toBeVisible()
  }

  const layout = await page.evaluate(() => ({
    documentWidth: document.documentElement.scrollWidth,
    viewportWidth: document.documentElement.clientWidth,
  }))
  expect(layout.documentWidth).toBeLessThanOrEqual(layout.viewportWidth + 1)

  await page.evaluate(() => window.scrollTo(0, 0))
  await page.screenshot({
    path: testInfo.outputPath(`guide-${testInfo.project.name}-viewport.png`),
  })
  await page.screenshot({
    path: testInfo.outputPath(`guide-${testInfo.project.name}.png`),
    fullPage: true,
  })
})
