// Copyright © 2019, 2021 Genome Research Limited
// Author: Sendu Bala <sb10@sanger.ac.uk>.
//
//  This file is part of wr.
//
//  wr is free software: you can redistribute it and/or modify
//  it under the terms of the GNU Lesser General Public License as published by
//  the Free Software Foundation, either version 3 of the License, or
//  (at your option) any later version.
//
//  wr is distributed in the hope that it will be useful,
//  but WITHOUT ANY WARRANTY; without even the implied warranty of
//  MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
//  GNU Lesser General Public License for more details.
//
//  You should have received a copy of the GNU Lesser General Public License
//  along with wr. If not, see <http://www.gnu.org/licenses/>.

package limiter

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	. "github.com/smartystreets/goconvey/convey"
)

// synctestConvey runs a single top-level Convey block inside its own synctest
// bubble, so the wait-time windows in the block resolve on a synthetic clock
// (instantly and deterministically) instead of depending on real wall-clock
// timing, which is flaky under heavy parallel-test load.
func synctestConvey(t *testing.T, desc string, action func()) {
	t.Helper()
	synctest.Test(t, func(t *testing.T) {
		Convey(desc, t, action)
	})
}

func BenchmarkLimiterIncDec(b *testing.B) {
	ctx := context.Background()
	limits := make(map[string]int64)
	limits["l1"] = 5
	limits["l2"] = 6
	cb := func(ctx context.Context, name string) *GroupData {
		if limit, exists := limits[name]; exists {
			return NewCountGroupData(limit)
		}

		return NewCountGroupData(-1)
	}
	both := []string{"l1", "l2"}
	first := []string{"l1"}
	b.ResetTimer()

	for n := 0; n < b.N; n++ {
		l := New(cb)
		l.Increment(ctx, both)
		l.Increment(ctx, both)
		l.Increment(ctx, both)
		l.Increment(ctx, both)
		l.Increment(ctx, both)
		l.Increment(ctx, both)
		l.Increment(ctx, both)
		l.Increment(ctx, both)
		l.Increment(ctx, both)
		l.Increment(ctx, both)
		l.Decrement(both)
		l.Decrement(both)
		l.Decrement(both)
		l.Decrement(both)
		l.Decrement(both)
		l.Decrement(both)

		l.Increment(ctx, first)
		l.Increment(ctx, first)
		l.Increment(ctx, first)
		l.Increment(ctx, first)
		l.Increment(ctx, first)
		l.Increment(ctx, first)
		l.Increment(ctx, first)
		l.Increment(ctx, first)
		l.Increment(ctx, first)
		l.Increment(ctx, first)
		l.Decrement(first)
		l.Decrement(first)
		l.Decrement(first)
		l.Decrement(first)
		l.Decrement(first)
		l.Decrement(first)
	}
}

func BenchmarkLimiterCapacity(b *testing.B) {
	ctx := context.Background()
	limits := make(map[string]int64)
	limits["l1"] = 5
	limits["l2"] = 6
	cb := func(ctx context.Context, name string) *GroupData {
		if limit, exists := limits[name]; exists {
			return NewCountGroupData(limit)
		}

		return NewCountGroupData(-1)
	}
	both := []string{"l1", "l2"}
	b.ResetTimer()

	for n := 0; n < b.N; n++ {
		l := New(cb)
		for {
			l.Increment(ctx, both)
			cap := l.GetRemainingCapacity(ctx, both)
			if cap == 0 {
				break
			}
		}
		for {
			l.Decrement(both)
			cap := l.GetRemainingCapacity(ctx, both)
			if cap == 5 {
				break
			}
		}
	}
}

func TestLimiter(t *testing.T) {
	ctx := context.Background()
	Convey("You can make a new Limiter with a limit defining callback", t, func() {
		limits := make(map[string]int64)
		limits["l1"] = 3
		limits["l2"] = 2
		limits["l4"] = 100
		limits["l5"] = 200
		cb := func(ctx context.Context, name string) *GroupData {
			if limit, exists := limits[name]; exists {
				return NewCountGroupData(limit)
			}

			return NewCountGroupData(-1)
		}

		l := New(cb)
		So(l, ShouldNotBeNil)

		Convey("Increment and Decrement work as expected", func() {
			So(l.Increment(ctx, []string{"l1", "l2"}), ShouldBeTrue)
			l.Decrement([]string{"l1", "l2"})

			So(l.Increment(ctx, []string{"l2"}), ShouldBeTrue)
			So(l.Increment(ctx, []string{"l2"}), ShouldBeTrue)
			So(l.Increment(ctx, []string{"l2"}), ShouldBeFalse)
			So(l.Increment(ctx, []string{"l1", "l2"}), ShouldBeFalse)
			l.Decrement([]string{"l1", "l2"})
			So(l.Increment(ctx, []string{"l1", "l2"}), ShouldBeTrue)
			l.Decrement([]string{"l2"})
			So(l.Increment(ctx, []string{"l1", "l2"}), ShouldBeTrue)

			So(l.Increment(ctx, []string{"l3"}), ShouldBeTrue)
			l.Decrement([]string{"l3"})
		})

		Convey("You can change limits with SetLimit(), and Decrement() forgets about unused groups", func() {
			groups := []string{"l1", "l2"}
			two := []string{"l2"}
			So(l.GetLowestLimit(ctx, groups), ShouldEqual, 2)
			So(l.GetRemainingCapacity(ctx, groups), ShouldEqual, 2)
			So(l.Increment(ctx, two), ShouldBeTrue)
			So(l.GetRemainingCapacity(ctx, groups), ShouldEqual, 1)
			So(l.Increment(ctx, two), ShouldBeTrue)
			So(l.GetRemainingCapacity(ctx, groups), ShouldEqual, 0)
			So(l.Increment(ctx, two), ShouldBeFalse)
			l.SetLimit("l2", *NewCountGroupData(3))
			So(l.GetLowestLimit(ctx, groups), ShouldEqual, 3)
			So(l.GetRemainingCapacity(ctx, groups), ShouldEqual, 1)
			So(l.Increment(ctx, two), ShouldBeTrue)
			So(l.GetRemainingCapacity(ctx, groups), ShouldEqual, 0)
			So(l.Increment(ctx, two), ShouldBeFalse)
			l.Decrement(two)
			So(l.GetRemainingCapacity(ctx, groups), ShouldEqual, 1)
			l.Decrement(two)
			So(l.GetRemainingCapacity(ctx, groups), ShouldEqual, 2)
			l.Decrement(two)
			// at this point l2 should have been forgotten about, which means
			// we forgot we set the limit to 3
			So(l.GetRemainingCapacity(ctx, groups), ShouldEqual, 2)
			l.Decrement(two) // doesn't panic or something
			So(l.GetLowestLimit(ctx, groups), ShouldEqual, 2)
			So(l.GetRemainingCapacity(ctx, groups), ShouldEqual, 2)
			So(l.Increment(ctx, two), ShouldBeTrue)
			So(l.Increment(ctx, two), ShouldBeTrue)
			So(l.GetRemainingCapacity(ctx, groups), ShouldEqual, 0)
			So(l.Increment(ctx, two), ShouldBeFalse)
			l.Decrement(two)
			l.Decrement(two)
			limits["l2"] = 3
			So(l.GetRemainingCapacity(ctx, groups), ShouldEqual, 3)
			So(l.Increment(ctx, two), ShouldBeTrue)
			So(l.GetLowestLimit(ctx, groups), ShouldEqual, 3)
			So(l.GetRemainingCapacity(ctx, groups), ShouldEqual, 2)
			So(l.Increment(ctx, two), ShouldBeTrue)
			So(l.Increment(ctx, two), ShouldBeTrue)
			So(l.Increment(ctx, two), ShouldBeFalse)
		})

		Convey("You can set multiple limits and then get them all", func() {
			l.SetLimit("l1", *NewCountGroupData(1))
			l.SetLimit("l2", *NewCountGroupData(2))
			lgs := l.GetLimits()
			So(lgs, ShouldResemble, map[string]int{"l1": 1, "l2": 2})
		})

		Convey("You can have limits of 0 and also RemoveLimit()s", func() {
			l.SetLimit("l2", *NewCountGroupData(0))
			So(l.Increment(ctx, []string{"l2"}), ShouldBeFalse)

			limits["l2"] = 0
			l.RemoveLimit("l2")
			So(l.Increment(ctx, []string{"l2"}), ShouldBeFalse)
			So(l.GetLimit(ctx, "l2"), ShouldResemble, NewCountGroupData(0))

			limits["l2"] = -1
			So(l.Increment(ctx, []string{"l2"}), ShouldBeFalse)
			So(l.GetLimit(ctx, "l2"), ShouldResemble, NewCountGroupData(0))

			l.RemoveLimit("l2")
			So(l.Increment(ctx, []string{"l2"}), ShouldBeTrue)
			So(l.Increment(ctx, []string{"l2"}), ShouldBeTrue)
			So(l.Increment(ctx, []string{"l2"}), ShouldBeTrue)
			So(l.Increment(ctx, []string{"l2"}), ShouldBeTrue)
			So(l.Increment(ctx, []string{"l2"}), ShouldBeTrue)
			So(l.Increment(ctx, []string{"l2"}), ShouldBeTrue)
			So(l.Increment(ctx, []string{"l2"}), ShouldBeTrue)
			So(l.Increment(ctx, []string{"l2"}), ShouldBeTrue)
			So(l.Increment(ctx, []string{"l2"}), ShouldBeTrue)
			So(l.GetLimit(ctx, "l2"), ShouldResemble, NewCountGroupData(-1))
		})

		Convey("Concurrent SetLimit(), Increment() and Decrement() work", func() {
			var incs uint64
			var fails uint64
			var wg sync.WaitGroup
			for i := 0; i < 200; i++ {
				wg.Add(1)
				go func(i int) {
					defer wg.Done()
					groups := []string{"l4", "l5"}
					if i%2 == 0 {
						groups = []string{"l5", "l4"}
					}
					if l.Increment(ctx, groups) {
						atomic.AddUint64(&incs, 1)
						time.Sleep(100 * time.Millisecond)
						l.Decrement(groups)
					} else {
						atomic.AddUint64(&fails, 1)
						if atomic.LoadUint64(&fails) == 50 {
							l.SetLimit("l4", *NewCountGroupData(125))
						}
					}
				}(i)
			}
			wg.Wait()

			So(atomic.LoadUint64(&incs), ShouldEqual, 125)
			So(atomic.LoadUint64(&fails), ShouldEqual, 75)
		})
	})

	Convey("You can make non-count Limiters", t, func() {
		l := New(func(ctx context.Context, name string) *GroupData {
			if _, gd := NameToGroupData(name); gd.IsValid() && !gd.IsCount() {
				return gd
			}

			return NewCountGroupData(-1)
		})
		So(l, ShouldNotBeNil)

		So(l.Increment(ctx, []string{"time<" + timeAdd(time.Hour)}), ShouldBeTrue)
		So(l.Increment(ctx, []string{"time<" + timeAdd(-time.Hour)}), ShouldBeFalse)
		So(l.Increment(ctx, []string{timeAdd(-time.Hour) + "<time"}), ShouldBeTrue)
		So(l.Increment(ctx, []string{timeAdd(time.Hour) + "<time"}), ShouldBeFalse)
		So(l.Increment(ctx, []string{timeAdd(time.Hour) + "<time<" + timeAdd(2*time.Hour)}), ShouldBeFalse)
		So(l.Increment(ctx, []string{timeAdd(-2*time.Hour) + "<time<" + timeAdd(-time.Hour)}), ShouldBeFalse)
		So(l.Increment(ctx, []string{timeAdd(-time.Hour) + "<time<" + timeAdd(time.Hour)}), ShouldBeTrue)
		So(l.Increment(ctx, []string{"datetime<" + dateAdd(time.Hour)}), ShouldBeTrue)
		So(l.Increment(ctx, []string{"datetime<" + dateAdd(-time.Hour)}), ShouldBeFalse)
		So(l.Increment(ctx, []string{dateAdd(-time.Hour) + "<datetime"}), ShouldBeTrue)
		So(l.Increment(ctx, []string{dateAdd(time.Hour) + "<datetime"}), ShouldBeFalse)
		So(l.Increment(ctx, []string{dateAdd(time.Hour) + "<datetime<" + dateAdd(2*time.Hour)}), ShouldBeFalse)
		So(l.Increment(ctx, []string{dateAdd(-2*time.Hour) + "<datetime<" + dateAdd(-time.Hour)}), ShouldBeFalse)
		So(l.Increment(ctx, []string{dateAdd(-time.Hour) + "<datetime<" + dateAdd(time.Hour)}), ShouldBeTrue)
	})

	// Runs under synctest's fake clock so the 35ms/50ms/60ms/125ms wait windows
	// resolve deterministically instead of depending on real wall-clock timing,
	// which is flaky under heavy parallel-test load (e.g. CI's 1-2 cpus running
	// many test lanes at once). Proves Increment()s blocked at the limit are
	// released as capacity frees - quickly when freed immediately, slowly when
	// freed later - and time out when it isn't freed within the wait.
	synctestConvey(t, "Concurrent Increment()s at the limit work with wait times", func() {
		l := New(func(ctx context.Context, name string) *GroupData {
			switch name {
			case "l1":
				return NewCountGroupData(3)
			case "l2":
				return NewCountGroupData(2)
			}

			return NewCountGroupData(-1)
		})

		groups := []string{"l1", "l2"}
		So(l.Increment(ctx, groups), ShouldBeTrue)
		So(l.Increment(ctx, groups), ShouldBeTrue)
		So(l.Increment(ctx, groups), ShouldBeFalse)

		start := time.Now()

		go func() {
			l.Decrement(groups)
			l.Decrement(groups)
			<-time.After(50 * time.Millisecond)
			l.Decrement(groups)
		}()

		go func() {
			<-time.After(60 * time.Millisecond)
			// (decrementing the higher capacity group doesn't make an
			// increment of the lower capacity group work)
			l.Decrement([]string{"l1"})
		}()

		var quickIncs, slowIncs, fails atomic.Uint64

		wait := 125 * time.Millisecond

		var wg sync.WaitGroup
		for range 4 {
			wg.Go(func() {
				if !l.Increment(ctx, groups, wait) {
					if time.Since(start) > 100*time.Millisecond {
						fails.Add(1)
					}

					return
				}

				if time.Since(start) < 35*time.Millisecond {
					quickIncs.Add(1)
				} else {
					slowIncs.Add(1)
				}
			})
		}

		wg.Wait()

		So(quickIncs.Load(), ShouldEqual, 2)
		So(slowIncs.Load(), ShouldEqual, 1)
		So(fails.Load(), ShouldEqual, 1)
	})
}

func timeAdd(add time.Duration) string {
	now := time.Now().Truncate(time.Second)
	dayStart := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	dayEnd := dayStart.Add(24*time.Hour - time.Second)

	if add > 0 && now.Equal(dayEnd) {
		time.Sleep(time.Second)
		now = time.Now().Truncate(time.Second)
		dayStart = time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
		dayEnd = dayStart.Add(24*time.Hour - time.Second)
	}

	if add < 0 && now.Equal(dayStart) {
		time.Sleep(time.Second)
		now = time.Now().Truncate(time.Second)
		dayStart = time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
		dayEnd = dayStart.Add(24*time.Hour - time.Second)
	}

	target := now.Add(add)

	if target.Before(dayStart) {
		target = dayStart
	}

	if target.After(dayEnd) {
		target = dayEnd
	}

	if add > 0 && !target.After(now) && now.Before(dayEnd) {
		target = now.Add(time.Second)
	}

	if add < 0 && !target.Before(now) && now.After(dayStart) {
		target = now.Add(-time.Second)
	}

	return target.Format(time.TimeOnly)
}

func dateAdd(add time.Duration) string {
	return time.Now().Add(add).Format(time.DateTime)
}
