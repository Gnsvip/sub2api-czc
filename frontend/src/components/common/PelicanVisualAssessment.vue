<template>
  <div class="space-y-2 text-sm" data-testid="pelican-visual-assessment">
    <span class="inline-flex items-center gap-1 rounded-md px-2 py-1 text-xs font-medium" :class="tone" role="status">
      {{ label }}<template v-if="judged"> · {{ assessment?.score?.toFixed(1) }} 分</template>
    </span>
    <template v-if="!compact">
      <p v-if="assessment?.reason" class="whitespace-pre-wrap break-words text-gray-600 dark:text-gray-300">{{ assessment.reason }}</p>
      <p v-if="judged" class="text-xs text-gray-500">通过线 {{ assessment?.threshold }} 分 · auto-pelican · {{ assessment?.judge_model }} · {{ assessment?.sample_count }} 次评审</p>
      <details v-if="judged && assessment?.dimensions" class="text-xs">
        <summary class="cursor-pointer text-gray-500">查看评分依据</summary>
        <dl class="mt-2 space-y-3">
          <div v-for="(dimension, key) in assessment.dimensions" :key="key">
            <dt class="font-medium text-gray-700 dark:text-gray-200">{{ dimensionNames[key] || key }} · {{ dimension.score.toFixed(2) }} / 5</dt>
            <dd class="mt-1 whitespace-pre-wrap break-words text-gray-500">{{ dimension.reason }}</dd>
          </div>
        </dl>
      </details>
      <p v-if="assessment" class="text-xs text-gray-400">仅评价本次画面，不代表模型整体能力。</p>
    </template>
  </div>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import type { PelicanVisualJudgement } from '@/api/intelligentTests'
const props = defineProps<{ assessment?: PelicanVisualJudgement; compact?: boolean }>()
const judged = computed(() => !!props.assessment && ['passed', 'anomalous'].includes(props.assessment.state) && Number.isFinite(props.assessment.score))
const label = computed(() => !props.assessment ? '未评分' : !judged.value ? '评分未完成' : props.assessment.state === 'passed' ? '画面测试通过' : '画面异常')
const tone = computed(() => !judged.value ? 'bg-gray-100 text-gray-600 dark:bg-dark-700 dark:text-gray-300'
  : props.assessment?.state === 'passed' ? 'bg-emerald-50 text-emerald-800 dark:bg-emerald-900/20 dark:text-emerald-300'
    : 'bg-rose-50 text-rose-800 dark:bg-rose-900/20 dark:text-rose-300')
const dimensionNames: Record<string, string> = {
  prompt_fidelity: '题目符合程度', object_quality: '动物和自行车', interaction_spatial_correctness: '骑行姿势与空间关系',
  animation_quality: '动画动作', visual_coherence: '画面连贯性', aesthetic_quality: '美观程度'
}
</script>
