<template>
  <AppLayout>
    <div v-if="pelican.visible" class="space-y-6">
      <div>
        <h1 class="text-2xl font-semibold text-gray-900 dark:text-gray-100 lg:hidden">{{ t('nav.pelicanTest') }}</h1>
        <p class="mt-2 max-w-3xl text-sm leading-relaxed text-gray-500 dark:text-gray-400 lg:mt-0">{{ t('pelicanTest.subtitle') }}</p>
      </div>

      <div v-if="loading" class="flex justify-center py-16"><LoadingSpinner /></div>
      <p v-else-if="error" class="text-sm text-red-600" role="alert">{{ error }}</p>
      <p v-else-if="!items.length" class="rounded-2xl border border-dashed border-gray-200 p-10 text-center text-sm text-gray-400 dark:border-dark-700">{{ t('pelicanTest.empty') }}</p>
      <div v-else class="mx-auto flex max-w-5xl items-start gap-3 sm:gap-4">
        <div
          v-if="items.length > 1"
          class="flex max-h-[min(36rem,70vh)] w-[7.25rem] shrink-0 flex-col gap-1.5 overflow-y-auto sm:w-40"
        >
          <button
            v-for="row in items"
            :key="row.id"
            type="button"
            class="rounded-lg border px-2 py-1.5 text-left text-xs leading-snug"
            :class="selected?.id === row.id
              ? 'border-primary-500 bg-primary-50 text-primary-700 dark:border-primary-400 dark:bg-primary-900/30 dark:text-primary-200'
              : 'border-gray-200 text-gray-600 hover:border-gray-300 dark:border-dark-600 dark:text-gray-300'"
            @click="selected = row"
          >
            {{ formatSlot(row.finished_at || row.created_at) }}
            <span class="mt-1 block" :class="resultTone(row)">{{ resultLabel(row) }}</span>
            <span v-if="isIncomplete(row)" class="mt-1 block text-amber-700 dark:text-amber-400">{{ t('pelicanTest.incompleteLabel') }}</span>
          </button>
        </div>
        <article v-if="selected" class="relative min-w-0 flex-1 overflow-hidden rounded-2xl border border-gray-200 bg-white shadow-sm dark:border-dark-700 dark:bg-dark-800">
          <span class="pointer-events-none absolute -right-9 top-6 z-10 w-36 rotate-45 py-1.5 text-center text-xs font-semibold text-white shadow-sm" :class="ribbonTone(selected)" data-testid="pelican-ribbon">{{ resultLabel(selected) }}</span>
          <p v-if="previewMessage(selected)" class="border-b border-amber-200 bg-amber-50 px-4 py-3 text-sm text-amber-800 dark:border-amber-900 dark:bg-amber-950/30 dark:text-amber-200" role="status">{{ previewMessage(selected) }}</p>
          <div v-if="selected.html && !isIncomplete(selected)" class="aspect-[4/3] bg-[#0b1220]">
            <iframe
              class="h-full w-full border-0"
              sandbox=""
              referrerpolicy="no-referrer"
              :title="t('nav.pelicanTest')"
              :srcdoc="selected.html"
            />
          </div>
          <p v-else class="flex aspect-[4/3] items-center justify-center px-6 text-center text-sm text-gray-400">{{ isIncomplete(selected) ? t('pelicanTest.incompletePreview') : t('pelicanTest.htmlUnavailable') }}</p>
          <details class="border-t border-gray-100 p-4 dark:border-dark-700" data-testid="pelican-details">
            <summary class="cursor-pointer text-sm text-gray-500">{{ t('pelicanTest.details') }}</summary>
            <PelicanVisualAssessment :assessment="selected.visual_judgement" class="mt-4" />
            <dl class="grid grid-cols-2 gap-3 py-4 text-sm sm:grid-cols-4">
              <div>
                <dt class="text-xs text-gray-400">{{ t('pelicanTest.time') }}</dt>
                <dd class="mt-1 text-gray-800 dark:text-gray-100">{{ formatTime(selected.finished_at || selected.created_at) }}</dd>
              </div>
              <div>
                <dt class="text-xs text-gray-400">{{ t('pelicanTest.group') }}</dt>
                <dd class="mt-1 text-gray-800 dark:text-gray-100">{{ selected.group_name || '—' }}</dd>
              </div>
              <div>
                <dt class="text-xs text-gray-400">{{ t('pelicanTest.model') }}</dt>
                <dd class="mt-1 text-gray-800 dark:text-gray-100">{{ selected.model || 'gpt-6-astra' }}</dd>
              </div>
              <div>
                <dt class="text-xs text-gray-400">{{ t('pelicanTest.reasoning') }}</dt>
                <dd class="mt-1 text-gray-800 dark:text-gray-100">{{ selected.reasoning_effort || 'low' }}</dd>
              </div>
            </dl>
            <div v-if="selected.prompt" class="border-t border-gray-100 px-4 py-4 dark:border-dark-700">
              <p class="text-xs text-gray-400">{{ t('pelicanTest.prompt') }}</p>
              <pre class="mt-2 whitespace-pre-wrap break-words text-sm leading-relaxed text-gray-700 dark:text-gray-200">{{ selected.prompt }}</pre>
            </div>
          </details>
        </article>
      </div>
    </div>
  </AppLayout>
</template>

<script setup lang="ts">
import { onMounted, ref, watch } from 'vue'
import { usePelicanStore } from '@/stores/pelican'
import { useI18n } from 'vue-i18n'
import AppLayout from '@/components/layout/AppLayout.vue'
import LoadingSpinner from '@/components/common/LoadingSpinner.vue'
import PelicanVisualAssessment from '@/components/common/PelicanVisualAssessment.vue'
import { pelicanTestsAPI, type PelicanTestItem } from '@/api/pelicanTests'
import { extractApiErrorMessage } from '@/utils/apiError'

const { t } = useI18n()
const pelican = usePelicanStore()
const loading = ref(true)
const error = ref('')
const items = ref<PelicanTestItem[]>([])
const selected = ref<PelicanTestItem | null>(null)
watch(() => pelican.visible, visible => {
  if (!visible) { items.value = []; selected.value = null }
})

function resultLabel(item: PelicanTestItem) {
  if (isIncomplete(item)) return t('pelicanTest.incompleteLabel')
  if (item.visual_judgement?.state === 'passed') return t('pelicanTest.passed')
  if (item.visual_judgement?.state === 'anomalous') return t('pelicanTest.anomalous')
  if (item.visual_judgement?.state === 'unavailable') return t('pelicanTest.scoringFailed')
  return t('pelicanTest.unscored')
}

function resultTone(item: PelicanTestItem) {
  if (item.visual_judgement?.state === 'passed') return 'text-emerald-700 dark:text-emerald-400'
  if (item.visual_judgement?.state === 'anomalous') return 'text-rose-700 dark:text-rose-400'
  return 'text-gray-500'
}

function ribbonTone(item: PelicanTestItem) {
  if (isIncomplete(item)) return 'bg-amber-600'
  if (item.visual_judgement?.state === 'passed') return 'bg-emerald-700'
  if (item.visual_judgement?.state === 'anomalous') return 'bg-rose-700'
  return 'bg-gray-500'
}

function isIncomplete(item: PelicanTestItem) {
  return !['completed', 'success'].includes(item.status) || item.preview_issue === 'incomplete'
}

function previewMessage(item: PelicanTestItem) {
  if (isIncomplete(item)) return t('pelicanTest.incompleteResult')
  if (item.preview_issue === 'scripts_removed') return t('pelicanTest.scriptsRemoved')
  return ''
}

function formatTime(value?: string | null) {
  if (!value) return '—'
  const date = new Date(value)
  return Number.isFinite(date.getTime()) ? date.toLocaleString(undefined, { hour12: false }) : '—'
}

function formatSlot(value?: string | null) {
  if (!value) return '—'
  const date = new Date(value)
  if (!Number.isFinite(date.getTime())) return '—'
  const pad = (n: number) => String(n).padStart(2, '0')
  return `${pad(date.getMonth() + 1)}-${pad(date.getDate())} ${pad(date.getHours())}:${pad(date.getMinutes())}`
}

onMounted(async () => {
  try {
    const page = await pelicanTestsAPI.list(1, 32)
    items.value = page.items || []
    selected.value = items.value.find((item) => item.html && !isIncomplete(item)) ?? items.value[0] ?? null
  } catch (err) {
    items.value = []; selected.value = null
    if ((err as { response?: { status?: number }; status?: number }).response?.status === 404 || (err as { status?: number }).status === 404) pelican.setVisible(false)
    error.value = extractApiErrorMessage(err, t('pelicanTest.loadFailed'))
  } finally {
    loading.value = false
  }
})
</script>
