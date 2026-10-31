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

// This file is the log store (spec "Write path" and "In-memory state of
// logStore"): every write is one or more records appended to the group-commit
// log in jobqueue/wal, and is reflected in the store's in-memory state only once
// its batch is durable. Live *Job values are never held; jobs are decoded from
// their records through Refs.

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"maps"
	"os"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/VertebrateResequencing/wr/clog"
	"github.com/VertebrateResequencing/wr/internal"
	"github.com/VertebrateResequencing/wr/jobqueue/wal"
	"github.com/VertebrateResequencing/wr/limiter"
	"github.com/gofrs/uuid/v5"
	"github.com/ugorji/go/codec"
)

const (
	// walWriteFailedLogMsg is logged, at most once per walWriteFailedLogEvery,
	// when log writes fail.
	walWriteFailedLogMsg = "wal write failed"

	// walWriteFailedLogEvery is the least time between walWriteFailedLogMsg
	// lines.
	walWriteFailedLogEvery = time.Minute

	// logWriterName is the writer= value of the log's "db writer" line.
	logWriterName = "log"

	// logStoreDirPerm is the permission a new store directory is made with.
	logStoreDirPerm = 0o700

	// exitRecords is how many records an exit block holds: its state, its
	// exitstat and a std record per stream.
	exitRecords = 4
)

// logBlockMaxFrames is the most bytes of frames one block holds; a write with
// more is split into several blocks. Half the frame limit leaves room for s2's
// worst-case expansion. A var so tests can shrink it.
var logBlockMaxFrames = wal.MaxFrameLen / 2 //nolint:gochecknoglobals // tuning knob, varied by tests

// walSegmentSize is the size log segments rotate at. A var so tests can shrink
// it.
var walSegmentSize int64 = 256 << 20 //nolint:gochecknoglobals // tuning knob, varied by tests

// Test seams, nil outside tests.
var (
	// logStoreWaitHook is called by a write each time it is about to wait for
	// another write in flight to finish: one holding a claim it needs, one of a
	// state record of a key it adds, or one making live a key it would put back
	// or make a transition of.
	logStoreWaitHook func() //nolint:gochecknoglobals // a test seam

	// logPutBackHook is called by an add once it has read archived dependents
	// it will put back, before it decides on them: an add's own, or each a
	// guarded add finds archived.
	logPutBackHook func() //nolint:gochecknoglobals // a test seam

	// logAppendedHook is called by every write once it has appended its
	// records, before it has their Refs: a write applies only once it has them
	// and its batch is durable, so holding it here lets a later write apply
	// first.
	logAppendedHook func() //nolint:gochecknoglobals // a test seam

	// logTransitionSeqsHook is called by a transition or exit once it has
	// taken its records' seqs and released its locks, before it makes and
	// appends its records.
	logTransitionSeqsHook func() //nolint:gochecknoglobals // a test seam

	// logArchiveDecidedHook is called by an archive once it has decided what
	// it does with its job's live record and made its records, before it
	// appends them.
	logArchiveDecidedHook func() //nolint:gochecknoglobals // a test seam
)

var (
	// errLogStoreBackup is the error of asking the log store for a backup,
	// which it cannot make yet.
	errLogStoreBackup = errors.New("the log store does not make backups yet")

	// errModifyMismatch is the error of a modify given a different number of
	// old keys and jobs.
	errModifyMismatch = errors.New("modify needs one old key per job")
)

// logModify is one job of a modify: its old and new keys, the job and the old
// key's std.
type logModify struct {
	old, new logKey
	job      *Job
	std      [2][]byte
}

// logAdd is what an add will write and report.
type logAdd struct {
	frames       logFrames
	jobsToQueue  []*Job
	jobsToUpdate []*Job
	alreadyAdded DuplicateBreakdown
	member       string // the smallest key it adds: the guard's member

	// kept are the jobs it adds, whose keys are newKeys, and depGroups their
	// dep groups.
	kept               []*Job
	newKeys, depGroups map[string]bool

	// claim is the add's claim, and claimed the key of a dependent to put
	// back that another write has claimed.
	claim   *logClaim
	claimed logKey
}

// stored is what storeNewJobsGuarded reports of the add, before its write.
func (a *logAdd) stored() storedNewJobs {
	return storedNewJobs{
		jobsToQueue:  a.jobsToQueue,
		dependents:   liveDependents{jobs: a.jobsToUpdate},
		alreadyAdded: a.alreadyAdded,
	}
}

// memberKey returns the add's member as rerunGuard.setDependents takes it: nil
// if it adds no job.
func (a *logAdd) memberKey() []byte {
	if a.member == "" {
		return nil
	}

	return []byte(a.member)
}

// keep notes that the add keeps the keyed job, with its dep groups, returning
// its RepGroup.
func (a *logAdd) keep(job *Job, key string) string {
	a.kept = append(a.kept, job)
	a.newKeys[key] = true

	if a.member == "" || key < a.member {
		a.member = key
	}

	job.RLock()
	defer job.RUnlock()

	for _, dg := range job.DepGroups {
		if dg != "" {
			a.depGroups[dg] = true
		}
	}

	return job.RepGroup
}

// addRGLookupRecord adds an rglookup record of k under repGroup to f.
func (s *logStore) addRGLookupRecord(f *logFrames, repGroup string, k logKey) {
	seq := s.nextSeq()
	f.add(appendRGLookupRecord(f.rec, seq, repGroup, k), logOp{typ: logRecRGLookup, seq: seq, name: repGroup, key: k})
}

// addSpecLocked adds job's spec record, as k's, to f. The caller holds job's
// read lock, or job is not shared.
func (s *logStore) addSpecLocked(f *logFrames, job *Job, k logKey) error {
	spec, err := appendFlatSpec(nil, job, s.ch)
	if err != nil {
		return err
	}

	seq := s.nextSeq()
	f.add(appendKeyedRecord(f.rec, logRecSpec, seq, k, spec),
		logOp{typ: logRecSpec, seq: seq, key: k, index: newLogSpecIndex(job)})

	return nil
}

// decideArchive sets a's outcome, deciding what job's archive does with its
// live record, and returns the archive's records, all under job's read lock: a
// rerun mark set before the decision keeps the job live, and one set after it
// is followed by a put-back (storeLiveForRerun) whose records are later than
// the archive's, so it is live after both, as in the bbolt store, whose
// archive decides and writes in one transaction. If k is not live (a delete
// came first, as for a lost job removed before its run ended), the records
// start with job's spec record, which the complete record's history entry
// takes; the bbolt store records such a completion too.
func (s *logStore) decideArchive(a *logArchive, k logKey, job *Job) (logFrames, error) {
	var (
		f        logFrames
		keepLive bool
	)

	job.RLock()
	defer job.RUnlock()

	a.outcome, keepLive = job.archiveOutcomeLocked(s.archiveTxs, s.addStored, &s.rerunGuards)

	if !s.liveKey(k) {
		if err := s.addSpecLocked(&f, job, k); err != nil {
			return f, err
		}
	}

	rs := newLogRunState(job)
	s.addArchiveRecords(&f, k, &rs, keepLive)

	return f, nil
}

// addDelete adds a delete record of k to f.
func (s *logStore) addDelete(f *logFrames, k logKey) {
	seq := s.nextSeq()
	f.add(appendKeyedRecord(f.rec, logRecDelete, seq, k, nil), logOp{typ: logRecDelete, seq: seq, key: k})
}

// deleteLiveJobs removes each of the keyed jobs from the live plane, with its
// state and std, in one block, and returns once it is durable. As the bbolt
// store's, it leaves history as it is: a complete key stays complete, and
// keeps its place in the reverse dep group index.
func (s *logStore) deleteLiveJobs(_ context.Context, keys []string) error {
	if len(keys) == 0 {
		return nil
	}

	ks, err := parseLogKeys(keys)
	if err != nil {
		return err
	}

	return s.writeRemoval(ks, func(f *logFrames) error {
		for _, k := range ks {
			f.group()
			s.addDelete(f, k)
		}

		return nil
	})
}

// parseLogKeys is parseLogKey of each of keys.
func parseLogKeys(keys []string) ([]logKey, error) {
	ks := make([]logKey, len(keys))

	for i, key := range keys {
		k, err := parseLogKey(key)
		if err != nil {
			return nil, err
		}

		ks[i] = k
	}

	return ks, nil
}

// writeRemoval claims keys and appends the records build makes of a delete of
// them, returning once they are durable (see writeClaimedRemoval).
func (s *logStore) writeRemoval(keys []logKey, build func(f *logFrames) error) error {
	claim := s.claimKeySet(keys...)
	defer claim.release()

	return s.writeClaimedRemoval(build)
}

// writeClaimedRemoval appends the records build makes of a delete or modify of
// keys the caller has claimed, so they apply after those of any write of them
// in flight (see logClaim), and returns once they are durable. It takes its
// seqs and appends under guardMu, so its records also apply after those of any
// guarded add's put-back of them appended before it: that add has its Refs by
// then, and its batch is durable first.
func (s *logStore) writeClaimedRemoval(build func(f *logFrames) error) error {
	w, err := s.appendRemoval(build)
	if err != nil {
		return err
	}

	return w.wait()
}

// appendRemoval is writeClaimedRemoval's append, under closeMu and guardMu.
func (s *logStore) appendRemoval(build func(f *logFrames) error) (*logWrite, error) {
	s.closeMu.RLock()
	defer s.closeMu.RUnlock()

	if s.closed {
		return nil, errDBClosed
	}

	s.guardMu.Lock()
	defer s.guardMu.Unlock()

	var f logFrames
	if err := build(&f); err != nil {
		return nil, err
	}

	return s.write(&f, nil)
}

// modifyLiveJobs replaces the live record of each job with the old key at the
// same index of oldKeys by jobs[i], whose key may differ, as the bbolt store's
// does: in one block per call (split between jobs, as an add's is, if over
// the block size limit), each job's modify record, which removes the old key
// from the live plane, its rep groups' lookups and the reverse dep group index,
// then its new spec, state, std copied from the old key and rglookup, then a
// depgroup record of each dep group not seen before. It returns once they are
// durable. A transition or exit of a new key meanwhile waits for it (see
// reviving).
//
// Each modify applies on its own, in order, so one whose new key is an old key
// modified later in the same call (A to B, then B to C) leaves only the last
// new key live, where the bbolt store's, removing every old key before storing
// any new one, leaves both B and C. The server never asks for that: it skips a
// modify whose new key is already queued (JobModifier.skipForDuplicateKey).
func (s *logStore) modifyLiveJobs(ctx context.Context, oldKeys []string, jobs []*Job) error {
	olds, news, err := modifyKeys(oldKeys, jobs)
	if err != nil || len(jobs) == 0 {
		return err
	}

	claim := s.claimKeySet(slices.Concat(olds, news)...)
	defer claim.release()

	claim.noteReviving(movedKeys(olds, news)...)

	err = s.writeModifies(olds, news, jobs)
	if err != nil {
		clog.Error(ctx, "Database error during modify", "err", err)
	}

	return err
}

// modifyKeys returns oldKeys and the keys of jobs, which must be as many, as
// logKeys.
func modifyKeys(oldKeys []string, jobs []*Job) (olds, news []logKey, err error) {
	if len(oldKeys) != len(jobs) {
		return nil, nil, fmt.Errorf("%w: %d old keys, %d jobs", errModifyMismatch, len(oldKeys), len(jobs))
	}

	if olds, err = parseLogKeys(oldKeys); err != nil {
		return nil, nil, err
	}

	news = make([]logKey, len(jobs))

	for i, job := range jobs {
		if news[i], err = parseLogKey(job.Key()); err != nil {
			return nil, nil, err
		}
	}

	return olds, news, nil
}

// movedKeys returns those of news not the same as the old key at the same index
// of olds: the keys a modify makes live.
func movedKeys(olds, news []logKey) []logKey {
	var moved []logKey

	for i, k := range news {
		if k != olds[i] {
			moved = append(moved, k)
		}
	}

	return moved
}

// writeModifies is modifyLiveJobs' write, once it has claimed olds and news,
// reading each old key's std only then, so no delete, modify or put-back of one
// comes between. An exit's std record of one still can, and is then lost with
// the old key, as the copy did not see it.
func (s *logStore) writeModifies(olds, news []logKey, jobs []*Job) error {
	stds, err := s.liveStds(olds)
	if err != nil {
		return err
	}

	mods := make([]logModify, len(jobs))
	for i, job := range jobs {
		mods[i] = logModify{old: olds[i], new: news[i], job: job, std: stds[i]}
	}

	return s.writeClaimedRemoval(func(f *logFrames) error {
		return s.addModifies(f, mods)
	})
}

// liveStds returns the std of each of keys, empty for a stream it has none of.
func (s *logStore) liveStds(keys []logKey) ([][2][]byte, error) {
	stds := make([][2][]byte, len(keys))

	for i, k := range keys {
		s.mu.RLock()
		refs, has := s.state.stdRefs(k)
		s.mu.RUnlock()

		for stream := range stds[i] {
			if !has[stream] {
				continue
			}

			std, err := s.readRecordBody(refs[stream], logRecStd)
			if err != nil {
				return nil, err
			}

			stds[i][stream] = std
		}
	}

	return stds, nil
}

// addModifies adds to f the records of each of mods, then a depgroup record of
// each of their dep groups not seen before.
func (s *logStore) addModifies(f *logFrames, mods []logModify) error {
	depGroups := make(map[string]bool)

	for _, m := range mods {
		f.group()

		seq := s.nextSeq()
		f.add(appendModifyRecord(f.rec, seq, m.old, m.new), logOp{typ: logRecModify, seq: seq, key: m.old, newKey: m.new})

		repGroup, err := s.addModifiedJob(f, m.job, m.new, depGroups)
		if err != nil {
			return err
		}

		for _, stream := range [...]uint8{stdOut, stdErr} {
			if len(m.std[stream]) > 0 {
				s.addStd(f, m.new, stream, m.std[stream])
			}
		}

		s.addRGLookupRecord(f, repGroup, m.new)
	}

	s.addUnseenDepGroups(f, depGroups)

	return nil
}

// addModifiedJob adds job's spec and state records, as k's, to f, adding its
// dep groups to depGroups, and returns its RepGroup.
func (s *logStore) addModifiedJob(f *logFrames, job *Job, k logKey, depGroups map[string]bool) (string, error) {
	job.RLock()
	defer job.RUnlock()

	for _, dg := range job.DepGroups {
		if dg != "" {
			depGroups[dg] = true
		}
	}

	return job.RepGroup, s.addSpecAndStateLocked(f, job, k, newLogRunState(job))
}

// storeLiveForRerun puts each of jobs live, whether or not it is live now, as
// the bbolt store's does: as rerunRecords' live record, no longer marked to run
// again and reserved by no runner, in one block, and returns once it is
// durable. They are jobs whose run has ended, or is ending, in an archive, but
// which must run again; history is left as it is. It claims their keys (see
// logClaim), so it puts back a job whose archive is in flight once that
// archive has applied, and a transition or exit of one meanwhile waits for it
// (see reviving).
func (s *logStore) storeLiveForRerun(jobs []*Job) error {
	if len(jobs) == 0 {
		return nil
	}

	keys, err := jobLogKeys(jobs)
	if err != nil {
		return err
	}

	claim := s.claimKeySet(keys...)
	defer claim.release()

	claim.noteReviving(keys...)

	var f logFrames

	for i, job := range jobs {
		f.group()

		if err = s.addRerunLive(&f, job, keys[i]); err != nil {
			return err
		}
	}

	return s.writeAndWait(&f)
}

// jobLogKeys returns the keys of jobs, as logKeys.
func jobLogKeys(jobs []*Job) ([]logKey, error) {
	keys := make([]logKey, len(jobs))

	for i, job := range jobs {
		k, err := parseLogKey(job.Key())
		if err != nil {
			return nil, err
		}

		keys[i] = k
	}

	return keys, nil
}

// addRerunLive adds to f job's spec and state records, as k's, as
// rerunRecords' live record of it.
func (s *logStore) addRerunLive(f *logFrames, job *Job, k logKey) error {
	job.RLock()
	defer job.RUnlock()

	rs := newLogRunState(job)
	rs.RerunAfterRun = false
	rs.ReservedBy = uuid.UUID{}

	return s.addSpecAndStateLocked(f, job, k, rs)
}

// storeRunningRerunMarks records, as a state record, the mark of each of jobs
// marked to run again while running, in one write, and returns once it is
// durable. As the bbolt store's, it skips a job no longer live, no longer
// marked, or with a successful completion being archived: that archive decides
// under the job's read lock, so it sees the mark and keeps the job live.
func (s *logStore) storeRunningRerunMarks(jobs []*Job) error {
	var f logFrames

	finished, err := s.addRerunMarks(&f, jobs)
	if err != nil || len(f.ops) == 0 {
		return err
	}

	after := func() {
		for _, fin := range finished {
			fin()
		}
	}

	w, err := s.writeOpen(&f, after)
	if err != nil {
		after()

		return err
	}

	return w.wait()
}

// addRerunMarks is addRerunMark of each of jobs, returning the funcs it
// returned. On error it has called them.
func (s *logStore) addRerunMarks(f *logFrames, jobs []*Job) ([]func(), error) {
	var finished []func()

	for _, job := range jobs {
		k, err := parseLogKey(job.Key())
		if err != nil {
			for _, fin := range finished {
				fin()
			}

			return nil, err
		}

		if fin := s.addRerunMark(f, job, k); fin != nil {
			finished = append(finished, fin)
		}
	}

	return finished, nil
}

// addRerunMark adds job's state record, as k's, to f and returns the func to
// call once it is written (see addTransition), or adds nothing and returns nil
// if storeRunningRerunMarks skips job.
func (s *logStore) addRerunMark(f *logFrames, job *Job, k logKey) func() {
	return s.addTransition(job, k, func() bool {
		if !s.liveKey(k) || !job.RerunAfterRun || job.archivePendingLocked() {
			return false
		}

		f.group()

		rs := newLogRunState(job)
		s.addState(f, logRecState, k, &rs)

		return true
	})
}

// writeArchive writes f, a's records, noting it in flight, as k's archive,
// until settleArchive. The caller holds guardMu.
func (s *logStore) writeArchive(a *logArchive, k logKey, f *logFrames) error {
	w, err := s.write(f, nil)
	if err != nil {
		return err
	}

	a.w, a.settled = w, make(chan struct{})
	s.inFlight[k] = a.settled

	return nil
}

// stateFrames returns job's state record, as k's, and the func to call once
// its write has finished (see addTransition).
func (s *logStore) stateFrames(job *Job, k logKey) (logFrames, func()) {
	var (
		f   logFrames
		rs  logRunState
		seq uint64
	)

	finished := s.addTransition(job, k, func() bool {
		rs, seq = newLogRunState(job), s.nextSeq()

		return true
	})

	if logTransitionSeqsHook != nil {
		logTransitionSeqsHook()
	}

	s.addStateAt(&f, logRecState, seq, k, &rs)

	return f, finished
}

// addTransition calls build, which snapshots job and takes the seqs of the
// records of a transition or exit of it, as k's, or adds those records to a
// write, under job's read lock and stateMu, once no write in flight is making k
// live (see reviving), and notes k in stateWrites until the returned func,
// which the caller calls once the write has finished or failed to append, is
// called. If build reports false, having taken and added nothing, it notes
// nothing and returns nil. A build that only takes seqs leaves its caller to
// encode the records after, with neither lock held, so exits and transitions of
// other keys do not wait on that.
func (s *logStore) addTransition(job *Job, k logKey, build func() bool) func() {
	s.lockUnlessReviving(job, k)
	defer job.RUnlock()
	defer s.stateMu.Unlock()

	if !build() {
		return nil
	}

	return s.notePending(s.stateWrites, k)
}

// recover loads history.bolt's key index and folded position, replays the
// log, and resumes the seq counter above every seq the log and history.bolt
// hold (spec "Recovery", step 5). It returns the number the next wal file
// must have.
func (s *logStore) recover(ctx context.Context) (uint32, error) {
	folded, foldedSeq, err := s.history.load(s.state.history)
	if err != nil {
		return 0, err
	}

	s.state.folded = folded

	next, err := s.replay(ctx)
	if err != nil {
		return 0, err
	}

	s.seq.Store(max(s.state.maxSeq, foldedSeq))

	return next, nil
}

// takeSeqs takes the next n records' seqs, returning the first.
func (s *logStore) takeSeqs(n uint64) uint64 {
	return s.seq.Add(n) - n + 1
}

// addStateAt is addState with seq, taken already.
func (s *logStore) addStateAt(f *logFrames, typ logRecordType, seq uint64, k logKey, rs *logRunState) {
	f.add(appendKeyedRecord(f.rec, typ, seq, k, appendFlatRunState(nil, rs)),
		logOp{typ: typ, seq: seq, key: k, handedOut: runStateHandedOut(rs.State, rs.Attempts, rs.StartTime),
			endTime: rs.EndTime})
}

// addStdAt is addStd with seq, taken already.
func (s *logStore) addStdAt(f *logFrames, seq uint64, k logKey, stream uint8, std []byte) {
	f.add(appendStdRecord(f.rec, seq, k, stream, std),
		logOp{typ: logRecStd, seq: seq, key: k, stream: stream, hasStd: len(std) > 0})
}

// finishAdd returns once an add's write, w, is durable, then makes any
// put-backs it deferred (see putBackDeferred).
func (s *logStore) finishAdd(w *logWrite, pb logPutBacks, claim *logClaim) error {
	if err := w.wait(); err != nil || len(pb.deferred) == 0 {
		return err
	}

	return s.putBackDeferred(pb, claim)
}

// addExitRecords adds to f the records of an exit of k, with the seqs from seq
// on: rs as its state record, exit as its exitstat record, and a std record of
// each stream of std.
func (s *logStore) addExitRecords(f *logFrames, k logKey, seq uint64, rs *logRunState, exit *jobExitData,
	std [2][]byte,
) {
	s.addStateAt(f, logRecState, seq, k, rs)

	seq++
	f.add(appendExitStatRecord(f.rec, seq, k, exit), logOp{typ: logRecExitStat, seq: seq, key: k})

	for stream, b := range std {
		seq++
		s.addStdAt(f, seq, k, uint8(stream), b)
	}
}

// logWrite is one append's records on their way to being durable. The writer's
// done func and the appender, once it has the Ref, each arrive; whichever is
// second applies the records to the store's state if the batch succeeded, then
// marks the write finished. So the state reflects a write before its waiter
// returns, and done never waits for the appender.
type logWrite struct {
	s      *logStore
	ops    []logOp
	chunks [][2]int // the ops of each frame appended (see logFrames.chunks)
	block  bool
	left   atomic.Int32
	errs   []error // each frame's batch's result
	err    error   // the first of errs, once finished
	after  func()
	fin    chan struct{}
}

// doneFor returns the writer's done func for frame i. The writer calls each
// on its own goroutine, one after another.
func (w *logWrite) doneFor(i int) func(error) {
	return func(err error) {
		w.errs[i] = err
		w.arrive()
	}
}

// placed gives the write's records their Refs, given the Ref of each frame
// holding them.
func (w *logWrite) placed(refs []wal.Ref) {
	if logAppendedHook != nil {
		logAppendedHook()
	}

	for c, ref := range refs {
		chunk := w.chunks[c]

		for i := chunk[0]; i < chunk[1]; i++ {
			w.ops[i].ref = ref

			if w.block {
				w.ops[i].ref.Idx = uint32(i - chunk[0]) //nolint:gosec // a block holds far fewer than 2^32 records
			}
		}
	}

	w.arrive()
}

// arrive applies and finishes the write if it is the last to arrive: it
// applies the records of each frame whose batch succeeded, since those are
// durable, and the write's error is the first frame's that failed.
func (w *logWrite) arrive() {
	if w.left.Add(-1) != 0 {
		return
	}

	for c, chunk := range w.chunks {
		if w.errs[c] == nil {
			w.s.apply(w.ops[chunk[0]:chunk[1]])
		} else if w.err == nil {
			w.err = w.errs[c]
		}
	}

	if w.after != nil {
		w.after()
	}

	w.s.writing.Add(-1)
	close(w.fin)
}

// wait returns the write's error once it has finished.
func (w *logWrite) wait() error {
	<-w.fin

	return w.err
}

// finished reports whether the write has finished.
func (w *logWrite) finished() bool {
	select {
	case <-w.fin:
		return true
	default:
		return false
	}
}

// logArchive is an archive appendArchive decided on: its write, nil if it
// appended nothing, what the write does, the EndTime of the completion it
// archives, and the channel settleArchive closes.
type logArchive struct {
	w       *logWrite
	outcome archiveOutcome
	endTime time.Time
	settled chan struct{}
}

// logStore is the store over the log in one directory.
type logStore struct {
	dir string
	ctx context.Context //nolint:containedctx // for the store's own log lines
	ch  codec.Handle
	w   *wal.Writer
	seq atomic.Uint64

	// closeMu is read-held by every append until the writer has its record,
	// and write-held by close to set closed, so no append reaches a closed
	// writer.
	closeMu sync.RWMutex
	closed  bool

	// mu guards state, which done funcs change once their records are durable.
	mu    sync.RWMutex
	state logState

	// claimMu guards claims, the keys claimed by the adds, deletes, modifies,
	// archives, put-backs and rep group lookups in flight, each to a channel
	// closed once that write has finished, so that writes of a key's spec or
	// its removal are made one at a time, each deciding and taking its seqs
	// after the last has applied (see logClaim).
	claimMu sync.Mutex
	claims  map[logKey]chan struct{}

	// limitMu makes each storeLimitGroups plan against what the last left.
	limitMu sync.Mutex

	// stateMu orders the 260929 rule's decisions with state records' seqs. It
	// is held by each add deciding whether to write a key's spec and state,
	// and by each put-back deciding whether its key is still not live, through
	// taking their seqs, and by each transition or exit taking its records'
	// seqs and noting its key in stateWrites, the keys with state records
	// appended and not yet finished. An add waits, with stateMu released,
	// until its key has none, so it never decides on a state that is appended
	// but not committed. A put-back need not wait: a key not live has no state
	// record it could outrank but a stale one, which its later spec drops.
	//
	// It also guards reviving, the keys a write in flight is making live: a
	// modify's new keys, storeLiveForRerun's and every put-back's, noted
	// before the write takes its seqs (see logClaim.noteReviving) until it has
	// applied. A transition or exit of such a key waits, holding no lock,
	// until it has none, then takes its seqs: its records are later than the
	// write's spec, so must apply after it, as replay applies them, not be
	// dropped as orphans for meeting the key not yet live. The server can make
	// them: it rekeys its queue to a modify's new key before the modify, and
	// keeps a job it puts back in its queue. A put-back of a key in reviving
	// waits for that write, then decides again (see putBack), so two never both
	// put a key back, yet one whose reviving write fails is still made.
	stateMu     sync.Mutex
	stateWrites map[logKey]*logPending
	reviving    map[logKey]*logPending

	// guardMu orders rerun guard decisions with their appends (spec "Write
	// path", rerun guard), and a guarded add's put-backs, which claim no key,
	// with archives, deletes and modifies. It guards inFlight, the archives
	// appended but not yet settled, each to a channel closed once settled,
	// guardAdds, the write of each guarded add, and archiveTxs.
	guardMu     sync.Mutex
	inFlight    map[logKey]chan struct{}
	guardAdds   map[*rerunGuard]*logWrite
	archiveTxs  int
	rerunGuards rerunGuards

	// writing counts the writes appended and not yet finished, and
	// exitUpdates the exit updates expected or not yet finished.
	writing     atomic.Int64
	exitUpdates atomic.Int64

	filesMu sync.Mutex
	files   map[uint32]*os.File

	failures    atomic.Uint64
	failLogMu   sync.Mutex
	failLogged  time.Time
	stats       logWriterReport
	statsEvery  time.Duration
	statsStop   chan struct{}
	statsExited chan struct{}

	// lock, if not nil, is the store lock close releases.
	lock *storeLock

	// history is history.bolt.
	history *historyBolt
}

// openLogStore opens, creating if need be, the log store in dir: it checks its
// VERSION, opens its history.bolt, replays the log there into memory and
// starts a writer on a new segment. The caller holds the store's lock (see
// openLogStoreLocked).
func openLogStore(ctx context.Context, dir string) (*logStore, error) {
	if err := prepareStoreDir(dir); err != nil {
		return nil, err
	}

	s := newLogStore(ctx, dir)

	history, err := openHistoryBolt(dir, s.ch)
	if err != nil {
		return nil, err
	}

	s.history = history

	next, err := s.recover(ctx)
	if err != nil {
		return nil, errors.Join(err, s.closeFiles(), s.history.close())
	}

	if s.w, err = wal.NewWriter(dir, next, walSegmentSize, s.writeFailed); err != nil {
		return nil, errors.Join(err, s.closeFiles(), s.history.close())
	}

	go s.reportStats()

	return s, nil
}

// newLogStore returns a log store of dir with empty state and no writer.
func newLogStore(ctx context.Context, dir string) *logStore {
	return &logStore{
		dir:         dir,
		ctx:         ctx,
		ch:          new(codec.BincHandle),
		state:       newLogState(),
		claims:      make(map[logKey]chan struct{}),
		stateWrites: make(map[logKey]*logPending),
		reviving:    make(map[logKey]*logPending),
		inFlight:    make(map[logKey]chan struct{}),
		guardAdds:   make(map[*rerunGuard]*logWrite),
		files:       make(map[uint32]*os.File),
		statsEvery:  archiveFoldReportInterval,
		statsStop:   make(chan struct{}),
		statsExited: make(chan struct{}),
	}
}

// nextSeq returns the next record's seq.
func (s *logStore) nextSeq() uint64 {
	return s.seq.Add(1)
}

// writeFailed is the writer's onError: it logs walWriteFailedLogMsg at most
// once per walWriteFailedLogEvery, with how many batches failed since the last.
func (s *logStore) writeFailed(err error) {
	s.failures.Add(1)

	s.failLogMu.Lock()
	defer s.failLogMu.Unlock()

	if time.Since(s.failLogged) < walWriteFailedLogEvery {
		return
	}

	s.failLogged = time.Now()
	failures := s.failures.Swap(0)

	clog.Error(s.ctx, walWriteFailedLogMsg, "err", err, "failures", failures)
}

// reportStats logs the log writer's "db writer" line every statsEvery until
// close stops it.
func (s *logStore) reportStats() {
	defer internal.LogPanic(s.ctx, "jobqueue log store stats reporter", false)
	defer close(s.statsExited)

	ticker := time.NewTicker(s.statsEvery)
	defer ticker.Stop()

	for {
		select {
		case <-s.statsStop:
			return
		case <-ticker.C:
			s.stats.report(s.ctx, s.statsEvery, s.w.Stats(), s.w.TakeMaxSync())
		}
	}
}

// write appends f's records: one plain frame for one record, else one s2
// block, or, if their frames exceed logBlockMaxFrames, as many as hold them,
// split only between f's groups. Those are appended together with
// AppendBlocks, so they share one batch: a failed batch fails them all, and
// none is applied. A crash before the batch is durable can still leave a
// prefix of its blocks on disk, so the records of earlier blocks recovered
// without those of later ones, as bbolt's chunked adds (storeNewJobDataChunked,
// storeBatched) can leave earlier chunks stored; never a later block without
// an earlier one. An add puts its jobs' records before its depgroup and
// put-back records, so such a prefix never holds a put-back without the jobs
// it was put back for, and a retry of the add writes what the prefix lacks. A
// group, such as a job's records, is never separated. after, if not nil, is
// called once the write has finished. The caller holds closeMu for reading and
// has checked closed.
func (s *logStore) write(f *logFrames, after func()) (*logWrite, error) {
	w := &logWrite{s: s, ops: f.ops, block: len(f.ops) > 1, after: after, fin: make(chan struct{})}
	w.chunks = f.chunks(logBlockMaxFrames)
	w.errs = make([]error, len(w.chunks))
	w.left.Store(int32(len(w.chunks) + 1)) //nolint:gosec // a handful of chunks

	if !w.block {
		s.writing.Add(1)

		ref, _ := s.w.Append(f.frames[wal.FrameHeader:], w.doneFor(0))
		w.placed([]wal.Ref{ref})

		return w, nil
	}

	blocks, ns, err := f.blocks(w.chunks)
	if err != nil {
		return nil, err
	}

	dones := make([]func(error), len(blocks))
	for i := range dones {
		dones[i] = w.doneFor(i)
	}

	s.writing.Add(1)

	refs, _ := s.w.AppendBlocks(blocks, ns, dones)
	w.placed(refs)

	return w, nil
}

// writeAndWait is write then wait, with closeMu held for the write.
func (s *logStore) writeAndWait(f *logFrames) error {
	w, err := s.writeOpen(f, nil)
	if err != nil {
		return err
	}

	return w.wait()
}

// writeOpen is write, taking closeMu itself and returning errDBClosed after
// close.
func (s *logStore) writeOpen(f *logFrames, after func()) (*logWrite, error) {
	s.closeMu.RLock()
	defer s.closeMu.RUnlock()

	if s.closed {
		return nil, errDBClosed
	}

	return s.write(f, after)
}

// close makes every record appended before it durable, then refuses further
// writes with errDBClosed.
func (s *logStore) close(ctx context.Context) error {
	s.closeMu.Lock()
	already := s.closed
	s.closed = true
	s.closeMu.Unlock()

	if already {
		return nil
	}

	err := s.w.Close()

	close(s.statsStop)
	<-s.statsExited
	s.stats.report(ctx, s.statsEvery, s.w.Stats(), s.w.TakeMaxSync())

	return errors.Join(err, s.closeFiles(), s.history.close(), s.lock.release())
}

// closeFiles closes the files opened to read records.
func (s *logStore) closeFiles() error {
	s.filesMu.Lock()
	defer s.filesMu.Unlock()

	errs := make([]error, 0, len(s.files))

	for n, f := range s.files {
		errs = append(errs, f.Close())

		delete(s.files, n)
	}

	return errors.Join(errs...)
}

// readRecord returns the record ref names.
func (s *logStore) readRecord(ref wal.Ref) ([]byte, error) {
	f, err := s.segmentFile(ref.File)
	if err != nil {
		return nil, err
	}

	return wal.ReadAt(f, ref)
}

// segmentFile returns segment n, opened for reading.
func (s *logStore) segmentFile(n uint32) (*os.File, error) {
	s.filesMu.Lock()
	defer s.filesMu.Unlock()

	if f := s.files[n]; f != nil {
		return f, nil
	}

	f, err := os.Open(walFilePath(s.dir, n))
	if err != nil {
		return nil, err
	}

	s.files[n] = f

	return f, nil
}

// readRecordBody returns the body after the key of the record ref names, which
// must be of type typ.
func (s *logStore) readRecordBody(ref wal.Ref, typ logRecordType) ([]byte, error) {
	rec, err := s.readRecord(ref)
	if err != nil {
		return nil, err
	}

	r, err := parseLogRecord(rec)
	if err != nil {
		return nil, err
	}

	if r.typ != typ {
		return nil, fmt.Errorf("%w: %+v is type %d, not %d", errLogRecordMalformed, ref, r.typ, typ)
	}

	return r.body, nil
}

// decodeJob returns the job made of the spec record at spec and the run state
// record (state or complete) at state.
func (s *logStore) decodeJob(spec, state wal.Ref, stateType logRecordType) (*Job, error) {
	specBody, err := s.readRecordBody(spec, logRecSpec)
	if err != nil {
		return nil, err
	}

	stateBody, err := s.readRecordBody(state, stateType)
	if err != nil {
		return nil, err
	}

	return s.jobOf(specBody, stateBody)
}

// jobOf returns the job made of a flat spec and flat run state.
func (s *logStore) jobOf(spec, state []byte) (*Job, error) {
	job := &Job{}

	if err := decodeFlatSpec(spec, job, s.ch); err != nil {
		return nil, err
	}

	var rs logRunState

	if err := decodeFlatRunState(state, &rs); err != nil {
		return nil, err
	}

	rs.applyTo(job)

	return job, nil
}

// checkIfLive tells you if the keyed job is live.
func (s *logStore) checkIfLive(key string) (bool, error) {
	k, err := parseLogKey(key)
	if err != nil {
		return false, err
	}

	return s.liveKey(k), nil
}

// checkIfComplete tells you if the keyed job has a history entry.
func (s *logStore) checkIfComplete(key string) (bool, error) {
	k, err := parseLogKey(key)
	if err != nil {
		return false, err
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	_, complete := s.state.history[k]

	return complete, nil
}

// depGroupEverSeen says whether a job with depGroup has ever been added.
func (s *logStore) depGroupEverSeen(depGroup string) (bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	_, seen := s.state.depGroupsSeen[depGroup]

	return seen, nil
}

// depGroupsEverSeen is depGroupEverSeen for each of depGroups.
func (s *logStore) depGroupsEverSeen(depGroups []string) (map[string]bool, error) {
	seen := make(map[string]bool, len(depGroups))

	s.mu.RLock()
	defer s.mu.RUnlock()

	for _, depGroup := range depGroups {
		_, seen[depGroup] = s.state.depGroupsSeen[depGroup]
	}

	return seen, nil
}

// storeEnv stores env unless it is already stored, returning the key it can be
// retrieved by.
func (s *logStore) storeEnv(env []byte) (string, error) {
	envkey := byteKey(env)

	s.mu.RLock()
	_, stored := s.state.envs[envkey]
	s.mu.RUnlock()

	if stored {
		return envkey, nil
	}

	var f logFrames

	seq := s.nextSeq()
	f.add(appendEnvRecord(f.rec, seq, envkey, env),
		logOp{typ: logRecEnv, seq: seq, name: envkey, blob: append([]byte(nil), env...)})

	return envkey, s.writeAndWait(&f)
}

// retrieveEnv returns the env stored under envkey, or nil.
func (s *logStore) retrieveEnv(_ context.Context, envkey string) []byte {
	s.mu.RLock()
	defer s.mu.RUnlock()

	return s.state.envs[envkey]
}

// storeLimitGroups stores each count group's limit and removes each group
// given an invalid limit, as the bbolt store does: it returns the groups whose
// limit is new or different, and those removed.
func (s *logStore) storeLimitGroups(groups map[string]*limiter.GroupData) (changed, removed []string, err error) {
	s.limitMu.Lock()
	defer s.limitMu.Unlock()

	var f logFrames

	s.mu.RLock()

	for group, limitG := range groups {
		outcome, write := planLimitGroup(s.state.limitBytes(group), limitG)
		changed, removed = recordLimitGroupOutcome(outcome, group, changed, removed)

		if write == limitGroupWriteNone {
			continue
		}

		f.group()

		seq, gone := s.nextSeq(), write == limitGroupWriteDelete
		f.add(appendLimitRecord(f.rec, seq, group, limitG.Limit(), gone),
			logOp{typ: logRecLimit, seq: seq, name: group, limit: limitG.Limit(), removed: gone})
	}

	s.mu.RUnlock()

	if len(f.ops) == 0 {
		return changed, removed, nil
	}

	return changed, removed, s.writeAndWait(&f)
}

// retrieveLimitGroup returns the stored limit of group, or, as the bbolt store
// does, a time group's own limit, or an invalid GroupData if it has none.
func (s *logStore) retrieveLimitGroup(_ context.Context, group string) *limiter.GroupData {
	if _, gd := limiter.NameToGroupData(group); gd.IsValid() && !gd.IsCount() {
		return gd
	}

	s.mu.RLock()
	limit, stored := s.state.limits[group]
	s.mu.RUnlock()

	if !stored {
		return limiter.NewCountGroupData(-1)
	}

	return limiter.NewCountGroupData(limit)
}

// retrieveStoredLimits returns the limit stored for each of groups, in order,
// or -1 for one with none.
func (s *logStore) retrieveStoredLimits(groups []string) ([]int64, error) {
	limits := make([]int64, len(groups))

	s.mu.RLock()
	defer s.mu.RUnlock()

	for i, group := range groups {
		limit, stored := s.state.limits[group]
		if !stored {
			limit = -1
		}

		limits[i] = limit
	}

	return limits, nil
}

// storeRepGroupLookups records each of jobs under its RepGroup, so it can be
// found by it once complete. It is for queued jobs added again under another
// RepGroup. It claims their keys (see logClaim), as a modify, which drops an
// old key's lookups, does.
func (s *logStore) storeRepGroupLookups(_ context.Context, jobs []*Job) error {
	if len(jobs) == 0 {
		return nil
	}

	keys, err := jobLogKeys(jobs)
	if err != nil {
		return err
	}

	claim := s.claimKeySet(keys...)
	defer claim.release()

	var f logFrames

	for i, job := range jobs {
		job.RLock()
		repGroup := job.RepGroup
		job.RUnlock()

		s.addRGLookup(&f, repGroup, keys[i])
	}

	return s.writeAndWait(&f)
}

// addRGLookup adds an rglookup record of k under repGroup to f, starting a
// group.
func (s *logStore) addRGLookup(f *logFrames, repGroup string, k logKey) {
	f.group()
	s.addRGLookupRecord(f, repGroup, k)
}

// addSpecAndState adds job's spec and state records to f, taking their seqs
// under job's read lock.
func (s *logStore) addSpecAndState(f *logFrames, job *Job, k logKey) error {
	job.RLock()
	defer job.RUnlock()

	return s.addSpecAndStateLocked(f, job, k, newLogRunState(job))
}

// addSpecAndStateLocked adds job's spec record and rs as its state record to
// f. The caller holds job's read lock, or job is not shared.
func (s *logStore) addSpecAndStateLocked(f *logFrames, job *Job, k logKey, rs logRunState) error {
	if err := s.addSpecLocked(f, job, k); err != nil {
		return err
	}

	s.addState(f, logRecState, k, &rs)

	return nil
}

// addState adds rs as k's record of typ (state or complete) to f.
func (s *logStore) addState(f *logFrames, typ logRecordType, k logKey, rs *logRunState) {
	s.addStateAt(f, typ, s.nextSeq(), k, rs)
}

// updateJobRunStateDurable records job's run state and waits until it is
// durable.
func (s *logStore) updateJobRunStateDurable(job *Job) error {
	return s.writeJobState(job)
}

// updateJobAfterChangeDurable records job's state after a change and waits
// until it is durable. Everything a transition changes is in its run state, so
// it writes one state record, as updateJobRunStateDurable does.
func (s *logStore) updateJobAfterChangeDurable(job *Job) error {
	return s.writeJobState(job)
}

// writeJobState appends job's state record and waits until it is finished.
func (s *logStore) writeJobState(job *Job) error {
	k, err := parseLogKey(job.Key())
	if err != nil {
		return err
	}

	f, finished := s.stateFrames(job, k)

	w, err := s.writeOpen(&f, finished)
	if err != nil {
		finished()

		return err
	}

	return w.wait()
}

// awaitStateWrites returns, with stateMu held as when called, once k has no
// state record appended and not finished.
func (s *logStore) awaitStateWrites(k logKey) {
	for w := s.stateWrites[k]; w != nil; w = s.stateWrites[k] {
		s.stateMu.Unlock()

		if logStoreWaitHook != nil {
			logStoreWaitHook()
		}

		<-w.idle
		s.stateMu.Lock()
	}
}

// updateJobAfterExit records job's state after an exit, with its std (kept
// only if it failed or forceStorage) and exit sample, without waiting for it.
func (s *logStore) updateJobAfterExit(ctx context.Context, job *Job, stdo, stde []byte, forceStorage bool) {
	if _, err := s.appendExit(job, stdo, stde, forceStorage); err != nil && !errors.Is(err, errDBClosed) {
		clog.Error(ctx, "Database operation updateJobAfterExit failed", "err", err)
	}
}

// updateJobAfterExitDurable is updateJobAfterExit, waiting until it is durable.
func (s *logStore) updateJobAfterExitDurable(job *Job, stdo, stde []byte, forceStorage bool) error {
	w, err := s.appendExit(job, stdo, stde, forceStorage)
	if err != nil {
		return err
	}

	return w.wait()
}

// appendExit appends job's exit block: its state record, its exitstat record
// and a std record per stream, the new std if it failed or forceStorage, else
// empty to clear any earlier std. retrieveJobStd waits for it to finish.
func (s *logStore) appendExit(job *Job, stdo, stde []byte, forceStorage bool) (*logWrite, error) {
	k, err := parseLogKey(job.Key())
	if err != nil {
		return nil, err
	}

	f, finished := s.exitFrames(job, k, stdo, stde, forceStorage)

	s.exitUpdates.Add(1)

	after := func() {
		finished()
		s.exitUpdates.Add(-1)
	}

	w, err := s.writeOpen(&f, after)
	if err != nil {
		after()
	}

	return w, err
}

// exitFrames returns the records of job's exit: its state, its exitstat and
// its std, and the func to call once their write has finished (see
// addTransition).
func (s *logStore) exitFrames(job *Job, k logKey, stdo, stde []byte, forceStorage bool) (logFrames, func()) {
	var (
		f    logFrames
		exit jobExitData
		rs   logRunState
		seq  uint64
	)

	finished := s.addTransition(job, k, func() bool {
		exit, rs = newJobExitData(job, stdo, stde, forceStorage), newLogRunState(job)
		seq = s.takeSeqs(exitRecords)

		return true
	})

	if logTransitionSeqsHook != nil {
		logTransitionSeqsHook()
	}

	if exit.exitcode == 0 && !forceStorage {
		stdo, stde = nil, nil
	}

	s.addExitRecords(&f, k, seq, &rs, &exit, [...][]byte{stdOut: stdo, stdErr: stde})

	return f, finished
}

// addStd adds k's std record of stream to f.
func (s *logStore) addStd(f *logFrames, k logKey, stream uint8, std []byte) {
	s.addStdAt(f, s.nextSeq(), k, stream, std)
}

// expectJobExitUpdate makes retrieveJobStd wait for an exit update the caller
// may be about to make, until the returned func is called.
func (s *logStore) expectJobExitUpdate() func() {
	s.exitUpdates.Add(1)

	return func() { s.exitUpdates.Add(-1) }
}

// retrieveJobStd returns the keyed job's stored std, once no exit update is
// expected or in flight.
func (s *logStore) retrieveJobStd(ctx context.Context, jobkey string) (stdo, stde []byte) {
	for s.exitUpdates.Load() != 0 {
		<-time.After(jobExitUpdatePollInterval)
	}

	k, err := parseLogKey(jobkey)
	if err != nil {
		return nil, nil
	}

	s.mu.RLock()
	refs, has := s.state.stdRefs(k)
	s.mu.RUnlock()

	std := [2][]byte{}

	for stream := range std {
		if !has[stream] {
			continue
		}

		if std[stream], err = s.readRecordBody(refs[stream], logRecStd); err != nil {
			clog.Error(ctx, "Database retrieve failed", "err", err)
		}
	}

	return std[stdOut], std[stdErr]
}

// registerRerunGuard returns a new guard, held until releaseRerunGuard, for an
// add about to read its dependents. Archives appended but not yet settled may
// commit after the add reads, so the guard starts with them archived.
func (s *logStore) registerRerunGuard() *rerunGuard {
	s.guardMu.Lock()
	defer s.guardMu.Unlock()

	g := s.rerunGuards.register()

	for k := range s.inFlight {
		g.noteArchived(k.String())
	}

	return g
}

// releaseRerunGuard stops g seeing archives.
func (s *logStore) releaseRerunGuard(g *rerunGuard) {
	s.guardMu.Lock()
	defer s.guardMu.Unlock()

	delete(s.guardAdds, g)
	s.rerunGuards.release(g)
}

// archiveCompletion records job's successful completion in history and,
// unless it must run again, removes it from the live plane, in one block. It
// claims key from before it decides until it has applied (see logClaim), so it
// decides after any add, delete, modify or put-back of key in flight has
// applied, and none decides until it has.
func (s *logStore) archiveCompletion(key string, job *Job) (archiveOutcome, error) {
	k, err := parseLogKey(key)
	if err != nil {
		return archiveNotWritten, err
	}

	claim := s.claimKeySet(k)
	defer claim.release()

	a, err := s.appendArchiveOpen(k, key, job)
	if err != nil || a.w == nil {
		return a.outcome, err
	}

	defer s.settleArchive(k, a.settled)

	if err = a.w.wait(); err != nil {
		return archiveNotWritten, err
	}

	job.Lock()
	job.archivedEndTime = a.endTime
	job.Unlock()

	return a.outcome, nil
}

// appendArchiveOpen is appendArchive, returning errDBClosed after close.
func (s *logStore) appendArchiveOpen(k logKey, key string, job *Job) (logArchive, error) {
	s.closeMu.RLock()
	defer s.closeMu.RUnlock()

	if s.closed {
		return logArchive{}, errDBClosed
	}

	return s.appendArchive(k, key, job)
}

// appendArchive decides and appends the archive of job under guardMu, once no
// guarded add whose dependent k is unfinished.
func (s *logStore) appendArchive(k logKey, key string, job *Job) (logArchive, error) {
	s.guardMu.Lock()
	defer s.guardMu.Unlock()

	s.awaitArchivePeers(key)

	a := logArchive{outcome: archiveAlreadyWritten}

	var already bool
	if a.endTime, already = job.completionArchived(); already {
		return a, nil
	}

	s.archiveTxs++

	f, err := s.decideArchive(&a, k, job)
	if err != nil {
		return logArchive{}, err
	}

	if logArchiveDecidedHook != nil {
		logArchiveDecidedHook()
	}

	return a, s.writeArchive(&a, k, &f)
}

// awaitArchivePeers returns, with guardMu held as when called, once no guarded
// add with key as a dependent is unfinished. No other archive of key is
// unsettled: the caller holds key's claim, which an archive releases only once
// settled.
func (s *logStore) awaitArchivePeers(key string) {
	for {
		wait := s.unfinishedGuardAdd(key)
		if wait == nil {
			return
		}

		s.guardMu.Unlock()
		<-wait
		s.guardMu.Lock()
	}
}

// unfinishedGuardAdd returns the fin channel of the write of an unfinished add
// whose guard has key as a dependent, or nil. The caller holds guardMu.
func (s *logStore) unfinishedGuardAdd(key string) chan struct{} {
	s.rerunGuards.mu.Lock()
	defer s.rerunGuards.mu.Unlock()

	for g := range s.rerunGuards.guards {
		w := s.guardAdds[g]
		if w == nil || w.finished() {
			continue
		}

		g.mu.Lock()
		dependent := g.known && g.isDependentLocked(key)
		g.mu.Unlock()

		if dependent {
			return w.fin
		}
	}

	return nil
}

// addStored is the rerun guards' addStored: whether member, an add's first
// job's key, is live.
func (s *logStore) addStored(member []byte) bool {
	live, err := s.checkIfLive(string(member))

	return err == nil && live
}

// addArchiveRecords adds an archive's records of k to f: its complete record of
// rs and either its delete record or, if keepLive, a state record for it to
// run again, as rerunRecords makes them.
func (s *logStore) addArchiveRecords(f *logFrames, k logKey, rs *logRunState, keepLive bool) {
	if !keepLive {
		s.addState(f, logRecComplete, k, rs)
		s.addDelete(f, k)

		return
	}

	rs.RerunAfterRun = false
	s.addState(f, logRecComplete, k, rs)

	rs.ReservedBy = uuid.UUID{}
	s.addState(f, logRecState, k, rs)
}

// settleArchive forgets k's archive as in flight, once its caller is done with
// it.
func (s *logStore) settleArchive(k logKey, settled chan struct{}) {
	s.guardMu.Lock()
	delete(s.inFlight, k)
	s.guardMu.Unlock()

	close(settled)
}

// recoverIncompleteJobs returns every live job, as its latest durable spec and
// state; every one has its latest state record applied.
func (s *logStore) recoverIncompleteJobs() ([]*Job, runStateRecovery, error) {
	jobs, err := s.liveJobs()

	return jobs, runStateRecovery{applied: len(jobs)}, err
}

// storeNewJobsGuarded stores jobs live in one s2 block, as the bbolt store's
// storeNewJobsGuarded does: with ignoreAdded, ones already complete are counted
// and not stored; a key already live and handed out keeps its record; every
// job's RepGroup lookup and each dep group not seen before are recorded; and
// archived jobs depending on a dep group the add joins are put back live, as
// are those guard saw archived after the add read them live. It returns once
// the block is durable, and any further block of put-backs it deferred (see
// putBackDeferred).
func (s *logStore) storeNewJobsGuarded(_ context.Context, jobs []*Job, ignoreAdded bool,
	guard *rerunGuard,
) (storedNewJobs, error) {
	keys, err := jobLogKeys(jobs)
	if err != nil {
		return storedNewJobs{}, err
	}

	add, claim, err := s.planClaimedAdd(jobs, keys, ignoreAdded)
	defer claim.release()

	stored := add.stored()

	if err != nil {
		return stored, err
	}

	atRisk := guardDependentsRead(guard, add.jobsToUpdate, add.memberKey())
	if len(add.frames.ops) == 0 {
		return stored, nil
	}

	w, pb, err := s.appendAdd(&add.frames, guard, atRisk, claim)
	if err == nil {
		err = s.finishAdd(w, pb, claim)
	}

	// pb.put holds the keys of the add's first block and, as finishAdd adds
	// them, those of each further block of deferred put-backs made durable.
	stored.dependents.putBack = pb.put

	return stored, err
}

// planAdd builds an add's records: spec and state records of each job to
// store, an rglookup of each job not already complete, a depgroup record for
// each new dep group, and spec and state records putting back archived
// dependents.
func (s *logStore) planAdd(jobs []*Job, ignoreAdded bool, claim *logClaim) (logAdd, error) {
	add := logAdd{depGroups: make(map[string]bool), newKeys: make(map[string]bool), claim: claim}

	var dups duplicateCounter

	for _, job := range jobs {
		key := job.Key()

		complete, err := s.countIfComplete(job, key, ignoreAdded, &dups)
		if err == nil && !complete {
			err = s.planAddJob(&add, job, key)
		}

		if err != nil {
			return add, err
		}
	}

	add.alreadyAdded = dups.duplicates()

	if len(add.kept) == 0 {
		return add, nil
	}

	err := s.planDependents(&add)
	add.jobsToQueue = append(add.jobsToQueue, add.kept...)

	return add, err
}

// countIfComplete reports, if ignoreAdded, whether the keyed job is already
// complete, counting it in dups if so.
func (s *logStore) countIfComplete(job *Job, key string, ignoreAdded bool, dups *duplicateCounter) (bool, error) {
	if !ignoreAdded {
		return false, nil
	}

	complete, elsewhere, err := s.completeUnderRepGroup(job, key)
	if complete && err == nil {
		dups.count(elsewhere)
	}

	return complete, err
}

// planAddJob adds one job the add keeps to it: its rglookup record, and its
// spec and state records unless it is live and handed out, which the add must
// not touch. It decides once no state record of the job is appended and not
// finished, under stateMu, so a transition's state record either is decided on
// or takes a later seq than the add's spec.
func (s *logStore) planAddJob(add *logAdd, job *Job, key string) error {
	k, err := parseLogKey(key)
	if err != nil {
		return err
	}

	s.addRGLookup(&add.frames, add.keep(job, key), k)

	job.RLock()
	defer job.RUnlock()

	s.stateMu.Lock()
	defer s.stateMu.Unlock()

	s.awaitStateWrites(k)

	if s.liveHandedOut(k) {
		return nil
	}

	return s.addSpecAndStateLocked(&add.frames, job, k, newLogRunState(job))
}

// liveHandedOut reports whether k is live and its state is one an add must not
// touch (see liveRecordHandedOut).
func (s *logStore) liveHandedOut(k logKey) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()

	e := s.state.live[k]

	return e != nil && e.handedOut
}

// completeUnderRepGroup is the bbolt store's checkIfCompleteUnderRepGroup:
// whether the keyed job is complete and, if its RepGroup was not looked up
// under job's RepGroup, the RepGroup it completed under.
func (s *logStore) completeUnderRepGroup(job *Job, key string) (bool, *completedElsewhere, error) {
	k, err := parseLogKey(key)
	if err != nil {
		return false, nil, err
	}

	job.RLock()
	repGroup := job.RepGroup
	job.RUnlock()

	s.mu.RLock()
	_, complete := s.state.history[k]
	_, lookedUp := s.state.repGroupKeys[repGroup][k]
	s.mu.RUnlock()

	if !complete || lookedUp {
		return complete, nil, nil
	}

	var elsewhere *completedElsewhere

	err = s.viewHistoryKeys([]logKey{k}, func(r *historyRead) error {
		var errc error

		elsewhere, errc = r.completedElsewhere(k, repGroup)

		return errc
	})

	return true, elsewhere, err
}

// planDependents adds to add a depgroup record for each of its dep groups not
// seen before, and finds the jobs depending on them, recursively: live ones to
// update, and archived ones it does not add, to put back live and queue. One
// found archived that is live by the time it would be put back (another add
// re-added it) is updated instead.
func (s *logStore) planDependents(add *logAdd) error {
	if len(add.depGroups) == 0 {
		return nil
	}

	s.addUnseenDepGroups(&add.frames, add.depGroups)

	archived, live, err := s.dependentJobs(add.depGroups, add.newKeys)
	if err != nil {
		return err
	}

	add.jobsToUpdate = live

	if logPutBackHook != nil && len(archived) > 0 {
		logPutBackHook()
	}

	return s.putBackArchived(add, archived)
}

// putBackArchived adds to add the records putting back each of archived, to
// queue, or, for one live by now, adds it to those to update. It claims each
// one's key first, returning errLogKeyClaimed, with the key in add.claimed, if
// another write has it claimed.
func (s *logStore) putBackArchived(add *logAdd, archived []*Job) error {
	for _, job := range archived {
		put, err := s.claimAndPutBack(add, job)
		if err != nil {
			return err
		}

		if put {
			add.jobsToQueue = append(add.jobsToQueue, job)
		} else {
			add.jobsToUpdate = append(add.jobsToUpdate, job)
		}
	}

	return nil
}

// addUnseenDepGroups adds to f a depgroup record of each of depGroups not seen
// before, in name order.
func (s *logStore) addUnseenDepGroups(f *logFrames, depGroups map[string]bool) {
	s.mu.RLock()

	unseen := make([]string, 0, len(depGroups))

	for dg := range depGroups {
		if _, seen := s.state.depGroupsSeen[dg]; !seen {
			unseen = append(unseen, dg)
		}
	}

	s.mu.RUnlock()

	slices.Sort(unseen)

	for _, dg := range unseen {
		f.group()

		seq := s.nextSeq()
		f.add(appendDepGroupRecord(f.rec, seq, dg), logOp{typ: logRecDepGroup, seq: seq, name: dg})
	}
}

// addPutBack is putBack for job, decoded from its history entry, as k.
func (s *logStore) addPutBack(f *logFrames, job *Job, k logKey, claim *logClaim) (bool, chan struct{}, error) {
	job.RLock()
	defer job.RUnlock()

	return s.putBack(f, job, k, claim)
}

// putBack adds to f, as a group, the spec and state records putting job back
// live as k, noting k in reviving for claim's write (see
// logClaim.unnoteReviving), and reports true, unless k is live by now, when it
// adds nothing, or another write in flight is making it live, when it adds
// nothing and returns that write's idle channel: the caller decides again once
// it is closed, so an add's claimed put-back and a guarded add's put-back,
// which claims nothing, of the same key never both put it back, yet a key whose
// reviving write fails is still put back. It decides and takes their seqs
// under stateMu, as planAddJob does, so the state record of any reservation of
// a re-add it did not see live is later than its spec, and outranks it. The
// caller holds job's read lock, or job is not shared.
func (s *logStore) putBack(f *logFrames, job *Job, k logKey, claim *logClaim) (bool, chan struct{}, error) {
	s.stateMu.Lock()
	defer s.stateMu.Unlock()

	if s.liveKey(k) {
		return false, nil, nil
	}

	if p := s.reviving[k]; p != nil {
		return false, p.idle, nil
	}

	f.group()

	if err := s.addSpecAndStateLocked(f, job, k, newLogRunState(job)); err != nil {
		return false, nil, err
	}

	claim.noteRevivingLocked(k)

	return true, nil, nil
}

// liveKey reports whether k is live.
func (s *logStore) liveKey(k logKey) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()

	_, live := s.state.live[k]

	return live
}

// dependentJobs is the bbolt store's retrieveDependentJobs over the reverse dep
// group index: the jobs depending on depGroups, or on the dep groups of those
// jobs, recursively; live ones in jobsToUpdate, and archived ones whose keys
// are not in newKeys in jobsToQueue.
func (s *logStore) dependentJobs(depGroups, newKeys map[string]bool) (jobsToQueue, jobsToUpdate []*Job, err error) {
	scan := logDependentsScan{
		s:          s,
		newKeys:    newKeys,
		seenGroups: maps.Clone(depGroups),
		done:       make(map[logKey]bool),
	}

	for next := slices.Sorted(maps.Keys(depGroups)); len(next) > 0; {
		var found []string

		for _, dg := range next {
			more, errs := scan.group(dg)
			if errs != nil {
				return nil, nil, errs
			}

			found = append(found, more...)
		}

		slices.Sort(found)
		next = found
	}

	return scan.jobsToQueue, scan.jobsToUpdate, nil
}

// reverseDepKeys returns, sorted, the keys of the jobs depending on depGroup.
func (s *logStore) reverseDepKeys(depGroup string) []logKey {
	s.mu.RLock()
	keys := slices.Collect(maps.Keys(s.state.reverseDeps[depGroup]))
	s.mu.RUnlock()

	slices.SortFunc(keys, func(a, b logKey) int { return bytes.Compare(a[:], b[:]) })

	return keys
}

// dependentJob returns the keyed job and true if it is live, else its history
// entry's job unless its key is in newKeys, else nil.
func (s *logStore) dependentJob(k logKey, newKeys map[string]bool) (*Job, bool, error) {
	s.mu.RLock()
	e := s.state.live[k]

	var spec, state wal.Ref
	if e != nil {
		spec, state = e.spec, e.state
	}

	s.mu.RUnlock()

	if e != nil {
		job, err := s.decodeJob(spec, state, logRecState)

		return job, true, err
	}

	if newKeys[k.String()] {
		return nil, false, nil
	}

	job, _, err := s.historyJob(k)

	return job, false, err
}

// appendAdd appends an add's records. If atRisk is not nil, it first adds
// records putting back each dependent atRisk reports that is no longer live but
// complete, once no archive of one is unsettled, and appends under guardMu,
// recording the write as guard's if guard is not nil. It returns what those
// put-backs did, the keys put back noted in reviving for the write. If f is
// then empty, it appends nothing and returns a nil write.
func (s *logStore) appendAdd(f *logFrames, guard *rerunGuard, atRisk func() []string,
	claim *logClaim,
) (*logWrite, logPutBacks, error) {
	s.closeMu.RLock()
	defer s.closeMu.RUnlock()

	if s.closed {
		return nil, logPutBacks{}, errDBClosed
	}

	if atRisk == nil {
		w, err := s.write(f, nil)

		return w, logPutBacks{}, err
	}

	return s.appendGuardedAdd(f, guard, atRisk, claim)
}

// appendGuardedAdd is appendAdd with an atRisk, under guardMu. Its put-backs
// claim no key, so nothing but guardMu orders them with a delete or modify of
// the key: they rely on every delete and modify taking its seqs and appending
// under guardMu too (appendRemoval), so whichever of them appends first has
// lower seqs and applies first (it has its Refs by then, and its batch is
// durable first). An archive likewise decides and appends under guardMu. A key
// another write in flight is making live is deferred, not waited for: the
// caller waits for that write once its own has finished (putBackDeferred), so
// no write waits, before it has appended, for one that waits for it.
func (s *logStore) appendGuardedAdd(f *logFrames, guard *rerunGuard, atRisk func() []string,
	claim *logClaim,
) (*logWrite, logPutBacks, error) {
	s.guardMu.Lock()
	defer s.guardMu.Unlock()

	keys := s.awaitAtRisk(atRisk)

	pb, err := s.addGuardPutBacks(f, keys, claim)
	if err != nil || len(f.ops) == 0 {
		return nil, pb, err
	}

	w, err := s.write(f, claim.unnoteReviving)
	if err == nil && guard != nil {
		s.guardAdds[guard] = w
	}

	return w, pb, err
}

// awaitAtRisk returns atRisk's keys, with guardMu held as when called, once no
// archive of any of them is unsettled.
func (s *logStore) awaitAtRisk(atRisk func() []string) []string {
	for {
		keys := atRisk()

		var wait chan struct{}

		for _, key := range keys {
			if k, err := parseLogKey(key); err == nil && s.inFlight[k] != nil {
				wait = s.inFlight[k]

				break
			}
		}

		if wait == nil {
			return keys
		}

		s.guardMu.Unlock()
		<-wait
		s.guardMu.Lock()
	}
}

// addGuardPutBacks adds to f records putting back live, as rerunRecords' live
// record of their history entries, each of keys that is not live but complete,
// noting them in reviving for claim's write, and returns what it did: a key
// another write in flight is making live is deferred (see putBack).
func (s *logStore) addGuardPutBacks(f *logFrames, keys []string, claim *logClaim) (logPutBacks, error) {
	pb := logPutBacks{put: make(map[string]bool), deferred: make(map[string]chan struct{})}

	for _, key := range keys {
		put, wait, err := s.addGuardPutBack(f, key, claim)
		if err != nil {
			return pb, err
		}

		if put {
			pb.put[key] = true
		}

		if wait != nil {
			pb.deferred[key] = wait
		}
	}

	return pb, nil
}

// addGuardPutBack is addGuardPutBacks for one key, reporting whether it put it
// back, or the idle channel of the write making it live if it deferred it.
func (s *logStore) addGuardPutBack(f *logFrames, key string, claim *logClaim) (bool, chan struct{}, error) {
	k, err := parseLogKey(key)
	if err != nil {
		return false, nil, err
	}

	if s.liveKey(k) {
		return false, nil, nil
	}

	job, complete, err := s.historyJob(k)
	if err != nil || !complete {
		return false, nil, err
	}

	job.RerunAfterRun = false
	job.ReservedBy = uuid.UUID{}

	if logPutBackHook != nil {
		logPutBackHook()
	}

	return s.putBack(f, job, k, claim)
}

// logDependentsScan is the state of a dependentJobs traversal.
type logDependentsScan struct {
	s                         *logStore
	newKeys, seenGroups       map[string]bool
	done                      map[logKey]bool
	jobsToQueue, jobsToUpdate []*Job
}

// group classifies each job depending on depGroup not already done, returning
// the dep groups of those jobs not seen before.
func (d *logDependentsScan) group(depGroup string) ([]string, error) {
	var found []string

	for _, k := range d.s.reverseDepKeys(depGroup) {
		if d.done[k] {
			continue
		}

		d.done[k] = true

		job, live, err := d.s.dependentJob(k, d.newKeys)
		if err != nil {
			return nil, err
		}

		if job != nil {
			found = append(found, d.take(job, live)...)
		}
	}

	return found, nil
}

// take classifies job, live or archived, returning its dep groups not seen
// before.
func (d *logDependentsScan) take(job *Job, live bool) []string {
	if live {
		d.jobsToUpdate = append(d.jobsToUpdate, job)
	} else {
		d.jobsToQueue = append(d.jobsToQueue, job)
	}

	return newDepGroups(job.DepGroups, d.seenGroups)
}

// newDepGroups returns those of depGroups not in seen, adding them to it.
func newDepGroups(depGroups []string, seen map[string]bool) []string {
	var found []string

	for _, dg := range depGroups {
		if dg != "" && !seen[dg] {
			seen[dg] = true
			found = append(found, dg)
		}
	}

	return found
}
