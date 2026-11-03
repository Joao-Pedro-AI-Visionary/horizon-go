import { expect, test } from '@playwright/test'
import { expectNoHorizontalOverflow, feedTotal, navTo, revealAll, trackErrors } from './helpers.ts'

// Opens every screen against the real API and saves a full-page screenshot
// per viewport to docs/screenshots/<screen>-<project>.png.
const shot = (name: string, project: string) => `../docs/screenshots/${name}-${project}.png`

test.describe('screens', () => {
  test('feed', async ({ page }, info) => {
    const t = trackErrors(page)
    await page.goto('/?period=all')
    await expect(page.getByRole('heading', { level: 1, name: 'O que importa hoje' })).toBeVisible()
    await expect(page.getByTestId('feed-card').first()).toBeVisible()
    await expect(page.getByTestId('pagination')).toBeVisible()
    expect(await feedTotal(page)).toBeGreaterThan(0)
    await expectNoHorizontalOverflow(page)
    await revealAll(page)
    await page.screenshot({ path: shot('1-feed', info.project.name), fullPage: true })
    t.assertClean()
  })

  test('item detail', async ({ page }, info) => {
    const t = trackErrors(page)
    await page.goto('/?period=all')
    await page.getByTestId('open-item').first().click()
    await expect(page).toHaveURL(/\/items\/\d+$/)
    await expect(page.getByTestId('item-detail')).toBeVisible()
    await expect(page.getByTestId('cluster')).toBeVisible()
    await expect(page.getByTestId('item-original')).toHaveAttribute('href', /^https?:\/\//)
    await expectNoHorizontalOverflow(page)
    await revealAll(page)
    await page.screenshot({ path: shot('2-item', info.project.name), fullPage: true })
    t.assertClean()
  })

  test('digests', async ({ page }, info) => {
    const t = trackErrors(page)
    await page.goto('/')
    await navTo(page, 'digests')
    await expect(page.getByRole('heading', { level: 1, name: 'Digest diário' })).toBeVisible()
    await expect(page.getByTestId('digest-item').first()).toBeVisible()
    const frame = page.frameLocator('[data-testid="digest-frame"]')
    await expect(frame.locator('h1')).toContainText('Radar by JpDev')
    await expectNoHorizontalOverflow(page)
    await revealAll(page)
    await page.screenshot({ path: shot('3-digests', info.project.name), fullPage: true })
    t.assertClean()
  })

  test('sources & config', async ({ page }, info) => {
    const t = trackErrors(page)
    await page.goto('/')
    await navTo(page, 'sources')
    await expect(page.getByTestId('source-card').first()).toBeVisible()
    await expect(page.getByTestId('settings-cron')).not.toHaveValue('')
    await expectNoHorizontalOverflow(page)
    await revealAll(page)
    await page.screenshot({ path: shot('4-sources', info.project.name), fullPage: true })
    t.assertClean()
  })

  test('metrics', async ({ page }, info) => {
    const t = trackErrors(page)
    await page.goto('/')
    await navTo(page, 'metrics')
    await expect(page.getByTestId('kpi-fetched')).toBeVisible()
    await expect(page.getByTestId('chart-items-per-day').locator('svg')).toBeVisible()
    await expect(page.getByTestId('chart-tokens').locator('svg')).toBeVisible()
    await expectNoHorizontalOverflow(page)
    await revealAll(page)
    await page.screenshot({ path: shot('5-metrics', info.project.name), fullPage: true })
    t.assertClean()
  })

  test('reduced motion renders the final state at once', async ({ page }) => {
    const t = trackErrors(page)
    await page.emulateMedia({ reducedMotion: 'reduce' })
    await page.goto('/?period=all')
    const card = page.getByTestId('feed-card').first()
    await expect(card).toHaveClass(/is-in/)
    expect(await card.evaluate((el) => getComputedStyle(el).opacity)).toBe('1')
    // Count-up shows the final number immediately.
    const total = page.getByTestId('feed-total').locator('[data-value]')
    await expect(total.locator('[aria-hidden="true"]')).toHaveText(/\d/)
    const shown = (await total.locator('[aria-hidden="true"]').innerText()).replace(/\D/g, '')
    expect(Number(shown)).toBe(Number(await total.getAttribute('data-value')))
    await page.goto('/metrics')
    await expect(page.getByTestId('kpi-fetched')).toHaveClass(/is-in/)
    t.assertClean()
  })
})
