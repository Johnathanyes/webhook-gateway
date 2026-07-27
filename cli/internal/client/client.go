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

func (c *Client) get(ctx context.Context, path string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+path, nil)
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
