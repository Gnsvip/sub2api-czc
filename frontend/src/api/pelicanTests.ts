import { apiClient } from './client'
import type { PelicanVisualJudgement } from './intelligentTests'

export interface PelicanTestItem {
  visual_judgement?: PelicanVisualJudgement
  id: number
  status: string
  html: string
  preview_issue?: 'incomplete' | 'unavailable' | 'scripts_removed'
  prompt: string
  model: string
  group_name: string
  reasoning_effort: string
  created_at: string
  finished_at?: string | null
  duration_ms: number
}

export interface PelicanTestPage {
  items: PelicanTestItem[]
  total: number
  page: number
  page_size: number
}

export const pelicanTestsAPI = {
  async visibility() {
    return (await apiClient.get<{ visible: boolean }>('/pelican-tests/visibility')).data
  },
  async list(page = 1, pageSize = 12) {
    return (await apiClient.get<PelicanTestPage>('/pelican-tests', { params: { page, page_size: pageSize } })).data
  }
}
