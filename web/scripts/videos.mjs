// Turns the Playwright recordings (video: 'on') into one H.264 MP4 per project:
// videos/e2e-<project>.mp4, each clip captioned with the test title and result.
// Reads playwright-report/results.json (JSON reporter) for titles and paths.
//   node scripts/videos.mjs [results.json]
import { spawnSync } from 'node:child_process'
import { existsSync, mkdirSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { dirname, join, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

const web = resolve(dirname(fileURLToPath(import.meta.url)), '..')
const out = resolve(web, '..', 'videos')
const report = resolve(process.argv[2] ?? join(web, 'playwright-report', 'results.json'))
const font = process.env.CAPTION_FONT ?? 'C:/Windows/Fonts/arialbd.ttf'

export function ffmpeg(args, cwd = out) {
  const r = spawnSync('ffmpeg', ['-hide_banner', '-loglevel', 'error', '-y', ...args], { cwd, stdio: 'inherit' })
  if (r.status !== 0) throw new Error(`ffmpeg failed (${r.status}): ${args.join(' ')}`)
}

export function duration(file) {
  const r = spawnSync('ffprobe', ['-v', 'error', '-show_entries', 'format=duration', '-of', 'csv=p=0', file], {
    encoding: 'utf8',
  })
  return Number(r.stdout.trim())
}

export function videoWidth(file) {
  const r = spawnSync('ffprobe', ['-v', 'error', '-select_streams', 'v:0', '-show_entries', 'stream=width', '-of', 'csv=p=0', file], {
    encoding: 'utf8',
  })
  return Number(r.stdout.trim()) || 1440
}

// drawtext needs ':' escaped inside filter option values.
export const esc = (p) => p.replace(/\\/g, '/').replace(/:/g, '\\:')

/** wrap breaks text into lines that fit `width` px at `size` px (Arial Bold ~0.62em per char). */
export function wrap(text, width, size) {
  const max = Math.max(10, Math.floor((width - size * 1.4) / (size * 0.62)))
  const lines = []
  let line = ''
  for (const word of text.split(' ')) {
    if (line && (line + ' ' + word).length > max) {
      lines.push(line)
      line = word
    } else line = line ? `${line} ${word}` : word
  }
  if (line) lines.push(line)
  return lines
}

/**
 * captionFilter draws a bottom bar with one drawtext per line: drawtext renders
 * a line break inside a textfile as a missing-glyph box. Each line is read from
 * `${prefix}-<i>.txt` (written by the caller) to avoid escaping the title.
 */
export function captionFilter(prefix, lines, size, color = '0xFFD400') {
  const pad = Math.round(size * 0.6)
  const step = Math.round(size * 1.3)
  const h = lines * step + pad * 2
  return [
    `drawbox=x=0:y=ih-${h}:w=iw:h=${h}:color=0x0B0B0B@0.82:t=fill`,
    ...Array.from({ length: lines }, (_, i) =>
      `drawtext=fontfile='${esc(font)}':textfile='${prefix}-${i}.txt':fontcolor=${color}:fontsize=${size}` +
        `:x=${pad}:y=h-${h - pad - i * step}`,
    ),
  ].join(',')
}

function collect(suite, path, acc) {
  const titles = suite.title && !suite.title.endsWith('.ts') ? [...path, suite.title] : path
  for (const spec of suite.specs ?? []) {
    for (const t of spec.tests) {
      for (const r of t.results) {
        const video = r.attachments?.find((a) => a.name === 'video' && a.path)
        if (video) acc.push({ project: t.projectName, title: [...titles, spec.title].join(' › '), file: spec.file, status: r.status, path: video.path })
      }
    }
  }
  for (const s of suite.suites ?? []) collect(s, titles, acc)
  return acc
}

function main() {
  if (!existsSync(report)) throw new Error(`missing ${report}: run npx playwright test first`)
  const data = JSON.parse(readFileSync(report, 'utf8'))
  const clips = data.suites.flatMap((s) => collect(s, [], []))
  const tmp = join(out, '.tmp')
  rmSync(tmp, { recursive: true, force: true })
  mkdirSync(tmp, { recursive: true })

  const byProject = Map.groupBy(clips, (c) => c.project)
  for (const [project, list] of byProject) {
    // The JSON report does not carry the viewport: read it from the recording.
    const width = videoWidth(list[0].path)
    const size = Math.max(15, Math.round(width / 52))
    const parts = []
    list.forEach((c, i) => {
      const n = String(i + 1).padStart(2, '0')
      const ok = c.status === 'passed'
      const text = `${n}/${list.length} ${ok ? 'PASSOU' : c.status.toUpperCase()} · ${c.file} · ${c.title} [${project}]`
      const lines = wrap(text, width, size)
      lines.forEach((l, j) => writeFileSync(join(tmp, `${project}-${n}-${j}.txt`), l))
      const part = `.tmp/${project}-${n}.mp4`
      ffmpeg([
        '-i', c.path,
        '-vf', `fps=25,scale=trunc(iw/2)*2:trunc(ih/2)*2,${captionFilter(`.tmp/${project}-${n}`, lines.length, size, ok ? '0xFFD400' : '0xFF7A00')}`,
        '-c:v', 'libx264', '-preset', 'veryfast', '-crf', '24', '-pix_fmt', 'yuv420p', '-an', part,
      ])
      parts.push(part)
    })
    writeFileSync(join(tmp, `${project}.list`), parts.map((p) => `file '${p.replace('.tmp/', '')}'`).join('\n'))
    const target = `e2e-${project}.mp4`
    ffmpeg(['-f', 'concat', '-safe', '0', '-i', `.tmp/${project}.list`, '-c', 'copy', '-movflags', '+faststart', target])
    console.log(`${target}: ${list.length} clips, ${duration(join(out, target)).toFixed(1)}s`)
  }
  rmSync(tmp, { recursive: true, force: true })
}

if (process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url)) main()
