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

// This file is the store record/replay harness (spec A5): recordingStore
// records every call made across a store, and replayRecording replays such a
// recording against another store, reporting where the answers differ.

import (
	"cmp"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"reflect"
	"slices"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/VertebrateResequencing/wr/clog"
	"github.com/VertebrateResequencing/wr/jobqueue/scheduler"
	"github.com/VertebrateResequencing/wr/jobqueue/wal"
	"github.com/VertebrateResequencing/wr/limiter"
	"github.com/ugorji/go/codec"
)

const (
	// storeRecordEnv names the env var that, when set at openStore, records the
	// store's calls to the file it names, which must not already exist.
	storeRecordEnv = "WR_STORE_RECORD"

	// expectJobExitUpdateDone is the recorded method name of a call of the func
	// expectJobExitUpdate returned.
	expectJobExitUpdateDone = "expectJobExitUpdate.done"

	// recordedLimitFromName is how a recorded limit group stands for a valid
	// group that is not a count, such as a time-based one, whose GroupData
	// comes from its name.
	recordedLimitFromName = int64(-2)

	// recordFileMode is the permissions of a new recording file.
	recordFileMode = 0o600
)

var (
	errRecordedCallTooBig    = errors.New("recorded store call is too big for a wal frame")
	errUnknownRecordedMethod = errors.New("recording has an unknown store method")
)

// recordHandle encodes recorded arguments and, being canonical (maps sorted by
// key), the results storeDigest hashes.
var recordHandle = newRecordHandle() //nolint:gochecknoglobals // a shared, read-only codec handle

// recordedWrites are the store methods whose calls change what a store holds.
// replayRecording compares the result of a call only if no other call to one of
// these ran at the same time, since a concurrent write may legitimately change
// it.
var recordedWrites = map[string]bool{ //nolint:gochecknoglobals // a constant set
	"recoverIncompleteJobs":          true,
	"storeNewJobsGuarded":            true,
	"storeEnv":                       true,
	"storeLimitGroups":               true,
	"storeRepGroupLookups":           true,
	"updateJobRunStateDurable":       true,
	"updateJobRunStateDurableWithin": true,
	"updateJobAfterChangeDurable":    true,
	"updateJobAfterExit":             true,
	"updateJobAfterExitDurable":      true,
	"archiveCompletion":              true,
	"queueJobChangeAhead":            true,
	"deleteLiveJobs":                 true,
	"modifyLiveJobs":                 true,
	"storeLiveForRerun":              true,
	"storeRunningRerunMarks":         true,
}

var _ store = (*recordingStore)(nil)

// replayFunc replays one recorded call from its Binc arguments, returning the
// digest of its results.
type replayFunc func(rp *replayer, args []byte) ([32]byte, error)

// storeReplays replay each recorded method.
//
//nolint:gochecknoglobals // a constant table
var storeReplays = map[string]replayFunc{
	"depGroupEverSeen": replayWith(func(rp *replayer, a *recKey) [32]byte {
		return storeDigest(rp.s.depGroupEverSeen(a.Key))
	}),
	"depGroupsEverSeen": replayWith(func(rp *replayer, a *recKeys) [32]byte {
		return storeDigest(rp.s.depGroupsEverSeen(a.Keys))
	}),
	"checkIfLive": replayWith(func(rp *replayer, a *recKey) [32]byte {
		return storeDigest(rp.s.checkIfLive(a.Key))
	}),
	"recoverIncompleteJobs": replayWith(func(rp *replayer, _ *recNone) [32]byte {
		return storeDigest(rp.s.recoverIncompleteJobs())
	}),
	"resolveDependencies": replayWith(func(rp *replayer, a *recResolve) [32]byte {
		groups := replayedDepGroups{rp: rp, members: a.HasMembers, variants: a.Variants}

		return storeDigest(rp.s.resolveDependencies(rp.ctx, rp.jobList(a.Jobs), groups))
	}),
	"backup": replayWith(func(rp *replayer, _ *recNone) [32]byte { //nolint:goconst // as recorded
		return storeDigest(rp.s.backup(io.Discard))
	}),
	"upgradedOnOpen": replayWith(func(rp *replayer, _ *recNone) [32]byte {
		return storeDigest(rp.s.upgradedOnOpen())
	}),
	"storeNewJobsGuarded": replayWith(func(rp *replayer, a *recNewJobs) [32]byte {
		guard := rp.guards[a.Guard]
		if guard != nil && a.GuardKnown {
			guard.setDependents(a.GuardKeys, a.GuardMember)
		}

		return storeDigest(rp.s.storeNewJobsGuarded(rp.ctx, rp.jobList(a.Jobs), a.IgnoreAdded, guard))
	}),
	"registerRerunGuard": replayWith(func(rp *replayer, _ *recNone) [32]byte {
		guard := rp.s.registerRerunGuard()
		rp.guards[rp.call.Start] = guard

		return storeDigest(guard)
	}),
	"releaseRerunGuard": replayWith(func(rp *replayer, a *recHandle) [32]byte {
		rp.s.releaseRerunGuard(rp.guards[a.ID])
		delete(rp.guards, a.ID)

		return storeDigest()
	}),
	"storeEnv": replayWith(func(rp *replayer, a *recEnv) [32]byte {
		return storeDigest(rp.s.storeEnv(a.Env))
	}),
	"storeLimitGroups": replayWith(func(rp *replayer, a *recLimits) [32]byte {
		groups := make(map[string]*limiter.GroupData, len(a.Limits))
		for name, limit := range a.Limits {
			groups[name] = replayedLimit(name, limit)
		}

		return storeDigest(rp.s.storeLimitGroups(groups))
	}),
	"storeRepGroupLookups": replayWith(func(rp *replayer, a *recJobs) [32]byte {
		return storeDigest(rp.s.storeRepGroupLookups(rp.ctx, rp.jobList(a.Jobs)))
	}),
	"checkIfComplete": replayWith(func(rp *replayer, a *recKey) [32]byte {
		return storeDigest(rp.s.checkIfComplete(a.Key))
	}),
	"updateJobRunStateDurable": replayWith(func(rp *replayer, a *recJob) [32]byte {
		return storeDigest(rp.s.updateJobRunStateDurable(rp.job(a.Job)))
	}),
	"updateJobRunStateDurableWithin": replayWith(func(rp *replayer, a *recWithin) [32]byte {
		return storeDigest(rp.s.updateJobRunStateDurableWithin(rp.job(a.Job), a.Wait))
	}),
	"logReserveNotDurable": replayWith(func(rp *replayer, a *recKey) [32]byte {
		rp.s.logReserveNotDurable(a.Key)

		return storeDigest()
	}),
	"updateJobAfterChangeDurable": replayWith(func(rp *replayer, a *recJob) [32]byte {
		return storeDigest(rp.s.updateJobAfterChangeDurable(rp.job(a.Job)))
	}),
	"updateJobAfterExit": replayWith(func(rp *replayer, a *recExit) [32]byte {
		rp.s.updateJobAfterExit(rp.ctx, rp.job(a.Job), a.Stdo, a.Stde, a.Force)

		return storeDigest()
	}),
	"updateJobAfterExitDurable": replayWith(func(rp *replayer, a *recExit) [32]byte {
		return storeDigest(rp.s.updateJobAfterExitDurable(rp.job(a.Job), a.Stdo, a.Stde, a.Force))
	}),
	"expectJobExitUpdate": replayWith(func(rp *replayer, _ *recNone) [32]byte {
		done := rp.s.expectJobExitUpdate()
		rp.exitDones[rp.call.Start] = done

		return storeDigest(done)
	}),
	expectJobExitUpdateDone: replayWith(func(rp *replayer, a *recHandle) [32]byte {
		if done := rp.exitDones[a.ID]; done != nil {
			done()
		}

		delete(rp.exitDones, a.ID)

		return storeDigest()
	}),
	"archiveCompletion": replayWith(func(rp *replayer, a *recArchive) [32]byte {
		outcome, err := rp.s.archiveCompletion(a.Key, rp.job(a.Job))
		if outcome == archiveRemovedLive {
			rp.forget(a.Key)
		}

		return storeDigest(outcome, err)
	}),
	"prepareJobChange": replayWith(func(rp *replayer, a *recChange) [32]byte {
		job, change := rp.change(a.Job)
		ahead := rp.s.prepareJobChange(rp.ctx, job, change)

		if ahead != nil {
			rp.aheads[rp.call.Start] = ahead
		}

		return storeDigest(ahead)
	}),
	"queueJobChangeAhead": replayWith(func(rp *replayer, a *recChange) [32]byte {
		job, change := rp.change(a.Job)
		rp.s.queueJobChangeAhead(rp.ctx, job, rp.takeAhead(a.Ahead), change)

		return storeDigest()
	}),
	"moveJobChangeAhead": replayWith(func(rp *replayer, a *recMove) [32]byte {
		moved := rp.s.moveJobChangeAhead(rp.job(a.From), rp.job(a.To), rp.takeAhead(a.Ahead))

		if moved != nil {
			rp.aheads[rp.call.Start] = moved
		}

		return storeDigest(moved)
	}),
	"discardJobChangeAhead": replayWith(func(rp *replayer, a *recChange) [32]byte {
		rp.s.discardJobChangeAhead(rp.job(a.Job), rp.takeAhead(a.Ahead))

		return storeDigest()
	}),
	"deleteLiveJobs": replayWith(func(rp *replayer, a *recKeys) [32]byte {
		err := rp.s.deleteLiveJobs(rp.ctx, a.Keys)
		rp.forget(a.Keys...)

		return storeDigest(err)
	}),
	"modifyLiveJobs": replayWith(func(rp *replayer, a *recModify) [32]byte {
		return storeDigest(rp.s.modifyLiveJobs(rp.ctx, a.OldKeys, rp.jobList(a.Jobs)))
	}),
	"storeLiveForRerun": replayWith(func(rp *replayer, a *recJobs) [32]byte {
		return storeDigest(rp.s.storeLiveForRerun(rp.jobList(a.Jobs)))
	}),
	"storeRunningRerunMarks": replayWith(func(rp *replayer, a *recJobs) [32]byte {
		return storeDigest(rp.s.storeRunningRerunMarks(rp.jobList(a.Jobs)))
	}),
	"retrieveCompleteJobsByKeys": replayWith(func(rp *replayer, a *recKeys) [32]byte {
		return storeDigest(rp.s.retrieveCompleteJobsByKeys(a.Keys))
	}),
	"retrieveCompleteJobsByRepGroup": replayWith(func(rp *replayer, a *recKey) [32]byte {
		return storeDigest(rp.s.retrieveCompleteJobsByRepGroup(a.Key))
	}),
	"retrieveOldestCompleteJobsByRepGroup": replayWith(func(rp *replayer, a *recOldest) [32]byte {
		return storeDigest(rp.s.retrieveOldestCompleteJobsByRepGroup(a.RepGroup, rp.replayedJobsQuery(a)))
	}),
	"retrieveCompleteJobStatusByRepGroup": replayWith(func(rp *replayer, a *recStatus) [32]byte {
		return storeDigest(rp.s.retrieveCompleteJobStatusByRepGroup(a.RepGroup, a.Details))
	}),
	"retrieveCompleteJobsRecent": replayWith(func(rp *replayer, a *recCutoff) [32]byte {
		return storeDigest(rp.s.retrieveCompleteJobsRecent(a.Cutoff))
	}),
	"retrieveLastCompletionTimeByRepGroup": replayWith(func(rp *replayer, a *recKeys) [32]byte {
		return storeDigest(rp.s.retrieveLastCompletionTimeByRepGroup(a.Keys))
	}),
	"repGroupHasHistory": replayWith(func(rp *replayer, a *recKey) [32]byte {
		return storeDigest(rp.s.repGroupHasHistory(a.Key))
	}),
	"retrieveRepGroups": replayWith(func(rp *replayer, _ *recNone) [32]byte {
		return storeDigest(rp.s.retrieveRepGroups())
	}),
	"spendArchivedBytesByRepGroup": replayWith(func(rp *replayer, a *recSpend) [32]byte {
		var budget *archivedBytesBudget
		if a.Budget {
			budget = &archivedBytesBudget{remaining: a.Remaining}
		}

		return storeDigest(rp.s.spendArchivedBytesByRepGroup(a.RepGroup, budget) != nil)
	}),
	"retrieveJobStd": replayWith(func(rp *replayer, a *recKey) [32]byte {
		return storeDigest(rp.s.retrieveJobStd(rp.ctx, a.Key))
	}),
	"retrieveEnv": replayWith(func(rp *replayer, a *recKey) [32]byte {
		return storeDigest(rp.s.retrieveEnv(rp.ctx, a.Key))
	}),
	"retrieveLimitGroup": replayWith(func(rp *replayer, a *recKey) [32]byte {
		return storeDigest(rp.s.retrieveLimitGroup(rp.ctx, a.Key))
	}),
	"retrieveStoredLimits": replayWith(func(rp *replayer, a *recKeys) [32]byte {
		return storeDigest(rp.s.retrieveStoredLimits(a.Keys))
	}),
	"cachedReqGroupRecs": replayWith(func(rp *replayer, a *recKey) [32]byte {
		return storeDigest(rp.s.cachedReqGroupRecs(a.Key))
	}),
	"refreshReqGroupRecs": replayWith(func(rp *replayer, a *recKey) [32]byte {
		return storeDigest(rp.s.refreshReqGroupRecs(a.Key))
	}),
	"refreshReqGroupRecsLater": replayWith(func(rp *replayer, a *recKey) [32]byte {
		rp.s.refreshReqGroupRecsLater(a.Key)

		return storeDigest()
	}),
	"warmReqGroupRecs": replayWith(func(rp *replayer, a *recJobs) [32]byte {
		rp.s.warmReqGroupRecs(rp.jobList(a.Jobs))

		return storeDigest()
	}),
	"reqStatChanges": replayWith(func(rp *replayer, _ *recNone) [32]byte {
		return storeDigest(rp.s.reqStatChanges())
	}),
	"setReqStatRefreshInterval": replayWith(func(rp *replayer, a *recDuration) [32]byte {
		rp.s.setReqStatRefreshInterval(a.D)

		return storeDigest()
	}),
	"setRecRounding": replayWith(func(rp *replayer, a *recRounding) [32]byte {
		rp.s.setRecRounding(a.Sec, a.MB)

		return storeDigest()
	}),
	"setBatchTuning": replayWith(func(rp *replayer, a *recBatch) [32]byte {
		rp.s.setBatchTuning(a.Delay, a.Size)

		return storeDigest()
	}),
}

// newRecordHandle returns a canonical Binc handle.
func newRecordHandle() *codec.BincHandle {
	h := new(codec.BincHandle)
	h.Canonical = true

	return h
}

// replayedLimit is the GroupData recordedLimit recorded as limit for the named
// group.
func replayedLimit(name string, limit int64) *limiter.GroupData {
	if limit == recordedLimitFromName {
		_, gd := limiter.NameToGroupData(name)

		return gd
	}

	return limiter.NewCountGroupData(limit)
}

// recordedCall is one store call in a recording, one per wal frame, in End
// order.
type recordedCall struct {
	Start, End uint64 // global call sequence at entry and at return
	Method     string
	Args       []byte // Binc of the method's rec* argument struct
	Digest     [32]byte
	Nanos      int64
	Write      bool
}

// The rec* types are the recorded arguments of the store methods, by shape.
type (
	recNone   struct{}
	recKey    struct{ Key string }
	recKeys   struct{ Keys []string }
	recJob    struct{ Job []byte }
	recJobs   struct{ Jobs [][]byte }
	recHandle struct{ ID uint64 }
	recWithin struct {
		Job  []byte
		Wait time.Duration
	}
	recExit struct {
		Job, Stdo, Stde []byte
		Force           bool
	}
	recArchive struct {
		Key string
		Job []byte
	}
	recNewJobs struct {
		Jobs        [][]byte
		IgnoreAdded bool
		Guard       uint64
		GuardKnown  bool
		GuardKeys   []string
		GuardMember []byte
	}
	recEnv    struct{ Env []byte }
	recLimits struct{ Limits map[string]int64 }
	recChange struct {
		Job   []byte
		Ahead uint64
	}
	recMove struct {
		From, To []byte
		Ahead    uint64
	}
	recModify struct {
		OldKeys []string
		Jobs    [][]byte
	}
	recStatus struct {
		RepGroup string
		Details  bool
	}
	recCutoff struct{ Cutoff time.Time }
	recSpend  struct {
		RepGroup  string
		Remaining int
		Budget    bool
	}
	recOldest struct {
		RepGroup string
		Groups   map[string]recGroupAnswer
		Limits   map[string]int
	}
	recGroupAnswer struct {
		Group string
		OK    bool
	}
	recResolve struct {
		Jobs       [][]byte
		HasMembers map[string]bool
		Variants   map[string][]string
	}
	recDuration struct{ D time.Duration }
	recRounding struct{ Sec, MB int }
	recBatch    struct {
		Delay time.Duration
		Size  int
	}
)

// replayMismatch is a replayed call whose result differed from the recording's,
// by its End.
type replayMismatch struct {
	Seq    uint64
	Method string
}

// replayStats is what replayRecording did: calls replayed (skipped included),
// calls skipped, and the calls whose results differed from the recording's.
type replayStats struct {
	calls, skipped int
	mismatches     []replayMismatch
}

// replayRecording replays the recording at path against s, serially in the
// recorded End order, and reports a mismatch for each call whose result digest
// differs from the recorded one, unless the call overlapped a write call in
// the recording, which may legitimately have changed its answer. A torn last
// frame ends the recording without error. Handles are passed as the ones s
// returned for the calls that returned them; ctx is passed to every call that
// takes one.
func replayRecording(ctx context.Context, path string, s store) (replayStats, error) {
	return newReplayer(ctx, s).replayFile(path)
}

// writeSpans are the [Start, End] intervals of a recording's write calls,
// sorted by Start, with the greatest End of each prefix.
type writeSpans struct {
	starts  []uint64
	maxEnds []uint64
}

// recordedWriteSpans scans a recording for its write calls' intervals.
func recordedWriteSpans(r io.Reader) (writeSpans, error) {
	var spans []recordedSpan

	_, err := wal.Scan(r, func(_ wal.Ref, rec []byte) error {
		var span recordedSpan
		if err := decodeRecorded(rec, &span); err != nil {
			return err
		}

		if span.Write {
			spans = append(spans, span)
		}

		return nil
	})
	if err != nil {
		return writeSpans{}, err
	}

	slices.SortFunc(spans, func(a, b recordedSpan) int { return cmp.Compare(a.Start, b.Start) })

	w := writeSpans{starts: make([]uint64, len(spans)), maxEnds: make([]uint64, len(spans))}

	var maxEnd uint64

	for i, span := range spans {
		maxEnd = max(maxEnd, span.End)
		w.starts[i], w.maxEnds[i] = span.Start, maxEnd
	}

	return w, nil
}

// overlaps reports whether a write call other than c ran at any time during c.
// Starts are unique, so such a write either started before c and ended after
// c's Start, or started inside c.
func (w writeSpans) overlaps(c *recordedCall) bool {
	before, _ := slices.BinarySearch(w.starts, c.Start)
	if before > 0 && w.maxEnds[before-1] > c.Start {
		return true
	}

	inside, _ := slices.BinarySearch(w.starts, c.End)
	inside -= before

	if c.Write {
		inside-- // c itself
	}

	return inside > 0
}

// replayer replays one recording's calls against s, keeping the state a server
// keeps between calls: one *Job per key until it is archived or deleted, whose
// persisted fields each call sets to the recorded ones, and the handles s
// returned, by the recorded id of the call that returned them.
type replayer struct {
	ctx       context.Context //nolint:containedctx // the replay's own, passed to each call
	s         store
	jobs      map[string]*Job
	guards    map[uint64]*rerunGuard
	aheads    map[uint64]*jobChangeAhead
	exitDones map[uint64]func()
	lastEnd   uint64

	// call is the call being replayed, and unrecorded is set when it asked a
	// question the recording has no answer to, which makes it a mismatch.
	call       *recordedCall
	unrecorded bool
	err        error
}

// newReplayer returns a replayer for s.
func newReplayer(ctx context.Context, s store) *replayer {
	return &replayer{
		ctx:       ctx,
		s:         s,
		jobs:      make(map[string]*Job),
		guards:    make(map[uint64]*rerunGuard),
		aheads:    make(map[uint64]*jobChangeAhead),
		exitDones: make(map[uint64]func()),
	}
}

// replayFile replays the recording at path, as replayRecording.
func (rp *replayer) replayFile(path string) (replayStats, error) {
	var stats replayStats

	file, err := os.Open(path) //nolint:gosec // the developer names the recording to replay
	if err != nil {
		return stats, err
	}

	defer file.Close()

	writes, err := recordedWriteSpans(file)
	if err != nil {
		return stats, err
	}

	if _, err = file.Seek(0, io.SeekStart); err != nil {
		return stats, err
	}

	_, err = wal.Scan(file, func(_ wal.Ref, rec []byte) error {
		var call recordedCall
		if errd := decodeRecorded(rec, &call); errd != nil {
			return fmt.Errorf("could not decode recorded call after End %d: %w", rp.lastEnd, errd)
		}

		return rp.replay(&call, writes, &stats)
	})

	return stats, err
}

// decodeRecorded decodes encoded, made by encodeRecorded, into v.
func decodeRecorded(encoded []byte, v any) error {
	return codec.NewDecoderBytes(encoded, recordHandle).Decode(v)
}

// replay replays call, counting it in stats.
func (rp *replayer) replay(call *recordedCall, writes writeSpans, stats *replayStats) error {
	replayFn, ok := storeReplays[call.Method]
	if !ok {
		return fmt.Errorf("%w: %q", errUnknownRecordedMethod, call.Method)
	}

	rp.call, rp.unrecorded, rp.err = call, false, nil
	rp.lastEnd = call.End
	stats.calls++

	digest, err := replayFn(rp, call.Args)
	if err == nil {
		err = rp.err
	}

	if err != nil {
		return fmt.Errorf("could not replay %s (End %d): %w", call.Method, call.End, err)
	}

	if (digest != call.Digest || rp.unrecorded) && !writes.overlaps(call) {
		stats.mismatches = append(stats.mismatches, replayMismatch{Seq: call.End, Method: call.Method})
	}

	return nil
}

// decodedJob decodes a recorded job afresh; nil for nil.
func (rp *replayer) decodedJob(encoded []byte) *Job {
	if encoded == nil {
		return nil
	}

	job := &Job{}
	if err := decodeRecorded(encoded, job); err != nil {
		rp.err = err
	}

	return job
}

// forget drops the replay's jobs for keys, which have left the live queue, so
// that a long recording's jobs are not all held at once. A later call naming
// one of them gets it decoded afresh.
func (rp *replayer) forget(keys ...string) {
	for _, key := range keys {
		delete(rp.jobs, key)
	}
}

// sharedJob returns the replay's job for decoded's key, decoded itself if it
// has none yet. If set, the shared job's persisted fields are set to decoded's.
func (rp *replayer) sharedJob(decoded *Job, set bool) *Job {
	if decoded == nil {
		return nil
	}

	key := decoded.Key()

	job, ok := rp.jobs[key]
	if !ok {
		rp.jobs[key] = decoded

		return decoded
	}

	if set {
		job.Lock()
		setPersistedFields(job, decoded)
		job.Unlock()
	}

	return job
}

// setPersistedFields sets dst's exported fields, the ones encodeJob persists,
// to src's. The caller holds dst's write lock.
func setPersistedFields(dst, src *Job) {
	d, s := reflect.ValueOf(dst).Elem(), reflect.ValueOf(src).Elem()
	t := d.Type()

	for i := range t.NumField() {
		if field := t.Field(i); field.IsExported() && !field.Anonymous {
			d.Field(i).Set(s.Field(i))
		}
	}
}

// job is the replay's job for a recorded job, set as recorded.
func (rp *replayer) job(encoded []byte) *Job {
	return rp.sharedJob(rp.decodedJob(encoded), true)
}

// jobList is job of each of the recorded jobs.
func (rp *replayer) jobList(encoded [][]byte) []*Job {
	jobs := make([]*Job, len(encoded))
	for i, e := range encoded {
		jobs[i] = rp.job(e)
	}

	return jobs
}

// change returns the replay's job for a job recorded as a change func left
// it, unchanged, and a change func that sets its persisted fields to the
// recorded ones.
func (rp *replayer) change(encoded []byte) (*Job, func(*Job)) {
	changed := rp.decodedJob(encoded)

	return rp.sharedJob(changed, false), func(job *Job) {
		if job != changed {
			setPersistedFields(job, changed)
		}
	}
}

// replayedJobsQuery is a completeJobsQuery answering from a recording, noting a
// question it has no answer to.
func (rp *replayer) replayedJobsQuery(a *recOldest) completeJobsQuery {
	return completeJobsQuery{
		group: func(facets *archivedJobFacets) (string, bool) {
			answer, ok := a.Groups[facetsKey(facets)]
			if !ok {
				rp.unrecorded = true
			}

			return answer.Group, answer.OK
		},
		limit: func(group string) int {
			limit, ok := a.Limits[group]
			if !ok {
				rp.unrecorded = true
			}

			return limit
		},
	}
}

// facetsKey identifies facets by value, times as UTC instants, so that the
// same archived job's facets give the same key from any store.
func facetsKey(facets *archivedJobFacets) string {
	return fmt.Sprintf("%s|%s|%s|%d|%q|%t", facets.StartTime.UTC().Format(time.RFC3339Nano),
		facets.EndTime.UTC().Format(time.RFC3339Nano), facets.State, facets.Exitcode, facets.FailReason, facets.Lost)
}

// takeAhead returns the replay's ahead for a recorded id, forgetting it, since
// the call it is passed to uses it up; nil for 0 or an unknown id.
func (rp *replayer) takeAhead(id uint64) *jobChangeAhead {
	ahead := rp.aheads[id]
	delete(rp.aheads, id)

	return ahead
}

// replayWith returns a replayFunc that decodes the arguments into an A and
// replays with fn.
func replayWith[A any](fn func(rp *replayer, a *A) [32]byte) replayFunc {
	return func(rp *replayer, args []byte) ([32]byte, error) {
		a := new(A)
		if err := decodeRecorded(args, a); err != nil {
			return [32]byte{}, err
		}

		return fn(rp, a), nil
	}
}

// pendingCall is a call recordingStore has seen enter.
type pendingCall struct {
	start  uint64
	began  time.Time
	method string
}

// recordingDepGroups is a depGroupState that notes each question asked of it
// and the first answer it gave.
type recordingDepGroups struct {
	groups depGroupState

	mu       sync.Mutex
	members  map[string]bool
	variants map[string][]string
}

func (g *recordingDepGroups) hasMembers(depGroup string) bool {
	has := g.groups.hasMembers(depGroup)

	g.mu.Lock()
	defer g.mu.Unlock()

	if _, asked := g.members[depGroup]; !asked {
		g.members[depGroup] = has
	}

	return has
}

func (g *recordingDepGroups) liveCommandVariants(commandKey string) []string {
	variants := g.groups.liveCommandVariants(commandKey)

	g.mu.Lock()
	defer g.mu.Unlock()

	if _, asked := g.variants[commandKey]; !asked {
		g.variants[commandKey] = slices.Clone(variants)
	}

	return variants
}

// answers returns the answers noted so far.
func (g *recordingDepGroups) answers() (map[string]bool, map[string][]string) {
	g.mu.Lock()
	defer g.mu.Unlock()

	return g.members, g.variants
}

// recordingJobsQuery wraps a completeJobsQuery, noting each question asked of
// its funcs and the first answer given.
type recordingJobsQuery struct {
	query completeJobsQuery

	mu     sync.Mutex
	groups map[string]recGroupAnswer
	limits map[string]int
}

// completeJobsQuery returns the noting query to give the store.
func (q *recordingJobsQuery) completeJobsQuery() completeJobsQuery {
	return completeJobsQuery{group: q.group, limit: q.limit}
}

func (q *recordingJobsQuery) group(facets *archivedJobFacets) (string, bool) {
	group, ok := q.query.group(facets)
	key := facetsKey(facets)

	q.mu.Lock()
	defer q.mu.Unlock()

	if _, asked := q.groups[key]; !asked {
		q.groups[key] = recGroupAnswer{Group: group, OK: ok}
	}

	return group, ok
}

func (q *recordingJobsQuery) limit(group string) int {
	limit := q.query.limit(group)

	q.mu.Lock()
	defer q.mu.Unlock()

	if _, asked := q.limits[group]; !asked {
		q.limits[group] = limit
	}

	return limit
}

// answers returns the answers noted so far.
func (q *recordingJobsQuery) answers() (map[string]recGroupAnswer, map[string]int) {
	q.mu.Lock()
	defer q.mu.Unlock()

	return q.groups, q.limits
}

// recordingStore is a store that records each call made to the store it wraps
// (spec A5), written at return as one wal frame holding a Binc recordedCall.
// ctx is never recorded. A handle a call returns (a *rerunGuard, a
// *jobChangeAhead, expectJobExitUpdate's func) is recorded by the Start of that
// call, so that a replay can pass the handle its own store returned.
//
// close is not recorded: it ends the recording, and the store a recording is
// replayed against belongs to the replay's caller.
type recordingStore struct {
	s   store
	ctx context.Context //nolint:containedctx // only for logging a recording failure
	seq atomic.Uint64

	mu     sync.Mutex
	file   *os.File
	frame  []byte
	err    error
	closed bool

	handlesMu sync.Mutex
	guards    map[*rerunGuard]uint64
	aheads    map[*jobChangeAhead]uint64
}

// newRecordingStore returns a recordingStore wrapping s that records to a new
// file at path.
func newRecordingStore(ctx context.Context, s store, path string) (*recordingStore, error) {
	//nolint:gosec // the developer names the recording file
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, recordFileMode)
	if err != nil {
		return nil, fmt.Errorf("could not create the store recording: %w", err)
	}

	return &recordingStore{
		s:      s,
		ctx:    ctx,
		file:   file,
		guards: make(map[*rerunGuard]uint64),
		aheads: make(map[*jobChangeAhead]uint64),
	}, nil
}

// begin notes a call entering.
func (r *recordingStore) begin(method string) pendingCall {
	return pendingCall{start: r.seq.Add(1), began: time.Now(), method: method}
}

// end records call c, which has returned, with its arguments and results.
func (r *recordingStore) end(c pendingCall, args any, results ...any) {
	call := recordedCall{
		Start:  c.start,
		Method: c.method,
		Args:   encodeRecorded(args),
		Digest: storeDigest(results...),
		Nanos:  time.Since(c.began).Nanoseconds(),
		Write:  recordedWrites[c.method],
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	// End is taken with mu held, so the recording's frames are in End order.
	call.End = r.seq.Add(1)

	if r.closed || r.err != nil {
		return
	}

	encoded := encodeRecorded(call)
	if len(encoded) > wal.MaxFrameLen {
		r.failLocked(fmt.Errorf("%w: %s of %d bytes", errRecordedCallTooBig, call.Method, len(encoded)))

		return
	}

	r.frame = wal.AppendFrame(r.frame[:0], encoded)

	if _, err := r.file.Write(r.frame); err != nil {
		r.failLocked(err)
	}
}

// encodeRecorded Binc-encodes v with recordHandle. Everything recorded is plain
// data that encodes without error, so a failure is a bug in this file.
func encodeRecorded(v any) []byte {
	var encoded []byte

	if err := codec.NewEncoderBytes(&encoded, recordHandle).Encode(v); err != nil {
		panic(fmt.Sprintf("store recording could not encode %T: %v", v, err))
	}

	return encoded
}

// storeDigest hashes a call's results in a canonical form, so that two stores
// giving the same answer give the same digest: jobs are sorted by key and
// reduced to the fields encodeJob persists, maps are sorted, and lists whose
// order a store does not promise are sorted.
func storeDigest(results ...any) [32]byte {
	canonical := make([]any, len(results))
	for i, result := range results {
		canonical[i] = canonicalResult(result)
	}

	return sha256.Sum256(encodeRecorded(canonical))
}

// failLocked stops the recording, which is incomplete from here on, logging why;
// close returns the error. mu must be held.
func (r *recordingStore) failLocked(err error) {
	r.err = err
	clog.Error(r.ctx, "store recording failed; the recording is incomplete", "err", err)
}

// close closes the wrapped store, then the recording.
func (r *recordingStore) close(ctx context.Context) error {
	err := r.s.close(ctx)

	r.mu.Lock()
	defer r.mu.Unlock()

	if r.closed {
		return err
	}

	r.closed = true

	return errors.Join(err, r.err, r.file.Close())
}

func (r *recordingStore) depGroupEverSeen(depGroup string) (bool, error) {
	c := r.begin("depGroupEverSeen")
	seen, err := r.s.depGroupEverSeen(depGroup)
	r.end(c, recKey{Key: depGroup}, seen, err)

	return seen, err
}

func (r *recordingStore) depGroupsEverSeen(depGroups []string) (map[string]bool, error) {
	c := r.begin("depGroupsEverSeen")
	seen, err := r.s.depGroupsEverSeen(depGroups)
	r.end(c, recKeys{Keys: depGroups}, seen, err)

	return seen, err
}

func (r *recordingStore) checkIfLive(jobKey string) (bool, error) {
	c := r.begin("checkIfLive")
	live, err := r.s.checkIfLive(jobKey)
	r.end(c, recKey{Key: jobKey}, live, err)

	return live, err
}

func (r *recordingStore) recoverIncompleteJobs() ([]*Job, runStateRecovery, error) {
	c := r.begin("recoverIncompleteJobs")
	jobs, rsr, err := r.s.recoverIncompleteJobs()
	r.end(c, recNone{}, jobs, rsr, err)

	return jobs, rsr, err
}

// resolveDependencies records each question the store asks groups, and its
// answer, so that a replay can give the same answers.
func (r *recordingStore) resolveDependencies(ctx context.Context, jobs []*Job,
	groups depGroupState) ([]resolvedJob, error) {
	c := r.begin("resolveDependencies")
	args := recResolve{Jobs: recordJobs(jobs)}
	asked := &recordingDepGroups{groups: groups, members: make(map[string]bool),
		variants: make(map[string][]string)}

	resolved, err := r.s.resolveDependencies(ctx, jobs, asked)

	args.HasMembers, args.Variants = asked.answers()
	r.end(c, args, resolved, err)

	return resolved, err
}

// recordJobs is recordJob of each of jobs.
func recordJobs(jobs []*Job) [][]byte {
	encoded := make([][]byte, len(jobs))
	for i, job := range jobs {
		encoded[i] = recordJob(job)
	}

	return encoded
}

func (r *recordingStore) backup(w io.Writer) error {
	c := r.begin("backup")
	err := r.s.backup(w)
	r.end(c, recNone{}, err)

	return err
}

func (r *recordingStore) upgradedOnOpen() bool {
	c := r.begin("upgradedOnOpen")
	upgraded := r.s.upgradedOnOpen()
	r.end(c, recNone{}, upgraded)

	return upgraded
}

// storeNewJobsGuarded records guard by id, with the dependents set on it at
// entry, which a replay sets on its own guard.
func (r *recordingStore) storeNewJobsGuarded(ctx context.Context, jobs []*Job, ignoreAdded bool,
	guard *rerunGuard) (storedNewJobs, error) {
	c := r.begin("storeNewJobsGuarded")
	args := recNewJobs{Jobs: recordJobs(jobs), IgnoreAdded: ignoreAdded}

	if guard != nil {
		args.Guard = handleID(r, r.guards, guard)
		args.GuardKeys, args.GuardMember, args.GuardKnown = guard.dependentsSet()
	}

	stored, err := r.s.storeNewJobsGuarded(ctx, jobs, ignoreAdded, guard)
	r.end(c, args, stored, err)

	return stored, err
}

// handleID returns the recorded id of the handle h, 0 if it has none.
func handleID[H comparable](r *recordingStore, ids map[H]uint64, h H) uint64 {
	r.handlesMu.Lock()
	defer r.handlesMu.Unlock()

	return ids[h]
}

func (r *recordingStore) registerRerunGuard() *rerunGuard {
	c := r.begin("registerRerunGuard")
	guard := r.s.registerRerunGuard()
	setHandleID(r, r.guards, guard, c.start)
	r.end(c, recNone{}, guard)

	return guard
}

func (r *recordingStore) releaseRerunGuard(g *rerunGuard) {
	c := r.begin("releaseRerunGuard")
	id := takeHandleID(r, r.guards, g)
	r.s.releaseRerunGuard(g)
	r.end(c, recHandle{ID: id})
}

// takeHandleID is handleID for a handle the call uses up.
func takeHandleID[H comparable](r *recordingStore, ids map[H]uint64, h H) uint64 {
	r.handlesMu.Lock()
	defer r.handlesMu.Unlock()

	id := ids[h]
	delete(ids, h)

	return id
}

func (r *recordingStore) storeEnv(env []byte) (string, error) {
	c := r.begin("storeEnv")
	key, err := r.s.storeEnv(env)
	r.end(c, recEnv{Env: env}, key, err)

	return key, err
}

func (r *recordingStore) storeLimitGroups(groups map[string]*limiter.GroupData) ([]string, []string, error) {
	c := r.begin("storeLimitGroups")
	args := recLimits{Limits: make(map[string]int64, len(groups))}

	for name, gd := range groups {
		args.Limits[name] = recordedLimit(gd)
	}

	changed, removed, err := r.s.storeLimitGroups(groups)
	r.end(c, args, changed, removed, err)

	return changed, removed, err
}

// recordedLimit is how storeLimitGroups records gd: a count group's limit, -1
// for the invalid GroupData that forgets a group, or recordedLimitFromName.
func recordedLimit(gd *limiter.GroupData) int64 {
	switch {
	case gd.IsCount():
		return gd.Limit()
	case gd.IsValid():
		return recordedLimitFromName
	default:
		return -1
	}
}

func (r *recordingStore) storeRepGroupLookups(ctx context.Context, jobs []*Job) error {
	c := r.begin("storeRepGroupLookups")
	args := recJobs{Jobs: recordJobs(jobs)}
	err := r.s.storeRepGroupLookups(ctx, jobs)
	r.end(c, args, err)

	return err
}

func (r *recordingStore) checkIfComplete(key string) (bool, error) {
	c := r.begin("checkIfComplete")
	complete, err := r.s.checkIfComplete(key)
	r.end(c, recKey{Key: key}, complete, err)

	return complete, err
}

func (r *recordingStore) updateJobRunStateDurable(job *Job) error {
	c := r.begin("updateJobRunStateDurable")
	args := recJob{Job: recordJob(job)}
	err := r.s.updateJobRunStateDurable(job)
	r.end(c, args, err)

	return err
}

func (r *recordingStore) updateJobRunStateDurableWithin(job *Job, wait time.Duration) error {
	c := r.begin("updateJobRunStateDurableWithin")
	args := recWithin{Job: recordJob(job), Wait: wait}
	err := r.s.updateJobRunStateDurableWithin(job, wait)
	r.end(c, args, err)

	return err
}

func (r *recordingStore) logReserveNotDurable(key string) {
	c := r.begin("logReserveNotDurable")
	r.s.logReserveNotDurable(key)
	r.end(c, recKey{Key: key})
}

func (r *recordingStore) updateJobAfterChangeDurable(job *Job) error {
	c := r.begin("updateJobAfterChangeDurable")
	args := recJob{Job: recordJob(job)}
	err := r.s.updateJobAfterChangeDurable(job)
	r.end(c, args, err)

	return err
}

func (r *recordingStore) updateJobAfterExit(ctx context.Context, job *Job, stdo, stde []byte, forceStorage bool) {
	c := r.begin("updateJobAfterExit")
	args := recExit{Job: recordJob(job), Stdo: stdo, Stde: stde, Force: forceStorage}
	r.s.updateJobAfterExit(ctx, job, stdo, stde, forceStorage)
	r.end(c, args)
}

func (r *recordingStore) updateJobAfterExitDurable(job *Job, stdo, stde []byte, forceStorage bool) error {
	c := r.begin("updateJobAfterExitDurable")
	args := recExit{Job: recordJob(job), Stdo: stdo, Stde: stde, Force: forceStorage}
	err := r.s.updateJobAfterExitDurable(job, stdo, stde, forceStorage)
	r.end(c, args, err)

	return err
}

// expectJobExitUpdate returns a func that, when called, calls the wrapped
// store's and records an expectJobExitUpdateDone call with this call's id.
func (r *recordingStore) expectJobExitUpdate() func() {
	c := r.begin("expectJobExitUpdate")
	done := r.s.expectJobExitUpdate()
	r.end(c, recNone{}, done)

	if done == nil {
		return nil
	}

	return func() {
		cd := r.begin(expectJobExitUpdateDone)

		done()
		r.end(cd, recHandle{ID: c.start})
	}
}

func (r *recordingStore) archiveCompletion(key string, job *Job) (archiveOutcome, error) {
	c := r.begin("archiveCompletion")
	args := recArchive{Key: key, Job: recordJob(job)}
	outcome, err := r.s.archiveCompletion(key, job)
	r.end(c, args, outcome, err)

	return outcome, err
}

// prepareJobChange records job as change left it, encoded by the closure the
// store runs with the job's lock held; job as it was at entry if the store
// never ran it.
func (r *recordingStore) prepareJobChange(ctx context.Context, job *Job, change func(*Job)) *jobChangeAhead {
	c := r.begin("prepareJobChange")
	args := recChange{Job: recordJob(job)}

	ahead := r.s.prepareJobChange(ctx, job, recordingChange(change, &args.Job))
	setHandleID(r, r.aheads, ahead, c.start)
	r.end(c, args, ahead)

	return ahead
}

// recordJob returns job's persisted fields encoded, as encodeJob would, under
// its read lock; nil for a nil job.
func recordJob(job *Job) []byte {
	if job == nil {
		return nil
	}

	job.RLock()
	defer job.RUnlock()

	return recordJobLocked(job)
}

// recordingChange returns a change func that calls change, then encodes the
// job it was given into *recorded. The store runs it with that job's lock held.
func recordingChange(change func(*Job), recorded *[]byte) func(*Job) {
	return func(job *Job) {
		change(job)
		*recorded = recordJobLocked(job)
	}
}

// setHandleID records h as returned by the call with the given Start, unless h
// is the zero handle (nil). It returns the id to record, 0 for nil.
func setHandleID[H comparable](r *recordingStore, ids map[H]uint64, h H, start uint64) uint64 {
	var zero H
	if h == zero {
		return 0
	}

	r.handlesMu.Lock()
	defer r.handlesMu.Unlock()

	ids[h] = start

	return start
}

func (r *recordingStore) queueJobChangeAhead(ctx context.Context, job *Job, ahead *jobChangeAhead,
	change func(*Job)) {
	c := r.begin("queueJobChangeAhead")
	args := recChange{Job: recordJob(job), Ahead: takeHandleID(r, r.aheads, ahead)}
	r.s.queueJobChangeAhead(ctx, job, ahead, recordingChange(change, &args.Job))
	r.end(c, args)
}

func (r *recordingStore) moveJobChangeAhead(from, to *Job, ahead *jobChangeAhead) *jobChangeAhead {
	c := r.begin("moveJobChangeAhead")
	args := recMove{From: recordJob(from), To: recordJob(to), Ahead: takeHandleID(r, r.aheads, ahead)}
	moved := r.s.moveJobChangeAhead(from, to, ahead)
	setHandleID(r, r.aheads, moved, c.start)
	r.end(c, args, moved)

	return moved
}

func (r *recordingStore) discardJobChangeAhead(job *Job, ahead *jobChangeAhead) {
	c := r.begin("discardJobChangeAhead")
	args := recChange{Job: recordJob(job), Ahead: takeHandleID(r, r.aheads, ahead)}
	r.s.discardJobChangeAhead(job, ahead)
	r.end(c, args)
}

func (r *recordingStore) deleteLiveJobs(ctx context.Context, keys []string) error {
	c := r.begin("deleteLiveJobs")
	err := r.s.deleteLiveJobs(ctx, keys)
	r.end(c, recKeys{Keys: keys}, err)

	return err
}

func (r *recordingStore) modifyLiveJobs(ctx context.Context, oldKeys []string, jobs []*Job) error {
	c := r.begin("modifyLiveJobs")
	args := recModify{OldKeys: oldKeys, Jobs: recordJobs(jobs)}
	err := r.s.modifyLiveJobs(ctx, oldKeys, jobs)
	r.end(c, args, err)

	return err
}

func (r *recordingStore) storeLiveForRerun(jobs []*Job) error {
	c := r.begin("storeLiveForRerun")
	args := recJobs{Jobs: recordJobs(jobs)}
	err := r.s.storeLiveForRerun(jobs)
	r.end(c, args, err)

	return err
}

func (r *recordingStore) storeRunningRerunMarks(jobs []*Job) error {
	c := r.begin("storeRunningRerunMarks")
	args := recJobs{Jobs: recordJobs(jobs)}
	err := r.s.storeRunningRerunMarks(jobs)
	r.end(c, args, err)

	return err
}

func (r *recordingStore) retrieveCompleteJobsByKeys(keys []string) ([]*Job, error) {
	c := r.begin("retrieveCompleteJobsByKeys")
	jobs, err := r.s.retrieveCompleteJobsByKeys(keys)
	r.end(c, recKeys{Keys: keys}, jobs, err)

	return jobs, err
}

func (r *recordingStore) retrieveCompleteJobsByRepGroup(repgroup string) ([]*Job, error) {
	c := r.begin("retrieveCompleteJobsByRepGroup")
	jobs, err := r.s.retrieveCompleteJobsByRepGroup(repgroup)
	r.end(c, recKey{Key: repgroup}, jobs, err)

	return jobs, err
}

// retrieveOldestCompleteJobsByRepGroup records the answer to each question the
// store asks query's funcs, so that a replay can give the same answers.
func (r *recordingStore) retrieveOldestCompleteJobsByRepGroup(repgroup string,
	query completeJobsQuery) (*completeJobsPage, error) {
	c := r.begin("retrieveOldestCompleteJobsByRepGroup")
	asked := &recordingJobsQuery{query: query, groups: make(map[string]recGroupAnswer),
		limits: make(map[string]int)}

	page, err := r.s.retrieveOldestCompleteJobsByRepGroup(repgroup, asked.completeJobsQuery())

	groups, limits := asked.answers()
	r.end(c, recOldest{RepGroup: repgroup, Groups: groups, Limits: limits}, page, err)

	return page, err
}

func (r *recordingStore) retrieveCompleteJobStatusByRepGroup(repgroup string,
	includeDetails bool) (*RepGroupStatus, error) {
	c := r.begin("retrieveCompleteJobStatusByRepGroup")
	status, err := r.s.retrieveCompleteJobStatusByRepGroup(repgroup, includeDetails)
	r.end(c, recStatus{RepGroup: repgroup, Details: includeDetails}, status, err)

	return status, err
}

func (r *recordingStore) retrieveCompleteJobsRecent(cutoff time.Time) ([]*Job, error) {
	c := r.begin("retrieveCompleteJobsRecent")
	jobs, err := r.s.retrieveCompleteJobsRecent(cutoff)
	r.end(c, recCutoff{Cutoff: cutoff}, jobs, err)

	return jobs, err
}

func (r *recordingStore) retrieveLastCompletionTimeByRepGroup(repGroups []string) (map[string]time.Time, error) {
	c := r.begin("retrieveLastCompletionTimeByRepGroup")
	times, err := r.s.retrieveLastCompletionTimeByRepGroup(repGroups)
	r.end(c, recKeys{Keys: repGroups}, times, err)

	return times, err
}

func (r *recordingStore) repGroupHasHistory(repGroup string) bool {
	c := r.begin("repGroupHasHistory")
	has := r.s.repGroupHasHistory(repGroup)
	r.end(c, recKey{Key: repGroup}, has)

	return has
}

func (r *recordingStore) retrieveRepGroups() ([]string, error) {
	c := r.begin("retrieveRepGroups")
	rgs, err := r.s.retrieveRepGroups()
	r.end(c, recNone{}, rgs, err)

	return rgs, err
}

// spendArchivedBytesByRepGroup records the budget's remaining bytes at entry.
// Its digest covers only whether it returned an error, since stores price
// records differently.
func (r *recordingStore) spendArchivedBytesByRepGroup(repgroup string, budget *archivedBytesBudget) error {
	c := r.begin("spendArchivedBytesByRepGroup")
	args := recSpend{RepGroup: repgroup, Budget: budget != nil}

	if budget != nil {
		args.Remaining = budget.remaining
	}

	err := r.s.spendArchivedBytesByRepGroup(repgroup, budget)
	r.end(c, args, err != nil)

	return err
}

func (r *recordingStore) retrieveJobStd(ctx context.Context, jobkey string) ([]byte, []byte) {
	c := r.begin("retrieveJobStd")
	stdo, stde := r.s.retrieveJobStd(ctx, jobkey)
	r.end(c, recKey{Key: jobkey}, stdo, stde)

	return stdo, stde
}

func (r *recordingStore) retrieveEnv(ctx context.Context, envkey string) []byte {
	c := r.begin("retrieveEnv")
	env := r.s.retrieveEnv(ctx, envkey)
	r.end(c, recKey{Key: envkey}, env)

	return env
}

func (r *recordingStore) retrieveLimitGroup(ctx context.Context, group string) *limiter.GroupData {
	c := r.begin("retrieveLimitGroup")
	gd := r.s.retrieveLimitGroup(ctx, group)
	r.end(c, recKey{Key: group}, gd)

	return gd
}

func (r *recordingStore) retrieveStoredLimits(groups []string) ([]int64, error) {
	c := r.begin("retrieveStoredLimits")
	limits, err := r.s.retrieveStoredLimits(groups)
	r.end(c, recKeys{Keys: groups}, limits, err)

	return limits, err
}

func (r *recordingStore) cachedReqGroupRecs(reqGroup string) (*scheduler.Requirements, bool) {
	c := r.begin("cachedReqGroupRecs")
	req, ok := r.s.cachedReqGroupRecs(reqGroup)
	r.end(c, recKey{Key: reqGroup}, req, ok)

	return req, ok
}

func (r *recordingStore) refreshReqGroupRecs(reqGroup string) *scheduler.Requirements {
	c := r.begin("refreshReqGroupRecs")
	req := r.s.refreshReqGroupRecs(reqGroup)
	r.end(c, recKey{Key: reqGroup}, req)

	return req
}

func (r *recordingStore) refreshReqGroupRecsLater(reqGroup string) {
	c := r.begin("refreshReqGroupRecsLater")
	r.s.refreshReqGroupRecsLater(reqGroup)
	r.end(c, recKey{Key: reqGroup})
}

func (r *recordingStore) warmReqGroupRecs(jobs []*Job) {
	c := r.begin("warmReqGroupRecs")
	args := recJobs{Jobs: recordJobs(jobs)}
	r.s.warmReqGroupRecs(jobs)
	r.end(c, args)
}

func (r *recordingStore) reqStatChanges() uint32 {
	c := r.begin("reqStatChanges")
	changes := r.s.reqStatChanges()
	r.end(c, recNone{}, changes)

	return changes
}

func (r *recordingStore) setReqStatRefreshInterval(d time.Duration) {
	c := r.begin("setReqStatRefreshInterval")
	r.s.setReqStatRefreshInterval(d)
	r.end(c, recDuration{D: d})
}

func (r *recordingStore) setRecRounding(secRound, mbRound int) {
	c := r.begin("setRecRounding")
	r.s.setRecRounding(secRound, mbRound)
	r.end(c, recRounding{Sec: secRound, MB: mbRound})
}

func (r *recordingStore) setBatchTuning(delay time.Duration, size int) {
	c := r.begin("setBatchTuning")
	r.s.setBatchTuning(delay, size)
	r.end(c, recBatch{Delay: delay, Size: size})
}

// recordedSpan is the part of a recordedCall that says when it ran.
type recordedSpan struct {
	Start, End uint64
	Write      bool
}

// replayedDepGroups is a depGroupState answering from a recording, noting a
// question it has no answer to.
type replayedDepGroups struct {
	rp       *replayer
	members  map[string]bool
	variants map[string][]string
}

func (g replayedDepGroups) hasMembers(depGroup string) bool {
	has, ok := g.members[depGroup]
	if !ok {
		g.rp.unrecorded = true
	}

	return has
}

func (g replayedDepGroups) liveCommandVariants(commandKey string) []string {
	variants, ok := g.variants[commandKey]
	if !ok {
		g.rp.unrecorded = true
	}

	return variants
}

// jobKey is job's key, "" for nil.
func jobKey(job *Job) string {
	if job == nil {
		return ""
	}

	return job.Key()
}

// canonicalStoreResult is canonicalResult of the store's own result types.
func canonicalStoreResult(result any) any {
	switch r := result.(type) {
	case []resolvedJob:
		return canonicalResolved(r)
	case storedNewJobs:
		return []any{canonicalJobs(r.jobsToQueue), canonicalJobs(r.dependents.jobs), r.dependents.putBack,
			r.alreadyAdded}
	case *completeJobsPage:
		return canonicalPage(r)
	case *RepGroupStatus:
		return canonicalRepGroupStatus(r)
	case runStateRecovery:
		return []any{r.applied, r.dropped, sortedStrings(r.undecodable), canonicalResult(r.dropErr)}
	case *limiter.GroupData:
		return []any{r.IsValid(), r.IsCount(), r.LimitForDisplay()}
	default:
		return result
	}
}

// canonicalResolved is resolved sorted by job key, each with its dependency
// keys sorted.
func canonicalResolved(resolved []resolvedJob) []any {
	sorted := slices.Clone(resolved)
	sort.SliceStable(sorted, func(i, j int) bool { return jobKey(sorted[i].job) < jobKey(sorted[j].job) })

	canonical := make([]any, len(sorted))
	for i, r := range sorted {
		canonical[i] = []any{recordJob(r.job), sortedStrings(r.deps)}
	}

	return canonical
}

// canonicalJobs returns the persisted encodings of jobs, sorted by key.
func canonicalJobs(jobs []*Job) [][]byte {
	sorted := slices.Clone(jobs)
	sort.SliceStable(sorted, func(i, j int) bool { return jobKey(sorted[i]) < jobKey(sorted[j]) })

	encoded := make([][]byte, len(sorted))
	for i, job := range sorted {
		encoded[i] = recordJob(job)
	}

	return encoded
}

// canonicalPage is page with its jobs canonical.
func canonicalPage(page *completeJobsPage) any {
	if page == nil {
		return nil
	}

	return []any{canonicalJobs(page.jobs), page.fetched, page.counted}
}

// canonicalRepGroupStatus is status with each buried group's keys sorted.
func canonicalRepGroupStatus(status *RepGroupStatus) any {
	if status == nil {
		return nil
	}

	canonical := *status
	canonical.Buried = make(map[string][]string, len(status.Buried))

	for group, keys := range status.Buried {
		canonical.Buried[group] = sortedStrings(keys)
	}

	return canonical
}

// sortedStrings returns a sorted copy of s.
func sortedStrings(s []string) []string {
	sorted := slices.Clone(s)
	slices.Sort(sorted)

	return sorted
}

// canonicalResult is storeDigest's canonical form of one result.
func canonicalResult(result any) any {
	switch r := result.(type) {
	case error:
		return r.Error()
	case *Job:
		return recordJob(r)
	case []*Job:
		return canonicalJobs(r)
	case []string:
		return sortedStrings(r)
	case *rerunGuard, *jobChangeAhead, func():
		return !reflect.ValueOf(r).IsNil()
	default:
		return canonicalStoreResult(result)
	}
}

// recordJobLocked is recordJob for a caller that holds job's lock.
func recordJobLocked(job *Job) []byte {
	return encodeRecorded(job)
}
