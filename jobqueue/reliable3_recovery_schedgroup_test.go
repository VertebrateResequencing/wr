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

// Regression test for the recovery scheduler-group bug: Job.schedulerGroup is an
// unexported field, so it is NOT gob-serialised to the DB. When the manager
// recovers prior state after a crash/kill-9, jobs that were JobStateRunning are
// decoded with schedulerGroup == "". recoveredItemDef must recompute the real
// scheduler group from the persisted Requirements+LimitGroups, so both the queue
// item's ReserveGroup and later accountForRunningJobs bucket the running job
// under its real group (matching the scheduler cmd identity recoverRunningJob
// uses) rather than the empty group "". Without the fix the manager perpetually
// schedules empty-group runners that immediately exit, and adds zero running work
// to the real group so its genuinely-ready siblings never get a runner.

import (
	"context"
	"testing"
	"time"

	"github.com/VertebrateResequencing/wr/internal"
	"github.com/VertebrateResequencing/wr/jobqueue/scheduler"
	. "github.com/smartystreets/goconvey/convey"
)

const (
	// recSchedGroupRepGroup and recSchedGroupLimitGrp identify the persisted
	// running job used by the recovery scheduler-group regression test.
	recSchedGroupRepGroup = "reliable3-recover-schedgroup"
	recSchedGroupLimitGrp = "reliable3-recover-lg"
)

// TestReliable3RecoveryRestoresSchedulerGroup pins the fix for the recovery
// scheduler-group bug. It persists a JobStateRunning job with non-trivial
// Requirements+LimitGroups directly into the live DB (so gob-decoding on recovery
// clears its unexported schedulerGroup), restarts the manager, and asserts the
// recovered running job carries its real scheduler group - both on the Job itself
// and in accountForRunningJobs' bucketing.
func TestReliable3RecoveryRestoresSchedulerGroup(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()
	serverConfig := recoverySchedGroupServerConfig()

	req := &scheduler.Requirements{RAM: 24576, Cores: 2, Time: time.Hour, Disk: 1}
	limitGroups := []string{recSchedGroupLimitGrp}
	wantGroup := schedulerGroupString(reqForScheduler(req), limitGroups)

	Convey("A recovered running job carries its real scheduler group, not the empty group", t, func() {
		// the expected group must be non-trivial (this is what the running job
		// diverges from when its unexported schedulerGroup is lost on decode).
		So(wantGroup, ShouldNotEqual, "")

		runningKey := persistRunningJobForRecovery(ctx, t, serverConfig, req, limitGroups)

		server, _, _, err := serve(ctx, serverConfig)
		So(err, ShouldBeNil)
		So(server != nil, ShouldBeTrue)

		defer server.Stop(ctx, true)

		So(waitUntilRecovered(server), ShouldBeTrue)

		// primary assertion: the recovered running job's schedulerGroup is
		// recomputed from its Requirements+LimitGroups, matching the scheduler
		// cmd identity, and is NOT the empty group that gob-decoding leaves.
		item, errg := server.q.Get(runningKey)
		So(errg, ShouldBeNil)
		So(item != nil, ShouldBeTrue)

		recovered, ok := item.Data().(*Job)
		So(ok, ShouldBeTrue)
		So(recovered.getSchedulerGroup(), ShouldNotEqual, "")
		So(recovered.getSchedulerGroup(), ShouldEqual, wantGroup)

		// end-to-end accounting assertion: accountForRunningJobs must bucket the
		// recovered running job under its real group (so a ready sibling there
		// gets a runner requested), not under the empty group "".
		Convey("and accountForRunningJobs counts it under the real group, not the empty group", func() {
			server.psgmutex.Lock()
			groups := make(map[string]*sgroup)
			server.accountForRunningJobs(server.q, groups)
			server.psgmutex.Unlock()

			So(groups[""], ShouldBeNil)
			So(groups[wantGroup], ShouldNotBeNil)
			So(groups[wantGroup].count, ShouldBeGreaterThanOrEqualTo, 1)
		})
	})
}

// recoverySchedGroupServerConfig returns a test ServerConfig set to preserve its
// DB across a restart (dontWipeDevDB), so a job persisted before serve() is
// recovered rather than wiped. Only the ServerConfig is needed here, so the other
// jobqueueTestInit return values are intentionally discarded.
func recoverySchedGroupServerConfig() ServerConfig {
	_, serverConfig, _, _, _ := jobqueueTestInit(true) //nolint:dogsled
	serverConfig.dontWipeDevDB = true

	return serverConfig
}

// persistRunningJobForRecovery stores a single JobStateRunning job with the given
// Requirements and LimitGroups into serverConfig's live DB, then closes the DB so
// a subsequent serve() recovers it. It returns the job's Key(). The job is stored
// with the normal store path (populating indices) but with State pre-set to
// Running, mirroring how a crashed manager leaves an in-flight job in jobslive.
func persistRunningJobForRecovery(ctx context.Context, t *testing.T, serverConfig ServerConfig,
	req *scheduler.Requirements, limitGroups []string) string {
	t.Helper()

	testDB, _, err := initDB(ctx, serverConfig.DBFile, serverConfig.DBFileBackup, internal.Development, false, false)
	So(err, ShouldBeNil)

	job := &Job{
		Cmd:          "sleep 3600 # reliable3-recover-schedgroup",
		Cwd:          defaultUploadDir,
		ReqGroup:     "reliable3-recover",
		RepGroup:     recSchedGroupRepGroup,
		Requirements: req,
		LimitGroups:  limitGroups,
		State:        JobStateRunning,
		Host:         "recover-host",
		HostID:       "recover-host-id",
	}

	jobsToQueue, jobsToUpdate, alreadyAdded, err := testDB.storeNewJobs(ctx, []*Job{job}, false)
	So(err, ShouldBeNil)
	So(jobsToQueue, ShouldHaveLength, 1)
	So(jobsToUpdate, ShouldHaveLength, 0)
	So(alreadyAdded, ShouldEqual, 0)

	So(testDB.close(ctx), ShouldBeNil)

	return job.Key()
}
