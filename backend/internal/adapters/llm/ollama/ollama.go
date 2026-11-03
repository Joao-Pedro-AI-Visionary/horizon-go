// Package ollama implements ports.LLM (/api/chat) and ports.Embedder
// (/api/embed) on a local Ollama server.
package ollama

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/jpdev/horizon-go/internal/ports"
	"github.com/jpdev/horizon-go/internal/retry"
)

// Defaults from CLAUDE.md.
const (
	DefaultURL         = "http://localhost:11434"
	DefaultChatModel   = "qwen3:14b"
	DefaultEmbedModel  = "nomic-embed-text"
	DefaultTemperature = 0.3
	// The first call loads the model into VRAM (~1 min for qwen3:14b).
	DefaultTimeout = 3 * time.Minute
)

// Config selects the server and models.
type Config struct {
	URL         string
	ChatModel   string
	EmbedModel  string
	Temperature float64
	Client      *http.Client
}

// Client talks to one Ollama server.
type Client struct {
	base   string
	chat   string
	embed  string
	temp   float64
	client *http.Client
}

var (
	_ ports.LLM      = (*Chat)(nil)
	_ ports.Embedder = (*Embed)(nil)
)

// New fills defaults and returns a client.
func New(cfg Config) *Client {
	c := &Client{
		base:   strings.TrimRight(cfg.URL, "/"),
		chat:   cfg.ChatModel,
		embed:  cfg.EmbedModel,
		temp:   cfg.Temperature,
		client: cfg.Client,
	}
	if c.base == "" {
		c.base = DefaultURL
	}
	if c.chat == "" {
		c.chat = DefaultChatModel
	}
	if c.embed == "" {
		c.embed = DefaultEmbedModel
	}
	if c.temp == 0 {
		c.temp = DefaultTemperature
	}
	if c.client == nil {
		c.client = &http.Client{Timeout: DefaultTimeout}
	}
	return c
}

// Chat is the ports.LLM view of the client.
type Chat struct{ c *Client }

// Embed is the ports.Embedder view of the client.
type Embed struct{ c *Client }

// LLM returns the chat model port.
func (c *Client) LLM() *Chat { return &Chat{c} }

// Embedder returns the embedding model port.
func (c *Client) Embedder() *Embed { return &Embed{c} }

func (c *Client) post(ctx context.Context, path string, body, out any) error {
	data, err := json.Marshal(body)
	if err != nil {
		return retry.Permanent(err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+path, bytes.NewReader(data))
	if err != nil {
		return retry.Permanent(err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.client.Do(req)
	if err != nil {
		return fmt.Errorf("ollama %s: %w", path, err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return fmt.Errorf("ollama %s: read: %w", path, err)
	}
	if resp.StatusCode != http.StatusOK {
		err := fmt.Errorf("ollama %s: status %d: %s", path, resp.StatusCode, strings.TrimSpace(string(raw)))
		// 4xx (model not found, bad request) will not fix itself; 408/429/5xx may.
		if resp.StatusCode >= 400 && resp.StatusCode < 500 && resp.StatusCode != http.StatusRequestTimeout && resp.StatusCode != http.StatusTooManyRequests {
			return retry.Permanent(err)
		}
		return err
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("ollama %s: decode: %w", path, err)
	}
	return nil
}

type message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatRequest struct {
	Model    string         `json:"model"`
	Messages []message      `json:"messages"`
	Stream   bool           `json:"stream"`
	Think    bool           `json:"think"`
	Format   string         `json:"format,omitempty"`
	Options  map[string]any `json:"options"`
}

type chatResponse struct {
	Message message `json:"message"`
	Done    bool    `json:"done"`
}

// Model is the chat model name.
func (m *Chat) Model() string { return m.c.chat }

// Chat sends one non-streaming completion with thinking disabled.
func (m *Chat) Chat(ctx context.Context, req ports.ChatRequest) (string, error) {
	body := chatRequest{
		Model:   m.c.chat,
		Stream:  false,
		Think:   false,
		Options: map[string]any{"temperature": m.c.temp},
	}
	if req.JSON {
		body.Format = "json"
	}
	if req.System != "" {
		body.Messages = append(body.Messages, message{Role: "system", Content: req.System})
	}
	body.Messages = append(body.Messages, message{Role: "user", Content: req.User})
	var out chatResponse
	if err := m.c.post(ctx, "/api/chat", body, &out); err != nil {
		return "", err
	}
	if strings.TrimSpace(out.Message.Content) == "" {
		return "", fmt.Errorf("ollama /api/chat: empty response")
	}
	return out.Message.Content, nil
}

type embedRequest struct {
	Model string   `json:"model"`
	Input []string `json:"input"`
}

type embedResponse struct {
	Embeddings [][]float32 `json:"embeddings"`
}

// Model is the embedding model name.
func (m *Embed) Model() string { return m.c.embed }

// Embed returns one vector per text.
func (m *Embed) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	var out embedResponse
	if err := m.c.post(ctx, "/api/embed", embedRequest{Model: m.c.embed, Input: texts}, &out); err != nil {
		return nil, err
	}
	if len(out.Embeddings) != len(texts) {
		return nil, fmt.Errorf("ollama /api/embed: %d vectors for %d inputs", len(out.Embeddings), len(texts))
	}
	return out.Embeddings, nil
}
