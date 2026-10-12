<script setup lang="ts">
import { ref } from 'vue'
import ProfileFeishuCard from '@/components/user/profile/ProfileFeishuCard.vue'
import { createFeishuClient } from '@/api/feishu'
const dark = ref(true)
const balance = ref('20')
const status = ref('')
const busy = ref(false)
async function mock(path: string, method: string, body: unknown = {}) {
  const headers: Record<string, string> = { 'Content-Type': 'application/json' }
  if (path !== 'session') headers.Authorization = `Bearer ${(await mock('session', 'POST')).proof}`
  const response = await fetch(`/integrations/feishu/api/mock/${path}`, { method, credentials: 'same-origin', headers, body: JSON.stringify(body) })
  if (!response.ok) throw new Error('模拟服务不可用，请确认 sidecar 以 mock 模式运行。')
  if (response.status === 204) return {}
  return response.json()
}
const client = createFeishuClient(async () => (await mock('session', 'POST')).proof)
async function simulate(tick: boolean) {
  busy.value = true
  try {
    const micros = Math.round(Number(balance.value) * 1000000)
    if (!/^\d+(\.\d{1,6})?$/.test(balance.value) || !Number.isSafeInteger(micros)) throw new Error('请输入非负美元金额，最多 6 位小数。')
    await mock('balance', 'PUT', { balance_micros: micros })
    if (tick) await mock('tick', 'POST')
    status.value = tick ? '已更新模拟余额并触发 dry-run 扫描；未发送真实消息。' : '模拟余额已更新。'
  } catch (error) { status.value = error instanceof Error ? error.message : '操作失败，请重试。' } finally { busy.value = false }
}
</script>
<template>
  <div class="preview" :class="{ dark }">
    <main>
      <div class="banner"><strong>MOCK · 本地模拟预览</strong><span>未连接真实飞书 · 未发送真实消息</span></div>
      <header><div><small>SOM / INTEGRATIONS</small><h1>让用量提醒，抵达飞书。</h1><p>授权、确认身份与提醒设置的完整体验。</p></div><button @click="dark = !dark">{{ dark ? '切换浅色' : '切换深色' }}</button></header>
      <ProfileFeishuCard :client="client" />
      <section class="controls" aria-label="模拟余额控制"><h2>模拟控制台</h2><p>仅开发预览可用。修改余额后触发扫描以检查告警状态。</p><label>模拟余额（USD）<input v-model="balance" inputmode="decimal" /></label><div><button :disabled="busy" @click="simulate(false)">更新余额</button><button :disabled="busy" @click="simulate(true)">更新并扫描（dry-run）</button></div><p role="status">{{ status }}</p></section>
    </main>
  </div>
</template>
<style scoped>
:global(*){box-sizing:border-box}:global(body){margin:0;font-family:Inter,-apple-system,BlinkMacSystemFont,"Segoe UI",sans-serif}button,input{font:inherit}button{cursor:pointer}button:focus-visible,input:focus-visible{outline:3px solid #397ddc;outline-offset:3px}.preview{min-height:100vh;background:#eff2f6;color:#171c23;padding:32px 20px}.preview.dark{background:#0c1118;color:#eef2f7}main{max-width:840px;margin:auto}.banner{display:flex;flex-wrap:wrap;justify-content:space-between;gap:8px;background:#ffdda9;color:#512e09;padding:14px 18px;border-radius:12px;font-size:13px}header{display:flex;align-items:center;justify-content:space-between;gap:20px;margin:36px 0}header small{color:#508bdb;letter-spacing:.12em}h1{font-size:32px;line-height:1.3;margin:10px 0}header p,.controls p{opacity:.75}button{border:1px solid #788394;border-radius:8px;padding:10px 14px;min-height:44px;background:transparent;color:inherit}header button{white-space:nowrap}.controls{border:1px dashed #788394;padding:22px;border-radius:16px;margin-top:24px}.controls h2{font-size:18px;margin-top:0}.controls label{display:grid;gap:8px;max-width:260px}.controls input{border:1px solid #788394;border-radius:8px;padding:10px;background:transparent;color:inherit;width:100%}.controls button{margin:14px 10px 0 0}@media(max-width:500px){.preview{padding:20px 12px}header{display:block}header button{margin-top:12px}h1{font-size:27px}.controls{padding:18px}}
</style>
