# Horizon-Go — Radar by JpDev

Agregador de notícias tech com nota por LLM, dedup semântico (pgvector) e digest
diário. Reescrita em **Go + Vue 3** do [Horizon](https://github.com/Thysrael/Horizon)
de Thysrael (MIT). Plano completo em [`PLANO.md`](PLANO.md).

> Status: **Fase 0** — monorepo, migrations e tooling. Pipeline, API e telas chegam nas fases 1–4.

## Estrutura

```
backend/                 módulo Go github.com/jpdev/horizon-go (hexagonal)
  cmd/api                API REST de leitura (Fase 3)
  cmd/worker             pipeline de ingestão (Fase 1+)
  cmd/migrate            aplica as migrations goose embutidas
  internal/domain        entidades (Item, Score, Cluster, Digest)
  internal/ports         interfaces (Source, LLM, Embedder, ItemRepo, EventBus, FeedQuery)
  internal/adapters/     source, llm, store, bus, http
  internal/app           casos de uso / orquestração
  migrations/            SQL goose: vector, sources, items (vector(768)), clusters,
                         digests, feed_view (projeção CQRS), índices HNSW
web/                     Vue 3 + Vite + TS + Pinia + Vue Router + Tailwind v4 + Playwright
scripts/*.ps1            dev, test, e2e, migrate (Windows / PowerShell)
Makefile                 mesmos alvos para Unix
docker-compose.yml       alternativa: pgvector pg16 + NATS JetStream
```

## Requisitos

- Go 1.23+ · Node 24 · Postgres 16 + pgvector · NATS JetStream
- Ollama local (`qwen3:14b`, `nomic-embed-text`) — a partir da Fase 2

Infra padrão: Postgres em `localhost:5450` (user/senha/db `horizon`) e NATS em
`localhost:4230`. Sem ela rodando, use `docker compose up -d` (mesmas portas).

## Comandos

```powershell
.\scripts\migrate.ps1          # goose up (DATABASE_URL ou o DSN padrão)
.\scripts\test.ps1             # go build/vet/test + web build
.\scripts\e2e.ps1              # Playwright: desktop 1440x900, tablet 834x1112, mobile 390x844
.\scripts\dev.ps1              # migrations + Vite dev server
```

## Créditos e licença

MIT — veja [`LICENSE`](LICENSE) e [`NOTICE`](NOTICE).
Baseado em [Horizon](https://github.com/Thysrael/Horizon) © 2026 Thysrael, MIT.
