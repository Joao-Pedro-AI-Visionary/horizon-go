# Horizon-Go — Radar by JpDev

Agregador de notícias tech que coleta Hacker News, Reddit e feeds RSS, dá uma nota de 0 a 10
a cada item com um LLM local (Ollama), agrupa duplicados por similaridade de embeddings
(pgvector) e gera um digest diário em Markdown/HTML, tudo visível num dashboard Vue 3.

É uma reescrita em **Go + Vue 3** do [Horizon](https://github.com/Thysrael/Horizon), de
Thysrael (Python, MIT). O plano e a análise do original estão em [`PLANO.md`](PLANO.md).

| Feed | Detalhe/Cluster | Métricas |
|---|---|---|
| ![Feed](docs/screenshots/1-feed-desktop.png) | ![Detalhe](docs/screenshots/2-item-desktop.png) | ![Métricas](docs/screenshots/5-metrics-desktop.png) |

## O que ele faz

- **Coleta concorrente**: uma goroutine por fonte (`errgroup` + `context`), fan-in num channel,
  dedup por URL normalizada e publicação em `items.raw` no NATS JetStream.
- **Nota por LLM**: `qwen3:14b` via Ollama (JSON, `think: false`) com rubrica inspirada nos
  profiles do original; nota, motivo, resumo e tags. Cache por hash de conteúdo e contagem de tokens.
- **Dedup semântico**: embeddings `nomic-embed-text` (768 dims) em pgvector com índice HNSW.
  Cosseno > 0,88 é duplicado direto; entre 0,80 e 0,88 o LLM decide; abaixo disso é item novo.
- **CQRS**: comandos gravam em `items`/`clusters`; a projeção `feed_view` é o read model da API.
- **Digest diário**: cron (`robfig/cron`) às 07:00 America/Sao_Paulo, seleção por nota de corte,
  máximo de itens e cota por fonte, renderizado em Markdown e HTML.
- **Dashboard**: Feed, Detalhe/Cluster, Digests, Fontes & Config e Métricas, em desktop,
  tablet e mobile. Toda ação chama a API real; não há mock no front.

## Arquitetura

Hexagonal: `domain` (entidades), `ports` (interfaces) e `adapters` (fontes, LLM, Postgres,
NATS, HTTP). Dois binários: `cmd/api` (REST, chi) e `cmd/worker` (coleta, nota, dedup, cron).

```mermaid
flowchart LR
  subgraph Fontes
    HN[Hacker News]
    RD[Reddit]
    RSS[RSS]
  end
  HN & RD & RSS -->|goroutine por fonte| FI((fan-in))
  FI -->|dedup por URL| RAW[[NATS items.raw]]
  API -->|Rodar pipeline / Coletar agora| FI
  RAW --> SC[Scorer pool<br/>qwen3:14b + nomic-embed-text]
  SC --> SCORED[[NATS items.scored]]
  SCORED --> DD[Deduper<br/>pgvector cosseno + LLM na faixa 0,80–0,88]
  DD --> PG[(Postgres 16 + pgvector<br/>items, clusters, ai_cache, llm_usage)]
  PG --> FV[Projeção feed_view]
  CRON[Cron 07:00] --> DG[Digest MD/HTML] --> PG
  FV --> API[API REST Go :8080]
  PG --> API
  API --> VUE[Dashboard Vue 3]
  OLL[(Ollama :11434)] -.-> SC
  OLL -.-> DD
```

### Estrutura

```
backend/                 módulo Go github.com/jpdev/horizon-go
  cmd/api                API REST (feed, itens, digests, fontes, settings, métricas, pipeline)
  cmd/worker             coleta contínua, scorer, deduper e cron do digest (--once, --seed)
  cmd/migrate            migrations goose embutidas (up|down|status)
  internal/domain        Item, Score, Cluster, Digest, limiares
  internal/ports         Source, LLM, Embedder, ItemRepo, EventBus, FeedQuery
  internal/adapters      source (hn, reddit, rss), llm (ollama), store/postgres, bus/natsbus, httpapi
  internal/app|ingest|enrich|digest|retry   casos de uso e estágios do pipeline
  migrations/            extensions, core tables, feed_view, ingestion, ai stages, api/digest
web/                     Vue 3 + Vite + TS + Pinia + Vue Router + Tailwind v4 (detalhes em web/README.md)
  e2e/                   suíte Playwright (telas + ações)
  demo/                  tour narrado que gera videos/demo-final.mp4
  scripts/               videos.mjs e demo-video.mjs (FFmpeg)
scripts/*.ps1            migrate, dev, test, e2e (Windows / PowerShell)
docker-compose.yml       Postgres pgvector pg16 + NATS JetStream
```

### API

| Método e rota | O que faz |
|---|---|
| `GET /healthz` | saúde (Postgres e NATS) |
| `GET /api/feed` | feed paginado (`source_id`, `tag`, `q`, `period` ou `from`/`to`, `min_score`, `sort`, `page`) |
| `GET /api/feed/timeline` | itens do feed por dia nos últimos `days` (1–90, padrão 30), mesmos filtros não-temporais; alimenta o scrubber de datas |
| `GET /api/items/{id}` | item, cluster e duplicados com similaridade |
| `GET /api/digests` · `GET /api/digests/{date}` · `POST /api/digests/generate` | digests (`format=html\|md`) |
| `GET/POST /api/sources` · `PATCH/DELETE /api/sources/{id}` · `POST /api/sources/{id}/fetch` | CRUD de fontes e coleta manual |
| `GET/PUT /api/settings` | nota de corte, máximo de itens, cota por fonte, cron e fuso do digest |
| `GET /api/metrics?days=7\|14\|30` | itens/dia, por fonte, dedup, falhas e tokens |
| `POST /api/pipeline/run` | coleta todas as fontes ativas e publica no NATS |

## Como rodar no Windows (Docker no WSL)

Requisitos: Go 1.26+ (o `go.mod` pede 1.26), Node 24, Docker dentro do WSL, Ollama no Windows
e FFmpeg no `PATH` (só para os vídeos).

```powershell
# 1. Infra no WSL: Postgres+pgvector em :5450 e NATS JetStream em :4230 (monitor :8230)
wsl docker compose up -d

# 2. Modelos locais
ollama pull qwen3:14b
ollama pull nomic-embed-text

# 3. Banco e dependências do front
.\scripts\migrate.ps1 up
cd web; npm install; cd ..

# 4. Backend (dois terminais, na pasta backend)
go run ./cmd/worker --seed     # insere HN, r/golang e Go blog e fica em modo contínuo
go run ./cmd/api               # :8080

# 5. Front
.\scripts\dev.ps1              # Vite em http://localhost:5173 (proxy de /api para :8080)
```

O WSL repassa as portas publicadas para `localhost` do Windows, então o DSN padrão
`postgres://horizon:horizon@localhost:5450/horizon?sslmode=disable` e `nats://localhost:4230`
funcionam sem configuração. Variáveis: `DATABASE_URL`, `NATS_URL`, `API_ADDR`, `CORS_ORIGINS`,
`OLLAMA_URL`, `LLM_MODEL`, `EMBED_MODEL`, `LLM_NUM_CTX`, `SCORE_WORKERS`.

## Como rodar via docker-compose (Linux/macOS)

```bash
docker compose up -d           # pgvector/pgvector:pg16 + nats:2.10 -js, mesmas portas
make migrate
cd backend && go run ./cmd/worker --seed &   # e em outro terminal: go run ./cmd/api
make dev                       # Vite em :5173
docker compose down            # para; o volume pgdata é preservado
```

O compose sobe só a infra; API, worker e Vite rodam no host. O Ollama também fica no host
(`http://localhost:11434`).

## Testes

```powershell
.\scripts\test.ps1             # go build + go vet + go test + vue-tsc + vite build
cd backend; go test ./... -count=1 -cover
.\scripts\e2e.ps1              # sobe api + worker, roda o Playwright, gera os vídeos, para tudo pelo PID
.\scripts\e2e.ps1 -Demo        # idem + grava videos/demo-final.mp4
```

- **Go**: testes de unidade e de integração contra Postgres e NATS reais. A integração usa um
  banco próprio (criado e migrado pelo teste) e é pulada com `HORIZON_SKIP_INTEGRATION=1`.
- **E2E (Playwright)**: `web/e2e/screens.spec.ts` abre as 5 telas e salva os screenshots;
  `web/e2e/actions.spec.ts` cobre filtros, busca, tag, ordem, nota mínima, paginação, rodar pipeline,
  abrir detalhe e 404 de item, gerar e ler digest, CRUD de fonte com erro de validação, ativar/desativar,
  coletar agora, salvar settings com cron inválido, métricas (período, atualizar, tabela) e navegação.
  `web/e2e/widgets.spec.ts` cobre o scrubber de datas (teclado, arrasto, toque, link salvo), as pílulas
  dos segmentados, os painéis de disclosure e o reduced motion (detalhes em `docs/DESIGN.md` › Referência 2).
  Cada teste roda em 3 projects: **desktop 1440x900**, **tablet 834x1112** e **mobile 390x844**,
  falha em erro de console, exceção de página ou 5xx, e checa overflow horizontal.
  Os testes que alteram dados apagam a fonte criada e restauram as settings.
- **Vídeos**: com `video: 'on'`, `web/scripts/videos.mjs` lê `playwright-report/results.json` e
  gera `videos/e2e-desktop.mp4`, `videos/e2e-tablet.mp4` e `videos/e2e-mobile.mp4` (H.264), com
  uma legenda por clipe (número, resultado, arquivo e nome do teste). `videos/demo-final.mp4` é o
  tour narrado por legendas: pipeline rodando, feed, detalhe com cluster, digest, fontes, métricas
  e a troca para o mobile. A pasta `videos/` não é versionada.

## Referências do Dribbble

Escolhidas pelos títulos e descrições na busca do Dribbble; o que foi aproveitado de cada uma:

| Referência | O que foi aproveitado |
|---|---|
| [Analytics UI components — bento grid design, Naimur Rahman](https://dribbble.com/shots/25096075-Analytics-Ui-components-bento-grid-design) | KPIs em tiles iguais no topo e gráfico principal ocupando 3/4 da linha (Métricas) |
| [Bento grid & dark mode designs, Naavyd](https://dribbble.com/shots/23353521-Bento-grid-dark-mode-designs) | Cards escuros com borda fina e cantos de 16px em vez de sombra |
| [Bento grid & dashboard for revops startup, Robin Holesinsky](https://dribbble.com/shots/25765472-Bento-grid-dashboard-for-revops-startup) | Blocos de tamanhos diferentes na mesma grade |
| [Bento Grid — Dashboard UI Widgets, Jigar Fotaria](https://dribbble.com/shots/22968599-Bento-Grid-Dashboard-UI-Widgets) | Número grande em fonte mono como herói do tile |
| [AI News Aggregator App, Hasan Mahmud](https://dribbble.com/shots/23806625-AI-News-Aggregator-App) | Card de notícia com fonte · domínio · tempo acima do título e tags em chips |
| [Dark UI — News Feed dashboard, Iza Bulska](https://dribbble.com/shots/2730818-Dark-UI-News-Feed-dashboard) | Feed em duas colunas no desktop com filtros fixos acima |

Identidade visual JpDev: preto `#0B0B0B`/`#141414`, amarelo `#FFD400` como primária, laranja
`#FF7A00` só de apoio; Space Grotesk, Inter e JetBrains Mono. Decisões de design e acessibilidade
em [`web/README.md`](web/README.md).

## Créditos e licença

Baseado em [Horizon](https://github.com/Thysrael/Horizon) © 2026 Thysrael, licença MIT.
A rubrica de nota e a ideia de dedup por tema foram adaptadas dos prompts/profiles do original,
com atribuição.

Licença **MIT**, mantida do upstream — veja [`LICENSE`](LICENSE) (copyright Thysrael 2026 + JpDev)
e [`NOTICE`](NOTICE).
