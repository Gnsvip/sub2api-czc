import { existsSync, readFileSync, readdirSync } from 'node:fs'
import { dirname, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'
import { describe, expect, it } from 'vitest'

const dir = dirname(fileURLToPath(import.meta.url))
const batPath = resolve(dir, '../../../public/downloads/select-fastest-codex-base-url.bat')
const legacyGuideDir = resolve(dir, '../../../../docs/guide')
const modernGuideDir = resolve(dir, '../../../guide-content')
const guide = readFileSync(resolve(modernGuideDir, 'index.html'), 'utf8')
const snippets = readFileSync(resolve(modernGuideDir, 'snippets.js'), 'utf8')
const imageSource = readFileSync(resolve(modernGuideDir, 'images.js'), 'utf8')
const document = new DOMParser().parseFromString(guide, 'text/html')

describe('bundled AIFXO guide', () => {
  it('ships the approved fifteen-page tutorial with external scripts and production downloads', () => {
    expect([...document.querySelectorAll('article.page')].map((page) => page.id)).toEqual([
      'overview', 'start', 'codex', 'tools', 'openclaw', 'kilo', 'claude',
      'api-responses', 'api-chat', 'api-messages', 'streaming', 'tool-calls', 'usage', 'faq', 'support',
    ])
    expect(guide).toContain('<script type="module" src="./main.js"></script>')
    expect(document.querySelectorAll('script:not([src])')).toHaveLength(0)
    expect(guide).not.toMatch(/127\.0\.0\.1|localhost|本地预览|HTML 预览版/)
    expect(guide).toContain('https://github.com/Gnsvip/sub2api-czc/releases/download/czc-v2026.11.15.4/AIFXO-Config-1.2.0-Windows-x64.zip')
    expect(document.getElementById('codex-auth-file')).not.toBeNull()
    expect(guide).toContain('特别注意：不要添加')
    expect(guide).toContain('Microsoft Store')
    expect(guide).toContain('response.completed')
    expect(snippets).toContain('https://api.aifxo.com/v1/responses')
    expect(guide).toContain('https://api.aifxo.com/v1/messages')
    expect(snippets).toContain('https://api.aifxo.com/v1/usage')
  })

  it('separates website navigation from API examples and removes retired downloads and shops', () => {
    expect(document.querySelector('.top-actions .btn')?.getAttribute('href')).toBe('https://www.aifxo.com/')
    for (const address of ['https://www.aifxo.com/', 'https://api.aifxo.com', 'https://api.aifxo.com/v1']) {
      expect(document.querySelector(`[data-copy="${address}"]`)).not.toBeNull()
    }
    expect(snippets).toContain('base_url = "https://api.aifxo.com/v1"')
    for (const path of ['keys', 'usage', 'redeem', 'subscriptions']) {
      expect(document.querySelector(`a[href="https://www.aifxo.com/${path}"]`)).not.toBeNull()
    }
    expect(document.querySelector('a[href="https://api.aifxo.com"]')).toBeNull()
    expect(guide).toContain('1038539305')
    for (const content of [guide, snippets]) {
      expect(content).not.toMatch(/catfk\.com|mofa\.love\.gd|mofayaoshipu\.cc\.cd|key66\.cc\.cd|key66\.vip|mofa\.duoduohuiying\.cn|15\.204\.82\.11/)
      expect(content).not.toContain('/redeem-store')
      expect(content).not.toContain('IGUZN6L4')
      expect(content).not.toContain('select-fastest-codex-base-url.bat')
      expect(content).not.toContain('并发支付竞态')
      expect(content).not.toContain('没有支付但是到账')
    }
    expect(existsSync(batPath)).toBe(false)
    expect(existsSync(`${batPath}.sha256`)).toBe(false)
  })

  it('resolves every bundled module, screenshot and chapter link from the actual HTML guide', () => {
    for (const path of ['./main.js', './style.css', './snippets.js', './images.js', './images/site-logo.ico']) {
      expect(existsSync(resolve(modernGuideDir, path)), path).toBe(true)
    }
    const images = new Map([...imageSource.matchAll(/"([^"]+)":\s*\{\s*src: new URL\("([^"]+)"/g)]
      .map((match) => [match[1], match[2]] as const))
    expect(images.size).toBeGreaterThan(0)
    for (const [id, path] of images) {
      expect(path.startsWith('./images/'), id).toBe(true)
      expect(existsSync(resolve(modernGuideDir, path)), id).toBe(true)
    }
    for (const gallery of document.querySelectorAll<HTMLElement>('[data-gallery]')) {
      const ids = gallery.dataset.gallery!.split(',')
      expect(gallery.dataset.captions?.split('|')).toHaveLength(ids.length)
      for (const id of ids) expect(images.has(id), `missing gallery image ${id}`).toBe(true)
    }
    for (const link of document.querySelectorAll<HTMLAnchorElement>('a[href^="#"]')) {
      expect(document.getElementById(link.getAttribute('href')!.slice(1)), link.outerHTML).not.toBeNull()
    }
    for (const block of document.querySelectorAll<HTMLElement>('[data-code]')) {
      const key = block.dataset.code!
      expect(snippets.includes(`'${key}':`) || snippets.includes(`"${key}":`), `missing code snippet ${key}`).toBe(true)
    }
  })

  it('keeps published streaming and error guidance tied to the current tutorial', () => {
    const streaming = document.getElementById('streaming')!.textContent!
    expect(streaming).toContain('response.completed')
    expect(streaming).toContain('message_stop')
    expect(streaming).toContain('[DONE]')
    expect(streaming).toContain('错误')
    const faq = document.getElementById('faq')!.textContent!
    for (const code of ['400', '401', '403', '404', '429', '502']) expect(faq).toContain(code)
    expect(faq).toContain('请求 ID')
    expect(faq).toContain('不要执行删除')
    expect(faq).toContain('完整密钥')
  })

  it('removes the old sixteen Markdown chapters and their build-time loader', () => {
    const legacyFiles = existsSync(legacyGuideDir) ? readdirSync(legacyGuideDir) : []
    expect(legacyFiles.filter((name) => /^\d{3}-[a-z0-9-]+\.md$/.test(name))).toEqual([])
    expect(existsSync(resolve(legacyGuideDir, '../guide.zh.md'))).toBe(false)
    expect(existsSync(resolve(dir, '../guideSections.ts'))).toBe(false)
    const customView = readFileSync(resolve(dir, '../../views/public/CustomGuideView.vue'), 'utf8')
    expect(customView).not.toMatch(/getBundledGuideChapters|bundledGuideVersion|bundledGuideUpdatedAt/)
  })
})
