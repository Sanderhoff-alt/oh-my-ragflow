package reconciler

import (
	"math/rand"
	"time"
)

// BackoffPolicy defines the unified retry backoff schedule for deferred tenant
// reconciliation and catalog rollout retries.
type BackoffPolicy struct {
	Intervals []time.Duration
}

// DefaultBackoffPolicy provides the production retry schedule:
// 5s -> 15s -> 30s -> 1m -> 2m (capped at 2m after jitter).
//
// Combined with a 15s Tier-2 poll interval, this heals short gateway restarts
// promptly while capping steady-state retry delay at 2m so tenants recover
// quickly once an extended outage ends.
var DefaultBackoffPolicy = BackoffPolicy{
	Intervals: []time.Duration{
		5 * time.Second,
		15 * time.Second,
		30 * time.Second,
		1 * time.Minute,
		2 * time.Minute,
	},
}

// FastTestBackoffPolicy provides millisecond-scale intervals for fast,
// deterministic unit testing.
var FastTestBackoffPolicy = BackoffPolicy{
	Intervals: []time.Duration{
		10 * time.Millisecond,
		20 * time.Millisecond,
		30 * time.Millisecond,
	},
}

// CalculateBackoff returns the delay for zero-based retry attempt (0 = delay
// after the first failure, 1 = after the second failure, ...), with up to 10%
// positive jitter and capped at the last configured interval.
func (p BackoffPolicy) CalculateBackoff(attempt int) time.Duration {
	if len(p.Intervals) == 0 {
		return 15 * time.Second
	}
	if attempt < 0 {
		attempt = 0
	}
	var base time.Duration
	if attempt < len(p.Intervals) {
		base = p.Intervals[attempt]
	} else {
		base = p.Intervals[len(p.Intervals)-1]
	}
	capDelay := p.Intervals[len(p.Intervals)-1]
	jitter := time.Duration(0)
	if base > 10*time.Millisecond {
		jitter = time.Duration(rand.Int63n(int64(base / 10)))
	}
	delay := base + jitter
	if delay > capDelay {
		delay = capDelay
	}
	return delay
}
