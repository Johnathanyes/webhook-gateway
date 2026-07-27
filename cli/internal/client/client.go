// Package client is a thin HTTP client for the gateway's REST API. Every
// command that talks to a gateway goes through it, so bearer auth and the
// error-envelope convention are handled in exactly one place.
package client

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Client talks to one gateway with one credential.
type Client struct {
	baseURL string
	apiKey  string
	http    *http.Client
}

const defaultTimeout = 30 * time.Second

// New builds a client for a gateway base URL. The credential is either an API
// key or the admin password — the gateway accepts both as a bearer token.
func New(baseURL, apiKey string) *Client {
	return &Client{
		baseURL: strings.TrimSuffix(baseURL, "/"),
		apiKey:  apiKey,
		http:    &http.Client{Timeout: defaultTimeout},
	}
}

// Source mirrors the gateway's source response. EndpointPath is the unguessable
// segment providers POST to.
type Source struct {
	ID           string    `json:"id"`
	Name         string    `json:"name"`
	ProviderType string    `json:"provider_type"`
	EndpointPath string    `json:"endpoint_path"`
	CreatedAt    time.Time `json:"created_at"`
}

// ListSources fetches every configured source. It doubles as the credential
// check for `login`: it needs only the `read` scope, and a gateway that answers
// it is reachable, running, and accepting the credential.
func (c *Client) ListSources(ctx context.Context) ([]Source, error) {
	var sources []Source
	if err := c.get(ctx, "/api/sources", &sources); err != nil {
		return nil, err
	}
	return sources, nil
}

// SourceByName resolves a source name to its record. Developers address sources
// by name; every API path below /api/sources takes an id, so this is the bridge.
func (c *Client) SourceByName(ctx context.Context, name string) (Source, error) {
	sources, err := c.ListSources(ctx)
	if err != nil {
		return Source{}, err
	}
	for _, s := range sources {
		if s.Name == name {
			return s, nil
		}
	}
	return Source{}, fmt.Errorf("no source named %q on %s", name, c.baseURL)
}

// Event is a stored event in full. RawBody is base64 on the wire, so it decodes
// back to the provider's exact bytes; RawHeaders is the original http.Header.
type Event struct {
	ID          string              `json:"id"`
	SourceID    string              `json:"source_id"`
	RawHeaders  map[string][]string `json:"raw_headers"`
	RawBody     []byte              `json:"raw_body"`
	ContentType string              `json:"content_type,omitempty"`
	Verified    bool                `json:"verified"`
	ReceivedAt  time.Time           `json:"received_at"`
}

// GetEvent fetches one stored event, including the bytes needed to replay it.
func (c *Client) GetEvent(ctx context.Context, id string) (Event, error) {
	var event Event
	if err := c.get(ctx, "/api/events/"+url.PathEscape(id), &event); err != nil {
		return Event{}, err
	}
	return event, nil
}

// EventSummary is the list-view projection: metadata only, no payload.
type EventSummary struct {
	ID         string    `json:"id"`
	SourceID   string    `json:"source_id"`
	Verified   bool      `json:"verified"`
	ReceivedAt time.Time `json:"received_at"`
}

// ListEvents returns events newest-first, optionally filtered to one source.
func (c *Client) ListEvents(ctx context.Context, sourceID string, limit int) ([]EventSummary, error) {
	query := url.Values{}
	if sourceID != "" {
		query.Set("source_id", sourceID)
	}
	if limit > 0 {
		query.Set("limit", strconv.Itoa(limit))
	}

	var resp struct {
		Events []EventSummary `json:"events"`
	}
	if err := c.get(ctx, "/api/events?"+query.Encode(), &resp); err != nil {
		return nil, err
	}
	return resp.Events, nil
}

// TestEvent is the result of triggering a sample event.
type TestEvent struct {
	EventID  string `json:"event_id"`
	Verified bool   `json:"verified"`
}

// TriggerTestEvent asks the gateway to sign its sample payload with the
// source's real secret and run it through the ingest path. Needs `write`.
func (c *Client) TriggerTestEvent(ctx context.Context, sourceID string) (TestEvent, error) {
	var out TestEvent
	if err := c.post(ctx, "/api/sources/"+url.PathEscape(sourceID)+"/test-event", &out); err != nil {
		return TestEvent{}, err
	}
	return out, nil
}

func (c *Client) get(ctx context.Context, path string, out any) error {
	return c.do(ctx, http.MethodGet, path, out)
}

func (c *Client) post(ctx context.Context, path string, out any) error {
	return c.do(ctx, http.MethodPost, path, out)
}

func (c *Client) do(ctx context.Context, method, path string, out any) error {
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, nil)
	if err != nil {
		return fmt.Errorf("building request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("Accept", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("contacting gateway: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return statusError(resp)
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("decoding %s response: %w", path, err)
	}
	return nil
}

// APIError is a non-2xx response, carrying the gateway's error envelope message
// when there is one. Callers match on Status to tell 401 from 403.
type APIError struct {
	Status  int
	Message string
}

func (e *APIError) Error() string {
	if e.Message == "" {
		return fmt.Sprintf("gateway returned %d %s", e.Status, http.StatusText(e.Status))
	}
	return fmt.Sprintf("gateway returned %d: %s", e.Status, e.Message)
}

// statusError decodes the {"error": "..."} envelope every gateway handler uses.
// A body that isn't that shape (a proxy's HTML 502, say) still yields a useful
// status-only error rather than a decode failure.
func statusError(resp *http.Response) error {
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	var envelope struct {
		Error string `json:"error"`
	}
	_ = json.Unmarshal(body, &envelope)
	return &APIError{Status: resp.StatusCode, Message: envelope.Error}
}

// NormalizeURL cleans up what a human types at the login prompt: it defaults to
// http:// when no scheme is given and rejects anything that isn't a usable
// http(s) base URL.
func NormalizeURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", fmt.Errorf("gateway URL is required")
	}
	if !strings.Contains(raw, "://") {
		raw = "http://" + raw
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("invalid gateway URL: %w", err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "", fmt.Errorf("gateway URL must be http or https, got %q", parsed.Scheme)
	}
	if parsed.Host == "" {
		return "", fmt.Errorf("gateway URL is missing a host")
	}
	return strings.TrimSuffix(parsed.String(), "/"), nil
}
