package ingest

import (
	"math/rand"
	"time"
)

// Backoff returns the wait before retry number attempt (0-based):
// base * 2^attempt, capped at max, plus up to 10% jitter.
//
// WHY jitter: when several venue loops fail at the same instant (a local
// network blip), jitter stops them retrying in lockstep and re-spiking
// the same failure.
func Backoff(attempt int, base, max time.Duration) time.Duration {
	d := base
	for i := 0; i < attempt; i++ {
		d *= 2
		if d >= max {
			break
		}
	}
	if d > max {
		d = max
	}
	jitter := time.Duration(rand.Int63n(int64(d)/10 + 1))
	return d + jitter
}
