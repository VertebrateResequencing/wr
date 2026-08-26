/*******************************************************************************
 * Copyright (c) 2016-2021, 2024-2026 Genome Research Ltd.
 *
 * Author: Sendu Bala <sb10@sanger.ac.uk>
 * Author: Ashwini Chhipa <ac55@sanger.ac.uk>
 * Author: Michael Woolnough <mw31@sanger.ac.uk>
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

// This file contains functions for interacting with our database, which is
// boltdb, a simple key/val store with transactions and hot backup ability.
// We don't use a generic ORM for boltdb like Storm, because we can do custom
// queries that are multiple times faster than what Storm can do.

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/VertebrateResequencing/muxfys/v5"
	"github.com/VertebrateResequencing/wr/clog"
	"github.com/VertebrateResequencing/wr/internal"
	"github.com/VertebrateResequencing/wr/limiter"
	lru "github.com/hashicorp/golang-lru/arc/v2"
	"github.com/sb10/waitgroup"
	"github.com/ugorji/go/codec"
	bolt "go.etcd.io/bbolt"
	berrors "go.etcd.io/bbolt/errors"
)

const (
	dbDelimiter                   = "_::_"
	jobStatWindowPercent          = float32(5)
	dbFilePermission              = 0o600
	rgEndTimeBytes                = 8
	endTimeBytes                  = 8
	envCacheSize                  = 12
	minimumTimeBetweenBackups     = 30 * time.Second
	dbRunningTransactionsWaitTime = 1 * time.Minute

	// backupDirtyPollInterval is how often the backup ticker checks the
	// (lock-free) backupDirty flag. This is only the CHECK cadence: actual
	// backups stay spaced out by backupWait (>= minimumTimeBetweenBackups) via
	// waitBeforeBackup. A short cadence preserves the pre-fix behaviour that the
	// first backup after activity is prompt (only subsequent ones are spaced),
	// which several backup regression tests rely on.
	backupDirtyPollInterval = 1 * time.Second

	// offlineDBOpenTimeout bounds how long the offline subcommand (CompactDBFile)
	// waits for the BoltDB file lock before erroring. The up-check in cmd guards
	// against a running manager,
	// but if that check is fooled (e.g. a missing token file) a manager may still
	// hold the lock; a bounded timeout makes the subcommand fail cleanly instead
	// of blocking forever. The manager's own initDB opens are bounded too, by
	// managerDBOpenTimeout.
	offlineDBOpenTimeout = 10 * time.Second

	// s3ProfilePathParts is the number of parts in a "profile@path" S3 spec.
	s3ProfilePathParts = 2
)

//nolint:gochecknoglobals // bucket names are shared BoltDB keys.
var (
	bucketJobsLive         = []byte("jobslive")
	bucketJobsComplete     = []byte("jobscomplete")
	bucketRTK              = []byte("repgroupToKey")
	bucketRGs              = []byte("repgroups")
	bucketLGs              = []byte("limitgroups")
	bucketDTK              = []byte("depgroupToKey")
	bucketDepGroups        = []byte("depgroups")
	bucketRDTK             = []byte("reverseDepgroupToKey")
	bucketJobLookupEntries = []byte("jobLookupEntries")
	bucketEnvs             = []byte("envs")
	bucketStdO             = []byte("stdo")
	bucketStdE             = []byte("stde")
	bucketJobRAM           = []byte("jobRAM")
	bucketJobDisk          = []byte("jobDisk")
	bucketJobSecs          = []byte("jobSecs")
	bucketRGEndTime        = []byte("repgroupEndTime") //nolint:gochecknoglobals
	bucketEndTimeToKey     = []byte("endTimeToKey")    //nolint:gochecknoglobals
)

// Rec* variables are only exported for testing purposes (*** though they should
// probably be user configurable somewhere...).
//
//nolint:gochecknoglobals // exported tunables, overridable by tests
var (
	RecMBRound  = 100 // when we recommend amount of memory to reserve for a job, we round up to the nearest RecMBRound MBs
	RecSecRound = 1   // when we recommend time to reserve for a job, we round up to the nearest RecSecRound seconds
)

// managerDBOpenTimeout bounds how long the manager's initDB waits for the
// BoltDB file lock before failing with ErrDBLocked (spec E7). Without it bbolt
// retries the flock every 50ms forever, so a second manager started while the
// first is still in its startup window blocks indefinitely and then acquires the
// database the instant the winner exits - which collides with the documented
// rollback procedure, since it would start writing to the file being restored.
//
// 30s is not derived from ServerShutdownWaitTime: the lock is held well past it,
// because db.close copies the whole database to db_bk before closing bolt, and
// at production's 7GB on NFS that copy alone can exceed 30s. It sits between the
// bounded waits inside a shutdown and the 120s wr manager stop itself allows
// (daemonStopGiveupS), so a start racing a prompt shutdown still wins the lock
// while a start racing a slow one fails with a message naming the file rather
// than lurking until the winner exits. The cost is real and accepted: a restart
// that overlaps a slow shutdown can now fail and need retrying.
//
// It is a package var (not user-configurable) purely so tests can shorten it,
// which is also what keeps their harness deadlines short.
//
//nolint:gochecknoglobals // internal tuning knob; a var only so tests can vary it
var managerDBOpenTimeout = 30 * time.Second

// ErrDBLocked is returned by initDB when another wr manager holds the database
// file lock. It is deliberately NOT treated as a corrupt database: the
// restore-from-backup path would unlink the live file out from under the running
// manager and come up on a stale backup, on a fresh inode so the flock protects
// nothing (spec E7).
var ErrDBLocked = errors.New("another wr manager holds this database")

// errDBClosed is returned when an operation is attempted on a closed database.
var errDBClosed = errors.New("database closed")

// errArchivePanic is returned to the one caller whose archive panicked, so a
// malformed job fails only itself (as it did under bbolt.Batch's safelyCall).
var errArchivePanic = errors.New("panic while archiving job")

// jobExitUpdatePollInterval is how often retrieveJobStd polls for in-progress
// updateJobAfterExit() calls to complete.
const jobExitUpdatePollInterval = 10 * time.Millisecond

// jobStatWindowScaleThreshold is the prior-value count above which the 95th
// percentile window is scaled up proportionally.
const jobStatWindowScaleThreshold = 100

const (
	// storeBatchDivisor, storeBatchGranularity and storeBatchRoundThreshold
	// control storeBatched's batch sizing: it aims for batches of len(data) /
	// storeBatchDivisor, at least storeBatchGranularity, rounded to the nearest
	// storeBatchGranularity.
	storeBatchDivisor        = 10
	storeBatchGranularity    = 1000
	storeBatchRoundThreshold = 500
)

// slowBackupTestDelay is an artificial delay used only in tests (when
// db.slowBackups is set) to make backups take a noticeable amount of time.
const slowBackupTestDelay = 100 * time.Millisecond

const (
	dbUpgradeProgressEntries  = 10000
	dbUpgradeProgressInterval = 2 * time.Second

	// dbUpgradeLogIntervalDefault is how often a database upgrade's progress is
	// logged where a default `wr manager start` can show it (see
	// dbUpgradeReporter). It is far coarser than dbUpgradeProgressInterval, which
	// paces the status sidecar: a rebuild of millions of entries must add tens of
	// manager log lines, not thousands, while still showing an operator that a
	// minutes-long phase is moving. A phase shorter than one interval logs no
	// progress at all, its start and completion lines being enough.
	dbUpgradeLogIntervalDefault = 30 * time.Second
)

// dbUpgradeLogInterval is the live interval. It is a var only so tests can
// shorten it; nothing user-facing changes it.
//
//nolint:gochecknoglobals // internal tuning knob; a var only so tests can vary it
var dbUpgradeLogInterval = dbUpgradeLogIntervalDefault

// compactTxMaxSize bounds the size (bytes) of each bolt.Compact destination
// transaction, so compacting a multi-gigabyte database commits regularly instead
// of buffering the whole copy in one transaction (see bolt.Compact). A value of
// 0 would use a single transaction.
const compactTxMaxSize = 64 * 1024 * 1024

// backupCopySyncInterval is how many bytes copyBackup writes to the backup file
// before forcing writeback of that region (see backupCopyWriter.pace). 8 MiB caps
// the delay a concurrent foreground archive/touch commit's fdatasync can suffer to
// roughly one interval's worth of backup writeback, independent of the DB's size
// or the storage's speed; it was tuned as the freeze-floor minimum below which the
// periodic full-file backup no longer stalls the manager.
const backupCopySyncInterval = 8 * 1024 * 1024

//nolint:gochecknoglobals // prod-inert test seams for exercising the backup copy's pacing.
var (
	// backupCopySyncBytes is the pacing interval copyBackup uses; it defaults to
	// backupCopySyncInterval and is a var only so tests can shrink it to exercise
	// pacing on a small DB. Production never changes it.
	backupCopySyncBytes int64 = backupCopySyncInterval

	// backupPaceHook, when non-nil, is called at the start of each
	// backupCopyWriter.pace(). It is nil in production and exists only so tests
	// can observe that the backup copy is being paced.
	backupPaceHook func()
)

const (
	limitGroupUnchanged limitGroupOutcome = iota
	limitGroupChanged
	limitGroupRemoved

	// limitGroupBytes is the number of bytes used to store a limit group's
	// count (a uint64).
	limitGroupBytes = 8
)

// sobsd ('slice of byte slice doublets') implements sort interface so we can
// sort a slice of []byte doublets, sorting on the first byte slice, needed for
// efficient Puts in to the database.
type sobsd [][2][]byte

func (s sobsd) Len() int {
	return len(s)
}

func (s sobsd) Swap(i, j int) {
	s[i], s[j] = s[j], s[i]
}

func (s sobsd) Less(i, j int) bool {
	cmp := bytes.Compare(s[i][0], s[j][0])

	return cmp == -1
}

// sobsdStorer is the kind of function that stores the contents of a sobsd in
// a particular bucket.
type sobsdStorer func(bucket []byte, encodes sobsd) (err error)

// reverseLookupEntries stores complete keys for bucketJobLookupEntries. During
// old-DB upgrades we collect only these index keys, not job payloads, so they
// can be sorted into destination-bucket order before BoltDB sees any Put.
type reverseLookupEntries [][]byte

// collectReverseLookupRebuildEntries keeps one generated reverse key per
// historical lookup key in memory for this one-time upgrade. That is
// intentionally limited to index keys (not encoded jobs), and lets us sort once
// so the new BoltDB bucket is written in destination-key order instead of doing
// the hours-long random insertion pattern seen on large legacy DBs.
func collectReverseLookupRebuildEntries(tx *bolt.Tx,
	progress *dbUpgradeReporter,
) (reverseLookupEntries, int, error) {
	totalProcessed := 0
	entries := reverseLookupEntries{}

	for _, bucket := range indexedLookupBuckets() {
		processed, err := collectReverseLookupRebuildBucket(tx, bucket, progress, totalProcessed, &entries)
		if err != nil {
			return nil, totalProcessed, err
		}

		totalProcessed += processed
	}

	sort.Sort(entries)

	return compactSortedReverseLookupEntries(entries), totalProcessed, nil
}

func compactSortedReverseLookupEntries(entries reverseLookupEntries) reverseLookupEntries {
	if len(entries) == 0 {
		return entries
	}

	writeAt := 1
	for _, entry := range entries[1:] {
		if bytes.Equal(entry, entries[writeAt-1]) {
			continue
		}

		entries[writeAt] = entry
		writeAt++
	}

	clear(entries[writeAt:])

	return entries[:writeAt]
}

func (r reverseLookupEntries) Len() int {
	return len(r)
}

func (r reverseLookupEntries) Swap(i, j int) {
	r[i], r[j] = r[j], r[i]
}

func (r reverseLookupEntries) Less(i, j int) bool {
	return bytes.Compare(r[i], r[j]) < 0
}

func collectReverseLookupRebuildBucket(tx *bolt.Tx, bucket []byte, progress *dbUpgradeReporter,
	processedBefore int, entries *reverseLookupEntries,
) (int, error) {
	b := tx.Bucket(bucket)
	if b == nil {
		return 0, nil
	}

	processed := 0
	bucketName := string(bucket)

	err := b.ForEach(func(k, _ []byte) error {
		processed++

		totalProcessed := processedBefore + processed
		if progress.progressDue(totalProcessed) {
			progress.progress(internal.DBUpgradeJobLookupState,
				fmt.Sprintf("rebuilding database job lookup index (%d source entries processed so far; currently reading %s)",
					totalProcessed, bucketName),
				totalProcessed)
		}

		appendReverseLookupRebuildEntry(entries, bucket, k)

		return nil
	})

	return processed, err
}

func appendReverseLookupRebuildEntry(entries *reverseLookupEntries, lookupBucket, lookupKey []byte) {
	jobKey := lookupEntryJobKey(lookupKey)
	if len(jobKey) == 0 {
		return
	}

	*entries = append(*entries, reverseLookupEntryKey(jobKey, lookupBucket, lookupKey))
}

func putReverseLookupRebuildEntries(tx *bolt.Tx, entries reverseLookupEntries, progress *dbUpgradeReporter,
	totalProcessed int,
) error {
	b := tx.Bucket(bucketJobLookupEntries)
	if b == nil {
		return fmt.Errorf("%w: %s", berrors.ErrBucketNotFound, bucketJobLookupEntries)
	}

	for i, key := range entries {
		if i > 0 && progress.progressDue(i) {
			progress.writeProgress(internal.DBUpgradeJobLookupState,
				fmt.Sprintf("writing sorted database job lookup index (%d reverse entries written, %d entries processed)",
					i, totalProcessed),
				totalProcessed)
		}

		if err := b.Put(key, nil); err != nil {
			return err
		}
	}

	return nil
}

// archivedJobFacets are the few fields of an archived job needed to place it in a
// bounded page of its RepGroup's history: the two times it is ordered by, and the
// state, exit code, fail reason and lost flag that decide which of limitJobs'
// groups it falls in.
//
// Its field names are deliberately the same as Job's. The codec encodes a Job as
// a map of its exported field names, so decoding a record into this instead
// matches these six by name and structurally SKIPS the rest, without allocating
// the Cmd, Env, StdOut and StdErr values that make a full decode expensive: a
// 130KB-command record costs 1.7us and 48 bytes here versus 46.5us and 134KB
// through decodeArchivedJob.
type archivedJobFacets struct {
	StartTime  time.Time
	EndTime    time.Time
	State      JobState
	Exitcode   int
	FailReason string
	Lost       bool
}

// completeJobsQuery describes how the caller of
// retrieveOldestCompleteJobsByRepGroup will group and limit the archived jobs it
// asks for, so that the ones it could never return are counted instead of
// decoded.
type completeJobsQuery struct {
	// group returns the key of the group the caller will put an archived job with
	// these facets in, and false if the caller's filters discard it outright.
	group func(*archivedJobFacets) (string, bool)

	// limit returns how many more jobs of the given group the caller can still
	// use. Any beyond that are only counted.
	limit func(string) int
}

// completeJobsPage is a bounded page of one RepGroup's archived jobs.
type completeJobsPage struct {
	// jobs are the archived jobs that were decoded, oldest-started first.
	jobs []*Job

	// fetched is how many of jobs fell in each group.
	fetched map[string]int

	// counted is how many of the RepGroup's archived jobs fall in each group,
	// including the ones jobs does not contain.
	counted map[string]int
}

// archivedJobCandidate is an archived job a bounded page might contain: its bolt
// key (which, like every bolt key, is only valid for the life of the transaction
// it was read in), its group, and the times it is ordered by.
type archivedJobCandidate struct {
	key   []byte
	group string
	start time.Time
	end   time.Time
}

// oldestArchivedJobs keeps the limit oldest-started of the archived jobs offered
// to it, without holding on to the rest.
//
// It sorts and truncates only once it has twice as many candidates as it needs,
// so offering the whole of a RepGroup's history costs O(history log limit) time
// but only O(limit) memory - which is the entire point, since it is the memory
// that took production's manager to a 12.1GB heap. The sort is stable, so jobs
// with identical times stay in the order the cursor produced them.
type oldestArchivedJobs struct {
	limit      int
	pruneAt    int
	candidates []archivedJobCandidate
}

// newOldestArchivedJobs returns a selector that keeps the limit oldest-started of
// the jobs offered to it, pruning once it holds twice that many - or never, if
// twice that many would overflow, which can only mean the limit already exceeds
// any history it could be asked about.
func newOldestArchivedJobs(limit int) *oldestArchivedJobs {
	limit = max(limit, 0)

	pruneAt := limit + limit
	if pruneAt < limit {
		pruneAt = math.MaxInt
	}

	return &oldestArchivedJobs{limit: limit, pruneAt: pruneAt}
}

// offer gives the selector another of the group's archived jobs to consider.
func (o *oldestArchivedJobs) offer(key []byte, group string, facets *archivedJobFacets) {
	// NOTE: this early-out is a memory optimisation, NOT a protected guarantee: with
	// limit 0 pruneAt is 0 too, so without it every offer would append and then
	// immediately have oldest() truncate back to nothing, returning the same (empty)
	// selection. It matters because a group whose budget is already spent is offered
	// every remaining record of the history, and that is exactly the append this fix
	// exists to avoid.
	if o.limit == 0 {
		return
	}

	o.candidates = append(o.candidates, archivedJobCandidate{
		key:   key,
		group: group,
		start: facets.StartTime,
		end:   facets.EndTime,
	})

	if len(o.candidates) >= o.pruneAt {
		o.oldest()
	}
}

// oldest sorts the candidates offered so far oldest-started first, drops any
// beyond the limit, and returns what is left.
func (o *oldestArchivedJobs) oldest() []archivedJobCandidate {
	sort.SliceStable(o.candidates, func(i, j int) bool {
		return startedBefore(o.candidates[i].start, o.candidates[i].end,
			o.candidates[j].start, o.candidates[j].end)
	})

	if len(o.candidates) > o.limit {
		o.candidates = o.candidates[:o.limit]
	}

	return o.candidates
}

// startedBefore orders archived jobs by start time, then end time. Both the
// unbounded fetch and the bounded one must order by it, or a limited request
// would return different jobs from the ones the same request without a limit puts
// first.
func startedBefore(aStart, aEnd, bStart, bEnd time.Time) bool {
	if aStart.Equal(bStart) {
		return aEnd.Before(bEnd)
	}

	return aStart.Before(bStart)
}

// pace bounds the backup copy's dirty-page backlog for the bytes written since the
// last pace. On Linux it starts asynchronous writeback of the just-written range
// and waits on the previous one (cheap, pipelined, no full-file round-trip);
// elsewhere it falls back to a full fsync (portable, but adds copy duration).
func (w *backupCopyWriter) pace() error {
	if backupPaceHook != nil {
		backupPaceHook()
	}

	curOffset, curLength := w.syncedOffset, w.written-w.syncedOffset
	if handled, err := backupPaceRange(w.f, curOffset, curLength, w.prevOffset, w.prevLength); handled {
		w.prevOffset, w.prevLength = curOffset, curLength
		w.syncedOffset = w.written

		return err
	}

	return w.f.Sync()
}

// writePacedChunk writes the leading syncEvery-bounded slice of p to the backup
// file, advancing w.written and w.sinceSync, and paces once if that slice completes
// a syncEvery interval. Sizing each slice to the interval boundary keeps pacing at
// most one interval behind regardless of len(p). It returns the bytes consumed and
// the first error (a short write or a pace failure).
func (w *backupCopyWriter) writePacedChunk(p []byte) (int, error) {
	chunk := p[:min(int64(len(p)), w.syncEvery-w.sinceSync)]

	n, err := w.f.Write(chunk)
	w.written += int64(n)
	w.sinceSync += int64(n)

	if err != nil {
		return n, err
	}

	if w.sinceSync < w.syncEvery {
		return n, nil
	}

	w.sinceSync = 0

	return n, w.pace()
}

func newJobExitData(job *Job, stdo, stde []byte, forceStorage bool) jobExitData {
	requiredRAM := 0
	if job.Requirements != nil {
		requiredRAM = job.Requirements.RAM
	}

	return jobExitData{
		key:          job.Key(),
		stdo:         stdo,
		stde:         stde,
		exitcode:     job.Exitcode,
		forceStorage: forceStorage,
		failReason:   job.FailReason,
		reqGroup:     job.ReqGroup,
		peakRAM:      job.PeakRAM,
		requiredRAM:  requiredRAM,
		peakDisk:     job.PeakDisk,
		secs:         int(math.Ceil(job.EndTime.Sub(job.StartTime).Seconds())),
	}
}

func (e jobExitData) shouldRecordHighPeakRAMStat() bool {
	return e.failReason != "" &&
		e.failReason != FailReasonRAM &&
		commandExceededMemoryEstimate(e.peakRAM, e.requiredRAM)
}

// beBatch is a snapshot of pending best-effort writes, taken by swapBestEffort
// and persisted by the writer in one transaction.
type beBatch struct {
	changes map[string][]byte
	exits   []jobExitData
	wgkeys  []string
}

// apply writes the batch's coalesced live-bucket changes and its ordered exit ops
// within tx.
func (b beBatch) apply(tx *bolt.Tx) error {
	if err := b.applyChanges(tx); err != nil {
		return err
	}

	return b.applyExits(tx)
}

// applyChanges rewrites each coalesced live job, but only if it is still present,
// preserving the archive-vs-change race guard: a "started" update must not
// resurrect a job that a concurrent archiveJob already removed from the live
// bucket.
func (b beBatch) applyChanges(tx *bolt.Tx) error {
	bjl := tx.Bucket(bucketJobsLive)

	for key, encoded := range b.changes {
		if bjl.Get([]byte(key)) == nil {
			continue
		}

		if err := bjl.Put([]byte(key), encoded); err != nil {
			return err
		}
	}

	return nil
}

// applyExits runs each exit op's transactional update (live-bucket rewrite, std
// refresh and fail-stat) in order, preserving every per-op side effect.
func (b beBatch) applyExits(tx *bolt.Tx) error {
	for i := range b.exits {
		if err := b.exits[i].update(tx); err != nil {
			return err
		}
	}

	return nil
}

// archivedScan is the state selectOldestArchivedJobs carries across the records it
// walks: the caller's query, the per-group counts and selections being built, and
// a decoder and facets buffer reused for every record so that walking a whole
// history allocates nothing per record.
type archivedScan struct {
	query     completeJobsQuery
	counted   map[string]int
	selectors map[string]*oldestArchivedJobs
	decoder   *codec.Decoder
	facets    archivedJobFacets
}

// consider decodes one archived record's facets and, if the query keeps it, counts
// it and offers it to its group's selector.
func (a *archivedScan) consider(key, encoded []byte) error {
	a.facets = archivedJobFacets{}

	a.decoder.ResetBytes(encoded)

	if err := a.decoder.Decode(&a.facets); err != nil {
		return err
	}

	group, keep := a.query.group(&a.facets)
	if !keep {
		return nil
	}

	a.counted[group]++
	a.selector(group).offer(key, group, &a.facets)

	return nil
}

// selector returns the selector keeping group's oldest jobs, creating it with the
// group's limit if this is the first job seen in it.
func (a *archivedScan) selector(group string) *oldestArchivedJobs {
	selector, exists := a.selectors[group]
	if !exists {
		selector = newOldestArchivedJobs(a.query.limit(group))
		a.selectors[group] = selector
	}

	return selector
}

type db struct {
	backupLast           time.Time
	backupPath           string
	backupPathTmp        string
	ch                   codec.Handle
	backupStopWait       chan bool
	backupMount          *muxfys.MuxFys
	backupNotification   chan bool
	backupWait           time.Duration
	backupTickerStop     chan struct{} // closed by close() to stop the backup ticker
	bolt                 *bolt.DB
	envcache             *lru.ARCCache[string, []byte]
	updatingAfterJobExit atomic.Int64
	// archivedDecodes counts how many archived jobs decodeArchivedJob has actually
	// codec-decoded. It is INERT observability in the style of Job.derivations and
	// archiveTxObserver: nothing but the reliable4 history-scan tests read it and it
	// affects no behaviour. It lives on the db, rather than being a package global,
	// so a test counts only the decodes of ITS OWN server; a process-wide counter
	// would be perturbed by any other live server in the same test binary.
	archivedDecodes atomic.Uint64
	// depGroupSeenGets counts the dep groups a resolution pass actually read from
	// bucketDepGroups - one per distinct group with no live member, not one per
	// job naming it. resolveDependencies passes it to newSeenDepGroupCache. Like
	// archivedDecodes it is INERT observability, and lives on the db so a test
	// counts only its own server's reads.
	depGroupSeenGets atomic.Uint64
	wg               *waitgroup.WaitGroup
	wgMutex          sync.Mutex // protects wg since we want to call Wait() while another goroutine might call Add()
	sync.RWMutex
	// The best-effort change/exit updates are persisted by a single long-lived
	// writer goroutine (bestEffortWriter) that folds ALL currently-pending work
	// into ONE write transaction per drain, instead of spawning one goroutine plus
	// one tiny bolt batch per state change. The old per-change spawn piled up ~100k
	// goroutines on a mass un-suspend and collapsed into thousands of fsync'd txns
	// that starved the synchronous archive path (the reliable4 prod freeze). beMu
	// guards the pending structures below; the writer never takes db.Lock or
	// db.wgMutex, so enqueuing under those locks can never deadlock against it.
	beMu         sync.Mutex
	beChanges    map[string][]byte // key -> latest encoded live value (coalescing, latest-wins)
	beExits      []jobExitData     // exit ops, applied in order (std/fail-stat side effects, not coalesced)
	beWGKeys     []string          // db.wg keys to Done once the pending batch is persisted
	beSignal     chan struct{}     // buffered(1) kick: work is pending
	beStop       chan struct{}     // closed by close() to stop the writer after a final drain
	beWriterDone chan struct{}     // closed by the writer when it has fully stopped
	// Archives are SYNCHRONOUS - the client's archive RPC blocks on the outcome -
	// but they too are persisted by a single long-lived coalescing writer
	// (archiveWriter), which folds every currently-pending archive into ONE
	// db.Update and then replies to each waiter individually. Previously each
	// archive committed its own transaction via db.bolt.Batch, and bbolt's batching
	// could not save it: Batch detaches its current batch the instant one STARTS
	// and arms a fresh MaxBatchDelay timer, so archives arriving further apart than
	// that delay each got a transaction of their own and queued on the single bolt
	// write lock. Live production (2026-08-17, ~660 runners) measured that queue
	// persistently ~600 deep draining at ~12/s, spread over 234 concurrent
	// bbolt.(*batch).run goroutines, for a MEAN archive block of 43s against the
	// 60s ClientMinRequestTimeout floor - which turned successfully exited jobs
	// into `delayed` ones (reliable4 FINDING 2). arMu guards the pending queue; the
	// writer takes neither db.Lock nor db.wgMutex, so the archive path stays off
	// the exclusive db lock exactly as before (bugfix 260727-2 part A).
	arMu         sync.Mutex
	arPending    []*archiveOp  // archives waiting to be folded into the next transaction
	arSignal     chan struct{} // buffered(1) kick: archives are pending
	arStop       chan struct{} // closed by close() to stop the writer after a final drain
	arWriterDone chan struct{} // closed by the writer when it has fully stopped
	arStopped    bool          // guarded by arMu: the writer will drain no more
	// backupMu guards the backup-state fields below (backingUp, backupFinal,
	// backupQueued, backupStopped, slowBackups, backupLast, backupWait), keeping
	// backup coordination off the exclusive db RWMutex so the archive/exit hot
	// path never contends with it. backupDirty is lock-free (set by every
	// completion, consumed by the backup ticker).
	backupMu       sync.Mutex
	backupDirty    atomic.Bool
	backingUp      bool
	backupFinal    bool
	backupQueued   bool
	backupStopped  bool // set by close() so no further periodic backup starts
	backupsEnabled bool
	s3accessor     *muxfys.S3Accessor
	closed         bool
	slowBackups    bool // just for testing purposes
	upgradedOnOpen bool
	recSecRound    int // rounding (secs) for recommended reserve times; from the server's timings
	recMBRound     int // rounding (MBs) for recommended memory/disk; from the server's timings
}

// backupCopyWriter streams a consistent DB backup copy to f, forcing writeback of
// the copy every syncEvery bytes via pace(). Without this the copy's writes
// accumulate as gigabytes of dirty (e.g. NFS) pages that starve a concurrent
// foreground archive's fdatasync, freezing the manager for the copy's duration
// (the periodic full-file backup stall).
type backupCopyWriter struct {
	f            *os.File
	written      int64
	sinceSync    int64
	syncedOffset int64
	prevOffset   int64 // previous paced chunk (SFR pipeline: one chunk behind)
	prevLength   int64
	syncEvery    int64
}

// Write streams p to the backup file, pacing writeback every syncEvery bytes. It
// splits p into sub-writes that each land on a syncEvery boundary, so a single
// write larger than syncEvery (tx.WriteTo can hand us one) still paces once per
// interval instead of accumulating an unbounded dirty-page backlog behind one late
// pace. It returns the bytes consumed from p and the first error encountered.
func (w *backupCopyWriter) Write(p []byte) (int, error) {
	if w.syncEvery <= 0 {
		n, err := w.f.Write(p)
		w.written += int64(n)

		return n, err
	}

	var total int

	for len(p) > 0 {
		n, err := w.writePacedChunk(p)
		total += n
		p = p[n:]

		if err != nil {
			return total, err
		}
	}

	return total, nil
}

// copyBackup writes a consistent copy of the DB (via a read tx) to path. It always
// streams the copy through a backupCopyWriter that paces writeback every
// backupCopySyncBytes, then fsyncs for durability. This produces the same file
// tx.CopyFile would, but without letting the copy's dirty pages accumulate and
// stall concurrent foreground commits.
func (db *db) copyBackup(tx *bolt.Tx, path string) error {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_TRUNC, dbFilePermission)
	if err != nil {
		return err
	}

	w := &backupCopyWriter{
		f:         f,
		syncEvery: backupCopySyncBytes,
	}

	_, werr := tx.WriteTo(w)
	if werr == nil {
		werr = f.Sync()
	}

	cerr := f.Close()

	if werr != nil {
		return werr
	}

	return cerr
}

// initDB opens/creates our database and sets things up for use. If dbFile
// doesn't exist or seems corrupted, we copy it from backup if that exists,
// otherwise we start fresh.
//
// dbBkFile can be an S3 url specified like: s3://[profile@]bucket/path/file
// which will cause that s3 path to be mounted in the same directory as dbFile
// and backups will be written there.
//
// In development we delete any existing db and force a fresh start (unless
// wipeDevDB is false). Backups are also not carried out in development (unless
// forceBackups is true), so dbBkFile is ignored.
//
//nolint:lll,gocognit,gocyclo,cyclop,funlen,maintidx,nestif // entry point: sequential fallible db open/recovery
func initDB(ctx context.Context, dbFile, dbBkFile, deployment string, wipeDevDB, forceBackups bool) (*db, string, error) {
	// the manager is unreachable for the whole of this and the recovery phases
	// that follow it, so each phase reports its own elapsed time at warn, where
	// the default log level shows it and an operator sizing the startup window
	// can see where the time went (spec E9). initDB's own cost is dominated by
	// opening and mmapping the file, which at production's multi-GB size is the
	// bulk of "process start to first log line".
	initStarted := time.Now()

	var backupsEnabled bool

	var accessor *muxfys.S3Accessor

	backupPathTmp := dbBkFile + ".tmp"

	var msg string

	if deployment == internal.Production || forceBackups {
		backupsEnabled = true

		if internal.InS3(dbBkFile) {
			if deployment == internal.Development {
				dbBkFile += "." + deployment
			}

			path := strings.TrimPrefix(dbBkFile, internal.S3Prefix)
			pp := strings.Split(path, "@")

			profile := "default"
			if len(pp) == s3ProfilePathParts {
				profile = pp[0]
				path = pp[1]
			}

			path = filepath.Dir(path)

			accessorConfig, err := muxfys.S3ConfigFromEnvironment(profile, path)
			if err != nil {
				return nil, "", err
			}

			accessor, err = muxfys.NewS3Accessor(accessorConfig)
			if err != nil {
				return nil, "", err
			}

			dbBkFile = filepath.Join(path, filepath.Base(dbBkFile))

			dbBkFile, err = stripBucketFromS3Path(dbBkFile)
			if err != nil {
				return nil, "", err
			}

			backupPathTmp = dbFile + ".s3backup_tmp"

			if _, err = os.Stat(dbFile); os.IsNotExist(err) {
				err = accessor.DownloadFile(dbBkFile, dbFile)
				if err == nil {
					msg = "recreated missing db file " + dbFile + " from s3 backup file " + dbBkFile
				}
			}
		}
	}

	if wipeDevDB && deployment == internal.Development {
		errr := os.Remove(dbFile)
		if errr != nil && !os.IsNotExist(errr) {
			clog.Warn(ctx, "Failed to remove database file", "path", dbFile, "err", errr)
		}

		if accessor != nil {
			errr = accessor.DeleteFile(dbBkFile)
		} else {
			errr = os.Remove(dbBkFile)
		}

		if errr != nil && !os.IsNotExist(errr) {
			clog.Warn(ctx, "Failed to remove database backup file", "path", dbBkFile, "err", errr)
		}
	}

	var (
		boltdb           *bolt.DB
		err              error
		openedExistingDB bool
	)
	if _, err = os.Stat(dbFile); os.IsNotExist(err) {
		if _, err = os.Stat(dbBkFile); os.IsNotExist(err) {
			boltdb, err = openManagerBolt(dbFile)
			msg = "created new empty db file " + dbFile
		} else {
			err = copyFile(dbBkFile, dbFile)
			if err != nil {
				return nil, msg, err
			}

			boltdb, err = openManagerBolt(dbFile)
			msg = "recreated missing db file " + dbFile + " from backup file " + dbBkFile
			openedExistingDB = true
		}
	} else {
		openedExistingDB = true

		boltdb, err = openManagerBolt(dbFile)
		if err != nil {
			// a lock timeout means another wr manager is running, NOT that the
			// file is corrupt, and it must never reach the restore path below:
			// that path treats any open error as "corrupt (?) db file", so it
			// would unlink the live database out from under the running manager
			// (which keeps writing to a deleted inode and loses everything at
			// exit) and come up as a second live manager on a stale backup, on a
			// fresh inode so the flock protects nothing (spec E7).
			if errors.Is(err, berrors.ErrTimeout) {
				return nil, msg, fmt.Errorf("%w: %s", ErrDBLocked, dbFile)
			}

			// try the backup
			bkPath := dbBkFile
			if accessor != nil {
				bkPath = backupPathTmp

				errdl := accessor.DownloadFile(dbBkFile, bkPath)
				if errdl != nil {
					msg = fmt.Sprintf("tried to recreate corrupt (?) db file %s "+
						"from s3 backup file %s (error with original db file was: %s)",
						dbFile, dbBkFile, err)

					return nil, msg, errdl
				}

				defer func() {
					errr := os.Remove(bkPath)
					if errr != nil {
						clog.Warn(ctx, "failed to remove temporary s3 download of database backup", "err", errr)
					}
				}()
			}

			if _, errbk := os.Stat(bkPath); errbk == nil {
				backupDB, errbk := openManagerBolt(bkPath)
				if errbk == nil {
					msg = fmt.Sprintf("tried to recreate corrupt (?) db file %s from backup file %s "+
						"(error with original db file was: %s)", dbFile, dbBkFile, err)
					if errbk = backupDB.Close(); errbk != nil {
						return nil, msg, errbk
					}

					origerr := err

					err = os.Remove(dbFile)
					if err != nil {
						return nil, msg, err
					}

					err = copyFile(bkPath, dbFile)
					if err != nil {
						return nil, msg, err
					}

					boltdb, err = openManagerBolt(dbFile)
					msg = fmt.Sprintf("recreated corrupt (?) db file %s from backup file %s "+
						"(error with original db file was: %s)", dbFile, dbBkFile, origerr)
				}
			}
		}
	}

	if err != nil {
		return nil, msg, err
	}

	upgrade := newDBUpgradeReporter(ctx, dbFile)

	// ensure our buckets are in place
	err = boltdb.Update(func(tx *bolt.Tx) error {
		_, errf := tx.CreateBucketIfNotExists(bucketJobsLive)
		if errf != nil {
			return fmt.Errorf("create bucket %s: %w", bucketJobsLive, errf)
		}

		_, errf = tx.CreateBucketIfNotExists(bucketJobsComplete)
		if errf != nil {
			return fmt.Errorf("create bucket %s: %w", bucketJobsComplete, errf)
		}

		_, errf = tx.CreateBucketIfNotExists(bucketRTK)
		if errf != nil {
			return fmt.Errorf("create bucket %s: %w", bucketRTK, errf)
		}

		_, errf = tx.CreateBucketIfNotExists(bucketRGs)
		if errf != nil {
			return fmt.Errorf("create bucket %s: %w", bucketRGs, errf)
		}

		_, errf = tx.CreateBucketIfNotExists(bucketLGs)
		if errf != nil {
			return fmt.Errorf("create bucket %s: %w", bucketLGs, errf)
		}

		_, errf = tx.CreateBucketIfNotExists(bucketDTK)
		if errf != nil {
			return fmt.Errorf("create bucket %s: %w", bucketDTK, errf)
		}

		hadDepGroups := tx.Bucket(bucketDepGroups) != nil

		_, errf = tx.CreateBucketIfNotExists(bucketDepGroups)
		if errf != nil {
			return fmt.Errorf("create bucket %s: %w", bucketDepGroups, errf)
		}

		if openedExistingDB && !hadDepGroups {
			upgrade.startPhase(internal.DBUpgradeDepGroupIndexState, "rebuilding database dependency-group index")

			errf = rebuildDepGroups(tx, upgrade)
			if errf != nil {
				return fmt.Errorf("rebuild bucket %s: %w", bucketDepGroups, errf)
			}
		}

		_, errf = tx.CreateBucketIfNotExists(bucketRDTK)
		if errf != nil {
			return fmt.Errorf("create bucket %s: %w", bucketRDTK, errf)
		}

		hadJobLookupEntries := tx.Bucket(bucketJobLookupEntries) != nil

		_, errf = tx.CreateBucketIfNotExists(bucketJobLookupEntries)
		if errf != nil {
			return fmt.Errorf("create bucket %s: %w", bucketJobLookupEntries, errf)
		}

		if openedExistingDB && !hadJobLookupEntries {
			upgrade.startPhase(internal.DBUpgradeJobLookupState, "rebuilding database job lookup index")

			errf = rebuildJobLookupEntries(tx, upgrade)
			if errf != nil {
				return fmt.Errorf("rebuild bucket %s: %w", bucketJobLookupEntries, errf)
			}
		}

		_, errf = tx.CreateBucketIfNotExists(bucketEnvs)
		if errf != nil {
			return fmt.Errorf("create bucket %s: %w", bucketEnvs, errf)
		}

		_, errf = tx.CreateBucketIfNotExists(bucketStdO)
		if errf != nil {
			return fmt.Errorf("create bucket %s: %w", bucketStdO, errf)
		}

		_, errf = tx.CreateBucketIfNotExists(bucketStdE)
		if errf != nil {
			return fmt.Errorf("create bucket %s: %w", bucketStdE, errf)
		}

		_, errf = tx.CreateBucketIfNotExists(bucketJobRAM)
		if errf != nil {
			return fmt.Errorf("create bucket %s: %w", bucketJobRAM, errf)
		}

		_, errf = tx.CreateBucketIfNotExists(bucketJobDisk)
		if errf != nil {
			return fmt.Errorf("create bucket %s: %w", bucketJobDisk, errf)
		}

		_, errf = tx.CreateBucketIfNotExists(bucketJobSecs)
		if errf != nil {
			return fmt.Errorf("create bucket %s: %w", bucketJobSecs, errf)
		}

		_, errf = tx.CreateBucketIfNotExists(bucketRGEndTime)
		if errf != nil {
			return fmt.Errorf("create bucket %s: %w", bucketRGEndTime, errf)
		}

		_, errf = tx.CreateBucketIfNotExists(bucketEndTimeToKey)
		if errf != nil {
			return fmt.Errorf("create bucket %s: %w", bucketEndTimeToKey, errf)
		}

		if upgrade.active() {
			upgrade.startPhase(internal.DBUpgradeCommitState, "committing database upgrade")
		}

		return nil
	})
	upgradedOnOpen := upgrade.active()
	upgrade.finish(err)
	if err != nil {
		return nil, msg, err
	}

	// we will cache frequently used things to avoid actual db (disk) access.
	// We don't expect that many different ENVs to be in use at once.
	envcache, err := lru.NewARC[string, []byte](envCacheSize)
	if err != nil {
		return nil, msg, err
	}

	dbstruct := &db{
		bolt:               boltdb,
		envcache:           envcache,
		ch:                 new(codec.BincHandle),
		backupsEnabled:     backupsEnabled,
		backupPath:         dbBkFile,
		backupPathTmp:      backupPathTmp,
		backupNotification: make(chan bool),
		backupWait:         minimumTimeBetweenBackups,
		backupStopWait:     make(chan bool),
		backupTickerStop:   make(chan struct{}),
		s3accessor:         accessor,
		wg:                 waitgroup.New(),
		beChanges:          make(map[string][]byte),
		beSignal:           make(chan struct{}, 1),
		beStop:             make(chan struct{}),
		beWriterDone:       make(chan struct{}),
		arSignal:           make(chan struct{}, 1),
		arStop:             make(chan struct{}),
		arWriterDone:       make(chan struct{}),
		upgradedOnOpen:     upgradedOnOpen,
	}

	go dbstruct.bestEffortWriter(ctx)
	go dbstruct.archiveWriter(ctx)

	if backupsEnabled {
		go dbstruct.backupTicker(ctx)
	}

	clog.Warn(ctx, "recovering: opened database", "db", dbFile,
		"elapsed", time.Since(initStarted).Round(time.Millisecond))

	return dbstruct, msg, err
}

// setBatchTuning configures BoltDB's write-transaction coalescing on the live
// database. delay sets DB.MaxBatchDelay (how long Batch waits for concurrent
// writes to join before committing) and size sets DB.MaxBatchSize (how many
// coalesced writes force an early commit). Non-positive values are ignored,
// leaving bbolt's own defaults in place. This only widens or narrows the
// coalescing window: every commit is still fsync'd, so durability is unchanged.
func (db *db) setBatchTuning(delay time.Duration, size int) {
	if delay > 0 {
		db.bolt.MaxBatchDelay = delay
	}

	if size > 0 {
		db.bolt.MaxBatchSize = size
	}
}

// storeLimitGroups stores a mapping of group names to unsigned ints in a
// dedicated bucket. If a group was already in the database, and it had a
// different value, that group name will be returned in the changed slice. If
// the group is given with a value less than 0, it is not stored in the
// database; any existing entry is removed and the name is returned in the
// removed slice.
func (db *db) storeLimitGroups(limitGroups map[string]*limiter.GroupData) (changed, removed []string, err error) {
	err = db.bolt.Batch(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketLGs)

		for group, limitG := range limitGroups {
			outcome, errs := storeLimitGroup(b, group, limitG)
			if errs != nil {
				return errs
			}

			switch outcome {
			case limitGroupRemoved:
				removed = append(removed, group)
			case limitGroupChanged:
				changed = append(changed, group)
			case limitGroupUnchanged:
			}
		}

		return nil
	})

	return changed, removed, err
}

// limitGroupOutcome describes what storeLimitGroup did with a single group.
type limitGroupOutcome int

// storeLimitGroup stores (or deletes) a single limit group in the given bucket,
// reporting what it did.
func storeLimitGroup(b *bolt.Bucket, group string, limitG *limiter.GroupData) (limitGroupOutcome, error) {
	if !limitG.IsCount() {
		return limitGroupRemoved, nil
	}

	limit := limitG.Limit()
	key := []byte(group)
	existing := b.Get(key)

	if limit < 0 {
		return deleteLimitGroup(b, key, existing)
	}

	if existing != nil && binary.BigEndian.Uint64(existing) == uint64(limit) {
		return limitGroupUnchanged, nil
	}

	if err := putLimitGroup(b, key, limit); err != nil {
		return limitGroupUnchanged, err
	}

	if existing != nil {
		return limitGroupChanged, nil
	}

	return limitGroupUnchanged, nil
}

// retrieveCompleteJobsRecent returns archived jobs whose end time is at or past
// cutoff, decoded from bucketJobsComplete, by seeking bucketEndTimeToKey at
// cutoff's UnixNano and scanning forward. A job currently live again (being
// re-run) is skipped. Returns jobs in ascending end-time order. An absent/empty
// index yields an empty slice, no error.
func (db *db) retrieveCompleteJobsRecent(cutoff time.Time) ([]*Job, error) {
	var jobs []*Job

	err := db.bolt.View(func(tx *bolt.Tx) error {
		bucket := tx.Bucket(bucketEndTimeToKey)
		if bucket == nil {
			return nil // absent index -> empty result, matching the doc contract
		}

		var errs error

		jobs, errs = db.scanCompleteJobsRecent(tx, bucket, cutoff)

		return errs
	})

	return jobs, err
}

// scanCompleteJobsRecent seeks bucket (bucketEndTimeToKey) at cutoff and scans
// forward, decoding each indexed archived job from bucketJobsComplete and
// skipping any whose key is live again. Returns the in-window jobs in ascending
// end-time order.
func (db *db) scanCompleteJobsRecent(tx *bolt.Tx, bucket *bolt.Bucket, cutoff time.Time) ([]*Job, error) {
	newJobBucket := tx.Bucket(bucketJobsLive)
	completeJobBucket := tx.Bucket(bucketJobsComplete)
	cursor := bucket.Cursor()

	var jobs []*Job

	for k, _ := cursor.Seek(endTimeSeekKey(cutoff)); k != nil; k, _ = cursor.Next() {
		job, err := db.decodeArchivedJob(completeJobBucket, newJobBucket, lookupEntryJobKey(k))
		if err != nil {
			return nil, err
		}

		if job != nil {
			jobs = append(jobs, job)
		}
	}

	return jobs, nil
}

// endTimeSeekKey returns the bucketEndTimeToKey seek key for a cutoff time: its
// end-time UnixNano as 8 big-endian bytes. Stored index keys use real
// (post-1970, positive) end-time UnixNano values, but a very large recent
// window can put cutoff before 1970, giving a negative UnixNano; uint64(negative)
// would set the high bit and sort the seek key after every entry, matching
// nothing. A non-positive cutoff therefore returns all-zero bytes, which sort at
// or before the first entry, so the forward scan returns every archived job (the
// cutoff precedes them all).
func endTimeSeekKey(cutoff time.Time) []byte {
	return endTimeToBytes(cutoff.UnixNano())
}

// archiveTxObserver, when non-nil, is called at the start of every archive's
// transactional work with the id of the write transaction it is being applied in
// and the job key being archived. It is nil in production (a single nil compare
// per archive) and affects no behaviour: it is INERT observability in the style
// of Job.derivations, and exists so the reliable4 coalescing tests can count how
// many SEPARATE write transactions M concurrent archives cost (bolt's Tx.ID is
// unique per write transaction), which is the invariant the coalescing archive
// writer exists to hold.
//
//nolint:gochecknoglobals // prod-inert test seam, like backupPaceHook above.
var archiveTxObserver func(txID int, key []byte)

// archiveJobTx is the transactional part of archiveJob: it moves the job from
// the live bucket to the complete bucket, removes its std buckets, records its
// resource-usage stats, updates its repgroup end time and records the job's end
// time in the time-ordered per-job end-time index. updateEndTimeIndex runs
// before the complete-record Put because it recovers the job's prior end time
// from that record to drop any stale forward index entry.
func (db *db) archiveJobTx(tx *bolt.Tx, key, encoded []byte, job *Job) error {
	if archiveTxObserver != nil {
		archiveTxObserver(tx.ID(), key)
	}

	for _, bucket := range [][]byte{bucketStdO, bucketStdE, bucketJobsLive} {
		if err := tx.Bucket(bucket).Delete(key); err != nil {
			return err
		}
	}

	if err := db.updateEndTimeIndex(tx, key, job); err != nil {
		return err
	}

	if err := tx.Bucket(bucketJobsComplete).Put(key, encoded); err != nil {
		return err
	}

	if err := putJobStats(tx, job); err != nil {
		return err
	}

	return updateRGEndTime(tx.Bucket(bucketRGEndTime), job)
}

// updateEndTimeIndex records job's end time in the time-ordered per-job index,
// replacing any previous entry for the same key so only the latest completion
// per key is indexed. Uses job.EndTime.UnixNano as 8 big-endian bytes. The prior
// end time is recovered from the job's existing bucketJobsComplete record, so
// this must run before that record is overwritten. No-op if the stored end time
// is unchanged.
func (db *db) updateEndTimeIndex(tx *bolt.Tx, jobKey []byte, job *Job) error {
	newNanos := job.EndTime.UnixNano()

	changed, err := db.dropStaleEndTimeIndex(tx, jobKey, newNanos)
	if err != nil || !changed {
		return err
	}

	newTimeBytes := endTimeToBytes(newNanos)

	return tx.Bucket(bucketEndTimeToKey).Put(endTimeIndexKey(newTimeBytes, jobKey), nil)
}

// endTimeToBytes encodes a UnixNano end time as endTimeBytes big-endian bytes,
// clamping a non-positive value to all-zero bytes so a zero/pre-1970 time cannot
// wrap through uint64 into a high-sorting key.
func endTimeToBytes(nanos int64) []byte {
	b := make([]byte, endTimeBytes)
	if nanos > 0 {
		binary.BigEndian.PutUint64(b, uint64(nanos))
	}

	return b
}

// endTimeIndexKey returns the bucketEndTimeToKey key for an archived job:
// 8-byte big-endian end-time UnixNano, then dbDelimiter, then its job key.
// Sorts chronologically as raw bytes.
func endTimeIndexKey(endNanos, jobKey []byte) []byte {
	key := make([]byte, 0, len(endNanos)+len(dbDelimiter)+len(jobKey))
	key = append(key, endNanos...)
	key = append(key, dbDelimiter...)

	return append(key, jobKey...)
}

// dropStaleEndTimeIndex recovers the key's prior end time from its existing
// bucketJobsComplete record (so it must run before that record is overwritten)
// and, if different from newNanos, deletes the prior forward index entry. It
// reports whether the index still needs the new entry written: false only when
// the prior end time equals newNanos (idempotent re-archive). A Get result is
// only valid until the next mutation, so oldEncoded is decoded immediately and
// only the extracted nanos kept; this is safe because we read bucketJobsComplete
// and mutate only bucketEndTimeToKey.
func (db *db) dropStaleEndTimeIndex(tx *bolt.Tx, jobKey []byte, newNanos int64) (bool, error) {
	oldEncoded := tx.Bucket(bucketJobsComplete).Get(jobKey)
	if len(oldEncoded) == 0 {
		return true, nil
	}

	oldJob, err := db.decodeJob(oldEncoded)
	if err != nil {
		return false, err
	}

	oldNanos := oldJob.EndTime.UnixNano()
	if oldNanos == newNanos {
		return false, nil
	}

	oldTimeBytes := endTimeToBytes(oldNanos)

	return true, tx.Bucket(bucketEndTimeToKey).Delete(endTimeIndexKey(oldTimeBytes, jobKey))
}

// decodeJob decodes a Job we previously stored in one of our job buckets, and
// applies the invariants a stored Job must satisfy before anything else sees it.
// Every read of a stored Job goes through here, so that a db written by an older
// wr cannot bring a cwd_matters job carrying a cleanup behaviour back into a
// running server (see Job.dropImpossibleCleanups).
func (db *db) decodeJob(encoded []byte) (*Job, error) {
	job := &Job{}

	if err := codec.NewDecoderBytes(encoded, db.ch).Decode(job); err != nil {
		return nil, err
	}

	job.dropImpossibleCleanups()

	return job, nil
}

// bestEffortWriter is the single long-lived goroutine that persists all queued
// best-effort change/exit updates. Each wake drains everything currently pending
// into ONE write transaction, so a mass state-change burst can neither spawn an
// unbounded number of write goroutines nor collapse into thousands of tiny
// fsync'd txns that starve the synchronous archive path (the reliable4 freeze).
// Started by initDB; stopped, after a final drain, by close() via
// stopBestEffortWriter.
func (db *db) bestEffortWriter(ctx context.Context) {
	defer close(db.beWriterDone)
	defer internal.LogPanic(ctx, "jobqueue database best-effort writer", true)

	for {
		select {
		case <-db.beStop:
			db.drainBestEffort(ctx)

			return
		case <-db.beSignal:
			db.drainBestEffort(ctx)
		}
	}
}

// kickBestEffortWriter wakes the writer to drain pending work. beSignal is
// buffered(1) so this never blocks the caller (which may hold db.Lock/wgMutex)
// and coalesced kicks are harmless: one drain persists everything pending.
func (db *db) kickBestEffortWriter() {
	select {
	case db.beSignal <- struct{}{}:
	default:
	}
}

// drainBestEffort persists every currently-pending best-effort write in a single
// transaction, then releases the batch's wg/exit tracking. Best-effort: a write
// error is logged, not returned.
func (db *db) drainBestEffort(ctx context.Context) {
	batch := db.swapBestEffort()
	if len(batch.wgkeys) == 0 {
		return
	}

	defer db.doneBestEffort(batch)

	if err := db.bolt.Update(batch.apply); err != nil {
		clog.Error(ctx, "Database best-effort job update failed", "err", err)

		return
	}

	// Only change-updates mark the db dirty for backup, exactly as the pre-fix
	// per-path code did (launchJobChangeUpdate set it; launchJobExitUpdate did
	// not); a periodic backup may lag the last exit-only writes, which is fine for
	// the DR snapshot (see bugfix 260727-2).
	if len(batch.changes) > 0 {
		db.backupDirty.Store(true)
	}
}

// swapBestEffort atomically takes ownership of all currently-pending best-effort
// work and resets the shared structures, so enqueuers keep appending (never
// blocking) while the writer persists the snapshot.
func (db *db) swapBestEffort() beBatch {
	db.beMu.Lock()
	defer db.beMu.Unlock()

	batch := beBatch{changes: db.beChanges, exits: db.beExits, wgkeys: db.beWGKeys}
	db.beChanges = make(map[string][]byte)
	db.beExits = nil
	db.beWGKeys = nil

	return batch
}

// doneBestEffort releases a drained batch's db.wg tracking and decrements the
// in-progress exit counter. It is deferred so it runs even if the write panicked
// or errored, so close()'s wg.Wait and waitForJobExitUpdates can never hang.
func (db *db) doneBestEffort(batch beBatch) {
	for _, wgk := range batch.wgkeys {
		db.wg.Done(wgk)
	}

	if len(batch.exits) > 0 {
		db.updatingAfterJobExit.Add(-int64(len(batch.exits)))
	}
}

// stopBestEffortWriter tells the writer to do a final drain and exit, then waits
// for it. Called once from finaliseBackup (close()), before the wg drain and
// final backup, so no queued change/exit write is lost on shutdown.
func (db *db) stopBestEffortWriter() {
	close(db.beStop)
	<-db.beWriterDone
}

// archiveOp is one caller's pending archive, waiting for the coalescing archive
// writer to persist it and hand back its own outcome.
type archiveOp struct {
	key     []byte
	encoded []byte
	job     *Job
	result  chan error // buffered(1): this caller's individual reply
	replied bool       // writer-only, so reply() is idempotent
}

// reply gives this archive's own outcome to its waiting caller, at most once.
// result is buffered and never closed, so this neither blocks the writer nor can
// ever send on a closed channel.
func (op *archiveOp) reply(err error) {
	if op.replied {
		return
	}

	op.replied = true
	op.result <- err
}

// archiveWriter is the single long-lived goroutine that persists archives. Each
// wake folds EVERY currently-pending archive into ONE write transaction and then
// replies to each waiter individually, so a deep archive queue drains in one
// commit rather than one commit per job (see the arMu comment on the db struct for
// the production failure this exists to prevent). Started by initDB; stopped,
// after a final drain, by close() via stopArchiveWriter.
func (db *db) archiveWriter(ctx context.Context) {
	defer close(db.arWriterDone)
	defer db.failPendingArchives()
	defer internal.LogPanic(ctx, "jobqueue database archive writer", true)

	for {
		select {
		case <-db.arStop:
			db.drainArchives(true)

			return
		case <-db.arSignal:
			db.drainArchives(false)
		}
	}
}

// enqueueArchive queues op for the archive writer and kicks it. It reports false
// if the writer has already made its final drain, so the caller is told the
// database is closed instead of waiting for a reply that can never come. arSignal
// is buffered(1), so the kick never blocks and coalesced kicks are harmless: one
// drain persists everything pending.
func (db *db) enqueueArchive(op *archiveOp) bool {
	db.arMu.Lock()

	if db.arStopped {
		db.arMu.Unlock()

		return false
	}

	db.arPending = append(db.arPending, op)
	db.arMu.Unlock()

	select {
	case db.arSignal <- struct{}{}:
	default:
	}

	return true
}

// swapArchives takes ownership of every currently-pending archive and resets the
// queue, so callers keep enqueuing (never blocking) while the writer persists the
// snapshot. When final is true it also latches the queue shut in the same critical
// section, so an archive submitted after the last drain is rejected rather than
// left waiting forever.
func (db *db) swapArchives(final bool) []*archiveOp {
	db.arMu.Lock()
	defer db.arMu.Unlock()

	ops := db.arPending
	db.arPending = nil

	if final {
		db.arStopped = true
	}

	return ops
}

// drainArchives persists every currently-pending archive, all of them in ONE write
// transaction, and replies to each waiter with its own outcome.
func (db *db) drainArchives(final bool) {
	ops := db.swapArchives(final)
	if len(ops) == 0 {
		return
	}

	db.applyArchives(ops)

	// mark the db dirty for the backup ticker exactly as the pre-fix per-archive
	// path did: unconditionally, after the write attempt.
	db.backupDirty.Store(true)
}

// applyArchives folds ops into ONE write transaction and replies to each waiter
// individually. It mirrors bbolt.Batch's per-caller error semantics, so one bad
// job cannot fail its batch-mates: an archive that fails the shared transaction is
// taken out of it and the rest are retried together, then each removed archive is
// re-run in a transaction of its own so its caller gets its own error. The loop
// always terminates because every pass either replies to everything left or
// removes one op.
func (db *db) applyArchives(ops []*archiveOp) {
	var solo []*archiveOp

	for len(ops) > 0 {
		failed := -1

		err := db.archiveTx(ops, &failed)
		if failed >= 0 {
			solo = append(solo, ops[failed])
			ops[failed], ops = ops[len(ops)-1], ops[:len(ops)-1]

			continue
		}

		for _, op := range ops {
			op.reply(err)
		}

		break
	}

	for _, op := range solo {
		op.reply(db.archiveTx([]*archiveOp{op}, nil))
	}
}

// archiveTx applies every op's archive within one write transaction. If an op's
// own work fails, its index is recorded in failed (when non-nil) and the whole
// transaction is rolled back, so the caller can take that op out and retry the
// rest.
func (db *db) archiveTx(ops []*archiveOp, failed *int) error {
	return db.bolt.Update(func(tx *bolt.Tx) error {
		for i, op := range ops {
			if err := db.applyArchiveOp(tx, op); err != nil {
				if failed != nil {
					*failed = i
				}

				return err
			}
		}

		return nil
	})
}

// applyArchiveOp applies one archive within tx, turning a panic into that
// archive's own error. bbolt.Batch did this (safelyCall) and db.bolt.Update does
// not, so without it a single malformed job would take the whole manager down
// instead of failing one archive; the transaction is rolled back either way.
func (db *db) applyArchiveOp(tx *bolt.Tx, op *archiveOp) (err error) {
	defer func() {
		if p := recover(); p != nil {
			err = fmt.Errorf("%w: %v", errArchivePanic, p)
		}
	}()

	return db.archiveJobTx(tx, op.key, op.encoded, op.job)
}

// failPendingArchives latches the archive queue shut and fails anything still in
// it. The normal shutdown drain (drainArchives(true)) leaves nothing to do here;
// this is the safety net for a writer that stopped without it, so no archive
// caller can be left waiting forever.
func (db *db) failPendingArchives() {
	for _, op := range db.swapArchives(true) {
		op.reply(errDBClosed)
	}
}

// stopArchiveWriter tells the archive writer to do a final drain and exit, then
// waits for it. Called once from finaliseBackup (close()), before the wg drain and
// final backup, so no queued archive is lost on shutdown.
func (db *db) stopArchiveWriter() {
	close(db.arStop)
	<-db.arWriterDone
}

// retrieveOldestCompleteJobsByRepGroup is retrieveCompleteJobsByRepGroup with the
// caller's limit pushed down: it walks the same RTK cursor, but decodes only each
// record's archivedJobFacets, and fully decodes just the oldest-started
// query.limit(group) of each group. On the production database that is the
// difference between materialising 2.15M Jobs (a 12.1GB heap excursion) and
// materialising as many as the request can actually return.
func (db *db) retrieveOldestCompleteJobsByRepGroup(repgroup string,
	query completeJobsQuery) (*completeJobsPage, error) {
	page := &completeJobsPage{
		fetched: make(map[string]int),
		counted: make(map[string]int),
	}

	err := db.bolt.View(func(tx *bolt.Tx) error {
		selected, err := db.selectOldestArchivedJobs(tx, repgroup, query, page.counted)
		if err != nil {
			return err
		}

		return db.decodeArchivedJobs(tx, selected, page)
	})

	return page, err
}

// selectOldestArchivedJobs walks repgroup's archived records, counting them per
// group in counted, and returns the keys of the oldest-started query.limit(group)
// of each group, oldest-started first overall.
func (db *db) selectOldestArchivedJobs(tx *bolt.Tx, repgroup string,
	query completeJobsQuery, counted map[string]int) ([]archivedJobCandidate, error) {
	newJobBucket := tx.Bucket(bucketJobsLive)
	completeJobBucket := tx.Bucket(bucketJobsComplete)
	cursor := tx.Bucket(bucketRTK).Cursor()
	scan := &archivedScan{
		query:     query,
		counted:   counted,
		selectors: make(map[string]*oldestArchivedJobs),
		decoder:   codec.NewDecoderBytes(nil, db.ch),
	}

	prefix := []byte(repgroup + dbDelimiter)
	for k, _ := cursor.Seek(prefix); bytes.HasPrefix(k, prefix); k, _ = cursor.Next() {
		key := bytes.TrimPrefix(k, prefix)

		encoded := completeJobBucket.Get(key)
		if len(encoded) == 0 || newJobBucket.Get(key) != nil {
			continue
		}

		if err := scan.consider(key, encoded); err != nil {
			return nil, err
		}
	}

	return mergeOldestArchivedJobs(scan.selectors), nil
}

// mergeOldestArchivedJobs flattens the per-group selections into the single
// oldest-started-first order retrieveCompleteJobsByRepGroup's caller sorts a
// RepGroup's whole history into.
func mergeOldestArchivedJobs(selectors map[string]*oldestArchivedJobs) []archivedJobCandidate {
	var merged []archivedJobCandidate

	for _, selector := range selectors {
		merged = append(merged, selector.oldest()...)
	}

	// NOTE: this cross-group sort is presentation only, and is NOT a protected
	// guarantee: each selector's output is already oldest-first, and limitJobs
	// re-groups the page by the same key the selectors are keyed on, so removing
	// the sort changes no answer. It is here so that a page reads like the
	// unbounded fetch it stands in for (which sorts the same way), and so that a
	// future caller of this that does not re-group is not silently handed a
	// map-ordered list.
	sort.SliceStable(merged, func(i, j int) bool {
		return startedBefore(merged[i].start, merged[i].end, merged[j].start, merged[j].end)
	})

	return merged
}

// decodeArchivedJobs fully decodes the selected candidates into page.jobs,
// recording how many of them fell in each group.
func (db *db) decodeArchivedJobs(tx *bolt.Tx, selected []archivedJobCandidate, page *completeJobsPage) error {
	newJobBucket := tx.Bucket(bucketJobsLive)
	completeJobBucket := tx.Bucket(bucketJobsComplete)
	page.jobs = make([]*Job, 0, len(selected))

	for i := range selected {
		job, err := db.decodeArchivedJob(completeJobBucket, newJobBucket, selected[i].key)
		if err != nil {
			return err
		}

		if job == nil {
			// unreachable: selectOldestArchivedJobs applied decodeArchivedJob's two
			// guards (a non-empty complete record, and no live record under the same
			// key) to this very key, in this very bolt.View - and a View is a
			// consistent snapshot, so no writer can have changed either bucket since.
			// The count is undone rather than merely skipped so that this cannot be
			// silently WRONG if that ever stops holding: a candidate that yields no
			// job must leave counted as well as fetched, or the difference between
			// them would add a phantom to its group's Similar.
			page.counted[selected[i].group]--

			continue
		}

		page.jobs = append(page.jobs, job)
		page.fetched[selected[i].group]++
	}

	return nil
}

// deleteLimitGroup deletes a limit group's stored value if it had one.
func deleteLimitGroup(b *bolt.Bucket, key, existing []byte) (limitGroupOutcome, error) {
	if existing == nil {
		return limitGroupUnchanged, nil
	}

	if err := b.Delete(key); err != nil {
		return limitGroupUnchanged, err
	}

	return limitGroupRemoved, nil
}

// dbUpgradeReporter reports a one-off database upgrade (the index rebuilds
// initDB does the first time it opens a database an older wr wrote) to both the
// status sidecar `wr manager start` polls and the manager log.
//
// Its milestones - the upgrade starting, each phase starting and completing, and
// the upgrade finishing - are logged at warn, not info, because the log handler
// the manager hands the server is warn-filtered unless --debug is given
// (cmd.setupManagerLogging), and the upgrade runs before the manager serves
// anything: with nothing in the log, a rebuild that takes minutes on a large
// database is indistinguishable from a hang (.docs/bugfixes/260825-3.md item 2).
// That is the same reason prior-state recovery's milestones are warns - see
// recoverPriorJobsAndNote.
//
// Per-entry progress stays at info, ie. --debug only, because a real rebuild
// processes millions of entries and this log has to stay readable (53c323f).
// Alongside it, at most one "still running" line per dbUpgradeLogInterval is
// logged at warn, so that a phase long enough to worry an operator visibly moves
// while the default log stays proportional to the phase's duration rather than
// to how many entries it processed.
type dbUpgradeReporter struct {
	dbFile string
	info   func(string, ...any)
	warn   func(string, ...any)
	// now supplies the time the "still running" rate limit measures, and nothing
	// else (the sidecar's own pacing keeps its own clock). It is time.Now in
	// production, and a test seam only so the rate limit can be pinned exactly,
	// without depending on how long a loaded host takes to run a rebuild.
	now            func() time.Time
	startedAt      time.Time
	lastWrite      time.Time
	lastLog        time.Time
	started        bool
	phaseActive    bool
	phaseStartedAt time.Time
	phaseState     string
	phaseDetail    string
	phaseProcessed int
}

func newDBUpgradeReporter(ctx context.Context, dbFile string) *dbUpgradeReporter {
	warn := func(msg string, args ...any) {
		clog.Warn(ctx, msg, args...)
	}
	info := func(msg string, args ...any) {
		clog.Info(ctx, msg, args...)
	}

	if err := internal.RemoveDBUpgradeStatus(dbFile); err != nil {
		warn("failed to remove stale database upgrade status", "path", internal.DBUpgradeStatusPath(dbFile), "err", err)
	}

	return &dbUpgradeReporter{
		dbFile:    dbFile,
		info:      info,
		warn:      warn,
		now:       time.Now,
		startedAt: time.Now(),
	}
}

func (r *dbUpgradeReporter) active() bool {
	return r != nil && r.started
}

func (r *dbUpgradeReporter) startPhase(state, detail string) {
	if r == nil {
		return
	}

	if !r.started {
		r.started = true
		r.warn("database upgrade started", "db", r.dbFile)
	}

	r.phaseActive = true
	r.phaseStartedAt = time.Now()
	r.phaseState = state
	r.phaseDetail = detail
	r.phaseProcessed = 0
	// the phase's own start line is the first thing an operator sees of it, so
	// the rate limit's interval runs from here: a phase shorter than one interval
	// says nothing more.
	r.lastLog = r.now()

	r.writeStatus(state, detail, 0)
	r.warn("database upgrade step started", "state", state, "detail", detail, "processed", 0)
}

func (r *dbUpgradeReporter) progress(state, detail string, processed int) {
	if !r.progressDue(processed) {
		return
	}

	r.writeProgress(state, detail, processed)
}

func (r *dbUpgradeReporter) writeProgress(state, detail string, processed int) {
	if r == nil {
		return
	}

	r.phaseDetail = detail
	r.phaseProcessed = processed

	r.writeStatus(state, detail, processed)
	r.logProgress(state, detail, processed)
}

// logProgress logs every progress point at info, which only --debug records, and
// additionally reports at warn, at most once per dbUpgradeLogInterval, that the
// phase is still running and how far it has got. The warn line is what a default
// `wr manager start` shows, and it carries its own message so that a default-level
// log makes plain it is a sample taken every dbUpgradeLogInterval and not one
// line per progress point (the same reason recovery's heartbeat has its own
// message - see startRecoveryHeartbeat).
func (r *dbUpgradeReporter) logProgress(state, detail string, processed int) {
	r.info("database upgrade progress", "state", state, "detail", detail, "processed", processed)

	now := r.now()
	if now.Sub(r.lastLog) < dbUpgradeLogInterval {
		return
	}

	r.lastLog = now

	r.warn("database upgrade step still running", "state", state, "detail", detail, "processed", processed,
		"took", time.Since(r.phaseStartedAt))
}

func (r *dbUpgradeReporter) progressDue(processed int) bool {
	return r != nil && r.started &&
		(processed%dbUpgradeProgressEntries == 0 || time.Since(r.lastWrite) >= dbUpgradeProgressInterval)
}

func (r *dbUpgradeReporter) completePhase(state, detail string, processed int) {
	if r == nil || !r.phaseActive {
		return
	}

	if state == "" {
		state = r.phaseState
	}

	if detail == "" {
		detail = r.phaseDetail
	}

	r.phaseActive = false
	r.phaseState = state
	r.phaseDetail = detail
	r.phaseProcessed = processed

	r.writeStatus(state, detail, processed)
	r.warn("database upgrade step complete", "state", state, "detail", detail,
		"processed", processed, "took", time.Since(r.phaseStartedAt))
}

func (r *dbUpgradeReporter) writeStatus(state, detail string, processed int) {
	status := internal.DBUpgradeStatus{
		State:     state,
		Detail:    detail,
		Processed: processed,
		StartedAt: r.startedAt,
	}

	if err := internal.WriteDBUpgradeStatus(r.dbFile, status); err != nil {
		r.warn("failed to write database upgrade status", "path", internal.DBUpgradeStatusPath(r.dbFile), "err", err)
	}

	r.lastWrite = time.Now()
}

func (r *dbUpgradeReporter) finish(upgradeErr error) {
	if !r.active() {
		return
	}

	if upgradeErr != nil {
		r.warn("database upgrade failed", "db", r.dbFile, "state", r.phaseState, "err", upgradeErr,
			"took", time.Since(r.startedAt))
	} else {
		r.completePhase(r.phaseState, r.phaseDetail, r.phaseProcessed)
		r.warn("database upgrade complete", "db", r.dbFile, "took", time.Since(r.startedAt))
	}

	if err := internal.RemoveDBUpgradeStatus(r.dbFile); err != nil {
		r.warn("failed to remove database upgrade status", "path", internal.DBUpgradeStatusPath(r.dbFile), "err", err)
	}
}

func rebuildDepGroupEntries(depGroupBucket, lookupBucket *bolt.Bucket, progress *dbUpgradeReporter) (int, error) {
	processed := 0
	err := lookupBucket.ForEach(func(k, _ []byte) error {
		processed++
		if progress.progressDue(processed) {
			progress.progress(internal.DBUpgradeDepGroupIndexState,
				fmt.Sprintf("rebuilding database dependency-group index (%d entries processed)", processed),
				processed)
		}

		return putDepGroupFromLookupKey(depGroupBucket, k)
	})

	return processed, err
}

func putDepGroupFromLookupKey(depGroupBucket *bolt.Bucket, lookupKey []byte) error {
	idx := bytes.Index(lookupKey, []byte(dbDelimiter))
	if idx <= 0 {
		return nil
	}

	return depGroupBucket.Put(lookupKey[:idx], nil)
}

// CompactDBFile is the exported entry point for the offline `wr manager compact`
// subcommand (spec D2). It compacts the BoltDB at dbFile, reclaiming the free
// pages left by churn, and returns the file size (bytes) before and after.
//
// It opens the source with the map freelist, compacts (bolt.Compact) into a
// temporary file in the SAME directory as dbFile, then atomically replaces the
// original with os.Rename (same directory => same filesystem => atomic rename).
// On any error the original file is left untouched and the temporary file is
// removed, so a failed compaction never corrupts or partially overwrites the
// database. The manager MUST be stopped: BoltDB permits only one process to open
// the file at a time, so the subcommand refuses to run while a manager is up.
func CompactDBFile(dbFile string) (beforeSize, afterSize int64, err error) {
	beforeInfo, err := os.Stat(dbFile)
	if err != nil {
		return 0, 0, err
	}

	beforeSize = beforeInfo.Size()

	tmpPath, afterSize, err := compactToTempFile(dbFile)
	if err != nil {
		if tmpPath != "" {
			_ = os.Remove(tmpPath)
		}

		return beforeSize, 0, err
	}

	if err = os.Rename(tmpPath, dbFile); err != nil {
		_ = os.Remove(tmpPath)

		return beforeSize, afterSize, err
	}

	return beforeSize, afterSize, nil
}

// compactToTempFile compacts the BoltDB at dbFile into a fresh temporary file in
// the SAME directory (so CompactDBFile's later os.Rename onto dbFile is an atomic
// same-filesystem rename), returning the temp file's path and its (compacted)
// size. On error it returns the temp path (if one was created) so the caller can
// remove it, leaving the original database untouched.
func compactToTempFile(dbFile string) (tmpPath string, afterSize int64, err error) {
	tmp, err := os.CreateTemp(filepath.Dir(dbFile), filepath.Base(dbFile)+".compact-*")
	if err != nil {
		return "", 0, err
	}

	tmpPath = tmp.Name()
	if cerr := tmp.Close(); cerr != nil {
		return tmpPath, 0, cerr
	}

	if err = compactBoltInto(tmpPath, dbFile); err != nil {
		return tmpPath, 0, err
	}

	afterInfo, err := os.Stat(tmpPath)
	if err != nil {
		return tmpPath, 0, err
	}

	return tmpPath, afterInfo.Size(), nil
}

// compactBoltInto opens srcPath (map freelist) and the empty dstPath and copies a
// compacted image of the source into the destination via bolt.Compact, closing
// both handles before returning.
func compactBoltInto(dstPath, srcPath string) (err error) {
	// a bounded timeout so, if the up-check was fooled and a manager still holds
	// the source file lock, this errors cleanly instead of blocking forever (the
	// dst is a fresh temp file, but it uses the same options for consistency).
	src, err := bolt.Open(srcPath, dbFilePermission,
		&bolt.Options{FreelistType: bolt.FreelistMapType, Timeout: offlineDBOpenTimeout})
	if err != nil {
		return err
	}

	defer func() {
		if cerr := src.Close(); cerr != nil && err == nil {
			err = cerr
		}
	}()

	dst, err := bolt.Open(dstPath, dbFilePermission,
		&bolt.Options{FreelistType: bolt.FreelistMapType, Timeout: offlineDBOpenTimeout})
	if err != nil {
		return err
	}

	defer func() {
		if cerr := dst.Close(); cerr != nil && err == nil {
			err = cerr
		}
	}()

	return bolt.Compact(dst, src, compactTxMaxSize)
}

// openManagerBolt opens one of the manager's BoltDB files, bounding the wait for
// its file lock at managerDBOpenTimeout so a second manager fails with
// ErrDBLocked instead of blocking forever and then acquiring the database the
// instant the winner exits (spec E7).
func openManagerBolt(path string) (*bolt.DB, error) {
	return bolt.Open(path, dbFilePermission, &bolt.Options{
		FreelistType: bolt.FreelistMapType,
		Timeout:      managerDBOpenTimeout,
	})
}

// putLimitGroup stores a non-negative limit for a group.
func putLimitGroup(b *bolt.Bucket, key []byte, limit int64) error {
	v := make([]byte, limitGroupBytes)
	binary.BigEndian.PutUint64(v, uint64(limit)) //nolint:gosec // limit is >= 0 here, so fits in a uint64

	return b.Put(key, v)
}

// retrieveLimitGroup gets a value for a particular group from the db that was
// stored with storeLimitGroups(). If the group wasn't stored, returns an
// invalid limiter.GroupData (mode = 0).
func (db *db) retrieveLimitGroup(ctx context.Context, group string) *limiter.GroupData {
	if _, gd := limiter.NameToGroupData(group); gd.IsValid() && !gd.IsCount() {
		return gd
	}

	v := db.retrieve(ctx, bucketLGs, group)
	if v == nil {
		return limiter.NewCountGroupData(-1)
	}

	return limiter.NewCountGroupData(int64(binary.BigEndian.Uint64(v))) //nolint:gosec
}

// storeNewJobs stores jobs in the live bucket, where they will only be used for
// disaster recovery. It also stores a lookup from the Job.RepGroup to the Job's
// key, and since this is independent, and we call this prior to checking for
// dups, we allow the same job to be looked up by multiple RepGroups. Likewise,
// we store a lookup for the Job.DepGroups and .Dependencies.DepGroups().
//
// If ignoreAdded is true, jobs that have already completed will be ignored and
// the returned alreadyAdded value will increase. Callers should filter jobs
// known to be in the live queue before calling; live DB rows that did not reach
// the queue are then recovered through the normal queue add path.
//
// While storing it also checks if any previously stored jobs depend on a dep
// group that an input job is a member of. If not, jobsToQueue return value will
// be identical to the input job slice (minus any jobs ignored due to being
// complete). Otherwise, if the affected job was Archive()d (and not currently
// being re-run), then it will be returned in jobsToQueue alongside those same
// input jobs. If the affected job was in the live bucket (currently queued), it
// will be returned in the jobsToUpdate slice: you should use queue methods to
// update the job in the queue.
//
// Finally, it triggers a background database backup.
func (db *db) storeNewJobs(ctx context.Context, jobs []*Job, ignoreAdded bool) (
	jobsToQueue, jobsToUpdate []*Job, alreadyAdded int, err error,
) {
	encodedJobs, rgLookups, depGroupsSeen, rdgLookups, rgs,
		jobsToQueue, jobsToUpdate, alreadyAdded, err := db.prepareNewJobs(jobs, ignoreAdded)
	if err != nil {
		return jobsToQueue, jobsToUpdate, alreadyAdded, err
	}

	if len(encodedJobs) > 0 {
		err = db.storeNewJobData(ctx, encodedJobs, rgLookups, depGroupsSeen, rdgLookups, rgs)
	}

	// *** on error, because we were batching, and doing lookups separately to
	// each other and jobs, we should go through and remove anything we did
	// manage to add... (but this isn't so critical, since on failure here,
	// they are not added to the in-memory queue and user gets an error and they
	// would try to add everything back again; conversely, if we try to retrieve
	// non-existent jobs based on lookups that shouldn't be there, they are
	// silently skipped)

	if err == nil && alreadyAdded != len(jobs) {
		db.backupDirty.Store(true)
	}

	return jobsToQueue, jobsToUpdate, alreadyAdded, err
}

// batchStore describes one bucket-store to carry out concurrently in
// storeNewJobData.
type batchStore struct {
	label   string
	bucket  []byte
	encodes sobsd
	storer  sobsdStorer
}

// storeNewJobData concurrently stores the job and lookup data prepared by
// prepareNewJobs, returning the first error encountered. rgLookups and
// encodedJobs are always stored; the other lookups only if non-empty.
func (db *db) storeNewJobData(ctx context.Context, encodedJobs, rgLookups,
	depGroupsSeen, rdgLookups, rgs sobsd) error {
	stores := []batchStore{{"rglookups", bucketRTK, rgLookups, db.storeLookups}}

	for _, s := range []batchStore{
		{"repGroups", bucketRGs, rgs, db.storeLookups},
		{"depGroupsSeen", bucketDepGroups, depGroupsSeen, db.storeLookups},
		{"rdgLookups", bucketRDTK, rdgLookups, db.storeLookups},
	} {
		if len(s.encodes) > 0 {
			stores = append(stores, s)
		}
	}

	stores = append(stores, batchStore{"encodedJobs", bucketJobsLive, encodedJobs, db.storeEncodedJobs})

	errs := make(chan error, len(stores))

	db.wgMutex.Lock()
	for _, s := range stores {
		db.launchBatchStore(ctx, s, errs)
	}
	db.wgMutex.Unlock()

	return collectFirstError(errs, len(stores))
}

// launchBatchStore launches a goroutine that sorts and stores one batchStore,
// sending the result to errs. Must be called with db.wgMutex held.
func (db *db) launchBatchStore(ctx context.Context, s batchStore, errs chan<- error) {
	wgk := db.wg.Add(1)

	go func() {
		defer internal.LogPanic(ctx, "jobqueue database storeNewJobs "+s.label, true)
		defer db.wg.Done(wgk)

		sort.Sort(s.encodes)

		errs <- db.storeBatched(s.bucket, s.encodes, s.storer)
	}()
}

// collectFirstError reads n results from errs, returning the last non-nil error
// seen (matching the original storeNewJobs behaviour) and closing errs.
func collectFirstError(errs chan error, n int) error {
	var err error

	seen := 0

	for thisErr := range errs {
		if thisErr != nil {
			err = thisErr
		}

		seen++
		if seen == n {
			close(errs)

			break
		}
	}

	return err
}

//nolint:gocognit,gocyclo,cyclop,funlen,lll,nestif // Legacy persistence path coordinates several lookup buckets.
func (db *db) prepareNewJobs(jobs []*Job, ignoreAdded bool) (encodedJobs, rgLookups, depGroupsSeen, rdgLookups, rgs sobsd, jobsToQueue []*Job, jobsToUpdate []*Job, alreadyAdded int, err error) {
	// turn the jobs in to sobsd and sort by their keys, likewise for the
	// lookups
	repGroups := make(map[string]bool)
	depGroups := make(map[string]bool)
	newJobKeys := make(map[string]bool)

	var keptJobs []*Job

	for _, job := range jobs {
		keyStr := job.Key()

		if ignoreAdded {
			var added bool

			added, err = db.checkIfComplete(keyStr)
			if err != nil {
				return encodedJobs, rgLookups, depGroupsSeen, rdgLookups, rgs,
					jobsToQueue, jobsToUpdate, alreadyAdded, err
			}

			if added {
				alreadyAdded++

				continue
			}

			keptJobs = append(keptJobs, job)
		}

		newJobKeys[keyStr] = true
		key := []byte(keyStr)

		job.RLock()
		rgLookups = append(rgLookups, [2][]byte{db.generateLookupKey(job.RepGroup, key), nil})
		repGroups[job.RepGroup] = true

		for _, depGroup := range job.DepGroups {
			if depGroup != "" {
				depGroups[depGroup] = true
			}
		}

		for _, depGroup := range job.Dependencies.DepGroups() {
			rdgLookups = append(rdgLookups, [2][]byte{db.generateLookupKey(depGroup, key), nil})
		}

		job.RUnlock()

		var encoded []byte

		enc := codec.NewEncoderBytes(&encoded, db.ch)

		job.RLock()
		err = enc.Encode(job)
		job.RUnlock()

		if err != nil {
			return encodedJobs, rgLookups, depGroupsSeen, rdgLookups, rgs,
				jobsToQueue, jobsToUpdate, alreadyAdded, err
		}

		encodedJobs = append(encodedJobs, [2][]byte{key, encoded})
	}

	if len(encodedJobs) > 0 {
		if !ignoreAdded {
			keptJobs = jobs
		}

		// first determine if any of these new jobs are the parent of previously
		// stored jobs
		if len(depGroups) > 0 {
			jobsToQueue, jobsToUpdate, err = db.retrieveDependentJobs(depGroups, newJobKeys)

			// arrange to have resurrected complete jobs stored in the live
			// bucket again
			for _, job := range jobsToQueue {
				key := []byte(job.Key())

				var encoded []byte

				enc := codec.NewEncoderBytes(&encoded, db.ch)

				job.RLock()
				err = enc.Encode(job)
				job.RUnlock()

				if err != nil {
					return encodedJobs, rgLookups, depGroupsSeen, rdgLookups, rgs,
						jobsToQueue, jobsToUpdate, alreadyAdded, err
				}

				encodedJobs = append(encodedJobs, [2][]byte{key, encoded})
			}
		}

		// jobsToQueue now holds any resurrected archived dependents, to which we
		// add the input jobs we actually stored. It must be keptJobs and not
		// jobs: an input job that checkIfComplete ruled out was never encoded,
		// so queueing it would run it while it is absent from the live bucket.
		jobsToQueue = append(jobsToQueue, keptJobs...)

		for rg := range repGroups {
			rgs = append(rgs, [2][]byte{[]byte(rg), nil})
		}

		for depGroup := range depGroups {
			depGroupsSeen = append(depGroupsSeen, [2][]byte{[]byte(depGroup), nil})
		}
	}

	return encodedJobs, rgLookups, depGroupsSeen, rdgLookups, rgs, jobsToQueue, jobsToUpdate, alreadyAdded, err
}

// generateLookupKey creates a lookup key understood by the retrieval methods,
// concatenating prefix with a delimiter and the job key.
func (db *db) generateLookupKey(prefix string, jobKey []byte) []byte {
	key := make([]byte, 0, len(prefix)+len(dbDelimiter)+len(jobKey))
	key = append(key, prefix...)
	key = append(key, dbDelimiter...)

	return append(key, jobKey...)
}

// checkIfLive tells you if a job with the given key is currently in the live
// bucket.
func (db *db) checkIfLive(key string) (bool, error) {
	var isLive bool

	err := db.bolt.View(func(tx *bolt.Tx) error {
		isLive = checkIfLiveTx(tx, key)

		return nil
	})

	return isLive, err
}

// checkIfLiveTx is checkIfLive using the given read transaction.
func checkIfLiveTx(tx *bolt.Tx, key string) bool {
	return tx.Bucket(bucketJobsLive).Get([]byte(key)) != nil
}

// checkIfComplete tells you if a job with the given key is currently in the
// complete bucket.
func (db *db) checkIfComplete(key string) (bool, error) {
	var isComplete bool

	err := db.bolt.View(func(tx *bolt.Tx) error {
		completeJobBucket := tx.Bucket(bucketJobsComplete)
		isComplete = completeJobBucket.Get([]byte(key)) != nil

		return nil
	})

	return isComplete, err
}

// archiveJob deletes a job from the live bucket, and adds a new version of it
// (with different properties) to the complete bucket.
//
// Also does what updateJobAfterExit does, except for the storage of any new
// stdout/err.
//
// The key you supply must be the key of the job you supply, or bad things will
// happen - no checking is done! The db is marked dirty afterwards, so the
// backup ticker will back it up (this path takes no db lock).
//
// The job is encoded here, OUTSIDE any transaction, and then handed to the single
// coalescing archiveWriter, which folds it in with every other archive pending at
// that moment into ONE db.Update and replies here with this archive's own outcome.
// That keeps the per-job CPU (encoding) off the one bolt write lock and makes a
// deep archive queue cost one commit rather than one commit per job; see the arMu
// comment on the db struct. This call blocks until its own archive is persisted,
// exactly as the previous db.bolt.Batch did.
func (db *db) archiveJob(ctx context.Context, key string, job *Job) error {
	var encoded []byte

	enc := codec.NewEncoderBytes(&encoded, db.ch)

	job.RLock()
	err := enc.Encode(job)
	job.RUnlock()

	if err != nil {
		return err
	}

	op := &archiveOp{
		key:     []byte(key),
		encoded: encoded,
		job:     job,
		result:  make(chan error, 1),
	}

	if !db.enqueueArchive(op) {
		return errDBClosed
	}

	return <-op.result
}

// putJobStats records a completed job's peak RAM, peak disk and runtime in
// their per-ReqGroup stat buckets.
func putJobStats(tx *bolt.Tx, job *Job) error {
	if err := putJobStat(tx.Bucket(bucketJobRAM), job.ReqGroup, job.PeakRAM); err != nil {
		return err
	}

	if err := putJobStat(tx.Bucket(bucketJobDisk), job.ReqGroup, int(job.PeakDisk)); err != nil {
		return err
	}

	if job.EndTime.IsZero() {
		return nil
	}

	duration := job.EndTime.Sub(job.StartTime)
	if duration <= 0 {
		return nil
	}

	secs := int(math.Ceil(duration.Seconds()))

	return putJobStat(tx.Bucket(bucketJobSecs), job.ReqGroup, secs)
}

// putJobStat stores a single per-ReqGroup stat value, keyed so it sorts within
// the ReqGroup.
func putJobStat(b *bolt.Bucket, reqGroup string, val int) error {
	return b.Put(fmt.Appendf(nil, "%s%s%20d", reqGroup, dbDelimiter, val), []byte(strconv.Itoa(val)))
}

// updateRGEndTime records job.EndTime as its RepGroup's end time, unless a later
// end time is already stored.
func updateRGEndTime(b *bolt.Bucket, job *Job) error {
	rgKey := []byte(job.RepGroup)
	newUnix := job.EndTime.Unix()
	existing := b.Get(rgKey)

	//nolint:gosec // stored value is a non-negative unix time written by us
	if len(existing) == rgEndTimeBytes && int64(binary.BigEndian.Uint64(existing)) >= newUnix {
		return nil
	}

	val := make([]byte, rgEndTimeBytes)
	binary.BigEndian.PutUint64(val, uint64(newUnix)) //nolint:gosec // unix time is non-negative

	return b.Put(rgKey, val)
}

// deleteLiveJobs remove multiple jobs from the live bucket.
func (db *db) deleteLiveJobs(ctx context.Context, keys []string) error {
	err := db.bolt.Batch(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketJobsLive)
		for _, key := range keys {
			errd := b.Delete([]byte(key))
			if errd != nil {
				return errd
			}
		}

		return nil
	})
	if err != nil {
		return err
	}

	db.backupDirty.Store(true)
	// *** we're not removing the lookup entries from the bucket*TK buckets, or
	// their reverse entries, because the lookup buckets are historical.

	return nil
}

// recoverIncompleteJobs returns all jobs in the live bucket, for use when
// restarting the server, allowing you start working on any jobs that were
// stored with storeNewJobs() but not yet archived with archiveJob().
//
// Note that you will get back the job as it was in its last recorded state.
// The state is recorded when a job starts to run, when it exits, and when it
// is kicked.
func (db *db) recoverIncompleteJobs() ([]*Job, error) {
	var jobs []*Job

	err := db.bolt.View(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketJobsLive)

		return b.ForEach(func(_, encoded []byte) error {
			if encoded != nil {
				job, errf := db.decodeJob(encoded)
				if errf != nil {
					return errf
				}

				jobs = append(jobs, job)
			}

			return nil
		})
	})

	return jobs, err
}

// retrieveCompleteJobsByKeys gets jobs with the given keys from the completed
// jobs bucket (ie. those that have gone through the queue and been Remove()d).
func (db *db) retrieveCompleteJobsByKeys(keys []string) ([]*Job, error) {
	var jobs []*Job

	err := db.bolt.View(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketJobsComplete)
		for _, key := range keys {
			encoded := b.Get([]byte(key))
			if encoded != nil {
				job, err := db.decodeJob(encoded)
				if err == nil {
					jobs = append(jobs, job)
				}
			}
		}

		return nil
	})

	return jobs, err
}

// retrieveRepGroups gets the rep groups of all jobs that have ever been added.
func (db *db) retrieveRepGroups() ([]string, error) {
	var rgs []string

	err := db.bolt.View(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketRGs)

		return b.ForEach(func(k, _ []byte) error {
			rgs = append(rgs, string(k))

			return nil
		})
	})

	return rgs, err
}

// repGroupHasHistory says if repGroup has any archived (complete) job, ie. if it
// has completion history in the database.
//
// archiveJobTx always records a RepGroup's end time (updateRGEndTime), so this is
// a single O(log n) B+tree Get: it answers "did anything in this RepGroup ever
// complete?" without the unbounded cursor scan and codec decode of every one of
// those jobs that actually fetching them costs (see
// repGroupOptions.IncludeComplete).
func (db *db) repGroupHasHistory(repGroup string) bool {
	has := false

	if err := db.bolt.View(func(tx *bolt.Tx) error {
		has = len(tx.Bucket(bucketRGEndTime).Get([]byte(repGroup))) == rgEndTimeBytes

		return nil
	}); err != nil {
		return false
	}

	return has
}

// retrieveLastCompletionTimeByRepGroup gets the latest archived completion
// time for each supplied RepGroup as UTC instants.
func (db *db) retrieveLastCompletionTimeByRepGroup(repGroups []string) (map[string]time.Time, error) {
	completionTimes := make(map[string]time.Time)

	err := db.bolt.View(func(tx *bolt.Tx) error {
		bucket := tx.Bucket(bucketRGEndTime)

		for _, repGroup := range repGroups {
			encoded := bucket.Get([]byte(repGroup))
			if len(encoded) != rgEndTimeBytes {
				continue
			}

			completionTimes[repGroup] = time.Unix(int64(binary.BigEndian.Uint64(encoded)), 0).UTC() //nolint:gosec
		}

		return nil
	})

	return completionTimes, err
}

// retrieveCompleteJobsByRepGroup gets jobs with the given RepGroup from the
// completed jobs bucket (ie. those that have gone through the queue and been
// Archive()d), but not those that are also currently live (ie. are being
// re-run).
func (db *db) retrieveCompleteJobsByRepGroup(repgroup string) ([]*Job, error) {
	var jobs []*Job

	err := db.bolt.View(func(tx *bolt.Tx) error {
		newJobBucket := tx.Bucket(bucketJobsLive)
		completeJobBucket := tx.Bucket(bucketJobsComplete)
		lookupBucket := tx.Bucket(bucketRTK).Cursor()

		prefix := []byte(repgroup + dbDelimiter)
		for k, _ := lookupBucket.Seek(prefix); bytes.HasPrefix(k, prefix); k, _ = lookupBucket.Next() {
			key := bytes.TrimPrefix(k, prefix)

			job, err := db.decodeArchivedJob(completeJobBucket, newJobBucket, key)
			if err != nil {
				return err
			}

			if job != nil {
				jobs = append(jobs, job)
			}
		}

		return nil
	})

	return jobs, err
}

// spendArchivedBytesByRepGroup charges budget for every archived record
// retrieveCompleteJobsByRepGroup would decode for this RepGroup, WITHOUT decoding
// any of them: it walks the same index and reads each record's encoded length,
// which costs no allocation. It returns the budget's error as soon as the budget
// is exhausted, so a request that cannot bound how much history it will
// materialise finds out before it has materialised any of it.
//
// A record that is currently live (being re-run), which the decode would skip, is
// charged too. That makes the refusal marginally early rather than late.
func (db *db) spendArchivedBytesByRepGroup(repgroup string, budget *archivedBytesBudget) error {
	return db.bolt.View(func(tx *bolt.Tx) error {
		completeJobBucket := tx.Bucket(bucketJobsComplete)
		lookupBucket := tx.Bucket(bucketRTK).Cursor()

		prefix := []byte(repgroup + dbDelimiter)
		for k, _ := lookupBucket.Seek(prefix); bytes.HasPrefix(k, prefix); k, _ = lookupBucket.Next() {
			if err := budget.spend(completeJobBucket.Get(bytes.TrimPrefix(k, prefix))); err != nil {
				return err
			}
		}

		return nil
	})
}

// ErrArchivedHistoryTooBig is what a request that asks for complete jobs with no
// limit gets when the history it matches is too large to materialise. It is a
// deliberate, operator-visible refusal: the alternative is what production
// actually did, which was to take the manager's heap from 0.35GB to over 12GB
// (and, for 2.15M complete jobs, would be over 12GB again through
// `wr status -o plain`). The message names the way out, since every route to
// this request has one.
var ErrArchivedHistoryTooBig = errors.New("too much completed-job history to return at once: " +
	"re-run with --limit, or a state filter, or -o counts")

// isArchivedHistoryTooBig reports whether a getJobsByRepGroup error string is the
// ErrArchivedHistoryTooBig refusal rather than a genuine failure. spend wraps the
// sentinel with the numbers that produced it, so it is recognised by its prefix -
// the pricing pass returns no other error of its own. It exists so that a caller
// which has to translate the refusal (restJobsStatusByID, into an HTTP status)
// does not have to decide what the message looks like for itself.
func isArchivedHistoryTooBig(err string) bool {
	return strings.HasPrefix(err, ErrArchivedHistoryTooBig.Error())
}

// maxArchivedBytesDefault caps how many bytes of encoded archived records ONE
// request that cannot push its limit down (see newCompleteJobsBudget) will
// decode. It is a byte budget rather than a job count because archived records
// vary by three orders of magnitude in size - a job with a 130KB command line
// costs as much as a hundred ordinary ones - so only bytes bound the heap.
// Production's 12.1GB excursion came from roughly 3GB of records; at this cap
// that request is refused instead, while everything that fits (the 154,000-record
// group the 2026-08-20 validation gate measured is ~230MB) is returned exactly as
// before.
const maxArchivedBytesDefault = 256 * 1024 * 1024

// maxArchivedBytes is the live cap. It is a var only so tests can drive it down;
// nothing in the manager writes it, so it needs no synchronisation.
//
//nolint:gochecknoglobals // internal tuning knob; a var only so tests can vary it
var maxArchivedBytes = maxArchivedBytesDefault

// archivedBytesBudget is the byte budget one unbounded archived fetch may spend
// across all the RepGroups its request matches, spent by
// spendArchivedBytesByRepGroup before anything is decoded. A nil budget is
// unlimited.
type archivedBytesBudget struct {
	remaining int
	priced    int
}

// newArchivedBytesBudget returns a budget of maxArchivedBytes bytes.
func newArchivedBytesBudget() *archivedBytesBudget {
	return &archivedBytesBudget{remaining: maxArchivedBytes}
}

// spend accounts for one archived record, returning ErrArchivedHistoryTooBig
// once the budget is exhausted. A nil budget spends nothing and never errors.
func (b *archivedBytesBudget) spend(encoded []byte) error {
	if b == nil {
		return nil
	}

	b.remaining -= len(encoded)
	b.priced++

	if b.remaining < 0 {
		return fmt.Errorf("%w (over %d bytes at %d complete jobs)", ErrArchivedHistoryTooBig,
			maxArchivedBytes, b.priced)
	}

	return nil
}

// decodeArchivedJob decodes the job with the given key from the complete bucket,
// returning nil (and no error) if it is not present there or is currently live
// (being re-run).
func (db *db) decodeArchivedJob(completeJobBucket, newJobBucket *bolt.Bucket, key []byte) (*Job, error) {
	encoded := completeJobBucket.Get(key)
	if len(encoded) == 0 || newJobBucket.Get(key) != nil {
		return nil, nil //nolint:nilnil // absent/live job is a valid non-error nil result
	}

	db.archivedDecodes.Add(1)

	return db.decodeJob(encoded)
}

// retrieveCompleteJobStatusByRepGroup gets a compact status summary for
// archived jobs in the given RepGroup. Count-only mode checks keys and does not
// decode the archived jobs.
func (db *db) retrieveCompleteJobStatusByRepGroup(repgroup string, includeDetails bool) (*RepGroupStatus, error) {
	summary := NewRepGroupStatus()
	err := db.bolt.View(func(tx *bolt.Tx) error {
		return db.addCompleteJobStatusByRepGroup(tx, summary, repgroup, includeDetails)
	})

	return summary, err
}

func (db *db) addCompleteJobStatusByRepGroup(tx *bolt.Tx, summary *RepGroupStatus, repgroup string,
	includeDetails bool) error {
	newJobBucket := tx.Bucket(bucketJobsLive)
	completeJobBucket := tx.Bucket(bucketJobsComplete)
	lookupBucket := tx.Bucket(bucketRTK).Cursor()

	prefix := []byte(repgroup + dbDelimiter)
	for k, _ := lookupBucket.Seek(prefix); bytes.HasPrefix(k, prefix); k, _ = lookupBucket.Next() {
		key := bytes.TrimPrefix(k, prefix)

		encoded := completeJobBucket.Get(key)
		if len(encoded) == 0 || newJobBucket.Get(key) != nil {
			continue
		}

		if err := db.addCompleteJobStatus(summary, repgroup, encoded, includeDetails); err != nil {
			return err
		}
	}

	return nil
}

func (db *db) addCompleteJobStatus(summary *RepGroupStatus, repgroup string, encoded []byte,
	includeDetails bool) error {
	if !includeDetails {
		summary.AddState(JobStateComplete, 1)

		return nil
	}

	job, err := db.decodeJob(encoded)
	if err != nil {
		return err
	}

	job.RepGroup = repgroup
	summary.AddCompleteJob(job)

	return nil
}

// retrieveDependentJobs gets previously stored jobs that had a dependency on
// one for the input depGroups. If the job is found in the live bucket, then it
// is returned in the jobsToUpdate return value. If it is found in the complete
// bucket, and is not true in the supplied newJobKeys map, then it is returned
// in the jobsToQueue return value.
func (db *db) retrieveDependentJobs(depGroups, newJobKeys map[string]bool) (
	jobsToQueue, jobsToUpdate []*Job, err error,
) {
	scan := &dependentJobsScan{
		db:         db,
		depGroups:  depGroups,
		newJobKeys: newJobKeys,
		doneKeys:   make(map[string]bool),
		prefixes:   depGroupPrefixes(depGroups),
	}

	err = db.bolt.View(scan.run)

	return scan.jobsToQueue, scan.jobsToUpdate, err
}

// depGroupPrefixes converts a set of dep groups into sorted lookup-key prefixes,
// for linear searching.
func depGroupPrefixes(depGroups map[string]bool) sobsd {
	prefixes := make(sobsd, 0, len(depGroups))
	for depGroup := range depGroups {
		prefixes = append(prefixes, [2][]byte{[]byte(depGroup + dbDelimiter), nil})
	}

	sort.Sort(prefixes)

	return prefixes
}

// dependentJobsScan holds the state of a retrieveDependentJobs traversal.
type dependentJobsScan struct {
	db           *db
	depGroups    map[string]bool
	newJobKeys   map[string]bool
	doneKeys     map[string]bool
	prefixes     sobsd
	newDepGroups map[string]bool
	jobsToQueue  []*Job
	jobsToUpdate []*Job
}

// run performs the iterative dependency traversal within a read transaction,
// following newly discovered dep groups until none remain.
func (s *dependentJobsScan) run(tx *bolt.Tx) error {
	newJobBucket := tx.Bucket(bucketJobsLive)
	completeJobBucket := tx.Bucket(bucketJobsComplete)
	cursor := tx.Bucket(bucketRDTK).Cursor()

	for {
		s.newDepGroups = make(map[string]bool)

		for _, bsd := range s.prefixes {
			if err := s.scanPrefix(cursor, bsd[0], newJobBucket, completeJobBucket); err != nil {
				return err
			}
		}

		if len(s.newDepGroups) == 0 {
			return nil
		}

		s.prefixes = s.promoteNewDepGroups()
	}
}

// scanPrefix walks all reverse-dependency lookup entries with the given prefix,
// processing each referenced job key once.
func (s *dependentJobsScan) scanPrefix(cursor *bolt.Cursor, prefix []byte,
	newJobBucket, completeJobBucket *bolt.Bucket,
) error {
	for k, _ := cursor.Seek(prefix); bytes.HasPrefix(k, prefix); k, _ = cursor.Next() {
		key := bytes.TrimPrefix(k, prefix)

		keyStr := string(key)
		if s.doneKeys[keyStr] {
			continue
		}

		if err := s.processKey(key, keyStr, newJobBucket, completeJobBucket); err != nil {
			return err
		}

		s.doneKeys[keyStr] = true
	}

	return nil
}

// processKey decodes the job referenced by key (from the live bucket if present,
// else the complete bucket if not a brand-new job), classifies it as one to
// update or queue, and records any new dep groups it belongs to.
func (s *dependentJobsScan) processKey(key []byte, keyStr string, newJobBucket, completeJobBucket *bolt.Bucket) error {
	encoded := newJobBucket.Get(key)

	live := len(encoded) > 0
	if !live && !s.newJobKeys[keyStr] {
		encoded = completeJobBucket.Get(key)
	}

	if len(encoded) == 0 {
		return nil
	}

	job, err := s.db.decodeJob(encoded)
	if err != nil {
		return err
	}

	// since we're going to add this job, we also need to check its DepGroups
	// and repeat this loop on any new ones
	s.recordNewDepGroups(job.DepGroups)

	if live {
		s.jobsToUpdate = append(s.jobsToUpdate, job)
	} else {
		s.jobsToQueue = append(s.jobsToQueue, job)
	}

	return nil
}

// recordNewDepGroups notes any of the given dep groups that we haven't already
// seen, so they get scanned in the next iteration.
func (s *dependentJobsScan) recordNewDepGroups(depGroups []string) {
	for _, depGroup := range depGroups {
		if depGroup != "" && !s.depGroups[depGroup] {
			s.newDepGroups[depGroup] = true
		}
	}
}

// promoteNewDepGroups records the newly discovered dep groups as seen and
// returns the sorted prefixes to scan for them next.
func (s *dependentJobsScan) promoteNewDepGroups() sobsd {
	newPrefixes := make(sobsd, 0, len(s.newDepGroups))

	for depGroup := range s.newDepGroups {
		newPrefixes = append(newPrefixes, [2][]byte{[]byte(depGroup + dbDelimiter), nil})
		s.depGroups[depGroup] = true
	}

	sort.Sort(newPrefixes)

	return newPrefixes
}

// retrieveIncompleteJobKeysByDepGroup gets jobs with the given DepGroup from
// the live bucket (ie. those that have been added to the queue and not yet
// Archive()d - even if they've been added and archived in the past).
func (db *db) retrieveIncompleteJobKeysByDepGroup(depgroup string) ([]string, error) {
	var jobKeys []string

	err := db.bolt.View(func(tx *bolt.Tx) error {
		jobKeys = retrieveIncompleteJobKeysByDepGroupTx(tx, depgroup)

		return nil
	})

	return jobKeys, err
}

// retrieveIncompleteJobKeysByDepGroupTx is retrieveIncompleteJobKeysByDepGroup
// using the given read transaction.
func retrieveIncompleteJobKeysByDepGroupTx(tx *bolt.Tx, depgroup string) []string {
	var jobKeys []string

	newJobBucket := tx.Bucket(bucketJobsLive)
	lookupBucket := tx.Bucket(bucketDTK).Cursor()

	prefix := []byte(depgroup + dbDelimiter)
	for k, _ := lookupBucket.Seek(prefix); bytes.HasPrefix(k, prefix); k, _ = lookupBucket.Next() {
		key := bytes.TrimPrefix(k, prefix)
		if newJobBucket.Get(key) != nil {
			jobKeys = append(jobKeys, string(key))
		}
	}

	return jobKeys
}

func (db *db) depGroupEverSeen(depGroup string) (bool, error) {
	var seen bool

	err := db.bolt.View(func(tx *bolt.Tx) error {
		seen = depGroupEverSeenTx(tx, depGroup)

		return nil
	})

	return seen, err
}

// depGroupEverSeenTx is depGroupEverSeen using the given read transaction.
func depGroupEverSeenTx(tx *bolt.Tx, depGroup string) bool {
	return tx.Bucket(bucketDepGroups).Get([]byte(depGroup)) != nil
}

// depGroupsEverSeen says, for each of the given dep groups, whether a job with
// that dep group has ever been added.
//
// An empty list is answered without opening a transaction: every job's
// dependencies are resolved through here, and a job with no dep group
// dependencies (the common case) has nothing to look up. Paying a bolt read
// transaction for it cost prior-state recovery 150,472 pointless transactions
// against a cold 7 GB database in production (.docs/bugfixes/260825-2.md).
func (db *db) depGroupsEverSeen(depGroups []string) (map[string]bool, error) {
	if len(depGroups) == 0 {
		return make(map[string]bool), nil
	}

	seen := make(map[string]bool, len(depGroups))

	err := db.bolt.View(func(tx *bolt.Tx) error {
		seen = depGroupsEverSeenTx(tx, depGroups)

		return nil
	})

	return seen, err
}

// depGroupsEverSeenTx is depGroupsEverSeen using the given read transaction.
func depGroupsEverSeenTx(tx *bolt.Tx, depGroups []string) map[string]bool {
	seen := make(map[string]bool, len(depGroups))

	b := tx.Bucket(bucketDepGroups)
	for _, depGroup := range depGroups {
		seen[depGroup] = b.Get([]byte(depGroup)) != nil
	}

	return seen
}

// storeEnv stores a clientRequest.Env in db unless cached, which means it must
// already be there. Returns a key by which the stored Env can be retrieved.
func (db *db) storeEnv(env []byte) (string, error) {
	envkey := byteKey(env)
	if !db.envcache.Contains(envkey) {
		err := db.store(bucketEnvs, envkey, env)
		if err != nil {
			return envkey, err
		}

		db.envcache.Add(envkey, env)
	}

	return envkey, nil
}

// retrieveEnv gets a value from the db that was stored with storeEnv(). The
// value may come from the cache, avoiding db access.
func (db *db) retrieveEnv(ctx context.Context, envkey string) []byte {
	cached, got := db.envcache.Get(envkey)
	if got {
		return cached
	}

	envc := db.retrieve(ctx, bucketEnvs, envkey)
	db.envcache.Add(envkey, envc)

	return envc
}

// updateJobAfterExit stores the Job's peak RAM usage and wall time against the
// Job's ReqGroup, but only if the job failed for using too much RAM or time,
// allowing recommendedReqGroup*(ReqGroup) to work.
//
// So that state can be restored if the server crashes and is restarted, the
// job is rewritten in its current state in to the live bucket.
//
// It also updates the stdout/err associated with a job. We don't want to store
// these in the job, since that would waste a lot of the queue's memory; we
// store in db instead, and only retrieve when a client needs to see these. To
// stop the db file becoming enormous, we only store these if the cmd failed (or
// if forceStorage is true: used when the job got buried) and also delete these
// from db when the cmd completes successfully.
//
// By doing the deletion upfront, we also ensure we have the latest std, which
// may be nil even on cmd failure. Since it is not critical to the running of
// jobs and workflows that this works 100% of the time, we ignore errors and
// write to bolt in a goroutine, giving us a significant speed boost.
func (db *db) updateJobAfterExit(ctx context.Context, job *Job, stdo, stde []byte, forceStorage bool) {
	db.Lock()
	defer db.Unlock()

	if db.closed {
		return
	}

	exit, ok := db.snapshotJobExit(ctx, job, stdo, stde, forceStorage)
	if !ok {
		return
	}

	db.updatingAfterJobExit.Add(1)

	db.wgMutex.Lock()
	defer db.wgMutex.Unlock()

	db.launchJobExitUpdate(exit)
}

// snapshotJobExit encodes the job and snapshots the fields needed to persist it
// after exit. The bool is false (and nothing should be done) if encoding fails.
func (db *db) snapshotJobExit(ctx context.Context, job *Job, stdo, stde []byte, forceStorage bool) (jobExitData, bool) {
	var encoded []byte

	enc := codec.NewEncoderBytes(&encoded, db.ch)

	job.RLock()
	exit := newJobExitData(job, stdo, stde, forceStorage)
	err := enc.Encode(job)
	job.RUnlock()

	if err != nil {
		clog.Error(ctx, "Database operation updateJobAfterExit failed due to Encode failure", "err", err)

		return jobExitData{}, false
	}

	exit.encoded = encoded

	return exit, true
}

// launchJobExitUpdate queues an exit op for the best-effort writer. Exit ops are
// not coalesced (their std/fail-stat side effects must each run), so the writer
// applies them in order within its folded write tx. db.updatingAfterJobExit was
// already incremented by updateJobAfterExit and is decremented by the writer once
// the op is persisted. Must be called with db.Lock and db.wgMutex held.
func (db *db) launchJobExitUpdate(exit jobExitData) {
	db.beMu.Lock()
	db.beExits = append(db.beExits, exit)
	db.beWGKeys = append(db.beWGKeys, db.wg.Add(1))
	db.beMu.Unlock()

	db.kickBestEffortWriter()
}

// jobExitData is the snapshot of a job's state needed to persist it after it
// exits, used by updateJobAfterExit.
type jobExitData struct {
	key          string
	encoded      []byte
	stdo         []byte
	stde         []byte
	exitcode     int
	forceStorage bool
	failReason   string
	reqGroup     string
	peakRAM      int
	requiredRAM  int
	peakDisk     int64
	secs         int
}

// update is the transactional part of updateJobAfterExit: it rewrites the live
// job, refreshes its stored std, and records any resource-based failure stat.
func (e jobExitData) update(tx *bolt.Tx) error {
	key := []byte(e.key)

	bjl := tx.Bucket(bucketJobsLive)
	if bjl.Get(key) != nil {
		if errf := bjl.Put(key, e.encoded); errf != nil {
			return errf
		}
	}

	if err := e.updateStd(tx, key); err != nil {
		return err
	}

	return e.updateFailStat(tx)
}

// updateStd deletes any existing stored std for the job and, if the job failed
// (or storage is forced), stores the new std.
func (e jobExitData) updateStd(tx *bolt.Tx, key []byte) error {
	bo := tx.Bucket(bucketStdO)
	be := tx.Bucket(bucketStdE)

	if errf := bo.Delete(key); errf != nil {
		return errf
	}

	if errf := be.Delete(key); errf != nil {
		return errf
	}

	if e.exitcode == 0 && !e.forceStorage {
		return nil
	}

	var errf error

	if len(e.stdo) > 0 {
		errf = bo.Put(key, e.stdo)
	}

	if len(e.stde) > 0 {
		errf = be.Put(key, e.stde)
	}

	return errf
}

// updateFailStat records the job's resource usage in the appropriate stat
// bucket when it failed for a resource-based reason.
func (e jobExitData) updateFailStat(tx *bolt.Tx) error {
	if e.shouldRecordHighPeakRAMStat() {
		if err := putJobStat(tx.Bucket(bucketJobRAM), e.reqGroup, e.peakRAM); err != nil {
			return err
		}
	}

	switch e.failReason {
	case FailReasonRAM:
		return putJobStat(tx.Bucket(bucketJobRAM), e.reqGroup, e.peakRAM)
	case FailReasonDisk:
		return putJobStat(tx.Bucket(bucketJobDisk), e.reqGroup, int(e.peakDisk))
	case FailReasonTime:
		return putJobStat(tx.Bucket(bucketJobSecs), e.reqGroup, e.secs)
	default:
		return nil
	}
}

// updateJobAfterChange rewrites the job's entry in the live bucket, to enable
// complete recovery after a crash. This happens in a goroutine, since it isn't
// essential this happens, and we benefit from the speed.
func (db *db) updateJobAfterChange(ctx context.Context, job *Job) {
	var encoded []byte

	enc := codec.NewEncoderBytes(&encoded, db.ch)

	db.RLock()
	defer db.RUnlock()

	if db.closed {
		return
	}

	key := []byte(job.Key())
	job.RLock()
	err := enc.Encode(job)
	job.RUnlock()

	if err != nil {
		clog.Error(ctx, "Database operation updateJobAfterChange failed due to Encode failure", "err", err)

		return
	}

	db.wgMutex.Lock()
	defer db.wgMutex.Unlock()

	db.launchJobChangeUpdate(key, encoded)
}

// launchJobChangeUpdate queues the job's latest encoded live-bucket value for the
// best-effort writer, coalescing by key so a churning job is persisted once per
// drain (latest-wins), not once per change. The archive-vs-change guard (only
// rewrite a job that is still live) is applied by the writer at drain time. Must
// be called with db.RLock and db.wgMutex held.
func (db *db) launchJobChangeUpdate(key, encoded []byte) {
	db.beMu.Lock()
	db.beChanges[string(key)] = encoded
	db.beWGKeys = append(db.beWGKeys, db.wg.Add(1))
	db.beMu.Unlock()

	db.kickBestEffortWriter()
}

// modifyLiveJobs is for use if jobs currently in the queue are modified such
// that their Key() changes, or their dependencies or dependency groups change.
// We simply remove all reference to the old keys in the lookup buckets, as well
// as the old jobs from the live bucket, and then do the equivalent of
// storeNewJobs() on the supplied new version of the jobs. (This is all done in
// one transaction, so won't leave things in a bad state if interuppted half
// way.)
// The order of oldKeys should match the order or new jobs. Ie. oldKeys[0] is
// the old Key() of jobs[0]. This is so that any stdout/err of old jobs is
// associated with the new jobs.
func (db *db) modifyLiveJobs(ctx context.Context, oldKeys []string, jobs []*Job) error {
	//nolint:dogsled // modifyLiveJobs only needs the persistence fields from prepareNewJobs.
	encodedJobs, rgLookups, depGroupsSeen, rdgLookups, rgs, _, _, _, err := db.prepareNewJobs(jobs, false)
	if err != nil {
		return err
	}

	lookups := newJobLookups(rgLookups, rgs, depGroupsSeen, rdgLookups)

	sort.Sort(encodedJobs)

	err = db.bolt.Batch(func(tx *bolt.Tx) error {
		return db.modifyLiveJobsTx(tx, oldKeys, jobs, encodedJobs, lookups)
	})
	if err != nil {
		clog.Error(ctx, "Database error during modify", "err", err)
	}

	db.backupDirty.Store(true)

	return err
}

// jobLookups groups the various lookup index sobsds that accompany a set of
// jobs in the database.
type jobLookups struct {
	rg         sobsd
	repGroups  sobsd
	depGroups  sobsd
	reverseDep sobsd
}

// newJobLookups sorts and groups the lookup sobsds prepared by prepareNewJobs.
func newJobLookups(rgLookups, rgs, depGroupsSeen, rdgLookups sobsd) jobLookups {
	sort.Sort(rgLookups)
	sort.Sort(rgs)
	sort.Sort(depGroupsSeen)
	sort.Sort(rdgLookups)

	return jobLookups{
		rg:         rgLookups,
		repGroups:  rgs,
		depGroups:  depGroupsSeen,
		reverseDep: rdgLookups,
	}
}

// modifyLiveJobsTx is the transactional part of modifyLiveJobs: it removes the
// old jobs (preserving their std), then stores the new lookups, std and jobs.
func (db *db) modifyLiveJobsTx(tx *bolt.Tx, oldKeys []string, jobs []*Job, encodedJobs sobsd,
	lookups jobLookups,
) error {
	oldStd, err := deleteOldLiveJobs(tx, oldKeys)
	if err != nil {
		return err
	}

	if len(encodedJobs) == 0 {
		return nil
	}

	if err = db.putAllLookups(tx, lookups); err != nil {
		return err
	}

	if err = oldStd.restore(tx, jobs); err != nil {
		return err
	}

	return db.putEncodedJobs(tx, bucketJobsLive, encodedJobs)
}

// jobStd holds the stdout/stderr captured from the old jobs during
// modifyLiveJobs, indexed parallel to the old/new job slices.
type jobStd struct {
	stdo   [][]byte
	stde   [][]byte
	hadStd bool
}

// deleteOldLiveJobs removes the given jobs and their lookup entries from the
// live bucket, returning their captured stdout/stderr for re-association with
// the new jobs.
func deleteOldLiveJobs(tx *bolt.Tx, oldKeys []string) (jobStd, error) {
	newJobBucket := tx.Bucket(bucketJobsLive)
	bo := tx.Bucket(bucketStdO)
	be := tx.Bucket(bucketStdE)

	std := jobStd{stdo: make([][]byte, len(oldKeys)), stde: make([][]byte, len(oldKeys))}

	for i, oldKey := range oldKeys {
		key := []byte(oldKey)

		if err := deleteLookupEntriesForJobKey(tx, key); err != nil {
			return std, err
		}

		if err := newJobBucket.Delete(key); err != nil {
			return std, err
		}

		var err error

		if std.stdo[i], err = takeStd(bo, key, &std.hadStd); err != nil {
			return std, err
		}

		if std.stde[i], err = takeStd(be, key, &std.hadStd); err != nil {
			return std, err
		}
	}

	return std, nil
}

// takeStd reads and deletes any stored std for key from b, setting *hadStd if
// there was any.
func takeStd(b *bolt.Bucket, key []byte, hadStd *bool) ([]byte, error) {
	v := b.Get(key)
	if v == nil {
		return nil, nil
	}

	*hadStd = true

	return v, b.Delete(key)
}

// restore re-stores the captured std against the new jobs' keys.
func (s jobStd) restore(tx *bolt.Tx, jobs []*Job) error {
	if !s.hadStd {
		return nil
	}

	bo := tx.Bucket(bucketStdO)
	be := tx.Bucket(bucketStdE)

	for i, job := range jobs {
		key := []byte(job.Key())

		if err := putIfNotNil(bo, key, s.stdo[i]); err != nil {
			return err
		}

		if err := putIfNotNil(be, key, s.stde[i]); err != nil {
			return err
		}
	}

	return nil
}

// putIfNotNil puts val under key in b only if val is non-nil.
func putIfNotNil(b *bolt.Bucket, key, val []byte) error {
	if val == nil {
		return nil
	}

	return b.Put(key, val)
}

// putAllLookups stores all of a job set's lookup indexes, skipping empty ones.
func (db *db) putAllLookups(tx *bolt.Tx, lookups jobLookups) error {
	puts := []struct {
		bucket  []byte
		entries sobsd
	}{
		{bucketRTK, lookups.rg},
		{bucketRGs, lookups.repGroups},
		{bucketDepGroups, lookups.depGroups},
		{bucketRDTK, lookups.reverseDep},
	}

	for _, p := range puts {
		if len(p.entries) == 0 {
			continue
		}

		if err := db.putLookups(tx, p.bucket, p.entries); err != nil {
			return err
		}
	}

	return nil
}

func deleteLookupEntriesForJobKey(tx *bolt.Tx, jobKey []byte) error {
	b := tx.Bucket(bucketJobLookupEntries)
	if b == nil {
		return fmt.Errorf("%w: %s", berrors.ErrBucketNotFound, bucketJobLookupEntries)
	}

	reverseKeys, deletes := collectLookupDeletes(b, reverseLookupEntryPrefix(jobKey))

	for _, d := range deletes {
		lookupBucket := tx.Bucket(d.bucket)
		if lookupBucket == nil {
			return fmt.Errorf("%w: %s", berrors.ErrBucketNotFound, d.bucket)
		}

		if err := lookupBucket.Delete(d.key); err != nil {
			return err
		}
	}

	for _, key := range reverseKeys {
		err := b.Delete(key)
		if err != nil {
			return err
		}
	}

	return nil
}

// retrieveJobStd gets the values that were stored using updateJobStd() for the
// given job.
func (db *db) retrieveJobStd(ctx context.Context, jobkey string) (stdo []byte, stde []byte) {
	db.waitForJobExitUpdates()

	err := db.bolt.View(func(tx *bolt.Tx) error {
		key := []byte(jobkey)
		stdo = copyBucketValue(tx.Bucket(bucketStdO), key)
		stde = copyBucketValue(tx.Bucket(bucketStdE), key)

		return nil
	})
	if err != nil {
		// impossible, but to keep the linter happy and incase things change in
		// the future
		clog.Error(ctx, "Database retrieve failed", "err", err)
	}

	return stdo, stde
}

// waitForJobExitUpdates blocks until there are no in-progress
// updateJobAfterExit() calls.
//
// *** this method of waiting seems really bad and should be improved, but in
// practice we probably never wait.
func (db *db) waitForJobExitUpdates() {
	for db.updatingAfterJobExit.Load() != 0 {
		<-time.After(jobExitUpdatePollInterval)
	}
}

// copyBucketValue returns a copy of the value stored under key in b, or nil if
// there is none.
func copyBucketValue(b *bolt.Bucket, key []byte) []byte {
	v := b.Get(key)
	if v == nil {
		return nil
	}

	out := make([]byte, len(v))
	copy(out, v)

	return out
}

// recommendedReqGroupMemory returns the 95th percentile peak memory usage of
// all jobs that previously ran with the given reqGroup. If there are too few
// prior values to calculate a 95th percentile, or if the 95th percentile is
// very close to the maximum value, returns the maximum value instead. In either
// case, the true value is rounded up to the nearest 100 MB. Returns 0 if there
// are no prior values.
func (db *db) recommendedReqGroupMemory(reqGroup string) (int, error) {
	return db.recommendedReqGroupStat(bucketJobRAM, reqGroup, db.mbRound())
}

// recommendedReqGroupDisk returns the 95th percentile peak disk usage of
// all jobs that previously ran with the given reqGroup. If there are too few
// prior values to calculate a 95th percentile, or if the 95th percentile is
// very close to the maximum value, returns the maximum value instead. In either
// case, the true value is rounded up to the nearest 100 MB. Returns 0 if there
// are no prior values.
func (db *db) recommendedReqGroupDisk(reqGroup string) (int, error) {
	return db.recommendedReqGroupStat(bucketJobDisk, reqGroup, db.mbRound())
}

// mbRound returns the MB rounding for recommendations, falling back to the
// RecMBRound package default if this db wasn't given a positive value.
func (db *db) mbRound() int {
	return recommendationRound(db.recMBRound, RecMBRound)
}

// recommendationRound returns round, or defaultRound if round is not positive.
func recommendationRound(round, defaultRound int) int {
	if round <= 0 {
		return defaultRound
	}

	return round
}

// recommendReqGroupTime returns the 95th percentile wall time taken of all jobs
// that previously ran with the given reqGroup. If there are too few prior
// values to calculate a 95th percentile, or if the 95th percentile is very
// close to the maximum value, returns the maximum value instead. In either
// case, the true value is rounded up to the nearest second. Returns 0 if there
// are no prior values.
func (db *db) recommendedReqGroupTime(reqGroup string) (int, error) {
	return db.recommendedReqGroupStat(bucketJobSecs, reqGroup, recommendationRound(db.recSecRound, RecSecRound))
}

// recommendedReqGroupStat is the implementation for the other recommend*()
// methods.
func (db *db) recommendedReqGroupStat(statBucket []byte, reqGroup string, roundAmount int) (int, error) {
	var maxVal, recommendation int

	err := db.bolt.View(func(tx *bolt.Tx) error {
		var errs error

		maxVal, recommendation, errs = scanReqGroupStat(tx.Bucket(statBucket).Cursor(), []byte(reqGroup))

		return errs
	})
	if err != nil {
		return 0, err
	}

	return roundRecommendation(recommendation, maxVal, roundAmount), nil
}

// scanReqGroupStat seeks over a stat bucket for the given reqGroup prefix and
// returns the maximum stored value and the 95th-percentile recommendation. To
// avoid scanning twice it keeps a trailing 5%-sized window of values, updating
// the recommendation as the window fills.
func scanReqGroupStat(c *bolt.Cursor, prefix []byte) (maxVal, recommendation int, err error) {
	count := 0
	window := jobStatWindowPercent

	var prev []int

	for k, v := c.Seek(prefix); bytes.HasPrefix(k, prefix); k, v = c.Next() {
		maxVal, err = strconv.Atoi(string(v))
		if err != nil {
			return maxVal, recommendation, err
		}

		count++
		if count > jobStatWindowScaleThreshold {
			window = (float32(count) / jobStatWindowScaleThreshold) * jobStatWindowPercent
		}

		prev = append(prev, maxVal)
		if float32(len(prev)) > window {
			recommendation, prev = prev[0], prev[1:]
		}
	}

	return maxVal, recommendation, nil
}

// roundRecommendation applies the recommend*() fallback and rounding rules: it
// falls back to maxVal when the recommendation is unset or very close to the
// max, then rounds up to the nearest roundAmount.
func roundRecommendation(recommendation, maxVal, roundAmount int) int {
	if recommendation == 0 {
		if maxVal == 0 {
			return 0
		}

		recommendation = maxVal
	}

	if maxVal-recommendation < roundAmount {
		recommendation = maxVal
	}

	if recommendation < roundAmount {
		recommendation = roundAmount
	}

	if recommendation%roundAmount > 0 {
		recommendation = int(math.Ceil(float64(recommendation)/float64(roundAmount))) * roundAmount
	}

	return recommendation
}

// store does a basic set of a key/val in a given bucket.
func (db *db) store(bucket []byte, key string, val []byte) error {
	err := db.bolt.Batch(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucket)
		err := b.Put([]byte(key), val)

		return err
	})

	return err
}

// retrieve does a basic get of a key from a given bucket. An error isn't
// possible here.
func (db *db) retrieve(ctx context.Context, bucket []byte, key string) []byte {
	var val []byte

	err := db.bolt.View(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucket)

		v := b.Get([]byte(key))
		if v != nil {
			val = make([]byte, len(v))
			copy(val, v)
		}

		return nil
	})
	if err != nil {
		// impossible, but to keep the linter happy and incase things change in
		// the future
		clog.Error(ctx, "Database retrieve failed", "err", err)
	}

	return val
}

// storeBatched stores items in the db in batches for efficiency. bucket is the
// name of the bucket to store in.
func (db *db) storeBatched(bucket []byte, data sobsd, storer sobsdStorer) error {
	num := len(data)
	batchSize := batchSizeFor(num)

	// based on https://github.com/boltdb/bolt/issues/337#issue-64861745
	if num < batchSize {
		return storer(bucket, data)
	}

	batches := num / batchSize

	for i := range batches {
		if err := storer(bucket, data[i*batchSize:(i+1)*batchSize]); err != nil {
			return err
		}
	}

	if offset := num - (num % batchSize); offset != 0 {
		if err := storer(bucket, data[offset:]); err != nil {
			return err
		}
	}

	return nil
}

// batchSizeFor returns the storeBatched batch size for num items: num /
// storeBatchDivisor, at least storeBatchGranularity, rounded to the nearest
// storeBatchGranularity.
func batchSizeFor(num int) int {
	batchSize := num / storeBatchDivisor

	rem := batchSize % storeBatchGranularity
	if rem > storeBatchRoundThreshold {
		batchSize = batchSize - rem + storeBatchGranularity
	} else {
		batchSize -= rem
	}

	if batchSize < storeBatchGranularity {
		batchSize = storeBatchGranularity
	}

	return batchSize
}

// storeLookups is a sobsdStorer for storing Job.[somevalue]->Job.Key() lookups
// in the db.
func (db *db) storeLookups(bucket []byte, lookups sobsd) error {
	err := db.bolt.Batch(func(tx *bolt.Tx) error {
		return db.putLookups(tx, bucket, lookups)
	})

	return err
}

// putLookups does the work of storeLookups(). You must be inside a bolt
// transaction when calling this.
func (db *db) putLookups(tx *bolt.Tx, bucket []byte, lookups sobsd) error {
	lookup := tx.Bucket(bucket)

	for _, doublet := range lookups {
		err := lookup.Put(doublet[0], nil)
		if err != nil {
			return err
		}

		if isIndexedLookupBucket(bucket) {
			err = putReverseLookupEntry(tx, bucket, doublet[0])
			if err != nil {
				return err
			}
		}
	}

	return nil
}

func isIndexedLookupBucket(bucket []byte) bool {
	return bytes.Equal(bucket, bucketRTK) ||
		bytes.Equal(bucket, bucketDTK) ||
		bytes.Equal(bucket, bucketRDTK)
}

func putReverseLookupEntry(tx *bolt.Tx, lookupBucket, lookupKey []byte) error {
	jobKey := lookupEntryJobKey(lookupKey)
	if len(jobKey) == 0 {
		return nil
	}

	b := tx.Bucket(bucketJobLookupEntries)
	if b == nil {
		return fmt.Errorf("%w: %s", berrors.ErrBucketNotFound, bucketJobLookupEntries)
	}

	return b.Put(reverseLookupEntryKey(jobKey, lookupBucket, lookupKey), nil)
}

// storeEncodedJobs is a sobsdStorer for storing Jobs in the db.
func (db *db) storeEncodedJobs(bucket []byte, encodes sobsd) error {
	err := db.bolt.Batch(func(tx *bolt.Tx) error {
		return db.putEncodedJobs(tx, bucket, encodes)
	})

	return err
}

// putEncodedJobs does the work of storeEncodedJobs(). You nust be inside a bolt
// transaction when calling this.
func (db *db) putEncodedJobs(tx *bolt.Tx, bucket []byte, encodes sobsd) error {
	bjobs := tx.Bucket(bucket)
	for _, doublet := range encodes {
		err := bjobs.Put(doublet[0], doublet[1])
		if err != nil {
			return err
		}
	}

	return nil
}

// close shuts down the db, should be used prior to exiting. It stops the
// periodic backup ticker, waits for any in-progress periodic backup and all
// ongoing async write transactions to finish, then writes a final backup that
// captures the fully-drained committed state before closing bolt.
func (db *db) close(ctx context.Context) error {
	db.Lock()
	defer db.Unlock()

	if db.closed {
		return nil
	}

	db.closed = true

	db.finaliseBackup(ctx)

	return db.closeBolt()
}

// finaliseBackup is called once from close() (with db.Lock held). It stops the
// periodic backup ticker (if backups are enabled), waits for any in-progress
// periodic backup and all ongoing async write transactions to finish, then -
// unlike the periodic path - writes a final backup that captures everything, so
// a clean shutdown's backup is complete. No backup-path code takes db.Lock, so
// holding it here does not deadlock the in-progress backup we wait for.
func (db *db) finaliseBackup(ctx context.Context) {
	var inProgress bool

	if db.backupsEnabled {
		inProgress = db.stopBackupTicker()
	}

	// stop the archive and best-effort writers and drain their queues first, so
	// every enqueued archive is persisted (and its caller replied to) and all
	// enqueued change/exit writes are persisted (and their db.wg tracking released)
	// before the wg drain and final backup below. The archive queue is drained
	// first because its callers are synchronously waiting on the outcome.
	db.stopArchiveWriter()
	db.stopBestEffortWriter()

	// drain ongoing async write transactions so the final backup captures them.
	db.wgMutex.Lock()
	db.wg.Wait(dbRunningTransactionsWaitTime)
	db.wgMutex.Unlock()

	if db.backupsEnabled && (db.backupDirty.Swap(false) || inProgress) {
		clog.Debug(ctx, "Jobqueue database doing final backup before close")
		db.backupToBackupFile(ctx, false)
	}
}

// stopBackupTicker stops the periodic backup ticker and, if a periodic backup is
// currently running, waits for it to finish (cutting short any spacing wait so
// shutdown stays prompt). It returns whether a backup was in progress. Only
// called from close(), when backups are enabled.
func (db *db) stopBackupTicker() bool {
	close(db.backupTickerStop)

	db.backupMu.Lock()
	db.backupStopped = true

	inProgress := db.backingUp
	if inProgress {
		db.backupFinal = true
		close(db.backupStopWait)
	}
	db.backupMu.Unlock()

	if inProgress {
		<-db.backupNotification
	}

	return inProgress
}

// backupTicker is the single long-lived goroutine that drives periodic backups.
// Every backupDirtyPollInterval it consumes the lock-free backupDirty flag and,
// if set, runs one backup (spaced out by backupWait via waitBeforeBackup). It
// runs until close() closes backupTickerStop. Decoupling backup-triggering from
// the archive/exit hot path (which now just sets backupDirty, taking no lock) is
// the point of the fix.
func (db *db) backupTicker(ctx context.Context) {
	defer internal.LogPanic(ctx, "jobqueue database backup ticker", true)

	for db.awaitBackupTick() {
		if db.backupDirty.Swap(false) {
			db.backgroundBackup(ctx)
		}
	}
}

// awaitBackupTick waits one backupDirtyPollInterval, returning true when it is
// time to check backupDirty, or false when close() has stopped the ticker.
func (db *db) awaitBackupTick() bool {
	timer := time.NewTimer(backupDirtyPollInterval)
	defer timer.Stop()

	select {
	case <-db.backupTickerStop:
		return false
	case <-timer.C:
		return true
	}
}

// closeBolt closes the underlying bolt db and unmounts any backup mount,
// combining any errors.
func (db *db) closeBolt() error {
	err := db.bolt.Close()
	if db.backupMount == nil {
		return err
	}

	erru := db.backupMount.Unmount()
	if erru == nil {
		return err
	}

	if err == nil {
		return erru
	}

	return fmt.Errorf("%w (and unmounting backup failed: %w)", err, erru)
}

// backgroundBackup backs up the database to a file (the location given during
// initDB()) in a goroutine, doing one backup at a time and queueing a further
// backup if the ticker fires again while a backup is running. Any errors are
// silently ignored. Spaces out sequential backups so that there is a gap of
// max(30s, [time taken to complete previous backup]) seconds between them. It is
// driven by the backup ticker (and requeues), never by the hot path, and takes
// only backupMu - never the db RWMutex.
func (db *db) backgroundBackup(ctx context.Context) {
	db.backupMu.Lock()
	defer db.backupMu.Unlock()

	if db.backupStopped || !db.backupsEnabled {
		return
	}

	if db.backingUp {
		db.backupQueued = true

		return
	}

	db.backingUp = true

	go db.runBackgroundBackup(ctx, db.backupLast, db.backupWait, db.backupFinal, db.slowBackups)
}

// runBackgroundBackup is the goroutine body of backgroundBackup: it optionally
// waits to space out backups, performs the backup, then either finalises (for
// close()) or runs a queued backup.
func (db *db) runBackgroundBackup(ctx context.Context, last time.Time, wait time.Duration,
	doNotWait, slowBackups bool,
) {
	defer internal.LogPanic(ctx, "backgroundBackup", true)

	db.waitBeforeBackup(last, wait, doNotWait)

	if slowBackups {
		// just for testing purposes
		<-time.After(slowBackupTestDelay)
	}

	start := time.Now()

	db.backupToBackupFile(ctx, slowBackups)

	db.finishBackgroundBackup(ctx, start)
}

// waitBeforeBackup waits, if appropriate, to space sequential backups out, so
// we don't slow down new db accesses all the time. The wait can be cut short by
// a close() via backupStopWait.
func (db *db) waitBeforeBackup(last time.Time, wait time.Duration, doNotWait bool) {
	if doNotWait {
		return
	}

	now := time.Now()
	if last.IsZero() || !last.Add(wait).After(now) {
		return
	}

	select {
	case <-time.After(last.Add(wait).Sub(now)):
	case <-db.backupStopWait:
	}
}

// finishBackgroundBackup updates backup bookkeeping after a backup completes
// and either notifies a waiting close() or kicks off a queued backup.
func (db *db) finishBackgroundBackup(ctx context.Context, start time.Time) {
	db.backupMu.Lock()
	db.backingUp = false
	db.backupLast = time.Now()

	// don't backup more often than backups take
	duration := time.Since(start)
	if duration > minimumTimeBetweenBackups {
		db.backupWait = duration
	}

	if db.backupFinal {
		// close() is waiting for this in-progress backup; tell it we finished
		// and do not start any more backups.
		db.backupFinal = false
		db.backupMu.Unlock()

		db.backupNotification <- true

		return
	}

	if db.backupQueued {
		db.backupQueued = false
		db.backupMu.Unlock()
		db.backgroundBackup(ctx)
	} else {
		db.backupMu.Unlock()
	}
}

// backupToBackupFile writes a consistent snapshot of the committed database to
// the backup file. It is called by the periodic backup ticker (via
// runBackgroundBackup) and by close()'s final backup. bbolt's read transaction
// already yields a consistent view of committed state, so the PERIODIC path does
// NOT drain in-flight async writes here - that would hold db.wgMutex across a
// (up to) dbRunningTransactionsWaitTime wg.Wait, blocking every new async-write
// registration and serialising the exit hot path. A periodic backup may
// therefore miss the last few in-flight writes (they land in the next backup) -
// fine for a disaster-recovery fallback. close() instead drains the wg BEFORE
// calling this, so a clean shutdown's final backup still captures everything.
func (db *db) backupToBackupFile(ctx context.Context, slowBackups bool) {
	// create the new backup file with temp name
	tmpBackupPath := db.backupPathTmp

	err := db.bolt.View(func(tx *bolt.Tx) error {
		return db.copyBackup(tx, tmpBackupPath)
	})

	if slowBackups {
		<-time.After(slowBackupTestDelay)
	}

	if err != nil {
		db.handleFailedBackup(ctx, tmpBackupPath, err)

		return
	}

	db.handleSucceededBackup(ctx, tmpBackupPath)
}

// handleFailedBackup logs a failed backup and removes any partial backup file.
func (db *db) handleFailedBackup(ctx context.Context, tmpBackupPath string, err error) {
	clog.Error(ctx, "Database backup failed", "err", err)

	// if it failed, delete any partial file that got made
	errr := os.Remove(tmpBackupPath)
	if errr != nil && !os.IsNotExist(errr) {
		clog.Warn(ctx, "Removing bad database backup file failed", "path", tmpBackupPath, "err", errr)
	}
}

// handleSucceededBackup finalises a successful backup, either uploading it to
// S3 (then removing the temp file) or moving it over any old local backup.
func (db *db) handleSucceededBackup(ctx context.Context, tmpBackupPath string) {
	if db.s3accessor == nil {
		// move it over any old backup
		if errr := os.Rename(tmpBackupPath, db.backupPath); errr != nil {
			clog.Warn(ctx, "Renaming new database backup file failed",
				"source", tmpBackupPath, "dest", db.backupPath, "err", errr)
		}

		return
	}

	// upload to s3 then delete it
	errr := db.s3accessor.UploadFile(tmpBackupPath, db.backupPath, "application/octet-stream")
	if errr != nil {
		clog.Warn(ctx, "Uploading new database backup file to S3 failed",
			"source", tmpBackupPath, "dest", db.backupPath, "err", errr)
	}

	if errr = os.Remove(tmpBackupPath); errr != nil {
		clog.Warn(ctx, "failed to delete temporary backup file after uploading to s3",
			"path", tmpBackupPath, "err", errr)
	}
}

// backup backs up the database to the given writer. Can be called at the same
// time as an active backgroundBackup() or even another backup(). You will get
// a consistent view of the database at the time you call this. NB: this can be
// interrupted by calling db.close().
func (db *db) backup(w io.Writer) error {
	db.RLock()

	if db.closed {
		db.RUnlock()

		return errDBClosed
	}

	db.RUnlock()

	return db.bolt.View(func(tx *bolt.Tx) error {
		_, txErr := tx.WriteTo(w)

		return txErr
	})
}

type lookupDelete struct {
	bucket []byte
	key    []byte
}

func collectLookupDeletes(b *bolt.Bucket, prefix []byte) ([][]byte, []lookupDelete) {
	var (
		reverseKeys [][]byte
		deletes     []lookupDelete
	)

	c := b.Cursor()
	for k, _ := c.Seek(prefix); bytes.HasPrefix(k, prefix); k, _ = c.Next() {
		reverseKeys = append(reverseKeys, append([]byte(nil), k...))

		lookupBucket, lookupKey, ok := parseReverseLookupEntry(k, prefix)
		if !ok {
			continue
		}

		deletes = append(deletes, lookupDelete{
			bucket: append([]byte(nil), lookupBucket...),
			key:    append([]byte(nil), lookupKey...),
		})
	}

	return reverseKeys, deletes
}

func parseReverseLookupEntry(entry, prefix []byte) (lookupBucket []byte, lookupKey []byte, ok bool) {
	rest := entry[len(prefix):]

	idx := bytes.Index(rest, []byte(dbDelimiter))
	if idx <= 0 {
		return nil, nil, false
	}

	lookupKeyStart := idx + len(dbDelimiter)
	if lookupKeyStart >= len(rest) {
		return nil, nil, false
	}

	return rest[:idx], rest[lookupKeyStart:], true
}

func rebuildJobLookupEntries(tx *bolt.Tx, progress *dbUpgradeReporter) error {
	entries, totalProcessed, err := collectReverseLookupRebuildEntries(tx, progress)
	if err != nil {
		return err
	}

	if err = putReverseLookupRebuildEntries(tx, entries, progress, totalProcessed); err != nil {
		return err
	}

	progress.completePhase(internal.DBUpgradeJobLookupState,
		fmt.Sprintf("rebuilt database job lookup index (%d entries processed, %d reverse entries written)",
			totalProcessed, len(entries)),
		totalProcessed)

	return nil
}

func indexedLookupBuckets() [][]byte {
	return [][]byte{bucketRTK, bucketDTK, bucketRDTK}
}

func rebuildDepGroups(tx *bolt.Tx, progress *dbUpgradeReporter) error {
	depGroupBucket := tx.Bucket(bucketDepGroups)

	lookupBucket := tx.Bucket(bucketDTK)
	if depGroupBucket == nil || lookupBucket == nil {
		progress.completePhase(internal.DBUpgradeDepGroupIndexState,
			"rebuilt database dependency-group index (0 entries processed)",
			0)

		return nil
	}

	processed, err := rebuildDepGroupEntries(depGroupBucket, lookupBucket, progress)
	if err != nil {
		return err
	}

	progress.completePhase(internal.DBUpgradeDepGroupIndexState,
		fmt.Sprintf("rebuilt database dependency-group index (%d entries processed)", processed),
		processed)

	return nil
}

func lookupEntryJobKey(lookupKey []byte) []byte {
	idx := bytes.LastIndex(lookupKey, []byte(dbDelimiter))
	if idx == -1 {
		return nil
	}

	jobKeyStart := idx + len(dbDelimiter)
	if jobKeyStart >= len(lookupKey) {
		return nil
	}

	return lookupKey[jobKeyStart:]
}

func reverseLookupEntryKey(jobKey, lookupBucket, lookupKey []byte) []byte {
	key := reverseLookupEntryPrefix(jobKey)
	key = append(key, lookupBucket...)
	key = append(key, dbDelimiter...)
	key = append(key, lookupKey...)

	return key
}

func reverseLookupEntryPrefix(jobKey []byte) []byte {
	prefix := make([]byte, 0, len(jobKey)+len(dbDelimiter))
	prefix = append(prefix, jobKey...)
	prefix = append(prefix, dbDelimiter...)

	return prefix
}

// stripBucketFromS3Path removes the first directory from the given path. If
// there are no directories, returns an error.
func stripBucketFromS3Path(path string) (string, error) {
	if _, after, ok := strings.Cut(path, "/"); ok {
		return after, nil
	}

	return "", Error{Err: ErrS3DBBackupPath}
}
