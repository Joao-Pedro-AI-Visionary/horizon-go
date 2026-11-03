import type {
  CollectReport,
  Digest,
  FeedPage,
  FeedParams,
  ItemDetail,
  Metrics,
  Settings,
  Source,
  SourceInput,
} from './types'

/** ApiError carries the HTTP status and the API's {"error": "..."} message. */
export class ApiError extends Error {
  readonly status: number
  readonly body: unknown
  constructor(status: number, message: string, body?: unknown) {
    super(message)
    this.name = 'ApiError'
    this.status = status
    this.body = body
  }
}

const BASE = import.meta.env.VITE_API_BASE ?? ''

async function request<T>(method: string, path: string, body?: unknown, signal?: AbortSignal): Promise<T> {
  let res: Response
  try {
    res = await fetch(BASE + path, {
      method,
      signal,
      headers: body === undefined ? { Accept: 'application/json' } : { Accept: 'application/json', 'Content-Type': 'application/json' },
      body: body === undefined ? undefined : JSON.stringify(body),
    })
  } catch (err) {
    if ((err as Error).name === 'AbortError') throw err
    throw new ApiError(0, 'API indisponível — verifique se o cmd/api está rodando.')
  }
  if (res.status === 204) return undefined as T
  const text = await res.text()
  let data: unknown = undefined
  if (text) {
    try {
      data = JSON.parse(text)
    } catch {
      data = text
    }
  }
  if (!res.ok) {
    const apiMsg = data && typeof data === 'object' && 'error' in data ? String((data as { error: unknown }).error) : ''
    throw new ApiError(res.status, apiMsg || `HTTP ${res.status}`, data)
  }
  return data as T
}

function query(params: object): string {
  const sp = new URLSearchParams()
  for (const [k, v] of Object.entries(params)) {
    if (v === undefined || v === null || v === '') continue
    sp.set(k, String(v))
  }
  const s = sp.toString()
  return s ? `?${s}` : ''
}

export const api = {
  feed: (p: FeedParams, signal?: AbortSignal) => request<FeedPage>('GET', `/api/feed${query(p)}`, undefined, signal),
  item: (id: number, signal?: AbortSignal) => request<ItemDetail>('GET', `/api/items/${id}`, undefined, signal),
  digests: () => request<{ digests: Digest[] | null }>('GET', '/api/digests'),
  digest: (date: string) => request<Digest>('GET', `/api/digests/${encodeURIComponent(date)}`),
  digestHtmlUrl: (date: string) => `${BASE}/api/digests/${encodeURIComponent(date)}?format=html`,
  digestMarkdownUrl: (date: string) => `${BASE}/api/digests/${encodeURIComponent(date)}?format=md`,
  generateDigest: (date?: string) => request<Digest>('POST', '/api/digests/generate', date ? { date } : undefined),
  sources: () => request<{ sources: Source[] | null }>('GET', '/api/sources'),
  createSource: (in_: SourceInput) => request<Source>('POST', '/api/sources', in_),
  updateSource: (id: number, in_: SourceInput) => request<Source>('PATCH', `/api/sources/${id}`, in_),
  deleteSource: (id: number) => request<void>('DELETE', `/api/sources/${id}`),
  fetchSource: (id: number) => request<CollectReport>('POST', `/api/sources/${id}/fetch`),
  runPipeline: () => request<CollectReport>('POST', '/api/pipeline/run'),
  settings: () => request<Settings>('GET', '/api/settings'),
  saveSettings: (s: Settings) => request<Settings>('PUT', '/api/settings', s),
  metrics: (days: number) => request<Metrics>('GET', `/api/metrics${query({ days })}`),
  health: () => request<Record<string, string>>('GET', '/healthz'),
}

/** errorMessage turns any thrown value into a user-facing message. */
export function errorMessage(err: unknown): string {
  if (err instanceof ApiError) {
    if (err.status === 409) return 'Já existe uma coleta em andamento. Aguarde terminar.'
    if (err.status === 404) return 'Não encontrado.'
    return err.message
  }
  if (err instanceof Error) return err.message
  return String(err)
}
