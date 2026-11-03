import { expect, test, type Browser, type Page } from '@playwright/test'
import { mkdirSync } from 'node:fs'
import { resolve } from 'node:path'
import type { FeedPage } from '../src/api/types.ts'

// Tour of every screen with on-screen captions, recorded in two contexts:
// desktop (1440x900) and mobile (390x844). Everything runs against the real API.
const raw = resolve(import.meta.dirname, '../../videos/.raw')

/** caption shows a narration bar at the bottom of the page and waits `ms`. */
async function caption(page: Page, text: string, ms = 2600) {
  await page.evaluate((t) => {
    let el = document.getElementById('demo-caption')
    if (!el) {
      el = document.createElement('div')
      el.id = 'demo-caption'
      el.setAttribute('aria-hidden', 'true')
      Object.assign(el.style, {
        position: 'fixed',
        left: '50%',
        bottom: '28px',
        transform: 'translateX(-50%)',
        zIndex: '2147483647',
        width: 'max-content',
        maxWidth: 'min(92vw, 980px)',
        padding: '14px 22px',
        borderRadius: '14px',
        background: 'rgba(11,11,11,.92)',
        border: '1px solid #FFD400',
        color: '#FFFFFF',
        font: '600 20px/1.35 "Inter Variable", Inter, system-ui, sans-serif',
        textAlign: 'center',
        boxShadow: '0 8px 32px rgba(0,0,0,.6)',
        pointerEvents: 'none',
      })
      if (window.innerWidth < 600) Object.assign(el.style, { fontSize: '15px', bottom: '84px', padding: '10px 14px' })
      document.body.appendChild(el)
    }
    el.textContent = t
  }, text)
  await page.waitForTimeout(ms)
}

async function recorded(browser: Browser, baseURL: string, name: string, viewport: { width: number; height: number }, mobile = false) {
  mkdirSync(raw, { recursive: true })
  const context = await browser.newContext({
    baseURL,
    viewport,
    isMobile: mobile,
    hasTouch: mobile,
    recordVideo: { dir: resolve(raw, 'tmp'), size: viewport },
  })
  const page = await context.newPage()
  return {
    page,
    async done() {
      await context.close()
      await page.video()!.saveAs(resolve(raw, `${name}.webm`))
    },
  }
}

/** dragThumb drags a scrubber handle to a fraction of the track, slowly, so the live label is visible. */
async function dragThumb(page: Page, testid: 'scrub-from' | 'scrub-to', fraction: number) {
  const track = (await page.getByTestId('scrub-track').boundingBox())!
  const thumb = (await page.getByTestId(testid).boundingBox())!
  const y = track.y + track.height / 2
  await page.mouse.move(thumb.x + thumb.width / 2, y)
  await page.mouse.down()
  await page.mouse.move(track.x + track.width * fraction, y, { steps: 18 })
  await page.waitForTimeout(400)
  await page.mouse.up()
}

test('demo tour', async ({ browser, baseURL }) => {
  // Pick an item whose cluster has duplicates for the detail screen.
  const feed = (await (await fetch(`${baseURL}/api/feed?period=all&page_size=100`)).json()) as FeedPage
  const items = feed.items ?? []
  expect(items.length).toBeGreaterThan(0)
  const withDup = items.find((i) => i.duplicate_count > 0) ?? items[0]

  const d = await recorded(browser, baseURL!, 'demo-desktop', { width: 1440, height: 900 })
  const page = d.page
  await page.goto('/?period=all')
  await expect(page.getByTestId('feed-card').first()).toBeVisible()
  await caption(page, 'Radar by JpDev — notícias tech de HN, Reddit e RSS com nota de LLM local e dedup semântico', 3700)

  await page.getByTestId('run-pipeline').click()
  await caption(page, 'Rodar pipeline: coleta real em paralelo, publicada no NATS JetStream para o worker', 600)
  await expect(page.getByTestId('pipeline-report')).toBeVisible({ timeout: 150_000 })
  await caption(page, 'Relatório real da coleta, num cartão flutuante', 2900)
  await page.getByTestId('close-report').click()

  // R2: date scrubber over the real per-day histogram.
  await page.getByTestId('scrubber').evaluate((el) => el.scrollIntoView({ block: 'center' }))
  await caption(page, 'Linha do tempo: histograma real por dia (GET /api/feed/timeline)', 2700)
  await dragThumb(page, 'scrub-from', 0.72)
  await caption(page, 'Arraste a alça: datas e contagem mudam ao vivo; ao soltar, vira from/to na API', 3300)
  await page.getByTestId('scrub-to').focus()
  for (let i = 0; i < 3; i++) {
    await page.keyboard.press('ArrowLeft')
    await page.waitForTimeout(250)
  }
  await caption(page, 'Também pelo teclado: role=slider, setas, PageUp/PageDown, Home/End', 2900)
  await page.getByTestId('scrub-clear').click()
  await caption(page, 'Na superfície amarela, o primário é preto: "Limpar faixa"', 2100)

  // R2: segmented controls with a sliding pill.
  for (const id of ['filter-period-30d', 'filter-period-7d', 'filter-period-all', 'filter-sort-recent', 'filter-sort-score']) {
    await page.getByTestId(id).click()
    await page.waitForTimeout(350)
  }
  await caption(page, 'Período e ordem: a pílula desliza até a opção (180ms, ease-out)', 2100)

  // R2: disclosure.
  await page.getByTestId('filters-toggle').click()
  await caption(page, 'Mais filtros: o painel cresce em 340ms e os campos entram em sequência', 2900)
  await page.getByTestId('filters-toggle').click()

  await page.goto(`/items/${withDup.item_id}`)
  await expect(page.getByTestId('item-detail')).toBeVisible()
  await caption(page, 'Detalhe: resumo, motivo da nota e tags', 2500)
  await page.getByTestId('cluster').scrollIntoViewIfNeeded()
  await caption(
    page,
    withDup.duplicate_count > 0
      ? `Cluster: ${withDup.duplicate_count} duplicado(s) agrupado(s) por cosseno pgvector e pelo LLM na faixa ambígua`
      : 'Cluster: duplicados agrupados por cosseno pgvector e pelo LLM na faixa ambígua',
    2400,
  )

  await page.getByTestId('nav-digests').click()
  await expect(page.getByTestId('digest-item').first()).toBeVisible()
  await page.getByTestId('generate-digest').click()
  await expect(page.getByTestId('toast').first()).toContainText('gerado')
  await caption(page, 'Digest diário gerado agora: nota de corte e cota por fonte, em Markdown e HTML', 2900)

  await page.getByTestId('nav-sources').click()
  await expect(page.getByTestId('source-card').first()).toBeVisible()
  await caption(page, 'Fontes em cartões flutuantes, com gradiente suave por tipo', 2500)
  await page.getByTestId('source-details-toggle').first().click()
  await caption(page, 'Detalhes da fonte: config, datas e última coleta reais, no mesmo disclosure', 2900)

  await page.getByTestId('nav-metrics').click()
  await expect(page.getByTestId('kpi-fetched')).toBeVisible()
  await page.getByTestId('metrics-days-30').click()
  await caption(page, 'Métricas: itens/dia, fontes, dedup, falhas e tokens do Ollama', 2500)
  await page.mouse.wheel(0, 800)
  await caption(page, 'Agora no mobile (390x844)…', 1700)
  await d.done()

  const m = await recorded(browser, baseURL!, 'demo-mobile', { width: 390, height: 844 }, true)
  const mp = m.page
  await mp.goto('/?period=all')
  await expect(mp.getByTestId('feed-card').first()).toBeVisible()
  // Center it: partially visible, it would sit under the fixed bottom nav.
  await mp.getByTestId('scrubber').evaluate((el) => el.scrollIntoView({ block: 'center' }))
  await mp.waitForTimeout(300)
  await caption(mp, 'Mobile: o scrubber cabe na largura toda', 2100)
  await dragThumb(mp, 'scrub-from', 0.6)
  await caption(mp, 'Faixa aplicada sem perder a rolagem', 2100)
  await mp.getByTestId('scrub-clear').click()
  await mp.getByTestId('filters-toggle').click()
  await caption(mp, 'Filtros extras sob demanda', 2000)
  await mp.getByTestId('bnav-metrics').click()
  await expect(mp.getByTestId('kpi-fetched')).toBeVisible()
  await mp.mouse.wheel(0, 700)
  await caption(mp, 'Radar by JpDev · Go + Vue 3 · baseado no Horizon (Thysrael, MIT)', 3300)
  await m.done()
})
