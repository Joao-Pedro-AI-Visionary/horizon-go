package domain

import "time"

// Metrics is the operational overview of the last Days days (days are
// calendar days in the settings timezone).
type Metrics struct {
	Days        int             `json:"days"`
	Timezone    string          `json:"timezone"`
	ItemsPerDay []DayCount      `json:"items_per_day"`
	BySource    []SourceMetrics `json:"by_source"`
	Status      map[string]int  `json:"status"`
	Dedup       DedupMetrics    `json:"dedup"`
	Failures    FailureMetrics  `json:"failures"`
	Tokens      TokenMetrics    `json:"tokens"`
	GeneratedAt time.Time       `json:"generated_at"`
}

// DayCount is the activity of one day.
type DayCount struct {
	Day     string `json:"day"`
	Fetched int    `json:"fetched"` // items first fetched that day
	Scored  int    `json:"scored"`  // items scored that day
}

// SourceMetrics is the activity of one source in the period.
type SourceMetrics struct {
	SourceID    int64      `json:"source_id"`
	Name        string     `json:"name"`
	Kind        string     `json:"kind"`
	Enabled     bool       `json:"enabled"`
	Items       int        `json:"items"`
	FeedItems   int        `json:"feed_items"`
	AvgScore    *float64   `json:"avg_score"`
	FetchOK     int        `json:"fetch_ok"`
	FetchErrors int        `json:"fetch_errors"`
	LastFetchAt *time.Time `json:"last_fetch_at"`
	LastStatus  string     `json:"last_status"`
	LastError   string     `json:"last_error"`
}

// DedupMetrics measures the clustering (items fetched in the period).
type DedupMetrics struct {
	Clustered         int     `json:"clustered"`  // items with a cluster
	Duplicates        int     `json:"duplicates"` // clustered items that are not the primary
	Rate              float64 `json:"rate"`       // duplicates / clustered
	Clusters          int     `json:"clusters"`
	MultiItemClusters int     `json:"multi_item_clusters"`
}

// FailureMetrics counts what went wrong in the period.
type FailureMetrics struct {
	FetchErrors  int            `json:"fetch_errors"`
	ScoreFailed  int            `json:"score_failed"`  // gave up after the attempt budget
	ScoreRetries int            `json:"score_retries"` // still pending with an error
	Recent       []FetchFailure `json:"recent"`
}

// FetchFailure is one failed fetch run.
type FetchFailure struct {
	SourceID   int64     `json:"source_id"`
	SourceName string    `json:"source_name"`
	Error      string    `json:"error"`
	At         time.Time `json:"at"`
}

// TokenMetrics is the model usage of the period.
type TokenMetrics struct {
	Calls            int64      `json:"calls"`
	PromptTokens     int64      `json:"prompt_tokens"`
	CompletionTokens int64      `json:"completion_tokens"`
	TotalTokens      int64      `json:"total_tokens"`
	PerDay           []TokenDay `json:"per_day"`
}

// TokenDay is the usage of one model kind on one day.
type TokenDay struct {
	Day              string `json:"day"`
	Kind             string `json:"kind"`
	Model            string `json:"model"`
	Calls            int64  `json:"calls"`
	PromptTokens     int64  `json:"prompt_tokens"`
	CompletionTokens int64  `json:"completion_tokens"`
}
