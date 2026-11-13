<template>
  <section class="card space-y-4 p-6">
    <h2 class="text-lg font-semibold">Excel BPS</h2>
    <p v-if="error" role="alert" class="text-sm text-red-600">{{ error }}</p>
    <p v-if="loading" role="status">{{ text('加载中…', 'Loading…') }}</p>
    <template v-else>
      <label class="flex items-center gap-2"><input v-model="form.excel_bps_enabled" type="checkbox" />{{ text('启用 BPS 转发', 'Enable BPS forwarding') }}</label>
      <p class="text-xs text-gray-500">{{ text('默认关闭。开启后，还需在账号编辑中授权并选择 BPS 模型。', 'Disabled by default. Authorize an account and select its BPS models before use.') }}</p>
      <label class="flex items-center gap-2"><input v-model="form.excel_bps_image_relay_enabled" type="checkbox" />{{ text('允许图片输入', 'Allow image input') }}</label>
      <template v-if="form.excel_bps_image_relay_enabled">
        <label class="block">{{ text('图片传输', 'Image transport') }}
          <select v-model="form.excel_bps_image_mode" class="input"><option value="native">{{ text('原生附件上传', 'Native attachments') }}</option><option value="relay">{{ text('临时 HTTPS 中转', 'Temporary HTTPS relay') }}</option></select>
        </label>
        <label v-if="form.excel_bps_image_mode === 'relay'" class="block">{{ text('中转公开地址', 'Public relay origin') }}<input v-model="form.excel_bps_image_base_url" class="input" type="url" placeholder="https://api.example.com" /></label>
        <label class="block">{{ text('历史图片达到上限', 'Image history limit') }}<select v-model="form.excel_bps_image_limit_policy" class="input"><option value="off">{{ text('拒绝超限请求', 'Reject over-limit requests') }}</option><option value="auto_compact">{{ text('自动压缩（Codex）', 'Auto compact (Codex)') }}</option><option value="warn">{{ text('提前提醒压缩', 'Warn before limit') }}</option></select></label>
        <div class="grid gap-3 sm:grid-cols-2">
          <label v-for="field in limits" :key="field.key" class="block text-sm">{{ text(field.zh, field.en) }}<input v-model.number="form[field.key]" class="input" type="number" :min="field.min" :max="field.max" step="1" /></label>
        </div>
      </template>
      <button type="button" class="btn btn-primary" :disabled="saving" @click="save">{{ saving ? text('保存中…','Saving…') : text('保存 BPS 设置','Save BPS settings') }}</button>
      <p v-if="saved" role="status" class="text-sm text-green-600">{{ text('已保存','Saved') }}</p>
    </template>
  </section>
</template>
<script setup lang="ts">
import { onMounted, reactive, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { apiClient } from '@/api/client'
import { excelBPSImageLimits as cap } from '@/utils/excelBPSImageLimits'
const { locale } = useI18n()
const text = (zh: string, en: string) => locale.value.startsWith('zh') ? zh : en
const loading=ref(true), saving=ref(false), saved=ref(false), error=ref('')
const form=reactive<Record<string, string | number | boolean>>({
 excel_bps_enabled:false, excel_bps_image_relay_enabled:false, excel_bps_image_mode:'native', excel_bps_image_base_url:'', excel_bps_image_limit_policy:'off',
 excel_bps_image_body_limit_mib:64, excel_bps_image_budget_mib:1024, excel_bps_image_max_requests:128,
 excel_bps_image_max_image_mib:20, excel_bps_image_max_images:20, excel_bps_image_max_total_mib:32,
 excel_bps_image_storage_mib:1024, excel_bps_image_storage_entries:512, excel_bps_image_ttl_minutes:30,
 excel_bps_image_warning_remaining:8, excel_bps_image_compact_reserve:3,
})
const limits=[
 {key:'excel_bps_image_body_limit_mib',zh:'请求大小（MiB）',en:'Request limit (MiB)',min:1,max:cap.bodyMiB},
 {key:'excel_bps_image_budget_mib',zh:'并发内存预算（MiB）',en:'Memory budget (MiB)',min:cap.minBudgetMiB,max:cap.budgetMiB},
 {key:'excel_bps_image_max_requests',zh:'图片并发请求',en:'Concurrent image requests',min:1,max:cap.requests},
 {key:'excel_bps_image_max_image_mib',zh:'单图大小（MiB）',en:'Per-image limit (MiB)',min:1,max:cap.imageMiB},
 {key:'excel_bps_image_max_images',zh:'单次图片数',en:'Images per request',min:1,max:cap.images},
 {key:'excel_bps_image_max_total_mib',zh:'图片总大小（MiB）',en:'Total images (MiB)',min:1,max:cap.totalMiB},
 {key:'excel_bps_image_storage_mib',zh:'临时存储（MiB）',en:'Relay storage (MiB)',min:1,max:cap.storageMiB},
 {key:'excel_bps_image_storage_entries',zh:'临时图片条目数',en:'Relay entries',min:1,max:cap.storageEntries},
 {key:'excel_bps_image_ttl_minutes',zh:'临时图片保留（分钟）',en:'Relay retention (minutes)',min:1,max:cap.ttlMinutes},
 {key:'excel_bps_image_warning_remaining',zh:'剩余多少张时提醒',en:'Warn at remaining slots',min:1,max:cap.images},
 {key:'excel_bps_image_compact_reserve',zh:'预留压缩图片数',en:'Reserved compact slots',min:1,max:cap.images},
]
onMounted(async()=>{try{const {data}=await apiClient.get<Record<string,unknown>>('/admin/settings');for(const key of Object.keys(form)){if(typeof data[key]===typeof form[key])form[key]=data[key] as string|number|boolean}}catch{error.value=text('加载失败，请重试','Failed to load settings')}finally{loading.value=false}})
async function save(){saving.value=true;saved.value=false;error.value='';try{await apiClient.put('/admin/settings',{...form});saved.value=true}catch(e){const detail=e as {response?:{data?:{message?:string}}};error.value=detail.response?.data?.message||text('保存失败，请检查设置','Could not save settings')}finally{saving.value=false}}
</script>
