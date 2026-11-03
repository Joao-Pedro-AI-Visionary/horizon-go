import { expect, test, type Page } from '@playwright/test'
import { mkdirSync, readFileSync, writeFileSync } from 'node:fs'
import { resolve } from 'node:path'
import type { FeedPage } from '../src/api/types.ts'

// Narrated tour (1920x1080) against the real API. Each scene of
// videos/narrated/script.md lasts as long as its narration clip
// (work/scenes.json, from scripts/narrated-tts.mjs): actions are spread over
// the clip with at(fraction) and the scene holds until the audio ends.
// The real start of each scene (relative to the recording) goes to
// work/marks.json so scripts/narrated-video.mjs can place audio and subtitles.
const work = resolve(import.meta.dirname, '../../videos/narrated/work')
const GAP = 0.6 // seconds of silence between scenes

type Scene = { id: string; text: string; seconds: number }
const scenes = JSON.parse(readFileSync(resolve(work, 'scenes.json'), 'utf8')) as Scene[]

async function smoothScroll(page: Page, dy: number, steps = 12) {
  for (let i = 0; i < steps; i++) {
    await page.mouse.wheel(0, dy / steps)
    await page.waitForTimeout(40)
  }
}

async function dragThumb(page: Page, testid: 'scrub-from' | 'scrub-to', fraction: number) {
  const track = (await page.getByTestId('scrub-track').boundingBox())!
  const thumb = (await page.getByTestId(testid).boundingBox())!
  const y = track.y + track.height / 2
  await page.mouse.move(thumb.x + thumb.width / 2, y)
  await page.mouse.down()
  await page.mouse.move(track.x + track.width * fraction, y, { steps: 24 })
  await page.waitForTimeout(400)
  await page.mouse.up()
}

/** overlay covers the page with a full-screen panel (fades in over 300ms). */
async function overlay(page: Page, html: string) {
  await page.evaluate((h) => {
    document.getElementById('narr-overlay')?.remove()
    const el = document.createElement('div')
    el.id = 'narr-overlay'
    Object.assign(el.style, {
      position: 'fixed',
      inset: '0',
      zIndex: '2147483647',
      background: '#0B0B0B',
      display: 'flex',
      alignItems: 'flex-start',
      justifyContent: 'center',
      paddingTop: '56px', // keep clear of the burned-in subtitles at the bottom
      gap: '64px',
      opacity: '0',
      transition: 'opacity 300ms ease-out',
    })
    el.innerHTML = h
    document.body.appendChild(el)
    requestAnimationFrame(() => requestAnimationFrame(() => (el.style.opacity = '1')))
  }, html)
}

/** device renders the app in an iframe of the device's CSS size, scaled to fit. */
function device(label: string, w: number, h: number, scale: number) {
  return `<figure style="margin:0;display:flex;flex-direction:column;align-items:center;gap:16px">
    <div style="width:${w * scale}px;height:${h * scale}px;border:2px solid #FFD400;border-radius:22px;overflow:hidden;background:#141414">
      <iframe src="/?period=all" title="${label}" style="width:${w}px;height:${h}px;border:0;transform:scale(${scale});transform-origin:0 0"></iframe>
    </div>
    <figcaption style="font:600 22px 'Space Grotesk Variable',sans-serif;color:#fff">${label} <span style="font-family:'JetBrains Mono Variable',monospace;color:#FFD400">${w}×${h}</span></figcaption>
  </figure>`
}

test('narrated tour', async ({ browser, baseURL }) => {
  test.setTimeout(300_000)
  const feed = (await (await fetch(`${baseURL}/api/feed?period=all&page_size=100`)).json()) as FeedPage
  const items = feed.items ?? []
  expect(items.length).toBeGreaterThan(0)
  const withDup = items.find((i) => i.duplicate_count > 0) ?? items[0]

  mkdirSync(work, { recursive: true })
  const viewport = { width: 1920, height: 1080 }
  const context = await browser.newContext({ baseURL, viewport, recordVideo: { dir: resolve(work, 'tmp'), size: viewport } })
  const page = await context.newPage()
  const t0 = Date.now()
  const now = () => (Date.now() - t0) / 1000
  const marks: { id: string; start: number; end: number }[] = []

  async function scene(id: string, body: (at: (f: number) => Promise<void>) => Promise<void>) {
    const s = scenes.find((x) => x.id === id)
    if (!s) throw new Error(`scene ${id} missing from scenes.json`)
    const start = now()
    const waitUntil = async (t: number) => {
      const ms = (t - now()) * 1000
      if (ms > 0) await page.waitForTimeout(ms)
    }
    await body((f) => waitUntil(start + f * s.seconds))
    await waitUntil(start + s.seconds + GAP)
    marks.push({ id, start, end: now() })
  }

  await page.goto('/?period=all')
  await expect(page.getByTestId('feed-card').first()).toBeVisible()
  await page.waitForTimeout(400)

  await scene('abertura', async (at) => {
    await at(0.35)
    await page.getByTestId('feed-card').first().hover()
    await at(0.7)
    await page.getByTestId('run-pipeline').hover()
  })

  await scene('ia-local', async (at) => {
    await page.goto(`/items/${withDup.item_id}`)
    await expect(page.getByTestId('item-detail')).toBeVisible()
    await at(0.6)
    await page.getByTestId('cluster').evaluate((el) => el.scrollIntoView({ behavior: 'smooth', block: 'center' }))
  })

  await scene('feed', async (at) => {
    await page.getByTestId('nav-feed').click()
    await expect(page.getByTestId('feed-card').first()).toBeVisible()
    await at(0.25)
    await page.mouse.move(960, 600)
    await smoothScroll(page, 900, 20)
    await at(0.75)
    await smoothScroll(page, 700, 16)
  })

  await scene('scrubber', async (at) => {
    await page.getByTestId('scrubber').evaluate((el) => el.scrollIntoView({ behavior: 'smooth', block: 'center' }))
    await at(0.12)
    await dragThumb(page, 'scrub-from', 0.6)
    await at(0.55)
    await dragThumb(page, 'scrub-to', 0.88)
  })

  await scene('pilulas', async (at) => {
    await page.getByTestId('scrub-clear').click()
    for (const id of ['filter-period-30d', 'filter-period-7d', 'filter-period-all', 'filter-sort-recent', 'filter-sort-score']) {
      await page.getByTestId(id).click()
      await page.waitForTimeout(550)
    }
    await at(0.62)
    await page.getByTestId('filters-toggle').click()
  })

  await scene('digest', async (at) => {
    await page.getByTestId('nav-digests').click()
    await expect(page.getByTestId('digest-item').first()).toBeVisible()
    await at(0.3)
    await page.getByTestId('generate-digest').click()
    await expect(page.getByTestId('toast').first()).toContainText('gerado')
  })

  await scene('fontes', async (at) => {
    await page.getByTestId('nav-sources').click()
    await expect(page.getByTestId('source-card').first()).toBeVisible()
    await at(0.4)
    await page.getByTestId('source-details-toggle').first().click()
  })

  await scene('metricas', async (at) => {
    await page.getByTestId('nav-metrics').click()
    await expect(page.getByTestId('kpi-fetched')).toBeVisible()
    await at(0.55)
    await page.mouse.move(960, 600)
    await smoothScroll(page, 800, 20)
  })

  await scene('responsivo', async () => {
    await overlay(page, device('Tablet', 834, 1112, 0.68) + device('Celular', 390, 844, 0.9))
    const frames = page.frameLocator('#narr-overlay iframe')
    await expect(frames.first().getByTestId('feed-card').first()).toBeVisible()
    await expect(frames.last().getByTestId('feed-card').first()).toBeVisible()
    await page.waitForTimeout(1200)
    for (const f of page.frames().filter((x) => x !== page.mainFrame())) {
      await f.evaluate(() => window.scrollTo({ top: 700, behavior: 'smooth' }))
    }
  })

  await scene('encerramento', async () => {
    await overlay(
      page,
      `<div style="display:flex;flex-direction:column;align-items:center;gap:28px;text-align:center;margin-top:150px">
        <img src="/jpdev-logo.png" alt="" style="height:200px">
        <div style="font:700 72px 'Space Grotesk Variable',sans-serif;color:#FFD400;letter-spacing:-0.02em">Horizon</div>
        <div style="font:500 30px 'Inter Variable',sans-serif;color:#fff">Código aberto · Go + Vue 3 · IA local com Ollama</div>
        <div style="font:400 24px 'Inter Variable',sans-serif;color:#BBBBBB">Baseado em github.com/Thysrael/Horizon (MIT)</div>
      </div>`,
    )
  })

  await page.waitForTimeout(800)
  await context.close()
  await page.video()!.saveAs(resolve(work, 'tour.webm'))
  writeFileSync(resolve(work, 'marks.json'), JSON.stringify({ gap: GAP, marks }, null, 2))
})
