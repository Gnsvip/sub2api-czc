export interface BPSSettings {
  autoPolicy?: BPSAutoPolicy
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
  ignoreEncrypted: boolean
}

export function readBPSSettings(extra: Record<string, unknown> = {}, includePolicy = true): BPSSettings {
  return {
    ...(includePolicy ? { autoPolicy: readAutoPolicy(extra.openai_excel_bps_auto_policy) } : {}),
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
    ignoreEncrypted: extra.openai_excel_bps_ignore_encrypted_content === true
  }
}

export function writeBPSSettings(value: BPSSettings): Record<string, unknown> {
  return {
    ...(value.autoPolicy ? { openai_excel_bps_auto_policy: autoPolicyPayload(value.autoPolicy) } : {}),
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
    openai_excel_bps_ignore_encrypted_content: value.ignoreEncrypted
  }
}

export interface BPSAutoPolicy {
 enabled: boolean; autoRestore: boolean; failureThreshold: number; passThreshold: number
 usagePercent: number; requireAll: boolean; holdOnUsage: boolean; options: BPSSettings
}
export interface BPSDefaults {
 all_models: boolean; models: string[]; session_proxy: boolean; proxy_source: 'mihomo'|'ip_pool'
 cache_creation_as_input: boolean; auto_disable_on_403: boolean; auto_recover_on_403: boolean
 recovery_interval_minutes: number; auto_move_on_403: boolean; target_group_id: number
 omit_unsupported_tools: boolean; ignore_encrypted_content: boolean
}
export function bpsDefaultsPayload(v: BPSSettings): BPSDefaults {
 return {all_models:v.allModels,models:[...new Set(v.models.map(x=>x.trim()).filter(Boolean))],session_proxy:v.sessionProxy,proxy_source:v.proxySource,
 cache_creation_as_input:v.cacheCreationAsInput,auto_disable_on_403:v.autoDisable,auto_recover_on_403:v.autoDisable&&v.autoRecover,
 recovery_interval_minutes:v.recoveryMinutes,auto_move_on_403:v.autoMove,target_group_id:v.targetGroup===''?-1:v.targetGroup,
 omit_unsupported_tools:v.omitTools,ignore_encrypted_content:v.ignoreEncrypted}
}
export function fromBPSDefaults(d: BPSDefaults): BPSSettings {
 return {enabled:true,allModels:d.all_models,models:Array.isArray(d.models)?[...d.models]:[],sessionProxy:d.session_proxy,proxySource:d.proxy_source,
 cacheCreationAsInput:d.cache_creation_as_input,autoDisable:d.auto_disable_on_403,autoRecover:d.auto_recover_on_403,
 recoveryMinutes:d.recovery_interval_minutes,autoMove:d.auto_move_on_403,targetGroup:d.target_group_id<0?'':d.target_group_id,
 omitTools:d.omit_unsupported_tools,ignoreEncrypted:d.ignore_encrypted_content}
}
export function initialBPSDefaults(): BPSSettings {
 return {...readBPSSettings({}, false),enabled:true,allModels:false,models:['gpt-6-astra','gpt-5.6-sol','gpt-5.6-terra']}
}
function readAutoPolicy(raw: unknown): BPSAutoPolicy {
 const p = raw && typeof raw==='object' ? raw as Record<string,unknown> : {}
 const b = p.bps && typeof p.bps==='object' ? p.bps as Record<string,unknown> : {}
 const defaults: BPSDefaults = { ...bpsDefaultsPayload(initialBPSDefaults()),ignore_encrypted_content:true,auto_disable_on_403:true,cache_creation_as_input:true,...b } as BPSDefaults
 return {enabled:p.enabled===true,autoRestore:p.auto_restore===true,
 failureThreshold:typeof b.failure_threshold==='number'?b.failure_threshold:2,passThreshold:typeof b.pass_threshold==='number'?b.pass_threshold:2,
 usagePercent:typeof b.usage_percent==='number'?b.usage_percent:0,requireAll:b.require_all===true,holdOnUsage:b.hold_on_usage===true,options:fromBPSDefaults(defaults)}
}
function autoPolicyPayload(p: BPSAutoPolicy) {
 return {enabled:p.enabled,auto_restore:p.autoRestore,bps:{...bpsDefaultsPayload(p.options),failure_threshold:p.failureThreshold,
 pass_threshold:p.passThreshold,usage_percent:p.usagePercent,require_all:p.requireAll,hold_on_usage:p.holdOnUsage}}
}
