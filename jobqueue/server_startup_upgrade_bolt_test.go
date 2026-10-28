//go:build !windows

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
	"os"
	"testing"
	"time"

	"github.com/VertebrateResequencing/wr/internal"
	. "github.com/smartystreets/goconvey/convey"
	bolt "go.etcd.io/bbolt"
)

// This file holds the startup test whose database needs the dep-group upgrade,
// made by deleting bbolt buckets, so is deleted with bbolt (spec F5).

// TestServeReportsPostUpgradeStartupUntilTokenReady covers E4 acceptance test 7.
// It keeps its name because prepareDBNeedingStartupUpgrade is still its fixture,
// but the FIFO-backed token now holds PUBLICATION rather than Serve, so the
// sidecar assertion moves from "gone when Serve returns" to "gone when
// <-server.Serving() returns".
//
// It cannot assert postUpgradeStartupDetail in that window. The FIFO blocks at
// persistToken, which is publication step 2, and by then the startup phase writes
// have moved the sidecar on three times, the last of them to
// DBStartupRecoveryState. Nor is there any seam between initDB and
// startPriorStateRecovery at which the upgrade detail is guaranteed to still be
// readable. So it matches on State: the recovery phase's Detail is an elapsed
// time that changes on every heartbeat tick.
func TestServeReportsPostUpgradeStartupUntilTokenReady(t *testing.T) {
	Convey("Serve keeps a live startup-progress sidecar after a DB upgrade until the token is ready", t, func() {
		ctx := context.Background()
		_, serverConfig, _, _, _ := jobqueueTestInit(true)
		serverConfig.dontWipeDevDB = true

		prepareDBNeedingStartupUpgrade(ctx, t, serverConfig)
		initialTokenWritten := prepareFIFOBackedToken(t, serverConfig.TokenFile)

		serveResult := make(chan serveStartupResult, 1)

		go func() {
			server, _, token, err := Serve(ctx, serverConfig)
			serveResult <- serveStartupResult{server: server, token: token, err: err}
		}()

		So(waitForFIFOInitialToken(initialTokenWritten), ShouldBeNil)

		status, found := waitForDBUpgradeStatusState(
			serverConfig.DBFile,
			internal.DBStartupRecoveryState,
			2*time.Second,
		)

		tokenInfo, statErr := os.Stat(serverConfig.TokenFile)
		So(statErr, ShouldBeNil)
		So(tokenInfo.Size(), ShouldEqual, 0)
		So(found, ShouldBeTrue)
		So(status.State, ShouldEqual, internal.DBStartupRecoveryState)
		So(status.PID, ShouldEqual, os.Getpid())

		tokenRead := readFIFOAsync(serverConfig.TokenFile)

		result := waitForServeStartup(t, serveResult)
		if result.server != nil {
			defer result.server.Stop(ctx, true)
		}

		So(result.err, ShouldBeNil)

		tokenReadResult := waitForFIFORead(t, serverConfig.TokenFile, tokenRead)
		So(tokenReadResult.err, ShouldBeNil)
		So(tokenReadResult.payload, ShouldResemble, result.token)

		So(waitForServing(result.server), ShouldBeTrue)

		_, _, err := internal.ReadDBUpgradeStatus(serverConfig.DBFile)
		So(os.IsNotExist(err), ShouldBeTrue)
	})
}

func prepareDBNeedingStartupUpgrade(ctx context.Context, t *testing.T, config ServerConfig) {
	t.Helper()

	testDB, _, err := initDB(ctx, config.DBFile, config.DBFileBackup, internal.Development, false, false)
	So(err, ShouldBeNil)

	parent := testDBJob("echo parent", "startup-upgrade-parent")
	parent.DepGroups = []string{"startup-upgrade-parent-dg"}

	child := testDBJob("echo child", "startup-upgrade-child")
	child.Dependencies = Dependencies{NewDepGroupDependency("startup-upgrade-parent-dg")}

	jobsToQueue, jobsToUpdate, alreadyAdded, err := testDB.storeNewJobs(ctx, []*Job{parent, child}, false)
	So(err, ShouldBeNil)
	So(jobsToQueue, ShouldHaveLength, 2)
	So(jobsToUpdate, ShouldHaveLength, 0)
	So(alreadyAdded, ShouldResemble, DuplicateBreakdown{})

	err = testDB.bolt.Update(func(tx *bolt.Tx) error {
		if errd := tx.DeleteBucket(bucketDepGroups); errd != nil {
			return errd
		}

		return tx.DeleteBucket(bucketJobLookupEntries)
	})
	So(err, ShouldBeNil)
	So(testDB.close(ctx), ShouldBeNil)
}
