import apiClient from '../client'

export interface CodexLoginTask {
  id: number
  account_id: number
  status: 'queued' | 'running' | 'succeeded' | 'failed' | 'cancelled'
  stage: string
  error_code?: string
  diagnostic?: {
    stage: string
    action: string
    reason: string
    elapsed_ms: number
    action_ms: number
    http_status: number
    response: 'none' | 'document' | 'auth_request' | 'token'
  }
}
export interface CodexLoginStatus {
  config: { enabled: boolean; login_email: string; password_configured: boolean; totp_configured: boolean; revision: number; preferred_workspace_id?: string }
  task: CodexLoginTask | null
  runtime: { mode: string; state: string; reason?: string }
}
export interface CodexLoginResult {
  line: number
  email: string
  account_id?: number
  task?: CodexLoginTask | null
  error_code?: string
}
export interface CodexLoginCreateOptions {
  preferred_workspace_id?: string
  name?: string
  notes?: string
  group_ids: number[]
  proxy_ids: number[]
  concurrency: number
  priority: number
  rate_multiplier?: number
  load_factor?: number | null
  extra?: Record<string, unknown>
  credential_extras?: Record<string, unknown>
  expires_at?: number | null
  auto_pause_on_expired?: boolean
  skip_default_group_bind?: boolean
}
export async function createCodexLoginAccounts(options: CodexLoginCreateOptions, content: string, autoRecover: boolean) {
  const { data } = await apiClient.post<{ results: CodexLoginResult[]; runtime: CodexLoginStatus['runtime'] }>(
    '/admin/openai/automatic-authorization', { ...options, content, auto_recover: autoRecover }
  )
  return data
}
export async function getCodexLoginStatus(id: number) {
  const { data } = await apiClient.get<CodexLoginStatus>(`/admin/openai/accounts/${id}/codex-login`)
  return data
}
export async function saveCodexLogin(id: number, input: { enabled: boolean; login_email: string; password?: string; totp_secret?: string; clear_totp?: boolean; revision: number; preferred_workspace_id?: string }) {
  const { data } = await apiClient.put<CodexLoginStatus>(`/admin/openai/accounts/${id}/codex-login`, input)
  return data
}
export async function runCodexLogin(id: number) {
  const { data } = await apiClient.post<CodexLoginTask>(`/admin/openai/accounts/${id}/codex-login/run`)
  return data
}

export function codexLoginError(code?: string): string {
  const messages: Record<string, string> = {
    account_exists: '账号已存在，请在编辑账号中配置或重新授权',
    password_rejected: '密码不正确，请修改登录资料',
    totp_rejected: '2FA 验证失败，请检查 TOTP 密钥',
    identity_mismatch: '登录身份或所选工作区校验失败',
    workspace_unavailable: '未找到可用或指定的工作区，请检查设置或手动授权',
    workspace_selection_required: '有多个工作区，请填写工作区 ID 后重试',
    security_check: '登录被上游安全校验拦截，请通过账号菜单“重新授权”完成验证',
    account_blocked: '上游账号已停用或拒绝授权，需要人工处理',
    additional_verification: '需要人工完成额外验证',
    proxy_unavailable: '所选代理不可用',
    credentials_unavailable: '无法读取加密登录资料',
    config_changed: '登录资料已修改，旧任务已取消',
    account_changed: '账号状态已变更，请检查后重试',
    timeout: '本次授权超时',
    transport_error: '登录网络连接失败',
    browser_unavailable: '服务器授权浏览器未就绪，请检查 CloakBrowser、Xvfb 和 xauth',
    browser_start_failed: '授权浏览器启动失败，请检查浏览器依赖和沙箱权限',
    browser_dependency_failed: '浏览器组件下载或校验失败，请检查服务器访问 GitHub、npm 的连接及组件缓存',
    rate_limited: '上游暂时限流，稍后重试',
    service_error: '上游登录服务暂不可用',
    worker_failed: '授权未完成，请检查登录资料和运行环境',
    authorization_setup_failed: '任务创建失败，可在编辑账号中重试',
    account_lookup_failed: '查询账号失败，请稍后重试',
    CODEX_LOGIN_BUSY: '已有任务或仍在冷却期，请稍后重试',
    verification_failed: '授权后的连通验证未通过，请稍后重试',
  }
  return code ? messages[code] || '授权未完成，请在编辑账号中检查并重试' : ''
}
export function codexLoginTaskLabel(task?: CodexLoginTask | null): string {
  if (task?.status === 'succeeded') {
    const workspace = task.stage.startsWith('succeeded_personal_workspace_only') ? ' · 仅个人工作区'
      : task.stage.startsWith('succeeded_personal_workspace') ? ' · 个人工作区' : ''
    if (task.stage.endsWith('_astra_degraded')) return `已授权${workspace} · 降智，未恢复调度`
    if (task.stage.endsWith('_astra_unknown')) return `已授权${workspace} · 检测未完成，未恢复调度`
    return `已授权${workspace}`
  }
  if (task?.status === 'running') {
    const stages: Record<string, string> = {
      browser_starting: '正在启动授权浏览器',
      protocol_connecting: '正在打开登录页面',
      email_submitted: '已提交邮箱',
      password_submitted: '已提交密码',
      mfa_submitted: '已提交 2FA 验证码',
      waiting_callback: '正在确认 OAuth 授权',
      exchanging_token: '正在交换授权令牌',
      applying_credentials: '正在校验身份和连通性',
    }
    return stages[task.stage] || '授权中'
  }
  return ({ queued: '等待授权', running: '授权中', succeeded: '已授权', failed: '授权失败', cancelled: '已取消' } as Record<string, string>)[task?.status || ''] || '尚未授权'
}
