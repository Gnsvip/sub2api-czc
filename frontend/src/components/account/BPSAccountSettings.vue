<template>
  <fieldset class="space-y-3 rounded-lg border border-gray-200 p-4 dark:border-dark-600">
    <legend class="px-1 font-medium">Excel / BPS</legend>
    <label class="flex items-center gap-2"><input v-model="value.enabled" type="checkbox" data-testid="bps-enabled" />启用 BPS 协议</label>
    <p class="text-xs text-gray-500">需同时开启系统 BPS 开关；需要账号具备 BPS 授权。保存设置不会替换 OAuth 凭据。</p>
    <template v-if="value.enabled || recoveryPending">
      <label class="flex items-center gap-2"><input v-model="value.allModels" type="checkbox" />所有文本模型使用 BPS</label>
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
  </fieldset>
</template>

<script setup lang="ts">
import ModelWhitelistSelector from './ModelWhitelistSelector.vue'
import type { BPSSettings } from './bpsSettings'
const value = defineModel<BPSSettings>({ required: true })
defineProps<{ groups: { id: number; name: string; platform: string }[]; recoveryPending?: boolean }>()
</script>
