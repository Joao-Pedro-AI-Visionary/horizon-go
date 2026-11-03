import type { SourceKind } from '../api/types'

const nf = new Intl.NumberFormat('pt-BR')
const dtf = new Intl.DateTimeFormat('pt-BR', { dateStyle: 'short', timeStyle: 'short' })
const df = new Intl.DateTimeFormat('pt-BR', { weekday: 'short', day: '2-digit', month: 'short', year: 'numeric', timeZone: 'UTC' })
const rtf = new Intl.RelativeTimeFormat('pt-BR', { numeric: 'auto' })

export const fmtNumber = (n: number) => nf.format(n)

export function fmtCompact(n: number): string {
  return new Intl.NumberFormat('pt-BR', { notation: 'compact', maximumFractionDigits: 1 }).format(n)
}

export function fmtScore(n: number | null | undefined): string {
  if (n === null || n === undefined || Number.isNaN(n)) return '—'
  return n.toFixed(1).replace('.', ',')
}

export function fmtDateTime(iso: string | null | undefined): string {
  if (!iso) return '—'
  return dtf.format(new Date(iso))
}

/** fmtDay formats a YYYY-MM-DD calendar day without shifting its timezone. */
export function fmtDay(day: string): string {
  const s = df.format(new Date(`${day}T00:00:00Z`))
  return s.charAt(0).toUpperCase() + s.slice(1)
}

export function shortDay(day: string): string {
  const [, m, d] = day.split('-')
  return `${d}/${m}`
}

export function fmtRelative(iso: string | null | undefined): string {
  if (!iso) return '—'
  const diff = (new Date(iso).getTime() - Date.now()) / 1000
  const abs = Math.abs(diff)
  if (abs < 60) return rtf.format(Math.round(diff), 'second')
  if (abs < 3600) return rtf.format(Math.round(diff / 60), 'minute')
  if (abs < 86400) return rtf.format(Math.round(diff / 3600), 'hour')
  return rtf.format(Math.round(diff / 86400), 'day')
}

export const kindLabel: Record<SourceKind, string> = {
  hackernews: 'Hacker News',
  reddit: 'Reddit',
  rss: 'RSS',
}

export function hostOf(url: string): string {
  try {
    return new URL(url).hostname.replace(/^www\./, '')
  } catch {
    return url
  }
}
