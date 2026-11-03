# Horizon — vídeo narrado (pt-BR)

Roteiro do vídeo `videos/narrated/horizon-narrado-16x9.mp4`. Voz: `pt-BR-AntonioNeural` (edge-tts).
Cada seção `## <id>` é uma cena: o texto abaixo do título é a narração, lida exatamente como está
por `web/scripts/narrated-tts.mjs`. A linha `> tela:` descreve o que aparece na tela e não é narrada.
O tour gravado (`web/narrated/tour.spec.ts`) segura cada cena pelo tempo do áudio dela.

## abertura
> tela: feed no topo, com a manchete principal.

Este é o Horizon, um radar de notícias de tecnologia. Ele coleta sozinho o que sai no Hacker News, no Reddit e em feeds RSS, e junta tudo num lugar só.

## ia-local
> tela: detalhe de uma notícia: resumo, nota, motivo e grupo de duplicados.

Uma inteligência artificial que roda no seu próprio computador lê cada notícia, dá uma nota de zero a dez e escreve um resumo curto. Notícias repetidas viram um grupo só.

## feed
> tela: volta ao feed e rola pelas notícias.

O feed novo tem cara de jornal: manchete em destaque, nota bem visível e a fonte de cada notícia logo abaixo.

## scrubber
> tela: arrasta as alças da linha do tempo de datas.

Para escolher o período, é só arrastar as alças da linha do tempo. As datas e a contagem de notícias mudam na hora.

## pilulas
> tela: troca período e ordem nas pílulas e abre o painel "Mais filtros".

Período e ordem ficam em pílulas que deslizam até a opção escolhida. E os filtros extras abrem num painel, sem tirar você do lugar.

## digest
> tela: aba Digests, gera o digest do dia.

Todo dia, o Horizon monta um digest com o melhor do período, pronto para ler aqui ou baixar.

## fontes
> tela: aba Fontes, abre os detalhes de uma fonte.

Na tela de fontes, você liga, desliga e confere quando cada origem foi coletada pela última vez.

## metricas
> tela: aba Métricas, números animados e gráficos.

Nas métricas, os números sobem animados: quanto foi coletado, quantos duplicados foram removidos, as falhas e o uso da IA.

## responsivo
> tela: a mesma aplicação em tablet e celular, lado a lado.

E tudo se adapta à tela: no computador, no tablet ou no celular.

## encerramento
> tela: logo JpDev e créditos.

Horizon: código aberto, feito em Go e Vue, baseado no projeto original de Thysrael. Obrigado por assistir!
