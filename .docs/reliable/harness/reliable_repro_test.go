/*******************************************************************************
 * TEMP reliability reproduction tests (not for merge).
 *
 * TestReliableFalseLostRerun: a job marked lost and reclaimed, whose successful
 * archive then arrives, should be recorded complete (not re-run). On v0.37.1 the
 * archive is rejected (ErrBadJob) and the job re-runs. Fixed by #548.
 *
 * TestReliableCompletedRepGroupRemovedOnRefresh: after every job in a RepGroup
 * completes, a freshly-connected status client (web UI refresh) must NOT re-show
 * that complete-only RepGroup on its fresh seed - it disappears on refresh unless
 * the user searches for it or it completed during the current session. The
 * authoritative statusState still holds its complete count (so live-session
 * visibility, 260625-6, is unaffected); only the fresh-connect seed omits it
 * (260626-2).
 ******************************************************************************/

package jobqueue

import (
	"context"
	"os"
	"testing"
	"time"

	. "github.com/smartystreets/goconvey/convey"
)

func TestReliableFalseLostRerun(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()
	config, serverConfig, addr, standardReqs, clientConnectTime := jobqueueTestInit(true)
	const rg = "reliable_false_lost_rg"

	Convey("A successful archive arriving after a lost reclaim should win, not rerun", t, func() {
		server, _, token, err := serve(ctx, serverConfig)
		So(err, ShouldBeNil)

		defer server.Stop(ctx, true)

		jq, err := Connect(addr, config.ManagerCAFile, config.ManagerCertDomain, token, clientConnectTime)
		So(err, ShouldBeNil)

		defer disconnect(jq)

		job := &Job{
			Cmd: restFormTrue, Cwd: testCwdPath, RepGroup: rg, ReqGroup: rg,
			Requirements: standardReqs, Retries: 1,
		}
		inserts, already, err := jq.Add([]*Job{job}, os.Environ(), true)
		So(err, ShouldBeNil)
		So(inserts, ShouldEqual, 1)
		So(already, ShouldEqual, 0)

		reserved, err := jq.Reserve(200 * time.Millisecond)
		So(err, ShouldBeNil)
		So(reserved, ShouldNotBeNil)
		So(jq.Started(reserved, 1), ShouldBeNil)

		lostEnd := &JobEndState{Exited: true, Exitcode: -1, EndTime: time.Now()}
		So(jq.Release(reserved, lostEnd, FailReasonLost), ShouldBeNil)

		successEnd := &JobEndState{Exited: true, Exitcode: 0, EndTime: lostEnd.EndTime.Add(time.Second)}
		archiveErr := jq.Archive(reserved, successEnd)

		summaries, serr := jq.GetStatusByRepGroupMatch(rg, RepGroupMatchExact, nil, true, false)
		So(serr, ShouldBeNil)

		t.Logf("RESULT archiveErr=%v counts=%v", archiveErr, summaries[rg].Counts)

		So(archiveErr, ShouldBeNil)
		So(summaries[rg].Counts, ShouldResemble, map[JobState]int{JobStateComplete: 1})
	})
}

func TestReliableCompletedRepGroupRemovedOnRefresh(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()
	config, serverConfig, addr, standardReqs, clientConnectTime := jobqueueTestInit(true)
	const rg = "reliable_removed_rg"
	const n = 5

	Convey("A completed-only RepGroup is OMITTED from a fresh status seed (web UI refresh)", t, func() {
		server, _, token, err := serve(ctx, serverConfig)
		So(err, ShouldBeNil)

		defer server.Stop(ctx, true)

		jq, err := Connect(addr, config.ManagerCAFile, config.ManagerCertDomain, token, clientConnectTime)
		So(err, ShouldBeNil)

		defer disconnect(jq)

		jobs := make([]*Job, n)
		for i := range jobs {
			jobs[i] = &Job{
				Cmd: restFormTrue + " " + itoa(i), Cwd: testCwdPath, RepGroup: rg, ReqGroup: rg,
				Requirements: standardReqs, Retries: 0,
			}
		}
		inserts, _, err := jq.Add(jobs, os.Environ(), true)
		So(err, ShouldBeNil)
		So(inserts, ShouldEqual, n)

		// complete every job, so the RepGroup is complete-only.
		for range jobs {
			j, errr := jq.Reserve(2 * time.Second)
			So(errr, ShouldBeNil)
			So(j, ShouldNotBeNil)
			So(jq.Started(j, os.Getpid()), ShouldBeNil)
			So(jq.Archive(j, &JobEndState{Exited: true, Exitcode: 0, EndTime: time.Now()}), ShouldBeNil)
		}

		// wait for the authoritative statusState to reflect the completions.
		var snap map[string]map[JobState]int
		for range 100 {
			snap = server.statusState.snapshot()
			if snap[rg][JobStateComplete] == n {
				break
			}

			time.Sleep(20 * time.Millisecond)
		}
		So(snap[rg][JobStateComplete], ShouldEqual, n) // authoritative state is correct

		// a fresh client (web UI (re)connect / page refresh) subscribes and drains
		// its seed. The complete-only RepGroup must be OMITTED, so a refresh does
		// not re-show it.
		sub := server.statusState.subscribe()
		seed := server.statusState.drain(sub)
		server.statusState.unsubscribe(sub)

		t.Logf("RESULT authoritative complete=%d ; fresh-seed[%s]=%v", snap[rg][JobStateComplete], rg, seed[rg])

		So(seed, ShouldNotContainKey, rg)
	})
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}

	var b [20]byte

	pos := len(b)
	for i > 0 {
		pos--
		b[pos] = byte('0' + i%10)
		i /= 10
	}

	return string(b[pos:])
}
