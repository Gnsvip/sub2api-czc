<template>
  <div class="flex flex-wrap items-center gap-1" :class="compact ? 'text-[10px] leading-4' : 'text-sm'">
    <button
      v-if="probeEnabled !== undefined"
      type="button"
      role="switch"
      data-testid="astra-probe-toggle"
      :aria-checked="probeEnabled"
      :aria-label="t('admin.accounts.openai.astraProbeToggle')"
      :title="t('admin.accounts.openai.astraProbeToggleDesc')"
      :disabled="probeSaving"
      class="relative inline-flex h-3.5 w-6 shrink-0 items-center rounded-full transition-colors disabled:cursor-wait disabled:opacity-50"
      :class="probeEnabled ? 'bg-emerald-500' : 'bg-gray-300 dark:bg-gray-600'"
      @click.stop="emit('toggle-probe')"
    >
      <span class="inline-block h-2.5 w-2.5 rounded-full bg-white transition-transform" :class="probeEnabled ? 'translate-x-3' : 'translate-x-0.5'" />
    </button>
    <span class="truncate font-medium text-gray-500 dark:text-gray-400" :title="model">{{ shortName }}</span>
    <span
      class="inline-flex max-w-full flex-wrap items-center"
      :class="compact ? 'gap-px' : 'gap-0.5'"
      :aria-label="sparkLabel"
    >
      <span
        v-for="(slot, index) in slots"
        :key="index"
        class="inline-block shrink-0 rounded-full"
        data-testid="tibo-dot"
        :data-verdict="slot?.verdict || 'empty'"
        :class="[compact ? 'h-1.5 w-1.5' : 'h-2 w-2', slotClass(slot)]"
        :title="slotTitle(slot)"
      />
    </span>
    <span v-if="probeEnabled === false" class="text-gray-400" data-testid="astra-probe-disabled">{{ t('admin.accounts.openai.astraProbeDisabled') }}</span>
  </div>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import type { TiboRouteVerdict } from '@/types'

const props = withDefaults(defineProps<{
  model: string
  verdict: string
  samples?: { verdict: TiboRouteVerdict; at?: string }[]
  compact?: boolean
  probeEnabled?: boolean
  probeSaving?: boolean
}>(), {
  samples: () => [],
  compact: true,
  probeEnabled: undefined,
  probeSaving: false,
})

const emit = defineEmits<{ 'toggle-probe': [] }>()

const { t } = useI18n()
const sampleLimit = 24

const shortName = computed(() => {
  if (props.model === 'gpt-6-astra') return 'astra'
  if (props.model === 'gpt-5.6-sol') return 'sol'
  return props.model
})

const slots = computed(() => {
  const samples = (props.samples ?? []).filter((sample) => sample.verdict === 'healthy' || sample.verdict === 'degraded')
  const missing = Math.max(0, sampleLimit - samples.length)
  return [
    ...Array.from({ length: missing }, () => null),
    ...samples.slice(-sampleLimit),
  ]
})

const sparkLabel = computed(() => t('admin.accounts.openai.codexTiboRecentSamples'))

function slotClass(slot: { verdict: TiboRouteVerdict; at?: string } | null) {
  if (!slot) return 'bg-gray-200 dark:bg-gray-700'
  if (slot.verdict === 'healthy') return 'bg-emerald-500'
  return 'bg-red-500'
}

function slotTitle(slot: { verdict: TiboRouteVerdict; at?: string } | null) {
  if (!slot) return t('admin.accounts.openai.codexTiboSampleEmpty')
  const label = t(`admin.accounts.openai.codexTiboRouteVerdict.${slot.verdict}`)
  return slot.at ? `${label} · ${slot.at}` : label
}
</script>
