import { defineConfig, devices } from '@playwright/test'

// The dashboard needs the real API (cmd/api on :8080, proxied by Vite) and the
// worker, with Postgres, NATS and Ollama: scripts/e2e.ps1 starts api + worker,
// runs this suite and stops them by PID. E2E_BASE_URL points at an already
// running Vite server (e.g. http://localhost:5173); otherwise the build is
// served by vite preview.
//
// Every test is recorded (video: 'on'); scripts/videos.mjs reads the JSON
// report and turns the webm files into videos/e2e-<project>.mp4.
const PORT = 4173
const external = process.env.E2E_BASE_URL

const desktop = { width: 1440, height: 900 }
const tablet = { width: 834, height: 1112 }
const mobile = { width: 390, height: 844 }

export default defineConfig({
  testDir: './e2e',
  fullyParallel: false,
  workers: 1, // the specs share one real database
  forbidOnly: !!process.env.CI,
  reporter: [['list'], ['json', { outputFile: 'playwright-report/results.json' }]],
  timeout: 60_000,
  use: { baseURL: external ?? `http://localhost:${PORT}`, trace: 'retain-on-failure', video: 'on' },
  projects: [
    { name: 'desktop', use: { ...devices['Desktop Chrome'], viewport: desktop, video: { mode: 'on', size: desktop } } },
    {
      name: 'tablet',
      use: { ...devices['Desktop Chrome'], viewport: tablet, hasTouch: true, video: { mode: 'on', size: tablet } },
    },
    {
      name: 'mobile',
      use: {
        ...devices['Desktop Chrome'],
        viewport: mobile,
        hasTouch: true,
        isMobile: true,
        video: { mode: 'on', size: mobile },
      },
    },
  ],
  webServer: external
    ? undefined
    : {
        command: `npm run build && npx vite preview --port ${PORT} --strictPort`,
        url: `http://localhost:${PORT}`,
        reuseExistingServer: !process.env.CI,
        timeout: 120_000,
      },
})
