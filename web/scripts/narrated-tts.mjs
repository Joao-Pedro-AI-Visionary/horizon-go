// Narration audio for the narrated video: parses videos/narrated/script.md
// (one "## <id>" section per scene; "> tela:" lines are not spoken), runs
// edge-tts once per scene and writes videos/narrated/work/{<id>.mp3,<id>.srt}
// plus scenes.json with the measured (ffprobe) duration of each clip.
//   node scripts/narrated-tts.mjs        (needs: pip install edge-tts)
import { spawnSync } from 'node:child_process'
import { mkdirSync, readFileSync, writeFileSync } from 'node:fs'
import { join, resolve } from 'node:path'
import { duration } from './videos.mjs'

const dir = resolve(import.meta.dirname, '..', '..', 'videos', 'narrated')
const work = join(dir, 'work')
const voice = process.env.TTS_VOICE ?? 'pt-BR-AntonioNeural'
const rate = process.env.TTS_RATE ?? '+8%'

export function parseScript(md) {
  const scenes = []
  for (const block of md.split(/^## /m).slice(1)) {
    const [head, ...rest] = block.split('\n')
    const text = rest
      .filter((l) => l.trim() && !l.startsWith('>'))
      .join(' ')
      .replace(/\s+/g, ' ')
      .trim()
    if (text) scenes.push({ id: head.trim(), text })
  }
  return scenes
}

const scenes = parseScript(readFileSync(join(dir, 'script.md'), 'utf8'))
if (!scenes.length) throw new Error('no scenes in script.md')
mkdirSync(work, { recursive: true })

for (const s of scenes) {
  const mp3 = join(work, `${s.id}.mp3`)
  const r = spawnSync(
    'python',
    ['-m', 'edge_tts', '--voice', voice, `--rate=${rate}`, '--text', s.text, '--write-media', mp3, '--write-subtitles', join(work, `${s.id}.srt`)],
    { stdio: 'inherit' },
  )
  if (r.status !== 0) throw new Error(`edge-tts failed for ${s.id}`)
  s.seconds = Number(duration(mp3).toFixed(3))
  console.log(`${s.id}: ${s.seconds}s`)
}
writeFileSync(join(work, 'scenes.json'), JSON.stringify(scenes, null, 2))
console.log(`total narration: ${scenes.reduce((a, s) => a + s.seconds, 0).toFixed(1)}s`)
