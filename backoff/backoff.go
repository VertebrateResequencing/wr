/*******************************************************************************
 * Copyright (c) 2026 Genome Research Ltd.
 *
 * Author: Sendu Bala <sb10@sanger.ac.uk>
 *
 * Permission is hereby granted, free of charge, to any person obtaining
 * a copy of this software and associated documentation files (the
 * "Software"), to deal in the Software without restriction, including
 * without limitation the rights to use, copy, modify, merge, publish,
 * distribute, sublicense, and/or sell copies of the Software, and to
 * permit persons to whom the Software is furnished to do so, subject to
 * the following conditions:
 *
 * The above copyright notice and this permission notice shall be included
 * in all copies or substantial portions of the Software.
 *
 * THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND,
 * EXPRESS OR IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF
 * MERCHANTABILITY, FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT.
 * IN NO EVENT SHALL THE AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY
 * CLAIM, DAMAGES OR OTHER LIABILITY, WHETHER IN AN ACTION OF CONTRACT,
 * TORT OR OTHERWISE, ARISING FROM, OUT OF OR IN CONNECTION WITH THE
 * SOFTWARE OR THE USE OR OTHER DEALINGS IN THE SOFTWARE.
 ******************************************************************************/

// package backoff is used to implement waiting for increasing periods of time
// between attempts at doing something.
package backoff

import (
	"context"
	"math"
	"math/rand"
	"sync/atomic"
	"time"

	"github.com/VertebrateResequencing/wr/clog"
)

// Sleeper defines the Sleep method used by a Backoff.
type Sleeper interface {
	// Sleep sleeps for the given duration, stopping early if context is
	// cancelled.
	Sleep(ctx context.Context, d time.Duration)
}

// Backoff is used to sleep for increasing periods of time.
type Backoff struct {
	// Min is the minimum amount of time to sleep for.
	Min time.Duration

	// Max is the maximum amount of time to sleep for.
	Max time.Duration

	// Factor is the multiplying factor to apply to the time to sleep for on
	// each Sleep() call.
	Factor float64

	// Sleeper is an implementation of Sleeper, to determine how the sleep
	// actually happens.
	Sleeper Sleeper

	sleeps uint64 // number of Sleep() calls in a row.
}

// Sleep will sleep (using Sleeper.Sleep()) for Min on the first call,
// increasing the sleep duration by Factor up to Max on each subsequent call.
//
// Sleep times in between Min and Max are jittered so multiple Backoffs working
// at the same time don't all sleep for the same time periods.
//
// If the supplied context is cancelled, we stop sleeping early.
//
// Sleep durations are logged using the global context logger at debug level.
func (b *Backoff) Sleep(ctx context.Context) {
	d := b.duration()
	clog.Debug(ctx, "backoff", "sleep", d)
	b.Sleeper.Sleep(ctx, d)
}

// duration calculates the next amount of time we should Sleep() for.
func (b *Backoff) duration() time.Duration {
	sleeps := atomic.AddUint64(&b.sleeps, 1) - 1
	d := b.durationAfterSleeps(sleeps)
	d = b.jitter(d, sleeps)

	return b.durationWithinBounds(d)
}

// durationAfterSleeps calculates the duration we should sleep for after the
// given number of Sleep() calls.
func (b *Backoff) durationAfterSleeps(sleeps uint64) time.Duration {
	return time.Duration(float64(b.Min) * math.Pow(b.Factor, float64(sleeps)))
}

// jitter alters the given duration by subtracting a random amount of time from
// it (but not so it is less than the previous unjittered sleep time). If
// sleeps is 0 (there is no previous sleep time), applies no jitter, since
// that would violate Min.
func (b *Backoff) jitter(d time.Duration, sleeps uint64) time.Duration {
	if sleeps == 0 {
		return d
	}

	prev := b.durationAfterSleeps(sleeps - 1)

	return time.Duration((rand.Float64() * float64(d-prev)) + float64(prev)) // #nosec
}

// durationWithinBounds returns d but not less than Min and not more than Max.
func (b *Backoff) durationWithinBounds(d time.Duration) time.Duration {
	if d <= b.Min {
		return b.Min
	}

	if d > b.Max {
		return b.Max
	}

	return d
}

// Reset will cause the next Sleep() call to sleep for Min again.
func (b *Backoff) Reset() {
	atomic.StoreUint64(&b.sleeps, 0)
}
