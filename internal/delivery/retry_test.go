package delivery

import (
	"testing"
	"time"

	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"

	"webhook-gateway/internal/queue"
)

func TestBackoffWithJitter(t *testing.T) {
	base := 30 * time.Second
	maxDelay := 12 * time.Hour

	t.Run("non-positive base signals no policy", func(t *testing.T) {
		if d := backoffWithJitter(1, 0, maxDelay); d != 0 {
			t.Errorf("base=0 gave %s, want 0", d)
		}
	})

	t.Run("stays within [computed/2, computed] each attempt", func(t *testing.T) {
		for attempt := 1; attempt <= 10; attempt++ {
			computed := time.Duration(float64(base) * pow2(attempt-1))
			if computed > maxDelay || computed <= 0 {
				computed = maxDelay
			}
			// Sample repeatedly since the jitter is random.
			for range 50 {
				d := backoffWithJitter(attempt, base, maxDelay)
				if d < computed/2 || d > computed {
					t.Fatalf("attempt %d: %s outside [%s, %s]", attempt, d, computed/2, computed)
				}
			}
		}
	})

	t.Run("is capped at maxDelay for large attempts", func(t *testing.T) {
		for range 50 {
			d := backoffWithJitter(40, base, maxDelay)
			if d > maxDelay {
				t.Fatalf("attempt 40: %s exceeds cap %s", d, maxDelay)
			}
			if d < maxDelay/2 {
				t.Fatalf("attempt 40: %s below capped floor %s", d, maxDelay/2)
			}
		}
	})
}

func TestNextRetry(t *testing.T) {
	w := NewWorker(nil, nil)

	t.Run("no snapshot falls back to River default", func(t *testing.T) {
		job := &river.Job[queue.DeliveryArgs]{
			JobRow: &rivertype.JobRow{Attempt: 1},
			Args:   queue.DeliveryArgs{DeliveryID: "d"},
		}
		if got := w.NextRetry(job); !got.IsZero() {
			t.Errorf("NextRetry = %v, want zero time (fall back)", got)
		}
	})

	t.Run("schedules a future retry from the snapshot", func(t *testing.T) {
		job := &river.Job[queue.DeliveryArgs]{
			JobRow: &rivertype.JobRow{Attempt: 1},
			Args:   queue.DeliveryArgs{DeliveryID: "d", BackoffBaseSeconds: 30, BackoffMaxSeconds: 43200},
		}
		got := w.NextRetry(job)
		delay := time.Until(got)
		// Attempt 1 → base 30s, jittered to [15s, 30s]; allow slack for clock.
		if delay < 10*time.Second || delay > 31*time.Second {
			t.Errorf("next retry in %s, want ~15–30s", delay)
		}
	})
}

// pow2 mirrors the exponent used by backoffWithJitter so the test computes the
// same expected bound without importing math.
func pow2(n int) float64 {
	v := 1.0
	for range n {
		v *= 2
	}
	return v
}
