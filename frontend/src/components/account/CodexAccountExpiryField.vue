<template>
  <div data-testid="codex-account-expiry">
    <label class="block">
      <span class="input-label">账号过期时间（选填）</span>
      <input v-model="dateInput" type="datetime-local" class="input" :disabled="disabled" data-testid="codex-expiry-input" />
    </label>
    <div class="mt-2 flex gap-2">
      <button type="button" class="btn btn-secondary btn-sm" :disabled="disabled" @click="emit('update:modelValue', getAccountExpiryTimestamp(1))">一个月</button>
      <button type="button" class="btn btn-secondary btn-sm" :disabled="disabled" @click="emit('update:modelValue', getAccountExpiryTimestamp(12))">一年</button>
      <button type="button" class="btn btn-secondary btn-sm" :disabled="disabled || modelValue == null" @click="emit('update:modelValue', null)">清除</button>
    </div>
    <p class="input-hint">按 {{ timezone }}。临近到期优先使用；到期后{{ autoPauseOnExpired ? '停止调度' : '仍可调用' }}，掉授权后不再自动恢复。留空不限。</p>
  </div>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { formatDateTimeLocalInput, getBrowserTimeZone, parseDateTimeLocalInput } from '@/utils/format'
import { getAccountExpiryTimestamp } from './accountExpiry'

const props = defineProps<{ modelValue: number | null; disabled?: boolean; autoPauseOnExpired?: boolean }>()
const emit = defineEmits<{ 'update:modelValue': [value: number | null] }>()
const timezone = getBrowserTimeZone()
const dateInput = computed({
  get: () => formatDateTimeLocalInput(props.modelValue),
  set: (value: string) => emit('update:modelValue', parseDateTimeLocalInput(value)),
})
</script>
