package delivery

import (
	"math"
	"math/rand/v2"
	"time"

	"github.com/riverqueue/river"

	"webhook-gateway/internal/queue"
)

// NextRetry overrides River's default retry schedule with a per-destination
// exponential backoff + jitter (BR-08). The backoff base and cap were
// snapshotted into the job args at enqueue, so a job retries under the policy
// that was in effect when it was created. Returning the zero time falls back to
// River's built-in policy (e.g. for legacy jobs with no snapshot).
func (w *Worker) NextRetry(job *river.Job[queue.DeliveryArgs]) time.Time {
	base := time.Duration(job.Args.BackoffBaseSeconds) * time.Second
	maxDelay := time.Duration(job.Args.BackoffMaxSeconds) * time.Second

	delay := backoffWithJitter(job.Attempt, base, maxDelay)
	if delay <= 0 {
		return time.Time{}
	}
	return time.Now().Add(delay)
}

// backoffWithJitter computes the wait before the given attempt's retry:
// base * 2^(attempt-1), capped at maxDelay, then jittered to 50–100% of that so
// many deliveries failing at once don't retry in lockstep. A non-positive base
// returns 0 to signal "no policy" to the caller.
func backoffWithJitter(attempt int, base, maxDelay time.Duration) time.Duration {
	if base <= 0 {
		return 0
	}

	delay := base
	if attempt > 1 {
		scaled := float64(base) * math.Pow(2, float64(attempt-1))
		// A huge attempt count overflows the float→Duration conversion to a
		// non-positive value; treat that (and anything past the cap) as the cap.
		if maxDelay > 0 && (scaled >= float64(maxDelay) || scaled <= 0) {
			delay = maxDelay
		} else {
			delay = time.Duration(scaled)
		}
	}
	if maxDelay > 0 && delay > maxDelay {
		delay = maxDelay
	}

	half := delay / 2
	if half <= 0 {
		return delay
	}
	return half + time.Duration(rand.Int64N(int64(half)+1))
}
