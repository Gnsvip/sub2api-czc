import { existsSync, readFileSync, readdirSync } from 'node:fs'
import { dirname, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'
import { describe, expect, it } from 'vitest'

const dir = dirname(fileURLToPath(import.meta.url))
const batPath = resolve(dir, '../../../public/downloads/select-fastest-codex-base-url.bat')
const guideDir = resolve(dir, '../../../../docs/guide')
const guideViewPath = resolve(dir, '../../views/public/GuideView.vue')
const vipPath = resolve(dir, '../../../../backend/internal/service/vip.go')

// The guide is one file per chapter; join them in filename order so the
// assertions below still inspect the whole published document.
const guideChapterFiles = readdirSync(guideDir)
  .filter((name) => /^\d{3}-[a-z0-9-]+\.md$/.test(name))
  .sort()
const guide = guideChapterFiles
  .map((name) => readFileSync(resolve(guideDir, name), 'utf8').replace(/\s+$/, ''))
  .join('\n\n')
const guideViewSource = readFileSync(guideViewPath, 'utf8')
const vipSource = readFileSync(vipPath, 'utf8')

describe('bundled AIFXO guide', () => {
  it('uses the AIFXO endpoint and does not ship the retired domain-switching tool', () => {
    expect(guide).toContain('https://api.aifxo.com/v1/models')
    expect(guide).toContain('base_url = "https://api.aifxo.com/v1"')
    expect(guide).toContain('https://api.aifxo.com/health')
    expect(guide).toContain('| 网站、登录和控制台入口 | `https://www.aifxo.com/` |')
    expect(guide).toContain('| API 根地址（客户端要求根地址时） | `https://api.aifxo.com` |')
    expect(guide).toContain('[AIFXO](https://www.aifxo.com/)')
    expect(guide).not.toContain('](https://api.aifxo.com)')
    expect(guide).not.toContain('输入登录信息或 API Key 前，确认地址属于 `api.aifxo.com`')
    expect(guide).not.toContain('/redeem-store')
    expect(guide).not.toContain('IGUZN6L4')
    expect(guide).toContain('QQ：1038539305')
    expect(guide).not.toMatch(/mofa\.love\.gd|mofayaoshipu\.cc\.cd|key66\.cc\.cd|key66\.vip|mofa\.duoduohuiying\.cn|15\.204\.82\.11/)
    expect(guide).not.toContain('select-fastest-codex-base-url.bat')
    expect(existsSync(batPath)).toBe(false)
    expect(existsSync(`${batPath}.sha256`)).toBe(false)
    expect(guideViewSource).not.toContain('guide-download')
  })

  it('keeps the bundled guide version in sync between the document and the view', () => {
    const documentVersion = guide.match(/^- 教程版本：`([^`]+)`$/m)?.[1]
    const viewVersion = guideViewSource.match(/^const bundledGuideVersion = '([^']+)'$/m)?.[1]

    // Both patterns must actually match; an absent version is a failure, not a passing match.
    expect(documentVersion).toBe('1.11')
    expect(viewVersion).toBe('1.11')
    expect(documentVersion).toBe(viewVersion)
    const documentDate = guide.match(/^- 更新日期：`([^`]+)`$/m)?.[1]
    const viewDate = guideViewSource.match(/^const bundledGuideUpdatedAt = '([^']+)'$/m)?.[1]
    expect(documentDate).toBe('2026-11-09')
    expect(viewDate).toBe(documentDate)
  })

  it('ships the guide as one file per chapter, each starting with its heading', () => {
    expect(guideChapterFiles).toHaveLength(16)

    for (const name of guideChapterFiles) {
      const content = readFileSync(resolve(guideDir, name), 'utf8')
      expect(content.startsWith('## '), `${name} must start with a ## heading`).toBe(true)
    }

    // The single-file guide must be gone so no build path can read a stale copy.
    expect(existsSync(resolve(guideDir, '../guide.zh.md'))).toBe(false)
  })

  it('keeps the published SVIP rules synchronized with backend constants', () => {
    const constants = {
      threshold: vipSource.match(/VipBalanceThreshold\s*=\s*([\d.]+)/)?.[1],
      model: vipSource.match(/VipExclusiveModelName\s*=\s*"([^"]+)"/)?.[1],
      lunaMin: vipSource.match(/LunaMinRateMultiplier\s*=\s*([\d.]+)/)?.[1],
    }

    expect(constants).toEqual({
      threshold: '100.0',
      model: 'gpt-5.6-luna',
      lunaMin: '0.2',
    })
    expect(vipSource).not.toMatch(/VipFrozenReserve/)
    expect(guide).toContain('总余额（含赠送）达到 **100**')
    expect(guide).toContain('页面单位为美元')
    expect(guide).toContain('不扣押金')
    expect(guide).not.toContain('冻结 50 元')
    expect(guide).toContain('普通用户与 SVIP 使用相同的计费倍率规则')
    expect(guide).toContain('管理员单独配置的用户倍率仍然有效')
    expect(guide).toContain('`gpt-5.6-luna`')
    expect(guide).toContain('所有用户都可以使用')
    expect(guide).toContain('低于 0.2')
    expect(guide).toContain('按 0.2 计费')
    expect(guide).toContain('普通用户与 SVIP 都先按 0.2 计费')
    expect(guide).toContain('0.2 是 Luna 的基础倍率保底，也是叠加高峰因子后的 token 计费倍率下限')
    expect(guide).not.toContain('专属模型')
  })
})
