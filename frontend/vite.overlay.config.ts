import { defineConfig } from 'vite'
import productionConfig from './vite.config'

// A single self-contained application lets the running backend serve a new theme
// through its existing entry filename without changing old lazy chunks or Caddy.
export default defineConfig((environment) => {
  const config = productionConfig(environment)
  config.build.outDir = '../backend/internal/web/dist-overlay'
  config.build.cssCodeSplit = false
  config.build.rollupOptions.output = { inlineDynamicImports: true }
  return config
})
