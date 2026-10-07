// Package stats collects latency samples and prints percentiles.
package stats

import (
	"fmt"
	"slices"
	"sync"
	"time"
)

// Lat is a concurrency-safe set of latency samples.
type Lat struct {
	mu sync.Mutex
	d  []time.Duration
}

// New returns an empty Lat.
func New() *Lat { return &Lat{} }

// Add records one sample.
func (l *Lat) Add(d time.Duration) {
	l.mu.Lock()
	l.d = append(l.d, d)
	l.mu.Unlock()
}

// N is the number of samples.
func (l *Lat) N() int {
	l.mu.Lock()
	defer l.mu.Unlock()

	return len(l.d)
}

// P returns the p-th quantile (0..1).
func (l *Lat) P(p float64) time.Duration {
	l.mu.Lock()
	defer l.mu.Unlock()

	if len(l.d) == 0 {
		return 0
	}

	slices.Sort(l.d)

	return l.d[int(float64(len(l.d)-1)*p)]
}

// String gives p50/p99/max.
func (l *Lat) String() string {
	return fmt.Sprintf("p50=%-9s p99=%-9s max=%-9s", r(l.P(.5)), r(l.P(.99)), r(l.P(1)))
}

func r(d time.Duration) time.Duration {
	switch {
	case d > time.Second:
		return d.Round(time.Millisecond)
	case d > time.Millisecond:
		return d.Round(10 * time.Microsecond)
	default:
		return d.Round(time.Microsecond)
	}
}
