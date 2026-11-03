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
  await caption(page, 'Radar by JpDev — notícias tech de HN, Reddit e RSS com nota de LLM local e dedup semântico', 3500)

  await caption(page, 'Rodar pipeline: coleta real das fontes em paralelo, publicada no NATS para o worker', 1800)
  await page.getByTestId('run-pipeline').click()
  await caption(page, 'Coletando… uma goroutine por fonte, fan-in e items.raw no JetStream', 600)
  await expect(page.getByTestId('pipeline-report')).toBeVisible({ timeout: 150_000 })
  await caption(page, 'Relatório real da coleta: itens por fonte, duplicados por URL e falhas', 3500)
  await page.getByTestId('close-report').click()

  await caption(page, 'Feed ordenado pela nota 0–10 do qwen3:14b (Ollama), com motivo e tags', 2400)
  await page.mouse.wheel(0, 700)
  await page.waitForTimeout(1200)
  await page.mouse.wheel(0, -700)
  await page.getByTestId('filter-sort-recent').click()
  await caption(page, 'Filtros por fonte, tag, período, nota mínima e ordem ficam na URL', 2200)
  await page.getByTestId('filter-sort-score').click()
  await page.getByTestId('filter-search').pressSequentially('go', { delay: 120 })
  await caption(page, 'Busca textual no read model (projeção CQRS feed_view)', 2200)
  await page.getByTestId('clear-filters').click()

  await page.goto(`/items/${withDup.item_id}`)
  await expect(page.getByTestId('item-detail')).toBeVisible()
  await caption(page, 'Detalhe: resumo, motivo da nota e tags do item', 2600)
  await page.getByTestId('cluster').scrollIntoViewIfNeeded()
  await caption(
    page,
    withDup.duplicate_count > 0
      ? `Cluster: ${withDup.duplicate_count} duplicado(s) agrupado(s) por cosseno pgvector (> 0,88; entre 0,80 e 0,88 o LLM decide)`
      : 'Cluster do item: duplicados são agrupados por cosseno pgvector e pelo LLM na faixa ambígua',
    3600,
  )

  await page.getByTestId('nav-digests').click()
  await expect(page.getByTestId('digest-item').first()).toBeVisible()
  await caption(page, 'Digests diários: lista por data e leitura do HTML num iframe isolado', 2600)
  await page.getByTestId('generate-digest').click()
  await expect(page.getByTestId('toast').first()).toContainText('gerado')
  await caption(page, 'Gerar agora: seleção por nota de corte e cota por fonte, em Markdown e HTML', 3000)
  await page.getByTestId('digest-frame').hover()
  await page.mouse.wheel(0, 500)
  await page.waitForTimeout(1200)

  await page.getByTestId('nav-sources').click()
  await expect(page.getByTestId('source-card').first()).toBeVisible()
  await caption(page, 'Fontes & Config: criar, editar, ativar/desativar, excluir e "Coletar agora"', 2800)
  await page.getByTestId('source-new').click()
  await expect(page.getByTestId('source-form')).toBeVisible()
  await caption(page, 'Formulário validado pela API: Hacker News, subreddit ou feed RSS', 2200)
  await page.getByTestId('source-cancel').click()
  await page.getByTestId('settings-cron').scrollIntoViewIfNeeded()
  await caption(page, 'Settings do digest: nota de corte, máximo de itens, cota por fonte, cron e fuso', 2800)

  await page.getByTestId('nav-metrics').click()
  await expect(page.getByTestId('kpi-fetched')).toBeVisible()
  await caption(page, 'Métricas: itens/dia, por fonte, taxa de dedup, falhas e tokens do Ollama', 3000)
  await page.getByTestId('metrics-days-30').click()
  await page.mouse.wheel(0, 800)
  await caption(page, 'Gráficos em SVG próprio, com tooltip por teclado e tabela equivalente', 2800)
  await caption(page, 'Agora o mesmo painel no mobile (390x844)…', 1800)
  await d.done()

  const m = await recorded(browser, baseURL!, 'demo-mobile', { width: 390, height: 844 }, true)
  const mp = m.page
  await mp.goto('/?period=all')
  await expect(mp.getByTestId('feed-card').first()).toBeVisible()
  await caption(mp, 'Mobile: navegação inferior e filtros recolhidos', 2600)
  await mp.getByTestId('toggle-filters').click()
  await caption(mp, 'Filtros abrem sob demanda', 1800)
  await mp.getByTestId('toggle-filters').click()
  await mp.mouse.wheel(0, 900)
  await mp.waitForTimeout(1000)
  await mp.getByTestId('bnav-digests').click()
  await expect(mp.getByTestId('digest-item').first()).toBeVisible()
  await caption(mp, 'Digest legível no celular', 2200)
  await mp.getByTestId('bnav-metrics').click()
  await expect(mp.getByTestId('kpi-fetched')).toBeVisible()
  await caption(mp, 'Métricas em coluna única', 2000)
  await mp.mouse.wheel(0, 900)
  await caption(mp, 'Radar by JpDev · Go + Vue 3 · baseado no Horizon (Thysrael, MIT)', 3200)
  await m.done()
})
