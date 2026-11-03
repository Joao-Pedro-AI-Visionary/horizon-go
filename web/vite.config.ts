import tailwindcss from '@tailwindcss/vite'
import vue from '@vitejs/plugin-vue'
import { defineConfig } from 'vite'

// The dashboard calls the Go API (cmd/api) on the same origin: /api and
// /healthz are proxied in dev and preview. API_TARGET overrides the default.
const target = process.env.API_TARGET ?? 'http://localhost:8080'
const proxy = {
  '/api': { target, changeOrigin: true },
  '/healthz': { target, changeOrigin: true },
}

// https://vite.dev/config/
export default defineConfig({
  plugins: [vue(), tailwindcss()],
  server: { proxy },
  preview: { proxy },
})
