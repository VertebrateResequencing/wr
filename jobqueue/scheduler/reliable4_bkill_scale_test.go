//go:build reliability_repro

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

// SCALE GATE reproducer for reliable4 FINDING 4 (see
// .docs/reliable4/prod-run-20260817.md): the excess-runner kill path used to hand
// bkill ONE unbounded argv, with no timeout, re-issuing the identical failing kill
// every scheduling cycle and logging the entire id list (prod: ~1,900 ids in one
// argv, 116 warnings and ~75KB/min of `toKill=` text).
//
// It is deliberately INDICATIVE rather than faithful: the farm's real bkill cannot
// be driven at this scale without killing real LSF jobs, so bjobs and bkill are
// fake exes (the mock-exe pattern of scheduler_lsf_test.go) while everything on the
// wr side - checkCmd's collector, the argv building, the exec, the back-off and the
// logging - is the real code path. It runs at the prod-measured element count, and
// it does NOT reference any of the post-fix package vars, so the same test file can
// be dropped into a pre-fix tree to produce the RED side of an A/B.
//
// It prints its measurements BEFORE asserting anything (a failed GoConvey
// assertion abandons the rest of its block), so a pre-fix run still reports real
// numbers rather than only a failure. developers/wrdev.sh bkill-hygiene parses
// those lines and treats a missing measurement as a hard FAIL.
//
// Run via developers/wrdev.sh bkill-hygiene, or directly:
//
//	go test -tags reliability_repro ./jobqueue/scheduler/ -run TestReliable4BkillHygieneScale -v

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	. "github.com/smartystreets/goconvey/convey"
)

const (
	// bkScaleDefaultElements is how many excess LSF array elements the gate drives
	// by default: the ~1,900 ids prod measured in ONE bkill argv.
	bkScaleDefaultElements = 1900

	// bkScaleDefaultHangSecs is how long the hanging fake bkill sleeps for. It must
	// exceed the shipped bkill exec timeout (1m), which is what the gate is
	// measuring the effect of.
	bkScaleDefaultHangSecs = 120

	// bkScaleShippedCap is the maximum number of element ids a single bkill argv
	// may contain (the shipped defaultMaxBkillBatchSize; hard-coded here so this
	// file compiles against a pre-fix tree too).
	bkScaleShippedCap = 1000

	// bkScaleLogMax is the number of bytes one kill cycle's logging must stay
	// under. Prod's single warn line was ~26KB of ids plus ~78KB of bkill output.
	bkScaleLogMax = 4096

	// bkScaleHangBoundMs is how long killExcessCmds may take when bkill hangs: the
	// shipped 1m exec timeout plus its 2s pipe grace, with margin for a loaded
	// farm node.
	bkScaleHangBoundMs = 90000

	// bkScaleOutcomeElements is how many elements the killed-vs-already-gone
	// measurement uses (half are reported terminated, half already gone).
	bkScaleOutcomeElements = 10

	// bkScaleHangElements is how many elements the hanging-bkill measurement uses:
	// enough to need a bkill, few enough to be one batch.
	bkScaleHangElements = 6

	// bkScaleJobID is the LSF array job id the fake bjobs reports elements of.
	bkScaleJobID = "339466"

	// bkScalePrefix is the job-name prefix the fake bjobs output carries.
	bkScalePrefix = "wrd_"
)

// bkScaleCycle is what one kill cycle cost.
type bkScaleCycle struct {
	invocations int
	maxArgv     int
	uniqueIDs   int
	logBytes    int
	repeatedIDs int
}

// bkScaleMeasureCycles drives two immediately consecutive kill cycles over the
// same excess elements, with a bkill that reports every element already gone (what
// prod's bkill did), and returns what each cycle cost.
func bkScaleMeasureCycles(t *testing.T, elements int) (bkScaleCycle, bkScaleCycle) {
	t.Helper()

	s, argvFile := bkScaleHarness(t, elements, `for a in "$@"; do
  if [ "$a" = "-b" ]; then continue; fi
  echo "Job <$a>: No matching job found"
done
exit 255
`)

	ctx, logs := captureLogCtx()

	if _, err := s.killExcessCmds(ctx, bkScalePrefix, 0); err != nil {
		t.Fatal(err)
	}

	first := bkScaleInvocations(t, argvFile)
	chunk, firstIDs := bkScaleSummarise(first)
	chunk.logBytes = logs.Len()

	t.Logf("BKILL-HYGIENE-CHUNK: elements=%d invocations=%d maxArgv=%d uniqueIDs=%d logBytes=%d",
		elements, chunk.invocations, chunk.maxArgv, chunk.uniqueIDs, chunk.logBytes)

	if _, err := s.killExcessCmds(ctx, bkScalePrefix, 0); err != nil {
		t.Fatal(err)
	}

	second := bkScaleInvocations(t, argvFile)[len(first):]
	repeat, secondIDs := bkScaleSummarise(second)
	repeat.repeatedIDs = bkScaleOverlap(secondIDs, firstIDs)

	t.Logf("BKILL-HYGIENE-REPEAT: cycle2Invocations=%d cycle2RepeatedIDs=%d",
		repeat.invocations, repeat.repeatedIDs)

	return chunk, repeat
}

// bkScaleSummarise reduces a set of bkill invocations to the numbers the gate
// asserts on, plus the set of element ids they mentioned.
func bkScaleSummarise(invocations [][]string) (bkScaleCycle, map[string]bool) {
	cycle := bkScaleCycle{invocations: len(invocations)}
	seen := make(map[string]bool)

	for _, ids := range invocations {
		if len(ids) > cycle.maxArgv {
			cycle.maxArgv = len(ids)
		}

		for _, id := range ids {
			seen[id] = true
		}
	}

	cycle.uniqueIDs = len(seen)

	return cycle, seen
}

// TestReliable4BkillHygieneScale measures, at prod scale, the four things prod's
// diagnostics could not distinguish: how big a single bkill argv gets, how many
// bkills a cycle costs, whether an identical failing kill is re-issued on the next
// cycle, how many elements were actually killed versus already gone, and how many
// bytes of log a cycle emits. It also measures whether a hung bkill can block the
// kill path indefinitely.
func TestReliable4BkillHygieneScale(t *testing.T) {
	elements := bkScaleEnvInt("WR_BK_ELEMENTS", bkScaleDefaultElements)
	hangSecs := bkScaleEnvInt("WR_BK_HANG", bkScaleDefaultHangSecs)

	chunk, repeat := bkScaleMeasureCycles(t, elements)
	outcome := bkScaleMeasureOutcome(t)
	hangMs := bkScaleMeasureHang(t, hangSecs)

	Convey("At prod scale, the excess-runner kill path is bounded, backed off and summarised", t, func() {
		Convey("no single bkill argv exceeds the cap, and the batches cover every element once", func() {
			So(chunk.invocations, ShouldBeGreaterThan, 0)
			So(chunk.maxArgv, ShouldBeLessThanOrEqualTo, bkScaleShippedCap)
			So(chunk.uniqueIDs, ShouldEqual, elements)
		})

		Convey("one cycle's logging is a bounded summary, not the id list", func() {
			So(chunk.logBytes, ShouldBeLessThanOrEqualTo, bkScaleLogMax)
		})

		Convey("the next cycle does not re-issue the identical failing kill", func() {
			So(repeat.repeatedIDs, ShouldEqual, 0)
		})

		Convey("the summary distinguishes elements killed from elements already gone", func() {
			So(outcome.killed, ShouldEqual, bkScaleOutcomeElements/2)
			So(outcome.gone, ShouldEqual, bkScaleOutcomeElements/2)
		})

		Convey("a hung bkill is abandoned rather than blocking the kill path", func() {
			So(hangMs, ShouldBeLessThanOrEqualTo, bkScaleHangBoundMs)
		})
	})
}

// bkScaleEnvInt returns the named environment variable as a positive int, or the
// given default.
func bkScaleEnvInt(name string, dflt int) int {
	if n, err := strconv.Atoi(os.Getenv(name)); err == nil && n > 0 {
		return n
	}

	return dflt
}

// bkScaleMeasureOutcome drives one kill cycle with a bkill that terminates half its
// elements and reports the rest already gone, and returns the split the cycle
// logged (-1 each if it logged no split at all).
func bkScaleMeasureOutcome(t *testing.T) bkScaleOutcomeCounts {
	t.Helper()

	s, _ := bkScaleHarness(t, bkScaleOutcomeElements, `n=0
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
`)

	ctx, logs := captureLogCtx()

	if _, err := s.killExcessCmds(ctx, bkScalePrefix, 0); err != nil {
		t.Fatal(err)
	}

	outcome := bkScaleOutcomeCounts{
		killed: bkScaleLoggedNumber(logs.String(), "killed="),
		gone:   bkScaleLoggedNumber(logs.String(), "alreadyGone="),
	}

	t.Logf("BKILL-HYGIENE-OUTCOME: elements=%d killedReported=%d goneReported=%d",
		bkScaleOutcomeElements, outcome.killed, outcome.gone)

	return outcome
}

// bkScaleHarness returns an *lsf wired to a fake bjobs reporting the given number
// of non-RUN elements of one LSF array (so all of them are excess), and a fake
// bkill that appends each invocation's argv as one line to the returned file before
// running the given shell body.
func bkScaleHarness(t *testing.T, elements int, bkillBody string) (*lsf, string) {
	t.Helper()

	dir := t.TempDir()

	var bjobs strings.Builder

	for i := 1; i <= elements; i++ {
		fmt.Fprintf(&bjobs, "%s sb10 PEND normal host1 host2 %sfakecmd.uniq[%d] Aug 18 15:36\n",
			bkScaleJobID, bkScalePrefix, i)
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

	s := &lsf{
		config:   &ConfigLSF{Shell: testShell},
		bjobsExe: bjobsExe,
		bkillExe: bkillExe,
	}

	return s, argvFile
}

// bkScaleLoggedNumber returns the number logged after the given logfmt key, or -1
// if the logging does not report it at all.
func bkScaleLoggedNumber(logged, key string) int {
	i := strings.Index(logged, key)
	if i < 0 {
		return -1
	}

	rest := logged[i+len(key):]

	end := strings.IndexFunc(rest, func(r rune) bool {
		return r < '0' || r > '9'
	})
	if end == 0 {
		return -1
	}

	if end > 0 {
		rest = rest[:end]
	}

	n, err := strconv.Atoi(rest)
	if err != nil {
		return -1
	}

	return n
}

// bkScaleMeasureHang drives one kill cycle whose bkill never responds, and returns
// how many milliseconds the kill path took to return.
func bkScaleMeasureHang(t *testing.T, hangSecs int) int {
	t.Helper()

	s, _ := bkScaleHarness(t, bkScaleHangElements, fmt.Sprintf("sleep %d\nexit 0\n", hangSecs))

	ctx, _ := captureLogCtx()

	start := time.Now()

	if _, err := s.killExcessCmds(ctx, bkScalePrefix, 0); err != nil {
		t.Fatal(err)
	}

	elapsedMs := int(time.Since(start).Milliseconds())

	t.Logf("BKILL-HYGIENE-HANG: hangSeconds=%d elapsedMs=%d", hangSecs, elapsedMs)

	return elapsedMs
}

// bkScaleOutcomeCounts is the killed-versus-already-gone split a cycle reported,
// or -1 each when it reported no such thing at all.
type bkScaleOutcomeCounts struct {
	killed int
	gone   int
}

// bkScaleInvocations returns the element ids handed to each fake bkill invocation,
// in invocation order (with the -b flag dropped).
func bkScaleInvocations(t *testing.T, argvFile string) [][]string {
	t.Helper()

	data, err := os.ReadFile(argvFile)
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

// bkScaleOverlap returns how many of the second cycle's element ids the first
// cycle had already asked LSF to kill.
func bkScaleOverlap(second, first map[string]bool) int {
	overlap := 0

	for id := range second {
		if first[id] {
			overlap++
		}
	}

	return overlap
}
