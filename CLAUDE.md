# Horizon-Go — regras para agentes

Projeto: reescrita em Go + Vue 3 do Thysrael/Horizon (MIT). Leia `PLANO.md`. O código Python original está em `upstream/` (somente leitura, referência; não versionado).

## Diretrizes de colaboração técnica
- Crítica fundamentada: se o pedido/plano tiver falha técnica, aponte e proponha a alternativa melhor, com motivo.
- Inspeção real: leia o código antes de mudar; não presuma APIs. Cite arquivos.
- Validação real: só declare pronto depois de rodar build/testes e ver a saída. Nunca invente resultado.

## Regras de qualidade
- Todo elemento do front tem backend real funcionando. Sem mock, sem botão morto, sem dado fake no front.
- Testes Go (unit + integração com Postgres real) e e2e Playwright cobrindo cada tela e ação, em desktop (1440x900), tablet (834x1112) e mobile (390x844).
- Visual JpDev: preto (#0B0B0B/#141414) + amarelo (#FFD400) primário; laranja (#FF7A00) só apoio. Logo: `assets/jpdev-logo.png` (não redesenhar). Tipografia: Space Grotesk (títulos) + Inter (texto) + JetBrains Mono (números).
- IA: Ollama local em http://localhost:11434 — LLM `qwen3:14b` (use `"think": false` / formato json), embeddings `nomic-embed-text` (768 dims). Nunca invente chave de API.

## Infra local (já rodando, docker no WSL)
- Postgres+pgvector: `postgres://horizon:horizon@localhost:5450/horizon?sslmode=disable`
- NATS JetStream: `nats://localhost:4230` (monitor 8230)
- Go: `%LOCALAPPDATA%\Programs\go\bin\go.exe` (go1.27). Node 24. Shell: PowerShell no Windows.

## Git
- Commits pequenos, mensagem em inglês convencional (feat:, test:, chore:).
- PROIBIDO: Co-Authored-By, "Generated with", qualquer atribuição de IA na mensagem; `--no-verify`; force push; criar remoto; push.
- Não versionar `upstream/`, `node_modules/`, `videos/`, binários.

## Licença
MIT mantida; LICENSE com copyright do upstream (Thysrael 2026) + JpDev; README credita https://github.com/Thysrael/Horizon.
