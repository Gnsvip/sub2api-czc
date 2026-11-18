<template>
  <section class="space-y-4" data-testid="codex-automatic-authorization">
    <label class="block">
      <span class="input-label">登录资料</span>
      <textarea v-model="content" class="input font-mono text-sm" rows="5" autocomplete="off" autocapitalize="off" :spellcheck="false" :disabled="submitting"
        placeholder="邮箱----密码----TOTP 密钥&#10;每行一个，最多 50 个；未启用二次验证可留空最后一项" data-testid="codex-login-content" />
    </label>
    <label class="flex items-center gap-2 text-sm">
      <input v-model="autoRecover" type="checkbox" :disabled="submitting" /> 掉授权后自动恢复
    </label>
    <p class="text-xs text-gray-500">关闭页面后任务继续。启用自动恢复后，临时失败至少间隔 15 分钟重试，连续失败 3 次后停止；密码或验证问题需手动处理。</p>
    <p v-if="error" class="text-sm text-red-600" role="alert">{{ error }}</p>
    <p v-if="runtime?.state === 'preparing'" class="text-sm text-gray-500">正在准备登录运行环境…</p>
    <p v-if="runtime?.state === 'unavailable'" class="text-sm text-amber-600">登录运行环境暂不可用，任务尚未完成。请稍后检查。</p>
    <button type="button" class="btn btn-primary" :disabled="submitting || !content.trim()" data-testid="codex-login-submit" @click="submit">
      {{ submitting ? '正在提交…' : '开始自动授权' }}
    </button>
    <ul v-if="results.length" class="divide-y divide-gray-200 rounded-lg border border-gray-200 dark:divide-dark-600 dark:border-dark-600" aria-live="polite">
      <li v-for="result in results" :key="result.line" class="space-y-1 p-3 text-sm">
        <div class="flex flex-wrap justify-between gap-2"><span>{{ result.email }}<span v-if="result.account_id" class="ml-2 text-gray-400">#{{ result.account_id }}</span></span>
          <span :class="result.task?.status === 'succeeded' ? 'text-green-600' : result.error_code || result.task?.status === 'failed' ? 'text-red-600' : 'text-gray-500'">{{ result.error_code ? '未启动' : codexLoginTaskLabel(result.task) }}</span>
        </div>
        <p v-if="result.error_code || result.task?.error_code" class="text-xs text-red-600">{{ codexLoginError(result.error_code || result.task?.error_code) }}</p>
      </li>
    </ul>
  </section>
</template>

<script setup lang="ts">
import { onBeforeUnmount, ref } from 'vue'
import { createCodexLoginAccounts, getCodexLoginStatus, codexLoginTaskLabel, codexLoginError, type CodexLoginCreateOptions, type CodexLoginResult, type CodexLoginStatus } from '@/api/admin/codexLogin'

const props = defineProps<{ options: CodexLoginCreateOptions }>()
const emit = defineEmits<{ created: [] }>()
const content = ref('')
const autoRecover = ref(false)
const submitting = ref(false)
const error = ref('')
const results = ref<CodexLoginResult[]>([])
const runtime = ref<CodexLoginStatus['runtime']>()
let timer: ReturnType<typeof setTimeout> | undefined
let disposed = false
let generation = 0

async function poll() {
  const current = generation
  const pending = results.value.filter(result => result.account_id && !result.error_code && ['queued', 'running'].includes(result.task?.status || ''))
  if (disposed || !pending.length) return
  await Promise.all(pending.map(async result => {
    try {
      const state = await getCodexLoginStatus(result.account_id!)
      if (disposed || current !== generation) return
      const wasSucceeded = result.task?.status === 'succeeded'
      result.task = state.task
      runtime.value = state.runtime
      if (!wasSucceeded && state.task?.status === 'succeeded') emit('created')
    } catch { if (!disposed && current === generation) error.value = '进度暂时读取失败，后台任务仍会继续。' }
  }))
  if (!disposed && current === generation && results.value.some(result => ['queued', 'running'].includes(result.task?.status || ''))) timer = setTimeout(poll, 5000)
}
async function submit() {
  if (submitting.value || !content.value.trim()) return
  submitting.value = true
  generation++
  error.value = ''
  clearTimeout(timer)
  try {
    const response = await createCodexLoginAccounts(props.options, content.value, autoRecover.value)
    content.value = ''
    if (disposed) return
    results.value = response.results
    runtime.value = response.runtime
    if (response.results.some(result => result.account_id && !result.error_code)) emit('created')
    timer = setTimeout(poll, 1500)
  } catch (cause) {
    content.value = ''
    if (!disposed) error.value = (cause as { message?: string }).message || '提交失败，请检查资料后重试'
  } finally { submitting.value = false }
}
onBeforeUnmount(() => { disposed = true; generation++; clearTimeout(timer); content.value = '' })
</script>
