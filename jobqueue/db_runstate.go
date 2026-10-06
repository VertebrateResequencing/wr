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
	"encoding/binary"
	"hash/crc32"
	"time"

	"github.com/VertebrateResequencing/wr/jobqueue/scheduler"
	"github.com/gofrs/uuid/v5"
	bolt "go.etcd.io/bbolt"
)

// runStateCRCBytes is the length of the CRC-32C of the live record that starts
// a run-state record.
const runStateCRCBytes = 4

//nolint:gochecknoglobals // a read-only lookup table, built once.
var runStateCRCTable = crc32.MakeTable(crc32.Castagnoli)

// runStateRecord returns the bucketJobRunState value for encodedRunState
// (db.encode of a jobRunState) over the live record live.
func runStateRecord(live, encodedRunState []byte) []byte {
	record := make([]byte, runStateCRCBytes, runStateCRCBytes+len(encodedRunState))
	binary.BigEndian.PutUint32(record, crc32.Checksum(live, runStateCRCTable))

	return append(record, encodedRunState...)
}

// runStateOver returns the encoded jobRunState in record, and true, if record
// was written over exactly the live record live; otherwise nil and false.
func runStateOver(live, record []byte) ([]byte, bool) {
	if len(record) < runStateCRCBytes {
		return nil, false
	}

	if binary.BigEndian.Uint32(record) != crc32.Checksum(live, runStateCRCTable) {
		return nil, false
	}

	return record[runStateCRCBytes:], true
}

// jobRunState is every persisted Job field that a reservation
// (resetJobForReservation, respondWithReservedJob) or a start (applyJobStart,
// acceptDuplicateStartLocked) sets, plus the requirements prepareReadyJob may
// learn in memory before the reservation, and RerunAfterRun, which recovery may
// clear in memory only. Field names and types match Job's, with no omitempty,
// so every field, zero or not, is encoded.
type jobRunState struct {
	State             JobState
	Exited            bool
	Exitcode          int
	FailReason        string
	Lost              bool
	Pid               int
	RunnerPid         int
	Host              string
	HostID            string
	HostIP            string
	ActualCwd         string
	StartTime         time.Time
	EndTime           time.Time
	PeakRAM           int
	PeakDisk          int64
	CPUtime           time.Duration
	StdOutC           []byte
	StdErrC           []byte
	ReservedBy        uuid.UUID
	RunnerReservation uint64
	Attempts          uint32
	DelayTime         time.Duration
	Requirements      *scheduler.Requirements
	RequirementsOrig  *scheduler.Requirements
	RerunAfterRun     bool
}

// newJobRunState returns j's run state, with deep copies of Requirements and
// RequirementsOrig (their Other maps included). The caller holds j's read lock.
func newJobRunState(j *Job) jobRunState {
	return jobRunState{
		State:             j.State,
		Exited:            j.Exited,
		Exitcode:          j.Exitcode,
		FailReason:        j.FailReason,
		Lost:              j.Lost,
		Pid:               j.Pid,
		RunnerPid:         j.RunnerPid,
		Host:              j.Host,
		HostID:            j.HostID,
		HostIP:            j.HostIP,
		ActualCwd:         j.ActualCwd,
		StartTime:         j.StartTime,
		EndTime:           j.EndTime,
		PeakRAM:           j.PeakRAM,
		PeakDisk:          j.PeakDisk,
		CPUtime:           j.CPUtime,
		StdOutC:           j.StdOutC,
		StdErrC:           j.StdErrC,
		ReservedBy:        j.ReservedBy,
		RunnerReservation: j.RunnerReservation,
		Attempts:          j.Attempts,
		DelayTime:         j.DelayTime,
		Requirements:      cloneRequirements(j.Requirements),
		RequirementsOrig:  cloneRequirements(j.RequirementsOrig),
		RerunAfterRun:     j.RerunAfterRun,
	}
}

// applyTo sets every field of r on j and invalidates j's derived state
// (invalidateDerivedLocked), since Requirements feed it. The caller holds j's
// lock, or j is not yet shared.
//
//nolint:funlen // a flat field-by-field copy of every jobRunState field.
func (r *jobRunState) applyTo(j *Job) {
	j.State = r.State
	j.Exited = r.Exited
	j.Exitcode = r.Exitcode
	j.FailReason = r.FailReason
	j.Lost = r.Lost
	j.Pid = r.Pid
	j.RunnerPid = r.RunnerPid
	j.Host = r.Host
	j.HostID = r.HostID
	j.HostIP = r.HostIP
	j.ActualCwd = r.ActualCwd
	j.StartTime = r.StartTime
	j.EndTime = r.EndTime
	j.PeakRAM = r.PeakRAM
	j.PeakDisk = r.PeakDisk
	j.CPUtime = r.CPUtime
	j.StdOutC = r.StdOutC
	j.StdErrC = r.StdErrC
	j.ReservedBy = r.ReservedBy
	j.RunnerReservation = r.RunnerReservation
	j.Attempts = r.Attempts
	j.DelayTime = r.DelayTime
	j.Requirements = r.Requirements
	j.RequirementsOrig = r.RequirementsOrig
	j.RerunAfterRun = r.RerunAfterRun

	j.invalidateDerivedLocked()
}

// putLiveRecord puts encoded as key's live record and deletes key's run-state
// record, in tx: a full write of the job is newer than any run state written
// over the live record it replaces.
func putLiveRecord(tx *bolt.Tx, key, encoded []byte) error {
	if err := tx.Bucket(bucketJobsLive).Put(key, encoded); err != nil {
		return err
	}

	return deleteRunStateRecord(tx, key)
}

// deleteLiveRecord deletes key's live record and run-state record, in tx.
func deleteLiveRecord(tx *bolt.Tx, key []byte) error {
	if err := tx.Bucket(bucketJobsLive).Delete(key); err != nil {
		return err
	}

	return deleteRunStateRecord(tx, key)
}

// deleteRunStateRecord deletes key's run-state record, in tx. A database
// without bucketJobRunState (a bare test database) has none to delete.
func deleteRunStateRecord(tx *bolt.Tx, key []byte) error {
	bucket := tx.Bucket(bucketJobRunState)
	if bucket == nil {
		return nil
	}

	return bucket.Delete(key)
}

// cloneRequirements returns a deep copy of req, or nil for a nil req.
func cloneRequirements(req *scheduler.Requirements) *scheduler.Requirements {
	if req == nil {
		return nil
	}

	return req.Clone()
}
