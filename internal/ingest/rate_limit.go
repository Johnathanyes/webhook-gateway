package ingest

import (
	"errors"
	"io"
	"net/http"
	"sync"
	"time"
)

// readBody reads the request body under a hard size cap (BR-06). tooLarge is
// true when the body exceeds maxBytes — the caller should reply 413 and persist
// nothing, since the cap trips during the read, before any insert. err is any
// other read failure. http.MaxBytesReader also caps how much of an oversized
// body is buffered, so a huge upload can't exhaust memory before it's rejected.
func readBody(w http.ResponseWriter, r *http.Request, maxBytes int64) (body []byte, tooLarge bool, err error) {
	r.Body = http.MaxBytesReader(w, r.Body, maxBytes)
	body, err = io.ReadAll(r.Body)
	if err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			return nil, true, nil
		}
		return nil, false, err
	}
	return body, false, nil
}

// rateLimiter holds one token bucket per source, created lazily on first
// request. It is in-memory only (no Redis, BR-06): buckets are keyed by a
// source's stable endpoint path and the map is bounded by the number of
// admin-created sources, so no eviction is needed.
type rateLimiter struct {
	mu         sync.Mutex
	buckets    map[string]*tokenBucket
	ratePerSec float64
	burst      float64
}

// newRateLimiter builds a limiter refilling perSecond tokens per second per
// source, with a burst capacity equal to that rate.
func newRateLimiter(perSecond int) *rateLimiter {
	return &rateLimiter{
		buckets:    make(map[string]*tokenBucket),
		ratePerSec: float64(perSecond),
		burst:      float64(perSecond),
	}
}

// allow consumes a token for key, returning false when the source's bucket is
// empty (the caller should reply 429).
func (rl *rateLimiter) allow(key string) bool {
	rl.mu.Lock()
	b, ok := rl.buckets[key]
	if !ok {
		b = &tokenBucket{tokens: rl.burst, burst: rl.burst, ratePerSec: rl.ratePerSec, last: time.Now()}
		rl.buckets[key] = b
	}
	rl.mu.Unlock()
	return b.allow()
}

// tokenBucket is a single source's bucket: it refills continuously at
// ratePerSec up to burst, and allow() spends one token if available.
type tokenBucket struct {
	mu         sync.Mutex
	tokens     float64
	burst      float64
	ratePerSec float64
	last       time.Time
}

func (b *tokenBucket) allow() bool {
	b.mu.Lock()
	defer b.mu.Unlock()

	now := time.Now()
	b.tokens += now.Sub(b.last).Seconds() * b.ratePerSec
	if b.tokens > b.burst {
		b.tokens = b.burst
	}
	b.last = now

	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}
