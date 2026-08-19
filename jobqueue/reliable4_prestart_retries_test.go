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
	"context"
	"errors"
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"

	jqs "github.com/VertebrateResequencing/wr/jobqueue/scheduler"
	"github.com/VertebrateResequencing/wr/queue"
	. "github.com/smartystreets/goconvey/convey"
)

// preStartRetries is the Retries value every job in these tests is given, so
// the retry budget (UntilBuried) starts at preStartRetries+1 and a correctly
// counted failure buries on attempt preStartRetries+1.
const preStartRetries uint8 = 2

// preStartAttemptCap is how many reserve cycles the helpers below will do
// before giving up. It is deliberately MORE than preStartRetries+1, so an
// uncounted (unbounded) retry loop shows up as "hit the cap" rather than as a
// pass.
const preStartAttemptCap = 6

// preStartDirMode is the mode of the per-job cwd these tests create.
const preStartDirMode = 0o755

// preStartMaxRetryAttempts is how many attempts the maximum-retries Convey
// makes. It only has to be enough to see the budget counting down from a real
// (saturated) 255 instead of sitting at an overflowed 0; actually exhausting
// 255 retries would need 255 reservations.
const preStartMaxRetryAttempts = 2

// errPreStartNotAJob is returned when the queue held something that was not a
// *Job, which can never happen in practice.
var errPreStartNotAJob = errors.New("queue item was not a job")

// preStartOutcome is what the helpers below measured: how many reservations the
// job consumed, the remaining retry budget seen after each one, and the
// server-side job state at the end.
type preStartOutcome struct {
	key            string
	reserves       int
	untilBuriedSeq []int
	state          JobState
	itemState      queue.ItemState
	failReason     string
	attempts       int
	untilBuried    int
	stillQueued    bool

	// clientUntilBuriedSeq and clientState are the RUNNER's view of the same
	// thing, taken off the reserved job object after each attempt: what
	// Client.finishRelease mirrored locally. They must track untilBuriedSeq and
	// state exactly, because a runner that thinks it has retries left when the
	// server has buried the job (or vice versa) is a client/server divergence.
	clientUntilBuriedSeq []int
	clientState          JobState
}

// executePreStartJob reserves and Executes the job repeatedly (up to
// preStartAttemptCap times), recording the retry budget left after each
// attempt. It stops as soon as the job is no longer reservable, which is what
// burying it achieves.
func executePreStartJob(ctx context.Context, t *testing.T, jq *Client, server *Server,
	cmd, shell, what string,
) preStartOutcome {
	t.Helper()

	job := addPreStartJob(t, jq, cmd)
	out := preStartOutcome{key: job.Key()}

	for range preStartAttemptCap {
		reserved := execFailReserve(jq)
		if reserved == nil {
			break
		}

		out.reserves++

		_ = jq.Execute(ctx, reserved, shell) //nolint:errcheck // a failing Execute is the point

		out.untilBuriedSeq = append(out.untilBuriedSeq, preStartUntilBuried(server, out.key))

		clientUntilBuried, clientState := preStartClientRetryState(reserved)
		out.clientUntilBuriedSeq = append(out.clientUntilBuriedSeq, clientUntilBuried)
		out.clientState = clientState
	}

	recordPreStartOutcome(t, server, &out, what)

	return out
}

// TestReliable4PreStartReleaseRetries is the behavioural reproducer for
// reliable4 ITEM A: a release that happens BEFORE the job reported a start
// never decremented UntilBuried, so any pre-start release retried forever and
// ignored --retries. Server-side StartTime is set only by a landed start report
// (applyJobStart, which needs a real pid+host) and resetJobForReservation
// re-zeroes it at every reservation, so a command that fails in cmd.Start()
// never had a StartTime and never spent a retry: it burned one scheduled
// runner, one reservation, one copy of the command over RPC and one bolt write
// per iteration, for ever.
//
// The counterpart Conveys pin the property that makes this safe to fix: a job
// that is merely SLOW to report its start - the false-lost problem this whole
// reliable* effort exists to fight - must NOT burn its retries. Only a release
// reported by the job's own owner AFTER it tried to run the command counts.
func TestReliable4PreStartReleaseRetries(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()
	config, serverConfig, addr, _, clientConnectTime := jobqueueTestInit(false)

	Convey("Given a live manager", t, func() {
		server, _, token, err := serve(ctx, serverConfig)
		So(err, ShouldBeNil)

		defer server.Stop(ctx, true)

		jq, err := Connect(addr, config.ManagerCAFile, config.ManagerCertDomain, token, clientConnectTime)
		So(err, ShouldBeNil)

		defer disconnect(jq)

		Convey("A transiently-failing start is buried after exactly Retries+1 attempts", func() {
			// ETXTBSY: a shell being written to right now cannot exec, but would
			// exec fine a moment later, so it is deliberately NOT in the permanent
			// bury set (0d22eda). Pre-fix this looped for ever; the job must now
			// spend one retry per failed attempt like any other failure.
			busy, closeBusy := testBusyExecutable(t.TempDir())
			defer closeBusy()

			run := executePreStartJob(ctx, t, jq, server, "echo hi", busy, "transient-start-failure")

			So(run.untilBuriedSeq, ShouldResemble, []int{2, 1, 0})
			So(run.reserves, ShouldEqual, int(preStartRetries)+1)
			So(run.state, ShouldEqual, JobStateBuried)
			So(run.itemState, ShouldEqual, queue.ItemStateBury)
			So(run.untilBuried, ShouldEqual, 0)
			So(run.failReason, ShouldEqual, FailReasonStart)

			Convey("and the RUNNER's own copy of the job agrees with the server about it", func() {
				// this is the ONLY release path where the runner's local mirror
				// (Client.finishRelease) needs to be told the release was attempted:
				// reportStartFailure releases with a nil JobEndState, so job.Exited
				// stays false and the pre-existing "exited non-zero" test cannot
				// fire. Without that, the runner would carry 3,2,1 and think the job
				// was merely delayed while the server had buried it.
				So(run.clientUntilBuriedSeq, ShouldResemble, run.untilBuriedSeq)
				So(run.clientUntilBuriedSeq, ShouldResemble, []int{2, 1, 0})
				So(run.clientState, ShouldEqual, run.state)
				So(run.clientState, ShouldEqual, JobStateBuried)
			})
		})

		Convey("A job that DOES report a start still gets its full retry budget", func() {
			// the counterpart: a command that starts fine and exits non-zero must
			// still get exactly Retries+1 attempts, not fewer.
			run := executePreStartJob(ctx, t, jq, server, "false", "/bin/sh", "started-then-failed")

			So(run.untilBuriedSeq, ShouldResemble, []int{2, 1, 0})
			So(run.reserves, ShouldEqual, int(preStartRetries)+1)
			So(run.state, ShouldEqual, JobStateBuried)
			So(run.itemState, ShouldEqual, queue.ItemStateBury)
			So(run.attempts, ShouldEqual, int(preStartRetries)+1)
			So(run.failReason, ShouldEqual, FailReasonExit)
		})

		Convey("A slow start report is not penalised: a manager-side release spends no retry", func() {
			// the job is reserved but never reports a start - exactly a healthy
			// runner that is merely slow to get its Started() through under load.
			// The manager's own lost/kill release (server.killJob ->
			// server.releaseJob) must leave the retry budget untouched, however
			// often it happens, or a busy farm would bury healthy work.
			job := addPreStartJob(t, jq, "echo slowstart")
			out := preStartOutcome{key: job.Key()}

			for range preStartAttemptCap {
				reserved := execFailReserve(jq)
				if reserved == nil {
					break
				}

				out.reserves++

				So(preStartMarkLostAndKill(ctx, server, out.key), ShouldBeNil)

				out.untilBuriedSeq = append(out.untilBuriedSeq, preStartUntilBuried(server, out.key))
			}

			recordPreStartOutcome(t, server, &out, "manager-released-never-started")

			So(out.reserves, ShouldEqual, preStartAttemptCap)
			So(out.untilBuriedSeq, ShouldResemble, []int{3, 3, 3, 3, 3, 3})
			So(out.state, ShouldEqual, JobStateDelayed)
			So(out.untilBuried, ShouldEqual, int(preStartRetries)+1)

			Convey("and when its runner finally does start it, the job runs and completes", func() {
				reserved := execFailReserve(jq)
				So(reserved, ShouldNotBeNil)
				So(jq.Execute(ctx, reserved, "/bin/sh"), ShouldBeNil)

				_, _, _, _, stillQueued := execFailServerJob(server, out.key)
				So(stillQueued, ShouldBeFalse)
			})
		})

		Convey("A command that ran but whose start report never landed still spends a retry", func() {
			// Execute keeps a healthy command running when its start report hits a
			// transient failure, re-sending the report in the background (see
			// retryStartReport). During that window the command really is running
			// while the server still has no StartTime for it, so a failure reported
			// in that window must spend a retry like any other - which is why
			// Execute's final-state release goes through applyFinalState's
			// attempted release rather than the plain hand-back Release.
			job := addPreStartJob(t, jq, "echo neverreported")
			out := preStartOutcome{key: job.Key()}

			for range preStartAttemptCap {
				reserved := execFailReserve(jq)
				if reserved == nil {
					break
				}

				out.reserves++

				// reserved, never started: the server's StartTime is zero, exactly
				// as it is while a start report is still being retried.
				So(preStartServerStartTimeIsZero(server, out.key), ShouldBeTrue)

				So(jq.applyFinalState(reserved, &JobEndState{
					Exited: true, Exitcode: 1, EndTime: time.Now(),
				}, execAction{release: true, failreason: FailReasonExit}), ShouldBeNil)

				out.untilBuriedSeq = append(out.untilBuriedSeq, preStartUntilBuried(server, out.key))

				clientUntilBuried, clientState := preStartClientRetryState(reserved)
				out.clientUntilBuriedSeq = append(out.clientUntilBuriedSeq, clientUntilBuried)
				out.clientState = clientState
			}

			recordPreStartOutcome(t, server, &out, "ran-but-start-report-never-landed")

			So(out.untilBuriedSeq, ShouldResemble, []int{2, 1, 0})
			So(out.reserves, ShouldEqual, int(preStartRetries)+1)
			So(out.state, ShouldEqual, JobStateBuried)
			So(out.itemState, ShouldEqual, queue.ItemStateBury)

			// the runner's local mirror agrees here too, but note that this
			// particular case does NOT pin the attempted flag in
			// Client.finishRelease: the JobEndState says exit 1, so the
			// pre-existing "exited non-zero" test would decrement anyway. The
			// nil-JobEndState start-failure Convey above is what pins it.
			So(out.clientUntilBuriedSeq, ShouldResemble, out.untilBuriedSeq)
			So(out.clientState, ShouldEqual, out.state)
		})

		Convey("An owner release that did not try to run the command spends no retry", func() {
			// cmd/runner.go hands a job straight back ("not enough time to run",
			// "failed to read job's Env", "failed to add env var overrides")
			// WITHOUT ever calling Execute. Nothing was attempted, so - as
			// Client.Release documents - that may happen an unlimited number of
			// times without eroding the job's retries.
			job := addPreStartJob(t, jq, "echo handback")
			out := preStartOutcome{key: job.Key()}

			for range preStartAttemptCap {
				reserved := execFailReserve(jq)
				if reserved == nil {
					break
				}

				out.reserves++

				So(jq.Release(reserved, nil, "not enough time to run"), ShouldBeNil)

				out.untilBuriedSeq = append(out.untilBuriedSeq, preStartUntilBuried(server, out.key))
			}

			recordPreStartOutcome(t, server, &out, "owner-handback-never-attempted")

			So(out.reserves, ShouldEqual, preStartAttemptCap)
			So(out.untilBuriedSeq, ShouldResemble, []int{3, 3, 3, 3, 3, 3})
			So(out.state, ShouldEqual, JobStateDelayed)
			So(out.untilBuried, ShouldEqual, int(preStartRetries)+1)
		})

		Convey("A start report landing mid-release cannot make the release spend a retry it decided not to spend", func() {
			// white-box: releaseJob's body, with a jstart landing in the one window
			// it has. releaseJobSnapshot reads job.StartTime under RLock and
			// finalizeReleasedJob acts under a SEPARATE Lock, with
			// applyReleaseQueueChange in between, so a start report arriving in that
			// window used to make the snapshot say "do not bury" while
			// finalizeReleasedJob said "spend a retry". That drives UntilBuried to 0
			// with the item still in Delay, and the NEXT attempted release then
			// underflows the uint8 to 255 - restoring exactly the unbounded retrying
			// this whole change removes. Deciding spendsRetry once, in the snapshot,
			// closes it.
			job := addPreStartJob(t, jq, "echo interleaved")
			So(execFailReserve(jq), ShouldNotBeNil)

			sjob := preStartServerJob(server, job.Key())
			So(sjob, ShouldNotBeNil)
			So(preStartServerStartTimeIsZero(server, job.Key()), ShouldBeTrue)

			rep := lostJobReleaseReport()
			bury, key, currentState := releaseJobSnapshot(sjob, &rep)
			So(bury, ShouldBeFalse)
			So(rep.spendsRetry, ShouldBeFalse)

			item, errg := server.q.Get(key)
			So(errg, ShouldBeNil)

			// the jstart lands HERE, in the window between the two lock windows.
			sjob.Lock()
			sjob.StartTime = time.Now()
			sjob.Unlock()

			alreadyDone, errq := server.applyReleaseQueueChange(ctx, server.q, item, key, bury, currentState, sjob)
			So(errq, ShouldBeNil)
			So(alreadyDone, ShouldBeFalse)

			server.finalizeReleasedJob(ctx, sjob, rep)

			So(preStartUntilBuried(server, key), ShouldEqual, int(preStartRetries)+1)
			So(preStartServerState(server, key), ShouldEqual, JobStateDelayed)
		})

		Convey("The documented maximum --retries 255 gets a real retry budget, not an overflowed 0", func() {
			// UntilBuried is a uint8 and Retries is a documented, accepted 0-255
			// (cmd/add.go's "--retries [0-255]", wr mod -r, and the REST API's
			// restModifyUint8Max), so seeding it with Retries+1 overflows to 0 at
			// the maximum: a LIVE job whose retry budget is already exhausted, which
			// is the very state the clamp in finalizeReleasedJob defends against.
			// Its item is then never buried while finalizeReleasedJob calls the job
			// buried, so wr status reports "buried" for ever while the job goes on
			// being reserved, run and released - one runner slot per iteration,
			// which is exactly the unbounded pre-start retrying this change removes.
			// initialUntilBuried saturating instead of wrapping is what stops it.
			busy, closeBusy := testBusyExecutable(t.TempDir())
			defer closeBusy()

			job := addPreStartJobWithRetries(t, jq, "echo maxretries", math.MaxUint8)
			So(preStartUntilBuried(server, job.Key()), ShouldEqual, math.MaxUint8)

			out := preStartOutcome{key: job.Key()}

			for range preStartMaxRetryAttempts {
				reserved := execFailReserve(jq)
				if reserved == nil {
					break
				}

				out.reserves++

				_ = jq.Execute(ctx, reserved, busy) //nolint:errcheck // a failing Execute is the point

				out.untilBuriedSeq = append(out.untilBuriedSeq, preStartUntilBuried(server, out.key))
			}

			recordPreStartOutcome(t, server, &out, "max-retries-transient-start-failure")

			So(out.reserves, ShouldEqual, preStartMaxRetryAttempts)
			So(out.untilBuriedSeq, ShouldResemble, []int{math.MaxUint8 - 1, math.MaxUint8 - 2})
			So(out.state, ShouldEqual, JobStateDelayed)
			So(out.itemState, ShouldEqual, queue.ItemStateDelay)
		})

		Convey("A release cannot leave a live job reported buried with its item still in delay", func() {
			// the second, independent guard on the same hazard: UntilBuried is a
			// uint8, so a decrement of an exhausted budget wraps to 255 and hands the
			// job 255 more attempts. Whatever leaves a live job at 0 (the
			// interleaving above, a future refactor, a database an older manager
			// wrote with the overflowed --retries 255 seed), a release must never
			// make it worse - and must never leave finalizeReleasedJob calling the
			// job buried while its queue item stays in delay, because that item goes
			// on being reserved and released while wr status calls it buried.
			job := addPreStartJob(t, jq, "echo clamped")
			So(execFailReserve(jq), ShouldNotBeNil)

			sjob := preStartServerJob(server, job.Key())
			So(sjob, ShouldNotBeNil)

			sjob.Lock()
			sjob.UntilBuried = 0
			sjob.Unlock()

			So(server.releaseJob(ctx, sjob, releaseReport{
				endState:   &JobEndState{Exited: true, Exitcode: 1, EndTime: time.Now()},
				failReason: FailReasonExit,
				attempted:  true,
			}), ShouldBeNil)

			So(preStartUntilBuried(server, job.Key()), ShouldEqual, 0)
			So(preStartServerState(server, job.Key()), ShouldEqual, JobStateBuried)
			So(preStartItemState(server, job.Key()), ShouldEqual, queue.ItemStateBury)

			Convey("and that holds for a release that spends no retry at all", func() {
				// the manager's own lost/kill release does not decrement, so an
				// exhausted budget simply stays at 0 - and finalizeReleasedJob still
				// calls the job buried, so the item has to be buried with it.
				lost := addPreStartJob(t, jq, "echo clamped lost")
				So(execFailReserve(jq), ShouldNotBeNil)

				slost := preStartServerJob(server, lost.Key())
				So(slost, ShouldNotBeNil)

				slost.Lock()
				slost.UntilBuried = 0
				slost.Unlock()

				So(server.releaseJob(ctx, slost, lostJobReleaseReport()), ShouldBeNil)

				So(preStartUntilBuried(server, lost.Key()), ShouldEqual, 0)
				So(preStartServerState(server, lost.Key()), ShouldEqual, JobStateBuried)
				So(preStartItemState(server, lost.Key()), ShouldEqual, queue.ItemStateBury)
			})
		})
	})
}

// addPreStartJob adds a single job with the given command and preStartRetries
// retries, returning it.
func addPreStartJob(t *testing.T, jq *Client, cmd string) *Job {
	t.Helper()

	return addPreStartJobWithRetries(t, jq, cmd, preStartRetries)
}

// addPreStartJobWithRetries adds a single job with the given command and
// Retries, returning it.
func addPreStartJobWithRetries(t *testing.T, jq *Client, cmd string, retries uint8) *Job {
	t.Helper()

	cwd := filepath.Join(t.TempDir(), "job")
	So(os.MkdirAll(cwd, preStartDirMode), ShouldBeNil)

	repGroup := "reliable4_prestart"
	job := &Job{
		Cmd: cmd, Cwd: cwd, CwdMatters: true,
		RepGroup: repGroup, ReqGroup: repGroup, Retries: retries,
		Requirements: &jqs.Requirements{
			RAM: 10, Time: 10 * time.Second, Cores: 0, Other: make(map[string]string),
		},
	}

	added, _, err := jq.Add([]*Job{job}, os.Environ(), true)
	So(err, ShouldBeNil)
	So(added, ShouldEqual, 1)

	return job
}

// preStartMarkLostAndKill marks the keyed job lost and has the server release it
// the way a confirmed-dead runner's job is released (killJob -> releaseJob with
// FailReasonLost). This is the manager-initiated release path, as opposed to a
// report from the job's owner.
func preStartMarkLostAndKill(ctx context.Context, server *Server, key string) error {
	job := preStartServerJob(server, key)
	if job == nil {
		return errPreStartNotAJob
	}

	job.Lock()
	job.Lost = true
	job.Unlock()

	_, err := server.killJob(ctx, key)

	return err
}

// preStartServerJob returns the server's own copy of the keyed job - the one the
// release path mutates - or nil if it is no longer in the queue.
func preStartServerJob(server *Server, key string) *Job {
	item, err := server.q.Get(key)
	if err != nil || item == nil {
		return nil
	}

	job, ok := item.Data().(*Job)
	if !ok {
		return nil
	}

	return job
}

// preStartUntilBuried reads the server-side job's remaining retry budget, or -1
// if the job is no longer in the queue.
func preStartUntilBuried(server *Server, key string) int {
	job := preStartServerJob(server, key)
	if job == nil {
		return -1
	}

	job.RLock()
	defer job.RUnlock()

	return int(job.UntilBuried)
}

// preStartServerStartTimeIsZero reports whether the server has no start time
// recorded for the keyed job, i.e. no start report has landed for it.
func preStartServerStartTimeIsZero(server *Server, key string) bool {
	job := preStartServerJob(server, key)
	if job == nil {
		return false
	}

	job.RLock()
	defer job.RUnlock()

	return job.StartTime.IsZero()
}

// preStartClientRetryState reads the RUNNER-side copy of a job: the retry budget
// and state that Client.finishRelease mirrored onto it. It is what an operator's
// runner believes, and it must agree with the server.
func preStartClientRetryState(job *Job) (int, JobState) {
	job.RLock()
	defer job.RUnlock()

	return int(job.UntilBuried), job.State
}

// preStartServerState reads the server-side job's state, or "" if it is no
// longer in the queue.
func preStartServerState(server *Server, key string) JobState {
	job := preStartServerJob(server, key)
	if job == nil {
		return ""
	}

	job.RLock()
	defer job.RUnlock()

	return job.State
}

// preStartItemState reports the queue sub-queue the job's item is in, or "" if
// it is no longer queued.
func preStartItemState(server *Server, key string) queue.ItemState {
	item, err := server.q.Get(key)
	if err != nil || item == nil {
		return ""
	}

	return item.Stats().State
}

// recordPreStartOutcome fills in the final server-side view of the job and logs
// the whole measurement unconditionally, so it is on the record even though
// GoConvey's default FailureHalts stops at the first failed So.
func recordPreStartOutcome(t *testing.T, server *Server, out *preStartOutcome, what string) {
	t.Helper()

	state, failReason, attempts, untilBuried, ok := execFailServerJob(server, out.key)
	out.state, out.failReason, out.attempts, out.untilBuried, out.stillQueued =
		state, failReason, attempts, untilBuried, ok
	out.itemState = preStartItemState(server, out.key)

	t.Logf("PRESTART-MEASURED %s reserves=%d untilBuriedSeq=%v queued=%t state=%s item=%s "+
		"failReason=%q attempts=%d untilBuried=%d clientUntilBuriedSeq=%v clientState=%s",
		what, out.reserves, out.untilBuriedSeq, out.stillQueued, out.state, out.itemState,
		out.failReason, out.attempts, out.untilBuried, out.clientUntilBuriedSeq, out.clientState)
}
