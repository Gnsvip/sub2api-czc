<template>
  <CustomGuideView v-if="published?.display_mode === 'custom' && published.has_custom_content" :published="published" />
  <iframe
    v-else-if="loaded"
    ref="guideFrame"
    :src="guideSource"
    title="AIFXO 使用教程"
    data-test="aifxo-guide-frame"
    class="aifxo-guide-frame"
    allow="clipboard-write"
  />
  <div v-else class="guide-loading" role="status">正在加载使用教程…</div>
</template>

<script setup lang="ts">
import { onBeforeUnmount, onMounted, ref } from 'vue'
import { getPublicGuide, type PublicGuide } from '@/api/guide'
import CustomGuideView from './CustomGuideView.vue'

const published = ref<PublicGuide | null>(null)
const loaded = ref(false)
const guideFrame = ref<HTMLIFrameElement | null>(null)
// This separately built document is repository-maintained, never administrator HTML.
// Its external module works with the production nonce CSP without unsafe-inline.
const initialHash = /^#[a-z0-9-]*$/.test(window.location.hash) ? window.location.hash : ''
const guideSource = `/guide-content/${initialHash}`

function onGuideMessage(event: MessageEvent) {
  const sourceWindow = guideFrame.value?.contentWindow
  if (!sourceWindow || event.origin !== window.location.origin || event.source !== sourceWindow) return
  if (event.data?.type !== 'aifxo-guide-route' || typeof event.data.hash !== 'string') return
  const hash = event.data.hash
  if (hash !== '' && !/^#[a-z0-9-]*$/.test(hash)) return
  if (window.location.hash !== hash) {
    window.history.replaceState(window.history.state, '', `${window.location.pathname}${window.location.search}${hash}`)
  }
  if (typeof event.data.title === 'string') document.title = event.data.title
}

function syncHashToGuide() {
  guideFrame.value?.contentWindow?.postMessage({
    type: 'aifxo-guide-navigate',
    hash: window.location.hash || '#overview',
  }, window.location.origin)
}

onMounted(async () => {
  window.addEventListener('message', onGuideMessage)
  window.addEventListener('hashchange', syncHashToGuide)
  try {
    published.value = await getPublicGuide()
  } catch {
    // Keep the shipped tutorial available when its optional settings API fails.
  } finally {
    loaded.value = true
  }
})

onBeforeUnmount(() => {
  window.removeEventListener('message', onGuideMessage)
  window.removeEventListener('hashchange', syncHashToGuide)
})
</script>

<style scoped>
.aifxo-guide-frame {
  display: block;
  width: 100%;
  height: 100dvh;
  border: 0;
  background: #fff;
}
.guide-loading {
  display: grid;
  min-height: 100dvh;
  place-items: center;
  color: #697870;
  background: #fff;
}
</style>
