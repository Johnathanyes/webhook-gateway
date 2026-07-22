package alerting

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/jackc/pgx/v5"

	"webhook-gateway/internal/db"
)

const instanceSettingKey = "alerting"

// Config is the alerting configuration stored as JSON in instance_settings.
// Zero values fall back to sensible defaults via applyDefaults.
type Config struct {
	Disabled bool `json:"disabled"`

	// CooldownMinutes is the minimum gap between two alerts for the same
	// destination+condition.
	CooldownMinutes int `json:"cooldown_minutes"`
	// WindowMinutes is the lookback for both the failure-rate ratio and the DLQ
	// recency check.
	WindowMinutes int `json:"window_minutes"`
	// FailureThreshold in [0,1] trips the failure-rate condition; 0 disables it.
	FailureThreshold float64 `json:"failure_threshold"`
	// MinDeliveries guards the ratio against small samples.
	MinDeliveries int `json:"min_deliveries"`

	Slack *SlackConfig `json:"slack,omitempty"`
	SMTP  *SMTPConfig  `json:"smtp,omitempty"`

	Enabled bool `json:"-"`
}

type SlackConfig struct {
	WebhookURL string `json:"webhook_url"`
}

type SMTPConfig struct {
	Host     string   `json:"host"` // host:port
	From     string   `json:"from"`
	To       []string `json:"to"`
	Username string   `json:"username"`
	Password string   `json:"password"`
}

func (c *Config) applyDefaults() {
	if c.CooldownMinutes <= 0 {
		c.CooldownMinutes = 60
	}
	if c.WindowMinutes <= 0 {
		c.WindowMinutes = 15
	}
	if c.MinDeliveries <= 0 {
		c.MinDeliveries = 20
	}
}

// LoadConfig reads and parses the alerting configuration. A missing setting is
// not an error, means alert is off
func LoadConfig(ctx context.Context, q *db.Queries) (Config, error) {
	raw, err := q.GetInstanceSetting(ctx, instanceSettingKey)
	if errors.Is(err, pgx.ErrNoRows) {
		return Config{Enabled: false}, nil
	}
	if err != nil {
		return Config{}, err
	}
	var c Config
	if err := json.Unmarshal(raw, &c); err != nil {
		return Config{}, err
	}
	c.Enabled = !c.Disabled
	c.applyDefaults()
	return c, nil
}

// buildNotifier assembles a fan-out notifier from whatever channels the config
// enables. Returns nil if none are configured.
func buildNotifier(c Config) Notifier {
	var ns []Notifier
	if c.Slack != nil && c.Slack.WebhookURL != "" {
		ns = append(ns, newSlackNotifier(c.Slack.WebhookURL))
	}
	if c.SMTP != nil && c.SMTP.Host != "" {
		ns = append(ns, newSMTPNotifier(*c.SMTP))
	}
	if len(ns) == 0 {
		return nil
	}
	return multiNotifier(ns)
}
