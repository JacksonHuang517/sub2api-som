<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { feishuClient, FeishuError, type FeishuClient, type FeishuMe, type SubscriptionSnapshot } from '@/api/feishu'

const props = defineProps<{ client?: FeishuClient }>()
const client = props.client || feishuClient
const visible = ref(false)
const busy = ref(false)
const me = ref<FeishuMe | null>(null)
const error = ref('')
const notice = ref('')
const enabled = ref(false)
const warning = ref('10')
const critical = ref('2')
const confirmUnbind = ref(false)
const subscriptionEnabled = ref(false)
const subscriptionWarning = ref('80')
const subscriptionCritical = ref('95')
const subscriptions = ref<SubscriptionSnapshot | null>(null)
const subscriptionsUnavailable = ref(false)
const windowName = { daily: '每日', weekly: '每周', monthly: '每月' }
const money = (micros: number | null) => micros === null ? '不可用' : (micros / 1000000).toLocaleString(undefined, { maximumFractionDigits: 6 })
const date = (value: string) => new Date(value).toLocaleString()
const activeSubscription = (sub: { status: string; expires_at: string }) => sub.status === 'active' && new Date(sub.expires_at).getTime() > Date.now()


function explain(value: unknown) {
  if (!(value instanceof FeishuError)) return '操作未完成，请检查网络后重试。'
  if (value.status === 401) return '登录凭证已过期，请重新登录 SOM 后重试。'
  if (value.status === 403) return '当前账号没有操作权限，请联系管理员检查应用授权。'
  if (value.code === 'CONFIRMATION_EXPIRED') return '飞书确认已过期，请重新关联飞书。'
  if (value.code === 'IDENTITY_ALREADY_BOUND') return '该飞书身份已关联其他 SOM 账号，请更换飞书账号或联系管理员。'
  if (value.status === 409) return '设置已被更新，请刷新状态后重新保存。'
  if (value.status === 429) return '请求过于频繁，请稍后重试。'
  const messages: Record<string, string> = {
    NETWORK_ERROR: '网络连接超时或中断，请检查连接后重试。',
    PENDING_EXPIRED: '飞书确认已过期，请重新关联飞书。',
    NO_PENDING: '没有待确认的飞书账号，请重新关联飞书。',
    INVALID_PREFERENCES: '请输入有效阈值：警戒金额必须大于紧急金额，且紧急金额大于 0。',
    INVALID_SUBSCRIPTION_THRESHOLDS: '已用比例必须是整数，且 0 < 警戒 < 紧急 < 100。',
    INVALID_THRESHOLDS: '请输入有效阈值：警戒金额必须大于紧急金额，且紧急金额大于 0。',
    NOT_BOUND: '请先关联飞书，再开启余额提醒。',
    STATE_INVALID: '授权已失效，请重新关联飞书。',
    OAUTH_INVALID: '飞书授权无效，请重新关联飞书。',
    OAUTH_EXPIRED: '飞书授权已过期，请重新关联飞书。',
    OAUTH_IDENTITY_REJECTED: '无法确认飞书身份，请联系管理员检查应用的可用范围与身份权限。',
    STORAGE_ERROR: '服务暂时不可用，请稍后重试或联系管理员。'
  }
  return messages[value.code] || '操作未完成，请刷新状态后重试；如仍失败，请联系管理员。'
}
async function refresh() {
  me.value = await client.me()
  enabled.value = me.value.preferences.enabled
  warning.value = String(me.value.preferences.warning_micros / 1000000)
  critical.value = String(me.value.preferences.critical_micros / 1000000)
  subscriptionEnabled.value = me.value.subscription_preferences.enabled
  subscriptionWarning.value = String(me.value.subscription_preferences.warning_percent)
  subscriptionCritical.value = String(me.value.subscription_preferences.critical_percent)
  try { subscriptions.value = await client.subscriptions(); subscriptionsUnavailable.value = false }
  catch { subscriptions.value = null; subscriptionsUnavailable.value = true }
}
async function run(action: () => Promise<void>) {
  if (busy.value) return
  busy.value = true
  error.value = ''
  notice.value = ''
  try { await action() } catch (value) { error.value = explain(value) } finally { busy.value = false }
}
async function load() {
  await run(async () => {
    try {
      visible.value = (await client.config()).enabled
      if (visible.value) {
        await refresh()
        const callbackError = new URLSearchParams(window.location.search).get('feishu_error')
        if (callbackError) error.value = explain(new FeishuError(callbackError))
      }
    } catch (value) {
      if (value instanceof FeishuError && value.status === 404) { visible.value = false; return }
      visible.value = true
      throw value
    }
  })
}
function start() {
  return run(async () => {
    const { authorize_url } = await client.start()
    const url = new URL(authorize_url, window.location.origin)
    if (url.origin !== window.location.origin && !(url.protocol === 'https:' && url.hostname === 'accounts.feishu.cn')) {
      throw new FeishuError('INVALID_AUTHORIZE_URL')
    }
    window.location.assign(url.href)
  })
}
function save() {
  return run(async () => {
    if (!me.value) return
    const parse = (text: string) => /^\d+(\.\d{1,6})?$/.test(text) ? Math.round(Number(text) * 1000000) : NaN
    const w = parse(warning.value)
    const c = parse(critical.value)
    if (!Number.isSafeInteger(w) || !Number.isSafeInteger(c) || w <= c || c <= 0 || w > 1000000000000) {
      throw new FeishuError('INVALID_THRESHOLDS')
    }
    me.value.preferences = await client.preferences({ enabled: enabled.value, warning_micros: w, critical_micros: c, version: me.value.preferences.version })
    notice.value = '余额提醒设置已保存。'
  })
}
function saveSubscription() {
  return run(async () => {
    if (!me.value) return
    const w = /^\d+$/.test(subscriptionWarning.value) ? Number(subscriptionWarning.value) : NaN
    const c = /^\d+$/.test(subscriptionCritical.value) ? Number(subscriptionCritical.value) : NaN
    if (!Number.isInteger(w) || !Number.isInteger(c) || w <= 0 || w >= c || c >= 100) throw new FeishuError('INVALID_SUBSCRIPTION_THRESHOLDS')
    me.value.subscription_preferences = await client.subscriptionPreferences({ enabled: subscriptionEnabled.value, warning_percent: w, critical_percent: c, version: me.value.subscription_preferences.version })
    notice.value = '订阅用量提醒设置已保存。'
  })
}
function confirm() { return run(async () => { await client.confirm(); await refresh(); notice.value = '飞书关联已确认。' }) }
function unbind() { return run(async () => { await client.unbind(); confirmUnbind.value = false; await refresh(); notice.value = '已解除飞书关联并停止提醒。' }) }
onMounted(load)
</script>

<template>
  <section v-if="visible" class="feishu-card" aria-labelledby="feishu-title" :aria-busy="busy">
    <header><div><span class="eyebrow">SOM · CONNECT</span><h2 id="feishu-title">飞书关联与用量提醒</h2></div><span class="status">{{ me?.binding ? '已关联' : '未关联' }}</span></header>
    <p class="muted">关联飞书身份，独立设置钱包余额与订阅用量提醒。此操作不会修改 SOM 用户名。</p>
    <p v-if="error" role="alert" class="alert">{{ error }}</p>
    <p v-if="notice" role="status" class="notice">{{ notice }}</p>
    <div v-if="me?.pending" class="pending">
      <h3>确认这是你的飞书账号</h3>
      <p>识别姓名：<strong>{{ me.pending.display_name }}</strong></p>
      <p>SOM 用户 ID：<strong>{{ me.user_id }}</strong></p>
      <p class="muted">确认后，该飞书身份将关联到此 SOM 账号。</p>
      <button class="primary" :disabled="busy" @click="confirm">确认关联此账号</button>
      <button :disabled="busy" @click="start">不是我的账号，重新授权</button>
    </div>
    <template v-if="me?.binding">
      <dl><div><dt>飞书姓名</dt><dd>{{ me.binding.display_name }}</dd></div><div><dt>最近同步</dt><dd>{{ me.binding.synced_at ? new Date(me.binding.synced_at).toLocaleString() : '尚未同步' }}</dd></div></dl>
      <form @submit.prevent="save">
        <label class="toggle"><input v-model="enabled" type="checkbox" :disabled="busy" />开启飞书余额提醒</label>
        <div class="thresholds">
          <label>警戒金额（USD）<input v-model="warning" name="warning" inputmode="decimal" :disabled="busy" required aria-describedby="threshold-help" /></label>
          <label>紧急金额（USD）<input v-model="critical" name="critical" inputmode="decimal" :disabled="busy" required aria-describedby="threshold-help" /></label>
        </div>
        <p id="threshold-help" class="muted">警戒 &gt; 紧急 &gt; 0；最多 6 位小数，最高 $1,000,000。余额耗尽也会提醒。</p>
        <button class="primary" :disabled="busy" type="submit">保存提醒设置</button>
      </form>
      <form aria-label="订阅用量提醒设置" @submit.prevent="saveSubscription">
        <label class="toggle"><input v-model="subscriptionEnabled" name="subscription-enabled" type="checkbox" :disabled="busy" />开启飞书订阅用量提醒</label>
        <div class="thresholds">
          <label>警戒已用比例（%）<input v-model="subscriptionWarning" name="subscription-warning" inputmode="numeric" :disabled="busy" required aria-describedby="subscription-help" /></label>
          <label>紧急已用比例（%）<input v-model="subscriptionCritical" name="subscription-critical" inputmode="numeric" :disabled="busy" required aria-describedby="subscription-help" /></label>
        </div>
        <p id="subscription-help" class="muted">百分比表示订阅额度已用比例，与钱包余额独立；0 &lt; 警戒 &lt; 紧急 &lt; 100，均为整数。100% 耗尽也会提醒。仅监控未到期且已激活的有限额窗口。</p>
        <button class="primary" :disabled="busy" type="submit">保存订阅提醒设置</button>
      </form>
      <div class="subscription-summary" aria-label="当前订阅用量">
        <h3>当前订阅用量</h3>
        <p v-if="subscriptionsUnavailable" class="muted">订阅数据暂不可用，请刷新状态重试。</p>
        <p v-else-if="subscriptions && !subscriptions.active" class="muted">当前用户未激活，订阅提醒已停止。</p>
        <p v-else-if="subscriptions && !subscriptions.subscriptions.length" class="muted">暂无订阅。</p>
        <article v-for="sub in subscriptions?.subscriptions || []" :key="sub.subscription_id">
          <h3>{{ sub.group_name }} · #{{ sub.subscription_id }}</h3>
          <p class="muted">{{ activeSubscription(sub) ? '有效' : sub.status === 'active' ? '已到期' : sub.status }} · 到期 {{ date(sub.expires_at) }}</p>
          <ul>
            <li v-for="window in sub.windows" :key="window.kind">
              <strong>{{ windowName[window.kind] }}</strong>：
              <span v-if="!window.limited">无限额</span>
              <span v-else>已用 ${{ money(window.used_micros) }} / ${{ money(window.limit_micros) }} · 剩余 ${{ money(window.remaining_micros) }}
                <template v-if="!activeSubscription(sub)"> · 当前不监控，不刷新</template>
                <template v-else-if="!window.window_start"> · 窗口未激活</template>
                <template v-else-if="window.reset_at"> · 重置 {{ date(window.reset_at) }}</template>
                <template v-else> · 本窗口到期前不再刷新</template>
              </span>
            </li>
          </ul>
        </article>
      </div>
      <div class="actions"><button :disabled="busy" @click="start">重新同步飞书身份</button><button class="danger" :disabled="busy" @click="confirmUnbind = true">解除关联</button></div>
      <div v-if="confirmUnbind" class="pending" role="group" aria-label="确认解除飞书关联">
        <h3>解除与 {{ me.binding.display_name }} 的关联？</h3><p>解除后将停止全部飞书用量提醒，并取消尚未发送的提醒。</p>
        <button class="danger" :disabled="busy" @click="unbind">确认解除并停止提醒</button><button :disabled="busy" @click="confirmUnbind = false">取消</button>
      </div>
    </template>
    <div v-else-if="me && !me.pending" class="empty"><p>尚未关联飞书账号。授权后，请核对姓名并确认关联。</p><button class="primary" :disabled="busy" @click="start">关联飞书</button></div>
    <button class="refresh" :disabled="busy" @click="load">{{ busy ? '正在处理…' : '刷新状态' }}</button>
  </section>
</template>

<style scoped>
.feishu-card{--surface:#fff;--ink:#161b22;--muted:#526071;--line:#dbe1e8;--soft:#f5f7fa;--blue:#1759b8;background:var(--surface);color:var(--ink);border:1px solid var(--line);border-radius:20px;padding:24px;font-size:14px;line-height:1.6;overflow-wrap:anywhere}
:global(.dark .feishu-card){--surface:#151b24;--ink:#f2f5fa;--muted:#b1bdce;--line:#354150;--soft:#202b39;--blue:#86b5ff}
header{display:flex;justify-content:space-between;align-items:center;gap:12px}h2{font-size:21px;font-weight:700;margin:4px 0}h3{font-weight:700;font-size:16px;margin:0 0 8px}.eyebrow{font-size:11px;letter-spacing:.12em;color:var(--blue);font-weight:700}.status{white-space:nowrap;border:1px solid var(--line);border-radius:24px;padding:3px 10px;color:var(--blue)}.muted{color:var(--muted);font-size:13px;margin:10px 0 16px}button{border:1px solid var(--line);border-radius:9px;padding:10px 14px;min-height:44px;background:var(--surface);color:var(--ink);font-weight:600;cursor:pointer}button.primary{background:#f28a35;color:#151515;border-color:#f28a35}button:disabled{opacity:.55;cursor:wait}button:focus-visible,input:focus-visible{outline:3px solid var(--blue);outline-offset:3px}.danger{color:#bd3426}:global(.dark .feishu-card .danger){color:#ffa296}.pending{background:var(--soft);border:1px solid var(--line);border-left:3px solid #f28a35;padding:16px;margin:18px 0;border-radius:10px}.pending button{margin:10px 8px 0 0}.alert{background:#ffe7df;color:#872d1c;padding:12px;border-radius:8px}.notice{background:#e4f0ff;color:#184c89;padding:12px;border-radius:8px}dl{margin:20px 0;display:grid;gap:10px}dl div{display:flex;justify-content:space-between;gap:16px}dt{color:var(--muted)}dd{margin:0;text-align:right}.toggle{display:flex;gap:10px;align-items:center;font-weight:600;margin:18px 0}.toggle input{width:18px;height:18px;accent-color:#e77722}.thresholds{display:grid;grid-template-columns:1fr 1fr;gap:16px}.thresholds label{display:grid;gap:7px}.thresholds input{min-width:0;width:100%;box-sizing:border-box;border:1px solid var(--line);border-radius:8px;padding:11px;background:var(--soft);color:var(--ink)}.subscription-summary{border-top:1px solid var(--line);margin-top:22px;padding-top:20px}.subscription-summary article{background:var(--soft);padding:14px;border-radius:10px;margin-top:12px}.subscription-summary ul{padding-left:20px}.subscription-summary li{margin:8px 0}.actions{display:flex;flex-wrap:wrap;gap:10px;border-top:1px solid var(--line);padding-top:20px;margin-top:22px}.refresh{margin-top:16px;color:var(--blue);font-size:13px}.empty{padding:12px 0}.empty p{margin-bottom:14px}@media(max-width:440px){.feishu-card{padding:18px;border-radius:14px}h2{font-size:18px}.thresholds{grid-template-columns:1fr}header{align-items:flex-start}dl div{display:block}dd{text-align:left}.pending button{width:100%;margin-right:0}}
</style>
