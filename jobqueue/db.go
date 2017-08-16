// Copyright © 2016 Genome Research Limited
// Author: Sendu Bala <sb10@sanger.ac.uk>.
//
//  This file is part of wr.
//
//  wr is free software: you can redistribute it and/or modify
//  it under the terms of the GNU Lesser General Public License as published by
//  the Free Software Foundation, either version 3 of the License, or
//  (at your option) any later version.
//
//  wr is distributed in the hope that it will be useful,
//  but WITHOUT ANY WARRANTY; without even the implied warranty of
//  MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
//  GNU Lesser General Public License for more details.
//
//  You should have received a copy of the GNU Lesser General Public License
//  along with wr. If not, see <http://www.gnu.org/licenses/>.

package jobqueue

// This file contains functions for interacting with our database, which is
// boltdb, a simple key/val store with transactions and hot backup ability.
// We don't use a generic ORM for boltdb like Storm, because we can do custom
// queries that are multiple times faster than what Storm can do.

import (
	"bytes"
	"fmt"
	"github.com/boltdb/bolt"
	"github.com/hashicorp/golang-lru"
	"github.com/ugorji/go/codec"
	"io"
	"math"
	"os"
	"sort"
	"strconv"
	"sync"
	"time"
)

const (
	dbDelimiter          = "_::_"
	jobStatWindowPercent = float32(5)
	dbFilePermission     = 0600
)

var (
	bucketJobsLive     = []byte("jobslive")
	bucketJobsComplete = []byte("jobscomplete")
	bucketRTK          = []byte("repgroupToKey")
	bucketDTK          = []byte("depgroupToKey")
	bucketRDTK         = []byte("reverseDepgroupToKey")
	bucketEnvs         = []byte("envs")
	bucketStdO         = []byte("stdo")
	bucketStdE         = []byte("stde")
	bucketJobMBs       = []byte("jobMBs")
	bucketJobSecs      = []byte("jobSecs")
	wipeDevDBOnInit    = true
)

// Rec* variables are only exported for testing purposes (*** though they should
// probably be user configurable somewhere...).
var (
	RecMBRound  = 100  // when we recommend amount of memory to reserve for a job, we round up to the nearest RecMBRound MBs
	RecSecRound = 1800 // when we recommend time to reserve for a job, we round up to the nearest RecSecRound seconds
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
	if cmp == -1 {
		return true
	}
	return false
}

// sobsdStorer is the kind of function that stores the contents of a sobsd in
// a particular bucket
type sobsdStorer func(bucket []byte, encodes sobsd) (err error)

type db struct {
	bolt                 *bolt.DB
	envcache             *lru.ARCCache
	ch                   codec.Handle
	updatingAfterJobExit int
	backupPath           string
	backingUp            bool
	backupQueued         bool
	backupFinal          bool
	backupNotification   chan bool
	slowBackups          bool // just for testing purposes
	closed               bool
	sync.RWMutex
}

// initDB opens/creates our database and sets things up for use. If dbFile
// doesn't exist or seems corrupted, we copy it from backup if that exists,
// otherwise we start fresh. In development we delete any existing db and force
// a fresh start.
func initDB(dbFile string, dbBkFile string, deployment string) (dbstruct *db, msg string, err error) {
	if wipeDevDBOnInit && deployment == "development" {
		os.Remove(dbFile)
		os.Remove(dbBkFile)
	}

	var boltdb *bolt.DB
	if _, err = os.Stat(dbFile); os.IsNotExist(err) {
		if _, err = os.Stat(dbBkFile); os.IsNotExist(err) { //*** need to handle bk being on another machine, possibly an S3-style object store
			boltdb, err = bolt.Open(dbFile, dbFilePermission, nil)
			msg = "created new empty db file " + dbFile
		} else {
			// copy bk to main *** need to handle bk being in an object store
			err = copyFile(dbBkFile, dbFile)
			if err != nil {
				return
			}
			boltdb, err = bolt.Open(dbFile, dbFilePermission, nil)
			msg = "recreated missing db file " + dbFile + " from backup file " + dbBkFile
		}
	} else {
		boltdb, err = bolt.Open(dbFile, dbFilePermission, nil)
		if err != nil {
			// try the backup *** again, need to handle bk being elsewhere
			if _, errbk := os.Stat(dbBkFile); errbk == nil {
				boltdb, errbk = bolt.Open(dbBkFile, dbFilePermission, nil)
				if errbk == nil {
					origerr := err
					msg = fmt.Sprintf("tried to recreate corrupt (?) db file %s from backup file %s (error with original db file was: %s)", dbFile, dbBkFile, err)
					err = os.Remove(dbFile)
					if err != nil {
						return
					}
					err = copyFile(dbBkFile, dbFile)
					if err != nil {
						return
					}
					boltdb, err = bolt.Open(dbFile, dbFilePermission, nil)
					msg = fmt.Sprintf("recreated corrupt (?) db file %s from backup file %s (error with original db file was: %s)", dbFile, dbBkFile, origerr)
				}
			}
		}
	}
	if err != nil {
		return
	}

	// ensure our buckets are in place
	err = boltdb.Update(func(tx *bolt.Tx) error {
		_, err := tx.CreateBucketIfNotExists(bucketJobsLive)
		if err != nil {
			return fmt.Errorf("create bucket %s: %s", bucketJobsLive, err)
		}
		_, err = tx.CreateBucketIfNotExists(bucketJobsComplete)
		if err != nil {
			return fmt.Errorf("create bucket %s: %s", bucketJobsComplete, err)
		}
		_, err = tx.CreateBucketIfNotExists(bucketRTK)
		if err != nil {
			return fmt.Errorf("create bucket %s: %s", bucketRTK, err)
		}
		_, err = tx.CreateBucketIfNotExists(bucketDTK)
		if err != nil {
			return fmt.Errorf("create bucket %s: %s", bucketDTK, err)
		}
		_, err = tx.CreateBucketIfNotExists(bucketRDTK)
		if err != nil {
			return fmt.Errorf("create bucket %s: %s", bucketRDTK, err)
		}
		_, err = tx.CreateBucketIfNotExists(bucketEnvs)
		if err != nil {
			return fmt.Errorf("create bucket %s: %s", bucketEnvs, err)
		}
		_, err = tx.CreateBucketIfNotExists(bucketStdO)
		if err != nil {
			return fmt.Errorf("create bucket %s: %s", bucketStdO, err)
		}
		_, err = tx.CreateBucketIfNotExists(bucketStdE)
		if err != nil {
			return fmt.Errorf("create bucket %s: %s", bucketStdE, err)
		}
		_, err = tx.CreateBucketIfNotExists(bucketJobMBs)
		if err != nil {
			return fmt.Errorf("create bucket %s: %s", bucketJobMBs, err)
		}
		_, err = tx.CreateBucketIfNotExists(bucketJobSecs)
		if err != nil {
			return fmt.Errorf("create bucket %s: %s", bucketJobSecs, err)
		}
		return nil
	})
	if err != nil {
		return
	}

	// we will cache frequently used things to avoid actual db (disk) access
	envcache, err := lru.NewARC(12) // we don't expect that many different ENVs to be in use at once
	if err != nil {
		return
	}

	dbstruct = &db{
		bolt:               boltdb,
		envcache:           envcache,
		ch:                 new(codec.BincHandle),
		backupPath:         dbBkFile,
		backupNotification: make(chan bool),
	}
	return
}

// storeNewJobs stores jobs in the live bucket, where they will only be used for
// disaster recovery. It also stores a lookup from the Job.RepGroup to the Job's
// key, and since this is independent, and we call this prior to checking for
// dups, we allow the same job to be looked up by multiple RepGroups. Likewise,
// we store a lookup for the Job.DepGroups and .Dependencies.DepGroups().
//
// If ignoreAdded is true, jobs that have already completed will be ignored
// along with those that have been added and the returned alreadyAdded value
// will increase.
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
func (db *db) storeNewJobs(jobs []*Job, ignoreAdded bool) (jobsToQueue []*Job, jobsToUpdate []*Job, alreadyAdded int, err error) {
	// turn the jobs in to sobsd and sort by their keys, likewise for the
	// lookups
	var encodedJobs sobsd
	var rgLookups sobsd
	var dgLookups sobsd
	var rdgLookups sobsd
	depGroups := make(map[string]bool)
	newJobKeys := make(map[string]bool)
	var keptJobs []*Job
	for _, job := range jobs {
		keyStr := job.key()

		if ignoreAdded {
			var added bool
			added, err = db.checkIfAdded(keyStr)
			if err != nil {
				return
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
		err = enc.Encode(job)
		if err != nil {
			return
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
				key := []byte(job.key())
				var encoded []byte
				enc := codec.NewEncoderBytes(&encoded, db.ch)
				err = enc.Encode(job)
				if err != nil {
					return
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

		// now go ahead and store the lookups and jobs
		numStores := 2
		if len(dgLookups) > 0 {
			numStores++
		}
		if len(rdgLookups) > 0 {
			numStores++
		}
		errors := make(chan error, numStores)

		go func() {
			sort.Sort(rgLookups)
			errors <- db.storeBatched(bucketRTK, rgLookups, db.storeLookups)
		}()

		if len(dgLookups) > 0 {
			go func() {
				sort.Sort(dgLookups)
				errors <- db.storeBatched(bucketDTK, dgLookups, db.storeLookups)
			}()
		}

		if len(rdgLookups) > 0 {
			go func() {
				sort.Sort(rdgLookups)
				errors <- db.storeBatched(bucketRDTK, rdgLookups, db.storeLookups)
			}()
		}

		go func() {
			sort.Sort(encodedJobs)
			errors <- db.storeBatched(bucketJobsLive, encodedJobs, db.storeEncodedJobs)
		}()

		seen := 0
		for thisErr := range errors {
			if thisErr != nil {
				err = thisErr
			}

			seen++
			if seen == numStores {
				close(errors)
				break
			}
		}
	}

	// *** on error, because we were batching, and doing lookups separately to
	// each other and jobs, we should go through and remove anything we did
	// manage to add... (but this isn't so critical, since on failure here,
	// they are not added to the in-memory queue and user gets an error and they
	// would try to add everything back again; conversely, if we try to retrieve
	// non-existent jobs based on lookups that shouldn't be there, they are
	// silently skipped)

	if err == nil && alreadyAdded != len(jobs) {
		db.backgroundBackup()
	}

	return
}

// generateLookupKey creates a lookup key understood by the retrieval methods,
// concatenating prefix with a delimiter and the job key.
func (db *db) generateLookupKey(prefix string, jobKey []byte) []byte {
	key := append([]byte(prefix), []byte(dbDelimiter)...)
	key = append(key, jobKey...)
	return key
}

// checkIfLive tells you if a job with the given key is currently in the live
// bucket.
func (db *db) checkIfLive(key string) (isLive bool, err error) {
	err = db.bolt.View(func(tx *bolt.Tx) error {
		newJobBucket := tx.Bucket(bucketJobsLive)
		if newJobBucket.Get([]byte(key)) != nil {
			isLive = true
		}
		return nil
	})
	return
}

// checkIfAdded tells you if a job with the given key is currently in the
// complete bucket or the live bucket.
func (db *db) checkIfAdded(key string) (isInDB bool, err error) {
	err = db.bolt.View(func(tx *bolt.Tx) error {
		newJobBucket := tx.Bucket(bucketJobsLive)
		completeJobBucket := tx.Bucket(bucketJobsComplete)
		if newJobBucket.Get([]byte(key)) != nil || completeJobBucket.Get([]byte(key)) != nil {
			isInDB = true
		}
		return nil
	})
	return
}

// archiveJob deletes a job from the live bucket, and adds a new version of it
// (with different properties) to the complete bucket. The key you supply must
// be the key of the job you supply, or bad things will happen - no checking is
// done! A backgroundBackup() is triggered afterwards.
func (db *db) archiveJob(key string, job *Job) (err error) {
	var encoded []byte
	enc := codec.NewEncoderBytes(&encoded, db.ch)
	err = enc.Encode(job)
	if err != nil {
		return
	}

	err = db.bolt.Batch(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketJobsLive)
		b.Delete([]byte(key))

		b = tx.Bucket(bucketJobsComplete)
		err := b.Put([]byte(key), encoded)
		return err
	})

	db.backgroundBackup()

	return
}

// deleteLiveJob remove a job from the live bucket, for use when jobs were
// added in error.
func (db *db) deleteLiveJob(key string) {
	db.remove(bucketJobsLive, key)
	db.backgroundBackup()
	//*** we're not removing the lookup entries from the bucket*TK buckets...
}

// recoverIncompleteJobs returns all jobs in the live bucket, for use when
// restarting the server, allowing you start working on any jobs that were
// stored with storeNewJobs() but not yet archived with archiveJob(). Note that
// any state changes to the Jobs that may have occurred will be lost: you get
// back the Jobs exactly as they were when you put them in with storeNewJobs().
func (db *db) recoverIncompleteJobs() (jobs []*Job, err error) {
	err = db.bolt.View(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketJobsLive)
		b.ForEach(func(_, encoded []byte) error {
			if encoded != nil {
				dec := codec.NewDecoderBytes(encoded, db.ch)
				job := &Job{}
				err = dec.Decode(job)
				if err != nil {
					return err
				}
				jobs = append(jobs, job)
			}
			return nil
		})
		return nil
	})
	return
}

// retrieveCompleteJobsByKeys gets jobs with the given keys from the completed
// jobs bucket (ie. those that have gone through the queue and been Remove()d).
func (db *db) retrieveCompleteJobsByKeys(keys []string, getstd bool, getenv bool) (jobs []*Job, err error) {
	err = db.bolt.View(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketJobsComplete)
		for _, key := range keys {
			encoded := b.Get([]byte(key))
			if encoded != nil {
				dec := codec.NewDecoderBytes(encoded, db.ch)
				job := &Job{}
				err = dec.Decode(job)
				if err == nil {
					jobs = append(jobs, job)
				}
			}
		}
		return nil
	})
	return
}

// retrieveCompleteJobsByRepGroup gets jobs with the given RepGroup from the
// completed jobs bucket (ie. those that have gone through the queue and been
// Archive()d), but not those that are also currently live (ie. are being
// re-run).
func (db *db) retrieveCompleteJobsByRepGroup(repgroup string) (jobs []*Job, err error) {
	err = db.bolt.View(func(tx *bolt.Tx) error {
		newJobBucket := tx.Bucket(bucketJobsLive)
		completeJobBucket := tx.Bucket(bucketJobsComplete)
		lookupBucket := tx.Bucket(bucketRTK).Cursor()
		prefix := []byte(repgroup + dbDelimiter)
		for k, _ := lookupBucket.Seek(prefix); bytes.HasPrefix(k, prefix); k, _ = lookupBucket.Next() {
			key := bytes.TrimPrefix(k, prefix)
			encoded := completeJobBucket.Get(key)
			if len(encoded) > 0 && newJobBucket.Get(key) == nil {
				dec := codec.NewDecoderBytes(encoded, db.ch)
				job := &Job{}
				err = dec.Decode(job)
				if err != nil {
					return err
				}
				jobs = append(jobs, job)
			}
		}
		return nil
	})
	return
}

// retrieveDependentJobs gets previously stored jobs that had a dependency on
// one for the input depGroups. If the job is found in the live bucket, then it
// is returned in the jobsToUpdate return value. If it is found in the complete
// bucket, and is not true in the supplied newJobKeys map, then it is returned
// in the jobsToQueue return value.
func (db *db) retrieveDependentJobs(depGroups map[string]bool, newJobKeys map[string]bool) (jobsToQueue []*Job, jobsToUpdate []*Job, err error) {
	// first convert the depGroups in to sorted prefixes, for linear searching
	var prefixes sobsd
	for depGroup := range depGroups {
		prefixes = append(prefixes, [2][]byte{[]byte(depGroup + dbDelimiter), nil})
	}
	sort.Sort(prefixes)

	err = db.bolt.View(func(tx *bolt.Tx) error {
		newJobBucket := tx.Bucket(bucketJobsLive)
		completeJobBucket := tx.Bucket(bucketJobsComplete)
		lookupBucket := tx.Bucket(bucketRDTK).Cursor()
		doneKeys := make(map[string]bool)
		for {
			newDepGroups := make(map[string]bool)
			for _, bsd := range prefixes {
				for k, _ := lookupBucket.Seek(bsd[0]); bytes.HasPrefix(k, bsd[0]); k, _ = lookupBucket.Next() {
					key := bytes.TrimPrefix(k, bsd[0])
					keyStr := string(key)
					if doneKeys[keyStr] {
						continue
					}

					encoded := newJobBucket.Get(key)
					live := false
					if len(encoded) > 0 {
						live = true
					} else if !newJobKeys[keyStr] {
						encoded = completeJobBucket.Get(key)
					}

					if len(encoded) > 0 {
						dec := codec.NewDecoderBytes(encoded, db.ch)
						job := &Job{}
						err = dec.Decode(job)
						if err != nil {
							return err
						}

						// since we're going to add this job, we also need to
						// check its DepGroups and repeat this loop on any new
						// ones
						for _, depGroup := range job.DepGroups {
							if depGroup != "" && !depGroups[depGroup] {
								newDepGroups[depGroup] = true
							}
						}

						if live {
							jobsToUpdate = append(jobsToUpdate, job)
						} else {
							jobsToQueue = append(jobsToQueue, job)
						}
					}

					doneKeys[keyStr] = true
				}
			}

			if len(newDepGroups) > 0 {
				var newPrefixes sobsd
				for depGroup := range newDepGroups {
					newPrefixes = append(newPrefixes, [2][]byte{[]byte(depGroup + dbDelimiter), nil})
					depGroups[depGroup] = true
				}
				sort.Sort(newPrefixes)
				prefixes = newPrefixes
			} else {
				break
			}
		}
		return nil
	})
	return
}

// retrieveIncompleteJobKeysByDepGroup gets jobs with the given RepGroup from
// the live bucket (ie. those that have been added to the queue and not yet
// Archive()d - even if they've been added and archived in the past).
func (db *db) retrieveIncompleteJobKeysByDepGroup(depgroup string) (jobKeys []string, err error) {
	return db.retrieveIncompleteJobsKeysByGroup(depgroup, bucketDTK)
}

// retrieveIncompleteJobKeysByDepGroupDependency gets jobs that had a dependency
// on jobs with the given RepGroup from the live bucket (ie. those that have
// been added to the queue and not yet Archive()d - even if they've been added
// and archived in the past).
func (db *db) retrieveIncompleteJobKeysByDepGroupDependency(depgroup string) (jobKeys []string, err error) {
	return db.retrieveIncompleteJobsKeysByGroup(depgroup, bucketRDTK)
}

// retrieveIncompleteJobsKeysByGroup gets job keys with the given group (using
// the lookup from the given bucket) from the live jobs bucket.
func (db *db) retrieveIncompleteJobsKeysByGroup(group string, bucket []byte) (jobKeys []string, err error) {
	err = db.bolt.View(func(tx *bolt.Tx) error {
		newJobBucket := tx.Bucket(bucketJobsLive)
		lookupBucket := tx.Bucket(bucket).Cursor()
		prefix := []byte(group + dbDelimiter)
		for k, _ := lookupBucket.Seek(prefix); bytes.HasPrefix(k, prefix); k, _ = lookupBucket.Next() {
			key := bytes.TrimPrefix(k, prefix)
			if newJobBucket.Get(key) != nil {
				jobKeys = append(jobKeys, string(key))
			}
		}
		return nil
	})
	return
}

// storeEnv stores a clientRequest.Env in db unless cached, which means it must
// already be there. Returns a key by which the stored Env can be retrieved.
func (db *db) storeEnv(env []byte) (envkey string, err error) {
	envkey = byteKey(env)
	if !db.envcache.Contains(envkey) {
		err = db.store(bucketEnvs, envkey, env)
		if err != nil {
			return
		}
		db.envcache.Add(envkey, env)
	}
	return
}

// retrieveEnv gets a value from the db that was stored with storeEnv(). The
// value may come from the cache, avoiding db access.
func (db *db) retrieveEnv(envkey string) (envc []byte) {
	cached, got := db.envcache.Get(envkey)
	if got {
		envc = cached.([]byte)
	} else {
		envc = db.retrieve(bucketEnvs, envkey)
		db.envcache.Add(envkey, envc)
	}
	return
}

// updateJobAfterExit stores the Job's peak RAM usage and wall time against the
// Job's ReqGroup, allowing recommendedReqGroup*(ReqGroup) to work. It also
// updates the stdout/err associated with a job. We don't want to store these in
// the job, since that would waste a lot of the queue's memory; we store in db
// instead, and only retrieve when a client needs to see these. To stop the db
// file becoming enormous, we only store these if the cmd failed (or if
// forceStorage is true: used when the job got buried) and also delete these
// from db when the cmd completes successfully. By doing the deletion upfront,
// we also ensure we have the latest std, which may be nil even on cmd failure.
// Since it is not critical to the running of jobs and workflows that this works
// 100% of the time, we ignore errors and write to bolt in a goroutine, giving
// us a significant speed boost.
func (db *db) updateJobAfterExit(job *Job, stdo []byte, stde []byte, forceStorage bool) {
	jobkey := job.key()
	job.RLock()
	secs := int(math.Ceil(job.EndTime.Sub(job.StartTime).Seconds()))
	jrg := job.ReqGroup
	jpm := job.PeakRAM
	jec := job.Exitcode
	job.RUnlock()
	go func() {
		db.Lock()
		db.updatingAfterJobExit++
		db.Unlock()
		db.bolt.Batch(func(tx *bolt.Tx) error {
			bo := tx.Bucket(bucketStdO)
			be := tx.Bucket(bucketStdE)
			key := []byte(jobkey)
			bo.Delete(key)
			be.Delete(key)

			var err error
			if jec != 0 || forceStorage {
				if len(stdo) > 0 {
					err = bo.Put(key, stdo)
				}
				if len(stde) > 0 {
					err = be.Put(key, stde)
				}
			}
			if err != nil {
				return err
			}

			b := tx.Bucket(bucketJobMBs)
			err = b.Put([]byte(fmt.Sprintf("%s%s%20d", jrg, dbDelimiter, jpm)), []byte(strconv.Itoa(jpm)))
			if err != nil {
				return err
			}
			b = tx.Bucket(bucketJobSecs)
			err = b.Put([]byte(fmt.Sprintf("%s%s%20d", jrg, dbDelimiter, secs)), []byte(strconv.Itoa(secs)))

			return err
		})
		db.Lock()
		db.updatingAfterJobExit--
		db.Unlock()
	}()
}

// retrieveJobStd gets the values that were stored using updateJobStd() for the
// given job.
func (db *db) retrieveJobStd(jobkey string) (stdo []byte, stde []byte) {
	// first wait for any existing updateJobAfterExit() calls to complete
	//*** this method of waiting seems really bad and should be improved, but in
	//    practice we probably never wait
	for {
		db.RLock()
		if db.updatingAfterJobExit == 0 {
			db.RUnlock()
			break
		}
		db.RUnlock()
		<-time.After(10 * time.Millisecond)
	}

	db.bolt.View(func(tx *bolt.Tx) error {
		bo := tx.Bucket(bucketStdO)
		be := tx.Bucket(bucketStdE)
		key := []byte(jobkey)
		o := bo.Get(key)
		if o != nil {
			stdo = make([]byte, len(o))
			copy(stdo, o)
		}
		e := be.Get(key)
		if e != nil {
			stde = make([]byte, len(e))
			copy(stde, e)
		}
		return nil
	})
	return
}

// recommendedReqGroupMemory returns the 95th percentile peak memory usage of
// all jobs that previously ran with the given reqGroup. If there are too few
// prior values to calculate a 95th percentile, or if the 95th percentile is
// very close to the maximum value, returns the maximum value instead. In either
// case, the true value is rounded up to the nearest 100 MB. Returns 0 if there
// are no prior values.
func (db *db) recommendedReqGroupMemory(reqGroup string) (mbs int, err error) {
	mbs, err = db.recommendedReqGroupStat(bucketJobMBs, reqGroup, RecMBRound)
	return
}

// recommendReqGroupTime returns the 95th percentile wall time taken of all jobs
// that previously ran with the given reqGroup. If there are too few prior
// values to calculate a 95th percentile, or if the 95th percentile is very
// close to the maximum value, returns the maximum value instead. In either
// case, the true value is rounded up to the nearest 30mins (but returned in
// seconds). Returns 0 if there are no prior values.
func (db *db) recommendedReqGroupTime(reqGroup string) (seconds int, err error) {
	seconds, err = db.recommendedReqGroupStat(bucketJobSecs, reqGroup, RecSecRound)
	return
}

// recommendedReqGroupStat is the implementation for the other recommend*()
// methods.
func (db *db) recommendedReqGroupStat(statBucket []byte, reqGroup string, roundAmount int) (recommendation int, err error) {
	prefix := []byte(reqGroup)
	max := 0
	err = db.bolt.View(func(tx *bolt.Tx) error {
		c := tx.Bucket(statBucket).Cursor()

		// we seek over the bucket, and to avoid having to do it twice (first to
		// get the overall count, then to get the 95th percentile), we keep the
		// previous 5%-sized window of values, updating recommendation as the
		// window fills
		count := 0
		window := jobStatWindowPercent
		var prev []int
		for k, v := c.Seek(prefix); bytes.HasPrefix(k, prefix); k, v = c.Next() {
			max, _ = strconv.Atoi(string(v))

			count++
			if count > 100 {
				window = (float32(count) / 100) * jobStatWindowPercent
			}

			prev = append(prev, max)
			if float32(len(prev)) > window {
				recommendation, prev = prev[0], prev[1:]
			}
		}

		return nil
	})
	if err != nil {
		return
	}

	if recommendation == 0 {
		if max == 0 {
			return
		}
		recommendation = max
	}

	if max-recommendation < roundAmount {
		recommendation = max
	}

	if recommendation%roundAmount > 0 {
		recommendation = int(math.Ceil(float64(recommendation)/float64(roundAmount))) * roundAmount
	}

	return
}

// store does a basic set of a key/val in a given bucket
func (db *db) store(bucket []byte, key string, val []byte) (err error) {
	err = db.bolt.Batch(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucket)
		err := b.Put([]byte(key), val)
		return err
	})
	return
}

// retrieve does a basic get of a key from a given bucket. An error isn't
// possible here.
func (db *db) retrieve(bucket []byte, key string) (val []byte) {
	db.bolt.View(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucket)
		v := b.Get([]byte(key))
		if v != nil {
			val = make([]byte, len(v))
			copy(val, v)
		}
		return nil
	})
	return
}

// remove does a basic delete of a key from a given bucket. We don't care about
// errors here.
func (db *db) remove(bucket []byte, key string) {
	go db.bolt.Batch(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucket)
		b.Delete([]byte(key))
		return nil
	})
}

// storeBatched stores items in the db in batches for efficiency. bucket is the
// name of the bucket to store in.
func (db *db) storeBatched(bucket []byte, data sobsd, storer sobsdStorer) (err error) {
	// we want to add in batches of size data/10, minimum 1000, rounded to
	// the nearest 1000
	num := len(data)
	batchSize := num / 10
	rem := batchSize % 1000
	if rem > 500 {
		batchSize = batchSize - rem + 1000
	} else {
		batchSize = batchSize - rem
	}
	if batchSize < 1000 {
		batchSize = 1000
	}

	// based on https://github.com/boltdb/bolt/issues/337#issue-64861745
	if num < batchSize {
		err = storer(bucket, data)
		return
	}

	batches := num / batchSize
	offset := num - (num % batchSize)

	for i := 0; i < batches; i++ {
		err = storer(bucket, data[i*batchSize:(i+1)*batchSize])
		if err != nil {
			return
		}
	}

	if offset != 0 {
		err = storer(bucket, data[offset:])
		if err != nil {
			return
		}
	}
	return
}

// storeLookups is a sobsdStorer for storing Job.[somevalue]->Job.Key() lookups
// in the db.
func (db *db) storeLookups(bucket []byte, lookups sobsd) (err error) {
	err = db.bolt.Batch(func(tx *bolt.Tx) error {
		lookup := tx.Bucket(bucket)
		for _, doublet := range lookups {
			err = lookup.Put(doublet[0], nil)
			if err != nil {
				return err
			}
		}
		return nil
	})
	return
}

// storeEncodedJobs is a sobsdStorer for storing Jobs in the db.
func (db *db) storeEncodedJobs(bucket []byte, encodes sobsd) (err error) {
	err = db.bolt.Batch(func(tx *bolt.Tx) error {
		bjobs := tx.Bucket(bucket)
		for _, doublet := range encodes {
			err := bjobs.Put(doublet[0], doublet[1])
			if err != nil {
				return err
			}
		}
		return nil
	})
	return
}

// close shuts down the db, should be used prior to exiting. Ensures any
// ongoing backgroundBackup() completes first (but does not wait for backup() to
// complete).
func (db *db) close() {
	db.Lock()
	defer db.Unlock()
	if !db.closed {
		db.closed = true

		// before actually closing, wait for any ongoing backup to complete
		if db.backingUp {
			db.backupFinal = true
			db.Unlock()
			<-db.backupNotification
			db.Lock()
		}

		db.bolt.Close()
	}
}

// backgroundBackup backs up the database to a file (the location given during
// initDB()) in a goroutine, doing one backup at a time and queueing a further
// backup if any other backup requests come in while a backup is running. Any
// errors are silently ignored.
func (db *db) backgroundBackup() {
	db.Lock()
	defer db.Unlock()
	if db.closed {
		return
	}
	if db.backingUp {
		db.backupQueued = true
		return
	}

	db.backingUp = true
	slowBackups := db.slowBackups
	go func() {
		// first move any existing backup file to one side
		backupBackupPath := db.backupPath + ".bk"
		os.Rename(db.backupPath, backupBackupPath)

		if slowBackups {
			// just for testing purposes
			<-time.After(200 * time.Millisecond)
		}

		// now create the new backup file
		err := db.bolt.View(func(tx *bolt.Tx) error {
			return tx.CopyFile(db.backupPath, dbFilePermission)
		})
		// *** currently not logging the error message anywhere...
		if err != nil {
			// if it failed, move any old backup back in place
			os.Rename(backupBackupPath, db.backupPath)
		} else {
			// backup succeeded, delete any old backup
			os.Remove(backupBackupPath)
		}

		db.Lock()
		db.backingUp = false

		if db.backupFinal {
			// close() has been called, don't do any more backups and tell
			// close() we finished our backup
			db.backupFinal = false
			db.Unlock()
			db.backupNotification <- true
			return
		}

		if db.backupQueued {
			db.backupQueued = false
			db.Unlock()
			db.backgroundBackup()
		} else {
			db.Unlock()
		}
	}()
}

// backup backs up the database to the given writer. Can be called at the same
// time as an active backgroundBackup() or even another backup(). You will get
// a consistent view of the database at the time you call this. NB: this can be
// interrupted by calling db.close().
func (db *db) backup(w io.Writer) error {
	db.RLock()
	if db.closed {
		db.RUnlock()
		return fmt.Errorf("database closed")
	}
	db.RUnlock()

	return db.bolt.View(func(tx *bolt.Tx) error {
		_, txErr := tx.WriteTo(w)
		return txErr
	})
}
