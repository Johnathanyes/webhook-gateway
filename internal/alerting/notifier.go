// Package alerting evaluates delivery-health conditions and notifies operators
package alerting

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// Alert is one condition firing for one destination.
type Alert struct {
	Condition       string // "dlq" | "failure_rate"
	DestinationID   string
	DestinationName string
	Detail          string
	FiredAt         time.Time
}

// Subject is a one-line summary suitable for an email subject or Slack line.
func (a Alert) Subject() string {
	return fmt.Sprintf("[webhook-gateway] %s alert for %q", a.Condition, a.DestinationName)
}

// Body is the human-readable alert message.
func (a Alert) Body() string {
	return fmt.Sprintf("%s\n\nDestination: %s (%s)\nCondition: %s\nDetail: %s\nAt: %s",
		a.Subject(), a.DestinationName, a.DestinationID, a.Condition, a.Detail,
		a.FiredAt.UTC().Format(time.RFC3339))
}

// Notifier delivers an alert to an operator channel.
type Notifier interface {
	Notify(ctx context.Context, a Alert) error
}

// multiNotifier fans an alert out to every configured channel, joining errors so
// one broken channel doesn't suppress the others.
type multiNotifier []Notifier

func (m multiNotifier) Notify(ctx context.Context, a Alert) error {
	var errs []error
	for _, n := range m {
		if err := n.Notify(ctx, a); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}
