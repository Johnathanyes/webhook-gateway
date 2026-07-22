package alerting

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// SlackNotifier posts alerts to a Slack incoming webhook URL.
type SlackNotifier struct {
	WebhookURL string
	Client     *http.Client
}

func newSlackNotifier(url string) *SlackNotifier {
	return &SlackNotifier{WebhookURL: url, Client: &http.Client{Timeout: 10 * time.Second}}
}

func (s *SlackNotifier) Notify(ctx context.Context, a Alert) error {
	payload, err := json.Marshal(map[string]string{"text": a.Body()})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.WebhookURL, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := s.Client.Do(req)
	if err != nil {
		return fmt.Errorf("posting slack alert: %w", err)
	}
	defer func() { _ = resp.Body.Close() } ()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("slack webhook returned %d", resp.StatusCode)
	}
	return nil
}
