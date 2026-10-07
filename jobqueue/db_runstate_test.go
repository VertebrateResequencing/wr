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
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/VertebrateResequencing/wr/clog"
	"github.com/VertebrateResequencing/wr/jobqueue/scheduler"
	"github.com/VertebrateResequencing/wr/queue"
	"github.com/gofrs/uuid/v5"
	. "github.com/smartystreets/goconvey/convey"
	"github.com/ugorji/go/codec"
	bolt "go.etcd.io/bbolt"
)

const runStateTestRepGroup = "runstate_supersede"

// runStateBigCmdBytes is how long the reservation tests make a job's Cmd, so
// its full record is at least that long.
const runStateBigCmdBytes = 10000

// runStateDuplicateRunnerPid is the runner pid a re-sent start reports.
const runStateDuplicateRunnerPid = 4242

// TestReservationLeavesWaitingForDepGroupsToRecovery checks that a
// reservation does not store WaitingForDepGroups, and recovery re-derives it,
// so a stale stored value is not what a recovered job waits on.
func TestReservationLeavesWaitingForDepGroupsToRecovery(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given a job with no dependencies whose stored record says it waits on dep group g", t, func() {
		f := newMovedOnFixture(ctx, t, "")
		defer f.stop(ctx)

		f.add(movedOnRetries, nil, "waits")

		key := (&Job{Cmd: restFormTrue + " movedon waits", Cwd: testCwd}).Key()

		So(rewriteStoredLiveJob(f.server.db, key, func(job *Job) {
			job.WaitingForDepGroups = []string{"g"}
		}), ShouldBeNil)
		So(storedLiveJob(t, f.server.db, key).WaitingForDepGroups, ShouldResemble, []string{"g"})

		Convey("once it is reserved and the manager crashes, it is recovered waiting on nothing", func() {
			So(f.reserve().Key(), ShouldEqual, key)

			f.crashOnto(ctx, f.backup())

			recovered := preStartServerJob(f.server, key)
			So(recovered, ShouldNotBeNil)

			recovered.RLock()
			defer recovered.RUnlock()

			So(recovered.WaitingForDepGroups, ShouldBeEmpty)
		})
	})
}

// TestReservationStoresClearedRerunMark checks that a job recovered out of the
// run sub-queue has its stale mark to run again cleared in memory, and its
// next reservation stores that, so a crash while it then runs does not recover
// it marked, and it runs only once.
func TestReservationStoresClearedRerunMark(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given a crash image in which a job is lost with its stored mark to run again", t, func() {
		f := newMovedOnFixture(ctx, t, "")
		defer f.stop(ctx)

		dir := t.TempDir()
		marker := filepath.Join(dir, "runs")
		stopFile := filepath.Join(dir, "stop")
		cmd := startDurabilityCmd(marker, stopFile)

		f.add(movedOnRetries, func(job *Job) { job.Cmd = cmd }, "rerun")

		key := (&Job{Cmd: cmd, Cwd: testCwd}).Key()

		So(rewriteStoredLiveJob(f.server.db, key, func(job *Job) {
			job.State = JobStateLost
			job.Lost = true
			job.RerunAfterRun = true
		}), ShouldBeNil)

		f.crashOnto(ctx, f.backup())

		Convey("once recovered, reserved, started and recovered again, it is running unmarked and runs once", func() {
			reserved := f.reserve()
			So(reserved.Key(), ShouldEqual, key)

			run := exec.CommandContext(ctx, "sh", "-c", cmd)
			So(run.Start(), ShouldBeNil)

			defer func() {
				_ = os.WriteFile(stopFile, nil, 0o600) //nolint:errcheck // best-effort test cleanup
				_ = run.Wait()                         //nolint:errcheck // best-effort test cleanup
			}()

			So(waitForRuns(marker, 1, reserveDurabilityFirstRunWait), ShouldBeTrue)
			So(f.runner.Started(reserved, run.Process.Pid), ShouldBeNil)

			f.crashOnto(ctx, f.backup())

			recovered := preStartServerJob(f.server, key)
			So(recovered, ShouldNotBeNil)

			recovered.RLock()
			state, mark := recovered.State, recovered.RerunAfterRun
			recovered.RUnlock()

			So(state, ShouldEqual, JobStateRunning)
			So(mark, ShouldBeFalse)

			killCalled, err := f.runner.Touch(reserved)
			So(err, ShouldBeNil)
			So(killCalled, ShouldBeFalse)

			So(os.WriteFile(stopFile, nil, 0o600), ShouldBeNil)
			So(run.Wait(), ShouldBeNil)

			So(f.runner.Archive(reserved, &JobEndState{Exited: true, Exitcode: 0, EndTime: time.Now()}), ShouldBeNil)

			_, err = f.server.q.Get(key)
			So(err, ShouldNotBeNil)

			again, err := f.user.Reserve(time.Second)
			So(err, ShouldBeNil)
			So(again, ShouldBeNil)
			So(runCount(marker), ShouldEqual, 1)
		})
	})
}

// TestRunStateRecord checks a run-state record's size and layout, that it
// matches only the live record it was written over, that applying a run state
// sets every run-state field of a job, Requirements included, and that
// jobRunState's fields mirror Job's.
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

// runStateOlderJob returns an older record of the example job: the same Cmd,
// with a different value in every jobRunState field.
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

// runStateExampleJob returns the running job, with a 10,000-byte Cmd, whose
// run-state record the record tests build.
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

func TestRecoveryOverlaysRunState(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given 3 live jobs, 2 with matching run-state records of them running and 1 without", t, func() {
		database, jobs := runStateRecoveryDB(t, ctx)
		defer func() { _ = database.close(ctx) }()

		for _, job := range jobs[:2] {
			putRunStateFixture(t, database, job.Key(), runStateRunningRecord(t, database, job))
		}

		Convey("recoverIncompleteJobs returns the 2 overlaid and the third as its live record says", func() {
			recovered, rsr, err := database.recoverIncompleteJobs()
			So(err, ShouldBeNil)
			So(rsr.applied, ShouldEqual, 2)
			So(rsr.dropped, ShouldEqual, 0)
			So(len(recovered), ShouldEqual, 3)

			byKey := runStateJobsByKey(recovered)

			for _, job := range jobs[:2] {
				got := byKey[job.Key()]
				So(got, ShouldNotBeNil)
				So(got.State, ShouldEqual, JobStateRunning)
				So(got.Host, ShouldEqual, "h1")
				So(got.Pid, ShouldEqual, 7)
				So(got.Attempts, ShouldEqual, 1)
				So(got.Cmd, ShouldEqual, job.Cmd)
				So(got.LimitGroups, ShouldResemble, job.LimitGroups)
			}

			third := byKey[jobs[2].Key()]
			So(third, ShouldNotBeNil)
			So(third.State, ShouldEqual, JobStateReady)
			So(third.Host, ShouldBeEmpty)
			So(third.Pid, ShouldEqual, 0)
			So(third.Attempts, ShouldEqual, 0)
			So(third.Cmd, ShouldEqual, jobs[2].Cmd)
			So(third.LimitGroups, ShouldResemble, jobs[2].LimitGroups)
		})

		Convey("decodePriorJobs logs runStates=2 on its decoded live jobs line", func() {
			logs := clog.ToBufferAtLevel("warn")

			defer clog.ToDefault()

			_, err := (&Server{}).decodePriorJobs(ctx, database)
			So(err, ShouldBeNil)
			So(logs.String(), ShouldContainSubstring, "recovering: decoded live jobs")
			So(logs.String(), ShouldContainSubstring, "runStates=2")
			So(logs.String(), ShouldNotContainSubstring, "undecodable")
		})

		Convey("a matching record whose body does not decode is not applied, and decodePriorJobs warns with its key",
			func() {
				key := jobs[2].Key()
				whole := runStateRunningRecord(t, database, jobs[2])
				putRunStateFixture(t, database, key, whole[:len(whole)/2])

				recovered, rsr, err := database.recoverIncompleteJobs()
				So(err, ShouldBeNil)
				So(rsr.applied, ShouldEqual, 2)
				So(rsr.undecodable, ShouldResemble, []string{key})
				So(rsr.dropped, ShouldEqual, 1)
				So(runStateBucketValue(t, database, bucketJobRunState, key), ShouldBeNil)

				third := runStateJobsByKey(recovered)[key]
				So(third, ShouldNotBeNil)
				So(third.State, ShouldEqual, JobStateReady)
				So(third.Host, ShouldBeEmpty)

				putRunStateFixture(t, database, key, whole[:len(whole)/2])

				logs := clog.ToBufferAtLevel("warn")

				defer clog.ToDefault()

				_, err = (&Server{}).decodePriorJobs(ctx, database)
				So(err, ShouldBeNil)
				So(logs.String(), ShouldContainSubstring, "recovering: undecodable job run-state record")
				So(logs.String(), ShouldContainSubstring, key)
				So(logs.String(), ShouldContainSubstring, "runStates=2")
			})

		Convey("orphans sorting before, between and after live keys, and a stale record, leave the others applied",
			func() {
				sorted := slices.Clone(jobs)
				slices.SortFunc(sorted, func(a, b *Job) int { return strings.Compare(a.Key(), b.Key()) })

				first, second, third := sorted[0], sorted[1], sorted[2]

				putRunStateFixture(t, database, first.Key(), runStateRunningRecord(t, database, first))
				putRunStateFixture(t, database, second.Key(), runStateRunningRecord(t, database, first))
				putRunStateFixture(t, database, third.Key(), runStateRunningRecord(t, database, third))

				orphan := runStateRunningRecord(t, database, first)
				for _, key := range []string{"\x00orphan", first.Key() + "\x00", second.Key() + "\x00", "\xff\xff\xff"} {
					putRunStateFixture(t, database, key, orphan)
				}

				recovered, rsr, err := database.recoverIncompleteJobs()
				So(err, ShouldBeNil)
				So(rsr.applied, ShouldEqual, 2)
				So(rsr.dropped, ShouldEqual, 5)
				So(len(recovered), ShouldEqual, 3)
				So(runStateBucketKeys(t, database), ShouldResemble, []string{first.Key(), third.Key()})

				byKey := runStateJobsByKey(recovered)

				for _, job := range []*Job{first, third} {
					got := byKey[job.Key()]
					So(got, ShouldNotBeNil)
					So(got.State, ShouldEqual, JobStateRunning)
					So(got.Host, ShouldEqual, "h1")
				}

				got := byKey[second.Key()]
				So(got, ShouldNotBeNil)
				So(got.State, ShouldEqual, JobStateReady)
				So(got.Host, ShouldBeEmpty)
			})

		Convey("an orphan matching the live record of a job without its own run-state record is not applied to it",
			func() {
				putRunStateFixture(t, database, jobs[2].Key()+"\x00", runStateRunningRecord(t, database, jobs[2]))

				recovered, rsr, err := database.recoverIncompleteJobs()
				So(err, ShouldBeNil)
				So(rsr.applied, ShouldEqual, 2)

				third := runStateJobsByKey(recovered)[jobs[2].Key()]
				So(third, ShouldNotBeNil)
				So(third.State, ShouldEqual, JobStateReady)
				So(third.Host, ShouldBeEmpty)
			})

		Convey("a live record that does not decode fails recovery", func() {
			key := jobs[2].Key()
			live := runStateBucketValue(t, database, bucketJobsLive, key)

			err := database.bolt.Update(func(tx *bolt.Tx) error {
				return tx.Bucket(bucketJobsLive).Put([]byte(key), live[:len(live)/2])
			})
			So(err, ShouldBeNil)

			_, _, err = database.recoverIncompleteJobs()
			So(err, ShouldNotBeNil)
		})

		Convey("with no run-state bucket at all, every job is returned as its live record says", func() {
			err := database.bolt.Update(func(tx *bolt.Tx) error {
				return tx.DeleteBucket(bucketJobRunState)
			})
			So(err, ShouldBeNil)

			recovered, rsr, err := database.recoverIncompleteJobs()
			So(err, ShouldBeNil)
			So(rsr.applied, ShouldEqual, 0)
			So(len(recovered), ShouldEqual, 3)

			for _, job := range recovered {
				So(job.State, ShouldEqual, JobStateReady)
			}
		})
	})
}

func TestRecoveryDropsStaleRunState(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given 3 live jobs, the first with a matching run-state record of it running", t, func() {
		database, jobs := runStateRecoveryDB(t, ctx)
		defer func() { _ = database.close(ctx) }()

		matching := runStateRunningRecord(t, database, jobs[0])
		putRunStateFixture(t, database, jobs[0].Key(), matching)

		staleKey := jobs[1].Key()
		staleRecord := runStateRunningRecord(t, database, jobs[0])

		Convey("a record whose CRC does not match its live record is ignored and dropped", func() {
			putRunStateFixture(t, database, staleKey, staleRecord)

			recovered, rsr, err := database.recoverIncompleteJobs()
			So(err, ShouldBeNil)
			So(rsr.applied, ShouldEqual, 1)
			So(rsr.dropped, ShouldEqual, 1)

			stale := runStateJobsByKey(recovered)[staleKey]
			So(stale, ShouldNotBeNil)
			So(stale.State, ShouldEqual, JobStateReady)
			So(stale.Host, ShouldBeEmpty)
			So(runStateBucketValue(t, database, bucketJobRunState, staleKey), ShouldBeNil)
			So(runStateBucketValue(t, database, bucketJobRunState, jobs[0].Key()), ShouldResemble, matching)
		})

		Convey("a record with no live record is dropped", func() {
			putRunStateFixture(t, database, "orphan", matching)

			_, rsr, err := database.recoverIncompleteJobs()
			So(err, ShouldBeNil)
			So(rsr.dropped, ShouldEqual, 1)
			So(runStateBucketKeys(t, database), ShouldResemble, []string{jobs[0].Key()})
		})

		Convey("a stale record rewritten as a matching one between the read and the write is kept", func() {
			putRunStateFixture(t, database, staleKey, staleRecord)

			rewritten := runStateRunningRecord(t, database, jobs[1])

			defer setRunStateDropHook(func() { putRunStateFixture(t, database, staleKey, rewritten) })()

			_, rsr, err := database.recoverIncompleteJobs()
			So(err, ShouldBeNil)
			So(rsr.dropped, ShouldEqual, 0)
			So(runStateBucketValue(t, database, bucketJobRunState, staleKey), ShouldResemble, rewritten)
		})

		Convey("an orphan whose job is added between the read and the write is kept", func() {
			orphan := testDBJob("echo runstate recovery late add", runStateTestRepGroup)
			orphan.State = JobStateReady
			orphanKey := orphan.Key()
			putRunStateFixture(t, database, orphanKey, []byte{0, 0, 0, 0})

			var kept []byte

			defer setRunStateDropHook(func() {
				_, _, _, errs := database.storeNewJobs(ctx, []*Job{orphan}, false)
				So(errs, ShouldBeNil)

				kept = runStateRunningRecord(t, database, orphan)
				putRunStateFixture(t, database, orphanKey, kept)
			})()

			_, rsr, err := database.recoverIncompleteJobs()
			So(err, ShouldBeNil)
			So(rsr.dropped, ShouldEqual, 0)
			So(runStateBucketValue(t, database, bucketJobRunState, orphanKey), ShouldResemble, kept)
		})

		Convey("an undecodable record rewritten as a decodable one between the read and the write is kept", func() {
			undecodable := runStateRunningRecord(t, database, jobs[1])
			putRunStateFixture(t, database, staleKey, undecodable[:len(undecodable)/2])

			defer setRunStateDropHook(func() { putRunStateFixture(t, database, staleKey, undecodable) })()

			_, rsr, err := database.recoverIncompleteJobs()
			So(err, ShouldBeNil)
			So(rsr.undecodable, ShouldResemble, []string{staleKey})
			So(rsr.dropped, ShouldEqual, 0)
			So(runStateBucketValue(t, database, bucketJobRunState, staleKey), ShouldResemble, undecodable)
		})

		Convey("a run-state bucket removed between the read and the write counts as empty", func() {
			putRunStateFixture(t, database, staleKey, staleRecord)

			defer setRunStateDropHook(func() {
				So(database.bolt.Update(func(tx *bolt.Tx) error {
					return tx.DeleteBucket(bucketJobRunState)
				}), ShouldBeNil)
			})()

			recovered, rsr, err := database.recoverIncompleteJobs()
			So(err, ShouldBeNil)
			So(rsr.dropErr, ShouldBeNil)
			So(rsr.dropped, ShouldEqual, 0)
			So(len(recovered), ShouldEqual, len(jobs))
		})

		Convey("with no stale or orphaned records, nothing is committed and no dropped line is logged", func() {
			hookCalled := false

			defer setRunStateDropHook(func() { hookCalled = true })()

			before := database.bolt.Stats()

			_, rsr, err := database.recoverIncompleteJobs()
			So(err, ShouldBeNil)
			So(rsr.applied, ShouldEqual, 1)
			So(rsr.dropped, ShouldEqual, 0)

			after := database.bolt.Stats()
			So(after.TxStats.GetWrite(), ShouldEqual, before.TxStats.GetWrite())
			So(hookCalled, ShouldBeFalse)

			logs := clog.ToBufferAtLevel("warn")

			defer clog.ToDefault()

			_, err = (&Server{}).decodePriorJobs(ctx, database)
			So(err, ShouldBeNil)
			So(logs.String(), ShouldContainSubstring, "recovering: decoded live jobs")
			So(logs.String(), ShouldNotContainSubstring, "dropped stale")
		})

		Convey("decodePriorJobs logs how many records it dropped", func() {
			putRunStateFixture(t, database, staleKey, staleRecord)
			putRunStateFixture(t, database, "orphan", matching)

			logs := clog.ToBufferAtLevel("warn")

			defer clog.ToDefault()

			_, err := (&Server{}).decodePriorJobs(ctx, database)
			So(err, ShouldBeNil)
			So(logs.String(), ShouldContainSubstring, "recovering: dropped stale job run-state records")
			So(logs.String(), ShouldContainSubstring, "count=2")
		})

		Convey("a failed drop transaction still recovers every job, with dropped 0 and no error", func() {
			putRunStateFixture(t, database, staleKey, staleRecord)

			defer setRunStateDropHook(func() { So(database.close(ctx), ShouldBeNil) })()

			recovered, rsr, err := database.recoverIncompleteJobs()
			So(err, ShouldBeNil)
			So(rsr.dropErr, ShouldNotBeNil)
			So(rsr.applied, ShouldEqual, 1)
			So(rsr.dropped, ShouldEqual, 0)
			So(len(recovered), ShouldEqual, len(jobs))
			So(runStateJobsByKey(recovered)[jobs[0].Key()].State, ShouldEqual, JobStateRunning)
		})

		Convey("decodePriorJobs warns about a failed drop transaction and returns the jobs", func() {
			putRunStateFixture(t, database, staleKey, staleRecord)

			defer setRunStateDropHook(func() { So(database.close(ctx), ShouldBeNil) })()

			logs := clog.ToBufferAtLevel("warn")

			defer clog.ToDefault()

			recovered, err := (&Server{}).decodePriorJobs(ctx, database)
			So(err, ShouldBeNil)
			So(len(recovered), ShouldEqual, len(jobs))
			So(logs.String(), ShouldContainSubstring, "recovering: failed to drop stale job run-state records")
			So(logs.String(), ShouldNotContainSubstring, "recovering: dropped stale")
		})

		Convey("on a read-only handle, stale and orphaned records are ignored and kept, with dropped 0", func() {
			putRunStateFixture(t, database, staleKey, staleRecord)
			putRunStateFixture(t, database, "orphan", matching)

			path := database.bolt.Path()
			So(database.close(ctx), ShouldBeNil)

			rdb, err := bolt.Open(path, dbFilePermission, &bolt.Options{ReadOnly: true, Timeout: time.Second})
			So(err, ShouldBeNil)

			readOnly := &db{bolt: rdb, ch: new(codec.BincHandle)}

			before := rdb.Stats()

			recovered, rsr, err := readOnly.recoverIncompleteJobs()
			So(err, ShouldBeNil)
			So(rsr.dropErr, ShouldBeNil)
			So(rsr.applied, ShouldEqual, 1)
			So(rsr.dropped, ShouldEqual, 0)

			after := rdb.Stats()
			So(after.TxStats.GetWrite(), ShouldEqual, before.TxStats.GetWrite())
			So(runStateJobsByKey(recovered)[staleKey].State, ShouldEqual, JobStateReady)
			So(runStateBucketKeys(t, readOnly), ShouldResemble,
				slices.Sorted(slices.Values([]string{jobs[0].Key(), staleKey, "orphan"})))
			So(rdb.Close(), ShouldBeNil)
		})
	})
}

// runStateRecoveryDB returns a database holding the add-time live records
// (State ready) of 3 jobs with distinct Cmds and LimitGroups, along with those
// jobs.
func runStateRecoveryDB(t *testing.T, ctx context.Context) (*db, []*Job) {
	t.Helper()

	database := openReliable4WriteStormDB(t, ctx)

	jobs := make([]*Job, 3)
	for i := range jobs {
		jobs[i] = testDBJob("echo runstate recovery "+strconv.Itoa(i), runStateTestRepGroup)
		jobs[i].State = JobStateReady
		jobs[i].LimitGroups = []string{"rsrlg" + strconv.Itoa(i)}
	}

	if _, _, _, err := database.storeNewJobs(ctx, jobs, false); err != nil {
		t.Fatalf("storeNewJobs failed: %v", err)
	}

	return database, jobs
}

// TestReserveAndStartWriteRunState checks that a reservation and a start, a
// duplicate start included, leave the job's add-time live record as it was and
// write its run state over it, which recovery overlays.
func TestReserveAndStartWriteRunState(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given a runner's manager with a job whose Cmd is 10,000 bytes, added and its add committed", t, func() {
		f := newMovedOnFixture(ctx, t, serverRC)
		defer f.stop(ctx)

		cmd := restFormTrue + " " + strings.Repeat("x", runStateBigCmdBytes)

		f.add(movedOnRetries, func(job *Job) { job.Cmd = cmd }, "big")

		key := (&Job{Cmd: cmd, Cwd: testCwd}).Key()
		added := runStateBucketValue(t, f.server.db, bucketJobsLive, key)
		So(len(added), ShouldBeGreaterThanOrEqualTo, runStateBigCmdBytes)

		Convey("once it is reserved and started, its live record is unchanged and recovery reads the in-memory job", func() {
			So(f.reserveAndStart().Key(), ShouldEqual, key)

			So(bytes.Equal(runStateBucketValue(t, f.server.db, bucketJobsLive, key), added), ShouldBeTrue)
			So(runStateBucketValue(t, f.server.db, bucketJobRunState, key), ShouldNotBeEmpty)

			want, err := f.server.db.encodeJob(preStartServerJob(f.server, key))
			So(err, ShouldBeNil)

			got, err := f.server.db.encode(runStateRecoveredJob(t, f.server.db, key))
			So(err, ShouldBeNil)
			So(bytes.Equal(got, want), ShouldBeTrue)
		})

		Convey("once it is reserved, recovery reads it reserved by the runner, with the runner's host and pid", func() {
			So(f.reserve().Key(), ShouldEqual, key)

			So(bytes.Equal(runStateBucketValue(t, f.server.db, bucketJobsLive, key), added), ShouldBeTrue)

			inMemory := preStartServerJob(f.server, key)
			inMemory.RLock()
			reservation := inMemory.RunnerReservation
			inMemory.RUnlock()

			So(reservation, ShouldBeGreaterThan, 0)

			recovered := runStateRecoveredJob(t, f.server.db, key)
			host, pid := reserveHostAndPid()

			So(recovered.State, ShouldEqual, JobStateReserved)
			So(recovered.ReservedBy, ShouldEqual, f.runner.clientid)
			So(recovered.Host, ShouldEqual, host)
			So(recovered.Pid, ShouldEqual, pid)
			So(recovered.RunnerReservation, ShouldEqual, reservation)
		})

		Convey("a manager killed after the reservation and before Started recovers its run state, "+
			"holding the job in Run for the runner", func() {
			So(f.reserve().Key(), ShouldEqual, key)

			image := f.backup()

			logs := captureLogsAtLevel("warn")

			defer clog.ToDefault()

			f.crashOnto(ctx, image)

			So(logs.String(), ShouldContainSubstring, "recovering: decoded live jobs")
			So(logs.String(), ShouldContainSubstring, "runStates=1 ")

			item, err := f.server.q.Get(key)
			So(err, ShouldBeNil)
			So(item.Stats().State, ShouldEqual, queue.ItemStateRun)

			recovered := preStartServerJob(f.server, key)
			recovered.RLock()
			reservedBy := recovered.ReservedBy
			recovered.RUnlock()

			So(reservedBy, ShouldEqual, f.runner.clientid)
		})

		Convey("a duplicate start giving the runner pid of a job recovered without one is recovered with it", func() {
			reserved := f.reserveAndStart()

			// what a job recovered from a record written without a runner pid is.
			inMemory := preStartServerJob(f.server, key)
			inMemory.Lock()
			inMemory.RunnerPid = 0
			inMemory.Unlock()

			So(f.server.db.updateJobRunStateDurable(inMemory), ShouldBeNil)
			So(runStateRecoveredJob(t, f.server.db, key).RunnerPid, ShouldEqual, 0)

			req, err := f.runner.startedRequest(reserved, f.cmdPid, time.Now())
			So(err, ShouldBeNil)

			req.Job.RunnerPid = runStateDuplicateRunnerPid

			_, err = f.runner.request(req)
			So(err, ShouldBeNil)

			So(bytes.Equal(runStateBucketValue(t, f.server.db, bucketJobsLive, key), added), ShouldBeTrue)

			f.crashOnto(ctx, f.backup())

			recovered := preStartServerJob(f.server, key)
			So(recovered, ShouldNotBeNil)

			recovered.RLock()
			runnerPid, state := recovered.RunnerPid, recovered.State
			recovered.RUnlock()

			So(state, ShouldEqual, JobStateRunning)
			So(runnerPid, ShouldEqual, runStateDuplicateRunnerPid)
		})
	})
}

// runStateRunningRecord returns a run-state record, matching job's stored live
// record, of job running on host h1 with Pid 7 and Attempts 1.
func runStateRunningRecord(t *testing.T, database *db, job *Job) []byte {
	t.Helper()

	running := &Job{
		State:        JobStateRunning,
		Host:         "h1",
		Pid:          7,
		Attempts:     1,
		StartTime:    time.Now(),
		Requirements: job.Requirements,
	}

	encodedRunState, err := database.encode(newJobRunState(running))
	if err != nil {
		t.Fatalf("encoding the run state failed: %v", err)
	}

	return runStateRecord(runStateBucketValue(t, database, bucketJobsLive, job.Key()), encodedRunState)
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

// runStateRecoveredJob returns the job recoverIncompleteJobs reads for key.
func runStateRecoveredJob(t *testing.T, database *db, key string) *Job {
	t.Helper()

	recovered, _, err := database.recoverIncompleteJobs()
	if err != nil {
		t.Fatalf("recoverIncompleteJobs failed: %v", err)
	}

	job := runStateJobsByKey(recovered)[key]
	if job == nil {
		t.Fatalf("recoverIncompleteJobs did not return %s", key)
	}

	return job
}

// runStateJobsByKey returns jobs keyed by their keys.
func runStateJobsByKey(jobs []*Job) map[string]*Job {
	byKey := make(map[string]*Job, len(jobs))
	for _, job := range jobs {
		byKey[job.Key()] = job
	}

	return byKey
}

// runStateBucketKeys returns the keys of every run-state record, in key order.
func runStateBucketKeys(t *testing.T, database *db) []string {
	t.Helper()

	var keys []string

	err := database.bolt.View(func(tx *bolt.Tx) error {
		return tx.Bucket(bucketJobRunState).ForEach(func(key, _ []byte) error {
			keys = append(keys, string(key))

			return nil
		})
	})
	if err != nil {
		t.Fatalf("reading the run-state bucket failed: %v", err)
	}

	return keys
}

// setRunStateDropHook sets recoverRunStateDropHook to hook, and returns a func
// that unsets it.
func setRunStateDropHook(hook func()) func() {
	recoverRunStateDropHook = hook

	return func() { recoverRunStateDropHook = nil }
}

// TestReservationKeepsLearnedRequirements checks that requirements a job
// learned from its ReqGroup in memory before its reservation are what recovery
// reads after a crash, though its live record was written before it learned
// them.
func TestReservationKeepsLearnedRequirements(t *testing.T) {
	if runnermode || servermode {
		return
	}

	const (
		reqGroup  = "lr"
		requested = 100
		peak      = 1500
		completed = 10
	)

	ctx := context.Background()

	Convey("Given a manager whose ReqGroup learned from 10 completed 100MB jobs that peaked at 1500MB", t, func() {
		f := newMovedOnFixture(ctx, t, "")
		defer f.stop(ctx)

		learning := func(job *Job) {
			job.ReqGroup = reqGroup
			job.Requirements.RAM = requested
		}

		names := make([]string, completed)
		for i := range names {
			names[i] = "done" + strconv.Itoa(i)
		}

		f.add(movedOnRetries, learning, names...)

		for range completed {
			done := f.reserveAndStart()
			So(f.runner.Archive(done, &JobEndState{
				Exited: true, Exitcode: 0, PeakRAM: peak, EndTime: time.Now(),
			}), ShouldBeNil)
		}

		So(pollUntil(func() bool {
			ram, err := f.server.db.recommendedReqGroupMemory(reqGroup)

			return err == nil && ram > requested
		}), ShouldBeTrue)

		Convey("an 11th job reserved after learning them is recovered after a crash with them", func() {
			f.add(movedOnRetries, learning, "learner")

			key := (&Job{Cmd: restFormTrue + " movedon learner", Cwd: testCwd}).Key()

			// the reservation's write is what has to carry them, so the job must
			// have learned them before it.
			So(pollUntil(func() bool { return runStateJobRAM(preStartServerJob(f.server, key)) > requested }),
				ShouldBeTrue)

			So(f.reserve().Key(), ShouldEqual, key)

			learned := runStateJobRAM(preStartServerJob(f.server, key))

			f.crashOnto(ctx, f.backup())

			recovered := preStartServerJob(f.server, key)
			So(recovered, ShouldNotBeNil)

			recovered.RLock()
			defer recovered.RUnlock()

			So(recovered.Requirements.RAM, ShouldEqual, learned)
			So(recovered.RequirementsOrig, ShouldNotBeNil)
			So(recovered.RequirementsOrig.RAM, ShouldEqual, requested)
		})
	})
}

// runStateJobRAM returns job's Requirements.RAM, read under its lock, or 0 for
// a nil job.
func runStateJobRAM(job *Job) int {
	if job == nil {
		return 0
	}

	job.RLock()
	defer job.RUnlock()

	return job.Requirements.RAM
}
