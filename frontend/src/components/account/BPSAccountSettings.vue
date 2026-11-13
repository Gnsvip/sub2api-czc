<template>
  <fieldset class="space-y-3 rounded-lg border border-gray-200 p-4 dark:border-dark-600">
    <legend class="px-1 font-medium">Excel / BPS</legend>
    <label v-if="!optionsOnly" class="flex items-center gap-2"><input v-model="value.enabled" type="checkbox" data-testid="bps-enabled" />启用 BPS 协议</label>
    <p class="text-xs text-gray-500">需同时开启系统 BPS 开关；需要账号具备 BPS 授权。保存设置不会替换 OAuth 凭据。</p>
    <div v-if="templateControls" class="flex flex-wrap gap-2">
      <button type="button" class="btn btn-secondary" :disabled="loadingTemplate" @click="applyTemplate">应用已保存模板</button>
      <button type="button" class="btn btn-secondary" @click="applyInitial">使用初始设置</button>
    </div>
    <p v-if="templateError" role="alert" class="text-xs text-red-600">{{ templateError }}</p>
    <template v-if="value.enabled || recoveryPending || optionsOnly">
      <label class="flex items-center gap-2"><input v-model="value.allModels" type="checkbox" data-testid="bps-all-models" />所有文本模型使用 BPS</label>
      <ModelWhitelistSelector v-if="!value.allModels" v-model="value.models" platform="openai" />
      <p class="text-xs text-gray-500">生图需单独选择 gpt-image-2；取消全选且未选择模型时，不使用 BPS。</p>
      <label class="flex items-center gap-2"><input v-model="value.sessionProxy" type="checkbox" data-testid="bps-session-proxy" />按会话分配代理 IP</label>
      <select v-if="value.sessionProxy" v-model="value.proxySource" class="input" aria-label="BPS 代理来源"><option value="mihomo">Mihomo 节点池</option><option value="ip_pool">IP 管理中的代理池</option></select>
      <label class="flex items-center gap-2"><input v-model="value.autoDisable" type="checkbox" />遇到 BPS 403 自动关闭协议</label>
      <label class="flex items-center gap-2"><input v-model="value.autoRecover" type="checkbox" :disabled="!value.autoDisable" />403 后自动探测恢复</label>
      <label v-if="value.autoRecover && value.autoDisable" class="block">恢复间隔（分钟）<input v-model.number="value.recoveryMinutes" class="input" type="number" min="1" max="10080" step="1" required /></label>
      <label class="flex items-center gap-2"><input v-model="value.autoMove" type="checkbox" />遇到 BPS 403 自动调整分组</label>
      <select v-if="value.autoMove" v-model="value.targetGroup" class="input" required aria-label="403 目标分组"><option disabled value="">选择目标</option><option :value="0">退出所有分组</option><option v-for="group in groups.filter(g => g.platform === 'openai' || g.platform === 'composite')" :key="group.id" :value="group.id">{{ group.name }}</option></select>
      <p v-if="value.autoMove" class="text-xs text-gray-500">替换全部原分组；恢复 BPS 时不会还原分组。</p>
      <label class="flex items-center gap-2"><input v-model="value.cacheCreationAsInput" type="checkbox" />缓存创建按普通输入计费</label>
      <label class="flex items-center gap-2"><input v-model="value.omitTools" type="checkbox" />忽略 BPS 不支持的托管工具</label>
      <label class="flex items-center gap-2"><input v-model="value.ignoreEncrypted" type="checkbox" />用缺失提示替代不支持的加密历史</label>
      <p v-if="value.ignoreEncrypted || value.omitTools" class="text-xs text-amber-600">这些兼容选项会省略不支持的历史内容或工具，默认关闭。</p>
    </template>
    <div v-if="policyControls && value.autoPolicy" class="space-y-2 border-t pt-3">
      <label class="flex items-center gap-2"><input v-model="value.autoPolicy.enabled" type="checkbox" />按 Astra 检测 / 用量自动开启 BPS</label>
      <template v-if="value.autoPolicy.enabled">
        <p class="text-xs text-gray-500">仅统计保存后的新 Astra 结果，不使用鹈鹕评分；不修改原有自动换组。仍需系统 BPS 开关开启。</p>
        <label class="block text-sm">连续降智次数（0 为不使用）<input v-model.number="value.autoPolicy.failureThreshold" class="input" type="number" min="0" max="100" /></label>
        <label class="block text-sm">5h / 7d 已用额度达到百分比（0 为不使用）<input v-model.number="value.autoPolicy.usagePercent" class="input" type="number" min="0" max="100" /></label>
        <label class="flex items-center gap-2"><input v-model="value.autoPolicy.requireAll" type="checkbox" />两个条件都满足才开启</label>
        <label class="flex items-center gap-2"><input v-model="value.autoPolicy.autoRestore" type="checkbox" />恢复健康后还原原设置</label>
        <template v-if="value.autoPolicy.autoRestore">
          <label class="block text-sm">连续健康次数<input v-model.number="value.autoPolicy.passThreshold" class="input" type="number" min="1" max="100" /></label>
          <label class="flex items-center gap-2"><input v-model="value.autoPolicy.holdOnUsage" type="checkbox" />额度仍高于阈值时保持 BPS</label>
        </template>
        <p class="text-xs text-gray-500">下方是自动开启时使用的设置。手动修改 BPS 或因 403 关闭后，规则不会覆盖；关闭自动规则只停止后续动作。</p>
        <BPSAccountSettings v-model="value.autoPolicy.options" :groups="groups" :policy-controls="false" :options-only="true" />
      </template>
    </div>
  </fieldset>
</template>

<script setup lang="ts">
import ModelWhitelistSelector from './ModelWhitelistSelector.vue'
import { onBeforeUnmount, ref, watch } from 'vue'
import apiClient from '@/api/client'
import { extractApiErrorMessage } from '@/utils/apiError'
import { fromBPSDefaults, initialBPSDefaults, type BPSDefaults, type BPSSettings } from './bpsSettings'
const value = defineModel<BPSSettings>({ required: true })
withDefaults(defineProps<{ groups: { id: number; name: string; platform: string }[]; recoveryPending?: boolean; templateControls?: boolean; policyControls?: boolean; optionsOnly?: boolean }>(), {templateControls:true,policyControls:true,optionsOnly:false})
const loadingTemplate=ref(false), templateError=ref('')
let generation=0
watch(value, (next, previous)=>{if(next!==previous){generation++;loadingTemplate.value=false;templateError.value=''}},{flush:'sync'})
onBeforeUnmount(()=>{generation++})
function applyInitial(){generation++;loadingTemplate.value=false;templateError.value='';value.value={...initialBPSDefaults(),autoPolicy:value.value.autoPolicy}}
async function applyTemplate(){
 const version=++generation, snapshot=JSON.stringify(value.value);loadingTemplate.value=true;templateError.value=''
 try {const {data}=await apiClient.get<BPSDefaults>('/admin/openai/bps-defaults');if(version===generation){if(JSON.stringify(value.value)===snapshot)value.value={...fromBPSDefaults(data),autoPolicy:value.value.autoPolicy};else templateError.value='表单已修改，请重新应用模板'}}
 catch(e){if(version===generation)templateError.value=extractApiErrorMessage(e,'读取模板失败')}
 finally{if(version===generation)loadingTemplate.value=false}
}
</script>
