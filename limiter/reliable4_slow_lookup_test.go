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

package limiter

// This file guards the behaviour of a Limiter whose SetLimitCallback is slow.
// In production the callback does a bolt read transaction, which stalls for the
// duration of a DB backup copy; the Limiter's lock is on the path of every job
// completion's Decrement(), so a stalled lookup of one group must not stop any
// other group's jobs completing (.docs/bugfixes/260827-1.md item 2).

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	. "github.com/smartystreets/goconvey/convey"
)

const (
	// stalledGroup is the group whose limit lookup never returns, and
	// otherLiveGroup an unrelated group that is already in memory with a live
	// count, as a running job's limit group is.
	stalledGroup   = "stalled"
	otherLiveGroup = "other"

	// unblockedWait is how long an operation that must not wait for a stalled
	// lookup is given to return. It is generous because these tests run on
	// heavily loaded machines, and the operations they time out on are blocked
	// for as long as the lookup stalls (ie. forever), not merely slow.
	unblockedWait = 5 * time.Second

	// limitedGroup is the group every worker of the concurrency test contends
	// for, and contendedLimit is its limit, and so the maximum number of them
	// that may hold it at once.
	limitedGroup   = "slowLimited"
	contendedLimit = 1

	// otherGroup is a second group that only the contender population
	// increments, and it is the group whose lookup is slow. Its limit is high
	// enough that it never blocks.
	otherGroup           = "slowOther"
	contendedSecondLimit = 2

	// churners increment limitedGroup alone and let go of it immediately, so
	// it is created and forgotten as fast as the Limiter allows: the maximum
	// number of concurrent attempts at incrementing it, and so the best chance
	// of a check that is not held under the same lock as its increment letting
	// two of them through.
	contendedChurners = 24

	// contenders increment limitedGroup together with otherGroup, so they
	// routinely see limitedGroup in memory and then have it forgotten by a
	// churner while their slow otherGroup lookup is in progress: the case
	// where a resolution loop that gave up after one pass would wrongly treat
	// limitedGroup as unlimited.
	contendedWorkers = 8
	contendedHold    = time.Millisecond

	// workers ask to wait for capacity, so that every release wakes all of
	// them at once and they attempt together.
	contendedWait     = 20 * time.Millisecond
	contendedDuration = time.Second
	contendedLookup   = 25 * time.Millisecond

	// waitedGroup is the limited group that TestReliable4LimiterWaitRelease's
	// waiter has to wait for, and unlimitedGroup is a second group in the same
	// call that has no limit, so it is never remembered and every attempt looks
	// it up again.
	waitedGroup    = "waited"
	unlimitedGroup = "unlimited"
	waitedLimit    = 1

	// gapLookup is which lookup of unlimitedGroup the waiter must not have
	// reached before it has been released: a second one means an attempt looked
	// a group up again after its check failed, so its notification is not
	// registered under the same lock hold as that check.
	gapLookup = 2

	// gapWait is how long the second lookup is given to show up, and waitedWait
	// how long the waiter waits for capacity. The latter must be comfortably
	// longer than the former, so that the waiter is still waiting when capacity
	// is freed.
	gapWait    = time.Second
	waitedWait = 5 * time.Second

	// contendedMinIncrements is how many successful Increment()s the
	// concurrency test needs to have observed for its result to mean anything.
	// It is far below the ~550,000 measured in contendedDuration on a machine
	// at a load average of 130, so that it does not become a load-sensitive
	// assertion.
	contendedMinIncrements = 10
)

func TestReliable4LimiterWaitRelease(t *testing.T) {
	ctx := context.Background()

	Convey("A Decrement racing a waiting Increment's failed attempt still releases it", t, func() {
		var lookups atomic.Int64

		atGap := make(chan struct{})
		gapDone := make(chan struct{})

		l := New(func(_ context.Context, name string) *GroupData {
			if name != unlimitedGroup {
				return NewCountGroupData(waitedLimit)
			}

			if lookups.Add(1) == gapLookup {
				close(atGap)

				<-gapDone
			}

			return NewCountGroupData(-1)
		})

		// fill waitedGroup to its limit without looking unlimitedGroup up, so
		// that the waiter's own attempts are the only lookups of it.
		l.SetLimit(waitedGroup, *NewCountGroupData(waitedLimit))
		So(l.Increment(ctx, []string{waitedGroup}), ShouldBeTrue)

		result := make(chan bool, 1)

		go func() {
			result <- l.Increment(ctx, []string{waitedGroup, unlimitedGroup}, waitedWait)
		}()

		// hold the free-up until the waiter has either registered for it (no
		// second lookup) or looked up again after its failed check (which is
		// exactly when a separately-registered notification is lost).
		select {
		case <-atGap:
		case <-time.After(gapWait):
		}

		l.Decrement([]string{waitedGroup})
		close(gapDone)

		So(<-result, ShouldBeTrue)
	})
}

// contentionResult records what the workers of TestReliable4LimiterSlowLimit
// observed.
type contentionResult struct {
	mu         sync.Mutex
	holders    int
	maxHolders int
	increments int
	lookups    int
}

// noteLookup records that the limit callback was called.
func (c *contentionResult) noteLookup() {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.lookups++
}

// acquired records that a worker's Increment() of limitedGroup succeeded,
// tracking the greatest number of workers ever holding it at once.
func (c *contentionResult) acquired() {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.holders++
	c.increments++

	if c.holders > c.maxHolders {
		c.maxHolders = c.holders
	}
}

// released records that a worker is about to Decrement().
func (c *contentionResult) released() {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.holders--
}

func TestReliable4LimiterSlowLimit(t *testing.T) {
	ctx := context.Background()

	Convey("Given a Limiter whose group-limit lookups are slow", t, func() {
		limits := map[string]int64{limitedGroup: contendedLimit, otherGroup: contendedSecondLimit}
		result := &contentionResult{}

		l := New(func(_ context.Context, name string) *GroupData {
			result.noteLookup()

			if name == otherGroup {
				time.Sleep(contendedLookup)
			}

			return NewCountGroupData(limits[name])
		})

		Convey("Concurrent Increment()s and Decrement()s never exceed the limit", func() {
			deadline := time.Now().Add(contendedDuration)

			var wg sync.WaitGroup

			for range contendedChurners {
				wg.Go(func() {
					hammer(ctx, l, []string{limitedGroup}, 0, deadline, result)
				})
			}

			for range contendedWorkers {
				wg.Go(func() {
					hammer(ctx, l, []string{limitedGroup, otherGroup}, contendedHold, deadline, result)
				})
			}

			wg.Wait()

			result.mu.Lock()
			defer result.mu.Unlock()

			So(result.increments, ShouldBeGreaterThan, contendedMinIncrements)
			So(result.lookups, ShouldBeGreaterThan, 1)
			So(result.maxHolders, ShouldEqual, contendedLimit)
		})
	})
}

// hammer repeatedly increments the given groups until the deadline, waiting
// contendedWait for capacity each time, recording each hold in result, and
// holding for hold each time.
func hammer(ctx context.Context, l *Limiter, groups []string, hold time.Duration,
	deadline time.Time, result *contentionResult,
) {
	for time.Now().Before(deadline) {
		if !l.Increment(ctx, groups, contendedWait) {
			continue
		}

		result.acquired()
		time.Sleep(hold)
		result.released()
		l.Decrement(groups)
	}
}

func TestReliable4LimiterSlowLookup(t *testing.T) {
	ctx := context.Background()

	Convey("Given a Limiter with a group whose limit lookup stalls", t, func() {
		var once sync.Once

		entered := make(chan struct{})
		release := make(chan struct{})

		l := New(func(_ context.Context, name string) *GroupData {
			if name == stalledGroup {
				once.Do(func() { close(entered) })

				<-release
			}

			return NewCountGroupData(contendedSecondLimit)
		})

		defer close(release)

		l.SetLimit(otherLiveGroup, *NewCountGroupData(contendedSecondLimit))
		So(l.Increment(ctx, []string{otherLiveGroup}), ShouldBeTrue)

		go l.Increment(ctx, []string{stalledGroup})

		<-entered

		Convey("Decrement of another group does not wait for the stalled lookup", func() {
			So(returnsWithin(unblockedWait, func() {
				l.Decrement([]string{otherLiveGroup})
			}), ShouldBeTrue)
		})

		Convey("Increment of another group does not wait for the stalled lookup", func() {
			// otherLiveGroup is already in memory so needs no lookup of its
			// own; "unknown" needs one, so it also proves that one group's
			// stalled lookup doesn't stop another group's lookup happening.
			for _, name := range []string{otherLiveGroup, "unknown"} {
				var incremented bool

				So(returnsWithin(unblockedWait, func() {
					incremented = l.Increment(ctx, []string{name})
				}), ShouldBeTrue)
				So(incremented, ShouldBeTrue)
			}
		})

		Convey("GetRemainingCapacity of another group does not wait for the stalled lookup", func() {
			capacity := -2

			So(returnsWithin(unblockedWait, func() {
				capacity = l.GetRemainingCapacity(ctx, []string{otherLiveGroup})
			}), ShouldBeTrue)
			So(capacity, ShouldEqual, contendedSecondLimit-1)
		})
	})
}

// returnsWithin reports whether fn returned within the given duration. It
// deliberately asserts only "did it return at all", not how long it took, so it
// is not sensitive to machine load.
func returnsWithin(d time.Duration, fn func()) bool {
	done := make(chan struct{})

	go func() {
		fn()
		close(done)
	}()

	select {
	case <-done:
		return true
	case <-time.After(d):
		return false
	}
}
