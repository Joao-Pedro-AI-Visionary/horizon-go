// JSON contracts of the Go API (backend/internal/domain and
// backend/internal/adapters/httpapi). Keep in sync with the json tags there.

export type SourceKind = 'hackernews' | 'reddit' | 'rss'

export interface FeedItem {
  item_id: number
  source_id: number
  source_kind: SourceKind
  source_name: string
  title: string
  url: string
  summary: string
  score_reason: string
  score: number
  tags: string[] | null
  published_at: string | null
  cluster_id: number | null
  duplicate_count: number
  projected_at: string
}

export interface FeedPage {
  items: FeedItem[] | null
  total: number
  page: number
  page_size: number
}

export interface FeedParams {
  source_id?: number
  tag?: string
  q?: string
  period?: '24h' | '7d' | '30d' | 'all'
  min_score?: number
  sort?: 'score' | 'recent'
  page?: number
  page_size?: number
}

export interface ClusterInfo {
  id: number
  title: string
  item_count: number
  representative_item_id: number | null
}

export interface RelatedItem {
  id: number
  title: string
  url: string
  source_kind: SourceKind
  source_name: string
  score: number | null
  status: string
  published_at: string | null
  similarity: number | null
}

export interface ItemDetail {
  id: number
  source_id: number
  source_kind: SourceKind
  source_name: string
  title: string
  url: string
  author: string
  content: string
  status: string
  score: number | null
  score_reason: string
  summary: string
  tags: string[] | null
  published_at: string | null
  fetched_at: string
  scored_at: string | null
  last_error: string
  is_primary: boolean
  cluster: ClusterInfo | null
  duplicates: RelatedItem[] | null
}

export interface Digest {
  id: number
  date: string
  language: string
  title: string
  markdown?: string
  html?: string
  item_ids: number[] | null
  item_count: number
  considered: number
  window_start: string
  window_end: string
  created_at: string
  updated_at: string
}

export interface FetchRun {
  source_id: number
  source_name: string
  run_id: string
  status: 'ok' | 'error' | string
  item_count: number
  error: string
  started_at: string
  finished_at: string
}

export interface Source {
  id: number
  kind: SourceKind
  name: string
  config: Record<string, unknown>
  enabled: boolean
  created_at: string
  updated_at: string
  item_count?: number
  last_fetch: FetchRun | null
}

export interface SourceInput {
  kind?: SourceKind
  name?: string
  config?: Record<string, unknown>
  enabled?: boolean
}

export interface CollectReport {
  run_id: string
  sources: FetchRun[]
  fetched: number
  invalid: number
  duplicates: number
  published: number
  error?: string
}

export interface Settings {
  score_threshold: number
  digest_max_items: number
  digest_per_source: number
  digest_cron: string
  timezone: string
  updated_at?: string
}

export interface DayCount {
  day: string
  fetched: number
  scored: number
}

export interface SourceMetrics {
  source_id: number
  name: string
  kind: SourceKind
  enabled: boolean
  items: number
  feed_items: number
  avg_score: number | null
  fetch_ok: number
  fetch_errors: number
  last_fetch_at: string | null
  last_status: string
  last_error: string
}

export interface TokenDay {
  day: string
  kind: 'chat' | 'embed' | string
  model: string
  calls: number
  prompt_tokens: number
  completion_tokens: number
}

export interface Metrics {
  days: number
  timezone: string
  items_per_day: DayCount[] | null
  by_source: SourceMetrics[] | null
  status: Record<string, number> | null
  dedup: {
    clustered: number
    duplicates: number
    rate: number
    clusters: number
    multi_item_clusters: number
  }
  failures: {
    fetch_errors: number
    score_failed: number
    score_retries: number
    recent: { source_id: number; source_name: string; error: string; at: string }[] | null
  }
  tokens: {
    calls: number
    prompt_tokens: number
    completion_tokens: number
    total_tokens: number
    per_day: TokenDay[] | null
  }
  generated_at: string
}
