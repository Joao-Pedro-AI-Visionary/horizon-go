// Builds docs/screenshots/before-after-{desktop,mobile}.png: the screenshots
// kept in docs/screenshots/before/ next to the current ones, for feed, item
// and metrics, labelled BEFORE / AFTER.
//   node scripts/before-after.mjs
import { existsSync } from 'node:fs'
import { join, resolve } from 'node:path'
import { esc, ffmpeg } from './videos.mjs'

const shots = resolve(import.meta.dirname, '..', '..', 'docs', 'screenshots')
const font = process.env.CAPTION_FONT ?? 'C:/Windows/Fonts/arialbd.ttf'
const screens = [
  ['1-feed', 'FEED'],
  ['2-item', 'ITEM'],
  ['5-metrics', 'METRICS'],
]

// Each cell: top of the full-page screenshot, scaled to `w`, cropped/padded to
// `h`, with a label bar on top.
function cell(input, w, h, label, color) {
  const bar = Math.round(w / 14)
  return (
    `[${input}:v]scale=${w}:-2,crop=${w}:'min(ih,${h})':0:0,pad=${w}:${h}:0:0:color=0x0B0B0B,` +
    `pad=${w}:${h + bar}:0:${bar}:color=${color},` +
    `drawtext=fontfile='${esc(font)}':text='${label}':fontcolor=0x0B0B0B:fontsize=${Math.round(bar * 0.55)}:x=(w-text_w)/2:y=${Math.round(bar * 0.24)}`
  )
}

function build(project, w, h, layout) {
  const inputs = []
  const cells = []
  screens.forEach(([name, title], i) => {
    const before = join(shots, 'before', `${name}-${project}.png`)
    const after = join(shots, `${name}-${project}.png`)
    for (const f of [before, after]) if (!existsSync(f)) throw new Error(`missing ${f}`)
    inputs.push('-i', before, '-i', after)
    cells.push(`${cell(i * 2, w, h, `${title} - BEFORE`, '0x8A8A8A')}[b${i}]`)
    cells.push(`${cell(i * 2 + 1, w, h, `${title} - AFTER`, '0xFFD400')}[a${i}]`)
  })
  const sep = 8
  const rows = screens.map((_, i) => `[b${i}]pad=iw+${sep}:ih:0:0:color=0x0B0B0B[bp${i}];[bp${i}][a${i}]hstack=inputs=2[r${i}]`)
  const join_ =
    layout === 'rows'
      ? screens.map((_, i) => `[r${i}]pad=iw:ih+${sep * 3}:0:0:color=0x0B0B0B[rp${i}]`).join(';') +
        ';' + screens.map((_, i) => `[rp${i}]`).join('') + `vstack=inputs=${screens.length}[out]`
      : screens.map((_, i) => `[r${i}]pad=iw+${sep * 3}:ih:0:0:color=0x0B0B0B[rp${i}]`).join(';') +
        ';' + screens.map((_, i) => `[rp${i}]`).join('') + `hstack=inputs=${screens.length}[out]`
  ffmpeg(
    [...inputs, '-filter_complex', [...cells, ...rows, join_].join(';'), '-map', '[out]', '-frames:v', '1', `before-after-${project}.png`],
    shots,
  )
  console.log(`before-after-${project}.png`)
}

build('desktop', 960, 640, 'rows')
build('mobile', 390, 1500, 'cols')
