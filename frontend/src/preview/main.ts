import { createApp, h } from 'vue'
import { createPinia } from 'pinia'
import { createRouter, createWebHistory, RouterView } from 'vue-router'
import i18n, { loadLocaleMessages } from '@/i18n'
import { useAuthStore } from '@/stores/auth'
import { useAppStore } from '@/stores/app'
import DashboardPreview from './DashboardPreview.vue'
import AppLayout from '@/components/layout/AppLayout.vue'
import { installDemoAdapter, demoUser } from './fixtures'
import '@/style.css'
import '@/styles/som-theme.css'

async function bootstrapPreview() {
  if (!['127.0.0.1', 'localhost', '[::1]'].includes(location.hostname)) {
    throw new Error('This demo is available only on loopback hosts.')
  }
  installDemoAdapter()
  document.documentElement.classList.toggle('dark', new URLSearchParams(location.search).get('theme') !== 'light')
  const app = createApp({ render: () => h(RouterView) })
  app.use(createPinia())
  const auth = useAuthStore()
  auth.user = demoUser
  auth.token = 'local-demo-only'
  // These are harmless local demo markers, not an imported production session.
  localStorage.setItem('user_guide_1_user_v4_interactive', 'true')
  const state = useAppStore()
  state.siteName = 'SOM AI HUB'
  state.siteLogo = '/logo.svg'
  state.siteVersion = '0.2.15'
  state.publicSettingsLoaded = true
  state.$patch({ cachedPublicSettings: {
    site_name: 'SOM AI HUB', site_logo: '/logo.svg',
    subscription_enabled: false, payment_enabled: false,
    model_plaza_enabled: false, channel_monitor_enabled: false,
    affiliate_enabled: false, custom_menu_items: [],
  } })
  await loadLocaleMessages('zh')
  i18n.global.locale.value = 'zh'
  app.use(i18n)
  const router = createRouter({ history: createWebHistory(), routes: [
    { path: '/', redirect: '/dashboard' },
    { path: '/dashboard', component: DashboardPreview, meta: { titleKey: 'dashboard.title', descriptionKey: 'dashboard.welcomeMessage' } },
    { path: '/:pathMatch(.*)*', component: { render: () => h(AppLayout, {}, () => h('div', { class: 'card p-6 space-y-4' }, [
      h('p', '当前预览仅展示 Dashboard 配色。'),
      h('a', { class: 'btn btn-primary', href: '/dashboard' }, '返回仪表盘')
    ])) }, meta: { title: '本地预览' } }
  ] })
  app.use(router)
  await router.isReady()
  app.mount('#app')
}

void bootstrapPreview()
