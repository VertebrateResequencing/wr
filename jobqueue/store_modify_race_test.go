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

// This file covers spec F2: two concurrent Modify calls that change the keys
// of the same jobs persist both jobs.

import (
	"context"
	"fmt"
	"maps"
	"path/filepath"
	"slices"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	. "github.com/smartystreets/goconvey/convey"
)

const (
	// modifyRaceRounds is how many concurrent pairs of Modify calls F2 test 1
	// makes.
	modifyRaceRounds = 50

	// modifyRaceHold is the most time the first modify of a pair involving
	// the REST API waits at the hook for the second to return. The second can
	// only return while the first waits if the two are not mutually exclusive,
	// and a second blocked as it should be cannot be observed, so the wait is
	// bounded rather than certain.
	modifyRaceHold = 50 * time.Millisecond
)

// modifyRaceSignal is closed by the first modify of a pair to reach the hook,
// which then waits up to hold for released to be closed.
type modifyRaceSignal struct {
	once     sync.Once
	ch       chan struct{}
	hold     time.Duration
	released chan struct{}
}

// modifyRaceEntry signals, once per pair, that a modify has reached
// modifyAppliedHook.
type modifyRaceEntry struct {
	current atomic.Pointer[modifyRaceSignal]
}

// installModifyRaceEntry makes modifyAppliedHook signal a new modifyRaceEntry
// until the Convey block ends.
func installModifyRaceEntry() *modifyRaceEntry {
	entry := &modifyRaceEntry{}
	modifyAppliedHook = func() {
		sig := entry.current.Load()
		if sig == nil {
			return
		}

		first := false

		sig.once.Do(func() {
			close(sig.ch)

			first = true
		})

		// the wait is outside once.Do, which would otherwise block the other
		// modify at its own hook.
		if !first || sig.hold == 0 {
			return
		}

		select {
		case <-sig.released:
		case <-time.After(sig.hold):
		}
	}

	Reset(func() { modifyAppliedHook = nil })

	return entry
}

// next starts a new pair whose first modify to reach the hook waits there up
// to hold for the returned signal's released to be closed.
func (e *modifyRaceEntry) next(hold time.Duration) *modifyRaceSignal {
	sig := &modifyRaceSignal{ch: make(chan struct{}), hold: hold, released: make(chan struct{})}
	e.current.Store(sig)

	return sig
}

// TestStoreModifyRace covers spec F2 acceptance test 1: of two concurrent
// Modify calls that each change the keys of jobs A and B, neither loses a
// job's persist, so a restart recovers every job as it was in memory. The
// second call alternates between a client's and the REST API's, and in the
// REST rounds which of the two goes first alternates too, so both modify paths
// are raced from either side.
func TestStoreModifyRace(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	for _, kind := range []storeKind{storeKindLog, storeKindBolt} {
		Convey("Given a manager on a "+storeKindName(kind)+" store holding jobs A and B", t, func() {
			entry := installModifyRaceEntry()
			m := startReserveTestManager(t, ctx, kind, nil)
			keys := []string{m.add("modify race A"), m.add("modify race B")}
			cwdBase := t.TempDir()

			Convey("two concurrent Modify calls changing both their keys, each followed by a restart, "+
				"lose no persist", func() {
				var lost []string

				for round := range modifyRaceRounds {
					lost = append(lost, modifyRacePair(ctx, m, entry, round, keys, filepath.Join(cwdBase, strconv.Itoa(round)))...)

					inMemory := modifyRaceJobs(m)
					if len(inMemory) != len(keys) {
						lost = append(lost, fmt.Sprintf("round %d held %d jobs in memory", round, len(inMemory)))
					}

					m.restart(ctx, nil)

					lost = append(lost, modifyRaceLost(round, inMemory, modifyRaceJobs(m))...)
					keys = slices.Collect(maps.Keys(inMemory))
				}

				So(lost, ShouldBeEmpty)
			})
		})
	}
}

// modifyRaceJobs returns the command of every job in m's queue, by key.
func modifyRaceJobs(m *reserveTestManager) map[string]string {
	cmds := make(map[string]string)

	for _, item := range m.server.q.AllItems() {
		job, ok := item.Data().(*Job)
		if !ok {
			continue
		}

		job.RLock()
		cmds[job.Key()] = job.Cmd
		job.RUnlock()
	}

	return cmds
}

// modifyRaceLost returns a description of every job of inMemory that recovered
// does not hold with the same key and command, and of every job recovered that
// was not in memory.
func modifyRaceLost(round int, inMemory, recovered map[string]string) []string {
	var lost []string

	for key, cmd := range inMemory {
		got, ok := recovered[key]
		if !ok || got != cmd {
			lost = append(lost, fmt.Sprintf("round %d: %s (%q) recovered as %q", round, key, cmd, got))
		}
	}

	for key, cmd := range recovered {
		if _, ok := inMemory[key]; !ok {
			lost = append(lost, fmt.Sprintf("round %d: %s (%q) recovered but not in memory", round, key, cmd))
		}
	}

	return lost
}

// modifyRacePair has a client of m and, in even rounds, another client or, in
// odd rounds, the manager's REST modify concurrently modify the keyed jobs,
// each changing every job's key by giving it a cwd under base that matters. It
// returns a description of each call that failed once both have returned.
//
// In odd rounds one call goes first, the client's in rounds 1 mod 4 and the
// REST API's in rounds 3 mod 4, and the other starts once the first has
// changed the jobs in memory and is about to change their queue keys and
// persist them. The first then waits there until the other returns or for
// modifyRaceHold, so a modify that does not hold the lock from collecting its
// jobs through persisting them, on either path, has the other run whole in
// between. An unlock placed immediately after modifyAppliedHook is the one
// exception: nothing runs between that unlock and the key change, so the hold
// cannot observe it, and only a natural race in the microseconds before the
// first ChangeKey can catch it.
func modifyRacePair(ctx context.Context, m *reserveTestManager, entry *modifyRaceEntry, round int, keys []string,
	base string,
) []string {
	other, err := Connect(m.addr, m.serverConfig.CAFile, m.serverConfig.CertDomain, m.token, m.connectTime)
	if err != nil {
		return []string{"connect: " + err.Error()}
	}

	defer disconnect(other)

	jes := make([]*JobEssence, len(keys))
	for i, key := range keys {
		jes[i] = &JobEssence{JobKey: key}
	}

	modifiers := make([]*JobModifier, 2)
	for i := range modifiers {
		modifiers[i] = NewJobModifer()
		modifiers[i].SetCwd(filepath.Join(base, strconv.Itoa(i)))
		modifiers[i].SetCwdMatters(true)
	}

	var wg sync.WaitGroup

	errs := make([]error, len(modifiers))

	if round%2 == 0 {
		entry.next(0)

		wg.Go(func() { _, errs[0] = m.jq.Modify(jes, modifiers[0]) })
		wg.Go(func() { _, errs[1] = other.Modify(jes, modifiers[1]) })
		wg.Wait()

		return modifyRaceFailed(errs)
	}

	calls := []func(){
		func() { _, errs[0] = m.jq.Modify(jes, modifiers[0]) },
		func() { _, errs[1] = m.server.modifyJobsByKeys(ctx, keys, modifiers[1]) },
	}

	if round%4 == 3 {
		calls[0], calls[1] = calls[1], calls[0]
	}

	sig := entry.next(modifyRaceHold)

	wg.Go(calls[0])
	wg.Go(func() {
		defer close(sig.released)

		// started together, the in-process call would be over before the
		// client's request reached the manager, so the second starts only once
		// the first is about to change its jobs' queue keys.
		select {
		case <-sig.ch:
		case <-time.After(reserveTestWait):
		}

		calls[1]()
	})

	wg.Wait()

	return modifyRaceFailed(errs)
}

// modifyRaceFailed returns a description of each non-nil error in errs.
func modifyRaceFailed(errs []error) []string {
	var failed []string

	for _, err := range errs {
		if err != nil {
			failed = append(failed, "modify: "+err.Error())
		}
	}

	return failed
}
