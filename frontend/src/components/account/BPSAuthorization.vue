<template>
  <div class="space-y-2 rounded-lg border border-gray-200 p-3 dark:border-dark-600">
    <p class="text-sm">Excel BPS 独立授权 <span v-if="state" class="text-gray-500">· {{ labels[state.status] || '未知' }}</span></p>
    <p class="text-xs text-gray-500">使用同一 ChatGPT 用户和工作区授权，原生 Codex 凭据保持不变。</p>
    <button type="button" class="btn btn-secondary" :disabled="busy" @click="begin">{{ busy ? '处理中…' : '授权 / 重新授权 BPS' }}</button>
    <template v-if="authorization">
      <a :href="authorization.auth_url" target="_blank" rel="noopener noreferrer" class="block text-sm text-primary-600">打开 Excel 授权页面</a>
      <input v-model="callback" class="input" autocomplete="off" placeholder="粘贴完整回调 URL" aria-label="Excel 授权回调 URL" />
      <button type="button" class="btn btn-primary" :disabled="busy || !callback" @click="complete">保存独立授权</button>
    </template>
    <BPSAutomaticAuthorization :account-id="accountId" />
    <p v-if="error" role="alert" class="text-sm text-red-600">{{ error }}</p>
  </div>
</template>

<script setup lang="ts">
import { ref, watch } from 'vue'
import apiClient from '@/api/client'
import BPSAutomaticAuthorization from './BPSAutomaticAuthorization.vue'

const props = defineProps<{ accountId: number }>()
const state = ref<{ status: string } | null>(null)
const authorization = ref<{ auth_url: string; session_id: string } | null>(null)
const callback = ref('')
const error = ref('')
const busy = ref(false)
const labels: Record<string, string> = { missing: '未授权', not_expired: '已授权', expired: '待自动刷新', revoked: '已撤销', auth_failed: '待刷新或重新授权', unknown: '未知' }
const endpoint = () => `/admin/openai/accounts/${props.accountId}/excel-authorization`
function message(err: unknown): string {
  const e = err as { message?: string; response?: { data?: { message?: string } } }
  return e.response?.data?.message || e.message || '授权操作失败，请重试'
}
watch(() => props.accountId, async () => {
  state.value = null; authorization.value = null; callback.value = ''; error.value = ''
  const accountId = props.accountId
  try {
    const { data } = await apiClient.get(endpoint())
    if (accountId === props.accountId) state.value = data
  } catch (err) { if (accountId === props.accountId) error.value = message(err) }
}, { immediate: true })
async function begin() {
  busy.value = true; error.value = ''; callback.value = ''
  try { const { data } = await apiClient.post(`${endpoint()}/start`); authorization.value = data }
  catch (err) { error.value = message(err) }
  finally { busy.value = false }
}
async function complete() {
  if (!authorization.value) return
  busy.value = true; error.value = ''
  try {
    const url = new URL(callback.value.trim())
    const code = url.searchParams.get('code'); const oauthState = url.searchParams.get('state')
    if (!code || !oauthState) { error.value = '回调 URL 缺少 code 或 state'; return }
    const { data } = await apiClient.post(endpoint(), { session_id: authorization.value.session_id, code, state: oauthState })
    state.value = data; authorization.value = null; callback.value = ''
  } catch (err) { error.value = message(err) }
  finally { busy.value = false }
}
</script>
