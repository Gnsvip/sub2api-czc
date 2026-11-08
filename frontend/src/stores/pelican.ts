import { defineStore } from 'pinia'
import { ref } from 'vue'
import { pelicanTestsAPI } from '@/api/pelicanTests'

export const usePelicanStore = defineStore('pelican', () => {
  const visible = ref(false)
  let revision = 0
  let request: Promise<boolean> | null = null

  function setVisible(value: boolean) {
    revision++
    visible.value = value
  }

  function refresh(): Promise<boolean> {
    if (request) return request
    const started = revision
    request = pelicanTestsAPI.visibility()
      .then(data => {
        if (revision === started) visible.value = data.visible === true
        return visible.value
      })
      .catch(() => {
        if (revision === started) visible.value = false
        return visible.value
      })
      .finally(() => { request = null })
    return request
  }

  return { visible, setVisible, refresh }
})
