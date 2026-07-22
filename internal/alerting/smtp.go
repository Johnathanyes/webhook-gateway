package alerting

import (
	"context"
	"fmt"
	"net/smtp"
	"strings"
)

// SMTPNotifier sends alerts as plain-text email via an SMTP relay.
type SMTPNotifier struct {
	Addr string // host:port
	From string
	To   []string
	Auth smtp.Auth // nil for an unauthenticated relay
}

func newSMTPNotifier(c SMTPConfig) *SMTPNotifier {
	var auth smtp.Auth
	if c.Username != "" {
		host := c.Host
		if i := strings.LastIndex(c.Host, ":"); i >= 0 {
			host = c.Host[:i]
		}
		auth = smtp.PlainAuth("", c.Username, c.Password, host)
	}
	return &SMTPNotifier{Addr: c.Host, From: c.From, To: c.To, Auth: auth}
}

// Notify sends the alert. net/smtp.SendMail is synchronous and ignores ctx; the
// evaluator bounds overall work, so a slow relay only delays the next check.
func (s *SMTPNotifier) Notify(_ context.Context, a Alert) error {
	msg := fmt.Sprintf("From: %s\r\nTo: %s\r\nSubject: %s\r\n\r\n%s\r\n",
		s.From, strings.Join(s.To, ", "), a.Subject(), a.Body())
	if err := smtp.SendMail(s.Addr, s.Auth, s.From, s.To, []byte(msg)); err != nil {
		return fmt.Errorf("sending smtp alert: %w", err)
	}
	return nil
}
