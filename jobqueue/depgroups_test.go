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

package jobqueue

import (
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	. "github.com/smartystreets/goconvey/convey"
)

const (
	dgGroup1 = "g1"
	dgGroup2 = "g2"
	dgGroup3 = "g3"
	dgKey1   = "k1"
	dgKey2   = "k2"
	dgOldKey = "old-key"
	dgNewKey = "new-key"

	// the concurrent add/remove churn: 8 goroutines each adding and removing 500
	// distinct job keys across 50 shared groups for 200 iterations, each add
	// naming dgChurnGroupsPerAdd groups and each goroutine ordering its group
	// list differently - roughly 1.6M sharded operations.
	dgChurnWorkers      = 8
	dgChurnKeys         = 500
	dgChurnGroups       = 50
	dgChurnIterations   = 200
	dgChurnGroupsPerAdd = 3

	// dgChurnDeadline is a hang detector, not a latency budget: it costs nothing
	// when the test passes, and is sized against the churn workload above on a
	// shared node at load average 85-120 on 8 cores. If it ever fires
	// spuriously, the answer is a larger bound.
	dgChurnDeadline = 2 * time.Minute

	// the opposing-rekey race: 4 goroutines rekeying a -> b against 4 rekeying
	// b -> a, 500 iterations each. dgRekeyDeadline is a hang detector, not a
	// latency budget: it costs nothing when the test passes, and only fires when
	// the two job-key shards were locked in call order rather than in ascending
	// shard index. If it ever fires spuriously, the answer is a larger bound.
	dgRekeyWorkers    = 4
	dgRekeyIterations = 500
	dgRekeyDeadline   = 10 * time.Second

	// dgShardSearchLimit bounds the search for a second job key in a different
	// job-key shard from the first.
	dgShardSearchLimit = 100

	// dgJobKeyLength is how many hex characters a job key has.
	dgJobKeyLength = 32
)

// TestDepGroupMembers covers the per-group live-member sets that dependency
// resolution answers "has this dep group a live member job?" from.
func TestDepGroupMembers(t *testing.T) {
	if runnermode || servermode {
		return
	}

	Convey("Given a fresh depGroupMembers", t, func() {
		m := newDepGroupMembers()

		Convey("Adding a job to two groups makes both have members", func() {
			m.add([]string{dgGroup1, dgGroup2}, dgKey1)

			So(m.hasMembers(dgGroup1), ShouldBeTrue)
			So(m.hasMembers(dgGroup2), ShouldBeTrue)
			So(m.hasMembers(dgGroup3), ShouldBeFalse)
			So(m.memberships(), ShouldEqual, 2)
		})

		Convey("Adding the same job to the same group repeatedly leaves one membership", func() {
			m.add([]string{dgGroup1}, dgKey1)
			m.add([]string{dgGroup1}, dgKey1)
			m.add([]string{dgGroup1}, dgKey1)

			So(m.memberships(), ShouldEqual, 1)
			So(m.remove(dgKey1), ShouldResemble, []string{dgGroup1})
			So(m.remove(dgKey1), ShouldBeEmpty)
			So(m.memberships(), ShouldEqual, 0)
		})

		Convey("A group only empties when its last member goes", func() {
			m.add([]string{dgGroup1}, dgKey1)
			m.add([]string{dgGroup1}, dgKey2)

			So(m.remove(dgKey1), ShouldBeEmpty)
			So(m.hasMembers(dgGroup1), ShouldBeTrue)

			So(m.remove(dgKey2), ShouldResemble, []string{dgGroup1})
			So(m.hasMembers(dgGroup1), ShouldBeFalse)
		})

		Convey("Replacing a job's groups reports only the ones it left empty", func() {
			m.add([]string{dgGroup1, dgGroup2}, dgKey1)

			So(m.replace(dgKey1, []string{dgGroup2, dgGroup3}), ShouldResemble, []string{dgGroup1})
			So(m.hasMembers(dgGroup1), ShouldBeFalse)
			So(m.hasMembers(dgGroup2), ShouldBeTrue)
			So(m.hasMembers(dgGroup3), ShouldBeTrue)
			So(m.memberships(), ShouldEqual, 2)
		})

		Convey("Replacing a job's groups with the same groups changes nothing", func() {
			m.add([]string{dgGroup1}, dgKey1)

			So(m.replace(dgKey1, []string{dgGroup1}), ShouldBeEmpty)
			So(m.memberships(), ShouldEqual, 1)
		})

		Convey("Replacing a job's groups with none empties them", func() {
			m.add([]string{dgGroup1}, dgKey1)

			So(m.replace(dgKey1, nil), ShouldResemble, []string{dgGroup1})
			So(m.memberships(), ShouldEqual, 0)
		})

		Convey("Rekeying a group's only member does not empty the group", func() {
			m.add([]string{dgGroup1}, dgOldKey)

			// an implementation that drops the old key first reports g1 emptied
			// here, and so releases the group's waiters while its member is only
			// being renamed.
			So(m.rekey(dgOldKey, dgNewKey, []string{dgGroup1}), ShouldBeEmpty)
			So(m.hasMembers(dgGroup1), ShouldBeTrue)
			So(m.memberships(), ShouldEqual, 1)
			So(m.remove(dgOldKey), ShouldBeEmpty)
		})

		Convey("Rekeying to a different group list reports the groups left behind", func() {
			m.add([]string{dgGroup1, dgGroup2}, dgOldKey)

			So(m.rekey(dgOldKey, dgNewKey, []string{dgGroup2, dgGroup3}), ShouldResemble, []string{dgGroup1})
			So(m.hasMembers(dgGroup1), ShouldBeFalse)
			So(m.hasMembers(dgGroup2), ShouldBeTrue)
			So(m.hasMembers(dgGroup3), ShouldBeTrue)
			So(m.memberships(), ShouldEqual, 2)
		})

		Convey("Rekeying a job to its own key behaves as a replace", func() {
			m.add([]string{dgGroup1}, dgKey1)

			So(m.rekey(dgKey1, dgKey1, []string{dgGroup1}), ShouldBeEmpty)
			So(m.memberships(), ShouldEqual, 1)
		})
	})

	Convey("A dep group's dependency key is its prefixed name, which no job key can be", t, func() {
		So(depGroupDependencyKey(dgGroup1), ShouldEqual, "depgroup:g1")

		jobKey := (&Job{Cmd: webiEcho1}).Key()
		So(jobKey, ShouldHaveLength, dgJobKeyLength)
		So(strings.HasPrefix(jobKey, depGroupDependencyPrefix), ShouldBeFalse)
	})
}

// TestDepGroupMembersConcurrent proves the locking rules that only concurrency
// can break. Both Conveys fail by HANGING rather than by asserting, so each
// bounds its wait and fails on the deadline branch naming the deadlock; without
// that the failure is go test's 10-minute package-wide panic instead.
func TestDepGroupMembersConcurrent(t *testing.T) {
	if runnermode || servermode {
		return
	}

	Convey("Concurrent adds and removes over shared groups neither deadlock nor leak", t, func() {
		m := newDepGroupMembers()

		var wg sync.WaitGroup

		for worker := range dgChurnWorkers {
			wg.Add(1)

			go func() {
				defer wg.Done()

				dgChurn(m, worker)
			}()
		}

		// a deadlock here means more than one group-name shard was held at once,
		// or that a job-key shard was taken while a group-name shard was held.
		So(dgWaitFor(&wg, dgChurnDeadline), ShouldBeTrue)
		So(m.memberships(), ShouldEqual, 0)
		So(dgGroupsWithMembers(m), ShouldEqual, 0)
	})

	Convey("Opposing concurrent rekeys neither deadlock nor lose the group's member", t, func() {
		m := newDepGroupMembers()

		ka, kb := dgKeysInDifferentShards()
		So(depGroupShardIndex(ka), ShouldNotEqual, depGroupShardIndex(kb))

		m.add([]string{dgGroup1}, ka)

		var (
			wg       sync.WaitGroup
			returned atomic.Int64
		)

		for worker := range dgRekeyWorkers * 2 {
			wg.Add(1)

			go func() {
				defer wg.Done()

				from, to := ka, kb
				if worker%2 == 1 {
					from, to = kb, ka
				}

				for range dgRekeyIterations {
					m.rekey(from, to, []string{dgGroup1})
				}

				returned.Add(1)
			}()
		}

		// a deadlock here means the two job-key shards were locked in call order
		// rather than in ascending shard index.
		So(dgWaitFor(&wg, dgRekeyDeadline), ShouldBeTrue)
		So(returned.Load(), ShouldEqual, int64(dgRekeyWorkers*2))
		So(m.memberships(), ShouldEqual, 1)
		So(m.hasMembers(dgGroup1), ShouldBeTrue)
	})
}

// dgChurn adds and removes this worker's own job keys across the shared groups,
// naming several groups per add in an order no other worker uses.
func dgChurn(m *depGroupMembers, worker int) {
	keys := make([]string, 0, dgChurnKeys)

	for i := range dgChurnKeys {
		keys = append(keys, "dgchurn-"+strconv.Itoa(worker)+"-"+strconv.Itoa(i))
	}

	windows := dgChurnWindows(worker)

	for range dgChurnIterations {
		for i, key := range keys {
			m.add(windows[i%len(windows)], key)
			m.remove(key)
		}
	}
}

// dgChurnWindows returns the group lists this worker's adds name, drawn from the
// shared group names in an ordering unique to the worker: concurrent adds naming
// the same groups in different orders is what deadlocks an implementation that
// holds more than one group-name shard at a time.
func dgChurnWindows(worker int) [][]string {
	order := make([]string, 0, dgChurnGroups)

	for i := range dgChurnGroups {
		name := (i + worker*dgChurnGroupsPerAdd) % dgChurnGroups
		order = append(order, dgChurnGroupName(name))
	}

	if worker%2 == 1 {
		slices.Reverse(order)
	}

	windows := make([][]string, 0, dgChurnGroups)

	for i := range dgChurnGroups {
		window := make([]string, 0, dgChurnGroupsPerAdd)

		for j := range dgChurnGroupsPerAdd {
			window = append(window, order[(i+j)%dgChurnGroups])
		}

		windows = append(windows, window)
	}

	return windows
}

// dgWaitFor reports whether wg finished within deadline. A false return is a
// deadlock, named by the assertion that reads it.
func dgWaitFor(wg *sync.WaitGroup, deadline time.Duration) bool {
	done := make(chan struct{})

	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		return true
	case <-time.After(deadline):
		return false
	}
}

// dgGroupsWithMembers counts how many of the churn's shared groups still report
// having a live member.
func dgGroupsWithMembers(m *depGroupMembers) int {
	with := 0

	for i := range dgChurnGroups {
		if m.hasMembers(dgChurnGroupName(i)) {
			with++
		}
	}

	return with
}

func dgChurnGroupName(i int) string {
	return "dgchurn-group-" + strconv.Itoa(i)
}

// dgKeysInDifferentShards returns two real job keys that fall in different
// job-key shards, so that opposing rekeys between them contend for two shards.
func dgKeysInDifferentShards() (string, string) {
	first := (&Job{Cmd: "echo depgroups rekey a"}).Key()

	for i := range dgShardSearchLimit {
		second := (&Job{Cmd: "echo depgroups rekey b " + strconv.Itoa(i)}).Key()
		if depGroupShardIndex(second) != depGroupShardIndex(first) {
			return first, second
		}
	}

	return first, first
}
