import { defineConfig } from 'vite'
import vue from '@vitejs/plugin-vue'
import { resolve } from 'node:path'
export default defineConfig({
  plugins: [vue(), {
    name: 'feishu-preview-fallback',
    configureServer(server) {
      server.middlewares.use((req, _res, next) => {
        if (req.url === '/' || req.url?.split('?')[0] === '/profile') req.url = '/feishu-preview.html'
        next()
      })
    }
  }],
  resolve: { alias: { '@': resolve(__dirname, 'src'), 'vue-i18n': 'vue-i18n/dist/vue-i18n.runtime.esm-bundler.js' } },
  server: { host: '127.0.0.1', port: 5186, strictPort: true, proxy: { '/integrations/feishu': { target: 'http://127.0.0.1:8390', changeOrigin: false } } },
  build: { outDir: 'dist-feishu-preview', rollupOptions: { input: resolve(__dirname, 'feishu-preview.html') } }
})
