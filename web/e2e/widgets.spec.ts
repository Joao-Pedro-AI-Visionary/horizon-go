import { expect, test, type Locator, type Page } from '@playwright/test'
import { trackErrors } from './helpers.ts'

// R2 widgets against the real API: date scrubber (GET /api/feed/timeline +
// from/to on /api/feed), segmented controls with a sliding pill, and
// disclosure panels. Runs in desktop, tablet and mobile.

/** The pill sits under `btn` (same x and width, 1px tolerance) once settled. */
async function expectPillUnder(pill: Locator, btn: Locator) {
  await expect
    .poll(async () => {
      const [p, b] = await Promise.all([pill.boundingBox(), btn.boundingBox()])
      return !!p && !!b && Math.abs(p.x - b.x) <= 1 && Math.abs(p.width - b.width) <= 1
    })
    .toBe(true)
}

async function feedTotalOrZero(page: Page): Promise<number> {
  if (await page.getByTestId('feed-empty').isVisible()) return 0
  return Number(await page.getByTestId('feed-total').locator('[data-value]').getAttribute('data-value'))
}

const num = (s: string) => Number(s.replace(/\D/g, ''))

test.describe('segmented control', () => {
  test('pill slides to the pressed option; arrows select; metrics range', async ({ page }) => {
    const t = trackErrors(page)
    await page.goto('/?period=all')
    const pill = page.getByTestId('filter-period-pill')
    await expectPillUnder(pill, page.getByTestId('filter-period-all'))
    expect(await pill.evaluate((el) => getComputedStyle(el).transitionDuration)).toContain('0.18s')

    await page.getByTestId('filter-period-7d').click()
    await expect(page).toHaveURL(/period=7d/)
    await expect(page.getByTestId('filter-period-7d')).toHaveAttribute('aria-pressed', 'true')
    await expectPillUnder(pill, page.getByTestId('filter-period-7d'))

    // Keyboard: ArrowRight moves focus and selection to the next option.
    await page.getByTestId('filter-sort-score').focus()
    await page.keyboard.press('ArrowRight')
    await expect(page).toHaveURL(/sort=recent/)
    await expect(page.getByTestId('filter-sort-recent')).toBeFocused()
    await expectPillUnder(page.getByTestId('filter-sort-pill'), page.getByTestId('filter-sort-recent'))

    await page.goto('/metrics')
    const mPill = page.getByTestId('metrics-days-pill')
    await expectPillUnder(mPill, page.getByTestId('metrics-days-14'))
    const res = page.waitForResponse((r) => r.url().includes('/api/metrics?days=7'))
    await page.getByTestId('metrics-days-7').click()
    expect((await res).status()).toBe(200)
    await expectPillUnder(mPill, page.getByTestId('metrics-days-7'))
    t.assertClean()
  })
})

test.describe('date scrubber', () => {
  test('keyboard: slider semantics, live label and real filtered feed', async ({ page }) => {
    const t = trackErrors(page)
    const tl = page.waitForResponse((r) => r.url().includes('/api/feed/timeline'))
    await page.goto('/?period=all')
    expect((await tl).status()).toBe(200)
    await expect(page.getByTestId('scrub-bar')).toHaveCount(30)

    const from = page.getByTestId('scrub-from')
    const to = page.getByTestId('scrub-to')
    await expect(from).toHaveAttribute('role', 'slider')
    await expect(from).toHaveAttribute('aria-valuenow', '0')
    await expect(to).toHaveAttribute('aria-valuenow', '29')
    await expect(to).toHaveAttribute('aria-valuetext', /\d{4}, \d+ ite(m|ns)$/)

    // Narrow to the last 7 days: End on "from" jumps to "to", then 6 days back.
    await from.focus()
    await page.keyboard.press('End')
    for (let i = 0; i < 6; i++) await page.keyboard.press('ArrowLeft')
    await expect(from).toHaveAttribute('aria-valuenow', '23')
    const label = await page.getByTestId('scrub-label').innerText()
    // Visible text "28 out → 03 nov" (+ an sr-only "até" between the dates).
    expect(label).toMatch(/^\d{2} \w{3}[\s\S]*\d{2} \w{3}$/)

    // Committed 400ms after the last key: from goes to the URL and the API.
    const feedRes = page.waitForResponse((r) => r.url().includes('/api/feed?') && r.url().includes('from='))
    await expect(page).toHaveURL(/from=/)
    expect((await feedRes).status()).toBe(200)
    await expect(page).not.toHaveURL(/period=/)
    await expect(page.getByTestId('scrub-active')).toBeVisible()
    await expect(page.getByTestId('filter-period-pill')).toHaveCSS('opacity', '0')
    // The histogram and the feed agree on the number of items in the range.
    const counted = num(await page.getByTestId('scrub-count').innerText())
    await expect.poll(() => feedTotalOrZero(page)).toBe(counted)

    // Primary action on the yellow surface: black button clears the range.
    await expect(page.getByTestId('scrub-clear')).toHaveCSS('background-color', 'rgb(11, 11, 11)')
    await page.getByTestId('scrub-clear').click()
    await expect(page).not.toHaveURL(/from=/)
    await expect(page.getByTestId('scrub-active')).toBeHidden()
    t.assertClean()
  })

  test('pointer: drag a handle, then tap the track to move the nearest one', async ({ page }) => {
    const t = trackErrors(page)
    await page.goto('/?period=all')
    await expect(page.getByTestId('scrub-bar')).toHaveCount(30)
    const track = page.getByTestId('scrub-track')
    await track.scrollIntoViewIfNeeded()
    const box = (await track.boundingBox())!
    const toThumb = (await page.getByTestId('scrub-to').boundingBox())!
    const y = box.y + box.height / 2

    // Drag the end handle to the middle; the label updates while dragging.
    const before = await page.getByTestId('scrub-label').innerText()
    await page.mouse.move(toThumb.x + toThumb.width / 2, y)
    await page.mouse.down()
    await page.mouse.move(box.x + box.width * 0.6, y, { steps: 8 })
    await page.mouse.move(box.x + box.width * 0.5, y, { steps: 6 })
    await expect(page.getByTestId('scrub-label')).not.toHaveText(before)
    await page.mouse.up()
    await expect(page).toHaveURL(/to=/)
    const end = Number(await page.getByTestId('scrub-to').getAttribute('aria-valuenow'))
    expect(end).toBeGreaterThanOrEqual(13)
    expect(end).toBeLessThanOrEqual(16)

    // The range change keeps the scroll position (query-only navigation).
    expect((await track.boundingBox())!.y).toBeCloseTo(box.y, 0)
    // Single-pointer alternative: a tap right of the range moves the end there.
    await page.mouse.click(box.x + box.width * 0.85, y)
    await expect.poll(async () => Number(await page.getByTestId('scrub-to').getAttribute('aria-valuenow'))).toBeGreaterThan(end)
    await expect(page.getByTestId('scrub-active')).toBeVisible()
    t.assertClean()
  })
})

test.describe('disclosure', () => {
  test('feed filters and source details expand with staggered children', async ({ page }) => {
    const t = trackErrors(page)
    await page.goto('/?period=all')
    const toggle = page.getByTestId('filters-toggle')
    const body = page.getByTestId('filters-body')
    await expect(toggle).toHaveAttribute('aria-expanded', 'false')
    await expect(page.getByTestId('filter-tag')).toBeHidden()
    expect(await body.evaluate((el) => getComputedStyle(el).gridTemplateRows)).toMatch(/^0(px)?$/)

    // Keyboard: Enter on the focused button opens it.
    await toggle.focus()
    await page.keyboard.press('Enter')
    await expect(toggle).toHaveAttribute('aria-expanded', 'true')
    expect(await body.evaluate((el) => getComputedStyle(el).transitionDuration)).toContain('0.34s')
    const tag = page.getByTestId('filter-tag')
    await expect(tag).toBeVisible()
    // Children fade in and rise from 10px, later ones with a longer delay.
    const delays = await body.locator('.dz-group > *').evaluateAll((els) => els.map((el) => parseFloat(getComputedStyle(el).transitionDelay)))
    expect(delays.length).toBe(3)
    expect(delays[0]!).toBeLessThan(delays[1]!)
    expect(delays[1]!).toBeLessThan(delays[2]!)
    await expect.poll(() => tag.evaluate((el) => getComputedStyle(el.parentElement!).opacity)).toBe('1')
    await tag.fill('go')
    await tag.press('Enter')
    await expect(page).toHaveURL(/tag=go/)
    await expect(page.getByTestId('filters-count')).toHaveText('1')
    await toggle.click()
    await expect(tag).toBeHidden()

    // Source details (real /api/sources data).
    await page.goto('/sources')
    const card = page.getByTestId('source-card').first()
    await expect(card).toBeVisible()
    const kind = await card.getAttribute('data-kind')
    expect(await card.evaluate((el) => getComputedStyle(el).backgroundImage)).toContain('linear-gradient')
    expect(await card.evaluate((el) => getComputedStyle(el).borderTopLeftRadius)).toBe('16px')
    expect(['hackernews', 'reddit', 'rss']).toContain(kind)
    const det = card.getByTestId('source-details-toggle')
    await det.click()
    await expect(det).toHaveAttribute('aria-expanded', 'true')
    await expect(card.getByTestId('source-details-run')).toBeVisible()
    await expect(card.getByTestId('source-details-body')).toContainText('#')
    await det.click()
    await expect(card.getByTestId('source-details-run')).toBeHidden()
    t.assertClean()
  })

  test('reduced motion: widgets settle at once', async ({ page }) => {
    const t = trackErrors(page)
    await page.emulateMedia({ reducedMotion: 'reduce' })
    await page.goto('/?period=all')
    await page.getByTestId('filters-toggle').click()
    const body = page.getByTestId('filters-body')
    expect(parseFloat(await body.evaluate((el) => getComputedStyle(el).transitionDuration))).toBeLessThan(0.001)
    // Settled within a frame or two, not after the 300ms + stagger of the full motion.
    await expect
      .poll(() => page.getByTestId('filter-tag').evaluate((el) => getComputedStyle(el.parentElement!).opacity), { timeout: 500, intervals: [16, 32, 64] })
      .toBe('1')
    expect(parseFloat(await page.getByTestId('filter-period-pill').evaluate((el) => getComputedStyle(el).transitionDuration))).toBeLessThan(0.001)
    t.assertClean()
  })
})
