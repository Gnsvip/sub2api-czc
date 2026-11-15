import { flushPromises, mount } from '@vue/test-utils'
import { describe, expect, it, vi } from 'vitest'
import { createI18n } from 'vue-i18n'
import { createPinia, setActivePinia } from 'pinia'
import { usePelicanStore } from '@/stores/pelican'
import PelicanTestView from '../PelicanTestView.vue'
import { pelicanTestsAPI } from '@/api/pelicanTests'
import { pelicanPreviewViewport } from '@/utils/pelicanPreview'

vi.mock('@/api/pelicanTests', () => ({
  pelicanTestsAPI: { list: vi.fn(), visibility: vi.fn() }
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
      loadFailed: '加载失败'
    }
  }
}

function mountView() {
  // Production and tests use the CSP-safe runtime build, which accepts
  // precompiled message functions rather than compiling strings at runtime.
  const compiled = {
    zh: {
      nav: { pelicanTest: () => messages.zh.nav.pelicanTest },
      pelicanTest: Object.fromEntries(Object.entries(messages.zh.pelicanTest).map(([key, value]) => [key, () => value]))
    }
  }
  const i18n = createI18n({ legacy: false, locale: 'zh', messages: compiled })
  const pinia = createPinia()
  setActivePinia(pinia)
  usePelicanStore().setVisible(true)
  return mount(PelicanTestView, { global: { plugins: [i18n, pinia] } })
}

describe('PelicanTestView', () => {
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
    expect(pelicanTestsAPI.list).toHaveBeenCalledWith(1, 33)
    expect(wrapper.findAll('h1')).toHaveLength(1)
    expect(wrapper.get('h1').classes()).toContain('lg:hidden')
    expect(wrapper.findAll('iframe')).toHaveLength(1)
    expect(wrapper.get('[data-testid="pelican-details"]').attributes('open')).toBeUndefined()
    expect(wrapper.get('[data-testid="pelican-details"]').find('pre').exists()).toBe(true)
    expect(wrapper.find('iframe').attributes('srcdoc')).toContain('id="latest"')
    expect(wrapper.get('iframe').element.style.height).toBe('900px')
    expect(wrapper.get('iframe').element.style.width).toBe('1600px')
    expect(wrapper.get('[data-testid="pelican-animation-preview"]').classes()).toContain('aspect-[4/3]')
    expect(wrapper.get('iframe').classes()).toContain('bg-white')
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
