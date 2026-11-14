import { enableAutoUnmount, flushPromises, mount } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import UserApiKeysModal from '../UserApiKeysModal.vue'
import type { AdminUser } from '@/types'

const { getKeys, enableApiKey, showError, showSuccess } = vi.hoisted(() => ({
  getKeys: vi.fn(), enableApiKey: vi.fn(), showError: vi.fn(), showSuccess: vi.fn(),
}))
vi.mock('@/api/admin', () => ({ adminAPI: {
  users: { getUserApiKeys: getKeys }, groups: { getAll: vi.fn().mockResolvedValue([]) },
  apiKeys: { enableApiKey },
} }))
vi.mock('@/stores/app', () => ({ useAppStore: () => ({ showError, showSuccess }) }))
vi.mock('@/utils/format', () => ({ formatDateTime: (value: string) => value }))
vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))
enableAutoUnmount(afterEach)
beforeEach(() => {
  getKeys.mockReset(); enableApiKey.mockReset(); showError.mockReset(); showSuccess.mockReset()
  vi.spyOn(console, 'error').mockImplementation(() => {})
})
afterEach(() => vi.restoreAllMocks())
function deferred() {
  let resolve!: (value: unknown) => void
  let reject!: (error: Error) => void
  const promise = new Promise((res, rej) => { resolve = res; reject = rej })
  return { promise, resolve, reject }
}
const user = (id: number) => ({ id, email: `user${id}@example.com`, username: `user${id}`, status: 'active' }) as AdminUser
const keys = (id: number, name: string) => ({ items: [{ id, name, key: 'sk-example-key-value-for-tests', status: 'active', created_at: '2026-09-20', group_id: null }] })
async function open() {
  const wrapper = mount(UserApiKeysModal, {
    props: { show: false, user: user(1) },
    global: { stubs: {
      BaseDialog: { props: ['show'], template: '<div v-if="show"><slot /></div>' },
      GroupBadge: true, GroupOptionItem: true,
    } },
  })
  await wrapper.setProps({ show: true })
  return wrapper
}
async function switchUser(wrapper: Awaited<ReturnType<typeof open>>) {
  await wrapper.setProps({ show: false })
  await wrapper.setProps({ show: true, user: user(2) })
}

describe('user API key loading', () => {
  it('does not display the previous user keys when the next load fails', async () => {
    getKeys.mockResolvedValueOnce(keys(1, 'first-user-key')).mockRejectedValueOnce(new Error('unavailable'))
    const wrapper = await open(); await flushPromises()
    expect(wrapper.text()).toContain('first-user-key')
    await switchUser(wrapper); await flushPromises()
    expect(wrapper.text()).toContain('user2@example.com')
    expect(wrapper.text()).not.toContain('first-user-key')
  })

  it('does not replace current keys with a late previous response', async () => {
    const old = deferred()
    getKeys.mockReturnValueOnce(old.promise).mockResolvedValueOnce(keys(2, 'current-user-key'))
    const wrapper = await open()
    await switchUser(wrapper); await flushPromises()
    old.resolve(keys(1, 'old-user-key')); await flushPromises()
    expect(wrapper.text()).toContain('current-user-key')
    expect(wrapper.text()).not.toContain('old-user-key')
  })

  it('keeps the current request loading when an obsolete request fails', async () => {
    const old = deferred(); const current = deferred()
    getKeys.mockReturnValueOnce(old.promise).mockReturnValueOnce(current.promise)
    const wrapper = await open()
    await switchUser(wrapper)
    old.reject(new Error('obsolete')); await flushPromises()
    expect(wrapper.find('.animate-spin').exists()).toBe(true)
    current.resolve(keys(2, 'current-user-key')); await flushPromises()
    expect(wrapper.text()).toContain('current-user-key')
    expect(wrapper.find('.animate-spin').exists()).toBe(false)
  })

  it('loads keys when the selected user changes while the dialog is open', async () => {
    getKeys.mockResolvedValueOnce(keys(1, 'first-user-key')).mockResolvedValueOnce(keys(2, 'second-user-key'))
    const wrapper = await open(); await flushPromises()
    await wrapper.setProps({ user: user(2) }); await flushPromises()
    expect(getKeys).toHaveBeenLastCalledWith(2)
    expect(wrapper.text()).toContain('second-user-key')
    expect(wrapper.text()).not.toContain('first-user-key')
  })
})

describe('admin API key enablement', () => {
  it.each(['inactive', 'disabled'])('enables only the selected %s key without unbanning its user', async (status) => {
    const first = { ...keys(11, 'selected-key').items[0], status }
    const second = { ...keys(12, 'other-key').items[0], status: 'disabled' }
    getKeys.mockResolvedValue({ items: [first, second] })
    enableApiKey.mockResolvedValue({ ...first, status: 'active' })
    const wrapper = await open(); await flushPromises()
    const banned = { ...user(1), status: 'disabled' } as AdminUser
    await wrapper.setProps({ user: banned })
    expect(wrapper.text()).toContain('admin.users.apiKeyEnableBannedHint')
    await wrapper.findAll('[data-testid="admin-key-enable"]')[0].trigger('click')
    await flushPromises()
    expect(enableApiKey).toHaveBeenCalledExactlyOnceWith(11)
    expect(wrapper.findAll('[data-testid="admin-key-enable"]')).toHaveLength(1)
    expect(wrapper.text()).toContain('active')
    expect(wrapper.text()).toContain('disabled')
    expect(banned.status).toBe('disabled')
    expect(showSuccess).toHaveBeenCalledWith('admin.users.apiKeyEnabled')
  })

  it('does not offer enablement for active, expired or exhausted keys', async () => {
    getKeys.mockResolvedValue({ items: ['active', 'expired', 'quota_exhausted'].map((status, id) => ({ ...keys(id, status).items[0], status })) })
    const wrapper = await open(); await flushPromises()
    expect(wrapper.find('[data-testid="admin-key-enable"]').exists()).toBe(false)
    expect(wrapper.text()).not.toContain('admin.users.apiKeyEnableBannedHint')
  })

  it('prevents duplicate submits and concurrent edits, and allows retry after failure', async () => {
    getKeys.mockResolvedValue({ items: [{ ...keys(11, 'selected-key').items[0], status: 'disabled' }] })
    const pending = deferred()
    enableApiKey.mockReturnValueOnce(pending.promise)
    const wrapper = await open(); await flushPromises()
    const button = wrapper.get('[data-testid="admin-key-enable"]')
    await button.trigger('click')
    expect((button.element as HTMLButtonElement).disabled).toBe(true)
    expect((wrapper.get('[data-testid="admin-key-concurrency"]').element as HTMLInputElement).disabled).toBe(true)
    await button.trigger('click')
    expect(enableApiKey).toHaveBeenCalledTimes(1)
    pending.reject({ message: 'enable failed' } as Error); await flushPromises()
    expect(showError).toHaveBeenCalledWith('enable failed')
    expect(showSuccess).not.toHaveBeenCalled()
    expect(wrapper.text()).toContain('disabled')
    expect((button.element as HTMLButtonElement).disabled).toBe(false)
    enableApiKey.mockResolvedValue(keys(11, 'selected-key').items[0])
    await button.trigger('click'); await flushPromises()
    expect(enableApiKey).toHaveBeenCalledTimes(2)
    expect(wrapper.find('[data-testid="admin-key-enable"]').exists()).toBe(false)
  })

  it('ignores a late enable response after switching users', async () => {
    getKeys.mockResolvedValueOnce({ items: [{ ...keys(11, 'old-user-key').items[0], status: 'disabled' }] })
      .mockResolvedValueOnce(keys(22, 'current-user-key'))
    const pending = deferred()
    enableApiKey.mockReturnValue(pending.promise)
    const wrapper = await open(); await flushPromises()
    await wrapper.get('[data-testid="admin-key-enable"]').trigger('click')
    await switchUser(wrapper); await flushPromises()
    pending.resolve(keys(11, 'old-user-key').items[0]); await flushPromises()
    expect(wrapper.text()).toContain('current-user-key')
    expect(wrapper.text()).not.toContain('old-user-key')
    expect(showSuccess).not.toHaveBeenCalled()
  })
})
