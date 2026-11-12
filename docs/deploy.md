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
3. Sobe o worker (com `--seed`, que cria as fontes padrão só se a tabela
   `sources` estiver vazia) e a API
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
   - `HORIZON_ADMIN_TOKEN`: token das rotas de escrita (`openssl rand -hex 32`).
     Sem ele, em produção, toda escrita responde `503`.
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
   **Só essa.** Toda variável `VITE_` vai pro bundle público: nunca ponha o
   token de admin nem outra chave na Vercel. O token é digitado no painel
   (Fontes & Config → Desbloquear edição) e fica só no `localStorage` do
   navegador.
3. Faça o deploy, copie o domínio final e confira se ele está em
   `HORIZON_CORS_ORIGINS` no Render. Se mudar o valor, o Render reinicia o
   serviço sozinho.

## Coleta 2x por dia (serviço dormindo)

O Render free **dorme depois de 15 minutos sem tráfego de entrada**. Por isso
o worker não coleta sozinho (`COLLECT_INTERVAL=off`): ele só pontua, deduplica
e roda o digest enquanto está acordado. As coletas vêm do workflow
`.github/workflows/wake-and-collect.yml`:

| Horário (America/Sao_Paulo) | Cron (UTC) |
|---|---|
| 07:30 | `30 10 * * *` |
| 16:30 | `30 19 * * *` |

Cada execução acorda o serviço (`GET /healthz`, até 10 tentativas), depois faz
`POST /api/pipeline/run` com `Authorization: Bearer $HORIZON_ADMIN_TOKEN`,
tentando de novo em erro 5xx. `409` (coleta em andamento) e `429`
(`COLLECT_COOLDOWN=10m`) contam como sucesso. `COLLECT_WINDOW=16h` cobre o
intervalo de 15 h entre as duas execuções.

Segredos do repositório (*Settings → Secrets and variables → Actions*):
`HORIZON_API_URL` (URL do Render, sem barra) e `HORIZON_ADMIN_TOKEN`.
O GitHub só roda `schedule` a partir da branch padrão (`main`) e pode atrasar
alguns minutos.

Abrir o front acorda o serviço, mas **não** dispara coleta.

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

Leituras (`GET`) são públicas. **Toda escrita** em `/api` (`POST`, `PUT`,
`PATCH`, `DELETE`: fontes, settings, `pipeline/run`, `sources/{id}/fetch`,
`digests/generate`, `items/ingest`) exige `Authorization: Bearer <token>`,
onde o token é um destes:

- **Sessão de login:** o painel tem a página `/login` (link discreto "Entrar"
  no rodapé). `POST /api/auth/login` com `{"username","password"}` confere o
  usuário em tempo constante e a senha com bcrypt, e devolve um token de sessão
  assinado (HMAC-SHA256 com `SESSION_SECRET`) que vale 12 h. Ele é Bearer, não
  cookie, porque Vercel e Render são sites diferentes e o navegador bloqueia
  cookie de terceiros. O painel guarda o token no `sessionStorage` e sai
  sozinho quando ele expira ou quando uma escrita volta `401`.
  `GET /api/auth/me` valida o token.
  - Limite: 5 tentativas erradas por minuto por IP. Depois disso o IP fica
    bloqueado por 1 min, e cada erro seguinte dobra o bloqueio, até 15 min
    (`429` com `Retry-After`). A mensagem de erro é sempre genérica.
- **`HORIZON_ADMIN_TOKEN`:** token fixo das automações (o workflow 2x/dia e o
  push do radar do X).

Sem token ou com token inválido, a API responde `401`. Em produção
(`HORIZON_ENV=production` ou no Render), sem nenhuma credencial configurada,
as escritas falham fechadas com `503`. Localmente, sem credenciais, tudo
continua aberto como antes. O `GET /api/auth` público diz o modo (`open`,
`login`, `token` ou `disabled`).

### Esqueci minha senha

Na página `/login`, o link "Esqueci minha senha" abre `/forgot`, com um botão
só. `POST /api/auth/forgot` responde sempre o mesmo 200 genérico e **não
aceita destinatário**: o link vai só pra `RESET_EMAIL_TO`, via Resend
(`RESEND_API_KEY`, remetente `RESET_FROM`). Limites: 3 pedidos por IP a cada
15 min e no máximo 5 e-mails por hora no total.

- O link `{FRONT_URL}/reset?token=…` vale 30 min e funciona uma vez. No banco
  fica só o sha256 do token (tabela `password_resets`).
- `POST /api/auth/reset {token, new_password}` (10+ caracteres) grava o hash
  bcrypt novo em `admin_credentials`. A partir daí o login usa essa senha, e
  não mais o `ADMIN_PASSWORD_HASH`. Também sobe a `session_version`, o que
  derruba todas as sessões abertas. O `HORIZON_ADMIN_TOKEN` das automações não
  muda.
- Pra voltar a usar a senha do Render: `DELETE FROM admin_credentials;` no
  Neon.

### Trocar a senha do admin

1. Gere o hash bcrypt da nova senha (12+ caracteres), na pasta `backend/`:

   ```bash
   printf '%s' 'NOVA-SENHA-FORTE' | go run ./cmd/hashpw
   ```

   Sem Go: `htpasswd -bnBC 12 "" 'NOVA-SENHA-FORTE' | tr -d ':\n'`.
2. No Render (*Environment*), troque `ADMIN_PASSWORD_HASH` pelo hash e salve.
   O serviço reinicia sozinho.
3. Pra derrubar todas as sessões abertas, troque também o `SESSION_SECRET`
   (`openssl rand -hex 32`).

## Variáveis (referência)

Veja `.env.production.example`. Resumo:

| Variável | Produção | Observação |
|---|---|---|
| `DATABASE_URL` | segredo | Neon direto, `sslmode=require` |
| `GROQ_API_KEY` | segredo | |
| `CF_ACCOUNT_ID`, `CF_API_TOKEN` | segredo | |
| `HORIZON_CORS_ORIGINS` | segredo/config | domínio da Vercel |
| `HORIZON_ADMIN_TOKEN` | segredo | token fixo das automações (workflow, push do X) |
| `ADMIN_USERNAME` | config | usuário do login do painel |
| `ADMIN_PASSWORD_HASH` | segredo | hash bcrypt da senha (`ADMIN_PASSWORD` em texto puro é aceito como fallback) |
| `SESSION_SECRET` | segredo | chave HMAC das sessões de 12 h (32+ caracteres) |
| `RESEND_API_KEY` | segredo | envio do e-mail de "esqueci minha senha" (Resend) |
| `RESET_EMAIL_TO` | config | único destinatário do link de redefinição |
| `RESET_FROM` | config | remetente (padrão `Horizon <onboarding@resend.dev>`) |
| `FRONT_URL` | config | URL do painel usada no link (`https://horizon-web-eight.vercel.app`) |
| `HORIZON_ENV` | `production` | liga o fail-closed (o Render também é detectado por `RENDER`) |
| `LLM_PROVIDER` | `groq` | `ollama` (padrão local), `groq` ou `openai` |
| `LLM_MODEL` | `openai/gpt-oss-20b` | |
| `LLM_RPM` | `20` | |
| `EMBED_PROVIDER` | `cloudflare` | `ollama` (padrão local), `cloudflare` ou `none` |
| `EMBED_MODEL` | `@cf/baai/bge-base-en-v1.5` | precisa ter 768 dims |
| `SCORE_WORKERS` | `1` | |
| `COLLECT_INTERVAL` | `off` | sem coleta própria; o GitHub Actions dispara 07:30 e 16:30 BRT (`30m` local) |
| `COLLECT_WINDOW` | `16h` | quão para trás uma coleta olha |
| `COLLECT_COOLDOWN` | `10m` | intervalo mínimo entre coletas disparadas pela API |
| `WORKER_SEED` | `true` | cria as fontes padrão só com a tabela `sources` vazia |
| `PORT` | Render define | a API usa `:$PORT` se `API_ADDR` estiver vazio |
| `MIGRATE_ON_BOOT` | `true` (padrão) | |

## Fontes padrão

25 feeds RSS/Atom (`backend/internal/ingest/ingest.go`), cada um com `limit`
(só os N mais novos por coleta), somando no máximo 94 itens por coleta. Com
duas coletas por dia e dedup por URL, isso fica dentro das ~100 a 130 notícias
pontuadas por dia do Groq free. O feed de trending do GitHub não data os
itens, por isso usa `"undated": true` (data do próprio feed).

## Depois do merge

O serviço do Render foi criado apontando pra branch de deploy. Quando os PRs
forem mergeados, troque a branch do serviço pra `main` (*Settings → Build &
Deploy → Branch*).

## Itens empurrados (radar do X)

A API do X é paga, então os posts de alto engajamento chegam por push:
`POST /api/items/ingest` (precisa do token de admin) cria, se faltar, a fonte
`X (radar)` do tipo `push`, que o coletor nunca busca. Cada item vai pro
`items.raw` e segue o caminho normal (persistência, nota do Groq, embedding,
dedup). URLs já gravadas ou repetidas no lote são ignoradas.

```bash
curl -X POST "$HORIZON_API_URL/api/items/ingest" \
  -H "Authorization: Bearer $HORIZON_ADMIN_TOKEN" -H 'Content-Type: application/json' \
  -d '[{"source":"X","url":"https://x.com/user/status/123","title":"Título curto",
        "text":"Texto do post","author":"@user","published_at":"2026-11-12T10:00:00Z",
        "metrics":{"views":120000,"likes":3400,"reposts":510}}]'
```

- Corpo: array JSON com 1 a 50 itens. `url` (http/https) e `title` ou `text`
  são obrigatórios; sem `title`, a primeira linha do `text` vira título.
  `published_at` (RFC 3339) é opcional; sem ele vale a hora do envio.
- Campos desconhecidos dão `400`. A resposta é `202` com
  `{"run_id","source_id","source_name","received","accepted","duplicates","invalid","errors"}`.
- As métricas entram no texto que o modelo lê ("Engagement: 120.0K views…").
- Recomendado: até 20 itens por chamada (cada item custa uma chamada ao Groq).
