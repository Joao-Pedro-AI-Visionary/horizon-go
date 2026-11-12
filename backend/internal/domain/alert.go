package domain

import "time"

// Alert kinds.
const (
	AlertSource     = "source"     // the latest fetch of a source failed
	AlertScoring    = "scoring"    // items of a source could not be scored
	AlertDigest     = "digest"     // digest generation failed
	AlertCollection = "collection" // no successful collection for too long
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
