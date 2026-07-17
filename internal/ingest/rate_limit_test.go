package ingest

import (
	"sync"
	"testing"
	"time"
)

// TestRateLimiterBurst spends the full burst, confirms the next request is
// denied, then simulates a second of elapsed time and confirms the bucket has
// refilled. Time is advanced by rewinding the bucket's last-seen instant rather
// than sleeping, so the test is fast and deterministic.
func TestRateLimiterBurst(t *testing.T) {
	const rate = 10 // burst 10, refills 10/sec
	rl := newRateLimiter(rate)
	const key = "src_a"

	for i := range rate {
		if !rl.allow(key) {
			t.Fatalf("request %d denied within the burst of %d", i+1, rate)
		}
	}
	if rl.allow(key) {
		t.Fatal("request allowed after the burst was exhausted")
	}

	// Rewind last by one second: the next allow() credits rate*1s tokens.
	rl.buckets[key].last = rl.buckets[key].last.Add(-time.Second)
	if !rl.allow(key) {
		t.Error("request denied after a second of refill")
	}
}

// TestRateLimiterPerSourceIsolation confirms buckets are keyed per source:
// exhausting one source's bucket does not affect another's.
func TestRateLimiterPerSourceIsolation(t *testing.T) {
	rl := newRateLimiter(1) // burst 1

	if !rl.allow("src_a") {
		t.Fatal("first request to src_a denied")
	}
	if rl.allow("src_a") {
		t.Fatal("second request to src_a allowed past its burst of 1")
	}
	if !rl.allow("src_b") {
		t.Error("src_b denied though it has its own full bucket")
	}
}

// TestRateLimiterConcurrent hammers one source's bucket from many goroutines so
// `go test -race` exercises the limiter's locking. It asserts the bucket never
// hands out more than the burst of tokens.
func TestRateLimiterConcurrent(t *testing.T) {
	const burst = 50
	rl := newRateLimiter(burst)

	var (
		wg      sync.WaitGroup
		mu      sync.Mutex
		allowed int
	)
	for range burst * 4 {
		wg.Go(func() {
			if rl.allow("src_shared") {
				mu.Lock()
				allowed++
				mu.Unlock()
			}
		})
	}
	wg.Wait()

	// Refill during the run is negligible (sub-millisecond), so no more than a
	// couple of extra tokens beyond the burst can appear.
	if allowed > burst+2 {
		t.Errorf("granted %d tokens, want <= burst %d (+refill slack)", allowed, burst)
	}
}