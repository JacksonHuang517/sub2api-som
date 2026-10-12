import { apiClient } from './client'

export interface FeishuPreferences {
  enabled: boolean
  warning_micros: number
  critical_micros: number
  version: number
}
export interface SubscriptionPreferences {
  enabled: boolean
  warning_percent: number
  critical_percent: number
  version: number
}
export interface SubscriptionWindow {
  kind: 'daily' | 'weekly' | 'monthly'
  limited: boolean
  limit_micros: number | null
  used_micros: number
  remaining_micros: number | null
  window_start: string | null
  reset_at: string | null
}
export interface SubscriptionSnapshot {
  user_id: number
  active: boolean
  at: string
  currency: string
  subscriptions: { subscription_id: number; group_id: number; group_name: string; status: string; expires_at: string; windows: SubscriptionWindow[] }[]
}
export interface FeishuMe {
  user_id: number
  binding: null | { display_name: string; tenant_key: string; version: number; synced_at: string }
  preferences: FeishuPreferences
  subscription_preferences: SubscriptionPreferences
  pending: null | { display_name: string; tenant_key: string }
}
export interface FeishuClient {
  config(): Promise<{ enabled: boolean }>
  me(): Promise<FeishuMe>
  start(): Promise<{ authorize_url: string }>
  confirm(): Promise<unknown>
  unbind(): Promise<void>
  preferences(value: FeishuPreferences): Promise<FeishuPreferences>
  subscriptionPreferences(value: SubscriptionPreferences): Promise<SubscriptionPreferences>
  subscriptions(): Promise<SubscriptionSnapshot>
}
export class FeishuError extends Error {
  constructor(public code: string, public status = 0) { super(code) }
}

// Proofs are short lived and are never persisted or shared with another origin.
export function createFeishuClient(proofProvider: () => Promise<string>): FeishuClient {
  async function request<T>(path: string, method = 'GET', body?: unknown, authenticated = true): Promise<T> {
    const headers: Record<string, string> = { 'Content-Type': 'application/json' }
    if (authenticated) headers.Authorization = `Bearer ${await proofProvider()}`
    let response: Response
    try {
      response = await fetch(`/integrations/feishu/api/${path}`, {
        method, headers, credentials: 'same-origin',
        body: body === undefined ? undefined : JSON.stringify(body),
        signal: AbortSignal.timeout(15000)
      })
    } catch { throw new FeishuError('NETWORK_ERROR') }
    if (!response.ok) {
      const data = await response.json().catch(() => ({}))
      throw new FeishuError(typeof data.error === 'string' ? data.error : 'REQUEST_FAILED', response.status)
    }
    if (response.status === 204) return undefined as T
    return response.json()
  }
  return {
    config: () => request('config', 'GET', undefined, false),
    me: () => request('me'),
    start: () => request('binding/start', 'POST', {}),
    confirm: () => request('binding/confirm', 'POST', {}),
    unbind: () => request('binding', 'DELETE'),
    preferences: value => request('preferences', 'PUT', value),
    subscriptionPreferences: value => request('subscription-preferences', 'PUT', value),
    subscriptions: () => request('subscriptions')
  }
}
export const feishuClient = createFeishuClient(async () => {
  try {
    const response = await apiClient.post<{ proof: string }>('/auth/feishu-session', {})
    if (!response.data.proof) throw new FeishuError('SESSION_REQUIRED', 401)
    return response.data.proof
  } catch (error) {
    if (error instanceof FeishuError) throw error
    const status = (error as { response?: { status?: number } }).response?.status
    throw new FeishuError(status === 404 ? 'NOT_ENABLED' : 'SESSION_REQUIRED', status || 401)
  }
})
