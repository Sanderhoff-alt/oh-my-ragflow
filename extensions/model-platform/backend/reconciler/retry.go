package reconciler

import (
	"math/rand"
	"time"
)

// BackoffPolicy defines the retry delays for Tier 1 fast queue and Tier 2 level-triggered poller.
type BackoffPolicy struct {
	Tier1Intervals []time.Duration
	Tier2Intervals []time.Duration
}

// DefaultBackoffPolicy provides the production retry schedules:
// Tier 1: 1s, 2s, 4s, 8s, 16s (~31s total fast retry window).
// Tier 2: 15s (meets ~45s gateway recovery target with Tier 1), 1m, 2m, 5m, 10m max (slow continuous self-healing).
var DefaultBackoffPolicy = BackoffPolicy{
	Tier1Intervals: []time.Duration{
		1 * time.Second,
		2 * time.Second,
		4 * time.Second,
		8 * time.Second,
		16 * time.Second,
	},
	Tier2Intervals: []time.Duration{
		15 * time.Second,
		1 * time.Minute,
		2 * time.Minute,
		5 * time.Minute,
		10 * time.Minute,
	},
}

// FastTestBackoffPolicy provides millisecond-scale intervals for fast, deterministic unit testing.
var FastTestBackoffPolicy = BackoffPolicy{
	Tier1Intervals: []time.Duration{
		5 * time.Millisecond,
		10 * time.Millisecond,
		15 * time.Millisecond,
	},
	Tier2Intervals: []time.Duration{
		10 * time.Millisecond,
		20 * time.Millisecond,
		30 * time.Millisecond,
	},
}

// CalculateTier1Backoff returns the delay for a Tier 1 fast retry attempt.
func (p BackoffPolicy) CalculateTier1Backoff(attempt int) time.Duration {
	if len(p.Tier1Intervals) == 0 {
		return 1 * time.Second
	}
	var base time.Duration
	if attempt >= 0 && attempt < len(p.Tier1Intervals) {
		base = p.Tier1Intervals[attempt]
	} else {
		base = p.Tier1Intervals[len(p.Tier1Intervals)-1]
	}
	jitter := time.Duration(0)
	if base > 10*time.Millisecond {
		jitter = time.Duration(rand.Int63n(int64(base / 5)))
	}
	return base + jitter
}

// CalculateTier2Backoff returns the delay for a Tier 2 slow self-healing retry attempt.
func (p BackoffPolicy) CalculateTier2Backoff(attempt int) time.Duration {
	if len(p.Tier2Intervals) == 0 {
		return 1 * time.Minute
	}
	var base time.Duration
	if attempt >= 0 && attempt < len(p.Tier2Intervals) {
		base = p.Tier2Intervals[attempt]
	} else {
		base = p.Tier2Intervals[len(p.Tier2Intervals)-1]
	}
	jitter := time.Duration(0)
	if base > 10*time.Millisecond {
		jitter = time.Duration(rand.Int63n(int64(base / 10)))
	}
	delay := base + jitter
	if delay > 10*time.Minute {
		delay = 10 * time.Minute
	}
	return delay
}
