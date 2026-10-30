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
	"errors"
	"io"
	"os"
	"time"

	"github.com/VertebrateResequencing/wr/jobqueue/scheduler"
	"github.com/VertebrateResequencing/wr/limiter"
)

// storeKind picks the store implementation openStore opens. Until the log store
// arrives, every kind opens bbolt.
type storeKind uint8

const (
	storeKindLog  storeKind = iota // the default once the log store exists
	storeKindBolt                  // bbolt; removed with it
)

// store is how the server reaches its persistent storage: the live jobs, their
// history, and the environments, limits and learned requirements beside them.
// *db, over bbolt, implements it.
//
// The rerun guard methods carry an ordering promise: an archive recorded after
// a guard registered sees it, and the addStored function a store gives
// rerunGuards.archiving reports true only if the add's first job was committed
// before the archive was recorded.
//
//nolint:interfacebloat // the whole storage surface the server uses, as one seam
type store interface {
	// depReader answers dependency resolution on the control paths, one read
	// at a time.
	depReader

	recoverIncompleteJobs() ([]*Job, runStateRecovery, error)
	resolveDependencies(ctx context.Context, jobs []*Job,
		groups depGroupState) ([]resolvedJob, error)
	close(ctx context.Context) error
	backup(w io.Writer) error
	upgradedOnOpen() bool

	storeNewJobsGuarded(ctx context.Context, jobs []*Job, ignoreAdded bool,
		guard *rerunGuard) (storedNewJobs, error)
	registerRerunGuard() *rerunGuard
	releaseRerunGuard(g *rerunGuard)
	storeEnv(env []byte) (string, error)
	storeLimitGroups(groups map[string]*limiter.GroupData) (changed,
		removed []string, err error)
	storeRepGroupLookups(ctx context.Context, jobs []*Job) error
	checkIfComplete(key string) (bool, error)

	updateJobRunStateDurable(job *Job) error
	updateJobRunStateDurableWithin(job *Job, wait time.Duration) error
	logReserveNotDurable(key string)
	updateJobAfterChangeDurable(job *Job) error
	updateJobAfterExit(ctx context.Context, job *Job, stdo, stde []byte,
		forceStorage bool)
	updateJobAfterExitDurable(job *Job, stdo, stde []byte,
		forceStorage bool) error
	expectJobExitUpdate() func()
	archiveCompletion(key string, job *Job) (archiveOutcome, error)

	// prepareJobChange may return nil; queueJobChangeAhead given nil records
	// the change afresh.
	prepareJobChange(ctx context.Context, job *Job,
		change func(*Job)) *jobChangeAhead
	queueJobChangeAhead(ctx context.Context, job *Job, ahead *jobChangeAhead,
		change func(*Job))
	moveJobChangeAhead(from, to *Job, ahead *jobChangeAhead) *jobChangeAhead
	discardJobChangeAhead(job *Job, ahead *jobChangeAhead)

	deleteLiveJobs(ctx context.Context, keys []string) error
	modifyLiveJobs(ctx context.Context, oldKeys []string, jobs []*Job) error
	storeLiveForRerun(jobs []*Job) error
	storeRunningRerunMarks(jobs []*Job) error

	retrieveCompleteJobsByKeys(keys []string) ([]*Job, error)
	retrieveCompleteJobsByRepGroup(repgroup string) ([]*Job, error)
	retrieveOldestCompleteJobsByRepGroup(repgroup string,
		query completeJobsQuery) (*completeJobsPage, error)
	retrieveCompleteJobStatusByRepGroup(repgroup string,
		includeDetails bool) (*RepGroupStatus, error)
	retrieveCompleteJobsRecent(cutoff time.Time) ([]*Job, error)
	retrieveLastCompletionTimeByRepGroup(
		repGroups []string) (map[string]time.Time, error)
	repGroupHasHistory(repGroup string) bool
	retrieveRepGroups() ([]string, error)
	spendArchivedBytesByRepGroup(repgroup string,
		budget *archivedBytesBudget) error
	retrieveJobStd(ctx context.Context, jobkey string) (stdo, stde []byte)

	retrieveEnv(ctx context.Context, envkey string) []byte
	retrieveLimitGroup(ctx context.Context, group string) *limiter.GroupData
	retrieveStoredLimits(groups []string) ([]int64, error)

	cachedReqGroupRecs(reqGroup string) (*scheduler.Requirements, bool)
	refreshReqGroupRecs(reqGroup string) *scheduler.Requirements
	refreshReqGroupRecsLater(reqGroup string)
	warmReqGroupRecs(jobs []*Job)
	reqStatChanges() uint32
	setReqStatRefreshInterval(d time.Duration)
	setRecRounding(secRound, mbRound int)
	setBatchTuning(delay time.Duration, size int)
}

// openStore opens the store described by cfg, returning it along with any
// startup message for the user. Before anything else it takes the store lock
// beside cfg.dbFile (see lockStore), which the store holds until it closes.
// Every kind currently opens bbolt (see initDB).
//
// If the WR_STORE_RECORD env var names a file, the store returned records every
// call made to it there (see recordingStore); otherwise it is not wrapped at
// all, so recording costs nothing unless asked for.
func openStore(ctx context.Context, cfg storeConfig) (store, string, error) {
	lock, err := lockStore(ctx, cfg.dbFile)
	if err != nil {
		return nil, "", err
	}

	bdb, msg, err := initDB(ctx, cfg.dbFile, cfg.dbBkFile, cfg.deployment, cfg.wipeDevDB, cfg.forceBackups)
	if err != nil {
		return nil, msg, errors.Join(err, lock.release())
	}

	bdb.storeLock = lock

	path := os.Getenv(storeRecordEnv)
	if path == "" {
		return bdb, msg, nil
	}

	recording, err := newRecordingStore(ctx, bdb, path)
	if err != nil {
		return nil, msg, errors.Join(err, bdb.close(ctx))
	}

	return recording, msg, nil
}

// storeConfig is what openStore needs to open a store.
type storeConfig struct {
	dbFile, dbBkFile, deployment string
	wipeDevDB, forceBackups      bool
	kind                         storeKind
}
