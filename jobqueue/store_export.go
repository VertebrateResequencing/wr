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
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
)

// errNotStoreDir is ExportStore's and ReadStore's error for a path that is not
// a store directory.
var errNotStoreDir = errors.New("not a store directory")

// readStoreChunk is how many complete jobs ReadStore decodes at a time, so
// that it never holds a big store's whole history in memory.
//
//nolint:gochecknoglobals // a test makes it smaller, to read in several chunks
var readStoreChunk = 1000

// ExportStore writes a self-contained file of the stopped store at dbDir to w
// (spec E7): the tar `wr manager backup -p` writes (see backup), which a
// manager started with it as its db file restores (spec E2). It opens the
// store as openStoppedStore does.
func ExportStore(dbDir string, w io.Writer) error {
	s, err := openStoppedStore(dbDir)
	if err != nil {
		return err
	}

	return errors.Join(s.backup(w), s.close(s.ctx))
}

// ReadStore calls fn for every live then every complete job in a stopped
// manager's store, taking the store lock (dbDir + ".lock"). It opens the store
// as openStoppedStore does. Each kind of job comes in key order, a live job
// with its latest durable run state, and a key both live and complete (a job
// being re-run) comes as both. It stops at the first error fn returns, and
// returns it.
func ReadStore(dbDir string, fn func(live bool, job *Job) error) error {
	s, err := openStoppedStore(dbDir)
	if err != nil {
		return err
	}

	return errors.Join(s.readJobs(fn), s.close(s.ctx))
}

// openStoppedStore opens the stopped log store at dbDir for ExportStore and
// ReadStore. It takes the store lock (dbDir + ".lock") first, so if a manager
// holds it, it fails with an error wrapping ErrDBLocked after waiting
// managerDBOpenTimeout for it, and recovers the store as a manager start
// would, making no backups. A dbDir that is missing is os.Stat's error, with
// nothing created; one that is not a directory is errNotStoreDir, as is one
// with no VERSION, rather than becoming a new, empty store.
func openStoppedStore(dbDir string) (*logStore, error) {
	info, err := os.Stat(dbDir)
	if err != nil {
		return nil, err
	}

	if !info.IsDir() {
		return nil, fmt.Errorf("%w: %s", errNotStoreDir, dbDir)
	}

	if _, err = os.Stat(filepath.Join(dbDir, storeVersionFile)); errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("%w: %s has no %s", errNotStoreDir, dbDir, storeVersionFile)
	}

	ctx := context.Background()

	lock, err := lockStore(ctx, dbDir)
	if err != nil {
		return nil, err
	}

	s, err := openLogStore(ctx, dbDir)
	if err != nil {
		return nil, errors.Join(err, lock.release())
	}

	s.lock = lock

	return s, nil
}

// callEach calls fn with live and each of jobs in turn, stopping at the first
// error it returns.
func callEach(fn func(live bool, job *Job) error, live bool, jobs []*Job) error {
	for _, job := range jobs {
		if err := fn(live, job); err != nil {
			return err
		}
	}

	return nil
}

// readJobs calls fn for each of s's live jobs, then each of its complete jobs,
// as ReadStore says.
func (s *logStore) readJobs(fn func(live bool, job *Job) error) error {
	live, err := s.liveJobs()
	if err != nil {
		return err
	}

	if err = callEach(fn, true, live); err != nil {
		return err
	}

	for chunk := range slices.Chunk(s.historyKeys(), readStoreChunk) {
		complete, errc := s.completeJobsOf(chunk)
		if errc != nil {
			return errc
		}

		if err = callEach(fn, false, complete); err != nil {
			return err
		}
	}

	return nil
}

// historyKeys returns every key in s's history key index, in key order.
func (s *logStore) historyKeys() []logKey {
	s.mu.RLock()
	defer s.mu.RUnlock()

	return slices.SortedFunc(maps.Keys(s.state.history), func(a, b logKey) int { return bytes.Compare(a[:], b[:]) })
}

// completeJobsOf returns the history entry's job of each of keys, in their
// order.
func (s *logStore) completeJobsOf(keys []logKey) ([]*Job, error) {
	jobs := make([]*Job, 0, len(keys))

	err := s.viewHistoryKeys(keys, func(r *historyRead) error {
		for _, k := range keys {
			job, err := r.job(k)
			if err != nil {
				return err
			}

			if job == nil {
				return fmt.Errorf("%w: complete job %s has no history entry", errStoreCorrupt, k)
			}

			jobs = append(jobs, job)
		}

		return nil
	})

	return jobs, err
}
