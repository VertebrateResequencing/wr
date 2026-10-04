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

package scheduler

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	. "github.com/smartystreets/goconvey/convey"
)

// retentionLifecycle drives an lsf through runner lifecycles the way the manager
// does, against a fake `bjobs -w` that lists whatever the test last wrote. Each
// group is a different cmd: it is scheduled with perGroup PEND elements plus
// extras excess ones (which get doomed and bkilled), its perGroup runners claim
// reservations, and its final count-0 schedule still sees them RUN and the
// extras PEND. Then they all finish and that cmd's prefix is never scanned
// again, as for a finished scheduler group.
type retentionLifecycle struct {
	t        *testing.T
	s        *lsf
	listFile string
	perGroup int
	extras   int
	ctx      context.Context //nolint:containedctx
	req      *Requirements
	group    int
}

func newRetentionLifecycle(t *testing.T, perGroup, extras int) *retentionLifecycle {
	t.Helper()

	dir := t.TempDir()
	s := newFakeLSFScheduler(t, dir, filepath.Join(dir, "jargs"), fakeLSFDelays{})
	listFile := filepath.Join(dir, "bjobs-list")
	writeFakeExe(t, s.bjobsExe, "#!/bin/bash\ncat "+listFile+"\n")

	ctx, _ := captureLogCtx()

	r := &retentionLifecycle{
		t: t, s: s, listFile: listFile, perGroup: perGroup, extras: extras, ctx: ctx,
		req: &Requirements{RAM: 100, Time: time.Minute, Cores: 1, Other: map[string]string{}},
	}
	r.writeList(nil)

	return r
}

func (r *retentionLifecycle) writeList(lines []string) {
	r.t.Helper()

	if err := os.WriteFile(r.listFile, []byte(strings.Join(lines, "\n")+"\n"), 0600); err != nil {
		r.t.Fatal(err)
	}
}

// elementLines returns bjobs -w lines for indices from..to of the given group's
// array, in the given state.
func (r *retentionLifecycle) elementLines(group, from, to int, stat string) []string {
	prefix := jobName(fmt.Sprintf("retention-cmd-%d", group), "development", false)
	lines := make([]string, 0, to-from+1)

	for i := from; i <= to; i++ {
		lines = append(lines, fmt.Sprintf("%d sb10 %s normal host1 host2 %s_Xn0KpDLt[%d] Jul 22 12:00",
			retentionJobID(group), stat, prefix, i))
	}

	return lines
}

func retentionJobID(group int) int { return 1000000 + group }

// runGroups runs n more groups' whole lifecycles.
func (r *retentionLifecycle) runGroups(n int) {
	r.t.Helper()

	for range n {
		g := r.group
		r.group++
		cmd := fmt.Sprintf("retention-cmd-%d", g)
		pending := r.elementLines(g, 1, r.perGroup, "PEND")
		excess := r.elementLines(g, r.perGroup+1, r.perGroup+r.extras, "PEND")

		r.writeList(append(pending, excess...))

		if err := r.s.schedule(r.ctx, cmd, r.req, 0, r.perGroup); err != nil {
			r.t.Fatal(err)
		}

		for i := 1; i <= r.perGroup; i++ {
			if !r.s.claimForReserve(fmt.Sprintf("%d[%d]", retentionJobID(g), i)) {
				r.t.Fatalf("claim of group %d element %d refused", g, i)
			}
		}

		r.writeList(append(r.elementLines(g, 1, r.perGroup, "RUN"), excess...))

		if err := r.s.schedule(r.ctx, cmd, r.req, 0, 0); err != nil {
			r.t.Fatal(err)
		}

		r.writeList(nil)
	}
}

// scanIdle runs a count-0 scheduling pass of a cmd that has no jobs, as the
// manager does for some other scheduler group after the runners have finished.
func (r *retentionLifecycle) scanIdle() {
	r.t.Helper()

	if err := r.s.schedule(r.ctx, "retention-idle-cmd", r.req, 0, 0); err != nil {
		r.t.Fatal(err)
	}
}

// retainedElements returns how many reserved and doomed element ids the lsf
// holds.
func (r *retentionLifecycle) retainedElements() (reserved, doomed int) {
	r.s.reservedMu.Lock()
	defer r.s.reservedMu.Unlock()

	return len(r.s.reservedElements), r.s.doomedElements.len()
}

// TestLSFRetentionFinishedRunners covers the .docs/bugfixes/260927-queue-heap-retention.md
// follow-up: the element ids of runners that have finished, and of excess
// elements killed in a scheduler group that has since finished, must not
// accumulate over a long-lived manager. Before the fix, every runner that ever
// took a job stayed in reservedElements (about 75B each) until shutdown, and a
// finished group's last doomed ids (about 220B each) were never forgotten.
func TestLSFRetentionFinishedRunners(t *testing.T) {
	Convey("Given an lsf that has run many groups of runners that have all finished", t, func() {
		setReservedPruneInterval(0)

		r := newRetentionLifecycle(t, 50, 5)
		r.runGroups(20)

		Convey("a later scheduling pass forgets all their reserved and doomed element ids", func() {
			r.scanIdle()

			reserved, doomed := r.retainedElements()
			So(reserved, ShouldEqual, 0)
			So(doomed, ShouldEqual, 0)
		})
	})

	Convey("Given an lsf pruning no more often than a non-zero interval", t, func() {
		const interval = 200 * time.Millisecond

		setReservedPruneInterval(interval)

		r := newRetentionLifecycle(t, 50, 5)
		r.runGroups(5)

		Convey("a scheduling pass once that interval has passed forgets the finished runners", func() {
			time.Sleep(interval + 50*time.Millisecond)
			r.scanIdle()

			reserved, doomed := r.retainedElements()
			So(reserved, ShouldEqual, 0)
			So(doomed, ShouldEqual, 0)
		})
	})
}

// setReservedPruneInterval sets reservedPruneInterval for the rest of the
// calling Convey, restoring it afterwards.
func setReservedPruneInterval(d time.Duration) {
	orig := reservedPruneInterval
	reservedPruneInterval = d

	Reset(func() {
		reservedPruneInterval = orig
	})
}

// TestLSFRetentionPruneSparesLiveElements proves pruning never forgets an
// element whose runner may still be live, so killExcessCmds still never bkills
// an element wr has handed a job reservation to (DEVELOPERS.md rule 5), and a
// doomed element is still refused a reservation.
func TestLSFRetentionPruneSparesLiveElements(t *testing.T) {
	Convey("Given an lsf with a reserved element bjobs still reports as PEND", t, func() {
		setReservedPruneInterval(0)

		r := newRetentionLifecycle(t, 1, 0)
		argvFile := recordBkills(t, r.s, t.TempDir())

		reservedID := fmt.Sprintf("%d[1]", retentionJobID(0))
		So(r.s.claimForReserve(reservedID), ShouldBeTrue)

		r.writeList(r.elementLines(0, 1, 1, "PEND"))

		Convey("a pruning pass of another group keeps it, so its own group's excess kill spares it", func() {
			r.scanIdle()

			So(r.s.schedule(r.ctx, "retention-cmd-0", r.req, 0, 0), ShouldBeNil)
			So(bkilledIDs(t, argvFile), ShouldNotContain, reservedID)
			So(r.s.snapshotReserved(), ShouldContainKey, reservedID)
		})
	})

	Convey("Given an lsf with a reserved element whose pruning bjobs scan fails", t, func() {
		setReservedPruneInterval(0)

		r := newRetentionLifecycle(t, 1, 0)

		reservedID := fmt.Sprintf("%d[1]", retentionJobID(0))
		So(r.s.claimForReserve(reservedID), ShouldBeTrue)

		writeFakeExe(t, r.s.bjobsExe, "#!/bin/bash\nexit 1\n")

		Convey("the reservation is kept, since that scan is no picture of LSF", func() {
			So(r.s.schedule(r.ctx, "retention-idle-cmd", r.req, 0, 0), ShouldNotBeNil)
			So(r.s.snapshotReserved(), ShouldContainKey, reservedID)
		})
	})

	Convey("Given an lsf whose pruning bjobs scan is running", t, func() {
		setReservedPruneInterval(0)

		dir := t.TempDir()
		started, proceed := filepath.Join(dir, "bjobs.started"), filepath.Join(dir, "bjobs.proceed")

		r := newRetentionLifecycle(t, 1, 0)
		argvFile := recordBkills(t, r.s, dir)
		writeFakeExe(t, r.s.bjobsExe, "#!/bin/bash\ntouch "+started+"\n"+
			"for i in $(seq 1 200); do [ -e "+proceed+" ] && break; sleep 0.05; done\n"+
			"cat "+r.listFile+"\n")

		claimedID := fmt.Sprintf("%d[1]", retentionJobID(0))
		doomedID := fmt.Sprintf("%d[2]", retentionJobID(0))
		prefix := jobName("retention-cmd-0", "development", false)

		// elements that start (or are doomed by another group's scan) while this
		// scan runs may postdate its snapshot of LSF, so its not reporting them
		// says nothing about whether they have finished.
		done := make(chan bool, 1)

		go func() {
			if !waitForFile(started, 10*time.Second) {
				done <- false

				return
			}

			ok := r.s.claimForReserve(claimedID)

			r.s.doomUnreserved(prefix, []string{doomedID}, nil, false)

			if err := os.WriteFile(proceed, nil, 0600); err != nil {
				ok = false
			}

			done <- ok
		}()

		r.scanIdle()
		So(<-done, ShouldBeTrue)

		Convey("the element reserved during the scan is not bkilled by its group's next excess kill", func() {
			writeFakeExe(t, r.s.bjobsExe, "#!/bin/bash\ncat "+r.listFile+"\n")
			r.writeList(r.elementLines(0, 1, 1, "PEND"))

			So(r.s.schedule(r.ctx, "retention-cmd-0", r.req, 0, 0), ShouldBeNil)
			So(bkilledIDs(t, argvFile), ShouldNotContain, claimedID)
		})

		Convey("the element doomed during the scan is still refused a reservation", func() {
			So(r.s.claimForReserve(doomedID), ShouldBeFalse)
		})
	})
}

// recordBkills makes the lsf's fake bkill record its argv in a file in dir,
// returning that file's path (see bkilledIDs).
func recordBkills(t *testing.T, s *lsf, dir string) string {
	t.Helper()

	argvFile := filepath.Join(dir, "bkill.argv")
	writeFakeExe(t, s.bkillExe, "#!/bin/bash\nprintf '%s\\n' \"$*\" >> "+argvFile+"\n")

	return argvFile
}
