import { describe, it, expect, vi } from 'vitest'
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { parse, compileStyle } from 'vue/compiler-sfc'
import { mount, flushPromises } from '@vue/test-utils'
import ProfileFeishuCard from '../ProfileFeishuCard.vue'
import { FeishuError, createFeishuClient, type FeishuClient, type FeishuMe } from '@/api/feishu'
vi.mock('@/api/client', () => ({ apiClient: { post: vi.fn() } }))
function fixture(overrides: Partial<FeishuMe> = {}): FeishuMe {
  return { subscription_preferences: { enabled: false, warning_percent: 80, critical_percent: 95, version: 1 }, user_id: 23, binding: null, pending: null, preferences: { enabled: false, warning_micros: 10000000, critical_micros: 2000000, version: 1 }, ...overrides }
}
function client(data = fixture()): FeishuClient {
  return { subscriptionPreferences: vi.fn().mockResolvedValue({ ...data.subscription_preferences, version: data.subscription_preferences.version + 1 }), subscriptions: vi.fn().mockResolvedValue({ user_id: data.user_id, active: true, currency: 'USD', at: '2026-10-12T00:00:00Z', subscriptions: [] }), config: vi.fn().mockResolvedValue({ enabled: true }), me: vi.fn().mockResolvedValue(data), start: vi.fn(), confirm: vi.fn().mockResolvedValue({}), unbind: vi.fn().mockResolvedValue(undefined), preferences: vi.fn().mockResolvedValue(data.preferences) }
}
const bound = () => fixture({ binding: { display_name: '测试用户', tenant_key: 'tenant', version: 1, synced_at: '2026-10-11T00:00:00Z' } })
describe('ProfileFeishuCard', () => {
  it('hides without authentication when disabled or config is absent', async () => {
    for (const missing of [false, true]) {
      const api = client()
      if (missing) vi.mocked(api.config).mockRejectedValue(new FeishuError('NOT_FOUND', 404))
      else vi.mocked(api.config).mockResolvedValue({ enabled: false })
      const wrapper = mount(ProfileFeishuCard, { props: { client: api } })
      await flushPromises()
      expect(wrapper.find('section').exists()).toBe(false)
      expect(api.me).not.toHaveBeenCalled()
    }
  })
  it('shows pending identity and SOM ID before explicit confirmation', async () => {
    const api = client(fixture({ pending: { display_name: '飞书姓名', tenant_key: 't' } }))
    const wrapper = mount(ProfileFeishuCard, { props: { client: api } })
    await flushPromises()
    expect(wrapper.text()).toContain('飞书姓名')
    expect(wrapper.text()).toContain('23')
    expect(api.confirm).not.toHaveBeenCalled()
    await wrapper.findAll('button').find(b => b.text() === '确认关联此账号')!.trigger('click')
    await flushPromises()
    expect(api.confirm).toHaveBeenCalledOnce()
  })
  it('validates thresholds and sends integer micro-USD and optimistic version', async () => {
    const api = client(bound())
    const wrapper = mount(ProfileFeishuCard, { props: { client: api } })
    await flushPromises()
    await wrapper.get('[name=warning]').setValue('1')
    await wrapper.findAll('form')[0].trigger('submit')
    await flushPromises()
    expect(api.preferences).not.toHaveBeenCalled()
    expect(wrapper.get('[role=alert]').text()).toContain('警戒')
    await wrapper.get('[name=warning]').setValue('10.123456')
    await wrapper.findAll('form')[0].trigger('submit')
    await flushPromises()
    expect(api.preferences).toHaveBeenCalledWith({ enabled: false, warning_micros: 10123456, critical_micros: 2000000, version: 1 })
  })
  it('saves subscription percentages independently and preserves wallet draft', async () => {
    const api = client(bound())
    const wrapper = mount(ProfileFeishuCard, { props: { client: api } })
    await flushPromises()
    await wrapper.get('[name=warning]').setValue('19')
    await wrapper.get('[name=subscription-enabled]').setValue(true)
    await wrapper.get('[name=subscription-warning]').setValue('81')
    await wrapper.get('[aria-label="订阅用量提醒设置"]').trigger('submit')
    await flushPromises()
    expect(api.subscriptionPreferences).toHaveBeenCalledWith({ enabled: true, warning_percent: 81, critical_percent: 95, version: 1 })
    expect(api.preferences).not.toHaveBeenCalled()
    expect((wrapper.get('[name=warning]').element as HTMLInputElement).value).toBe('19')
  })
  it('rejects decimal percentages and shows unavailable data without zero quotas', async () => {
    const api = client(bound())
    vi.mocked(api.subscriptions).mockRejectedValue(new Error('unavailable'))
    const wrapper = mount(ProfileFeishuCard, { props: { client: api } })
    await flushPromises()
    expect(wrapper.text()).toContain('订阅数据暂不可用')
    await wrapper.get('[name=subscription-warning]').setValue('80.5')
    await wrapper.get('[aria-label="订阅用量提醒设置"]').trigger('submit')
    await flushPromises()
    expect(api.subscriptionPreferences).not.toHaveBeenCalled()
    expect(wrapper.get('[role=alert]').text()).toContain('整数')
  })
  it('requires a second explicit action to unbind', async () => {
    const api = client(bound())
    const wrapper = mount(ProfileFeishuCard, { props: { client: api } })
    await flushPromises()
    await wrapper.findAll('button').find(b => b.text() === '解除关联')!.trigger('click')
    expect(api.unbind).not.toHaveBeenCalled()
    await wrapper.findAll('button').find(b => b.text() === '确认解除并停止提醒')!.trigger('click')
    await flushPromises()
    expect(api.unbind).toHaveBeenCalledOnce()
  })
  it('presents actionable conflict feedback', async () => {
    const api = client(bound())
    vi.mocked(api.preferences).mockRejectedValue(new FeishuError('VERSION_CONFLICT', 409))
    const wrapper = mount(ProfileFeishuCard, { props: { client: api } })
    await flushPromises()
    await wrapper.findAll('form')[0].trigger('submit')
    await flushPromises()
    expect(wrapper.get('[role=alert]').text()).toContain('刷新')
  })
  it('fetches a fresh proof per authenticated request and handles plain JSON/204', async () => {
    const proof = vi.fn().mockResolvedValue('short-lived')
    const fetcher = vi.fn().mockResolvedValueOnce(new Response('{"enabled":true}')).mockResolvedValueOnce(new Response(JSON.stringify(fixture()))).mockResolvedValueOnce(new Response(null, { status: 204 }))
    vi.stubGlobal('fetch', fetcher)
    try {
      const api = createFeishuClient(proof)
      expect(await api.config()).toEqual({ enabled: true })
      expect(proof).not.toHaveBeenCalled()
      expect((await api.me()).user_id).toBe(23)
      await api.unbind()
      expect(proof).toHaveBeenCalledTimes(2)
      expect(fetcher.mock.calls[1][1]).toMatchObject({ credentials: 'same-origin', headers: { Authorization: 'Bearer short-lived' } })
    } finally { vi.unstubAllGlobals() }
  })
})


describe('Feishu style boundaries', () => {
  it('compiles dark selectors without dropping the card or danger target', () => {
    const source = readFileSync(resolve(process.cwd(), 'src/components/user/profile/ProfileFeishuCard.vue'), 'utf8')
    const style = parse(source).descriptor.styles[0]
    const css = compileStyle({ source: style.content, filename: 'ProfileFeishuCard.vue', id: 'data-v-card', scoped: true }).code
    expect(css).toContain('.dark .feishu-card{--surface:#151b24')
    expect(css).toContain('.dark .feishu-card .danger{color:#ffa296}')
  })
  it('scopes preview headers so card headers cannot inherit preview margins', () => {
    const source = readFileSync(resolve(process.cwd(), 'src/feishu-preview/App.vue'), 'utf8')
    const style = parse(source).descriptor.styles[0]
    expect(style.scoped).toBe(true)
    const css = compileStyle({ source: style.content, filename: 'App.vue', id: 'data-v-preview', scoped: true }).code
    expect(css).toContain('header[data-v-preview]')
    expect(css).not.toContain('header{')
    expect(css).toContain('body{margin:0')
  })
})
