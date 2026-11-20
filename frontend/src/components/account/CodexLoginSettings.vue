<template>
  <details class="rounded-lg border border-gray-200 p-4 dark:border-dark-600" @toggle="onToggle">
    <summary class="cursor-pointer text-sm font-medium">Codex 自动授权</summary>
    <div v-if="opened" class="mt-4 space-y-3">
      <p v-if="loading" class="text-sm text-gray-500">正在读取…</p>
      <template v-if="state">
        <label class="flex items-center gap-2 text-sm"><input v-model="enabled" type="checkbox" /> 掉授权后自动恢复</label>
        <CodexAccountExpiryField :model-value="expiresAt" :disabled="loading" :auto-pause-on-expired="!expiryEdited && state.auto_pause_on_expired" @update:model-value="setExpiry" />
        <label class="block"><span class="input-label">邮箱</span><input v-model="email" type="email" class="input" autocomplete="off" /></label>
        <label class="block"><span class="input-label">密码</span><input v-model="password" type="password" class="input" autocomplete="new-password" :placeholder="state.config.password_configured ? '已保存；留空不修改' : '请输入密码'" /></label>
        <label class="block"><span class="input-label">TOTP 密钥</span><input v-model="totp" type="password" class="input" autocomplete="new-password" :placeholder="state.config.totp_configured ? '已保存；留空不修改' : '未开启二次验证可留空'" /></label>
        <label v-if="state.config.totp_configured" class="flex items-center gap-2 text-xs"><input v-model="clearTOTP" type="checkbox" /> 清除已保存的 TOTP 密钥</label>
        <label class="block"><span class="input-label">首次授权工作区 ID（选填）</span><input v-model="preferredWorkspaceID" class="input" maxlength="128" autocomplete="off" placeholder="留空优先 Business；重新授权保留原工作区" data-testid="codex-preferred-workspace" /></label>
        <p class="text-xs text-gray-500">{{ codexLoginTaskLabel(state.task) }}<span v-if="state.task?.error_code"> · {{ codexLoginError(state.task.error_code) }}</span></p>
        <p v-if="state.runtime.state === 'unavailable'" class="text-xs text-amber-600">登录运行环境暂不可用。</p>
        <p class="text-xs text-gray-500">其他账号修改请先保存。授权成功后会刷新并关闭编辑窗口。</p>
        <div class="flex gap-2">
          <button type="button" class="btn btn-secondary" :disabled="loading" data-testid="codex-login-save" @click="save(false)">保存登录设置</button>
          <button type="button" class="btn btn-primary" :disabled="loading || ['queued','running'].includes(state.task?.status || '')" data-testid="codex-login-run" @click="save(true)">立即授权</button>
        </div>
      </template>
      <p v-if="error" class="text-sm text-red-600" role="alert">{{ error }}</p>
    </div>
  </details>
</template>
<script setup lang="ts">
import { onBeforeUnmount, ref, watch } from 'vue'
import { useAppStore } from '@/stores/app'
import CodexAccountExpiryField from './CodexAccountExpiryField.vue'
import { getCodexLoginStatus, saveCodexLogin, runCodexLogin, codexLoginError, codexLoginTaskLabel, type CodexLoginStatus } from '@/api/admin/codexLogin'
const props = defineProps<{ accountId: number }>()
const appStore = useAppStore()
const emit = defineEmits<{ authorized: []; 'expiry-saved': [value: number | null] }>()
const opened = ref(false)
const loading = ref(false)
const enabled = ref(false)
const expiresAt = ref<number | null>(null)
const expiryEdited = ref(false)
function setExpiry(value: number | null) { expiresAt.value = value; expiryEdited.value = true }
const email = ref('')
const preferredWorkspaceID = ref('')
const password = ref('')
const totp = ref('')
const clearTOTP = ref(false)
const state = ref<CodexLoginStatus>()
const error = ref('')
let timer: ReturnType<typeof setTimeout> | undefined
let generation = 0
async function load(initial = false) {
  const current = generation
  try {
    const next = await getCodexLoginStatus(props.accountId)
    if (current !== generation || !opened.value) return
    const completed = state.value?.task?.id === next.task?.id && ['queued', 'running'].includes(state.value?.task?.status || '') && next.task?.status === 'succeeded'
    state.value = next
    if (initial) { email.value = next.config.login_email; enabled.value = next.config.enabled; preferredWorkspaceID.value = next.config.preferred_workspace_id || ''; expiresAt.value = next.expires_at ?? null; expiryEdited.value = false }
    if (['queued','running'].includes(next.task?.status || '')) timer = setTimeout(() => load(), 5000)
    if (completed) {
      if (next.task?.stage.startsWith('succeeded_personal_workspace') || next.task?.stage.includes('_astra_') || next.task?.stage.endsWith('_free_plan')) appStore.showWarning(codexLoginTaskLabel(next.task), 8000)
      emit('authorized')
    }
  } catch {
    if (current === generation && opened.value) {
      error.value = '无法读取自动授权设置，请稍后重试'
      if (!initial) timer = setTimeout(() => load(), 5000)
    }
  }
}
function onToggle(event: Event) {
  opened.value = (event.target as HTMLDetailsElement).open
  clearTimeout(timer)
  generation++
  const current = generation
  password.value = ''; totp.value = ''; clearTOTP.value = false; error.value = ''
  if (opened.value) { loading.value = true; void load(true).finally(() => { if (current === generation) loading.value = false }) }
}
async function save(run: boolean) {
  if (!state.value || loading.value) return
  loading.value = true; error.value = ''; clearTimeout(timer)
  const current = generation
  const accountId = props.accountId
  const savingExpiry = expiryEdited.value
  try {
    const saved = await saveCodexLogin(accountId, { enabled: enabled.value, login_email: email.value, password: password.value, totp_secret: totp.value, clear_totp: clearTOTP.value, revision: state.value.config.revision, preferred_workspace_id: preferredWorkspaceID.value.trim(), ...(expiryEdited.value ? { expires_at: expiresAt.value ?? 0 } : {}) })
    if (current !== generation || !opened.value) return
    state.value = saved; password.value = ''; totp.value = ''; clearTOTP.value = false
    expiresAt.value = saved.expires_at ?? null; expiryEdited.value = false
    if (savingExpiry) emit('expiry-saved', expiresAt.value)
    if (run) {
      const task = await runCodexLogin(accountId)
      if (current !== generation || !opened.value) return
      state.value.task = task
    }
    if (['queued','running'].includes(state.value.task?.status || '')) timer = setTimeout(() => load(), 1500)
  } catch (cause) { if (current === generation) error.value = (cause as { message?: string }).message || '保存失败，请重试' }
  finally { if (current === generation) { loading.value = false; password.value = ''; totp.value = '' } }
}
watch(() => props.accountId, () => { generation++; clearTimeout(timer); password.value = ''; totp.value = ''; clearTOTP.value = false; state.value = undefined; error.value = ''; loading.value = false; if (opened.value) void load(true) })
onBeforeUnmount(() => { generation++; clearTimeout(timer); password.value = ''; totp.value = '' })
</script>
