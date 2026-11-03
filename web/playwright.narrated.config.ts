import { defineConfig, devices } from '@playwright/test'

// Narrated tour (narrated/tour.spec.ts), 1920x1080, against the real backend.
// scripts/narrated.ps1 starts api, worker and vite preview and sets E2E_BASE_URL.
export default defineConfig({
  testDir: './narrated',
  outputDir: './test-results/narrated',
  workers: 1,
  reporter: 'list',
  timeout: 300_000,
  use: { ...devices['Desktop Chrome'], baseURL: process.env.E2E_BASE_URL ?? 'http://localhost:4173' },
})
