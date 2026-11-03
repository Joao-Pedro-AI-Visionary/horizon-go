import { expect, type Page } from '@playwright/test'

/**
 * trackErrors fails the test on console errors, page errors and 5xx API answers.
 * expected lists HTTP statuses a test provokes on purpose (Chrome logs them as console errors).
 */
export function trackErrors(page: Page, expected: number[] = []) {
  const errors: string[] = []
  page.on('console', (m) => {
    if (m.type() !== 'error') return
    // The test runner injects its own scripts into every frame; the sandboxed
    // digest iframe (no allow-scripts) blocks them. Not an app error: a plain
    // browser session logs nothing there.
    if (m.text().includes('frame is sandboxed') && m.text().includes('/api/digests/')) return
    if (expected.some((s) => m.text().includes(`status of ${s} `))) return
    errors.push(`console: ${m.text()}`)
  })
  page.on('pageerror', (e) => errors.push(`pageerror: ${e.message}`))
  page.on('response', (r) => {
    if (r.url().includes('/api/') && r.status() >= 500 && r.status() !== 502) errors.push(`${r.status()} ${r.url()}`)
  })
  return {
    errors,
    assertClean: () => expect(errors, errors.join('\n')).toEqual([]),
  }
}

/** noHorizontalOverflow asserts the document is not wider than the viewport. */
export async function expectNoHorizontalOverflow(page: Page) {
  const { scroll, client } = await page.evaluate(() => ({
    scroll: document.documentElement.scrollWidth,
    client: document.documentElement.clientWidth,
  }))
  expect(scroll, 'page must not scroll horizontally').toBeLessThanOrEqual(client)
}

/** navTo uses the sidebar/rail on md+ and the bottom nav on mobile. */
export async function navTo(page: Page, name: 'feed' | 'digests' | 'sources' | 'metrics') {
  const mobile = (page.viewportSize()?.width ?? 1440) < 768
  await page.getByTestId(`${mobile ? 'bnav' : 'nav'}-${name}`).click()
}

/**
 * revealAll scrolls through the page so every scroll-revealed block
 * (v-reveal) enters the viewport once, then returns to the top. Needed before
 * full-page screenshots, which capture beyond the viewport.
 */
export async function revealAll(page: Page) {
  await page.evaluate(async () => {
    const step = Math.max(200, window.innerHeight * 0.6)
    for (let y = 0; y < document.documentElement.scrollHeight; y += step) {
      window.scrollTo(0, y)
      await new Promise((r) => setTimeout(r, 60))
    }
    window.scrollTo(0, 0)
  })
  await expect(page.locator('[data-reveal]:not(.is-in)')).toHaveCount(0)
  await page.waitForTimeout(900) // let the last reveal/count-up finish
}

/** feedTotal reads the real total of the feed (not the animated digits). */
export async function feedTotal(page: Page): Promise<number> {
  return Number(await page.getByTestId('feed-total').locator('[data-value]').getAttribute('data-value'))
}
