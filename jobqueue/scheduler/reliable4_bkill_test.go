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

// These are the reliable4 FINDING 4 regression tests: killExcessCmds used to hand
// bkill ONE unbounded argv (~1,900 element ids measured on the live production
// manager), with no timeout and no context, blindly re-issuing the identical
// failing kill on every scheduling cycle, and logging the whole id list (75KB/min
// of pure `toKill=` text, 116 warnings and a 627KB manager log in 30 minutes).
// See .docs/reliable4/prod-run-20260817.md FINDING 4 and DEVELOPERS.md rules 7
// (cap+time-bound+back off what you hand external tools) and 8 (use the house
// backoff package).
//
// They need no real LSF: bjobs and bkill are tiny fake exes (the mock-exe pattern
// of scheduler_lsf_test.go), so the whole prod-scale shape (1,900 excess elements)
// runs in the main suite.

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	. "github.com/smartystreets/goconvey/convey"
)

const (
	// bkillTestElements is how many excess LSF array elements the fake bjobs
	// reports: the ~1,900 ids the live prod run measured in ONE bkill argv.
	bkillTestElements = 1900

	// bkillTestPrefix is the job-name prefix the fake bjobs output carries, and
	// which killExcessCmds is asked to act on.
	bkillTestPrefix = "wrd_"

	// bkillTestJobID is the LSF array job id whose elements the fake bjobs
	// reports (as prod's bjobs -w does: a bare JOBID column and an
	// [index]-suffixed JOB_NAME).
	bkillTestJobID = "339466"

	// bkillTestBatch is the (lowered) per-bkill element cap the tests use, so
	// bkillTestElements needs several batches.
	bkillTestBatch = 500

	// bkillTestLogMax is the size a whole cycle's logging must stay under: the
	// old code logged the entire id list, which for bkillTestElements ids is
	// ~26KB in a single warn line.
	bkillTestLogMax = 2048

	// bkillAlreadyGoneBody is a fake bkill that reports every id as gone, which
	// is what prod's bkill did (exit status 255, "No matching job found").
	bkillAlreadyGoneBody = `for a in "$@"; do
  if [ "$a" = "-b" ]; then continue; fi
  echo "Job <$a>: No matching job found"
done
exit 255
`

	// bkillHalfKilledBody is a fake bkill that terminates the first half of the
	// ids it is given and reports the rest as already gone.
	bkillHalfKilledBody = `n=0
for a in "$@"; do
  if [ "$a" = "-b" ]; then continue; fi
  n=$((n+1))
done
half=$((n/2))
i=0
for a in "$@"; do
  if [ "$a" = "-b" ]; then continue; fi
  i=$((i+1))
  if [ "$i" -le "$half" ]; then
    echo "Job <$a> is being terminated"
  else
    echo "Job <$a>: No matching job found"
  fi
done
exit 255
`

	// bkillSilentSuccessBody is a fake bkill that accepts the kill request
	// without saying anything, as `bkill -b` can.
	bkillSilentSuccessBody = "exit 0\n"

	// bkillHangBody is a fake bkill that never responds in time, to exercise the
	// exec timeout.
	bkillHangBody = "sleep 10\nexit 0\n"

	// bkillProdShapeBody is a fake bkill that fails the whole batch with ONE
	// global message naming no element at all, which is exactly what prod's bkill
	// did (exit status 255, out="No matching job found" for ~1,900 ids; see
	// FINDING 4). wr cannot tell from that what happened to any individual
	// element, so every one of them must be reported as unaccounted for, and at
	// warn - the one thing that must never happen is silently assuming they were
	// killed (prod problem #3) or filing them as benignly already-gone.
	bkillProdShapeBody = "echo 'No matching job found'\nexit 255\n"
)

// bkillHarness is an *lsf wired to fake bjobs and bkill exes, so the real
// killExcessCmds path can be driven at prod scale without a real LSF.
type bkillHarness struct {
	s        *lsf
	ids      []string
	argvFile string
}

// newBkillHarness returns a harness whose fake bjobs reports the given number of
// non-RUN elements of one LSF array (so every one of them is excess, and
// killExcessCmds will want to kill them all), and whose fake bkill appends each
// invocation's argv as one line to the harness's argvFile before running the
// given shell body.
func newBkillHarness(t *testing.T, elements int, bkillBody string) *bkillHarness {
	t.Helper()

	dir := t.TempDir()

	var bjobs strings.Builder

	ids := make([]string, 0, elements)

	for i := 1; i <= elements; i++ {
		fmt.Fprintf(&bjobs, "%s sb10 PEND normal host1 host2 %sfakecmd.uniq[%d] Aug 18 15:36\n",
			bkillTestJobID, bkillTestPrefix, i)

		ids = append(ids, fmt.Sprintf("%s[%d]", bkillTestJobID, i))
	}

	bjobsOut := filepath.Join(dir, "bjobs.out")
	if err := os.WriteFile(bjobsOut, []byte(bjobs.String()), 0600); err != nil {
		t.Fatal(err)
	}

	bjobsExe := filepath.Join(dir, "bjobs")
	writeFakeExe(t, bjobsExe, "#!/bin/bash\ncat "+bjobsOut+"\n")

	argvFile := filepath.Join(dir, "bkill.argv")
	bkillExe := filepath.Join(dir, "bkill")
	writeFakeExe(t, bkillExe, "#!/bin/bash\nprintf '%s\\n' \"$*\" >> "+argvFile+"\n"+bkillBody)

	return &bkillHarness{
		s: &lsf{
			// killExcessCmds needs only the shell (to run bjobs) and the two exe
			// paths; the job-name prefix it acts on is passed in by the caller.
			config:   &ConfigLSF{Shell: testShell},
			bjobsExe: bjobsExe,
			bkillExe: bkillExe,
		},
		ids:      ids,
		argvFile: argvFile,
	}
}

// invocations returns the element ids handed to each fake bkill invocation, in
// invocation order (with the -b flag dropped).
func (h *bkillHarness) invocations(t *testing.T) [][]string {
	t.Helper()

	data, err := os.ReadFile(h.argvFile)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}

	if err != nil {
		t.Fatal(err)
	}

	var invocations [][]string

	for line := range strings.SplitSeq(strings.TrimSpace(string(data)), "\n") {
		if line == "" {
			continue
		}

		var ids []string

		for field := range strings.FieldsSeq(line) {
			if field == "-b" {
				continue
			}

			ids = append(ids, field)
		}

		invocations = append(invocations, ids)
	}

	return invocations
}

// TestReliable4BkillBounded covers FINDING 4 (a), (d) and the never-kill-reserved
// guarantee: one prod-scale (1,900 excess elements) kill cycle must be split into
// bounded bkill argvs that between them cover every excess element exactly once,
// must never hand bkill an element wr has handed a job reservation to, and must
// log a bounded summary rather than the whole id list.
func TestReliable4BkillBounded(t *testing.T) {
	Convey("Given ~1,900 excess LSF elements and a bkill that reports them all already gone", t, func() {
		h := newBkillHarness(t, bkillTestElements, bkillAlreadyGoneBody)
		setBkillTunables(t, time.Minute, 10*time.Minute, 30*time.Minute)

		ctx, logs := captureLogCtx()

		Convey("(a) the bkill argv is chunked, covering every excess element exactly once", func() {
			count, err := h.s.killExcessCmds(ctx, bkillTestPrefix, 0)
			So(err, ShouldBeNil)
			So(count, ShouldEqual, 0)

			invocations := h.invocations(t)
			biggest, total, seen := summariseInvocations(invocations)

			So(len(invocations), ShouldEqual, (bkillTestElements+bkillTestBatch-1)/bkillTestBatch)
			So(biggest, ShouldBeLessThanOrEqualTo, maxBkillBatchSize)
			So(total, ShouldEqual, bkillTestElements)
			So(len(seen), ShouldEqual, bkillTestElements)
			So(countNotSeenExactlyOnce(h.ids, seen), ShouldEqual, 0)
		})

		Convey("(d) the whole cycle logs a bounded summary, not the id list", func() {
			_, err := h.s.killExcessCmds(ctx, bkillTestPrefix, 0)
			So(err, ShouldBeNil)

			logged := logs.String()
			So(logs.Len(), ShouldBeLessThan, bkillTestLogMax)
			So(logged, ShouldContainSubstring, fmt.Sprintf("requested=%d", bkillTestElements))
			So(logged, ShouldContainSubstring, fmt.Sprintf("alreadyGone=%d", bkillTestElements))
			So(strings.Count(logged, bkillTestJobID+"["), ShouldBeLessThanOrEqualTo, bkillSummarySampleIDs)
		})

		Convey("elements wr has reserved are never handed to bkill, across any batch", func() {
			first, last := h.ids[0], h.ids[len(h.ids)-1]
			h.s.reserved(first)
			h.s.reserved(last)

			count, err := h.s.killExcessCmds(ctx, bkillTestPrefix, 0)
			So(err, ShouldBeNil)
			So(count, ShouldEqual, 2)

			invocations := h.invocations(t)
			_, total, seen := summariseInvocations(invocations)

			So(total, ShouldEqual, bkillTestElements-2)
			So(seen, ShouldNotContainKey, first)
			So(seen, ShouldNotContainKey, last)
		})
	})
}

// setBkillTunables lowers the kill-path package vars for the duration of the
// test (the per-bkill element cap to bkillTestBatch, so a prod-scale cycle needs
// several batches), restoring them afterwards.
func setBkillTunables(t *testing.T, execTimeout, backoffMin, backoffMax time.Duration) {
	t.Helper()

	origBatch, origTimeout := maxBkillBatchSize, bkillExecTimeout
	origMin, origMax := killBackoffMin, killBackoffMax

	maxBkillBatchSize, bkillExecTimeout = bkillTestBatch, execTimeout
	killBackoffMin, killBackoffMax = backoffMin, backoffMax

	t.Cleanup(func() {
		maxBkillBatchSize, bkillExecTimeout = origBatch, origTimeout
		killBackoffMin, killBackoffMax = origMin, origMax
	})
}

// summariseInvocations returns the largest number of ids in any one invocation,
// the total number of ids across all of them, and how many times each id was
// seen.
func summariseInvocations(invocations [][]string) (biggest, total int, seen map[string]int) {
	seen = make(map[string]int)

	for _, ids := range invocations {
		if len(ids) > biggest {
			biggest = len(ids)
		}

		total += len(ids)

		for _, id := range ids {
			seen[id]++
		}
	}

	return biggest, total, seen
}

// countNotSeenExactlyOnce returns how many of the given ids were not seen exactly
// once (so a single assertion covers thousands of ids).
func countNotSeenExactlyOnce(ids []string, seen map[string]int) int {
	bad := 0

	for _, id := range ids {
		if seen[id] != 1 {
			bad++
		}
	}

	return bad
}

// TestReliable4BkillDefaultCap pins the SHIPPED cap, not just a test-lowered one:
// at the prod-measured ~1,900 excess elements, the default splits the kill into
// batches of at most defaultMaxBkillBatchSize. Without this, the other tests would
// still pass if the shipped default were raised back to something unbounded.
func TestReliable4BkillDefaultCap(t *testing.T) {
	Convey("Given ~1,900 excess LSF elements and the default per-bkill cap", t, func() {
		h := newBkillHarness(t, bkillTestElements, bkillSilentSuccessBody)

		ctx, logs := captureLogCtx()

		Convey("no single bkill argv exceeds the shipped cap", func() {
			_, err := h.s.killExcessCmds(ctx, bkillTestPrefix, 0)
			So(err, ShouldBeNil)

			biggest, total, _ := summariseInvocations(h.invocations(t))

			So(maxBkillBatchSize, ShouldEqual, defaultMaxBkillBatchSize)
			So(biggest, ShouldBeLessThanOrEqualTo, defaultMaxBkillBatchSize)
			So(total, ShouldEqual, bkillTestElements)
			So(logs.Len(), ShouldBeLessThan, bkillTestLogMax)
		})
	})
}

// TestReliable4BkillTimeout covers FINDING 4 (b): each bkill runs under its own
// timeout, so a hung bkill is abandoned (and the rest of the cycle's batches left
// for the next cycle) instead of blocking excess-runner reclamation indefinitely.
func TestReliable4BkillTimeout(t *testing.T) {
	Convey("Given excess elements needing several batches and a bkill that hangs", t, func() {
		h := newBkillHarness(t, 3*bkillTestBatch, bkillHangBody)
		setBkillTunables(t, 300*time.Millisecond, 10*time.Minute, 30*time.Minute)

		ctx, logs := captureLogCtx()

		Convey("(b) killExcessCmds returns promptly and stops issuing further batches", func() {
			start := time.Now()
			count, err := h.s.killExcessCmds(ctx, bkillTestPrefix, 0)
			elapsed := time.Since(start)

			So(err, ShouldBeNil)
			So(count, ShouldEqual, 0)
			So(elapsed, ShouldBeLessThan, 5*time.Second)

			So(len(h.invocations(t)), ShouldEqual, 1)

			logged := logs.String()
			So(logged, ShouldContainSubstring, "timed out")
			So(logged, ShouldContainSubstring, fmt.Sprintf("abandoned=%d", 2*bkillTestBatch))
		})
	})
}

// TestReliable4BkillBackoff covers FINDING 4 (c): the identical failing kill is
// not re-issued on the next scheduling cycle (it is deferred by the house
// backoff), but the deferral always expires, so genuinely excess runners can
// never be stranded un-reclaimed.
func TestReliable4BkillBackoff(t *testing.T) {
	const elements = 20

	Convey("Given a failing bkill driven over two immediately consecutive cycles", t, func() {
		h := newBkillHarness(t, elements, bkillAlreadyGoneBody)
		setBkillTunables(t, time.Minute, 10*time.Minute, 30*time.Minute)

		ctx, _ := captureLogCtx()

		Convey("(c) the second cycle does not repeat the first cycle's argv", func() {
			_, err := h.s.killExcessCmds(ctx, bkillTestPrefix, 0)
			So(err, ShouldBeNil)
			_, err = h.s.killExcessCmds(ctx, bkillTestPrefix, 0)
			So(err, ShouldBeNil)

			invocations := h.invocations(t)
			So(len(invocations), ShouldEqual, 1)
			So(len(invocations[0]), ShouldEqual, elements)
		})
	})

	Convey("Given a failing bkill and a back-off that expires", t, func() {
		h := newBkillHarness(t, elements, bkillAlreadyGoneBody)
		setBkillTunables(t, time.Minute, 50*time.Millisecond, 5*time.Second)

		ctx, logs := captureLogCtx()

		Convey("the excess elements are eventually re-killed, so they cannot be stranded", func() {
			_, err := h.s.killExcessCmds(ctx, bkillTestPrefix, 0)
			So(err, ShouldBeNil)

			time.Sleep(200 * time.Millisecond)

			_, err = h.s.killExcessCmds(ctx, bkillTestPrefix, 0)
			So(err, ShouldBeNil)

			invocations := h.invocations(t)
			So(len(invocations), ShouldEqual, 2)
			So(len(invocations[1]), ShouldEqual, elements)
			So(strings.Join(invocations[1], " "), ShouldEqual, strings.Join(invocations[0], " "))
			So(logs.String(), ShouldContainSubstring, fmt.Sprintf("retried=%d", elements))
			// an element still reported as excess after wr already had it killed is
			// the reliable3 "lost slots never reclaimed" symptom, so it has to reach
			// an operator running at the default log level, not just at debug.
			So(logs.String(), ShouldContainSubstring, "lvl=warn")
		})
	})
}

// TestReliable4BkillOutcome covers FINDING 4 (e): the reported outcome
// distinguishes elements bkill actually killed from elements that were already
// gone, so a "No matching job found" can no longer hide un-reclaimed
// over-provisioned runners. That includes the output prod actually saw, which
// named no element at all: whatever wr cannot account for must be reported as
// unaccounted for, at warn, never quietly counted as killed or already gone.
func TestReliable4BkillOutcome(t *testing.T) {
	const elements = 10

	Convey("Given a bkill that terminates half the elements and reports the rest gone", t, func() {
		h := newBkillHarness(t, elements, bkillHalfKilledBody)
		setBkillTunables(t, time.Minute, 10*time.Minute, 30*time.Minute)

		ctx, logs := captureLogCtx()

		Convey("(e) the summary distinguishes killed from already-gone", func() {
			_, err := h.s.killExcessCmds(ctx, bkillTestPrefix, 0)
			So(err, ShouldBeNil)

			logged := logs.String()
			So(logged, ShouldContainSubstring, "killed=5")
			So(logged, ShouldContainSubstring, "alreadyGone=5")
			So(logged, ShouldContainSubstring, "unaccounted=0")
		})
	})

	Convey("Given a bkill that silently accepts every kill request", t, func() {
		h := newBkillHarness(t, elements, bkillSilentSuccessBody)
		setBkillTunables(t, time.Minute, 10*time.Minute, 30*time.Minute)

		ctx, logs := captureLogCtx()

		Convey("every element is reported killed, with nothing unaccounted for", func() {
			_, err := h.s.killExcessCmds(ctx, bkillTestPrefix, 0)
			So(err, ShouldBeNil)

			logged := logs.String()
			So(logged, ShouldContainSubstring, fmt.Sprintf("killed=%d", elements))
			So(logged, ShouldContainSubstring, "alreadyGone=0")
			So(logged, ShouldContainSubstring, "unaccounted=0")
		})
	})

	Convey("Given the bkill output prod really saw: one global failure naming no element", t, func() {
		h := newBkillHarness(t, bkillTestElements, bkillProdShapeBody)
		setBkillTunables(t, time.Minute, 10*time.Minute, 30*time.Minute)

		ctx, logs := captureLogCtx()

		Convey("no element is assumed killed or already gone, and the operator is warned", func() {
			_, err := h.s.killExcessCmds(ctx, bkillTestPrefix, 0)
			So(err, ShouldBeNil)

			logged := logs.String()
			So(logged, ShouldContainSubstring, "killed=0")
			So(logged, ShouldContainSubstring, "alreadyGone=0")
			So(logged, ShouldContainSubstring, fmt.Sprintf("unaccounted=%d", bkillTestElements))
			So(logged, ShouldContainSubstring, "lvl=warn")
			So(logs.Len(), ShouldBeLessThan, bkillTestLogMax)
		})
	})
}
