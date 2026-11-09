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

package jobqueue

// The generator of developers/soak/dbstart's golden test data (spec F3): the
// same two psimjobs written through a bbolt store and through a log store, so
// that dbstart before F3 (which read bbolt) can print golden.tsv from the
// first and the test can check dbstart over the second prints the same:
//
//   CGO_ENABLED=1 WR_DBSTART_FIXTURE=<new dir> go test -tags "netgo reliability_repro" \
//     ./jobqueue -run 'TestWriteDBStartFixture' -v
//
// writes <dir>/bolt.db and the log store <dir>/store.

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/VertebrateResequencing/wr/internal"
	jqs "github.com/VertebrateResequencing/wr/jobqueue/scheduler"
	. "github.com/smartystreets/goconvey/convey"
)

const (
	dbstartFixtureStartMs = 1700000000123
	dbstartFixtureEndMs   = 1700000004567
)

func TestWriteDBStartFixture(t *testing.T) {
	if runnermode || servermode {
		return
	}

	dir := os.Getenv("WR_DBSTART_FIXTURE")
	if dir == "" {
		t.Skip("set WR_DBSTART_FIXTURE to the directory to write dbstart's fixture stores in")
	}

	ctx := context.Background()

	Convey("dbstart's fixture is written through a bbolt and a log store", t, func() {
		So(os.MkdirAll(dir, 0o700), ShouldBeNil)

		writeDBStartFixture(ctx, filepath.Join(dir, "bolt.db"), storeKindBolt)
		writeDBStartFixture(ctx, filepath.Join(dir, "store"), storeKindLog)
	})
}

// writeDBStartFixture writes to a new store of kind at dbFile one live psimjob
// reserved on host h1 as pid 7, and one complete psimjob that exited 0.
func writeDBStartFixture(ctx context.Context, dbFile string, kind storeKind) {
	s, _, err := openStore(ctx, storeConfig{dbFile: dbFile, dbBkFile: dbFile + ".bk",
		deployment: internal.Development, kind: kind})
	So(err, ShouldBeNil)

	reserved, complete := dbstartFixtureJob("portal 12"), dbstartFixtureJob("compress 3")

	_, err = s.storeNewJobsGuarded(ctx, []*Job{reserved, complete}, false, nil)
	So(err, ShouldBeNil)

	reserved.State, reserved.Host, reserved.Pid, reserved.Attempts = JobStateReserved, "h1", 7, 1
	commit, err := s.reserveRunState(reserved)
	So(err, ShouldBeNil)

	done, err := commit.WaitWithin(time.Minute)
	So(err, ShouldBeNil)
	So(done, ShouldBeTrue)

	complete.State, complete.Exited, complete.Exitcode, complete.Attempts = JobStateComplete, true, 0, 1
	complete.Host, complete.Pid = "h2", 9
	complete.StartTime, complete.EndTime = time.UnixMilli(dbstartFixtureStartMs), time.UnixMilli(dbstartFixtureEndMs)
	_, err = s.archiveCompletion(complete.Key(), complete)
	So(err, ShouldBeNil)

	So(s.close(ctx), ShouldBeNil)
}

// dbstartFixtureJob returns a psimjob.sh job of args (its kind and id).
func dbstartFixtureJob(args string) *Job {
	return &Job{
		Cmd:          "/x/psimjob.sh " + args,
		Cwd:          "/x",
		ReqGroup:     "dbstart",
		RepGroup:     "dbstart",
		Requirements: &jqs.Requirements{RAM: 10, Time: time.Second, Cores: 1, Other: map[string]string{}},
	}
}
