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
	"encoding/binary"
	"hash/crc32"
	"time"

	"github.com/VertebrateResequencing/wr/jobqueue/scheduler"
	"github.com/gofrs/uuid/v5"
	"github.com/ugorji/go/codec"
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

// decodeMatchingRunState returns the jobRunState in record, and true, if record
// was written over exactly the live record live, with an error if its body fails
// to decode; otherwise false. It is the one test of whether recovery applies
// record, shared by recovery's read and its drop's re-check.
func (db *db) decodeMatchingRunState(live, record []byte) (jobRunState, bool, error) {
	var runState jobRunState

	encodedRunState, matches := runStateOver(live, record)
	if !matches {
		return runState, false, nil
	}

	err := codec.NewDecoderBytes(encodedRunState, db.ch).Decode(&runState)

	return runState, true, err
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

// runStateWalk steps through bucketJobRunState in key order alongside a walk of
// bucketJobsLive, so recovery reads each run-state record without a Get per
// live job. A database without the bucket (a raw copied one) walks as empty.
type runStateWalk struct {
	cursor  *bolt.Cursor
	key     []byte
	value   []byte
	orphans []string
}

// newRunStateWalk returns a runStateWalk positioned at the first run-state
// record in tx.
func newRunStateWalk(tx *bolt.Tx) *runStateWalk {
	w := &runStateWalk{}

	if bucket := tx.Bucket(bucketJobRunState); bucket != nil {
		w.cursor = bucket.Cursor()
		w.key, w.value = w.cursor.First()
	}

	return w
}

// recordFor returns liveKey's run-state record, or nil if it has none. Calls
// must be in ascending liveKey order; the keys of records skipped over, which
// have no live record, go in orphans.
func (w *runStateWalk) recordFor(liveKey []byte) []byte {
	w.skipBefore(liveKey)

	if w.key == nil || !bytes.Equal(w.key, liveKey) {
		return nil
	}

	value := w.value
	w.key, w.value = w.cursor.Next()

	return value
}

// skipBefore steps past every record whose key sorts before liveKey, or every
// remaining record for a nil liveKey, putting their keys in orphans.
func (w *runStateWalk) skipBefore(liveKey []byte) {
	for w.key != nil && (liveKey == nil || bytes.Compare(w.key, liveKey) < 0) {
		w.orphans = append(w.orphans, string(w.key))
		w.key, w.value = w.cursor.Next()
	}
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

// runStateRecovery is what recoverIncompleteJobs did with run-state records.
type runStateRecovery struct {
	applied     int      // matching records overlaid
	dropped     int      // stale, orphaned or undecodable records deleted
	undecodable []string // keys of matching records whose body failed to decode
	dropErr     error    // why the drop transaction failed, leaving every record
}

// overlayRunState applies record to job, decoded from its live record live, if
// record matches live, counting it in rsr. It returns true if record is present
// but not applied, so recovery drops it: a stale record, or a matching one whose
// body fails to decode, whose key also goes in rsr.undecodable, since the full
// record is the durable fallback.
func (db *db) overlayRunState(job *Job, key, live, record []byte, rsr *runStateRecovery) bool {
	if record == nil {
		return false
	}

	runState, matches, err := db.decodeMatchingRunState(live, record)
	if !matches {
		return true
	}

	if err != nil {
		rsr.undecodable = append(rsr.undecodable, string(key))

		return true
	}

	runState.applyTo(job)

	rsr.applied++

	return false
}

// runStateDroppable returns true if record is a run-state record that recovery
// does not apply over the live record live: orphaned (live is nil), stale, or
// matching but with a body that fails to decode.
func (db *db) runStateDroppable(live, record []byte) bool {
	if record == nil {
		return false
	}

	if live == nil {
		return true
	}

	_, matches, err := db.decodeMatchingRunState(live, record)

	return !matches || err != nil
}

// dropRunStates deletes, in one write transaction, each of keys' run-state
// records that is still droppable against the live bucket as it is then,
// returning how many it deleted. A drain on a running server may have rewritten
// either record since recovery's read. A database without bucketJobRunState has
// none to delete.
func (db *db) dropRunStates(keys []string) (int, error) {
	dropped := 0

	err := db.bolt.Update(func(tx *bolt.Tx) error {
		var err error

		dropped, err = db.dropRunStatesTx(tx, keys)

		return err
	})

	return dropped, err
}

// dropRunStatesTx is dropRunStates' delete, in tx.
func (db *db) dropRunStatesTx(tx *bolt.Tx, keys []string) (int, error) {
	bucket := tx.Bucket(bucketJobRunState)
	if bucket == nil {
		return 0, nil
	}

	live := tx.Bucket(bucketJobsLive)
	dropped := 0

	for _, key := range keys {
		k := []byte(key)
		if !db.runStateDroppable(live.Get(k), bucket.Get(k)) {
			continue
		}

		if err := bucket.Delete(k); err != nil {
			return 0, err
		}

		dropped++
	}

	return dropped, nil
}
