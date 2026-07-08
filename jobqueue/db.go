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

// errDBClosed is returned when an operation is attempted on a closed database.
var errDBClosed = errors.New("database closed")

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

type db struct {
	backupLast           time.Time
	backupPath           string
	backupPathTmp        string
	ch                   codec.Handle
	backupStopWait       chan bool
	backupMount          *muxfys.MuxFys
	backupNotification   chan bool
	backupWait           time.Duration
	bolt                 *bolt.DB
	envcache             *lru.ARCCache[string, []byte]
	updatingAfterJobExit int
	wg                   *waitgroup.WaitGroup
	wgMutex              sync.Mutex // protects wg since we want to call Wait() while another goroutine might call Add()
	sync.RWMutex
	backingUp      bool
	backupFinal    bool
	backupQueued   bool
	backupsEnabled bool
	s3accessor     *muxfys.S3Accessor
	closed         bool
	slowBackups    bool // just for testing purposes
	recSecRound    int  // rounding (secs) for recommended reserve times; from the server's timings
	recMBRound     int  // rounding (MBs) for recommended memory/disk; from the server's timings
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
		boltdb *bolt.DB
		err    error
	)
	if _, err = os.Stat(dbFile); os.IsNotExist(err) {
		if _, err = os.Stat(dbBkFile); os.IsNotExist(err) {
			boltdb, err = bolt.Open(dbFile, dbFilePermission, nil)
			msg = "created new empty db file " + dbFile
		} else {
			err = copyFile(dbBkFile, dbFile)
			if err != nil {
				return nil, msg, err
			}

			boltdb, err = bolt.Open(dbFile, dbFilePermission, nil)
			msg = "recreated missing db file " + dbFile + " from backup file " + dbBkFile
		}
	} else {
		boltdb, err = bolt.Open(dbFile, dbFilePermission, nil)
		if err != nil {
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
				backupDB, errbk := bolt.Open(bkPath, dbFilePermission, nil)
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

					boltdb, err = bolt.Open(dbFile, dbFilePermission, nil)
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

		if !hadDepGroups {
			upgrade.startPhase("rebuild dep-group index", "rebuilding database dependency-group index", 0)

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

		if !hadJobLookupEntries {
			upgrade.startPhase("rebuild job lookup index", "rebuilding database job lookup index", 0)

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
			upgrade.startPhase("commit database upgrade", "committing database upgrade", 0)
		}

		return nil
	})
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
		s3accessor:         accessor,
		wg:                 waitgroup.New(),
	}

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

// archiveJobTx is the transactional part of archiveJob: it moves the job from
// the live bucket to the complete bucket, removes its std buckets, records its
// resource-usage stats, updates its repgroup end time and records the job's end
// time in the time-ordered per-job end-time index. updateEndTimeIndex runs
// before the complete-record Put because it recovers the job's prior end time
// from that record to drop any stale forward index entry.
func (db *db) archiveJobTx(tx *bolt.Tx, key, encoded []byte, job *Job) error {
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

	oldJob := &Job{}
	if err := codec.NewDecoderBytes(oldEncoded, db.ch).Decode(oldJob); err != nil {
		return false, err
	}

	oldNanos := oldJob.EndTime.UnixNano()
	if oldNanos == newNanos {
		return false, nil
	}

	oldTimeBytes := endTimeToBytes(oldNanos)

	return true, tx.Bucket(bucketEndTimeToKey).Delete(endTimeIndexKey(oldTimeBytes, jobKey))
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

type dbUpgradeReporter struct {
	dbFile         string
	info           func(string, ...any)
	warn           func(string, ...any)
	startedAt      time.Time
	lastWrite      time.Time
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
		startedAt: time.Now(),
	}
}

func (r *dbUpgradeReporter) active() bool {
	return r != nil && r.started
}

func (r *dbUpgradeReporter) startPhase(state, detail string, processed int) {
	if r == nil {
		return
	}

	if !r.started {
		r.started = true
		r.info("database upgrade started", "db", r.dbFile)
	}

	r.phaseActive = true
	r.phaseStartedAt = time.Now()
	r.phaseState = state
	r.phaseDetail = detail
	r.phaseProcessed = processed

	r.writeStatus(state, detail, processed)
	r.info("database upgrade step started", "state", state, "detail", detail, "processed", processed)
}

func (r *dbUpgradeReporter) progress(state, detail string, processed int) {
	if r == nil || !r.started {
		return
	}

	if processed%dbUpgradeProgressEntries != 0 && time.Since(r.lastWrite) < dbUpgradeProgressInterval {
		return
	}

	r.phaseDetail = detail
	r.phaseProcessed = processed

	r.writeStatus(state, detail, processed)
	r.info("database upgrade progress", "state", state, "detail", detail, "processed", processed)
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
	r.info("database upgrade step complete", "state", state, "detail", detail,
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
		r.info("database upgrade complete", "db", r.dbFile, "took", time.Since(r.startedAt))
	}

	if err := internal.RemoveDBUpgradeStatus(r.dbFile); err != nil {
		r.warn("failed to remove database upgrade status", "path", internal.DBUpgradeStatusPath(r.dbFile), "err", err)
	}
}

func rebuildDepGroupEntries(depGroupBucket, lookupBucket *bolt.Bucket, progress *dbUpgradeReporter) (int, error) {
	processed := 0
	err := lookupBucket.ForEach(func(k, _ []byte) error {
		processed++
		progress.progress("rebuild dep-group index",
			fmt.Sprintf("rebuilding database dependency-group index (%d entries processed)", processed),
			processed)

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

func rebuildJobLookupBucket(tx *bolt.Tx, bucket []byte, progress *dbUpgradeReporter,
	processedBefore int,
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
		progress.progress("rebuild job lookup index",
			fmt.Sprintf("rebuilding database job lookup index from %s (%d entries processed, %d total)",
				bucketName, processed, totalProcessed),
			totalProcessed)

		return putReverseLookupEntry(tx, bucket, k)
	})

	return processed, err
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
// being re-run), then it will be appended to (a copy of) the input job slice
// and returned in jobsToQueue. If the affected job was in the live bucket
// (currently queued), it will be returned in the jobsToUpdate slice: you should
// use queue methods to update the job in the queue.
//
// Finally, it triggers a background database backup.
func (db *db) storeNewJobs(ctx context.Context, jobs []*Job, ignoreAdded bool) (
	jobsToQueue, jobsToUpdate []*Job, alreadyAdded int, err error,
) {
	encodedJobs, rgLookups, dgLookups, depGroupsSeen, rdgLookups, rgs,
		jobsToQueue, jobsToUpdate, alreadyAdded, err := db.prepareNewJobs(jobs, ignoreAdded)
	if err != nil {
		return jobsToQueue, jobsToUpdate, alreadyAdded, err
	}

	if len(encodedJobs) > 0 {
		err = db.storeNewJobData(ctx, encodedJobs, rgLookups, dgLookups, depGroupsSeen, rdgLookups, rgs)
	}

	// *** on error, because we were batching, and doing lookups separately to
	// each other and jobs, we should go through and remove anything we did
	// manage to add... (but this isn't so critical, since on failure here,
	// they are not added to the in-memory queue and user gets an error and they
	// would try to add everything back again; conversely, if we try to retrieve
	// non-existent jobs based on lookups that shouldn't be there, they are
	// silently skipped)

	if err == nil && alreadyAdded != len(jobs) {
		db.backgroundBackup(ctx)
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
func (db *db) storeNewJobData(ctx context.Context, encodedJobs, rgLookups, dgLookups,
	depGroupsSeen, rdgLookups, rgs sobsd) error {
	stores := []batchStore{{"rglookups", bucketRTK, rgLookups, db.storeLookups}}

	for _, s := range []batchStore{
		{"repGroups", bucketRGs, rgs, db.storeLookups},
		{"dgLookups", bucketDTK, dgLookups, db.storeLookups},
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
func (db *db) prepareNewJobs(jobs []*Job, ignoreAdded bool) (encodedJobs, rgLookups, dgLookups, depGroupsSeen, rdgLookups, rgs sobsd, jobsToQueue []*Job, jobsToUpdate []*Job, alreadyAdded int, err error) {
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
				return encodedJobs, rgLookups, dgLookups, depGroupsSeen, rdgLookups, rgs,
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
				dgLookups = append(dgLookups, [2][]byte{db.generateLookupKey(depGroup, key), nil})
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
			return encodedJobs, rgLookups, dgLookups, depGroupsSeen, rdgLookups, rgs,
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
					return encodedJobs, rgLookups, dgLookups, depGroupsSeen, rdgLookups, rgs,
						jobsToQueue, jobsToUpdate, alreadyAdded, err
				}

				encodedJobs = append(encodedJobs, [2][]byte{key, encoded})
			}

			if len(jobsToQueue) > 0 {
				jobsToQueue = append(jobsToQueue, jobs...)
			} else {
				jobsToQueue = keptJobs
			}
		} else {
			jobsToQueue = keptJobs
		}

		for rg := range repGroups {
			rgs = append(rgs, [2][]byte{[]byte(rg), nil})
		}

		for depGroup := range depGroups {
			depGroupsSeen = append(depGroupsSeen, [2][]byte{[]byte(depGroup), nil})
		}
	}

	return encodedJobs, rgLookups, dgLookups, depGroupsSeen, rdgLookups, rgs, jobsToQueue, jobsToUpdate, alreadyAdded, err
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
		newJobBucket := tx.Bucket(bucketJobsLive)
		if newJobBucket.Get([]byte(key)) != nil {
			isLive = true
		}

		return nil
	})

	return isLive, err
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
// happen - no checking is done! A backgroundBackup() is triggered afterwards.
func (db *db) archiveJob(ctx context.Context, key string, job *Job) error {
	var encoded []byte

	enc := codec.NewEncoderBytes(&encoded, db.ch)

	job.RLock()
	err := enc.Encode(job)
	job.RUnlock()

	if err != nil {
		return err
	}

	err = db.bolt.Batch(func(tx *bolt.Tx) error {
		return db.archiveJobTx(tx, []byte(key), encoded, job)
	})

	db.backgroundBackup(ctx)

	return err
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

	secs := int(math.Ceil(job.EndTime.Sub(job.StartTime).Seconds()))

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

	db.backgroundBackup(ctx)
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
				dec := codec.NewDecoderBytes(encoded, db.ch)
				job := &Job{}

				errf := dec.Decode(job)
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
				dec := codec.NewDecoderBytes(encoded, db.ch)
				job := &Job{}

				err := dec.Decode(job)
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

// decodeArchivedJob decodes the job with the given key from the complete bucket,
// returning nil (and no error) if it is not present there or is currently live
// (being re-run).
func (db *db) decodeArchivedJob(completeJobBucket, newJobBucket *bolt.Bucket, key []byte) (*Job, error) {
	encoded := completeJobBucket.Get(key)
	if len(encoded) == 0 || newJobBucket.Get(key) != nil {
		return nil, nil //nolint:nilnil // absent/live job is a valid non-error nil result
	}

	dec := codec.NewDecoderBytes(encoded, db.ch)
	job := &Job{}

	if err := dec.Decode(job); err != nil {
		return nil, err
	}

	return job, nil
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

	dec := codec.NewDecoderBytes(encoded, db.ch)

	job := &Job{}
	if err := dec.Decode(job); err != nil {
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

	dec := codec.NewDecoderBytes(encoded, s.db.ch)
	job := &Job{}

	if err := dec.Decode(job); err != nil {
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
		newJobBucket := tx.Bucket(bucketJobsLive)
		lookupBucket := tx.Bucket(bucketDTK).Cursor()

		prefix := []byte(depgroup + dbDelimiter)
		for k, _ := lookupBucket.Seek(prefix); bytes.HasPrefix(k, prefix); k, _ = lookupBucket.Next() {
			key := bytes.TrimPrefix(k, prefix)
			if newJobBucket.Get(key) != nil {
				jobKeys = append(jobKeys, string(key))
			}
		}

		return nil
	})

	return jobKeys, err
}

func (db *db) depGroupEverSeen(depGroup string) (bool, error) {
	var seen bool

	err := db.bolt.View(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketDepGroups)
		seen = b.Get([]byte(depGroup)) != nil

		return nil
	})

	return seen, err
}

func (db *db) depGroupsEverSeen(depGroups []string) (map[string]bool, error) {
	seen := make(map[string]bool, len(depGroups))

	err := db.bolt.View(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketDepGroups)
		for _, depGroup := range depGroups {
			seen[depGroup] = b.Get([]byte(depGroup)) != nil
		}

		return nil
	})

	return seen, err
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

	db.updatingAfterJobExit++

	db.wgMutex.Lock()
	defer db.wgMutex.Unlock()

	db.launchJobExitUpdate(ctx, exit)
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

// launchJobExitUpdate runs exit.update in a background batch, decrementing the
// in-progress counter when done. Must be called with db.Lock and db.wgMutex
// held.
func (db *db) launchJobExitUpdate(ctx context.Context, exit jobExitData) {
	wgk := db.wg.Add(1)

	go func() {
		defer internal.LogPanic(ctx, "updateJobAfterExit", true)

		err := db.bolt.Batch(exit.update)
		db.wg.Done(wgk)

		if err != nil {
			clog.Error(ctx, "Database operation updateJobAfterExit failed", "err", err)
		}

		db.Lock()
		db.updatingAfterJobExit--
		db.Unlock()
	}()
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

	db.launchJobChangeUpdate(ctx, key, encoded)
}

// launchJobChangeUpdate rewrites the live job in a background batch and triggers
// a backup. Must be called with db.RLock and db.wgMutex held.
func (db *db) launchJobChangeUpdate(ctx context.Context, key, encoded []byte) {
	wgk := db.wg.Add(1)

	go func() {
		defer internal.LogPanic(ctx, "updateJobAfterChange", true)

		err := db.bolt.Batch(func(tx *bolt.Tx) error {
			bjl := tx.Bucket(bucketJobsLive)
			if bjl.Get(key) == nil {
				// it's possible for these batches to be interleaved with
				// archiveJob batches, and for this batch to update that a job
				// was started to actually execute after the batch that says the
				// job completed, removing it from the live bucket. In that
				// case, don't add it back to the live bucket here.
				return nil
			}

			return bjl.Put(key, encoded)
		})
		db.wg.Done(wgk)

		if err != nil {
			clog.Error(ctx, "Database operation updateJobAfterChange failed", "err", err)

			return
		}

		db.backgroundBackup(ctx)
	}()
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
	encodedJobs, rgLookups, dgLookups, depGroupsSeen, rdgLookups, rgs, _, _, _, err := db.prepareNewJobs(jobs, false)
	if err != nil {
		return err
	}

	lookups := newJobLookups(rgLookups, rgs, dgLookups, depGroupsSeen, rdgLookups)

	sort.Sort(encodedJobs)

	err = db.bolt.Batch(func(tx *bolt.Tx) error {
		return db.modifyLiveJobsTx(tx, oldKeys, jobs, encodedJobs, lookups)
	})
	if err != nil {
		clog.Error(ctx, "Database error during modify", "err", err)
	}

	go db.backgroundBackup(ctx)

	return err
}

// jobLookups groups the various lookup index sobsds that accompany a set of
// jobs in the database.
type jobLookups struct {
	rg         sobsd
	repGroups  sobsd
	dg         sobsd
	depGroups  sobsd
	reverseDep sobsd
}

// newJobLookups sorts and groups the lookup sobsds prepared by prepareNewJobs.
func newJobLookups(rgLookups, rgs, dgLookups, depGroupsSeen, rdgLookups sobsd) jobLookups {
	sort.Sort(rgLookups)
	sort.Sort(rgs)
	sort.Sort(dgLookups)
	sort.Sort(depGroupsSeen)
	sort.Sort(rdgLookups)

	return jobLookups{
		rg:         rgLookups,
		repGroups:  rgs,
		dg:         dgLookups,
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
		{bucketDTK, lookups.dg},
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

		err := lookupBucket.Delete(d.key)
		if err != nil {
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
	for {
		db.RLock()

		if db.updatingAfterJobExit == 0 {
			db.RUnlock()

			return
		}

		db.RUnlock()
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

// close shuts down the db, should be used prior to exiting. Ensures any
// ongoing backgroundBackup() completes first (but does not wait for backup() to
// complete).
func (db *db) close(ctx context.Context) error {
	db.Lock()
	defer db.Unlock()

	if db.closed {
		return nil
	}

	db.closed = true

	// before actually closing, wait for any go routines doing database
	// transactions to complete
	db.waitForOngoingTransactions()

	// do a final backup
	if db.backupsEnabled && db.backupQueued {
		clog.Debug(ctx, "Jobqueue database not backed up, will do final backup")
		db.backupToBackupFile(ctx, false)
	}

	return db.closeBolt()
}

// waitForOngoingTransactions waits, with db.Lock held, for any in-progress
// background backup and database transaction goroutines to finish. It
// temporarily releases db.Lock while waiting, and re-acquires it before
// returning.
func (db *db) waitForOngoingTransactions() {
	if db.backingUp {
		db.backupFinal = true
		close(db.backupStopWait)
		db.Unlock()
		<-db.backupNotification
	} else {
		db.Unlock()
	}

	db.wgMutex.Lock()
	db.wg.Wait(dbRunningTransactionsWaitTime)
	db.wgMutex.Unlock()
	db.Lock()
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
// backup if any other backup requests come in while a backup is running. Any
// errors are silently ignored. Spaces out sequential backups so that there is a
// gap of max(30s, [time taken to complete previous backup]) seconds between
// them.
func (db *db) backgroundBackup(ctx context.Context) {
	db.Lock()
	defer db.Unlock()

	if db.closed || !db.backupsEnabled {
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
	db.Lock()
	db.backingUp = false
	db.backupLast = time.Now()

	duration := time.Since(start)
	if duration > minimumTimeBetweenBackups {
		db.backupWait = duration
	}

	if db.backupFinal {
		// close() has been called, don't do any more backups and tell close()
		// we finished our backup
		db.backupFinal = false
		db.backupStopWait = make(chan bool)
		db.Unlock()

		db.backupNotification <- true

		return
	}

	if db.backupQueued {
		db.backupQueued = false
		db.Unlock()
		db.backgroundBackup(ctx)
	} else {
		db.Unlock()
	}
}

// backupToBackupFile is used by backgroundBackup() and close() to do the actual
// backup.
func (db *db) backupToBackupFile(ctx context.Context, slowBackups bool) {
	// we most likely triggered this backup immediately following an operation
	// that alters (the important parts of) the database; wait for those
	// transactions to actually complete before backing up
	db.wgMutex.Lock()
	db.wg.Wait(dbRunningTransactionsWaitTime)

	wgk := db.wg.Add(1)

	db.wgMutex.Unlock()
	defer db.wg.Done(wgk)

	// create the new backup file with temp name
	tmpBackupPath := db.backupPathTmp

	err := db.bolt.View(func(tx *bolt.Tx) error {
		return tx.CopyFile(tmpBackupPath, dbFilePermission)
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
	totalProcessed := 0

	for _, bucket := range indexedLookupBuckets() {
		processed, err := rebuildJobLookupBucket(tx, bucket, progress, totalProcessed)
		if err != nil {
			return err
		}

		totalProcessed += processed
	}

	progress.completePhase("rebuild job lookup index",
		fmt.Sprintf("rebuilt database job lookup index (%d entries processed)", totalProcessed),
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
		progress.completePhase("rebuild dep-group index",
			"rebuilt database dependency-group index (0 entries processed)",
			0)

		return nil
	}

	processed, err := rebuildDepGroupEntries(depGroupBucket, lookupBucket, progress)
	if err != nil {
		return err
	}

	progress.completePhase("rebuild dep-group index",
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
