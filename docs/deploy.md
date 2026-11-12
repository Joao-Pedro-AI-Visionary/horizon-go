# Deploy grátis: Render + Neon + Groq + Cloudflare Workers AI + Vercel

Este guia coloca o Horizon no ar sem custo:

| Peça | Onde | Plano |
|---|---|---|
| Front (horizon-web) | Vercel | Hobby (grátis) |
| Backend: API + worker + NATS JetStream, **um container só** | Render, web service Docker | Free |
| Postgres + pgvector | Neon | Free |
| Pontuação das notícias (LLM) | Groq, `openai/gpt-oss-20b` | Free |
| Embeddings (dedup semântico) | Cloudflare Workers AI, `@cf/baai/bge-base-en-v1.5` (768 dims) | Free (10.000 Neurons/dia) |

Localmente nada muda: sem variáveis novas, o worker continua usando o Ollama
(`LLM_PROVIDER=ollama` e `EMBED_PROVIDER=ollama` são os padrões).

## Como o container funciona

O `Dockerfile` da raiz gera uma imagem distroless com quatro binários. O
`/app/supervise` é o entrypoint e faz isto:

1. Sobe o `nats-server` com JetStream em `127.0.0.1:4222` (config em
   `deploy/nats.conf`, store em `/tmp/nats`).
2. Roda as migrations do goose (embutidas no binário), tentando de novo
   enquanto o Neon acorda.
3. Sobe o worker (com `--seed`, que cria as fontes padrão se faltarem) e a API
   na porta `$PORT` que o Render define.
4. Se qualquer processo morrer, encerra os outros e sai com erro, e o Render
   reinicia o container.

Memória medida no smoke test: nats ≈ 17 MB, worker ≈ 22 MB, api ≈ 16 MB e
supervise ≈ 10 MB de RSS, bem abaixo dos 512 MB do plano free.
`GOMEMLIMIT` por processo e `SCORE_WORKERS=1` já vêm na imagem.

## 1. Neon (Postgres + pgvector)

1. Crie a conta em <https://console.neon.tech> e um projeto `horizon` com
   Postgres 17, na região **AWS US East (N. Virginia)**, a mesma do Render
   `virginia`.
2. Em **Connect**, desmarque *Connection pooling* e copie a URL **direta**
   (o host sem `-pooler`). Ela termina em `?sslmode=require&channel_binding=require`.
   Pode colar assim mesmo: o app remove o `channel_binding`, que o driver pgx
   não suporta, e mantém o TLS (`sslmode=require`).
3. Não precisa rodar nada à mão. A extensão `vector` (pgvector 0.8) é criada
   pela primeira migration (`CREATE EXTENSION IF NOT EXISTS vector`), que o
   Neon permite, e o container migra sozinho a cada boot.

Use a URL direta porque as migrations usam advisory lock do goose e o app
abre poucas conexões. O pooler (PgBouncer) não traz ganho aqui.

## 2. Groq (pontuação)

1. Crie a conta em <https://console.groq.com> e gere a chave em **API Keys**.
   Ela começa com `gsk_`.
2. O modelo padrão é o `openai/gpt-oss-20b`, que está no plano free e tem JSON
   mode. O adapter manda `reasoning_effort=low` pra gastar poucos tokens de
   raciocínio. O `llama-3.1-8b-instant` passou a ser Enterprise e não aparece
   mais nos limites do plano free.

## 3. Cloudflare Workers AI (embeddings)

1. Crie a conta em <https://dash.cloudflare.com>. O **Account ID** aparece na
   página inicial da conta (ou em *Workers & Pages*, na coluna da direita).
2. Em **My Profile → API Tokens → Create Token**, use o template
   **Workers AI**. Ele dá a permissão *Account → Workers AI*, que basta.
3. O modelo `@cf/baai/bge-base-en-v1.5` devolve vetores de **768 dimensões**,
   o mesmo tamanho da coluna `items.embedding vector(768)` (antes
   `nomic-embed-text`). Por isso não há migration nova. O adapter usa
   `pooling: "cls"`, recomendado pela Cloudflare, e manda lotes de até 100
   textos.

**Se a cota diária acabar**, o adapter devolve `ErrEmbeddingUnavailable` e
para de chamar a API até 00:00 UTC. Nesse período o item é pontuado
normalmente e entra no feed como cluster próprio (`duplicate_count` 0), sem
erro nem retry. `EMBED_PROVIDER=none` faz o mesmo de forma permanente.

## 4. Render (backend)

1. Crie a conta em <https://render.com> e conecte o GitHub.
2. Vá em **New → Blueprint**, escolha o repositório `horizon-go` e o Render lê
   o `render.yaml`: um web service Docker `horizon-api`, plano free, região
   virginia, health check em `/healthz`.
3. O Render pede os segredos marcados com `sync: false`:
   - `DATABASE_URL`: a URL direta do Neon
   - `GROQ_API_KEY`
   - `CF_ACCOUNT_ID` e `CF_API_TOKEN`
   - `HORIZON_CORS_ORIGINS`: o domínio da Vercel, por exemplo
     `https://horizon-web.vercel.app`. Separe vários por vírgula e não coloque
     barra no final. Se ainda não tiver o domínio, ponha um provisório e
     troque depois do passo 5.
4. Depois do deploy, abra `https://horizon-api.onrender.com/healthz` (a URL
   aparece no painel). A resposta esperada é
   `{"nats":"ok","postgres":"ok","status":"ok"}`.

## 5. Vercel (front)

1. Em <https://vercel.com>, faça **Add New → Project** e importe o
   `horizon-web`. O framework Vite é detectado sozinho.
2. Em **Environment Variables**, defina
   `VITE_API_URL=https://horizon-api.onrender.com`, sem barra no final.
3. Faça o deploy, copie o domínio final e confira se ele está em
   `HORIZON_CORS_ORIGINS` no Render. Se mudar o valor, o Render reinicia o
   serviço sozinho.

## Coleta com o serviço dormindo

O Render free **dorme depois de 15 minutos sem tráfego de entrada**, e o
tráfego de saída (coletas, Groq) não conta. Enquanto ele dorme, nada roda: nem
o ticker de coleta de 30 minutos, nem o cron do digest diário. O que foi feito
pra lidar com isso:

- **Coleta ao acordar:** cada spin-up reinicia o container, e o worker coleta
  logo no boot. Abrir o front já dispara uma coleta.
- **Coleta sob demanda:** `POST /api/pipeline/run`, que é o botão do front.
  Pra não queimar a cota grátis, `COLLECT_COOLDOWN=10m` responde `429` com
  `Retry-After` se outra coleta tiver começado há menos de 10 minutos.
- **Cron externo grátis (opcional):** use uma das duas opções.
  - <https://cron-job.org>: crie um job a cada 30 a 60 minutos com
    `POST https://horizon-api.onrender.com/api/pipeline/run`, ou um `GET` em
    `/healthz` só pra acordar o serviço.
  - GitHub Actions: o workflow `.github/workflows/wake-and-collect.yml` vem
    desligado. Pra ligar, crie a *variável* de repositório `HORIZON_API_URL` e
    descomente o `schedule`. Ele não usa segredos.

Um ping a cada 30 minutos deixa o serviço acordado cerca de metade do tempo
(uns 370 h/mês), dentro das 750 h grátis. Manter acordado 24/7 com ping a cada
14 minutos dá 744 h, que cabe, mas consome toda a cota do workspace e mais
tokens do Groq.

## Limites grátis (verificados em 12/11/2026)

| Serviço | Limite | Fonte |
|---|---|---|
| Render Free | 512 MB de RAM e menos de 1 CPU. Dorme depois de 15 min sem tráfego de entrada e leva cerca de 1 min pra acordar. Disco efêmero (perde arquivos ao reiniciar ou dormir). 750 h de instância por mês por workspace. Pode reiniciar a qualquer momento. | <https://render.com/docs/free>, <https://render.com/pricing> |
| Neon Free | 100 CU-hours por projeto por mês, 1 GB de storage por projeto, 5 GB de egress. Suspende o compute depois de 5 min ocioso (não dá pra desligar), e o primeiro acesso depois disso leva uns segundos. | <https://neon.com/docs/introduction/plans> |
| Neon pgvector | `CREATE EXTENSION vector` é suportado (0.8.0) | <https://neon.com/docs/extensions/pgvector> |
| Groq Free, `openai/gpt-oss-20b` | 30 RPM, 1.000 requisições/dia, 8.000 tokens/min, 200.000 tokens/dia. Os headers de uma chamada real confirmaram 1.000 RPD e 8.000 TPM. | <https://console.groq.com/docs/rate-limits>, <https://console.groq.com/docs/models> |
| Cloudflare Workers AI Free | 10.000 Neurons/dia, zerando às 00:00 UTC. O bge-base custa 6.058 Neurons por milhão de tokens de entrada (cerca de 1,6 M tokens/dia). Embeddings têm limite de 3.000 req/min. Entrada máxima de 512 tokens. | <https://developers.cloudflare.com/workers-ai/platform/pricing/>, <https://developers.cloudflare.com/workers-ai/platform/limits/>, <https://developers.cloudflare.com/workers-ai/models/bge-base-en-v1.5/> |

O que isso significa na prática:

- **O gargalo é o Groq.** Cada notícia custa cerca de 1,5 a 2 mil tokens
  (prompt de até 4 mil caracteres mais a resposta), então os 200 mil tokens por
  dia pontuam em torno de **100 a 130 notícias novas por dia**. O adapter
  espaça as chamadas (`LLM_RPM=20`), espera a janela de tokens por minuto
  quando os headers `x-ratelimit-remaining-tokens` ficam baixos e respeita o
  `Retry-After` dos 429, com no máximo 3 tentativas. Quando a cota do dia
  acaba, o adapter para de chamar até o horário indicado, os itens ficam como
  `raw` e o backfill de 10 minutos tenta de novo depois, sem martelar a API.
- **JetStream é efêmero.** Mensagens pendentes se perdem quando o serviço
  reinicia ou dorme. Isso é aceitável, porque tudo está no Postgres e o
  backfill do scorer e do deduper recoloca na fila o que ficou sem score ou
  sem cluster.
- **Cold start:** o primeiro acesso depois de dormir leva cerca de 1 minuto,
  com o Render mostrando uma página de loading. Se o Neon também estiver
  suspenso, somam-se uns segundos.

## Segurança

As rotas de escrita da API (`/api/sources`, `/api/settings`,
`/api/pipeline/run`) **não têm autenticação**, assim como no ambiente local.
Qualquer pessoa com a URL do Render consegue editar fontes ou disparar
coletas. O cooldown limita o abuso de cota, mas não substitui um login. Se o
app ficar público de verdade, o próximo passo é proteger essas rotas.

## Variáveis (referência)

Veja `.env.production.example`. Resumo:

| Variável | Produção | Observação |
|---|---|---|
| `DATABASE_URL` | segredo | Neon direto, `sslmode=require` |
| `GROQ_API_KEY` | segredo | |
| `CF_ACCOUNT_ID`, `CF_API_TOKEN` | segredo | |
| `HORIZON_CORS_ORIGINS` | segredo/config | domínio da Vercel |
| `LLM_PROVIDER` | `groq` | `ollama` (padrão local), `groq` ou `openai` |
| `LLM_MODEL` | `openai/gpt-oss-20b` | |
| `LLM_RPM` | `20` | |
| `EMBED_PROVIDER` | `cloudflare` | `ollama` (padrão local), `cloudflare` ou `none` |
| `EMBED_MODEL` | `@cf/baai/bge-base-en-v1.5` | precisa ter 768 dims |
| `SCORE_WORKERS` | `1` | |
| `COLLECT_INTERVAL` | `30m` | ticker do worker (só enquanto acordado) |
| `COLLECT_COOLDOWN` | `10m` | intervalo mínimo entre coletas disparadas pela API |
| `WORKER_SEED` | `true` | cria as fontes padrão se faltarem (idempotente) |
| `PORT` | Render define | a API usa `:$PORT` se `API_ADDR` estiver vazio |
| `MIGRATE_ON_BOOT` | `true` (padrão) | |
