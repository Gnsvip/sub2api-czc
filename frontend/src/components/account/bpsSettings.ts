export interface BPSSettings {
  enabled: boolean
  allModels: boolean
  models: string[]
  sessionProxy: boolean
  proxySource: 'mihomo' | 'ip_pool'
  cacheCreationAsInput: boolean
  autoDisable: boolean
  autoRecover: boolean
  recoveryMinutes: number
  autoMove: boolean
  targetGroup: number | ''
  omitTools: boolean
  ignoreImages: boolean
  ignoreEncrypted: boolean
}

export function readBPSSettings(extra: Record<string, unknown> = {}): BPSSettings {
  return {
    enabled: extra.openai_excel_bps === true,
    allModels: !Object.prototype.hasOwnProperty.call(extra, 'openai_excel_bps_models') || extra.openai_excel_bps_models === null,
    models: Array.isArray(extra.openai_excel_bps_models) ? extra.openai_excel_bps_models.filter((v): v is string => typeof v === 'string') : [],
    sessionProxy: extra.openai_excel_bps_mihomo === true || extra.openai_excel_bps_session_proxy === true,
    proxySource: extra.openai_excel_bps_proxy_source === 'ip_pool' || (!extra.openai_excel_bps_proxy_source && extra.openai_excel_bps_session_proxy === true) ? 'ip_pool' : 'mihomo',
    cacheCreationAsInput: extra.openai_excel_bps_cache_creation_as_input === true,
    autoDisable: extra.openai_excel_bps_auto_disable_on_403 === true,
    autoRecover: extra.openai_excel_bps_auto_recover_on_403 === true,
    recoveryMinutes: typeof extra.openai_excel_bps_403_recovery_interval_minutes === 'number' ? extra.openai_excel_bps_403_recovery_interval_minutes : 60,
    autoMove: extra.openai_excel_bps_auto_move_on_403 === true,
    targetGroup: typeof extra.openai_excel_bps_403_target_group_id === 'number' ? extra.openai_excel_bps_403_target_group_id : '',
    omitTools: extra.openai_excel_bps_omit_unsupported_tools === true,
    ignoreImages: extra.openai_excel_bps_ignore_images === true,
    ignoreEncrypted: extra.openai_excel_bps_ignore_encrypted_content === true
  }
}

export function writeBPSSettings(value: BPSSettings): Record<string, unknown> {
  return {
    openai_excel_bps: value.enabled,
    openai_excel_bps_models: value.allModels ? null : [...new Set(value.models)],
    openai_excel_bps_mihomo: value.sessionProxy,
    openai_excel_bps_session_proxy: false,
    openai_excel_bps_proxy_source: value.proxySource,
    openai_excel_bps_cache_creation_as_input: value.cacheCreationAsInput,
    openai_excel_bps_auto_disable_on_403: value.autoDisable,
    openai_excel_bps_auto_recover_on_403: value.autoDisable && value.autoRecover,
    openai_excel_bps_403_recovery_interval_minutes: value.recoveryMinutes,
    openai_excel_bps_auto_move_on_403: value.autoMove,
    openai_excel_bps_403_target_group_id: value.targetGroup === '' ? null : value.targetGroup,
    openai_excel_bps_omit_unsupported_tools: value.omitTools,
    openai_excel_bps_ignore_images: value.ignoreImages,
    openai_excel_bps_ignore_encrypted_content: value.ignoreEncrypted
  }
}
