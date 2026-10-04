/*******************************************************************************
 * Copyright (c) 2019-2021, 2024-2026 Genome Research Ltd.
 *
 * Author: Sendu Bala <sb10@sanger.ac.uk>
 * Author: Ashwini Chhipa <ac55@sanger.ac.uk>
 * Author: Michael Woolnough <mw31@sanger.ac.uk>
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

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	. "github.com/smartystreets/goconvey/convey"
)

func TestLimiterAllocs(t *testing.T) {
	ctx := context.Background()

	Convey("Given a Limiter whose callback allocates nothing", t, func() {
		limit := NewCountGroupData(2)
		l := New(func(context.Context, string) *GroupData { return limit })
		groups := []string{"l1", "l2"}

		Convey("Increment, Decrement and capacity checks of groups in memory allocate nothing", func() {
			So(l.Increment(ctx, groups), ShouldBeTrue)

			allocs := testing.AllocsPerRun(100, func() {
				l.Increment(ctx, groups)
				l.GetRemainingCapacity(ctx, groups)
				l.Decrement(groups)
			})
			So(allocs, ShouldEqual, 0)
		})

		Convey("Increment of groups it must look up allocates only the new groups", func() {
			allocs := testing.AllocsPerRun(100, func() {
				l.Increment(ctx, groups)
				l.Decrement(groups)
			})
			So(allocs, ShouldEqual, float64(len(groups)))
		})
	})
}

func TestLimiterManyGroups(t *testing.T) {
	ctx := context.Background()

	Convey("Given a Limiter that must look up the limits of more than 8 groups", t, func() {
		groups := make([]string, 10)
		limits := make(map[string]int64, len(groups))

		for i := range groups {
			groups[i] = fmt.Sprintf("g%d", i)
			limits[groups[i]] = int64(3 + i)
		}

		l := New(func(_ context.Context, name string) *GroupData {
			return NewCountGroupData(limits[name])
		})

		So(l.Increment(ctx, groups[:5]), ShouldBeTrue)

		Convey("Increment, capacity and Decrement work across all of them", func() {
			So(l.Increment(ctx, groups), ShouldBeTrue)
			So(l.GetLowestLimit(ctx, groups), ShouldEqual, 3)
			So(l.GetRemainingCapacity(ctx, groups), ShouldEqual, 1)

			So(l.Increment(ctx, groups), ShouldBeTrue)
			So(l.GetRemainingCapacity(ctx, groups), ShouldEqual, 0)
			So(l.Increment(ctx, groups), ShouldBeFalse)

			l.Decrement(groups)
			So(l.GetRemainingCapacity(ctx, groups), ShouldEqual, 1)
			So(l.GetRemainingCapacity(ctx, groups[5:]), ShouldEqual, 7)
		})
	})
}

func TestLimiterDuplicateGroups(t *testing.T) {
	ctx := context.Background()

	Convey("Given a Limiter with a group limit of 5", t, func() {
		l := New(func(context.Context, string) *GroupData { return NewCountGroupData(5) })
		twice := []string{"a", "a"}

		test := func() {
			So(l.Increment(ctx, twice), ShouldBeTrue)
			So(l.GetRemainingCapacity(ctx, []string{"a"}), ShouldEqual, 3)

			l.Decrement(twice)
			So(l.GetLimits(), ShouldBeEmpty)
			So(l.GetRemainingCapacity(ctx, []string{"a"}), ShouldEqual, 5)
		}

		Convey("Incrementing a group named twice that must be looked up counts it twice", test)

		Convey("Incrementing a group named twice that is in memory counts it twice", func() {
			So(l.Increment(ctx, []string{"a"}), ShouldBeTrue)
			So(l.Increment(ctx, twice), ShouldBeTrue)
			So(l.GetRemainingCapacity(ctx, []string{"a"}), ShouldEqual, 2)

			l.Decrement(twice)
			So(l.GetRemainingCapacity(ctx, []string{"a"}), ShouldEqual, 4)
		})
	})
}

func TestLimiterCountsGroupsWithoutALimit(t *testing.T) {
	ctx := context.Background()

	Convey("Given a Limiter whose callback knows a limit only for groups it has been told about", t, func() {
		const limited = "limited"

		limits := map[string]int64{limited: 3, "closed": 0}
		lookups := 0
		l := New(func(_ context.Context, name string) *GroupData {
			lookups++

			if limit, exists := limits[name]; exists {
				return NewCountGroupData(limit)
			}

			return NewCountGroupData(-1)
		})
		g := []string{"g"}

		Convey("A limit set while more than it are counted lets no more in until fewer than it remain", func() {
			for range 3 {
				So(l.Increment(ctx, g), ShouldBeTrue)
			}

			l.SetLimit("g", *NewCountGroupData(2))
			So(l.Increment(ctx, g), ShouldBeFalse)
			So(l.GetRemainingCapacity(ctx, g), ShouldEqual, 0)

			l.Decrement(g)
			So(l.Increment(ctx, g), ShouldBeFalse)

			l.Decrement(g)
			So(l.GetRemainingCapacity(ctx, g), ShouldEqual, 1)
			So(l.Increment(ctx, g), ShouldBeTrue)
			So(l.Increment(ctx, g), ShouldBeFalse)
		})

		Convey("Removing a limit and setting it again keeps the count", func() {
			l.SetLimit("g", *NewCountGroupData(2))
			So(l.Increment(ctx, g), ShouldBeTrue)
			So(l.Increment(ctx, g), ShouldBeTrue)

			l.RemoveLimit("g")
			So(l.GetLimits(), ShouldBeEmpty)
			So(l.GetLimit(ctx, "g"), ShouldResemble, NewCountGroupData(-1))
			So(l.GetLowestLimit(ctx, g), ShouldEqual, -1)
			So(l.GetRemainingCapacity(ctx, g), ShouldEqual, -1)

			l.SetLimit("g", *NewCountGroupData(2))
			So(l.Increment(ctx, g), ShouldBeFalse)

			Convey("and while it has no limit, more can be counted against the limit set later", func() {
				l.RemoveLimit("g")
				So(l.Increment(ctx, g), ShouldBeTrue)

				l.SetLimit("g", *NewCountGroupData(2))
				l.Decrement(g)
				So(l.Increment(ctx, g), ShouldBeFalse)

				l.Decrement(g)
				So(l.Increment(ctx, g), ShouldBeTrue)
			})
		})

		Convey("A counted group without a limit is reported as having none", func() {
			both := []string{limited, "g"}
			So(l.Increment(ctx, both), ShouldBeTrue)

			So(l.GetLimits(), ShouldResemble, map[string]int{limited: 3})
			So(l.GetLimit(ctx, "g"), ShouldResemble, NewCountGroupData(-1))
			So(l.GetLowestLimit(ctx, g), ShouldEqual, -1)
			So(l.GetLowestLimit(ctx, both), ShouldEqual, 3)
			So(l.GetRemainingCapacity(ctx, g), ShouldEqual, -1)
			So(l.GetRemainingCapacity(ctx, both), ShouldEqual, 2)
		})

		Convey("A group without a limit is forgotten once nothing is counted against it", func() {
			for i := range 100 {
				fresh := []string{fmt.Sprintf("run%d", i)}
				So(l.Increment(ctx, fresh), ShouldBeTrue)
				l.Decrement(fresh)
			}

			So(l.groups, ShouldBeEmpty)

			So(l.Increment(ctx, g), ShouldBeTrue)
			l.Decrement(g)

			limits["g"] = 1

			So(l.Increment(ctx, g), ShouldBeTrue)
			So(l.Increment(ctx, g), ShouldBeFalse)
		})

		Convey("An Increment that fails does not remember a group without a limit", func() {
			So(l.Increment(ctx, []string{"g", "closed"}), ShouldBeFalse)

			before := lookups
			limits["g"] = 1

			So(l.Increment(ctx, g), ShouldBeTrue)
			So(lookups, ShouldEqual, before+1)
			So(l.Increment(ctx, g), ShouldBeFalse)
		})
	})
}

// BenchmarkLimiterIncDecUnlimited is BenchmarkLimiterIncDec for groups that
// have no limit.
func BenchmarkLimiterIncDecUnlimited(b *testing.B) {
	ctx := context.Background()
	cb := func(context.Context, string) *GroupData { return NewCountGroupData(-1) }
	both := []string{"u1", "u2"}

	for b.Loop() {
		l := New(cb)

		for range 10 {
			l.Increment(ctx, both)
		}

		for range 10 {
			l.Decrement(both)
		}

		l.Increment(ctx, both)
		l.Decrement(both)
	}
}

// heldLookupDB stands in for the database a SetLimitCallback reads. Its first
// lookup reads the limit stored at the time and then waits until released
// before returning it, so a test can change the stored limit, and tell the
// Limiter, while that lookup is in progress.
type heldLookupDB struct {
	mu      sync.Mutex
	limit   int64
	once    sync.Once
	entered chan struct{}
	release chan struct{}
}

// newHeldLookupDB returns a heldLookupDB storing the given limit.
func newHeldLookupDB(limit int64) *heldLookupDB {
	return &heldLookupDB{
		limit:   limit,
		entered: make(chan struct{}),
		release: make(chan struct{}),
	}
}

// lookup is a SetLimitCallback that reads the stored limit.
func (db *heldLookupDB) lookup(context.Context, string) *GroupData {
	data := NewCountGroupData(db.stored())

	db.once.Do(func() {
		close(db.entered)
		<-db.release
	})

	return data
}

// stored returns the stored limit.
func (db *heldLookupDB) stored() int64 {
	db.mu.Lock()
	defer db.mu.Unlock()

	return db.limit
}

// store changes the stored limit.
func (db *heldLookupDB) store(limit int64) {
	db.mu.Lock()
	defer db.mu.Unlock()

	db.limit = limit
}

// changeDuringLookup runs op in its own goroutine, and once op's first lookup
// has read the stored limit, stores the new limit and calls change, as the
// manager does for `wr limit`, before letting the lookup return. It returns
// once op has.
func (db *heldLookupDB) changeDuringLookup(op func(), limit int64, change func()) {
	done := make(chan struct{})

	go func() {
		defer close(done)

		op()
	}()

	<-db.entered
	db.store(limit)
	change()
	close(db.release)
	<-done
}

func TestLimiterLimitChangedDuringLookup(t *testing.T) {
	ctx := context.Background()

	Convey("Given a Limiter whose lookup of a group's limit of 5 is in progress", t, func() {
		const (
			name       = "g"
			oldLimit   = 5
			setLimit   = 3
			noLimit    = -1
			increments = oldLimit + 1
		)

		g := []string{name}
		db := newHeldLookupDB(oldLimit)
		l := New(db.lookup)

		remove := func() { l.RemoveLimit(name) }
		set := func() { l.SetLimit(name, *NewCountGroupData(setLimit)) }

		Convey("removing the limit during a GetLimit leaves the group without a limit", func() {
			var got *GroupData

			db.changeDuringLookup(func() { got = l.GetLimit(ctx, name) }, noLimit, remove)

			So(got, ShouldResemble, NewCountGroupData(noLimit))
			So(l.GetLimit(ctx, name), ShouldResemble, NewCountGroupData(noLimit))
			So(l.GetLimits(), ShouldBeEmpty)

			incremented := 0

			for range increments {
				if l.Increment(ctx, g) {
					incremented++
				}
			}

			So(incremented, ShouldEqual, increments)
		})

		Convey("removing the limit during an Increment leaves the group without a limit", func() {
			var first bool

			db.changeDuringLookup(func() { first = l.Increment(ctx, g) }, noLimit, remove)

			So(first, ShouldBeTrue)
			So(l.GetLimit(ctx, name).LimitForDisplay(), ShouldEqual, noLimit)

			incremented := 1

			for range increments - 1 {
				if l.Increment(ctx, g) {
					incremented++
				}
			}

			So(incremented, ShouldEqual, increments)
		})

		Convey("setting a limit during a GetLimit leaves the group with that limit", func() {
			var got *GroupData

			db.changeDuringLookup(func() { got = l.GetLimit(ctx, name) }, setLimit, set)

			So(got, ShouldResemble, NewCountGroupData(setLimit))
			So(l.GetLimits(), ShouldResemble, map[string]int{name: setLimit})
		})

		Convey("setting a limit that is forgotten again during a GetLimit leaves the group with that limit", func() {
			var got *GroupData

			setAndForget := func() {
				set()
				l.Decrement(g)
			}

			db.changeDuringLookup(func() { got = l.GetLimit(ctx, name) }, setLimit, setAndForget)

			So(got, ShouldResemble, NewCountGroupData(setLimit))
			So(l.GetLimits(), ShouldResemble, map[string]int{name: setLimit})
		})

		Convey("setting a limit during an Increment counts it against that limit", func() {
			var first bool

			db.changeDuringLookup(func() { first = l.Increment(ctx, g) }, setLimit, set)

			So(first, ShouldBeTrue)
			So(l.GetLimit(ctx, name).LimitForDisplay(), ShouldEqual, setLimit)
			So(l.GetRemainingCapacity(ctx, g), ShouldEqual, setLimit-1)
		})
	})
}

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

			remaining := l.GetRemainingCapacity(ctx, both)
			if remaining == 0 {
				break
			}
		}

		for {
			l.Decrement(both)

			remaining := l.GetRemainingCapacity(ctx, both)
			if remaining == 5 {
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
			var (
				incs  uint64
				fails uint64
				wg    sync.WaitGroup
			)
			// Release all workers at once and have each hold its slot long
			// enough that every worker makes its single attempt before any
			// successful worker releases. Spawning in a loop without this lets
			// later workers run only after earlier ones have slept and
			// decremented under load, which changes how many increments succeed
			// (the 125/75 split below depends on all 200 contending together).
			start := make(chan struct{})
			ready := make(chan struct{}, 200)

			for i := range 200 {
				wg.Add(1)
				go func(i int) {
					defer wg.Done()

					ready <- struct{}{}

					<-start

					groups := []string{"l4", "l5"}
					if i%2 == 0 {
						groups = []string{"l5", "l4"}
					}

					if l.Increment(ctx, groups) {
						atomic.AddUint64(&incs, 1)
						time.Sleep(500 * time.Millisecond)
						l.Decrement(groups)

						return
					}

					atomic.AddUint64(&fails, 1)

					if atomic.LoadUint64(&fails) == 50 {
						l.SetLimit("l4", *NewCountGroupData(125))
					}
				}(i)
			}

			for range 200 {
				<-ready
			}

			close(start)
			wg.Wait()

			succeeded := atomic.LoadUint64(&incs)
			failed := atomic.LoadUint64(&fails)

			So(succeeded+failed, ShouldEqual, 200)
			So(succeeded, ShouldBeBetweenOrEqual, 100, 125)
			So(failed, ShouldBeBetweenOrEqual, 75, 100)
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
