// Package resend sends e-mail through the Resend HTTP API
// (https://resend.com/docs/api-reference/emails/send-email).
package resend

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// DefaultBaseURL is the Resend API.
const DefaultBaseURL = "https://api.resend.com"

// Client sends e-mails from From with the API key.
type Client struct {
	APIKey  string
	From    string
	BaseURL string
	HTTP    *http.Client
}

// Send posts one e-mail and returns the Resend message id.
func (c *Client) Send(ctx context.Context, to, subject, htmlBody, textBody string) (string, error) {
	base := c.BaseURL
	if base == "" {
		base = DefaultBaseURL
	}
	hc := c.HTTP
	if hc == nil {
		hc = &http.Client{Timeout: 15 * time.Second}
	}
	body, _ := json.Marshal(map[string]any{
		"from": c.From, "to": []string{to}, "subject": subject, "html": htmlBody, "text": textBody,
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(base, "/")+"/emails", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+c.APIKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "horizon-go")
	resp, err := hc.Do(req)
	if err != nil {
		return "", fmt.Errorf("resend: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if resp.StatusCode/100 != 2 {
		var e struct {
			Message string `json:"message"`
			Name    string `json:"name"`
		}
		_ = json.Unmarshal(raw, &e)
		return "", fmt.Errorf("resend: status %d: %s %s", resp.StatusCode, e.Name, e.Message)
	}
	var out struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(raw, &out); err != nil || out.ID == "" {
		return "", fmt.Errorf("resend: unexpected reply (status %d)", resp.StatusCode)
	}
	return out.ID, nil
}
