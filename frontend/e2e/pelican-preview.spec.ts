import { readFileSync } from 'node:fs'
import { expect, test, type FrameLocator, type Page, type Route } from '@playwright/test'

// Use the actual backend default stylesheet instead of a second CSS copy that
// could pass browser checks while the HTML delivered by production differs.
const backendSource = readFileSync(new URL('../../backend/internal/service/intelligent_test_html.go', import.meta.url), 'utf8')
const previewDefaults = backendSource.match(/const intelligentTestHTMLPreviewStyle = `([^`]+)`/)?.[1]
if (!previewDefaults) throw new Error('Pelican backend preview stylesheet was not found')

function previewDocument(fragment: string) {
  // Matches wrapIntelligentTestHTML + addIntelligentTestHTMLPreviewDefaults.
  return `<!DOCTYPE html><html><head>${previewDefaults}<meta charset="utf-8"></head><body>${fragment}</body></html>`
}

const wideHTML = previewDocument(`<svg xmlns="http://www.w3.org/2000/svg" id="wide" viewBox="0 0 1600 900">
  <rect id="canvas" width="1600" height="900" fill="none"/>
  <rect id="bottom-right" x="1570" y="870" width="20" height="20" fill="tomato"/>
  <circle id="rider" cy="450" r="25" cx="40" fill="teal"><animate attributeName="cx" values="40;200;40" dur="2s" repeatCount="indefinite"/></circle>
</svg>`)

const portraitHTML = previewDocument(`<style>@layer artwork{body{background:#e6f4ea}}</style>
<svg xmlns="http://www.w3.org/2000/svg" id="portrait" width="900px" height="1600px" viewBox="0 0 900 1600">
  <rect id="canvas" width="900" height="1600" fill="none"/>
  <rect id="bottom-right" x="870" y="1570" width="20" height="20" fill="tomato"/>
  <svg id="nested" x="100" y="200" width="90" height="160" viewBox="0 0 90 160"><rect width="90" height="160" fill="teal"/></svg>
</svg>`)

const differingDimensionsHTML = previewDocument(`<svg xmlns="http://www.w3.org/2000/svg" id="differing-dimensions" width="800" height="600" viewBox="0 0 1600 900">
  <rect width="1600" height="900" fill="none"/>
  <rect id="bottom-right" x="1570" y="870" width="20" height="20" fill="tomato"/>
</svg>`)

const nativeCoordinatesHTML = previewDocument(`<svg xmlns="http://www.w3.org/2000/svg" id="native-coordinates" width="1200" height="800">
  <rect width="1200" height="800" fill="none"/>
  <rect id="bottom-right" x="1170" y="770" width="20" height="20" fill="tomato"/>
</svg>`)

// The API normally strips scripts already. Deliberately bypass that mock-side
// sanitization here to verify the independent, empty-sandbox browser boundary.
const hostileHTML = previewDocument(`<svg xmlns="http://www.w3.org/2000/svg" id="hostile" viewBox="0 0 800 600" onload="document.documentElement.dataset.handlerRan='yes'">
  <rect width="800" height="600" fill="#fff"/>
  <script>document.documentElement.dataset.scriptRan='yes';window.parent.document.documentElement.dataset.artworkEscaped='yes'</script>
</svg>`)

// The reported 13:30 picture uses a 3:2 SVG inside the fixed 4:3 player,
// with its background declared on the SVG itself rather than the document.
const borderedHTML = previewDocument(`<style>
svg { display:block; width:100%; max-width:1100px; height:auto; margin:auto; background:#e9efe4; border-radius:24px }
</style><svg xmlns="http://www.w3.org/2000/svg" id="bordered" viewBox="0 0 900 600">
  <rect id="bottom-right" x="870" y="570" width="20" height="20" fill="tomato"/>
</svg><img src="/pelican-preview-slow.svg" alt="" style="display:none">`)

const user = {
  id: 71, username: 'preview-test', email: 'preview@example.test', role: 'user',
  balance: 0, concurrency: 1, status: 'active', allowed_groups: null,
  balance_notify_enabled: false, balance_notify_threshold: null, balance_notify_extra_emails: [],
  created_at: '2026-11-15T00:00:00Z', updated_at: '2026-11-15T00:00:00Z',
}

const items = [wideHTML, portraitHTML, differingDimensionsHTML, nativeCoordinatesHTML, hostileHTML, borderedHTML].map((html, index) => ({
  id: 6 - index, html, status: 'completed', prompt: 'Draw an animal riding a bicycle.',
  model: 'fixture-model', group_name: 'Preview fixture', reasoning_effort: 'low', duration_ms: 1000,
  created_at: `2026-11-15T${['08:00', '07:30', '07:00', '06:30', '06:00', '05:30'][index]}:00Z`,
}))

function json(route: Route, body: unknown) {
  return route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(body) })
}

async function mockAPIs(page: Page) {
  await page.addInitScript(({ user }) => {
    localStorage.setItem('auth_token', 'browser-test-token')
    localStorage.setItem('auth_user', JSON.stringify(user))
    localStorage.setItem('sub2api_locale', 'zh')
    localStorage.setItem('theme', 'light')
    localStorage.setItem('user_guide_71_user_v4_interactive', 'true')
    Object.defineProperty(window, '__APP_CONFIG__', {
      configurable: true, writable: true,
      value: {
        site_name: 'Sub2API', site_logo: '', version: 'test', backend_mode_enabled: false,
        custom_menu_items: [], payment_enabled: false, channel_monitor_enabled: false,
        affiliate_enabled: false, risk_control_enabled: false,
      },
    })
  }, { user })

  await page.route('**/*', route => {
    const host = new URL(route.request().url()).hostname
    return host === '127.0.0.1' || host === 'localhost' ? route.continue() : route.abort()
  })
  await page.route('**/api/v1/**', route => {
    const path = new URL(route.request().url()).pathname
    if (path.endsWith('/auth/me')) return json(route, { ...user, run_mode: 'standard' })
    if (path.endsWith('/setup/status')) return json(route, { needs_setup: false })
    if (path.endsWith('/pelican-tests/visibility')) return json(route, { visible: true })
    if (path.endsWith('/pelican-tests')) return json(route, { items: items.map(item => ({ ...item, html: '', has_html: true })), total: items.length, page: 1, page_size: 33 })
    const detailID = path.match(/\/pelican-tests\/(\d+)$/)?.[1]
    if (detailID) return json(route, items.find(item => item.id === Number(detailID)))
    if (path.endsWith('/announcements') || path.endsWith('/subscriptions/active')) return json(route, [])
    return json(route, {})
  })
}

async function expectUncropped(page: Page, frame: FrameLocator, id: string, ratio: number) {
  // A desktop -> mobile breakpoint animates AppLayout's sidebar margin for
  // 300ms. Its available width can briefly be zero even though the iframe's
  // native SVG is visible. Wait for the actual parent/iframe layout first.
  await expectContainedViewport(page)
  await expect(frame.locator(`#${id}`)).toBeVisible()
  await expect.poll(() => frame.locator(`#${id}`).evaluate(svg => {
    const bounds = svg.getBoundingClientRect()
    const marker = document.getElementById('bottom-right')!.getBoundingClientRect()
    return {
      fullWidth: Math.abs(bounds.width - window.innerWidth) <= 1,
      fullHeight: Math.abs(bounds.height - window.innerHeight) <= 1,
      noOverflow: document.documentElement.scrollWidth <= window.innerWidth + 1 &&
        document.documentElement.scrollHeight <= window.innerHeight + 1,
      edgeVisible: marker.left >= -1 && marker.top >= -1 && marker.right <= window.innerWidth + 1 && marker.bottom <= window.innerHeight + 1,
    }
  })).toMatchObject({ fullWidth: true, fullHeight: true, noOverflow: true, edgeVisible: true })
  await expect.poll(async () => {
    const bounds = await frame.locator(`#${id}`).boundingBox()
    return bounds && bounds.width > 0 && bounds.height > 0 ? bounds.width / bounds.height : Number.NaN
  }).toBeCloseTo(ratio, 2)
}

async function expectContainedViewport(page: Page) {
  await expect.poll(() => page.getByTestId('pelican-animation-preview').evaluate(preview => {
    // Inspect finite CSS transitions on layout ancestors, not artwork SMIL.
    // This observes the real transition rather than disabling it or sleeping.
    for (let ancestor: Element | null = preview; ancestor; ancestor = ancestor.parentElement) {
      if (ancestor.getAnimations().some(animation => animation instanceof CSSTransition && animation.playState === 'running' &&
        animation.effect?.getComputedTiming().iterations !== Infinity)) return false
    }
    const outer = preview.getBoundingClientRect()
    const inner = preview.querySelector('iframe[data-preview-state="active"]')?.getBoundingClientRect()
    if (!inner || outer.width <= 0 || outer.height <= 0 || inner.width <= 0 || inner.height <= 0) return false
    return Math.abs(outer.width / outer.height - 4 / 3) < 0.01 &&
      inner.left >= outer.left - 1 && inner.top >= outer.top - 1 &&
      inner.right <= outer.right + 1 && inner.bottom <= outer.bottom + 1 &&
      Math.abs(inner.left + inner.width / 2 - outer.left - outer.width / 2) < 1 &&
      Math.abs(inner.top + inner.height / 2 - outer.top - outer.height / 2) < 1
  })).toBe(true)
}

test('鹈鹕预览按画面比例缩放且只运行选中的安全动画', async ({ page }, testInfo) => {
  await mockAPIs(page)
  await page.goto('/pelican-test')
  const iframe = page.locator('iframe[data-preview-state="active"]')
  const frame = page.frameLocator('iframe[data-preview-state="active"]')
  await expect(iframe).toHaveCount(1)
  await expect(iframe).toHaveAttribute('sandbox', '')
  await expect(iframe).toHaveAttribute('referrerpolicy', 'no-referrer')
  await expect(iframe).toHaveAttribute('srcdoc', wideHTML)
  await expectUncropped(page, frame, 'wide', 1600 / 900)
  await expectContainedViewport(page)
  await expect(frame.locator('body')).toHaveCSS('margin', '0px')
  await expect(frame.locator('body')).toHaveCSS('background-color', 'rgba(0, 0, 0, 0)')
  await expect(frame.locator('animate')).toHaveCount(1)
  const start = await frame.locator('#rider').evaluate(node => (node as SVGCircleElement).cx.animVal.value)
  await expect.poll(async () => Math.abs(await frame.locator('#rider').evaluate(node => (node as SVGCircleElement).cx.animVal.value) - start)).toBeGreaterThan(2)

  // The whole native iframe viewport scales, including SVGs without viewBox.
  // Every selection keeps the same outer dimensions on this narrow screen.
  await page.setViewportSize({ width: 360, height: 800 })
  await expectUncropped(page, frame, 'wide', 1600 / 900)
  await expectContainedViewport(page)
  const fixedFrame = await page.getByTestId('pelican-animation-preview').boundingBox()
  await page.getByRole('button', { name: /11-15 15:30/ }).click()
  await expectUncropped(page, frame, 'portrait', 900 / 1600)
  await expectContainedViewport(page)
  const portraitFrame = await page.getByTestId('pelican-animation-preview').boundingBox()
  expect(portraitFrame!.width).toBeCloseTo(fixedFrame!.width, 2)
  expect(portraitFrame!.height).toBeCloseTo(fixedFrame!.height, 2)
  await expect(iframe).toHaveCount(1)
  await expect(page.locator('iframe')).toHaveCount(1)
  await expect(frame.locator('#wide')).toHaveCount(0)
  await expect(frame.locator('body')).toHaveCSS('background-color', 'rgb(230, 244, 234)')
  const nested = await frame.locator('#nested').evaluate(node => {
    const rect = node.getBoundingClientRect()
    const root = document.getElementById('portrait')!.getBoundingClientRect()
    return { width: rect.width / root.width, height: rect.height / root.height }
  })
  expect(nested.width).toBeCloseTo(0.1, 3)
  expect(nested.height).toBeCloseTo(0.1, 3)
  await page.screenshot({ path: testInfo.outputPath('pelican-portrait-mobile.png'), fullPage: true })

  await page.getByRole('button', { name: /11-15 15:00/ }).click()
  await expectUncropped(page, frame, 'differing-dimensions', 800 / 600)
  await expectContainedViewport(page)
  await expect(iframe).toHaveCount(1)
  await expect(page.locator('iframe')).toHaveCount(1)

  await page.getByRole('button', { name: /11-15 14:30/ }).click()
  await expectUncropped(page, frame, 'native-coordinates', 1200 / 800)
  await expectContainedViewport(page)
  const nativeFrame = await page.getByTestId('pelican-animation-preview').boundingBox()
  expect(nativeFrame!.width).toBeCloseTo(fixedFrame!.width, 2)
  expect(nativeFrame!.height).toBeCloseTo(fixedFrame!.height, 2)
  await expect(frame.locator('#native-coordinates')).not.toHaveAttribute('viewBox')

  await page.getByRole('button', { name: /11-15 14:00/ }).click()
  await expect(frame.locator('#hostile')).toBeVisible()
  await expect(iframe).toHaveCount(1)
  await expect(page.locator('iframe')).toHaveCount(1)
  await expect(iframe).toHaveAttribute('sandbox', '')
  await expect(frame.locator('html')).not.toHaveAttribute('data-script-ran')
  await expect(frame.locator('html')).not.toHaveAttribute('data-handler-ran')
  await expect(page.locator('html')).not.toHaveAttribute('data-artwork-escaped')
})

test('切换时间保持旧画面直到加载完成，并用作品背景填充留边', async ({ page }, testInfo) => {
  await mockAPIs(page)
  let releaseImage!: () => void
  let imageRequested!: () => void
  const requested = new Promise<void>(resolve => { imageRequested = resolve })
  const ready = new Promise<void>(resolve => { releaseImage = resolve })
  await page.route('**/pelican-preview-slow.svg', async route => {
    imageRequested()
    await ready
    await route.fulfill({ contentType: 'image/svg+xml', body: '<svg xmlns="http://www.w3.org/2000/svg"/>' })
  })
  await page.goto('/pelican-test')
  const preview = page.getByTestId('pelican-animation-preview')
  const active = page.locator('iframe[data-preview-state="active"]')
  const frame = page.frameLocator('iframe[data-preview-state="active"]')
  await expectUncropped(page, frame, 'wide', 1600 / 900)
  const originalBox = await preview.boundingBox()
  const originalStyle = await active.getAttribute('style')

  await page.getByRole('button', { name: /11-15 13:30/ }).click()
  await requested
  await expect(preview).toHaveAttribute('aria-busy', 'true')
  await expect(page.locator('iframe')).toHaveCount(2)
  await expect(active).toHaveAttribute('srcdoc', wideHTML)
  await expect(active).toHaveAttribute('style', originalStyle!)
  await expect(active).toBeVisible()
  const pending = page.locator('iframe[data-preview-state="pending"]')
  await expect(pending).toHaveCSS('opacity', '0')
  await expect(pending).toHaveCSS('pointer-events', 'none')
  await expect(pending).toHaveAttribute('aria-hidden', 'true')
  const loadingBox = await preview.boundingBox()
  expect(loadingBox!.width).toBeCloseTo(originalBox!.width, 2)
  expect(loadingBox!.height).toBeCloseTo(originalBox!.height, 2)
  await page.screenshot({ path: testInfo.outputPath('pelican-switch-loading.png') })

  releaseImage()
  await expectUncropped(page, frame, 'bordered', 900 / 600)
  await expect(preview).toHaveAttribute('aria-busy', 'false')
  await expect(page.locator('iframe')).toHaveCount(1)
  await expect(active).toHaveAttribute('sandbox', '')
  await expect(preview).toHaveCSS('background-color', 'rgb(233, 239, 228)')
  await expect(active).toHaveCSS('background-color', 'rgba(0, 0, 0, 0)')
  const loadedBox = await preview.boundingBox()
  expect(loadedBox!.width).toBeCloseTo(originalBox!.width, 2)
  expect(loadedBox!.height).toBeCloseTo(originalBox!.height, 2)
  await page.screenshot({ path: testInfo.outputPath('pelican-matched-background.png'), fullPage: true })

  // Rapid selection must settle on the final choice without keeping hidden
  // animations alive. Stale load callbacks also have a controlled unit check.
  await page.getByRole('button', { name: /11-15 15:30/ }).click()
  await page.getByRole('button', { name: /11-15 14:30/ }).click()
  await expectUncropped(page, frame, 'native-coordinates', 1200 / 800)
  await expect(page.locator('iframe')).toHaveCount(1)
  await expect(preview).toHaveAttribute('aria-busy', 'false')
})
