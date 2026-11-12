# Design — Radar by JpDev (fases R1 e R2)

Redesign editorial do front Vue 3, hoje no repo [horizon-web](https://github.com/Joao-Pedro-AI-Visionary/horizon-web) (cópia deste documento em `docs/DESIGN.md` de lá). Referência estudada: Tubik, *The Alabama Solution* — páginas editoriais para a investigação da HBO (Webby 2026). As imagens do case foram baixadas para `.runs/ref/` (fora do git) só para estudo; nada da referência foi copiado: marca, cores, fontes e conteúdo são do Radar/JpDev.

## 1. O que a referência ensina

| Padrão na referência | Como aparece lá |
|---|---|
| Grade visível | Linhas verticais finas atravessam a página inteira; o conteúdo se alinha a elas. |
| Metadados nos cantos | "Profiles / 1 377" no canto esquerdo e "Data period / 2019–2024" no direito, pequenos, em duas linhas. |
| Display em caixa alta | Títulos grotescos, condensados, em caixa alta, entrelinha ~0,9. Parte da frase fica apagada para dar ênfase ao resto. |
| Narrativa por dados | Uma frase por vez, com o número como protagonista ("ten-fold", "seven times"). |
| Revelação progressiva | "Taken together, the data shows:": cada linha nova entra em branco e as anteriores escurecem. |
| Gráfico com draw-in | Linha fina com área em gradiente, eixos discretos, fonte do dado em versalete abaixo. |
| Blocos chapados | Cartões de cor sólida, sem sombra; contraste alto entre preto e papel. |
| Mobile em coluna única | Mesmo ritmo tipográfico, gráfico ocupando a largura toda. |

## 2. Mapeamento por tela

| Tela | Aplicação |
|---|---|
| **Shell** | Barra superior com seções numeradas (`01 Feed`, `02 Digests`…) separadas por fios verticais, sublinhado amarelo animado na seção ativa, saúde real da API (`/healthz`) à direita. Grade de colunas 4/6/12 no fundo (`.grid-rules`). Mobile: tab bar inferior de 4 itens, 64px de altura. |
| **Cabeçalho de página** (`PageHeader`) | Linha de canto `01 — Feed` / meta à direita, título display em caixa alta com as últimas palavras apagadas, lead e ações. As palavras sobem de uma máscara em sequência. |
| **Feed** | Faixa de 3 números de canto (itens no feed, fontes ativas/total, nota de corte), com contagem animada. A nº 1 vira **manchete**: nota gigante com medidor de 10 passos, título display, resumo, "por que essa nota" como citação. O resto é uma **lista numerada** (02, 03…) separada por fios, não cartões. Período e ordem viraram controles segmentados (um clique, sem abrir select). |
| **Item** | Cabeçalho de artigo: tipo/fonte/autor em mono, título display, CTA "Abrir original". Nota gigante na coluna lateral. Corpo: resumo em corpo grande, motivo como citação com fio amarelo, tags, conteúdo coletado em caixa rolável. Lateral: metadados em lista com fios e cluster com **barra de similaridade** por duplicado. |
| **Digests** | Índice de edições (`Nº 03 · data · itens`) com fio amarelo na selecionada; leitor com faixa "itens no digest / considerados" e o HTML do servidor em iframe sandbox. |
| **Fontes & Config** | Lista numerada de fontes com estatísticas em colunas separadas por fios (itens, última coleta, status), switch, ações. Configuração num painel fixo à direita, nota de corte com número grande. |
| **Métricas** | **Frase-dado** montada com os números reais ("Em 14 dias o radar coletou N itens…"). Faixa de 4 KPIs grandes com contagem. Figuras numeradas (Fig. 1–5): área de itens por dia com draw-in, status com barras, fontes com barras, falhas, tokens em barras que crescem. Toda figura mantém tooltip por teclado e "Ver tabela". |
| **404** | "404" gigante em amarelo, rota pedida em mono, um único botão de volta ao feed. |

Nada foi inventado: todo número vem de `/api/feed`, `/api/sources`, `/api/settings`, `/api/items/:id`, `/api/digests` ou `/api/metrics`. A frase de métricas só recombina esses campos.

## 3. Tokens

**Cores** (`src/style.css` (horizon-web), `@theme`)

| Token | Valor | Uso |
|---|---|---|
| `ink` | `#0B0B0B` | fundo |
| `surface` | `#141414` | painéis, controles |
| `raised` | `#1C1C1C` | skeleton, trilho |
| `line` | `#2A2A2A` | fios e bordas |
| `rule` | `#FFFFFF0F` | grade decorativa |
| `muted` | `#A3A3A3` | texto secundário (7,9:1 sobre ink) |
| `primary` | `#FFD400` | ação principal, nota acima do corte, seção ativa |
| `accent` | `#FF7A00` | só apoio: duplicados, falhas |
| `danger` | `#FF6B6B` | erros (5,9:1 sobre surface) |
| `series-2` | `#4F8DF7` | 2ª série dos gráficos |

**Tipografia**: Space Grotesk (display), Inter (texto, base 16px/1,5), JetBrains Mono (números, `tabular-nums`, eyebrows).

| Nome | Tamanho | Uso |
|---|---|---|
| `display-xl` | `clamp(2.6rem, 7.2vw, 6.5rem)`, caixa alta, lh 0,9, tracking −0,035em | título da página |
| `display-lg` | `clamp(1.9rem, 4.2vw, 3.75rem)`, lh 1,02 | manchete, título do item, estados vazios |
| nota `xl` | `clamp(4.5rem, 9vw, 7.5rem)` mono | nota da manchete e do item |
| KPI | 36px → 60px (md) mono | números de métricas |
| `eyebrow` | 12px mono caixa alta, tracking 0,14em | rótulos de canto |

**Espaço**: escala do Tailwind (4px). Seções a 40–56px; linhas da lista com 24–28px de respiro; container máximo de 1360px com 16/32px de margem.
**Raios**: 6px em controles, 10px em painéis, pílula só em chips. Sem sombras, exceto toast e diálogos.

## 4. Motion

| Item | Duração | Easing | Detalhe |
|---|---|---|---|
| Micro (hover, cor, press) | 150ms | `ease` | `active:scale(.97)` nos botões |
| Base (segmentado, sublinhado) | 220–300ms | expo out `cubic-bezier(.16,1,.3,1)` | |
| Rota: entrada | 420ms | expo out | fade + subida de 14px |
| Rota: saída | 140ms | quart in | só fade (saída mais rápida que entrada) |
| Palavras do título | 760ms | expo out | máscara, stagger 55ms |
| Revelação no scroll (`v-reveal`) | 640ms | expo out | IntersectionObserver, uma vez por elemento, stagger 55ms limitado a 8 itens (≤ 440ms) |
| Contagem de números (`CountUp`) | 900ms | expo out | o valor final fica em `data-value` e em texto `sr-only` |
| Barras | 700ms | expo out | `scaleY` a partir da base, 22ms por dia |
| Linha | 1100ms | expo out | `stroke-dashoffset` com `pathLength=1`; área entra com fade |
| Medidor de nota / similaridade | 900ms | expo out | `scaleX`, 35ms por segmento |
| Toast | 420ms entra / 160ms sai | expo out | sobe ao entrar, desliza para o lado ao sair |
| Skeleton | 1,4s loop | ease-in-out | pulso de opacidade |

**`prefers-reduced-motion: reduce`**: animações e transições vão a 0,01ms, sem delay; `v-reveal` já marca o elemento como visível; `CountUp` mostra o valor final; o ping do status da API some. Coberto pelo teste e2e `reduced motion renders the final state at once`.
Animação só em `transform`/`opacity` (nada de `width`/`height`), para não disparar layout.

## 5. Leis de UX aplicadas

| Lei | Onde |
|---|---|
| **Fitts** | Alvos ≥ 44px (botões, segmentados, links externos, tags do item, tab bar de 64px); CTA principal sempre no canto do cabeçalho, perto do conteúdo que afeta. |
| **Hick** | Período e ordem em segmentado de 4 e 2 opções visíveis; filtros avançados recolhidos no mobile; uma ação primária por tela. |
| **Jakob** | Barra superior com logo à esquerda, tab bar inferior no celular, "Voltar" no topo do item, paginação anterior/próxima. |
| **Miller** | Navegação com 4 seções; faixas de 3–4 números; 12 itens por página (manchete + 11). |
| **Doherty** | Skeleton imediato, busca com debounce de 350ms, conteúdo anterior a 50% de opacidade enquanto recarrega, botões que mudam de estado na hora ("Coletando…"). |
| **Tesler** | A complexidade fica no backend: o feed já vem deduplicado e ranqueado; a frase de métricas resume cinco números. |
| **Postel** | Subreddit aceita `r/golang` ou `golang`; tag aceita Enter ou blur; erros da API aparecem perto do campo. |
| **Peak-End** | Pico: manchete com nota gigante. Fim: relatório da coleta com contagem animada e toast de confirmação. |
| **Von Restorff** | Amarelo só para a ação primária, a nota acima do corte e a seção ativa; laranja só para duplicados e falhas. |
| **Posição serial** | O mais importante no começo (manchete, nº 01) e a navegação nas pontas (topo/rodapé). |
| **Zeigarnik** | Paginação `01 / 04`, índice de edições `Nº`, contador de filtros ativos no botão "Filtros". |
| **Estética-usabilidade** | Grade editorial, tipografia forte e motion contido dão confiança nos dados. |
| **Proximidade** | Metadados do item colados ao título; ações de cada fonte agrupadas sob ela. |
| **Similaridade** | Todo número em JetBrains Mono; todo rótulo em eyebrow mono; mesmos chips em feed e item. |
| **Região comum** | Fios horizontais delimitam cada linha da lista; o painel de configuração é o único cartão da tela de fontes. |
| **Conectividade uniforme** | Fios verticais ligam os números das faixas; o fio amarelo liga a edição selecionada ao leitor. |
| **Prägnanz** | Formas simples: linhas, números e retângulos, sem ícones decorativos nem sombras. |
| **Pareto** | Feed, item e métricas recebem o maior cuidado; a configuração é um formulário direto. |
| **Parkinson / Occam** | Sem dependência nova de animação (Vue `<Transition>` + CSS + IntersectionObserver); nada de elemento sem backend; formulários curtos que salvam em uma ação. |

## 6. Acessibilidade

Contraste ≥ 4,5:1 em texto (cinza `#A3A3A3` é o mínimo usado); foco visível amarelo em tudo; link "Pular para o conteúdo"; títulos com `aria-label` (as palavras animadas ficam `aria-hidden`); números animados com valor final em `sr-only`; gráficos com rótulo, alvos focáveis por dia e tabela equivalente; diálogos nativos (`<dialog>`) com Esc e foco preso; sem rolagem horizontal nos três viewports (verificado no e2e).

---

# Referência 2: MOXEN Encuentro (fase R2)

Segunda referência, aplicada **por cima** do R1 sem trocar a linguagem editorial. Estudamos os padrões de widget do *Encuentro* (MOXEN): cartões flutuantes arredondados sobre fundo escuro, um scrubber arrastável com rótulo numérico vivo, painéis que se expandem com os filhos entrando em sequência e controles segmentados com pílula deslizante. Não copiamos marca, cor, fonte nem conteúdo da referência. A paleta continua JpDev (preto + amarelo, laranja só como apoio).

## 1. Padrões e onde entraram

| Padrão da referência | No Radar | Backend real |
|---|---|---|
| **Cartão flutuante** (raio ~16px, sombra difusa, fundo escuro) | `card-float`: scrubber, painel "Mais filtros", relatório da coleta, cada fonte, painel de configuração, cluster do item | — |
| **Scrubber arrastável com rótulo `tabular-nums` ao vivo** | `TimelineScrubber.vue` no Feed: histograma de 30 dias com duas alças (início/fim) que escolhem uma faixa de datas. O rótulo `05 out → 03 nov` e a contagem "N itens nestes dias" mudam enquanto arrasta | **Novo** `GET /api/feed/timeline?days=30` (contagem por dia do `feed_view`, no fuso das settings, com os mesmos filtros não-temporais do feed). A faixa vira `from`/`to` (RFC 3339) em `/api/feed`; o teste de integração prova que o feed devolve exatamente o total que o histograma contou |
| **Painel de disclosure** (altura 300–400ms ease-out, filhos com fade + `translateY(10px)→0` em sequência) | `DisclosurePanel.vue`: "Mais filtros" do feed (fonte, tag, nota mínima; abre sozinho quando um deles fica ativo) e "Detalhes da fonte" (cada campo de `config`, ID, criada/atualizada, última coleta com duração, run ID) | `/api/sources` (dados já existentes, antes ocultos) |
| **Controle segmentado com pílula deslizante** (150–200ms ease-out) | `SegmentedControl.vue`: período e ordem do feed, período (7/14/30 dias) das métricas. Com faixa personalizada ativa, nenhuma opção de período fica marcada e a pílula some | `/api/feed?period=`, `/api/metrics?days=` |
| **Gradientes suaves por categoria** | Cartão de fonte com `--grad-<kind>`: amarelo (Hacker News), laranja (Reddit), neutro (RSS), opacidade ≤ 11% | `kind` da fonte |
| **Botão primário preto sobre superfície clara** | Faixa amarela "Faixa ativa · 28 out → 03 nov" com botão preto **Limpar faixa** (`surface-primary` + `btn-ink`). Sobre o escuro, o primário continua amarelo | — |

## 2. Tokens novos (CSS variables em `src/style.css` (horizon-web), `:root`)

| Token | Valor | Uso |
|---|---|---|
| `--radius-card` | `16px` | cartões flutuantes, superfície amarela |
| `--radius-control` | `6px` | botões e campos |
| `--shadow-float` | `inset 0 1px 0 #fff/4%, 0 18px 40px -18px #000/85%` | elevação dos cartões |
| `--border-float` | `#fff` a 7% | borda dos cartões |
| `--grad-hackernews` / `--grad-reddit` / `--grad-rss` | `linear-gradient(135deg, cor a 11% → 2% → transparente)` | blocos por tipo de fonte |
| `--grad-scrub` | amarelo 95% → 55% | barras dentro da faixa |
| `--ease-out` | `cubic-bezier(.22, 1, .36, 1)` | easing dos widgets |
| `--dur-seg` | `180ms` | pílula do segmentado |
| `--dur-disclose` | `340ms` | altura do disclosure (fechar: 220ms, sem stagger) |
| `--dur-disclose-item` | `300ms` | fade + subida dos filhos |
| `--stagger-disclose` | `45ms` | intervalo entre filhos (até 6 passos; atraso inicial de 80ms) |

Utilitários: `card-float`, `surface-primary`, `btn-ink`, `.dz-group` (os filhos de uma grade entram um a um).

## 3. Interação e acessibilidade

- **Scrubber**: cada alça é `role="slider"` com `aria-valuemin/max/now`, `aria-label` ("Início/Fim da faixa de datas") e `aria-valuetext` ("Seg., 27 de out. de 2026, 5 itens"). Teclado: ←/↓ e →/↑ (±1 dia), PageDown/PageUp (±7), Home/End. A faixa é aplicada 400ms após a última tecla, ou ao soltar o ponteiro, e só se mudou desde o início do gesto. Arrastar uma alça move a sua borda pelo deslocamento do ponteiro, então um clique na alça sem arrastar não muda nada. **Alternativa ao arrasto (WCAG 2.5.7):** tocar no trilho leva a alça mais próxima ao dia tocado. A faixa sempre vai como `from` **e** `to` (no último dia, `to` é o fim do próprio histograma), para o feed devolver exatamente as linhas contadas. Se um link salvo começa antes da janela de 30 dias, o rótulo mostra a data real da URL e esconde a contagem, em vez de fingir que a faixa começa no 1º dia. Alças com 44px de área de toque; `touch-action: pan-y` deixa a rolagem vertical livre no celular.
- **Disclosure**: padrão WAI-ARIA (botão com `aria-expanded` + `aria-controls`, região com `aria-labelledby`). Fechado, o conteúdo fica `visibility: hidden`: sai da ordem de tab e da árvore de acessibilidade, não só da vista.
- **Segmentado**: grupo rotulado de botões `aria-pressed`; setas e Home/End movem foco e seleção.
- **Rolagem**: troca de filtro (só a query muda) mantém a posição da página. Antes, cada filtro voltava ao topo e, no celular, tirava o scrubber da tela. Só os botões Anterior/Próxima rolam até o início da lista.
- **Landmarks**: só o painel "Mais filtros" é `role=region`. Os detalhes de cada fonte não são, para não encher a lista de landmarks.
- **Exceção registrada ao R1**: o R1 só anima `transform`/`opacity`. O disclosure anima `grid-template-rows` (altura) e a pílula anima `width`, porque são exatamente os padrões pedidos. O custo é pequeno: o layout é recalculado só no painel ou no grupo de botões.

## 4. `prefers-reduced-motion`

A regra global do R1 (durações a 0,01ms, sem delay) cobre os widgets: a pílula salta, o disclosure abre na altura final com os filhos já visíveis, as barras e alças do scrubber não deslizam. O teste e2e `reduced motion: widgets settle at once` cobre isso.

## 5. Testes

`e2e/widgets.spec.ts` (horizon-web), nos três viewports:
- **pílula**: assentada sob a opção pressionada (x e largura com tolerância de 1px), seleção pelas setas, períodos das métricas;
- **scrubber pelo teclado**: semântica de slider, rótulo ao vivo, `from` aplicado na URL e na API, total do feed igual à contagem do histograma (e à soma das barras), teclas que voltam ao início não aplicam nada, botão preto sobre o amarelo e "Limpar faixa";
- **scrubber pelo ponteiro**: arrasto com rótulo mudando durante o gesto, clique simples na alça sem efeito, toque no trilho como alternativa, posição de rolagem preservada;
- **scrubber em link salvo** com início fora da janela, e **filtro trocado na página 2** sem a tela pular;
- **disclosure**: `aria-expanded`, `0fr → 1fr` com 340ms, atrasos crescentes entre os filhos, contador de filtros, detalhes da fonte com dados reais, gradiente e raio de 16px;
- **reduced motion**.

Go: `TestDayStarts` (limites de dia no fuso), `TestTimelineIgnoresTimeFilters` e casos de validação em `TestStatusMapping`, além do bloco de timeline em `TestAPIAgainstPostgresAndNATS`, que roda contra o Postgres real.
