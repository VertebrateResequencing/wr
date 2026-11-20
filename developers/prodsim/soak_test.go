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

package main

import (
	"context"
	"flag"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	clienttesting "github.com/VertebrateResequencing/wr/client/testing"
	"github.com/VertebrateResequencing/wr/jobqueue"
	. "github.com/smartystreets/goconvey/convey"
)

const (
	// testPortalTarget and testPortalMedianMins make each dynamic burst 17
	// jobs a phase at testSimMinute and a 1s gap: 34 jobs a burst.
	testPortalTarget     = 2
	testPortalMedianMins = 1
	testPortalGap        = time.Second
	testBurstJobs        = 34
	// testPortalMax is exactly two bursts, so a count at the cap still gets
	// the third burst, and the portal pauses at three bursts' 102 jobs.
	testPortalMax    = 2 * testBurstJobs
	testBurstsToPass = 3
	testPausedGaps   = 3
	testRechecks     = 2
	// testRecheckWait bounds the wait for testRechecks counts while paused,
	// well under the 30s portalRecheck the 1s gap must override.
	testRecheckWait = 10 * testPortalGap
	eventCols       = 3
	burstPrefix     = "burst "
	pausePrefix     = "pause "
	resumePrefix    = "resume "
	countOp         = "count_incomplete"
	callErrCol      = 5
)

func TestPortalMaxIncompleteDefault(t *testing.T) {
	Convey("Given the soak flags, -portal-max-incomplete defaults to battery10's live set of 300000 jobs", t, func() {
		commandLine := flag.CommandLine
		flag.CommandLine = flag.NewFlagSet("prodsim", flag.ContinueOnError)

		defer func() { flag.CommandLine = commandLine }()

		var c soakConfig

		soakFlags(&c)
		So(flag.CommandLine.Parse(nil), ShouldBeNil)
		So(c.portalMaxIncomplete, ShouldEqual, 300000)
	})
}

func TestDynamicPortalPausesOverMaxIncomplete(t *testing.T) {
	Convey("Given the dynamic portal against a manager that runs none of its jobs", t, func() {
		portal := func(maxIncomplete int) *outageRun {
			return startOutageRun(t, testSimMinute, 1, func(s *sim, ctx context.Context) {
				target := filepath.Join(s.cfg.workDir, "portal_target")
				if err := os.WriteFile(target, []byte(strconv.Itoa(testPortalTarget)), filePerm); err != nil {
					return
				}

				s.cfg.soak.portalTargetFile = target
				s.cfg.soak.portalGap = testPortalGap
				s.cfg.soak.portalMedianMins = testPortalMedianMins
				s.cfg.soak.portalMaxIncomplete = maxIncomplete

				s.portal(ctx)
			})
		}

		Convey("with -portal-max-incomplete at two bursts, it bursts at the cap, stops after the burst that "+
			"passes it, logs the pause, and resumes, logged, once the incomplete jobs fall", func() {
			r := portal(testPortalMax)
			defer r.cleanup()

			So(waitForEvent(r.out, pausePrefix, 1), ShouldBeTrue)

			pausedAt := time.Now()
			countsAtPause := len(callRows(r.out, countOp))

			bursts := len(portalEvents(r.out, burstPrefix))
			So(bursts, ShouldEqual, testBurstsToPass)

			jq := r.s.newJQ(context.Background(), "test")
			So(jq, ShouldNotBeNil)

			defer disconnect(jq)

			incomplete, err := jq.GetIncomplete(0, "", false, false)
			So(err, ShouldBeNil)

			pause := portalEvents(r.out, pausePrefix)
			So(pause, ShouldHaveLength, 1)
			So(pause[0], ShouldContainSubstring, "max="+strconv.Itoa(testPortalMax))
			So(eventIncomplete(pause[0]), ShouldEqual, len(incomplete))
			So(len(incomplete), ShouldEqual, testBurstsToPass*testBurstJobs)

			So(waitForCallsWithin(r.out, countOp, countsAtPause+testRechecks, testRecheckWait), ShouldBeTrue)
			So(time.Since(pausedAt), ShouldBeGreaterThanOrEqualTo, testPortalGap)

			So(portalEvents(r.out, burstPrefix), ShouldHaveLength, bursts)
			So(portalEvents(r.out, pausePrefix), ShouldHaveLength, 1)
			So(portalEvents(r.out, resumePrefix), ShouldBeEmpty)

			So(deleteIncomplete(jq), ShouldBeNil)
			So(waitForEvent(r.out, burstPrefix, bursts+1), ShouldBeTrue)

			resume := portalEvents(r.out, resumePrefix)
			So(resume, ShouldHaveLength, 1)
			So(eventIncomplete(resume[0]), ShouldBeLessThanOrEqualTo, testPortalMax)
		})

		Convey("with -portal-max-incomplete at two bursts, a manager outage while paused adds no burst: "+
			"failed counts are waited out, and it stays paused until the incomplete jobs fall", func() {
			r := portal(testPortalMax)
			defer r.cleanup()

			So(waitForEvent(r.out, pausePrefix, 1), ShouldBeTrue)

			bursts := len(portalEvents(r.out, burstPrefix))
			So(bursts, ShouldEqual, testBurstsToPass)

			r.server.Stop(context.Background(), true)

			failed := waitForCountRow(r.out, len(callRows(r.out, countOp)), true)
			So(failed, ShouldBeGreaterThanOrEqualTo, 0)

			r.server = clienttesting.Serve(t, r.config)

			So(waitForCountRow(r.out, failed+1, false), ShouldBeGreaterThan, failed)

			So(portalEvents(r.out, burstPrefix), ShouldHaveLength, bursts)
			So(portalEvents(r.out, pausePrefix), ShouldHaveLength, 1)
			So(portalEvents(r.out, resumePrefix), ShouldBeEmpty)

			jq := r.s.newJQ(context.Background(), "test")
			So(jq, ShouldNotBeNil)

			defer disconnect(jq)

			So(deleteIncomplete(jq), ShouldBeNil)
			So(waitForEvent(r.out, burstPrefix, bursts+1), ShouldBeTrue)
			So(portalEvents(r.out, resumePrefix), ShouldHaveLength, 1)
		})

		Convey("ending the run while the dynamic portal is paused ends it within testEndWait", func() {
			r := portal(testPortalMax)
			defer r.cleanup()

			So(waitForEvent(r.out, pausePrefix, 1), ShouldBeTrue)
			So(r.end(), ShouldBeLessThan, testEndWait)
		})

		Convey("with -portal-max-incomplete 0, it keeps submitting bursts past that many incomplete jobs", func() {
			r := portal(0)
			defer r.cleanup()

			So(waitForEvent(r.out, burstPrefix, testPausedGaps+1), ShouldBeTrue)
			So(portalEvents(r.out, pausePrefix), ShouldBeEmpty)
			So(callRows(r.out, countOp), ShouldBeEmpty)
		})
	})
}

// waitForEvent waits for out to have n portal events starting with prefix.
func waitForEvent(out, prefix string, n int) bool {
	for deadline := time.Now().Add(testCallWait); time.Now().Before(deadline); time.Sleep(testPoll) {
		if len(portalEvents(out, prefix)) >= n {
			return true
		}
	}

	return false
}

// portalEvents returns the portal's events in out's events.tsv that start with
// prefix.
func portalEvents(out, prefix string) []string {
	b, err := os.ReadFile(filepath.Join(out, "events.tsv"))
	if err != nil {
		return nil
	}

	var evs []string

	for line := range strings.SplitSeq(string(b), "\n") {
		f := strings.SplitN(line, "\t", eventCols)
		if len(f) == eventCols && f[1] == actorPortal && strings.HasPrefix(f[2], prefix) {
			evs = append(evs, f[2])
		}
	}

	return evs
}

// eventIncomplete returns the incomplete=N of an event, or -1.
func eventIncomplete(ev string) int {
	for f := range strings.FieldsSeq(ev) {
		if v, ok := strings.CutPrefix(f, "incomplete="); ok {
			return atoiOr([]string{v}, unknown)
		}
	}

	return unknown
}

// deleteIncomplete deletes every incomplete job, as none are running.
func deleteIncomplete(jq *jobqueue.Client) error {
	jobs, err := jq.GetIncomplete(0, "", false, false)
	if err != nil {
		return err
	}

	jes := make([]*jobqueue.JobEssence, len(jobs))
	for i, job := range jobs {
		jes[i] = job.ToEssense()
	}

	_, err = jq.Delete(jes)

	return err
}

// waitForCountRow waits for a count_incomplete row in out's calls.tsv, at or
// after index from, that failed or, if not failed, succeeded, and returns its
// index, or -1 if none comes.
func waitForCountRow(out string, from int, failed bool) int {
	for deadline := time.Now().Add(testCallWait); time.Now().Before(deadline); time.Sleep(testPoll) {
		rows := callRows(out, countOp)
		for i := from; i < len(rows); i++ {
			if (rows[i][callErrCol] != "") == failed {
				return i
			}
		}
	}

	return unknown
}
