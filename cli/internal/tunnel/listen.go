package tunnel

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
)

// Reconnect pacing. A tunnel is a long-lived dev session, so a dropped socket
// retries indefinitely rather than exiting.
const (
	minBackoff = time.Second
	maxBackoff = 30 * time.Second
)

// readLimit caps one event frame. It must exceed the gateway's maximum ingest
// body plus base64 expansion and headers, or a large webhook would kill the
// socket instead of being delivered.
const readLimit = 16 << 20

// forwardTimeout is deliberately under the gateway's 30s tunnel timeout, so a
// hung local handler produces the client's own "timed out" ack — which names
// the developer's server — rather than the server's opaque deadline error.
const forwardTimeout = 25 * time.Second

// Options configures one `listen` session.
type Options struct {
	GatewayURL string    // gateway base URL, http(s)
	APIKey     string    // bearer credential with the `tunnel` scope
	Source     string    // source name; empty means every source
	ForwardTo  string    // local target, normalized http(s) URL
	Out        io.Writer // defaults to os.Stdout
}

// PermanentError is a failure that reconnecting cannot fix — a rejected
// credential, a missing scope, or an unknown source.
type PermanentError struct{ Message string }

func (e *PermanentError) Error() string { return e.Message }

// Listen attaches to the gateway and forwards events until ctx is cancelled.
// It reconnects with exponential backoff on transient failures and returns nil
// on a clean shutdown.
func Listen(ctx context.Context, opts Options) error {
	if opts.Out == nil {
		opts.Out = os.Stdout
	}

	backoff := minBackoff
	for {
		connected, err := runSession(ctx, opts)
		if ctx.Err() != nil {
			return nil // Ctrl-C
		}
		var permanent *PermanentError
		if errors.As(err, &permanent) {
			return permanent
		}
		// A session that got as far as `ready` proves the credential and the
		// gateway are fine, so the next drop starts over at the short backoff.
		if connected {
			backoff = minBackoff
		}

		fmt.Fprintf(opts.Out, "%s connection lost (%v) — reconnecting in %s\n",
			timestamp(), err, backoff.Round(time.Second))
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(jitter(backoff)):
		}
		if backoff < maxBackoff {
			backoff = min(backoff*2, maxBackoff)
		}
	}
}

// session holds one connection's shared state. The mutexes exist because the
// gateway pushes events concurrently (its worker pool is 10 wide) and each is
// forwarded in its own goroutine.
type session struct {
	ws         *websocket.Conn
	opts       Options
	httpClient *http.Client

	writeMu sync.Mutex // one WebSocket writer at a time
	outMu   sync.Mutex // keeps interleaved log lines intact
}

// runSession runs one connection to completion. The bool reports whether the
// tunnel ever became ready, which decides whether backoff resets.
func runSession(ctx context.Context, opts Options) (bool, error) {
	endpoint, err := websocketURL(opts.GatewayURL, opts.Source)
	if err != nil {
		return false, &PermanentError{Message: err.Error()}
	}

	ws, resp, err := websocket.Dial(ctx, endpoint, &websocket.DialOptions{
		HTTPHeader: http.Header{"Authorization": {"Bearer " + opts.APIKey}},
	})
	if err != nil {
		return false, dialError(opts, resp, err)
	}
	defer func() { _ = ws.CloseNow() }()
	ws.SetReadLimit(readLimit)

	s := &session{
		ws:         ws,
		opts:       opts,
		httpClient: &http.Client{Timeout: forwardTimeout},
	}

	var wg sync.WaitGroup
	// In-flight forwards must finish before the socket closes, or their acks
	// are written to a dead connection.
	defer wg.Wait()

	ready := false
	for {
		_, data, err := ws.Read(ctx)
		if err != nil {
			return ready, err
		}

		var typed typedFrame
		if err := json.Unmarshal(data, &typed); err != nil {
			continue // unparseable frame; the protocol says ignore
		}

		switch typed.Type {
		case FrameReady:
			var frame ReadyFrame
			if err := json.Unmarshal(data, &frame); err != nil {
				return ready, fmt.Errorf("decoding ready frame: %w", err)
			}
			ready = true
			s.printReady(frame)

		case FrameEvent:
			var frame EventFrame
			if err := json.Unmarshal(data, &frame); err != nil {
				continue
			}
			wg.Add(1)
			go func() {
				defer wg.Done()
				s.handle(ctx, frame)
			}()
		}
	}
}

// handle forwards one event and acks it with whatever the local handler said.
func (s *session) handle(ctx context.Context, frame EventFrame) {
	status, elapsed, err := forward(ctx, s.httpClient, s.opts.ForwardTo, frame)

	ack := AckFrame{
		Type:       FrameAck,
		DeliveryID: frame.DeliveryID,
		DurationMs: int(elapsed.Milliseconds()),
	}
	if err != nil {
		// The local target was never reached; the gateway records the reason
		// rather than a status code it never got.
		ack.Error = err.Error()
	} else {
		ack.StatusCode = status
	}

	s.printEvent(frame, status, elapsed, err)

	if err := s.writeFrame(ctx, ack); err != nil && ctx.Err() == nil {
		s.printf("%s failed to ack %s: %v\n", timestamp(), frame.DeliveryID, err)
	}
}

func (s *session) writeFrame(ctx context.Context, frame any) error {
	payload, err := json.Marshal(frame)
	if err != nil {
		return err
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	return s.ws.Write(ctx, websocket.MessageText, payload)
}

func (s *session) printReady(frame ReadyFrame) {
	scope := strings.Join(frame.Sources, ", ")
	if len(frame.Sources) == 0 {
		scope = "no sources"
	}
	s.printf("%s tunnel ready — %s → %s\n", timestamp(), scope, s.opts.ForwardTo)
}

// printEvent writes the one line per event that makes a tunnel readable:
//
//	12:04:31 stripe payment_intent.succeeded → 200 (45ms)
func (s *session) printEvent(frame EventFrame, status int, elapsed time.Duration, err error) {
	label := frame.Source
	if kind := eventType(frame); kind != "" {
		label += " " + kind
	}
	if err != nil {
		s.printf("%s %s → error: %v (%s)\n", timestamp(), label, err, roundMs(elapsed))
		return
	}
	s.printf("%s %s → %d (%s)\n", timestamp(), label, status, roundMs(elapsed))
}

func (s *session) printf(format string, args ...any) {
	s.outMu.Lock()
	defer s.outMu.Unlock()
	fmt.Fprintf(s.opts.Out, format, args...)
}

// dialError turns a failed upgrade into advice. A 4xx means the request itself
// is wrong — retrying it forever would just spin.
func dialError(opts Options, resp *http.Response, err error) error {
	if resp == nil {
		return fmt.Errorf("connecting to %s: %w", opts.GatewayURL, err)
	}
	switch resp.StatusCode {
	case http.StatusUnauthorized:
		return &PermanentError{Message: fmt.Sprintf("%s rejected the credential — run `login` again", opts.GatewayURL)}
	case http.StatusForbidden:
		return &PermanentError{Message: "that key lacks the `tunnel` scope"}
	case http.StatusNotFound:
		return &PermanentError{Message: fmt.Sprintf("no source named %q on %s", opts.Source, opts.GatewayURL)}
	case http.StatusBadRequest:
		return &PermanentError{Message: fmt.Sprintf("%s has no sources configured to tunnel", opts.GatewayURL)}
	}
	return fmt.Errorf("connecting to %s: %w", opts.GatewayURL, err)
}

// websocketURL derives the tunnel endpoint from the gateway's base URL,
// preserving any path prefix the gateway is mounted under.
func websocketURL(gatewayURL, source string) (string, error) {
	u, err := url.Parse(gatewayURL)
	if err != nil {
		return "", fmt.Errorf("invalid gateway URL %q: %w", gatewayURL, err)
	}
	switch u.Scheme {
	case "http":
		u.Scheme = "ws"
	case "https":
		u.Scheme = "wss"
	default:
		return "", fmt.Errorf("gateway URL must be http or https, got %q", u.Scheme)
	}
	u.Path = strings.TrimSuffix(u.Path, "/") + "/api/tunnel"

	query := url.Values{}
	if source != "" {
		query.Set("source", source)
	}
	u.RawQuery = query.Encode()
	return u.String(), nil
}

func timestamp() string { return time.Now().Format("15:04:05") }

func roundMs(d time.Duration) string { return d.Round(time.Millisecond).String() }

// jitter spreads reconnects over 50–100% of the backoff so a gateway restart
// doesn't get every tunnel back at the same instant.
func jitter(d time.Duration) time.Duration {
	half := d / 2
	if half <= 0 {
		return d
	}
	return half + time.Duration(rand.Int64N(int64(half)+1))
}

