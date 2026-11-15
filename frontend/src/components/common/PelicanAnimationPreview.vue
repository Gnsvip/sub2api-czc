<template>
  <div ref="frame" class="relative aspect-[4/3] w-full overflow-hidden bg-white" data-testid="pelican-animation-preview">
    <iframe
      class="absolute left-1/2 top-1/2 max-w-none border-0 bg-white"
      :style="viewportStyle"
      sandbox=""
      referrerpolicy="no-referrer"
      :title="title"
      :srcdoc="html"
    />
  </div>
</template>

<script setup lang="ts">
import { computed, ref } from 'vue'
import { useResizeObserver } from '@vueuse/core'
import { pelicanPreviewViewport } from '@/utils/pelicanPreview'

const props = defineProps<{ html: string; title: string }>()
const frame = ref<HTMLElement>()
const size = ref({ width: 0, height: 0 })
const viewport = computed(() => pelicanPreviewViewport(props.html))
useResizeObserver(frame, entries => {
  const bounds = entries[0]?.contentRect
  if (bounds) size.value = { width: bounds.width, height: bounds.height }
})
const viewportStyle = computed(() => {
  const { width, height } = viewport.value
  const scale = Math.min(size.value.width / width, size.value.height / height)
  return {
    width: `${width}px`, height: `${height}px`,
    transform: `translate(-50%, -50%) scale(${scale})`,
  }
})
</script>
