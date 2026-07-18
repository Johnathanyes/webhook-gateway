package delivery

import (
	"testing"
	"time"
)

var (
	destA = [16]byte{1}
	destB = [16]byte{2}
)

func TestPacerUnthrottled(t *testing.T) {
	p := newPacer()
	for i := range 100 {
		if wait := p.reserve(destA, 0); wait != 0 {
			t.Fatalf("call %d: rps=0 returned wait %s, want 0 (unthrottled)", i, wait)
		}
	}
	if wait := p.reserve(destA, -5); wait != 0 {
		t.Errorf("negative rps returned wait %s, want 0", wait)
	}
}

func TestPacerBurstThenThrottle(t *testing.T) {
	p := newPacer()
	// The bucket starts full, so the first rps calls pass immediately.
	for i := range 3 {
		if wait := p.reserve(destA, 3); wait != 0 {
			t.Fatalf("burst call %d returned wait %s, want 0", i, wait)
		}
	}
	// The next call is over the limit and must be told to wait.
	wait := p.reserve(destA, 3)
	if wait <= 0 {
		t.Fatalf("over-limit call returned %s, want a positive wait", wait)
	}
	if wait > time.Second {
		t.Errorf("wait %s is longer than the ~1/3s refill implies", wait)
	}
}

func TestPacerRefillsAfterWait(t *testing.T) {
	p := newPacer()
	for range 5 {
		p.reserve(destA, 5) // drain the burst
	}
	wait := p.reserve(destA, 5)
	if wait <= 0 {
		t.Fatalf("expected a positive wait after draining the bucket")
	}
	time.Sleep(wait + 20*time.Millisecond)
	if got := p.reserve(destA, 5); got != 0 {
		t.Errorf("after waiting %s, reserve returned %s, want 0", wait, got)
	}
}

func TestPacerIndependentPerDestination(t *testing.T) {
	p := newPacer()
	for range 2 {
		p.reserve(destA, 2) // drain destA
	}
	if wait := p.reserve(destA, 2); wait <= 0 {
		t.Fatal("destA should be throttled after draining its burst")
	}
	// destB has its own bucket and is untouched.
	if wait := p.reserve(destB, 2); wait != 0 {
		t.Errorf("destB returned wait %s, want 0 — buckets must be independent", wait)
	}
}

func TestPacerRebuildsOnRateChange(t *testing.T) {
	p := newPacer()
	for range 2 {
		p.reserve(destA, 2) // drain at rps=2
	}
	if wait := p.reserve(destA, 2); wait <= 0 {
		t.Fatal("expected throttling at the old rate")
	}
	// Raising the rate rebuilds the bucket with a fresh burst.
	if wait := p.reserve(destA, 10); wait != 0 {
		t.Errorf("after rate change reserve returned %s, want 0 (fresh bucket)", wait)
	}
}
