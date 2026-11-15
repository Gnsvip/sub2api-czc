import { flushPromises, mount } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import GuideView from '@/views/public/GuideView.vue'
import CustomGuideView from '@/views/public/CustomGuideView.vue'
import type { PublicGuide } from '@/api/guide'
import {
  buildGuideDocument,
  buildGuideDocumentFromChapters,
  deriveChapterSlug,
  extractGuideCommands,
  joinGuideChapters,
  splitGuideIntoChapters,
} from '@/utils/guideMarkdown'
const guideChapters = [
  { slug: 'setup', title: '自定义接入步骤', content: '## 自定义接入步骤\n\n管理员保存的操作说明。' },
  {
    slug: 'commands', title: '自定义命令',
    content: '## 自定义命令\n\n<!-- copy-command:skill-install -->\n```text\n管理员提供的示例命令\n```',
  },
  { slug: 'faq', title: '自定义常见问题', content: '## 自定义常见问题\n\n[帮助](https://example.test/help)' },
]
const guideMarkdown = joinGuideChapters(guideChapters)
const customGuide: PublicGuide = {
  display_mode: 'custom', content: guideMarkdown, chapters: guideChapters,
  version: 7, updated_at: '2026-11-15T12:00:00Z', has_custom_content: true,
}

const copyToClipboard = vi.hoisted(() => vi.fn().mockResolvedValue(true))
const getPublicGuide = vi.hoisted(() => vi.fn())

vi.mock('@/api/guide', () => ({ getPublicGuide }))

vi.mock('@/stores/auth', () => ({
  useAuthStore: () => ({ isAuthenticated: false, isAdmin: false }),
}))

vi.mock('@/composables/useClipboard', () => ({
  useClipboard: () => ({ copyToClipboard, copied: { value: false } }),
}))

describe('GuideView', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    copyToClipboard.mockClear()
    getPublicGuide.mockReset()
    getPublicGuide.mockResolvedValue({
      content: '',
      version: 0,
      updated_at: '',
      has_custom_content: false,
    })
    window.history.replaceState({}, '', '/guide')
    window.__APP_CONFIG__ = {
      site_name: '测试站点',
      site_logo: '',
      doc_url: '',
    } as typeof window.__APP_CONFIG__
  })

  it('renders only published database chapters after loading without a bundled-content flash', async () => {
    getPublicGuide.mockResolvedValue({
      content: '',
      chapters: [{
        slug: 'admin-chapter',
        title: '管理员发布的教程',
        content: '## 管理员发布的教程\n\n这是数据库中的内容。',
      }],
      version: 7,
      updated_at: '2026-08-30T12:00:00Z',
      has_custom_content: true,
      display_mode: 'custom',
    })

    const wrapper = mount(GuideView, {
      global: {
        stubs: {
          RouterLink: {
            props: ['to'],
            template: '<a :href="to"><slot /></a>',
          },
        },
      },
    })
    expect(wrapper.find('[data-test="guide-content"]').exists()).toBe(false)
    expect(wrapper.text()).not.toContain('旧的整篇内容')
    await flushPromises()

    expect(wrapper.get('[data-test="guide-content"]').text()).toContain('这是数据库中的内容。')
    expect(wrapper.text()).toContain('教程版本 在线第 7 版')
    // The published chapter slug becomes the anchor id.
    expect(wrapper.get('#admin-chapter').text()).toBe('管理员发布的教程')
    expect(getPublicGuide).toHaveBeenCalledTimes(1)
  })

  it('splits a guide published before chapters existed and keeps its anchors', async () => {
    getPublicGuide.mockResolvedValue({
      content: '## 充值：先买兑换码，再兑换余额\n\n旧的整篇内容。\n\n## 遇到问题先看这里\n\n旧的 FAQ。',
      version: 4,
      updated_at: '2026-08-30T12:00:00Z',
      has_custom_content: true,
      display_mode: 'custom',
    })

    const wrapper = mount(GuideView, {
      global: {
        stubs: {
          RouterLink: {
            props: ['to'],
            template: '<a :href="to"><slot /></a>',
          },
        },
      },
    })
    await flushPromises()

    expect(wrapper.get('#recharge').text()).toBe('充值：先买兑换码，再兑换余额')
    expect(wrapper.get('#faq').text()).toBe('遇到问题先看这里')
    expect(wrapper.get('[data-test="guide-content"]').text()).toContain('旧的整篇内容。')
  })

  it('defaults to the modern bundled tutorial without replacing old stored chapters', async () => {
    getPublicGuide.mockResolvedValue({
      content: '## Stored draft\n\nOld content',
      version: 7,
      updated_at: '',
      has_custom_content: true,
    })
    window.history.replaceState({}, '', '/guide#streaming')
    const wrapper = mount(GuideView)
    await flushPromises()
    expect(wrapper.get('[data-test="aifxo-guide-frame"]').attributes('src')).toBe('/guide-content/#streaming')
    expect(wrapper.find('[data-test="guide-content"]').exists()).toBe(false)
    expect(getPublicGuide).toHaveBeenCalledTimes(1)
    wrapper.unmount()
  })

  it('keeps the bundled tutorial available when settings fail', async () => {
    getPublicGuide.mockRejectedValue(new Error('offline'))
    const wrapper = mount(GuideView)
    await flushPromises()
    expect(wrapper.get('[data-test="aifxo-guide-frame"]').attributes('src')).toBe('/guide-content/')
    wrapper.unmount()
  })

  it('accepts navigation only from its own same-origin tutorial frame', async () => {
    const wrapper = mount(GuideView, { attachTo: document.body })
    await flushPromises()
    const frame = wrapper.get('iframe').element as HTMLIFrameElement
    const data = { type: 'aifxo-guide-route', hash: '#streaming', title: '流式输出 · AIFXO' }
    window.dispatchEvent(new MessageEvent('message', { data, origin: 'https://untrusted.example', source: frame.contentWindow }))
    expect(window.location.hash).toBe('')
    window.dispatchEvent(new MessageEvent('message', { data, origin: window.location.origin, source: window }))
    expect(window.location.hash).toBe('')
    window.dispatchEvent(new MessageEvent('message', { data, origin: window.location.origin, source: frame.contentWindow }))
    expect(window.location.hash).toBe('#streaming')
    expect(document.title).toBe('流式输出 · AIFXO')
    wrapper.unmount()
  })

  it('preserves the order and permanent anchors of published custom chapters', () => {
    const { sections } = buildGuideDocumentFromChapters(guideChapters)
    expect(sections.map((section) => section.id)).toEqual(guideChapters.map((chapter) => chapter.slug))
    expect(sections.map((section) => section.label)).toEqual(guideChapters.map((chapter) => chapter.title))
  })

  it('anchors every chapter to its own slug even when one has no heading', () => {
    // Splitting a legacy guide yields a `preface` chapter with no `##` heading.
    // Mapping anchors by heading position shifted every later anchor by one, so
    // #recharge landed on the wrong section and the last slug vanished.
    const { html, sections } = buildGuideDocumentFromChapters([
      { slug: 'preface', title: '前言', content: '开头前言一段。' },
      { slug: 'recharge', title: '充值', content: '## 充值\n\n充值正文。' },
      { slug: 'faq', title: '常见问题', content: '## 常见问题\n\nFAQ 正文。' },
    ])

    // Headings map to their own chapter's slug, never a neighbour's.
    expect(html).toContain('<h2 id="recharge">充值</h2>')
    expect(html).toContain('<h2 id="faq">常见问题</h2>')
    // The headingless chapter still exposes a resolvable anchor.
    expect(html).toContain('id="preface"')
    expect(html).toContain('开头前言一段。')

    // Only chapters with headings appear in the table of contents.
    expect(sections).toEqual([
      { id: 'recharge', label: '充值' },
      { id: 'faq', label: '常见问题' },
    ])
  })

  it('keeps external links safe when rendering from chapters', () => {
    const { html } = buildGuideDocumentFromChapters([
      {
        slug: 'links',
        title: '链接',
        content: '## 链接\n\n<script>alert(1)</script>\n\n[外部](https://example.test/x)\n\n[危险](javascript:alert(2))',
      },
    ])

    expect(html).not.toContain('<script')
    expect(html).not.toContain('javascript:')
    expect(html).toContain('target="_blank"')
    expect(html).toContain('rel="noopener noreferrer"')
  })

  it('round-trips a single document through split and join', () => {
    const document = '前言段落。\n\n## 第一章\n\n正文一。\n\n## 第二章\n\n正文二。'
    const chapters = splitGuideIntoChapters(document)

    expect(chapters).toHaveLength(3)
    expect(chapters[0].slug).toBe('preface')
    expect(chapters[0].content).toContain('前言段落。')
    expect(chapters.map((chapter) => chapter.title)).toEqual(['前言', '第一章', '第二章'])
    // Chinese-only headings fall back to deterministic digest slugs.
    expect(chapters[1].slug).toMatch(/^section-[a-z0-9]+$/)
    expect(chapters[2].slug).not.toBe(chapters[1].slug)

    const rejoined = joinGuideChapters(chapters)
    for (const fragment of ['前言段落。', '正文一。', '正文二。']) {
      expect(rejoined).toContain(fragment)
    }
  })

  it('derives deterministic, collision-free slugs for new chapters', () => {
    expect(deriveChapterSlug('SVIP 说明')).toBe('svip')
    expect(deriveChapterSlug('Billing & Quota')).toBe('billing-quota')
    expect(deriveChapterSlug('SVIP 说明', ['svip'])).toBe('svip-2')

    // A title without ASCII words still yields a stable, repeatable slug.
    const chineseOnly = deriveChapterSlug('全新章节')
    expect(chineseOnly).toMatch(/^section-[a-z0-9]+$/)
    expect(deriveChapterSlug('全新章节')).toBe(chineseOnly)
    expect(deriveChapterSlug('另一章节')).not.toBe(chineseOnly)
  })

  it('renders custom chapters and copies only commands supplied in that publication', async () => {
    const wrapper = mount(CustomGuideView, {
      props: { published: customGuide },
      global: {
        stubs: {
          RouterLink: {
            props: ['to'],
            template: '<a :href="to"><slot /></a>',
          },
        },
      },
    })

    expect(wrapper.get('#setup').text()).toBe('自定义接入步骤')
    expect(wrapper.get('#commands').text()).toBe('自定义命令')
    expect(wrapper.get('#faq').text()).toBe('自定义常见问题')
    expect(wrapper.get('[data-test="guide-content"]').text()).toContain('管理员保存的操作说明。')
    expect(wrapper.text()).toContain('在线第 7 版')
    expect(wrapper.text()).toContain('2026-11-15')
    expect(wrapper.find('[data-test="guide-download"]').exists()).toBe(false)

    const commands = extractGuideCommands(guideMarkdown)
    expect(commands.map((command) => command.id)).toEqual(['skill-install'])

    await wrapper.get('[data-test="copy-skill-install"]').trigger('click')
    expect(copyToClipboard).toHaveBeenCalledWith(
      '管理员提供的示例命令',
      '命令已复制'
    )
    expect(wrapper.get('[aria-live="polite"]').text()).toBe('安装 goal-workflow 已复制')
    await wrapper.get('[data-test="mobile-toc-toggle"]').trigger('click')
    expect(wrapper.get('#mobile-guide-toc').findAll('a')).toHaveLength(3)
    await wrapper.get('#mobile-guide-toc a[href="#faq"]').trigger('click')
    expect(wrapper.find('#mobile-guide-toc').exists()).toBe(false)
    expect(getPublicGuide).not.toHaveBeenCalled()
    wrapper.unmount()
  })

  it.each([undefined, { ...customGuide, display_mode: 'bundled' as const },
    { ...customGuide, has_custom_content: false },
    { ...customGuide, chapters: [], content: '' }])('does not render fallback content without an active custom publication (%j)', async (published) => {
    const wrapper = mount(CustomGuideView, { props: { published } })
    expect(wrapper.find('[data-test="guide-view"]').exists()).toBe(false)
    await flushPromises()
    expect(wrapper.html()).not.toContain('管理员保存的操作说明')
    expect(wrapper.find('[data-test="guide-content"]').exists()).toBe(false)
    expect(getPublicGuide).not.toHaveBeenCalled()
    wrapper.unmount()
  })

  it('updates from its supplied publication and hides retained drafts when switched to bundled mode', async () => {
    const wrapper = mount(CustomGuideView, {
      props: { published: customGuide },
      global: { stubs: { RouterLink: true } },
    })
    expect(wrapper.get('#setup').text()).toBe('自定义接入步骤')
    await wrapper.setProps({ published: { ...customGuide, display_mode: 'bundled' } })
    expect(wrapper.find('[data-test="guide-content"]').exists()).toBe(false)
    await wrapper.setProps({ published: {
      ...customGuide, chapters: [{ slug: 'new', title: '新稿', content: '## 新稿\n\n更新后的内容' }],
      version: 8, updated_at: '',
    } })
    expect(wrapper.get('#new').text()).toBe('新稿')
    expect(wrapper.find('#setup').exists()).toBe(false)
    expect(wrapper.text()).toContain('在线第 8 版')
    expect(wrapper.text()).not.toContain('更新日期')
    expect(getPublicGuide).not.toHaveBeenCalled()
    wrapper.unmount()
  })

  it('sanitizes Markdown HTML and secures external links', () => {
    const document = buildGuideDocument(
      '## 标题\n\n<script>alert(1)</script><a href="javascript:alert(2)">危险</a>\n\n[外部](https://example.test/help)'
    )

    expect(document.html).not.toContain('<script')
    expect(document.html).not.toContain('javascript:')
    expect(document.html).toContain('target="_blank"')
    expect(document.html).toContain('rel="noopener noreferrer"')
    expect(document.sections).toEqual([{ id: 'section-1', label: '标题' }])
  })

  it('keeps historical custom-document anchors without retaining an obsolete bundled document', () => {
    const { sections } = buildGuideDocument('## 错误码含义与处理方案\n\n管理员自己的错误说明。')
    expect(sections).toEqual([{ id: 'error-codes', label: '错误码含义与处理方案' }])
  })
})
