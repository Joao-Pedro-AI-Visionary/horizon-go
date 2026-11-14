package domain

import "time"

// Alert kinds.
const (
	AlertSource         = "source"          // the latest fetch of a source failed
	AlertScoring        = "scoring"         // items of a source could not be scored
	AlertDigest         = "digest"          // digest generation failed
	AlertCollection     = "collection"      // no successful collection for too long
	AlertJobFailed      = "job_failed"      // internal scheduler collect or cleanup failed
	AlertJobMissed      = "job_missed"      // scheduled slot missed on catch-up or overdue > 3h
	AlertCleanupBlocked = "cleanup_blocked" // retention cleanup blocked by safety guards
	AlertXIngestStale   = "x_ingest_stale"  // no X (push) posts ingested for > 36h
	AlertJobRecovered   = "job_recovered"   // internal scheduler job succeeded after a prior error
	AlertTest           = "test"            // manual operational alert verification
)

// Alert is one recent failure for the admin dashboard.
type Alert struct {
	Kind     string    `json:"kind"`
	SourceID *int64    `json:"source_id,omitempty"`
	Name     string    `json:"name"`
	Message  string    `json:"message"`
	At       time.Time `json:"at"`
}

// AdminCredential is the admin password saved by a password reset.
type AdminCredential struct {
	PasswordHash   string
	SessionVersion int
}
