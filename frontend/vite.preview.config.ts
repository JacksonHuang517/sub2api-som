import { defineConfig } from 'vite'
import vue from '@vitejs/plugin-vue'
import { resolve } from 'node:path'

// No backend proxy or remote settings request in the local visual preview.
export default defineConfig({
  plugins: [vue(), {
    name: 'som-local-dashboard-entry',
    configureServer(server) {
      server.middlewares.use((req, res, next) => {
        const path = req.url?.split('?')[0]
        if (path === '/home') {
          res.writeHead(302, { Location: 'http://127.0.0.1:5177/home' })
          res.end()
          return
        }
        if (req.headers.accept?.includes('text/html') && !path?.includes('.')) {
          req.url = '/preview.html'
        }
        next()
      })
    }
  }],
  resolve: { alias: { '@': resolve(__dirname, 'src'), 'vue-i18n': 'vue-i18n/dist/vue-i18n.runtime.esm-bundler.js' } },
  define: { __INTLIFY_JIT_COMPILATION__: true },
  server: { host: '127.0.0.1', port: 5178, strictPort: true },
  build: { outDir: 'dist-preview', rollupOptions: { input: resolve(__dirname, 'preview.html') } }
})
