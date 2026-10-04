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

// Once Job gained its first omitempty field (ContainerImageUser, #590), every
// encode of a Job by a new codec.Encoder allocated a 2 KiB scratch slice for the
// struct's fields, which with the Encoder itself put about 2.5 KiB more per job
// on the add, state-change and archive paths. These tests pin that storing a job
// no longer pays for that, and that the encoding it stores is unchanged.

import (
	"context"
	"path/filepath"
	"runtime"
	"slices"
	"testing"
	"time"

	"github.com/VertebrateResequencing/wr/internal"
	. "github.com/smartystreets/goconvey/convey"
	"github.com/ugorji/go/codec"
)

// jobEncodeMaxBytes bounds what encoding testDBJob's job for storage may
// allocate. Reusing an Encoder leaves only the growth of the encoded output,
// about 2.3 KiB. A new Encoder per encode adds the Encoder itself and its field
// scratch: a 32 byte {*structFieldInfo, reflect.Value} entry per encoded field,
// in a slice rounded up to a power of 2 (64 entries, 2 KiB, for Job), for about
// 4.8 KiB in all.
const jobEncodeMaxBytes = 3 * 1024

// jobEncodeRuns is how many encodes jobEncodeMedianBytes measures.
const jobEncodeRuns = 200

func TestDBEncodeJob(t *testing.T) {
	if runnermode || servermode {
		return
	}

	Convey("Given a db", t, func() {
		ctx := context.Background()
		tmpdir := t.TempDir()

		testDB, _, err := initDB(
			ctx,
			filepath.Join(tmpdir, "queue.db"),
			filepath.Join(tmpdir, "queue.db.bak"),
			internal.Development,
			false,
			false,
		)
		So(err, ShouldBeNil)

		defer func() {
			So(testDB.close(ctx), ShouldBeNil)
		}()

		Convey("encoding a job does not allocate a new encoder's field scratch", func() {
			job := testDBJob("echo encode alloc", "encode")

			So(jobEncodeMedianBytes(testDB, job), ShouldBeLessThan, jobEncodeMaxBytes)
		})

		Convey("encoding jobs one after another stores what a new encoder would", func() {
			flagged := testDBJob("echo encode flagged", "encode")
			flagged.WithDocker = testContainerImage
			flagged.ContainerImageUser = true
			flagged.RerunAfterRun = true
			flagged.RunnerReservation = 7
			flagged.ReadyTime = time.Date(2026, 10, 2, 9, 0, 0, 0, time.UTC)

			plain := testDBJob("echo encode plain", "encode")

			for _, job := range []*Job{flagged, plain, flagged, plain} {
				encoded, errE := testDB.encodeJob(job)
				So(errE, ShouldBeNil)
				So(encoded, ShouldResemble, encodeWithDBHandle(testDB, job))
			}

			encoded, err := testDB.encodeJob(flagged)
			So(err, ShouldBeNil)

			decoded := &Job{}
			So(codec.NewDecoderBytes(encoded, testDB.ch).Decode(decoded), ShouldBeNil)
			So(decoded.Key(), ShouldEqual, flagged.Key())
			So(decoded.ContainerImageUser, ShouldBeTrue)
			So(decoded.RerunAfterRun, ShouldBeTrue)
			So(decoded.RunnerReservation, ShouldEqual, flagged.RunnerReservation)
			So(decoded.ReadyTime.Equal(flagged.ReadyTime), ShouldBeTrue)
			So(decoded.Cmd, ShouldEqual, flagged.Cmd)
			So(decoded.RepGroup, ShouldEqual, flagged.RepGroup)
			So(decoded.Requirements, ShouldResemble, flagged.Requirements)
		})
	})
}

// jobEncodeMedianBytes returns the median bytes allocated by one of
// jobEncodeRuns encodes of job for storage, on 1 P, after a warm-up encode.
//
// It takes the median, not the mean, because under -race sync.Pool.Put drops a
// quarter of what it is given, so about one encode in four makes a new Encoder:
// that pushes the mean towards jobEncodeMaxBytes and, by chance, over it. The
// median stays at the cost of a reused Encoder unless at least half the encodes
// make a new one, which is what happens every time if encode stops reusing them.
func jobEncodeMedianBytes(testDB *db, job *Job) uint64 {
	defer runtime.GOMAXPROCS(runtime.GOMAXPROCS(1))

	encode := func() {
		if _, err := testDB.encodeJob(job); err != nil {
			panic(err)
		}
	}

	encode()

	var before, after runtime.MemStats

	allocated := make([]uint64, jobEncodeRuns)

	for i := range allocated {
		runtime.ReadMemStats(&before)
		encode()
		runtime.ReadMemStats(&after)

		allocated[i] = after.TotalAlloc - before.TotalAlloc
	}

	slices.Sort(allocated)

	return allocated[jobEncodeRuns/2]
}
