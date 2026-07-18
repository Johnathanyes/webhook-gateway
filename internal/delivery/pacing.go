package delivery

import (
	"sync"
	"time"
)

// Holds one token bucket per destination for delivery pacing.
type pacer struct {
	mu      sync.Mutex
	buckets map[[16]byte]*paceBucket
}

func newPacer() *pacer {
	return &pacer{buckets: make(map[[16]byte]*paceBucket)}
}

// Returns how long to wait before delivering to destID. 0 means deliver now (a token was consumed). A
// non-positive rps means the destination is unthrottled, so it returns 0.
func (p *pacer) reserve(destID [16]byte, rps int32) time.Duration {
	if rps <= 0 {
		return 0
	}
	p.mu.Lock()
	b, ok := p.buckets[destID]
	if !ok || b.ratePerSec != float64(rps) {
		b = &paceBucket{tokens: float64(rps), burst: float64(rps), ratePerSec: float64(rps), last: time.Now()}
		p.buckets[destID] = b
	}
	p.mu.Unlock()
	return b.reserve()
}

// One destination's token bucket
type paceBucket struct {
	mu         sync.Mutex
	tokens     float64
	burst      float64
	ratePerSec float64
	last       time.Time
}

func (b *paceBucket) reserve() time.Duration {
	b.mu.Lock()
	defer b.mu.Unlock()

	now := time.Now()
	b.tokens += now.Sub(b.last).Seconds() * b.ratePerSec
	if b.tokens > b.burst {
		b.tokens = b.burst
	}
	b.last = now

	if b.tokens >= 1 {
		b.tokens--
		return 0
	}
	wait := (1 - b.tokens) / b.ratePerSec
	return time.Duration(wait * float64(time.Second))
}
