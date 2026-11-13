<template>
 <section class="card space-y-3 p-6">
  <h2 class="text-lg font-semibold">BPS 默认模板</h2>
  <p class="text-xs text-gray-500">用于账号表单的“应用已保存模板”。保存模板不会自动修改或启用任何账号。</p>
  <p v-if="error" role="alert" class="text-sm text-red-600">{{ error }}</p>
  <template v-if="loaded">
   <BPSAccountSettings v-model="form" :groups="groups" :template-controls="false" :policy-controls="false" :options-only="true" />
   <button type="button" class="btn btn-primary" :disabled="saving" @click="save">保存默认模板</button>
   <p v-if="saved" role="status" class="text-sm text-green-600">模板已保存</p>
  </template>
 </section>
</template>
<script setup lang="ts">
import { onMounted, ref } from 'vue'
import apiClient from '@/api/client'
import { extractApiErrorMessage } from '@/utils/apiError'
import BPSAccountSettings from './BPSAccountSettings.vue'
import { bpsDefaultsPayload, fromBPSDefaults, initialBPSDefaults, type BPSDefaults } from './bpsSettings'
const form=ref(initialBPSDefaults()),groups=ref<{id:number;name:string;platform:string}[]>([])
const loaded=ref(false),saving=ref(false),saved=ref(false),error=ref('')
onMounted(async()=>{
 try {const [defaults, available]=await Promise.all([apiClient.get<BPSDefaults>('/admin/openai/bps-defaults'),apiClient.get('/admin/groups/all')]);form.value=fromBPSDefaults(defaults.data);groups.value=available.data;loaded.value=true}
 catch(e){error.value=extractApiErrorMessage(e,'读取模板失败')}
})
async function save(){saving.value=true;saved.value=false;error.value='';try{await apiClient.put('/admin/openai/bps-defaults',bpsDefaultsPayload(form.value));saved.value=true}catch(e){error.value=extractApiErrorMessage(e,'保存模板失败')}finally{saving.value=false}}
</script>
