package ingest

import (
	"testing"
	"time"
)

func TestBackoff(t *testing.T) {
	base := time.Second
	max := 5 * time.Minute

	tests := []struct {
		name    string
		attempt int
		want    time.Duration // deterministic part, before jitter
	}{
		{"first retry", 0, time.Second},
		{"second retry", 1, 2 * time.Second},
		{"third retry", 2, 4 * time.Second},
		{"sixth retry", 5, 32 * time.Second},
		{"ninth retry hits cap", 9, 5 * time.Minute},
		{"way past cap stays capped", 50, 5 * time.Minute},
		{"absurd attempt does not overflow", 100000, 5 * time.Minute},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Backoff(tt.attempt, base, max)
			// jitter adds at most 10% on top of the deterministic wait
			lo, hi := tt.want, tt.want+tt.want/10+1
			if got < lo || got > hi {
				t.Errorf("Backoff(%d) = %v, want in [%v, %v]", tt.attempt, got, lo, hi)
			}
		})
	}
}
