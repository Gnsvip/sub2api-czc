// Only non-secret form preferences belong in browser storage. Keep administrator,
// platform and account type separate; never serialize the entire account form.
export interface AccountCreationDefaults {
  group_ids: number[]
  proxy_ids: number[]
  concurrency: number
  priority: number
}
function key(userId: number, platform: string, type: string) {
  return `sub2api.account-create.v1.${userId}.${platform}.${type}`
}
function ids(value: unknown): number[] {
  return Array.isArray(value) ? [...new Set(value.filter((id): id is number => Number.isSafeInteger(id) && id > 0))].slice(0, 200) : []
}
export function readAccountCreationDefaults(userId: number | undefined, platform: string, type: string): { remember: boolean; settings?: AccountCreationDefaults } {
  if (!userId) return { remember: true }
  try {
    const raw = JSON.parse(localStorage.getItem(key(userId, platform, type)) || 'null')
    if (!raw || typeof raw !== 'object') return { remember: true }
    if (raw.remember === false) return { remember: false }
    if (!Number.isSafeInteger(raw.concurrency) || raw.concurrency < 0 || raw.concurrency > 100000 || !Number.isSafeInteger(raw.priority) || raw.priority < 0 || raw.priority > 100000) return { remember: true }
    return { remember: true, settings: { group_ids: ids(raw.group_ids), proxy_ids: ids(raw.proxy_ids), concurrency: raw.concurrency, priority: raw.priority } }
  } catch { return { remember: true } }
}
export function writeAccountCreationDefaults(userId: number | undefined, platform: string, type: string, remember: boolean, settings: AccountCreationDefaults) {
  if (!userId) return
  try {
    localStorage.setItem(key(userId, platform, type), JSON.stringify(remember ? {
      remember: true, group_ids: ids(settings.group_ids), proxy_ids: ids(settings.proxy_ids), concurrency: settings.concurrency, priority: settings.priority
    } : { remember: false }))
  } catch { /* Storage can be disabled; account creation must still work. */ }
}
