import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { createI18n } from 'vue-i18n'
import { createPinia, setActivePinia } from 'pinia'
import { usePelicanStore } from '@/stores/pelican'
import PelicanTestView from '../PelicanTestView.vue'
import PelicanAnimationPreview from '@/components/common/PelicanAnimationPreview.vue'
import { pelicanTestsAPI } from '@/api/pelicanTests'
import { pelicanPreviewBackground, pelicanPreviewViewport } from '@/utils/pelicanPreview'

let nextPaintId = 0
const paintCallbacks = new Map<number, FrameRequestCallback>()
beforeEach(() => {
  nextPaintId = 0
  paintCallbacks.clear()
  vi.stubGlobal('requestAnimationFrame', vi.fn((callback: FrameRequestCallback) => {
    const id = ++nextPaintId
    paintCallbacks.set(id, callback)
    return id
  }))
  vi.stubGlobal('cancelAnimationFrame', vi.fn((id: number) => { paintCallbacks.delete(id) }))
})
afterEach(() => {
  paintCallbacks.clear()
  vi.useRealTimers()
  vi.unstubAllGlobals()
})

function paintNextFrame() {
  const callbacks = [...paintCallbacks.values()]
  paintCallbacks.clear()
  callbacks.forEach(callback => callback(0))
}

async function loadPreview(wrapper: VueWrapper) {
  await wrapper.get('iframe[data-preview-state="pending"]').trigger('load')
  paintNextFrame()
  paintNextFrame()
  await flushPromises()
}

vi.mock('@/api/pelicanTests', () => ({
  pelicanTestsAPI: { list: vi.fn(), detail: vi.fn(), visibility: vi.fn() }
}))
vi.mock('@/components/layout/AppLayout.vue', () => ({
  default: { template: '<div><slot /></div>' }
}))
vi.mock('@/components/common/LoadingSpinner.vue', () => ({
  default: { template: '<div>loading</div>' }
}))

const messages = {
  zh: {
    nav: { pelicanTest: '鹈鹕测试' },
    pelicanTest: {
      subtitle: '每半小时自动', passed: '通过', anomalous: '异常', unscored: '未评分', details: '评分与测试详情',
      empty: '暂无测试结果',
      scoringFailed: '评分失败',
      time: '发起时间（北京）',
      group: '测试分组',
      model: '模型',
      reasoning: '推理深度',
      htmlUnavailable: '无法安全显示这段 HTML',
      incompleteLabel: '输出未完成',
      incompleteResult: '本次测试未返回完整内容，残缺结果不作为完整画面展示。',
      incompletePreview: '本次输出未完成，请选择其他时间的结果。',
      scriptsRemoved: '原答复包含脚本，当前预览不执行脚本，部分画面或动画可能无法显示。',
      prompt: '原始提示词',
      previewLoading: '正在加载画面…',
      previewLoadTimeout: '画面加载超时',
      previewRetry: '重试',
      loadFailed: '加载失败'
    }
  }
}

function createTestI18n() {
  // Production and tests use the CSP-safe runtime build, which accepts
  // precompiled message functions rather than compiling strings at runtime.
  const compiled = {
    zh: {
      nav: { pelicanTest: () => messages.zh.nav.pelicanTest },
      pelicanTest: Object.fromEntries(Object.entries(messages.zh.pelicanTest).map(([key, value]) => [key, () => value]))
    }
  }
  return createI18n({ legacy: false, locale: 'zh', messages: compiled })
}

function mountPreview(html: string) {
  return mount(PelicanAnimationPreview, {
    props: { html, title: 'preview' },
    global: { plugins: [createTestI18n()] },
  })
}

function mountView() {
  const i18n = createTestI18n()
  const pinia = createPinia()
  setActivePinia(pinia)
  usePelicanStore().setVisible(true)
  return mount(PelicanTestView, { global: { plugins: [i18n, pinia] } })
}

describe('PelicanTestView', () => {
  it('loads only selected HTML, ignores stale details and aborts when hidden', async () => {
    const base = { status: 'completed', html: '', has_html: true, prompt: 'draw', model: 'fixture', group_name: 'group', reasoning_effort: 'low', created_at: '2026-11-17T02:00:00Z', duration_ms: 10 }
    const rows = [{ ...base, id: 3 }, { ...base, id: 2 }, { ...base, id: 1 }]
    vi.mocked(pelicanTestsAPI.list).mockResolvedValue({ items: rows, total: 3, page: 1, page_size: 33 })
    const resolvers: Array<(value: typeof base & { id: number }) => void> = []
    vi.mocked(pelicanTestsAPI.detail).mockImplementation(() => new Promise(resolve => { resolvers.push(resolve) }))
    const wrapper = mountView()
    await flushPromises()
    expect(pelicanTestsAPI.detail).toHaveBeenLastCalledWith(3, expect.any(AbortSignal))
    const firstSignal = vi.mocked(pelicanTestsAPI.detail).mock.calls.at(-1)![1]!
    await wrapper.findAll('button')[1].trigger('click')
    expect(firstSignal.aborted).toBe(true)
    resolvers[1]({ ...rows[1], html: '<html><svg id="selected"></svg></html>' })
    await flushPromises()
    await loadPreview(wrapper)
    resolvers[0]({ ...rows[0], html: '<html><svg id="stale"></svg></html>' })
    await flushPromises()
    expect(wrapper.get('iframe').attributes('srcdoc')).toContain('id="selected"')
    expect(wrapper.html()).not.toContain('id="stale"')
    await wrapper.findAll('button')[2].trigger('click')
    // The previous frame remains mounted while the next picture downloads.
    expect(wrapper.get('iframe').attributes('srcdoc')).toContain('id="selected"')
    const lastSignal = vi.mocked(pelicanTestsAPI.detail).mock.calls.at(-1)![1]!
    usePelicanStore().setVisible(false)
    await flushPromises()
    expect(lastSignal.aborted).toBe(true)
    resolvers[2]({ ...rows[2], html: '<html><svg id="hidden"></svg></html>' })
    await flushPromises()
    expect(wrapper.find('iframe').exists()).toBe(false)
    wrapper.unmount()
  })

  it('refreshes visibility and ignores a response started before an administrator hid it', async () => {
    setActivePinia(createPinia())
    const store = usePelicanStore()
    expect(store.visible).toBe(false)
    vi.mocked(pelicanTestsAPI.visibility).mockResolvedValueOnce({ visible: true })
    expect(await store.refresh()).toBe(true)
    let resolve!: (value: { visible: boolean }) => void
    vi.mocked(pelicanTestsAPI.visibility).mockReturnValueOnce(new Promise(done => { resolve = done }))
    const pending = store.refresh()
    store.setVisible(false)
    resolve({ visible: true })
    expect(await pending).toBe(false)
    vi.mocked(pelicanTestsAPI.visibility).mockRejectedValueOnce(new Error('offline'))
    expect(await store.refresh()).toBe(false)
  })
  it('selects a complete result and labels interrupted attempts without rendering partial HTML', async () => {
    const base = {
      model: 'gpt-6-astra', group_name: 'GPT-PRO', reasoning_effort: 'low',
      prompt: 'draw', created_at: '2026-09-24T06:00:00Z', duration_ms: 90053
    }
    vi.mocked(pelicanTestsAPI.list).mockResolvedValue({
      items: [
        { ...base, id: 3, status: 'failed', html: '<html><body><svg><path' },
        { ...base, id: 2, status: 'completed', html: '', preview_issue: 'incomplete' },
        { ...base, id: 1, status: 'completed', html: '<html><svg id="complete"></svg></html>', visual_judgement: { state: 'unavailable', threshold: 50, reason: '评审超时' } }
      ],
      total: 3, page: 1, page_size: 96
    })
    const wrapper = mountView()
    await flushPromises()
    await loadPreview(wrapper)
    expect(wrapper.get('iframe').attributes('srcdoc')).toContain('id="complete"')
    const buttons = wrapper.findAll('button')
    expect(buttons[0].text()).toContain('输出未完成')
    expect(buttons[1].text()).toContain('输出未完成')
    expect(buttons[2].text()).toContain('评分失败')
    await buttons[0].trigger('click')
    expect(wrapper.find('iframe').exists()).toBe(false)
    expect(wrapper.get('article > p[role="status"]').text()).toContain('未返回完整内容')
    await buttons[1].trigger('click')
    expect(wrapper.find('iframe').exists()).toBe(false)
    wrapper.unmount()
  })

  it('explains disabled scripts while retaining the safe preview sandbox', async () => {
    vi.mocked(pelicanTestsAPI.list).mockResolvedValue({
      items: [{
        id: 1, status: 'completed', html: '<html><svg></svg></html>', preview_issue: 'scripts_removed',
        model: 'gpt-6-astra', group_name: 'GPT-PRO', reasoning_effort: 'low',
        prompt: 'draw', created_at: '2026-09-24T06:00:00Z', duration_ms: 100000
      }], total: 1, page: 1, page_size: 96
    })
    const wrapper = mountView()
    await flushPromises()
    await loadPreview(wrapper)
    expect(wrapper.get('article > p[role="status"]').text()).toContain('当前预览不执行脚本')
    expect(wrapper.get('iframe').attributes('sandbox')).toBe('')
    usePelicanStore().setVisible(false)
    await flushPromises()
    expect(wrapper.find('iframe').exists()).toBe(false)
    expect(wrapper.text()).not.toContain('测试分组')
    wrapper.unmount()
  })

  it('renders HTML results with time, group, model and reasoning and has no run button', async () => {
    vi.mocked(pelicanTestsAPI.list).mockResolvedValue({
      items: [
        {
          id: 10,
          status: 'completed',
          html: '<html><style>body{background:#193344}svg{width:100%}</style><body><svg id="latest" viewBox="0 0 1600 900"><animate attributeName="opacity" values="0;1" dur="2s" repeatCount="indefinite"/></svg></body></html>',
          prompt: '创建一个HTML，内容是SVG绘制一个火烈鸟骑自行车的2D动画',
          model: 'gpt-6-astra',
          group_name: 'GPT-PRO',
          reasoning_effort: 'low',
          created_at: '2026-09-20T16:00:00Z',
          finished_at: '2026-09-20T16:02:12Z',
          duration_ms: 132000
        },
        {
          id: 9,
          status: 'completed',
          html: '<html><body><svg id="older" viewBox="0 0 600 800"></svg></body></html>',
          prompt: '创建一个HTML，内容是SVG绘制一个鹈鹕骑自行车的2D动画',
          model: 'gpt-6-astra',
          group_name: 'GPT-PRO',
          reasoning_effort: 'low',
          created_at: '2026-09-20T15:30:00Z',
          finished_at: '2026-09-20T15:32:12Z',
          duration_ms: 132000
        }
      ],
      total: 2,
      page: 1,
      page_size: 12
    })
    const wrapper = mountView()
    await flushPromises()
    await loadPreview(wrapper)
    expect(pelicanTestsAPI.list).toHaveBeenCalledWith(1, 33, true, expect.any(AbortSignal))
    expect(wrapper.findAll('h1')).toHaveLength(1)
    expect(wrapper.get('h1').classes()).toContain('lg:hidden')
    expect(wrapper.findAll('iframe')).toHaveLength(1)
    expect(wrapper.get('[data-testid="pelican-details"]').attributes('open')).toBeUndefined()
    expect(wrapper.get('[data-testid="pelican-details"]').find('pre').exists()).toBe(true)
    expect(wrapper.find('iframe').attributes('srcdoc')).toContain('id="latest"')
    expect(wrapper.get('iframe').element.style.height).toBe('900px')
    expect(wrapper.get('iframe').element.style.width).toBe('1600px')
    expect(wrapper.get('[data-testid="pelican-animation-preview"]').classes()).toContain('aspect-[4/3]')
    expect(wrapper.get('iframe').classes()).not.toContain('bg-white')
    expect(wrapper.get('iframe').element.style.backgroundColor).toBe('transparent')
    expect(wrapper.get<HTMLElement>('[data-testid="pelican-animation-preview"]').element.style.backgroundColor)
      .toBe('rgb(25, 51, 68)')
    expect(wrapper.get('iframe').attributes('srcdoc')).toContain('body{background:#193344}')
    expect(wrapper.get('iframe').attributes('srcdoc')).toContain('<animate')
    expect(wrapper.get('iframe').attributes('sandbox')).toBe('')
    expect(wrapper.text()).toContain('火烈鸟')
    const buttons = wrapper.findAll('button')
    expect(buttons).toHaveLength(2)
    expect(buttons[0].text()).toContain('09-21 00:00')
    expect(buttons[1].text()).toContain('09-20 23:30')
    expect(wrapper.text()).not.toContain('00:02')
    expect(wrapper.get('dd').text()).toContain('00:00:00')
    const timeColumn = buttons[0].element.parentElement
    expect(timeColumn).toBeTruthy()
    expect(timeColumn?.parentElement).toBe(wrapper.find('article').element.parentElement)
    expect(timeColumn?.className).toContain('flex-col')
    await buttons[1].trigger('click')
    expect(wrapper.findAll('iframe')).toHaveLength(2)
    expect(wrapper.get('iframe[data-preview-state="active"]').attributes('srcdoc')).toContain('id="latest"')
    expect(wrapper.get<HTMLIFrameElement>('iframe[data-preview-state="active"]').element.style.height).toBe('900px')
    expect(wrapper.get('[data-testid="pelican-animation-preview"]').attributes('aria-busy')).toBe('true')
    await loadPreview(wrapper)
    expect(wrapper.findAll('iframe')).toHaveLength(1)
    expect(wrapper.find('iframe').attributes('srcdoc')).toContain('id="older"')
    expect(wrapper.get('iframe').element.style.height).toBe('800px')
    expect(wrapper.get('iframe').element.style.width).toBe('600px')
    expect(wrapper.get('[data-testid="pelican-animation-preview"]').classes()).toContain('aspect-[4/3]')
    expect(wrapper.text()).toContain('鹈鹕')
    expect(wrapper.text()).not.toContain('开始测试')
    expect(wrapper.text()).not.toContain('重新测试')
    wrapper.unmount()
  })
})

describe('pelican preview frame replacement', () => {
  const first = '<svg viewBox="0 0 900 600" style="background:#e9efe4;border-radius:24px"><animate attributeName="opacity" values="0;1" dur="2s"/></svg>'
  const second = '<svg viewBox="0 0 600 800" style="background:#fff7e7"></svg>'
  const third = '<svg viewBox="0 0 1200 600" style="background:#123456"></svg>'

  it('keeps the visible frame and its color until the new document has loaded and painted', async () => {
    const wrapper = mountPreview(first)
    expect(wrapper.findAll('iframe')).toHaveLength(1)
    expect(wrapper.get('iframe').attributes('data-preview-state')).toBe('pending')
    expect(wrapper.get('iframe').element.style.opacity).toBe('0')
    await loadPreview(wrapper)
    const oldFrame = wrapper.get('iframe').element
    expect(wrapper.attributes('aria-busy')).toBe('false')
    expect(wrapper.get<HTMLElement>('[data-testid="pelican-animation-preview"]').element.style.backgroundColor).toBe('rgb(233, 239, 228)')

    await wrapper.setProps({ html: second })
    const active = wrapper.get<HTMLIFrameElement>('iframe[data-preview-state="active"]')
    expect(active.element).toBe(oldFrame)
    expect(active.element.style.width).toBe('900px')
    expect(active.element.style.height).toBe('600px')
    expect(active.element.style.opacity).toBe('1')
    expect(wrapper.get<HTMLElement>('[data-testid="pelican-animation-preview"]').element.style.backgroundColor).toBe('rgb(233, 239, 228)')
    expect(wrapper.get('iframe[data-preview-state="pending"]').attributes('sandbox')).toBe('')
    expect(wrapper.get('iframe[data-preview-state="pending"]').attributes('aria-hidden')).toBe('true')
    expect(wrapper.get<HTMLIFrameElement>('iframe[data-preview-state="pending"]').element.style.pointerEvents).toBe('none')

    await wrapper.get('iframe[data-preview-state="pending"]').trigger('load')
    paintNextFrame()
    await flushPromises()
    expect(wrapper.findAll('iframe')).toHaveLength(2)
    expect(wrapper.get('iframe[data-preview-state="active"]').element).toBe(oldFrame)
    paintNextFrame()
    await flushPromises()
    expect(wrapper.findAll('iframe')).toHaveLength(1)
    expect(wrapper.get('iframe').element).not.toBe(oldFrame)
    expect(wrapper.get('iframe').attributes('srcdoc')).toBe(second)
    expect(wrapper.get<HTMLElement>('[data-testid="pelican-animation-preview"]').element.style.backgroundColor).toBe('rgb(255, 247, 231)')
    expect(wrapper.get('iframe').attributes('sandbox')).toBe('')
    expect(wrapper.get('iframe').attributes('referrerpolicy')).toBe('no-referrer')
    expect(wrapper.get('iframe').element.style.backgroundColor).toBe('transparent')
    wrapper.unmount()
  })

  it('discards stale loads during rapid switching and never retains more than two frames', async () => {
    const wrapper = mountPreview(first)
    await loadPreview(wrapper)
    const original = wrapper.get('iframe').element
    await wrapper.setProps({ html: second })
    await wrapper.get('iframe[data-preview-state="pending"]').trigger('load')
    paintNextFrame()
    const stalePaint = [...paintCallbacks.values()]
    await wrapper.setProps({ html: third })
    stalePaint.forEach(callback => callback(0))
    await flushPromises()
    expect(wrapper.findAll('iframe')).toHaveLength(2)
    expect(wrapper.get('iframe[data-preview-state="active"]').element).toBe(original)
    expect(wrapper.get('iframe[data-preview-state="pending"]').attributes('srcdoc')).toBe(third)
    await loadPreview(wrapper)
    expect(wrapper.findAll('iframe')).toHaveLength(1)
    expect(wrapper.get('iframe').attributes('srcdoc')).toBe(third)
    expect(wrapper.attributes('aria-busy')).toBe('false')
    wrapper.unmount()
  })

  it('cancels pending work when reselecting the current animation or unmounting', async () => {
    const wrapper = mountPreview(first)
    await loadPreview(wrapper)
    const original = wrapper.get('iframe').element
    await wrapper.setProps({ html: second })
    await wrapper.get('iframe[data-preview-state="pending"]').trigger('load')
    await wrapper.setProps({ html: first })
    expect(paintCallbacks.size).toBe(0)
    expect(wrapper.findAll('iframe')).toHaveLength(1)
    expect(wrapper.get('iframe').element).toBe(original)
    await wrapper.setProps({ html: third })
    await wrapper.get('iframe[data-preview-state="pending"]').trigger('load')
    expect(paintCallbacks.size).toBe(1)
    wrapper.unmount()
    expect(paintCallbacks.size).toBe(0)
  })

  it('shows delayed loading, times out without replacing the old frame, and retries the selected HTML', async () => {
    vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout'] })
    const wrapper = mountPreview(first)
    await loadPreview(wrapper)
    const original = wrapper.get('iframe').element
    await wrapper.setProps({ html: second })
    await vi.advanceTimersByTimeAsync(249)
    expect(wrapper.find('[data-testid="pelican-preview-status"]').exists()).toBe(false)
    await vi.advanceTimersByTimeAsync(1)
    expect(wrapper.get('[data-testid="pelican-preview-status"]').text()).toContain('正在加载画面')
    await vi.advanceTimersByTimeAsync(9750)
    expect(wrapper.findAll('iframe')).toHaveLength(1)
    expect(wrapper.get('iframe').element).toBe(original)
    expect(wrapper.attributes('aria-busy')).toBe('false')
    expect(wrapper.get('[data-testid="pelican-preview-status"]').text()).toContain('画面加载超时')
    expect(vi.getTimerCount()).toBe(0)
    await wrapper.get('[data-testid="pelican-preview-retry"]').trigger('click')
    expect(wrapper.findAll('iframe')).toHaveLength(2)
    expect(wrapper.get('iframe[data-preview-state="pending"]').attributes('srcdoc')).toBe(second)
    expect(wrapper.attributes('aria-busy')).toBe('true')
    await loadPreview(wrapper)
    expect(wrapper.findAll('iframe')).toHaveLength(1)
    expect(wrapper.get('iframe').attributes('srcdoc')).toBe(second)
    expect(wrapper.find('[data-testid="pelican-preview-status"]').exists()).toBe(false)
    expect(vi.getTimerCount()).toBe(0)
    await wrapper.setProps({ html: third })
    expect(vi.getTimerCount()).toBe(2)
    wrapper.unmount()
    expect(vi.getTimerCount()).toBe(0)
  })
})

describe('pelican preview geometry', () => {
  it.each([
    ['<svg viewBox="-10 -20 1600 900"></svg>', '1600 / 900'],
    ['<html><body><div><svg viewBox="0,0,600,800"></svg></div></body></html>', '600 / 800'],
    ['<svg width="960px" height="540px"></svg>', '960 / 540'],
    ['<svg width="800" height="600" viewBox="0 0 1600 900"></svg>', '800 / 600'],
    ['<svg width="100%" height="100%" viewBox="0 0 1200 800"></svg>', '1200 / 800'],
    ['<svg viewBox="0 0 1.6e3 9e2"><svg viewBox="0 0 20 20"/></svg>', '1600 / 900'],
    ['<svg viewBox="0 0 100 100"></svg><svg viewBox="0 0 200 100"></svg>', '4 / 3'],
    ['<div hidden><svg viewBox="0 0 100 100"></svg></div>', '4 / 3'],
    ['<svg viewBox="0 0 0 900"></svg>', '4 / 3'],
    ['<svg viewBox="0 0 -1600 900"></svg>', '4 / 3'],
    ['<svg viewBox="0 0 Infinity 900"></svg>', '4 / 3'],
    ['<svg viewBox="0 0 1e309 900"></svg>', '4 / 3'],
    ['<svg viewBox="0,,0,1600,900"></svg>', '4 / 3'],
    ['<svg viewBox="0 0 1600 900 1"></svg>', '4 / 3'],
    ['<svg viewBox="0 0 1600 1"></svg>', '4 / 3'],
    ['<svg width="100%" height="100%"></svg>', '4 / 3'],
    ['<svg width="800" height="-600"></svg>', '4 / 3'],
    ['<svg width="800" height="600px;position:fixed"></svg>', '4 / 3'],
    ['<p>no SVG</p>', '4 / 3'],
    ['', '4 / 3']
  ])('reads only a reliable root SVG viewport: %s', (html, expected) => {
    const { width, height } = pelicanPreviewViewport(html)
    expect(`${width} / ${height}`).toBe(expected === '4 / 3' ? '800 / 600' : expected)
  })

  it('does not attach model output or execute its script while reading geometry', () => {
    const html = '<html><style>body{background:navy}</style><body><svg id="untrusted-pelican-geometry" viewBox="0 0 800 600" onload="throw new Error()"><script>throw new Error("must not execute")</script><animate attributeName="x" values="0;20"/></svg></body></html>'
    expect(pelicanPreviewViewport(html)).toEqual({ width: 800, height: 600 })
    expect(document.getElementById('untrusted-pelican-geometry')).toBeNull()
    expect(pelicanPreviewViewport(' '.repeat(2_000_001))).toEqual({ width: 800, height: 600 })
  })
})

describe('pelican preview background', () => {
  it.each([
    ['<style>svg{width:100%;max-width:1100px;height:auto;margin:auto;background:#e9efe4;border-radius:24px}</style><svg viewBox="0 0 900 600"></svg>', '#e9efe4'],
    ['<svg viewBox="0 0 1000 680"><style>svg{position:fixed;inset:0;background:#fff7e7}</style></svg>', '#fff7e7'],
    ['<html style="background:#112233"><body style="background:#334455"><svg style="background:#556677"></svg></body></html>', '#334455'],
    ['<html style="background:navy"><body style="background:transparent"><svg></svg></body></html>', 'navy'],
    ['<style>html, body {background:#111111}body{background:#222222}</style><svg></svg>', '#222222'],
    ['<style>body{background:#111111!important}</style><body style="background:#222222"><svg></svg></body>', '#111111'],
    ['<style>body{background:#111111!important}</style><body style="background:#222222!important"><svg></svg></body>', '#222222'],
    ['<style>svg{background:#111111;background-color:#222222}</style><svg></svg>', '#222222'],
    ['<style>:root{background:#112233}html{background:#445566}</style><svg></svg>', '#112233'],
    ['<style>@media print{body{background:red}}@layer author{svg{background:blue}}svg{background:#123456}</style><svg></svg>', '#123456'],
    ['<style media="print">body{background:red}</style><svg></svg>', undefined],
    ['<style>body{background:#123456;background:var(--unavailable)}</style><svg></svg>', undefined],
    ['<style>svg{background:#123456;background-image:linear-gradient(red,blue)}</style><svg></svg>', undefined],
    ['<!-- <body style="background:red"> --><script>const text = \'<body style="background:blue">\'</script><body style="background:#123456"><svg></svg></body>', '#123456'],
    ['<svg style="background:red"></svg><svg style="background:blue"></svg>', undefined],
  ])('uses only an unambiguous fixed canvas color: %s', (html, expected) => {
    expect(pelicanPreviewBackground(html)).toBe(expected)
  })

  it.each([
    'transparent', 'currentColor', 'var(--color)', 'inherit', 'Canvas',
    'url(https://example.invalid/background.png)', 'linear-gradient(red, blue)',
    'rgba(1, 2, 3, .5)', '#1234', '#11223344',
  ])('does not turn relative, transparent or resource backgrounds into player styles: %s', background => {
    expect(pelicanPreviewBackground(`<svg style="background:${background}"></svg>`)).toBeUndefined()
  })

  it('keeps inspected output inert and rejects oversized documents', () => {
    const html = '<body style="background:#123456"><img id="pelican-inert-image" src="https://example.invalid/test.png"><iframe src="https://example.invalid/frame"></iframe><svg onload="throw new Error()"></svg><script>throw new Error()</script></body>'
    expect(pelicanPreviewBackground(html)).toBe('#123456')
    expect(document.querySelector('#pelican-inert-image')).toBeNull()
    expect(pelicanPreviewBackground(' '.repeat(2_000_001))).toBeUndefined()
  })
})
