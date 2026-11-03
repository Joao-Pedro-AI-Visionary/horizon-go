// Builds videos/demo-final.mp4 (H.264, 1440x900) from the tour recordings in
// videos/.raw: a title card with the JpDev logo, the desktop tour, then the
// mobile tour centered on the dark background.
//   node scripts/demo-video.mjs
import { existsSync, rmSync } from 'node:fs'
import { join, resolve } from 'node:path'
import { duration, esc, ffmpeg } from './videos.mjs'

const out = resolve(import.meta.dirname, '..', '..', 'videos')
const raw = join(out, '.raw')
const logo = resolve(import.meta.dirname, '..', 'public', 'jpdev-logo.png')
const font = process.env.CAPTION_FONT ?? 'C:/Windows/Fonts/arialbd.ttf'

for (const f of ['demo-desktop.webm', 'demo-mobile.webm']) {
  if (!existsSync(join(raw, f))) throw new Error(`missing ${join(raw, f)}: run the demo tour first`)
}

const W = 1440
const H = 900
const text = (t, size, y, color = 'white') =>
  `drawtext=fontfile='${esc(font)}':text='${t}':fontcolor=${color}:fontsize=${size}:x=(w-text_w)/2:y=${y}`

ffmpeg([
  '-f', 'lavfi', '-t', '3.5', '-i', `color=c=0x0B0B0B:s=${W}x${H}:r=25`,
  '-i', logo,
  '-i', join(raw, 'demo-desktop.webm'),
  '-i', join(raw, 'demo-mobile.webm'),
  '-filter_complex',
  [
    `[1:v]scale=-1:180[logo]`,
    `[0:v][logo]overlay=(W-w)/2:200,${text('Radar by JpDev', 64, 430, '0xFFD400')},` +
      `${text('Horizon-Go · Go + Vue 3 · Postgres/pgvector · NATS JetStream · Ollama', 28, 520)},` +
      `${text('Tour real contra a API - desktop 1440x900 e mobile 390x844', 24, 570, '0xBBBBBB')},format=yuv420p,setsar=1[intro]`,
    `[2:v]fps=25,scale=${W}:${H},format=yuv420p,setsar=1[desk]`,
    `[3:v]fps=25,scale=-2:${H},pad=${W}:${H}:(ow-iw)/2:0:color=0x0B0B0B,format=yuv420p,setsar=1[mob]`,
    `[intro][desk][mob]concat=n=3:v=1:a=0[v]`,
  ].join(';'),
  '-map', '[v]', '-c:v', 'libx264', '-preset', 'veryfast', '-crf', '22', '-movflags', '+faststart', 'demo-final.mp4',
])
rmSync(raw, { recursive: true, force: true })
console.log(`demo-final.mp4: ${duration(join(out, 'demo-final.mp4')).toFixed(1)}s`)
