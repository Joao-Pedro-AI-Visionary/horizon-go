// Builds the narrated videos from videos/narrated/work (tour.webm + marks.json
// from narrated/tour.spec.ts, <id>.mp3/.srt + scenes.json from narrated-tts.mjs):
//   horizon-narrado-16x9.mp4 (1920x1080), -9x16.mp4 (1080x1920), -1x1.mp4 (1080x1080)
// H.264 + AAC, pt-BR subtitles burned in (ASS, JpDev yellow on black box),
// each narration clip placed at the real start of its scene.
//   node scripts/narrated-video.mjs
import { existsSync, readFileSync, writeFileSync } from 'node:fs'
import { join, resolve } from 'node:path'
import { duration, ffmpeg } from './videos.mjs'

const dir = resolve(import.meta.dirname, '..', '..', 'videos', 'narrated')
const work = join(dir, 'work')
const logo = resolve(import.meta.dirname, '..', 'public', 'jpdev-logo.png')
for (const f of ['tour.webm', 'marks.json', 'scenes.json']) {
  if (!existsSync(join(work, f))) throw new Error(`missing ${join(work, f)}: run the TTS and the tour first`)
}

const scenes = JSON.parse(readFileSync(join(work, 'scenes.json'), 'utf8'))
const { marks } = JSON.parse(readFileSync(join(work, 'marks.json'), 'utf8'))
const lead = Math.max(0, marks[0].start - 0.25) // drop the blank page before the first scene
const total = marks.at(-1).end - lead + 0.6
const offset = (id) => {
  const m = marks.find((x) => x.id === id)
  if (!m) throw new Error(`scene ${id} has no mark`)
  return m.start - lead
}

// --- subtitles -------------------------------------------------------------
const toSec = (t) => {
  const [h, m, s] = t.replace(',', '.').split(':')
  return Number(h) * 3600 + Number(m) * 60 + Number(s)
}
function readSrt(file) {
  return readFileSync(file, 'utf8')
    .replace(/\r/g, '')
    .split(/\n\n+/)
    .map((b) => b.split('\n'))
    .filter((l) => l.length >= 3)
    .map(([, time, ...text]) => {
      const [a, b] = time.split(' --> ')
      return { start: toSec(a), end: toSec(b), text: text.join(' ').trim() }
    })
}

/** cues returns every narration sentence on the final timeline. */
function cues() {
  const out = []
  for (const s of scenes) {
    const base = offset(s.id)
    for (const c of readSrt(join(work, `${s.id}.srt`))) out.push({ start: base + c.start, end: base + c.end, text: c.text })
  }
  return out
}

function wrap(text, max) {
  const lines = []
  let line = ''
  for (const w of text.split(' ')) {
    if (line && `${line} ${w}`.length > max) {
      lines.push(line)
      line = w
    } else line = line ? `${line} ${w}` : w
  }
  if (line) lines.push(line)
  return lines
}

/** balance splits two lines near the middle word boundary. */
function balance(a, b) {
  const words = `${a} ${b}`.split(' ')
  let best = [a, b]
  let diff = Infinity
  for (let i = 1; i < words.length; i++) {
    const l = words.slice(0, i).join(' ')
    const r = words.slice(i).join(' ')
    const d = Math.abs(l.length - r.length)
    if (d < diff) [diff, best] = [d, [l, r]]
  }
  return best
}

/** chunks breaks a long sentence into <=2-line chunks, timed by character share. */
function chunks(cue, max) {
  const lines = wrap(cue.text, max)
  const groups = []
  for (let i = 0; i < lines.length; i += 2) groups.push(lines[i + 1] ? balance(lines[i], lines[i + 1]) : [lines[i]])
  const len = groups.reduce((a, g) => a + g.join(' ').length, 0)
  let t = cue.start
  return groups.map((g) => {
    const d = ((cue.end - cue.start) * g.join(' ').length) / len
    const c = { start: t, end: t + d, lines: g }
    t += d
    return c
  })
}

const assTime = (s) => {
  const cs = Math.round(Math.max(0, s) * 100)
  const h = Math.floor(cs / 360000)
  const m = Math.floor((cs % 360000) / 6000)
  const sec = Math.floor((cs % 6000) / 100)
  return `${h}:${String(m).padStart(2, '0')}:${String(sec).padStart(2, '0')}.${String(cs % 100).padStart(2, '0')}`
}

function writeAss(name, { w, h, size, max, marginV }) {
  // &HAABBGGRR: text #FFD400, box #0B0B0B at ~94% opacity (BorderStyle 3 = opaque box;
  // the box padding is large enough for the two lines to merge into one block).
  const style = `Style: Narr,Segoe UI,${size},&H0000D4FF,&H0000D4FF,&H100B0B0B,&H100B0B0B,-1,0,0,0,100,100,0,0,3,${Math.round(size * 0.5)},0,2,60,60,${marginV},1`
  const events = cues()
    .flatMap((c) => chunks(c, max))
    .map((c) => `Dialogue: 0,${assTime(c.start)},${assTime(c.end)},Narr,,0,0,0,,${c.lines.join('\\N')}`)
  const ass = [
    '[Script Info]',
    'ScriptType: v4.00+',
    `PlayResX: ${w}`,
    `PlayResY: ${h}`,
    'WrapStyle: 2',
    'ScaledBorderAndShadow: yes',
    '',
    '[V4+ Styles]',
    'Format: Name, Fontname, Fontsize, PrimaryColour, SecondaryColour, OutlineColour, BackColour, Bold, Italic, Underline, StrikeOut, ScaleX, ScaleY, Spacing, Angle, BorderStyle, Outline, Shadow, Alignment, MarginL, MarginR, MarginV, Encoding',
    style,
    '',
    '[Events]',
    'Format: Layer, Start, End, Style, Name, MarginL, MarginR, MarginV, Effect, Text',
    ...events,
    '',
  ].join('\n')
  writeFileSync(join(work, name), ass)
  return `work/${name}`
}

// SRT of the final timeline, for players that load external subtitles.
const srtTime = (s) => assTime(s).replace(/^(\d):/, '0$1:').replace('.', ',') + '0'
writeFileSync(
  join(dir, 'horizon-narrado.pt-BR.srt'),
  cues()
    .map((c, i) => `${i + 1}\n${srtTime(c.start)} --> ${srtTime(c.end)}\n${c.text}\n`)
    .join('\n'),
)

// --- master: trimmed tour + narration (48 kHz stereo AAC) -------------------
const audioIn = scenes.flatMap((s) => ['-i', join(work, `${s.id}.mp3`)])
const delays = scenes.map((s, i) => `[${i + 1}:a]adelay=${Math.round(offset(s.id) * 1000)}:all=1[a${i}]`)
const mix = `${scenes.map((_, i) => `[a${i}]`).join('')}amix=inputs=${scenes.length}:duration=longest:normalize=0,apad,atrim=0:${total.toFixed(3)}[a]`
ffmpeg(
  [
    '-i', join(work, 'tour.webm'), ...audioIn,
    '-filter_complex',
    [`[0:v]trim=start=${lead.toFixed(3)},setpts=PTS-STARTPTS,fps=30,scale=1920:1080,format=yuv420p,setsar=1,tpad=stop_mode=clone:stop_duration=2[v]`, ...delays, mix].join(';'),
    '-map', '[v]', '-map', '[a]', '-t', total.toFixed(3),
    '-c:v', 'libx264', '-preset', 'medium', '-crf', '16', '-c:a', 'aac', '-b:a', '192k', '-ar', '48000', '-ac', '2', 'work/master.mp4',
  ],
  dir,
)

// --- outputs ---------------------------------------------------------------
const enc = ['-c:v', 'libx264', '-preset', 'medium', '-crf', '20', '-pix_fmt', 'yuv420p', '-c:a', 'copy', '-movflags', '+faststart']

const s169 = writeAss('subs-16x9.ass', { w: 1920, h: 1080, size: 44, max: 52, marginV: 56 })
ffmpeg(['-i', 'work/master.mp4', '-vf', `ass=${s169}`, ...enc, 'horizon-narrado-16x9.mp4'], dir)

/**
 * framed crops the centered app column (1320x1080 of the 1920x1080 master),
 * scales it to the output width and lays it over a blurred, darkened copy of
 * itself, optionally with the logo on top.
 */
function framed(out, w, h, contentY, logoH, logoY, ass) {
  const graph = [
    `[0:v]split[a][b]`,
    `[a]scale=-2:${h},crop=${w}:${h},boxblur=28:2,eq=brightness=-0.2[bg]`,
    `[b]crop=1320:1080:300:0,scale=${w}:-2[fg]`,
    `[bg][fg]overlay=0:${contentY}${logoH ? '[v1]' : `,ass=${ass}[v]`}`,
  ]
  if (logoH) graph.push(`[1:v]scale=-1:${logoH}[logo]`, `[v1][logo]overlay=(W-w)/2:${logoY},ass=${ass}[v]`)
  ffmpeg(['-i', 'work/master.mp4', '-i', logo, '-filter_complex', graph.join(';'), '-map', '[v]', '-map', '0:a', ...enc, out], dir)
}
// The cropped column at 1080 wide is 1080x884.
framed('horizon-narrado-9x16.mp4', 1080, 1920, 500, 150, 250, writeAss('subs-9x16.ass', { w: 1080, h: 1920, size: 52, max: 30, marginV: 230 }))
framed('horizon-narrado-1x1.mp4', 1080, 1080, 0, 0, 0, writeAss('subs-1x1.ass', { w: 1080, h: 1080, size: 38, max: 44, marginV: 28 }))

for (const f of ['horizon-narrado-16x9.mp4', 'horizon-narrado-9x16.mp4', 'horizon-narrado-1x1.mp4']) {
  console.log(`${f}: ${duration(join(dir, f)).toFixed(2)}s`)
}
