// Copyright © 2016 Genome Research Limited
// Author: Sendu Bala <sb10@sanger.ac.uk>.
//
//  This file is part of VRPipe.
//
//  VRPipe is free software: you can redistribute it and/or modify
//  it under the terms of the GNU Lesser General Public License as published by
//  the Free Software Foundation, either version 3 of the License, or
//  (at your option) any later version.
//
//  VRPipe is distributed in the hope that it will be useful,
//  but WITHOUT ANY WARRANTY; without even the implied warranty of
//  MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
//  GNU Lesser General Public License for more details.
//
//  You should have received a copy of the GNU Lesser General Public License
//  along with VRPipe. If not, see <http://www.gnu.org/licenses/>.

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
	"os"
	"sort"
	"strings"
)

var (
	bucketJobsLive     []byte = []byte("jobslive")
	bucketJobsComplete []byte = []byte("jobscomplete")
	bucketRTK          []byte = []byte("repgroupTokey")
	bucketEnvs         []byte = []byte("envs")
	bucketStdO         []byte = []byte("stdo")
	bucketStdE         []byte = []byte("stde")
)

// bje implements sort interface so we can sort a slice of []byte triples,
// needed for efficient Puts in to the database
type bje [][3][]byte

func (s bje) Len() int {
	return len(s)
}
func (s bje) Swap(i, j int) {
	s[i], s[j] = s[j], s[i]
}
func (s bje) Less(i, j int) bool {
	cmp := bytes.Compare(s[i][0], s[j][0])
	if cmp == -1 {
		return true
	}
	return false
}

type db struct {
	bolt     *bolt.DB
	envcache *lru.ARCCache
	ch       codec.Handle
}

// initDB opens/creates our database and sets things up for use. If dbFile
// doesn't exist or seems corrupted, we copy it from backup if that exists,
// otherwise we start fresh. In development we delete any existing db and force
// a fresh start.
func initDB(dbFile string, dbBkFile string) (dbstruct *db, msg string, err error) {
	if strings.HasSuffix(dbFile, "_development") {
		os.Remove(dbFile)
		os.Remove(dbBkFile)
	}

	var boltdb *bolt.DB
	if _, err = os.Stat(dbFile); os.IsNotExist(err) {
		if _, err = os.Stat(dbBkFile); os.IsNotExist(err) { //*** need to handle bk being on another machine, possibly an S3-style object store
			boltdb, err = bolt.Open(dbFile, 0600, nil)
			msg = "created new empty db file " + dbFile
		} else {
			// copy bk to main *** need to handle bk being in an object store
			err = copyFile(dbBkFile, dbFile)
			if err != nil {
				return
			}
			boltdb, err = bolt.Open(dbFile, 0600, nil)
			msg = "recreated missing db file " + dbFile + " from backup file " + dbBkFile
		}
	} else {
		boltdb, err = bolt.Open(dbFile, 0600, nil)
		if err != nil {
			// try the backup *** again, need to handle bk being elsewhere
			if _, errbk := os.Stat(dbBkFile); errbk == nil {
				boltdb, errbk = bolt.Open(dbBkFile, 0600, nil)
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
					boltdb, err = bolt.Open(dbFile, 0600, nil)
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

	dbstruct = &db{boltdb, envcache, new(codec.BincHandle)}
	return
}

// storeNewJobs stores jobs in the live bucket, where they will only be used for
// disaster recovery. The jobs are supplied pre-encoded by binc in
// key,repgroup,data triples.
func (db *db) storeNewJobs(encjobs [][3][]byte) (err error) {
	// turn the jobs in to bjes and sort by their keys
	var encodes bje
	for _, encjob := range encjobs {
		rp := append(encjob[1], []byte("_::_")...)
		rp = append(rp, encjob[0]...)
		encodes = append(encodes, [3][]byte{encjob[0], encjob[2], rp})
	}
	sort.Sort(encodes)
	err = db.storeBatchedEncodedJobs(bucketJobsLive, encodes)
	return
}

// retrieveCompleteJobsByKeys gets jobs with the given keys from the completed
// jobs bucket (ie. those that have gone through the queue and been Remove()d).
func (db *db) retrieveCompleteJobsByKeys(keys []string, getstd bool, getenv bool) (jobs []*Job, err error) {

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

// updates the stdout/err associated with a job. we don't want to store these in
// the job, since that would waste a lot of the queue's memory; we store in db
// instead, and only retrieve when a client needs to see these. To stop the db
// file becoming enormous, we only store these if the cmd failed and also delete
// these from db when the cmd completes successfully. By doing the deletion
// upfront, we also ensure we have the latest std, which may be nil even on cmd
// failure.
func (db *db) updateJobStd(jobkey string, exitcode int, stdo []byte, stde []byte) (err error) {
	err = db.bolt.Batch(func(tx *bolt.Tx) error {
		bo := tx.Bucket(bucketStdO)
		be := tx.Bucket(bucketStdE)
		key := []byte(jobkey)
		bo.Delete(key)
		be.Delete(key)

		var err error
		if exitcode != 0 {
			if len(stdo) > 0 {
				err = bo.Put(key, stdo)
			}
			if len(stde) > 0 {
				err = be.Put(key, stde)
			}
		}
		return err
	})
	return
}

// retrieveJobStd gets the values that were stored using updateJobStd() for the
// given job.
func (db *db) retrieveJobStd(jobkey string) (stdo []byte, stde []byte) {
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
// func (db *db) remove(bucket []byte, key string) {
// 	db.bolt.Batch(func(tx *bolt.Tx) error {
// 		b := tx.Bucket(bucket)
// 		b.Delete([]byte(key))
// 		return nil
// 	})
// }

func (db *db) storeBatchedEncodedJobs(bucket []byte, encodes bje) (err error) {
	// we want to add in batches of size encodes/10, minimum 1000, rounded to
	// the nearest 1000
	num := len(encodes)
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
		err = db.storeEncodedJobs(bucket, encodes)
		return
	}

	batches := num / batchSize
	offset := num - (num % batchSize)

	for i := 0; i < batches; i++ {
		err = db.storeEncodedJobs(bucket, encodes[i*batchSize:(i+1)*batchSize])
		if err != nil {
			return
		}
	}

	if offset != 0 {
		err = db.storeEncodedJobs(bucket, encodes[offset:])
		if err != nil {
			return
		}
	}
	return
}

func (db *db) storeEncodedJobs(bucket []byte, encodes bje) (err error) {
	err = db.bolt.Batch(func(tx *bolt.Tx) error {
		bjobs := tx.Bucket(bucket)
		brtk := tx.Bucket(bucketRTK)
		for _, triple := range encodes {
			err := bjobs.Put(triple[0], triple[1])
			if err != nil {
				return err
			}

			err = brtk.Put(triple[2], nil)
			if err != nil {
				return err
			}
		}
		return nil
	})
	return
}

// close shuts down the db, should be used prior to exiting
func (db *db) close() {
	db.bolt.Close()
}
