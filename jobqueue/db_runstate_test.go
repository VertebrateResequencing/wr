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
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"hash/crc32"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/VertebrateResequencing/wr/jobqueue/scheduler"
	"github.com/gofrs/uuid/v5"
	. "github.com/smartystreets/goconvey/convey"
	"github.com/ugorji/go/codec"
	bolt "go.etcd.io/bbolt"
)

const runStateTestRepGroup = "runstate_supersede"

func TestRunStateRecord(t *testing.T) {
	Convey("Given the example job and its full encoding", t, func() {
		d := &db{ch: new(codec.BincHandle)}
		job := runStateExampleJob()

		live, err := d.encode(job)
		So(err, ShouldBeNil)

		encodedRunState, err := d.encode(newJobRunState(job))
		So(err, ShouldBeNil)

		record := runStateRecord(live, encodedRunState)

		Convey("the run-state record is at most 1,024 bytes over a live record of at least 10,000", func() {
			So(len(record), ShouldBeLessThanOrEqualTo, 1024)
			So(len(live), ShouldBeGreaterThanOrEqualTo, 10000)
		})

		Convey("the record is the big-endian CRC-32C of live followed by the encoded run state", func() {
			So(binary.BigEndian.Uint32(record), ShouldEqual, crc32.Checksum(live, crc32.MakeTable(crc32.Castagnoli)))
			So(record[4:], ShouldResemble, encodedRunState)
		})

		Convey("the record matches live, and applying it to the older job gives the example job", func() {
			got, ok := runStateOver(live, record)
			So(ok, ShouldBeTrue)

			var rs jobRunState
			So(codec.NewDecoderBytes(got, d.ch).Decode(&rs), ShouldBeNil)

			olderEncoded, err := d.encode(runStateOlderJob())
			So(err, ShouldBeNil)

			older, err := d.decodeJob(olderEncoded)
			So(err, ShouldBeNil)

			rs.applyTo(older)

			applied, err := d.encode(older)
			So(err, ShouldBeNil)
			So(applied, ShouldResemble, live)
		})

		Convey("the record does not match live changed in one byte", func() {
			changed := append([]byte(nil), live...)
			changed[len(changed)/2] ^= 0xff

			got, ok := runStateOver(changed, record)
			So(ok, ShouldBeFalse)
			So(got, ShouldBeNil)
		})

		Convey("a record shorter than 4 bytes matches nothing", func() {
			for _, short := range [][]byte{nil, {}, {1}, {1, 2, 3}} {
				got, ok := runStateOver(live, short)
				So(ok, ShouldBeFalse)
				So(got, ShouldBeNil)
			}
		})

		Convey("a 4-byte record whose CRC matches gives an empty body", func() {
			got, ok := runStateOver(live, record[:4])
			So(ok, ShouldBeTrue)
			So(got, ShouldBeEmpty)
		})
	})

	Convey("Given a jobRunState with every field zero, applying it to the example job zeroes every run-state field",
		t, func() {
			job := runStateExampleJob()

			var rs jobRunState
			rs.applyTo(job)

			jv := reflect.ValueOf(job).Elem()
			nonZero := []string{}

			for _, field := range reflect.VisibleFields(reflect.TypeFor[jobRunState]()) {
				if !jv.FieldByName(field.Name).IsZero() {
					nonZero = append(nonZero, field.Name)
				}
			}

			So(nonZero, ShouldBeEmpty)
			So(job.Lost, ShouldBeFalse)
			So(job.Requirements, ShouldBeNil)
			So(job.RequirementsOrig, ShouldBeNil)
		})

	Convey("applyTo makes the job's scheduler view use the applied Requirements", t, func() {
		job := runStateExampleJob()
		So(job.schedulerGroupSnapshot().requirements.RAM, ShouldEqual, 2000)

		rs := newJobRunState(job)
		rs.Requirements.RAM = 3000
		rs.applyTo(job)

		So(job.schedulerGroupSnapshot().requirements.RAM, ShouldEqual, 3000)
	})

	Convey("Every jobRunState field is a Job field of the same name and type, and copies round trip", t, func() {
		jobType := reflect.TypeFor[Job]()
		fields := reflect.VisibleFields(reflect.TypeFor[jobRunState]())
		mismatched := []string{}

		for _, field := range fields {
			jf, found := jobType.FieldByName(field.Name)
			if !found || !jf.IsExported() || jf.Type != field.Type {
				mismatched = append(mismatched, field.Name)
			}
		}

		So(mismatched, ShouldBeEmpty)

		src := &Job{}
		sv := reflect.ValueOf(src).Elem()

		var n uint8

		for _, field := range fields {
			n++
			sv.FieldByName(field.Name).Set(distinctRunStateValue(t, field.Type, n))
		}

		dst := &Job{}
		rs := newJobRunState(src)
		rs.applyTo(dst)

		dv := reflect.ValueOf(dst).Elem()
		unequal := []string{}

		for _, field := range fields {
			if !reflect.DeepEqual(dv.FieldByName(field.Name).Interface(), sv.FieldByName(field.Name).Interface()) {
				unequal = append(unequal, field.Name)
			}
		}

		So(unequal, ShouldBeEmpty)

		wantRAM, wantOrigRAM := dst.Requirements.RAM, dst.RequirementsOrig.RAM
		wantOther := fmt.Sprint(dst.Requirements.Other)
		wantOrigOther := fmt.Sprint(dst.RequirementsOrig.Other)

		src.Requirements.RAM++
		src.Requirements.Other["added"] = "1"
		src.RequirementsOrig.RAM++
		src.RequirementsOrig.Other["added"] = "1"

		So(dst.Requirements.RAM, ShouldEqual, wantRAM)
		So(fmt.Sprint(dst.Requirements.Other), ShouldEqual, wantOther)
		So(dst.RequirementsOrig.RAM, ShouldEqual, wantOrigRAM)
		So(fmt.Sprint(dst.RequirementsOrig.Other), ShouldEqual, wantOrigOther)
	})
}

// runStateOlderJob returns spec A1's older record of the example job: the same
// Cmd, with a different value in every jobRunState field.
func runStateOlderJob() *Job {
	example := runStateExampleJob()
	dayEarlier := example.StartTime.Add(-24 * time.Hour)

	return &Job{
		Cmd:               example.Cmd,
		State:             JobStateReserved,
		Exited:            true,
		Exitcode:          3,
		FailReason:        "ram",
		Lost:              true,
		Pid:               1,
		RunnerPid:         2,
		Host:              "old",
		HostID:            "oldid",
		HostIP:            "10.0.0.1",
		ActualCwd:         "/old",
		StartTime:         dayEarlier,
		EndTime:           dayEarlier,
		PeakRAM:           900,
		PeakDisk:          9,
		CPUtime:           time.Second,
		StdOutC:           []byte("o"),
		StdErrC:           []byte("old"),
		ReservedBy:        uuid.Must(uuid.FromString("99999999-8888-4777-8666-555555555555")),
		RunnerReservation: 4,
		Attempts:          0,
		DelayTime:         5 * time.Second,
		Requirements:      &scheduler.Requirements{RAM: 100},
		RequirementsOrig:  nil,
		RerunAfterRun:     true,
	}
}

// runStateExampleJob returns spec A1's example job.
func runStateExampleJob() *Job {
	return &Job{
		Cmd:               strings.Repeat("x", 10000),
		State:             JobStateRunning,
		Host:              "node-1-2-3.internal.sanger.ac.uk",
		HostIP:            "172.27.71.182",
		HostID:            "0e0b1c2d-3e4f-4a5b-8c6d-7e8f9a0b1c2d",
		ActualCwd:         "/" + strings.Repeat("c", 99),
		Pid:               7,
		RunnerPid:         6,
		Attempts:          1,
		RunnerReservation: 5,
		ReservedBy:        uuid.Must(uuid.FromString("11111111-2222-4333-8444-555555555555")),
		StartTime:         time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC),
		Requirements: &scheduler.Requirements{
			RAM: 2000, Time: 2 * time.Hour, Cores: 1, Disk: 10, DiskSet: true,
		},
		RequirementsOrig: &scheduler.Requirements{RAM: 1000, Time: time.Hour, Disk: 10, DiskSet: true},
	}
}

// distinctRunStateValue returns a non-zero value of type typ that differs for
// each n, failing the test for a type it does not know, so a jobRunState field
// of a new type must be given a value here.
func distinctRunStateValue(t *testing.T, typ reflect.Type, n uint8) reflect.Value {
	t.Helper()

	switch typ {
	case reflect.TypeFor[time.Time]():
		return reflect.ValueOf(time.Date(2026, 1, int(n), 0, 0, 0, 0, time.UTC))
	case reflect.TypeFor[uuid.UUID]():
		id := uuid.UUID{n}

		return reflect.ValueOf(id)
	case reflect.TypeFor[[]byte]():
		return reflect.ValueOf([]byte{n})
	case reflect.TypeFor[*scheduler.Requirements]():
		return reflect.ValueOf(&scheduler.Requirements{
			RAM: int(n), Time: time.Duration(n), Cores: float64(n), Disk: int(n),
			Other: map[string]string{"n": strconv.Itoa(int(n))}, CoresSet: true, DiskSet: true, OtherSet: true,
		})
	}

	v := reflect.New(typ).Elem()

	switch typ.Kind() {
	case reflect.Bool:
		v.SetBool(true)
	case reflect.Int, reflect.Int64:
		v.SetInt(int64(n))
	case reflect.Uint32, reflect.Uint64:
		v.SetUint(uint64(n))
	case reflect.String:
		v.SetString("s" + strconv.Itoa(int(n)))
	default:
		t.Fatalf("no distinct value for jobRunState field type %s", typ)
	}

	return v
}

func TestFullWritesSupersedeRunState(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given a live job with a committed matching run-state record of it running", t, func() {
		database, job := runStateLiveDB(t, ctx)
		defer func() { _ = database.close(ctx) }()

		key := job.Key()

		Convey("archiving it leaves its complete record and neither a live nor a run-state record", func() {
			archiveRunStateJob(database, job)

			So(runStateBucketValue(t, database, bucketJobsComplete, key), ShouldNotBeNil)
			So(runStateBucketValue(t, database, bucketJobsLive, key), ShouldBeNil)
			So(runStateBucketValue(t, database, bucketJobRunState, key), ShouldBeNil)
		})

		Convey("archiving it kept live for a rerun leaves rerunRecords' live record and no run-state record", func() {
			job.RerunAfterRun = true

			encoded, err := database.encodeJob(job)
			So(err, ShouldBeNil)

			_, want, err := database.rerunRecords(encoded)
			So(err, ShouldBeNil)

			outcome, err := database.archiveCompletion(key, job)
			So(err, ShouldBeNil)
			So(outcome, ShouldEqual, archiveKeptLive)

			live := runStateBucketValue(t, database, bucketJobsLive, key)
			So(live, ShouldResemble, want)

			stored, err := database.decodeJob(live)
			So(err, ShouldBeNil)
			So(stored.ReservedBy, ShouldEqual, uuid.UUID{})
			So(runStateBucketValue(t, database, bucketJobRunState, key), ShouldBeNil)
		})

		Convey("deleteLiveJobs removing it leaves no run-state record", func() {
			So(database.deleteLiveJobs(ctx, []string{key}), ShouldBeNil)

			So(runStateBucketValue(t, database, bucketJobsLive, key), ShouldBeNil)
			So(runStateBucketValue(t, database, bucketJobRunState, key), ShouldBeNil)
		})

		Convey("modifyLiveJobs replacing it under a new key leaves neither key a run-state record", func() {
			modified := testDBJob("echo runstate supersede modified", runStateTestRepGroup)
			newKey := modified.Key()
			So(newKey, ShouldNotEqual, key)

			putRunStateFixture(t, database, newKey, []byte("an orphaned record under the new key"))

			So(database.modifyLiveJobs(ctx, []string{key}, []*Job{modified}), ShouldBeNil)

			So(runStateBucketValue(t, database, bucketJobsLive, newKey), ShouldNotBeNil)
			So(runStateBucketValue(t, database, bucketJobRunState, key), ShouldBeNil)
			So(runStateBucketValue(t, database, bucketJobRunState, newKey), ShouldBeNil)
		})

		Convey("storeRunningRerunMarks writing its mark leaves RerunAfterRun stored and no run-state record", func() {
			job.RerunAfterRun = true

			So(database.storeRunningRerunMarks([]*Job{job}), ShouldBeNil)

			stored, err := database.decodeJob(runStateBucketValue(t, database, bucketJobsLive, key))
			So(err, ShouldBeNil)
			So(stored.RerunAfterRun, ShouldBeTrue)
			So(runStateBucketValue(t, database, bucketJobRunState, key), ShouldBeNil)
		})

		Convey("storeLiveForRerun storing it leaves no run-state record", func() {
			So(database.storeLiveForRerun([]*Job{job}), ShouldBeNil)

			So(runStateBucketValue(t, database, bucketJobsLive, key), ShouldNotBeNil)
			So(runStateBucketValue(t, database, bucketJobRunState, key), ShouldBeNil)
		})

		Convey("an add's dependent put-back restoring it archived, with an orphaned run-state record, removes that record",
			func() {
				archiveRunStateJob(database, job)
				putRunStateFixture(t, database, key, []byte("an orphaned record"))

				putBack := make(map[string]bool)
				err := database.bolt.Update(func(tx *bolt.Tx) error {
					return database.putBackArchivedDependentsTx(tx, []string{key}, putBack)
				})
				So(err, ShouldBeNil)
				So(putBack[key], ShouldBeTrue)

				So(runStateBucketValue(t, database, bucketJobsLive, key), ShouldNotBeNil)
				So(runStateBucketValue(t, database, bucketJobRunState, key), ShouldBeNil)
			})

		Convey("a best-effort full change of it leaves no run-state record", func() {
			So(database.updateJobAfterChangeDurable(job), ShouldBeNil)

			So(storedLiveJobState(t, database, key), ShouldEqual, JobStateRunning)
			So(runStateBucketValue(t, database, bucketJobRunState, key), ShouldBeNil)
		})

		Convey("a best-effort exit of it leaves no run-state record", func() {
			job.State = JobStateBuried
			job.Exited = true
			job.Exitcode = 1

			waiter := make(chan error, 1)
			So(database.queueJobExit(job, nil, nil, false, waiter), ShouldBeNil)
			So(<-waiter, ShouldBeNil)

			So(storedLiveJobState(t, database, key), ShouldEqual, JobStateBuried)
			So(runStateBucketValue(t, database, bucketJobRunState, key), ShouldBeNil)
		})
	})
}

// runStateLiveDB returns a database holding a job's add-time live record and a
// committed run-state record, matching it, of the job running, along with that
// running job.
func runStateLiveDB(t *testing.T, ctx context.Context) (*db, *Job) {
	t.Helper()

	database := openReliable4WriteStormDB(t, ctx)

	job := testDBJob("echo runstate supersede", runStateTestRepGroup)
	job.State = JobStateReady

	if _, _, _, err := database.storeNewJobs(ctx, []*Job{job}, false); err != nil {
		t.Fatalf("storeNewJobs failed: %v", err)
	}

	job.State = JobStateRunning
	job.Attempts = 1
	job.StartTime = time.Now()
	job.Host = reserveDurabilityHost
	job.Pid = 7
	job.ReservedBy = uuid.Must(uuid.FromString("11111111-2222-4333-8444-555555555555"))

	encodedRunState, err := database.encode(newJobRunState(job))
	if err != nil {
		t.Fatalf("encoding the run state failed: %v", err)
	}

	key := job.Key()
	live := runStateBucketValue(t, database, bucketJobsLive, key)
	putRunStateFixture(t, database, key, runStateRecord(live, encodedRunState))

	return database, job
}

// archiveRunStateJob archives job as a successful completion that is not kept
// live.
func archiveRunStateJob(database *db, job *Job) {
	job.State = JobStateComplete
	job.Exited = true
	job.EndTime = time.Now()

	outcome, err := database.archiveCompletion(job.Key(), job)
	So(err, ShouldBeNil)
	So(outcome, ShouldEqual, archiveRemovedLive)
}

func TestAddKeepsHandedOutRunState(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given a job's live add-time record, State ready with no attempts", t, func() {
		database, job, live := runStateAddDB(t, ctx)
		defer func() { _ = database.close(ctx) }()

		key := job.Key()

		fresh := testDBJob(job.Cmd, "runstate_second_add")
		fresh.State = JobStateReady
		fresh.Priority = 5

		freshRecord, err := database.encodeJob(fresh)
		So(err, ShouldBeNil)
		So(freshRecord, ShouldNotResemble, live)

		reserved := runStateReservedCopy(job)

		encodedReserved, err := database.encode(newJobRunState(reserved))
		So(err, ShouldBeNil)

		Convey("and a matching run-state record of it reserved, an add of a fresh copy leaves both records as they were",
			func() {
				record := runStateRecord(live, encodedReserved)
				putRunStateFixture(t, database, key, record)

				So(putNewLiveRecords(database, key, freshRecord), ShouldBeNil)

				So(runStateBucketValue(t, database, bucketJobsLive, key), ShouldResemble, live)
				So(runStateBucketValue(t, database, bucketJobRunState, key), ShouldResemble, record)
			})

		Convey("and a stale run-state record, an add of a fresh copy stores that copy and no run-state record", func() {
			stale := bytes.Clone(live)
			stale[len(stale)-1]++
			putRunStateFixture(t, database, key, runStateRecord(stale, encodedReserved))

			So(putNewLiveRecords(database, key, freshRecord), ShouldBeNil)

			So(runStateBucketValue(t, database, bucketJobsLive, key), ShouldResemble, freshRecord)
			So(runStateBucketValue(t, database, bucketJobRunState, key), ShouldBeNil)
		})

		Convey("and no live record but an orphaned run-state record, an add stores the job and no run-state record", func() {
			So(database.deleteLiveJobs(ctx, []string{key}), ShouldBeNil)
			putRunStateFixture(t, database, key, runStateRecord(live, encodedReserved))

			So(putNewLiveRecords(database, key, freshRecord), ShouldBeNil)

			So(runStateBucketValue(t, database, bucketJobsLive, key), ShouldResemble, freshRecord)
			So(runStateBucketValue(t, database, bucketJobRunState, key), ShouldBeNil)
		})

		Convey("and no run-state bucket at all, an add of a fresh copy stores that copy", func() {
			err := database.bolt.Update(func(tx *bolt.Tx) error {
				return tx.DeleteBucket(bucketJobRunState)
			})
			So(err, ShouldBeNil)

			So(putNewLiveRecords(database, key, freshRecord), ShouldBeNil)

			So(runStateBucketValue(t, database, bucketJobsLive, key), ShouldResemble, freshRecord)
		})

		Convey("a second add's fresh copy put before an undrained reservation's run state is drained over it "+
			"gives the reservation's run state on the second add's other fields", func() {
			So(putNewLiveRecords(database, key, freshRecord), ShouldBeNil)

			stored := runStateBucketValue(t, database, bucketJobsLive, key)
			So(stored, ShouldResemble, freshRecord)

			// stands in for the drain putting the reservation's queued run state
			putRunStateFixture(t, database, key, runStateRecord(stored, encodedReserved))

			encodedRunState, matches := runStateOver(stored,
				runStateBucketValue(t, database, bucketJobRunState, key))
			So(matches, ShouldBeTrue)

			var runState jobRunState
			So(codec.NewDecoderBytes(encodedRunState, database.ch).Decode(&runState), ShouldBeNil)

			recovered, err := database.decodeJob(stored)
			So(err, ShouldBeNil)
			runState.applyTo(recovered)

			So(recovered.State, ShouldEqual, JobStateReserved)
			So(recovered.Attempts, ShouldEqual, reserved.Attempts)
			So(recovered.ReservedBy, ShouldEqual, reserved.ReservedBy)
			So(recovered.RepGroup, ShouldEqual, fresh.RepGroup)
			So(recovered.Priority, ShouldEqual, fresh.Priority)
		})
	})
}

// runStateAddDB returns a database holding a job's add-time live record (State
// ready, Attempts 0), along with that job and record.
func runStateAddDB(t *testing.T, ctx context.Context) (*db, *Job, []byte) {
	t.Helper()

	database := openReliable4WriteStormDB(t, ctx)

	job := testDBJob("echo runstate add", runStateTestRepGroup)
	job.State = JobStateReady

	if _, _, _, err := database.storeNewJobs(ctx, []*Job{job}, false); err != nil {
		t.Fatalf("storeNewJobs failed: %v", err)
	}

	return database, job, runStateBucketValue(t, database, bucketJobsLive, job.Key())
}

// runStateReservedCopy returns a Job holding the run state a reservation of job
// would give it.
func runStateReservedCopy(job *Job) *Job {
	return &Job{
		State:             JobStateReserved,
		Attempts:          1,
		RunnerReservation: 3,
		ReservedBy:        uuid.Must(uuid.FromString("11111111-2222-4333-8444-555555555555")),
		Requirements:      job.Requirements,
	}
}

// putRunStateFixture puts record as key's run-state record directly.
func putRunStateFixture(t *testing.T, database *db, key string, record []byte) {
	t.Helper()

	err := database.bolt.Update(func(tx *bolt.Tx) error {
		return tx.Bucket(bucketJobRunState).Put([]byte(key), record)
	})
	if err != nil {
		t.Fatalf("putting the run-state record failed: %v", err)
	}
}

// putNewLiveRecords has putNewLiveJobs store record as key's live record, as
// an add does.
func putNewLiveRecords(database *db, key string, record []byte) error {
	return database.bolt.Update(func(tx *bolt.Tx) error {
		return database.putNewLiveJobs(tx, bucketJobsLive, sobsd{{[]byte(key), record}})
	})
}

// runStateBucketValue returns a copy of key's value in bucket, or nil if it has
// none.
func runStateBucketValue(t *testing.T, database *db, bucket []byte, key string) []byte {
	t.Helper()

	var value []byte

	err := database.bolt.View(func(tx *bolt.Tx) error {
		value = bytes.Clone(tx.Bucket(bucket).Get([]byte(key)))

		return nil
	})
	if err != nil {
		t.Fatalf("reading bucket %s failed: %v", bucket, err)
	}

	return value
}
