import { defineConfig, devices } from '@playwright/test'

// Narrated tour of the dashboard against the real backend (see demo/tour.spec.ts).
// Recordings go to ../videos/.raw; scripts/demo-video.mjs builds videos/demo-final.mp4.
const PORT = 4173
const external = process.env.E2E_BASE_URL

export default defineConfig({
  testDir: './demo',
  outputDir: './test-results/demo', // keep ./test-results (e2e videos) intact
  workers: 1,
  reporter: 'list',
  timeout: 420_000,
  use: { ...devices['Desktop Chrome'], baseURL: external ?? `http://localhost:${PORT}`, launchOptions: { slowMo: 120 } },
  webServer: external
    ? undefined
    : {
        command: `npm run build && npx vite preview --port ${PORT} --strictPort`,
        url: `http://localhost:${PORT}`,
        reuseExistingServer: true,
        timeout: 120_000,
      },
})
