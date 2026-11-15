<template>
  <div
    ref="frame"
    class="relative aspect-[4/3] w-full overflow-hidden bg-gray-100 dark:bg-dark-900"
    :style="{ backgroundColor: (active ?? pending)?.background }"
    :aria-busy="pending !== null"
    data-testid="pelican-animation-preview"
  >
    <iframe
      v-for="preview in previews"
      :key="preview.id"
      class="absolute left-1/2 top-1/2 max-w-none border-0"
      :style="viewportStyle(preview)"
      :data-preview-state="preview.id === active?.id ? 'active' : 'pending'"
      :data-preview-id="preview.id"
      :aria-hidden="preview.id !== active?.id"
      :tabindex="preview.id === active?.id ? undefined : -1"
      sandbox=""
      referrerpolicy="no-referrer"
      loading="eager"
      :title="title"
      :srcdoc="preview.html"
      @load="prepareFrame(preview.id, $event)"
    />
    <div
      v-if="showLoading || loadTimedOut"
      class="absolute bottom-2 left-2 flex items-center gap-2 rounded bg-white/90 px-2 py-1 text-xs text-gray-700 shadow-sm dark:bg-dark-800/90 dark:text-gray-200"
      role="status"
      data-testid="pelican-preview-status"
    >
      <span v-if="showLoading" class="h-3 w-3 animate-spin rounded-full border-2 border-gray-300 border-t-primary-600" aria-hidden="true"></span>
      <span>{{ t(loadTimedOut ? 'pelicanTest.previewLoadTimeout' : 'pelicanTest.previewLoading') }}</span>
      <button
        v-if="loadTimedOut"
        type="button"
        class="font-medium text-primary-700 underline dark:text-primary-300"
        data-testid="pelican-preview-retry"
        @click="queuePreview(html)"
      >{{ t('pelicanTest.previewRetry') }}</button>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, onBeforeUnmount, ref, shallowRef, watch } from 'vue'
import { useResizeObserver } from '@vueuse/core'
import { useI18n } from 'vue-i18n'
import { pelicanPreviewBackground, pelicanPreviewViewport } from '@/utils/pelicanPreview'

interface PreviewFrame {
  id: number
  html: string
  viewport: { width: number; height: number }
  background: string | undefined
}

const props = defineProps<{ html: string; title: string }>()
const { t } = useI18n()
const frame = ref<HTMLElement>()
const size = ref({ width: 0, height: 0 })
const active = shallowRef<PreviewFrame | null>(null)
const pending = shallowRef<PreviewFrame | null>(null)
const showLoading = ref(false)
const loadTimedOut = ref(false)
const previews = computed(() => [active.value, pending.value].filter((preview): preview is PreviewFrame => preview !== null))
let nextId = 0
let paintFrame: number | undefined
let hintTimer: ReturnType<typeof setTimeout> | undefined
let loadTimer: ReturnType<typeof setTimeout> | undefined

function clearLoadTimers() {
  if (hintTimer !== undefined) clearTimeout(hintTimer)
  if (loadTimer !== undefined) clearTimeout(loadTimer)
  hintTimer = undefined
  loadTimer = undefined
  showLoading.value = false
}

function cancelPendingPaint() {
  if (paintFrame !== undefined) cancelAnimationFrame(paintFrame)
  paintFrame = undefined
}

function queuePreview(html: string) {
  cancelPendingPaint()
  clearLoadTimers()
  loadTimedOut.value = false
  if (active.value?.html === html) {
    pending.value = null
    return
  }
  const candidate: PreviewFrame = {
    id: ++nextId,
    html,
    viewport: pelicanPreviewViewport(html),
    background: pelicanPreviewBackground(html),
  }
  pending.value = candidate
  hintTimer = setTimeout(() => {
    if (pending.value === candidate) showLoading.value = true
  }, 250)
  loadTimer = setTimeout(() => {
    if (pending.value !== candidate) return
    cancelPendingPaint()
    clearLoadTimers()
    pending.value = null
    loadTimedOut.value = true
  }, 10_000)
}

watch(() => props.html, queuePreview, { immediate: true })

function prepareFrame(id: number, event: Event) {
  const candidate = pending.value
  const element = event.currentTarget as HTMLIFrameElement | null
  if (!candidate || candidate.id !== id || element?.dataset.previewId !== String(id)) return
  cancelPendingPaint()
  // Give the loaded document a paint opportunity while the old frame remains
  // visible. Each callback belongs to this immutable frame, never a newer click.
  paintFrame = requestAnimationFrame(() => {
    if (pending.value !== candidate) return
    paintFrame = requestAnimationFrame(() => {
      if (pending.value !== candidate || element.dataset.previewId !== String(id)) return
      paintFrame = undefined
      clearLoadTimers()
      active.value = candidate
      pending.value = null
    })
  })
}

useResizeObserver(frame, entries => {
  const bounds = entries[0]?.contentRect
  if (bounds) size.value = { width: bounds.width, height: bounds.height }
})

function viewportStyle(preview: PreviewFrame) {
  const { width, height } = preview.viewport
  const scale = Math.min(size.value.width / width, size.value.height / height)
  const visible = preview.id === active.value?.id
  return {
    width: `${width}px`, height: `${height}px`,
    transform: `translate(-50%, -50%) scale(${scale})`,
    backgroundColor: 'transparent',
    opacity: visible ? 1 : 0,
    pointerEvents: visible ? 'auto' as const : 'none' as const,
  }
}

onBeforeUnmount(() => {
  cancelPendingPaint()
  clearLoadTimers()
  pending.value = null
})
</script>
