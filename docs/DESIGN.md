# Design — Radar by JpDev (fase R1)

Redesign editorial do front Vue 3 (`web/`). Referência estudada: Tubik, *The Alabama Solution* — páginas editoriais para a investigação da HBO (Webby 2026). As imagens do case foram baixadas para `.runs/ref/` (fora do git) só para estudo; nada da referência foi copiado: marca, cores, fontes e conteúdo são do Radar/JpDev.

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

**Cores** (`web/src/style.css`, `@theme`)

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
