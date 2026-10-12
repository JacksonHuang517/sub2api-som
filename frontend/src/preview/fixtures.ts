import { apiClient } from '@/api/client'
import type { User, TrendDataPoint, ModelStat } from '@/types'
import type { UserDashboardStats } from '@/api/usage'
import type { InternalAxiosRequestConfig } from 'axios'

// Invented visual fixtures. No production accounts, usage, keys, or prices.
export const demoUser: User = {
  id: 1, username: 'SOM 成员', email: 'demo@example.invalid', role: 'user',
  balance: 128.5, concurrency: 5, status: 'active', allowed_groups: null,
  balance_notify_enabled: false, balance_notify_threshold: null,
  balance_notify_extra_emails: [], created_at: '2026-10-01T00:00:00Z', updated_at: '2026-10-10T00:00:00Z'
}

const stats: UserDashboardStats = {
  total_api_keys: 3, active_api_keys: 2, total_requests: 8246, today_requests: 126,
  total_input_tokens: 6800000, total_output_tokens: 1820000,
  total_cache_creation_tokens: 0, total_cache_read_tokens: 4380000, total_tokens: 13000000,
  today_input_tokens: 248000, today_output_tokens: 68000,
  today_cache_creation_tokens: 0, today_cache_read_tokens: 164000, today_tokens: 480000,
  total_cost: 52.48, total_actual_cost: 36.74, today_cost: 2.76, today_actual_cost: 1.93,
  average_duration_ms: 1840, rpm: 6, tpm: 24800, by_platform: [
    { platform: 'openai', total_requests: 8246, today_requests: 126, total_tokens: 13000000, today_tokens: 480000, total_actual_cost: 36.74, today_actual_cost: 1.93 }
  ]
}

export function demoTrend(params: Record<string, unknown> = {}): TrendDataPoint[] {
  const from = new Date(String(params.start_date || new Date(Date.now() - 6 * 86400000).toISOString().slice(0, 10)))
  const to = new Date(String(params.end_date || new Date().toISOString().slice(0, 10)))
  const hourly = params.granularity === 'hour'
  const days = Math.max(1, Math.min(90, Math.round((to.getTime() - from.getTime()) / 86400000) + 1))
  const count = hourly ? Math.min(72, days * 24) : days
  return Array.from({ length: count }, (_, i) => {
    const scale = hourly ? .055 : 1
    const input = Math.round((210000 + Math.sin(i * .8) * 70000 + i * 6000) * scale)
    const output = Math.round(input * (.23 + (i % 3) * .015))
    const cache = Math.round(input * .62)
    return {
      date: new Date(from.getTime() + i * (hourly ? 3600000 : 86400000)).toISOString().slice(0, hourly ? 16 : 10),
      requests: Math.round((100 + i * 7) * scale), input_tokens: input, output_tokens: output,
      cache_creation_tokens: 0, cache_read_tokens: cache, total_tokens: input + output + cache,
      cost: 2.14, actual_cost: 1.5
    }
  })
}

const models: ModelStat[] = [
  { model: 'gpt-5.6-terra', requests: 610, input_tokens: 940000, output_tokens: 280000, cache_creation_tokens: 0, cache_read_tokens: 420000, total_tokens: 1640000, cost: 12.3, actual_cost: 8.61 },
  { model: 'gpt-5.6-sol', requests: 248, input_tokens: 420000, output_tokens: 110000, cache_creation_tokens: 0, cache_read_tokens: 260000, total_tokens: 790000, cost: 4.9, actual_cost: 3.43 },
  { model: 'gpt-5.6-astra', requests: 76, input_tokens: 126000, output_tokens: 44000, cache_creation_tokens: 0, cache_read_tokens: 80000, total_tokens: 250000, cost: 4.2, actual_cost: 2.94 }
]

const recent = Array.from({ length: 5 }, (_, i) => ({
  id: i + 1, model: models[i % 3].model, input_tokens: 12000 + i * 1400,
  output_tokens: 3200 + i * 230, actual_cost: .032 + i * .011, total_cost: .045 + i * .016,
  created_at: new Date(Date.now() - i * 11 * 60000).toISOString()
}))

export const demoRequests: string[] = []

export function demoPayload(path: string, params: Record<string, unknown> = {}): unknown {
  switch (path) {
    case '/auth/me': return { ...demoUser, run_mode: 'standard' }
    case '/usage/dashboard/stats': return stats
    case '/usage/dashboard/trend': return { trend: demoTrend(params) }
    case '/usage/dashboard/models': return { models }
    case '/usage': return { items: recent, total: 5, pages: 1, page: 1, page_size: 20 }
    case '/user/platform-quotas': return { platform_quotas: [] }
    case '/announcements': return []
    case '/keys': return { items: [], total: 0, pages: 1, page: 1, page_size: 100 }
    default: throw new Error(`No local fixture for ${path}`)
  }
}

export function installDemoAdapter() {
  // The stock interceptor can read local auth storage; the demo needs none of it.
  apiClient.interceptors.request.clear()
  apiClient.defaults.adapter = async (config: InternalAxiosRequestConfig) => {
    const path = String(config.url || '')
    if (config.method !== 'get' || !path.startsWith('/') || path.startsWith('//')) {
      throw new Error('Only whitelisted local demo reads are available.')
    }
    demoRequests.push(path)
    return { config, status: 200, statusText: 'OK', headers: {}, data: { code: 0, data: demoPayload(path, config.params) } }
  }
}
