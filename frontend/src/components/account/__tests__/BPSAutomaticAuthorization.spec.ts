import BPSAccountSettings from '../BPSAccountSettings.vue'
import { readBPSSettings, initialBPSDefaults, bpsDefaultsPayload } from '../bpsSettings'
import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import BPSAutomaticAuthorization from '../BPSAutomaticAuthorization.vue'

const api = vi.hoisted(() => ({ get: vi.fn(), put: vi.fn(), post: vi.fn() }))
vi.mock('@/api/client', () => ({ default: api }))
const status = () => ({ config: { enabled: false, login_email: 'user@example.com', password_configured: false, totp_configured: false, revision: 0 }, task: null, runtime: { state: 'idle' } })
beforeEach(() => { vi.resetAllMocks(); api.get.mockResolvedValue({ data: status() }) })

describe('BPS automatic authorization', () => {
  it('loads only when opened and clears submitted passwords', async () => {
    const wrapper = mount(BPSAutomaticAuthorization, { props: { accountId: 7 } })
    expect(api.get).not.toHaveBeenCalled()
    wrapper.get('details').element.open = true
    await wrapper.get('details').trigger('toggle'); await flushPromises()
    expect(api.get).toHaveBeenCalledWith('/admin/openai/accounts/7/excel-login')
    await wrapper.get('[aria-label="登录密码"]').setValue('private-password')
    api.put.mockResolvedValue({ data: { ...status(), config: { ...status().config, password_configured: true, revision: 1 } } })
    await wrapper.findAll('button')[0]!.trigger('click'); await flushPromises()
    expect(api.put).toHaveBeenCalledWith('/admin/openai/accounts/7/excel-login', expect.objectContaining({ password: 'private-password', enabled: false, revision: 0 }))
    expect((wrapper.get('[aria-label="登录密码"]').element as HTMLInputElement).value).toBe('')
    expect(wrapper.text()).not.toContain('private-password')
    wrapper.unmount()
  })
  it('does not apply a delayed response to a different account', async () => {
    let resolve!: (value: unknown) => void
    api.get.mockReturnValueOnce(new Promise(r => { resolve = r }))
    const wrapper = mount(BPSAutomaticAuthorization, { props: { accountId: 7 } })
    wrapper.get('details').element.open = true
    await wrapper.get('details').trigger('toggle')
    await wrapper.setProps({ accountId: 8 }); await flushPromises()
    resolve({ data: { ...status(), config: { ...status().config, login_email: 'old@example.com' } } }); await flushPromises()
    expect((wrapper.get('[aria-label="登录邮箱"]').element as HTMLInputElement).value).not.toBe('old@example.com')
    wrapper.unmount()
  })
})

it('does not apply a delayed default template after switching account forms', async () => {
 let resolve!: (value: unknown) => void
 api.get.mockReturnValueOnce(new Promise(r => { resolve=r }))
 const wrapper=mount(BPSAccountSettings,{props:{modelValue:readBPSSettings({}),groups:[]},global:{stubs:{ModelWhitelistSelector:true}}})
 await wrapper.findAll('button').find(button=>button.text()==='应用已保存模板')!.trigger('click')
 await wrapper.setProps({modelValue:readBPSSettings({openai_excel_bps_models:['other-account-model']})})
 resolve({data:bpsDefaultsPayload(initialBPSDefaults())});await flushPromises()
 expect(wrapper.emitted('update:modelValue')).toBeUndefined()
 expect(wrapper.findAll('button')[0]!.attributes('disabled')).toBeUndefined()
 wrapper.unmount()
})
