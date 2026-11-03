import { defineConfig, devices } from '@playwright/test'

// The dashboard needs the real API (cmd/api on :8080, proxied by Vite) with
// Postgres and NATS. E2E_BASE_URL points at an already running Vite server
// (e.g. http://localhost:5173); otherwise the build is served by vite preview.
const PORT = 4173
const external = process.env.E2E_BASE_URL

export default defineConfig({
  testDir: './e2e',
  fullyParallel: false,
  workers: 1, // the specs share one real database
  forbidOnly: !!process.env.CI,
  reporter: 'list',
  timeout: 60_000,
  use: { baseURL: external ?? `http://localhost:${PORT}`, trace: 'retain-on-failure' },
  projects: [
    { name: 'desktop', use: { ...devices['Desktop Chrome'], viewport: { width: 1440, height: 900 } } },
    { name: 'tablet', use: { ...devices['Desktop Chrome'], viewport: { width: 834, height: 1112 }, hasTouch: true } },
    { name: 'mobile', use: { ...devices['Desktop Chrome'], viewport: { width: 390, height: 844 }, hasTouch: true, isMobile: true } },
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
