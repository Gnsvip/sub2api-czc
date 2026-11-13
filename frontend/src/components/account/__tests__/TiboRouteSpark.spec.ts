import { describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'
import TiboRouteSpark from '../TiboRouteSpark.vue'
import type { TiboRouteVerdict } from '@/types'
import zh from '@/i18n/locales/zh/admin/accounts'
import en from '@/i18n/locales/en/admin/accounts'

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({
      t: (key: string) => key,
    }),
  }
})

describe.each([true, false])('TiboRouteSpark compact=%s', (compact) => {
  it.each([0, 10, 24, 29])('renders the latest 24 slots from %s valid samples', (count) => {
    const valid = Array.from({ length: count }, (_, index) => ({
      verdict: (index % 2 === 0 ? 'healthy' : 'degraded') as TiboRouteVerdict,
      at: new Date(Date.UTC(2026, 10, 2, 0, index)).toISOString(),
    }))
    const samples: { verdict: TiboRouteVerdict; at?: string }[] = valid.flatMap(sample => [sample, { verdict: 'unknown' as const }])
    samples.push({ verdict: 'unavailable' })
    const wrapper = mount(TiboRouteSpark, {
      props: {
        model: 'gpt-6-astra',
        verdict: 'healthy',
        samples,
        compact,
      },
    })
    expect(wrapper.text()).toContain('astra')
    expect(wrapper.text()).not.toContain('admin.accounts.openai.codexTiboRouteVerdict.healthy')
    expect(wrapper.text()).not.toContain('admin.accounts.openai.codexTiboRouteVerdict.unknown')
    const dots = wrapper.findAll('[data-testid="tibo-dot"]')
    expect(dots).toHaveLength(24)
    const recent = valid.slice(-24)
    const padding = 24 - recent.length
    expect(dots.map(dot => dot.attributes('data-verdict'))).toEqual([
      ...Array.from({ length: padding }, () => 'empty'),
      ...recent.map(sample => sample.verdict),
    ])
    dots.forEach((dot, index) => {
      expect(dot.classes()).toContain(compact ? 'h-1.5' : 'h-2')
      if (index < padding) {
        expect(dot.classes()).toContain('bg-gray-200')
        expect(dot.attributes('title')).toBe('admin.accounts.openai.codexTiboSampleEmpty')
      } else {
        const sample = recent[index - padding]
        expect(dot.classes()).toContain(sample.verdict === 'healthy' ? 'bg-emerald-500' : 'bg-red-500')
        expect(dot.attributes('title')).toBe(`admin.accounts.openai.codexTiboRouteVerdict.${sample.verdict} · ${sample.at}`)
      }
    })
    wrapper.unmount()
  })
})

it('describes 24 Tibo samples and xhigh probes in both locales', () => {
  expect(zh.accounts.openai.codexTiboRecentSamples).toBe('最近 24 次 Tibo 探测')
  expect(en.accounts.openai.codexTiboRecentSamples).toBe('Last 24 Tibo probes')
  for (const locale of [zh, en]) {
    expect(locale.accounts.openai.codexTiboRouteDesc).toContain('astra / xhigh')
  }
})

it('shows the newest timestamp and emits interval/default actions without changing the switch', async () => {
  const wrapper = mount(TiboRouteSpark, { props: { model: 'gpt-6-astra', verdict: 'healthy', probeEnabled: false, probeIntervalMinutes: 30,
    lastSampleAt: '2026-11-13T02:00:00Z', samples: [{ verdict: 'healthy', at: '2026-11-12T01:00:00Z' }, { verdict: 'healthy', at: 'invalid' }] } })
  expect(wrapper.get('[data-testid="astra-probe-time"]').attributes('datetime')).toBe('2026-11-13T02:00:00Z')
  await wrapper.get('[data-testid="astra-probe-interval"]').trigger('click')
  await wrapper.get('[data-testid="astra-probe-interval-input"]').setValue(0)
  await wrapper.get('form').trigger('submit')
  expect(wrapper.emitted('set-interval')).toBeUndefined()
  await wrapper.get('[data-testid="astra-probe-interval-input"]').setValue(1)
  await wrapper.get('form').trigger('submit')
  await wrapper.get('[data-testid="astra-probe-interval-default"]').trigger('click')
  expect(wrapper.emitted('set-interval')).toEqual([[1], [0]])
  expect(wrapper.emitted('toggle-probe')).toBeUndefined()
  wrapper.unmount()
})
