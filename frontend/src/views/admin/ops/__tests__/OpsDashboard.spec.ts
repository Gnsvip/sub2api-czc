import { flushPromises, shallowMount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import OpsDashboard from '../OpsDashboard.vue'

const { api, settings, replace, pause, resume } = vi.hoisted(() => ({
  api: {
    getAdvancedSettings: vi.fn(), getMetricThresholds: vi.fn(),
    getDashboardSnapshotV2: vi.fn(), getThroughputTrend: vi.fn(),
    getBillingOutboxHealth: vi.fn(), getWorkerRuntimeStatus: vi.fn(),
    getLatencyHistogram: vi.fn(), getErrorDistribution: vi.fn(),
  },
  settings: { fetch: vi.fn(), opsMonitoringEnabled: true, opsQueryModeDefault: 'auto' },
  replace: vi.fn(), pause: vi.fn(), resume: vi.fn(),
}))
vi.mock('@/api/admin/ops', () => ({ opsAPI: api, default: api }))
vi.mock('@/stores', () => ({ useAdminSettingsStore: () => settings, useAppStore: () => ({ showError: vi.fn() }) }))
vi.mock('vue-router', async () => ({ ...await vi.importActual('vue-router'), useRoute: () => ({ query: {} }), useRouter: () => ({ replace }) }))
vi.mock('vue-i18n', async () => ({ ...await vi.importActual('vue-i18n'), useI18n: () => ({ t: (key: string) => key }) }))
vi.mock('@vueuse/core', async () => ({
  ...await vi.importActual('@vueuse/core'), useIntervalFn: () => ({ pause, resume })
}))
function deferred<T>() {
  let resolve!: (value: T) => void
  const promise = new Promise<T>(done => { resolve = done })
  return { promise, resolve }
}
function mountDashboard() {
  return shallowMount(OpsDashboard, { global: { stubs: { AppLayout: { template: '<div><slot /></div>' } } } })
}
beforeEach(() => {
  vi.clearAllMocks()
  settings.opsMonitoringEnabled = true
  settings.fetch.mockResolvedValue(undefined)
  api.getAdvancedSettings.mockResolvedValue({ display_alert_events: true, auto_refresh_enabled: false, auto_refresh_interval_seconds: 30 })
  api.getMetricThresholds.mockResolvedValue(null)
  api.getDashboardSnapshotV2.mockResolvedValue({ overview: { marker: 'ready' }, throughput_trend: null, error_trend: null })
  api.getThroughputTrend.mockResolvedValue({ points: [] })
  api.getBillingOutboxHealth.mockResolvedValue(null)
  api.getWorkerRuntimeStatus.mockResolvedValue(null)
  api.getLatencyHistogram.mockResolvedValue(null)
  api.getErrorDistribution.mockResolvedValue(null)
})
describe('OpsDashboard initial loading', () => {
  it('renders core data without waiting for presentation settings or runtime health', async () => {
    const preferences = deferred<unknown>()
    const health = deferred<unknown>()
    api.getAdvancedSettings.mockReturnValue(preferences.promise)
    api.getBillingOutboxHealth.mockReturnValue(health.promise)
    const wrapper = mountDashboard()
    await flushPromises()
    expect(api.getDashboardSnapshotV2).toHaveBeenCalledTimes(1)
    expect(wrapper.findComponent({ name: 'OpsDashboardSkeleton' }).exists()).toBe(false)
    expect(wrapper.getComponent({ name: 'OpsDashboardHeader' }).props('overview')).toEqual({marker:'ready'})
    wrapper.unmount()
    preferences.resolve({ auto_refresh_enabled: true, auto_refresh_interval_seconds: 1 })
    health.resolve({ pending: 0 })
    await flushPromises()
    expect(resume).not.toHaveBeenCalled()
  })
  it('does not fetch data when monitoring is disabled', async () => {
    settings.opsMonitoringEnabled = false
    const wrapper = mountDashboard()
    await flushPromises()
    expect(replace).toHaveBeenCalledWith('/admin/settings')
    expect(api.getDashboardSnapshotV2).not.toHaveBeenCalled()
    expect(api.getAdvancedSettings).not.toHaveBeenCalled()
    wrapper.unmount()
  })
  it('does not start requests after navigating away during permission loading', async () => {
    const permission = deferred<void>()
    settings.fetch.mockReturnValue(permission.promise)
    const wrapper = mountDashboard()
    wrapper.unmount()
    permission.resolve()
    await flushPromises()
    expect(api.getDashboardSnapshotV2).not.toHaveBeenCalled()
  })
})
