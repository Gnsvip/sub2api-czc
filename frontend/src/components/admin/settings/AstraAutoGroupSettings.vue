<template>
  <div class="space-y-4 rounded-xl border border-gray-200 p-4 dark:border-dark-700" data-testid="astra-auto-group-settings">
    <div>
      <h3 class="font-medium text-gray-900 dark:text-white">{{ t('admin.settings.astraAutoGroup.title') }}</h3>
      <p class="mt-1 text-sm text-gray-500">{{ t('admin.settings.astraAutoGroup.description') }}</p>
    </div>
    <p v-if="loading" class="text-sm text-gray-500">{{ t('common.loading') }}</p>
    <fieldset :disabled="loading || saving || !loaded" class="space-y-4">
      <label class="flex items-center gap-2 text-sm">
        <input v-model="draft.enabled" type="checkbox" data-testid="astra-auto-group-enabled" class="rounded text-primary-600" @change="saved = false" />
        {{ t('admin.settings.astraAutoGroup.enabled') }}
      </label>
      <div class="grid gap-4 sm:grid-cols-2">
        <div class="text-sm">
          <p>{{ t('admin.settings.astraAutoGroup.healthy') }} <span class="text-gray-500">({{ draft.healthy_group_ids.length }})</span></p>
          <div class="mt-2 max-h-56 space-y-2 overflow-y-auto rounded-lg border border-gray-200 p-3 dark:border-dark-600" data-testid="astra-auto-group-healthy">
            <label v-for="group in groups" :key="group.id" class="flex items-center gap-2">
              <input v-model="draft.healthy_group_ids" type="checkbox" :value="group.id" :data-testid="`astra-normal-${group.id}`" class="rounded text-primary-600" @change="saved = false" />
              {{ group.name }}
            </label>
            <label v-for="id in unavailableNormalIDs" :key="id" class="flex items-center gap-2 text-amber-600">
              <input v-model="draft.healthy_group_ids" type="checkbox" :value="id" @change="saved = false" />
              #{{ id }} · {{ t('admin.settings.astraAutoGroup.unavailable') }}
            </label>
          </div>
        </div>
        <label class="block text-sm">
          {{ t('admin.settings.astraAutoGroup.degraded') }}
          <select v-model.number="draft.degraded_group_id" data-testid="astra-auto-group-degraded" class="input mt-2" @change="saved = false">
            <option :value="0">{{ t('admin.settings.astraAutoGroup.choose') }}</option>
            <option v-for="group in groups" :key="group.id" :value="group.id">{{ group.name }}</option>
            <option v-if="draft.degraded_group_id && !groups.some(group => group.id === draft.degraded_group_id)" :value="draft.degraded_group_id" disabled>{{ t('admin.settings.astraAutoGroup.unavailable') }}</option>
          </select>
        </label>
      </div>
      <p class="text-xs leading-relaxed text-gray-500">{{ t('admin.settings.astraAutoGroup.scope') }}</p>
      <p v-if="draft.enabled && !validTargets" class="text-sm text-amber-600">{{ t('admin.settings.astraAutoGroup.invalid') }}</p>
      <div class="flex items-center justify-end gap-3">
        <span v-if="saved" role="status" class="text-sm text-emerald-600">{{ t('admin.settings.astraAutoGroup.saved') }}</span>
        <button type="button" class="btn btn-primary btn-sm" data-testid="astra-auto-group-save" :disabled="draft.enabled && !validTargets" @click="save">
          {{ saving ? t('common.saving') : t('admin.settings.astraAutoGroup.save') }}
        </button>
      </div>
    </fieldset>
    <p v-if="error" role="alert" class="text-sm text-red-600">{{ error }}</p>
    <button v-if="!loaded && !loading" type="button" class="btn btn-secondary btn-sm" @click="load">{{ t('admin.settings.astraAutoGroup.retry') }}</button>
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, reactive, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { adminAPI } from '@/api'
import type { AstraAutoGroupSettings } from '@/api/admin/settings'
import type { AdminGroup } from '@/types'
import { extractApiErrorMessage } from '@/utils/apiError'

const { t } = useI18n()
const draft = reactive<AstraAutoGroupSettings & { healthy_group_ids: number[] }>({ enabled: false, healthy_group_id: 0, healthy_group_ids: [], degraded_group_id: 0 })
const groups = ref<AdminGroup[]>([])
const loading = ref(true), loaded = ref(false), saving = ref(false), saved = ref(false), error = ref('')
const unavailableNormalIDs = computed(() => draft.healthy_group_ids.filter(id => !groups.value.some(group => group.id === id)))
const validTargets = computed(() => draft.healthy_group_ids.length > 0 && !draft.healthy_group_ids.includes(draft.degraded_group_id) &&
  [...draft.healthy_group_ids, draft.degraded_group_id].every(id => groups.value.some(group => group.id === id)))

async function load() {
  loading.value = true
  error.value = ''
  try {
    const [settings, availableGroups] = await Promise.all([
      adminAPI.settings.getAstraAutoGroupSettings(), adminAPI.groups.getAll('openai'),
    ])
    Object.assign(draft, settings)
    draft.healthy_group_ids = [...(settings.healthy_group_ids ?? (settings.healthy_group_id ? [settings.healthy_group_id] : []))]
    draft.healthy_group_id = 0
    groups.value = availableGroups.filter(group => group.platform === 'openai' && group.status === 'active')
    loaded.value = true
  } catch (err) {
    error.value = extractApiErrorMessage(err, t('admin.settings.astraAutoGroup.loadFailed'))
  } finally {
    loading.value = false
  }
}

async function save() {
  if (!loaded.value || saving.value || (draft.enabled && !validTargets.value)) return
  saving.value = true
  saved.value = false
  error.value = ''
  try {
    const result = await adminAPI.settings.updateAstraAutoGroupSettings({ ...draft, healthy_group_ids: [...draft.healthy_group_ids] })
    Object.assign(draft, result)
    saved.value = true
  } catch (err) {
    error.value = extractApiErrorMessage(err, t('admin.settings.astraAutoGroup.saveFailed'))
  } finally {
    saving.value = false
  }
}

onMounted(load)
</script>
