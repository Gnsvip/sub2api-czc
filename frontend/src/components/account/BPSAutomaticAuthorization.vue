<template>
  <details class="rounded-lg border border-gray-200 p-3 dark:border-dark-600" @toggle="opened">
    <summary class="cursor-pointer text-sm">自动获取 / 恢复 Excel 授权</summary>
    <div class="mt-3 space-y-2">
      <label class="flex items-center gap-2 text-sm"><input v-model="enabled" type="checkbox" :disabled="busy || !loaded" />启用自动授权</label>
      <p class="text-xs text-gray-500">仅在 BPS 总开关开启时运行；失败间隔 15 分钟，最多自动尝试 3 次。额外验证需手动授权。</p>
      <input v-model="email" class="input" type="email" autocomplete="off" aria-label="登录邮箱" placeholder="ChatGPT 登录邮箱" />
      <input v-model="password" class="input" type="password" autocomplete="new-password" aria-label="登录密码" :placeholder="configuredPassword ? '密码已保存，留空保留' : '登录密码'" />
      <input v-model="totp" class="input" type="password" autocomplete="new-password" aria-label="TOTP 密钥" :placeholder="configuredTOTP ? 'TOTP 密钥已保存，留空保留' : 'TOTP 密钥（可选，不是 6 位验证码）'" />
      <label v-if="configuredTOTP" class="flex items-center gap-2 text-xs"><input v-model="clearTOTP" type="checkbox" />移除保存的 TOTP 密钥</label>
      <div class="flex gap-2">
        <button type="button" class="btn btn-secondary" :disabled="busy || !loaded" @click="save">保存自动授权设置</button>
        <button type="button" class="btn btn-secondary" :disabled="busy || !savedEnabled || pending" @click="run">立即重新授权</button>
      </div>
      <p v-if="task" class="text-xs text-gray-500">{{ taskLabels[task.status] || task.status }}<span v-if="task.error_code"> · {{ failureLabels[task.error_code] || '授权失败，可手动重试' }}</span></p>
      <p v-if="runtimeReason" class="text-xs text-gray-500">{{ runtimeReason }}</p>
      <p v-if="error" role="alert" class="text-sm text-red-600">{{ error }}</p>
    </div>
  </details>
</template>

<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import apiClient from '@/api/client'
import { extractApiErrorMessage } from '@/utils/apiError'

const props = defineProps<{ accountId: number }>()
type Task = { status: string; error_code?: string }
type Status = { config: { enabled: boolean; login_email: string; password_configured: boolean; totp_configured: boolean; revision: number }; task: Task | null; runtime: { state: string; reason?: string } }
const enabled = ref(false), savedEnabled = ref(false), email = ref(''), password = ref(''), totp = ref(''), clearTOTP = ref(false)
const configuredPassword = ref(false), configuredTOTP = ref(false), loaded = ref(false), busy = ref(false), error = ref(''), revision = ref(0)
const task = ref<Task | null>(null), runtimeReason = ref(''), expanded = ref(false)
const pending = computed(() => task.value?.status === 'queued' || task.value?.status === 'running')
const taskLabels: Record<string, string> = { queued: '等待授权', running: '正在授权', succeeded: '授权成功', failed: '授权失败', cancelled: '设置变更，任务已取消' }
const failureLabels: Record<string, string> = { timeout: '超时', config_changed: '设置已变化', account_changed: '账号已变化', identity_mismatch: '授权身份不匹配', proxy_unavailable: '代理不可用', password_rejected: '密码被拒绝', totp_rejected: 'TOTP 校验失败', security_check: '需要手动安全验证', additional_verification: '需要额外验证' }
let generation = 0
let timer: ReturnType<typeof setTimeout> | undefined
const endpoint = (id: number) => `/admin/openai/accounts/${id}/excel-login`
function stopPoll() { if (timer) clearTimeout(timer); timer = undefined }
function state(data: Status, fill: boolean) {
  task.value = data.task
  runtimeReason.value = data.runtime.state === 'unavailable' ? '自动授权运行环境尚未就绪，可使用手动授权。' : ''
  if (fill) {
    enabled.value = savedEnabled.value = data.config.enabled; email.value = data.config.login_email || ''
    configuredPassword.value = data.config.password_configured; configuredTOTP.value = data.config.totp_configured; revision.value = data.config.revision
    password.value = ''; totp.value = ''; clearTOTP.value = false; loaded.value = true
  }
}
async function refresh(fill = false) {
  const id = props.accountId, version = generation
  try {
    const { data } = await apiClient.get<Status>(endpoint(id))
    if (version !== generation) return
    state(data, fill)
    if (expanded.value && pending.value) { stopPoll(); timer = setTimeout(() => { void refresh() }, 5000) }
  } catch (err) { if (version === generation) error.value = extractApiErrorMessage(err, '读取自动授权状态失败') }
}
function opened(event: Event) {
  expanded.value = (event.target as HTMLDetailsElement).open
  if (expanded.value) void refresh(!loaded.value)
  else stopPoll()
}
async function save() {
  const id = props.accountId, version = generation
  busy.value = true; error.value = ''
  try {
    const { data } = await apiClient.put<Status>(endpoint(id), { enabled: enabled.value, login_email: email.value, password: password.value, totp_secret: totp.value, clear_totp: clearTOTP.value, revision: revision.value })
    if (version === generation) state(data, true)
  } catch (err) { if (version === generation) error.value = extractApiErrorMessage(err, '保存自动授权设置失败') }
  finally { if (version === generation) { busy.value = false; password.value = ''; totp.value = '' } }
}
async function run() {
  const id = props.accountId, version = generation
  busy.value = true; error.value = ''
  try {
    const { data } = await apiClient.post<Task>(`${endpoint(id)}/run`)
    if (version === generation) { task.value = data; void refresh() }
  } catch (err) { if (version === generation) error.value = extractApiErrorMessage(err, '启动自动授权失败') }
  finally { if (version === generation) busy.value = false }
}
watch(() => props.accountId, () => {
  generation++; stopPoll(); loaded.value = false; busy.value = false; task.value = null
  enabled.value = savedEnabled.value = false; email.value = password.value = totp.value = error.value = ''; revision.value = 0
  configuredPassword.value = configuredTOTP.value = clearTOTP.value = false; runtimeReason.value = ''
  if (expanded.value) void refresh(true)
})
onBeforeUnmount(() => { generation++; stopPoll() })
</script>
