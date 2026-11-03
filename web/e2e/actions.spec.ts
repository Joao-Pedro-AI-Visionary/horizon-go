import { expect, test } from '@playwright/test'
import { navTo, trackErrors } from './helpers.ts'

// Every action of the dashboard against the real API (Postgres + NATS).
// Mutating tests clean up after themselves (sources are deleted, settings
// restored).

test.describe('feed actions', () => {
  test('search, tag, source, period, min score, sort, pagination and clear', async ({ page }) => {
    const t = trackErrors(page)
    await page.goto('/?period=all')
    await expect(page.getByTestId('feed-card').first()).toBeVisible()
    const total = Number((await page.getByTestId('feed-total').innerText()).replace(/\D/g, ''))

    if ((page.viewportSize()?.width ?? 1440) < 640) {
      await expect(page.getByTestId('filter-source')).toBeHidden()
      await page.getByTestId('toggle-filters').click()
    }

    // Tag chip from a card filters by that tag.
    const chip = page.getByTestId('tag-chip').first()
    const tag = (await chip.innerText()).replace(/^#/, '')
    await chip.click()
    await expect(page).toHaveURL(new RegExp(`tag=${encodeURIComponent(tag).replace(/%20/g, '(\\+|%20)')}`))
    await expect(page.getByTestId('filter-tag')).toHaveValue(tag)
    await page.getByTestId('clear-filters').click()
    await expect(page).not.toHaveURL(/tag=/)

    await page.getByTestId('filter-period').selectOption('all')
    await page.getByTestId('filter-sort').selectOption('recent')
    await expect(page).toHaveURL(/sort=recent/)
    const firstSource = await page.getByTestId('filter-source').locator('option').nth(1).getAttribute('value')
    await page.getByTestId('filter-source').selectOption(firstSource!)
    await expect(page).toHaveURL(new RegExp(`source_id=${firstSource}`))
    await page.getByTestId('filter-source').selectOption('')

    // Min score 10 empties the feed (or leaves only perfect scores).
    await page.getByTestId('filter-min-score').fill('10')
    await page.getByTestId('filter-min-score').dispatchEvent('change')
    await expect(page).toHaveURL(/min_score=10/)
    await expect(page.getByTestId('feed-empty').or(page.getByTestId('feed-card').first())).toBeVisible()
    await page.getByTestId('clear-filters').click()

    // Search narrows the results.
    await page.goto('/?period=all')
    await page.getByTestId('filter-search').fill('zzzz-nothing-matches')
    await expect(page).toHaveURL(/q=zzzz-nothing-matches/)
    await expect(page.getByTestId('feed-empty')).toBeVisible()
    await page.getByTestId('empty-clear').click()
    await expect(page.getByTestId('feed-card').first()).toBeVisible()

    // Pagination (12 per page).
    await page.goto('/?period=all')
    if (total > 12) {
      await page.getByTestId('page-next').click()
      await expect(page).toHaveURL(/page=2/)
      await expect(page.getByTestId('feed-card').first()).toBeVisible()
      await page.getByTestId('page-prev').click()
      await expect(page).not.toHaveURL(/page=2/)
    }
    await expect(page.getByTestId('open-original').first()).toHaveAttribute('target', '_blank')
    t.assertClean()
  })

  test('run pipeline shows the real collector report', async ({ page }) => {
    test.setTimeout(180_000)
    const t = trackErrors(page)
    await page.goto('/?period=all')
    await page.getByTestId('run-pipeline').click()
    await expect(page.getByTestId('run-pipeline')).toBeDisabled()
    await expect(page.getByTestId('pipeline-report')).toBeVisible({ timeout: 150_000 })
    await expect(page.getByTestId('toast').first()).toBeVisible()
    await page.getByTestId('close-report').click()
    await expect(page.getByTestId('pipeline-report')).toBeHidden()
    t.assertClean()
  })
})

test('item detail: back and tag link', async ({ page }) => {
  const t = trackErrors(page, [404])
  await page.goto('/?period=all')
  await page.getByTestId('open-item').first().click()
  await expect(page.getByTestId('item-detail')).toBeVisible()
  const tag = page.getByTestId('item-tag').first()
  if (await tag.count()) {
    await tag.click()
    await expect(page).toHaveURL(/tag=/)
    await page.goBack()
  }
  await page.getByTestId('back').click()
  await expect(page.getByTestId('feed-list')).toBeVisible()
  await page.goto('/items/999999999')
  await expect(page.getByTestId('item-not-found')).toBeVisible()
  t.assertClean()
})

test('digests: generate now', async ({ page }) => {
  const t = trackErrors(page)
  await page.goto('/')
  await navTo(page, 'digests')
  const res = page.waitForResponse((r) => r.url().endsWith('/api/digests/generate') && r.request().method() === 'POST')
  await page.getByTestId('generate-digest').click()
  expect((await res).status()).toBe(201)
  await expect(page.getByTestId('toast').first()).toContainText('gerado')
  await expect(page).toHaveURL(/\/digests\/\d{4}-\d{2}-\d{2}$/)
  await expect(page.frameLocator('[data-testid="digest-frame"]').locator('h1')).toContainText('Radar by JpDev')
  await expect(page.getByTestId('digest-download-md')).toHaveAttribute('href', /format=md/)
  t.assertClean()
})

test('sources: create, validate, edit, toggle, collect and delete', async ({ page }, info) => {
  test.setTimeout(120_000)
  const t = trackErrors(page, [400])
  const name = `E2E feed ${info.project.name} ${Date.now()}`
  await page.goto('/sources')

  await page.getByTestId('source-new').click()
  await expect(page.getByTestId('source-form')).toBeVisible()
  await page.getByTestId('source-kind-rss').check({ force: true })
  await page.getByTestId('source-name').fill(name)
  await page.getByTestId('source-url').fill('not a url')
  await page.getByTestId('source-save').click()
  await expect(page.getByTestId('source-form-error')).toContainText('invalid feed url')

  await page.getByTestId('source-url').fill('https://github.blog/feed/')
  await page.getByTestId('source-save').click()
  await expect(page.getByTestId('source-form')).toBeHidden()
  const card = page.getByTestId('source-card').filter({ hasText: name })
  await expect(card).toBeVisible()

  // Edit the name.
  await card.getByTestId('source-edit').click()
  await page.getByTestId('source-name').fill(`${name} (editada)`)
  await page.getByTestId('source-save').click()
  const edited = page.getByTestId('source-card').filter({ hasText: `${name} (editada)` })
  await expect(edited).toBeVisible()

  // Toggle off and on.
  await edited.getByTestId('source-toggle').dispatchEvent('click')
  await expect(edited.getByText('desativada')).toBeVisible()
  await expect(edited.getByTestId('source-fetch')).toBeDisabled()
  await edited.getByTestId('source-toggle').dispatchEvent('click')
  await expect(edited.getByText('desativada')).toBeHidden()

  // Collect now (real fetch of the feed).
  const fetched = page.waitForResponse((r) => /\/api\/sources\/\d+\/fetch$/.test(r.url()), { timeout: 90_000 })
  await edited.getByTestId('source-fetch').click()
  expect([200, 502]).toContain((await fetched).status())
  await expect(page.getByTestId('toast').first()).toBeVisible()

  // Delete with confirmation.
  await edited.getByTestId('source-delete').click()
  await expect(page.getByTestId('confirm-dialog')).toBeVisible()
  await page.getByTestId('confirm-ok').click()
  await expect(edited).toHaveCount(0)
  t.assertClean()
})

test('settings: validation error and save', async ({ page }) => {
  const t = trackErrors(page, [400])
  await page.goto('/sources')
  const cron = page.getByTestId('settings-cron')
  await expect(cron).not.toHaveValue('')
  const original = await cron.inputValue()

  await cron.fill('not a cron')
  await page.getByTestId('settings-save').click()
  await expect(page.getByTestId('settings-error')).toBeVisible()

  await cron.fill('30 7 * * *')
  await page.getByTestId('settings-save').click()
  await expect(page.getByTestId('toast').first()).toContainText('Configurações salvas')
  await page.reload()
  await expect(page.getByTestId('settings-cron')).toHaveValue('30 7 * * *')

  await page.getByTestId('settings-cron').fill(original)
  await page.getByTestId('settings-save').click()
  await expect(page.getByTestId('toast').first()).toContainText('Configurações salvas')
  t.assertClean()
})

test('metrics: period and refresh', async ({ page }) => {
  const t = trackErrors(page)
  await page.goto('/metrics')
  await expect(page.getByTestId('kpi-fetched')).toBeVisible()
  const res = page.waitForResponse((r) => r.url().includes('/api/metrics?days=30'))
  await page.getByTestId('metrics-days-30').click()
  expect((await res).status()).toBe(200)
  await expect(page.getByTestId('metrics-days-30')).toHaveAttribute('aria-pressed', 'true')
  const again = page.waitForResponse((r) => r.url().includes('/api/metrics?days=30'))
  await page.getByTestId('metrics-refresh').click()
  expect((await again).status()).toBe(200)
  await page.getByTestId('chart-table-toggle').first().click()
  await expect(page.getByRole('table').first()).toBeVisible()
  t.assertClean()
})

test('navigation and 404', async ({ page }) => {
  const t = trackErrors(page)
  await page.goto('/')
  for (const n of ['digests', 'sources', 'metrics', 'feed'] as const) {
    await navTo(page, n)
    await expect(page.locator(`[data-testid$="nav-${n}"][aria-current="page"]`).first()).toBeAttached()
  }
  await page.goto('/nao-existe')
  await page.getByTestId('go-feed').click()
  await expect(page).toHaveURL(/\/$/)
  t.assertClean()
})
