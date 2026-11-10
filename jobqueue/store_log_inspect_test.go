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

// The log store's storeInspector (spec A2).

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/VertebrateResequencing/wr/internal"
	. "github.com/smartystreets/goconvey/convey"
)

var (
	// errLogStoreUnknownKind is count's error for a kind it does not know.
	errLogStoreUnknownKind = errors.New("unknown record kind")

	// errLogStoreNoBackups is forceBackup's error for a store with no backup
	// path, or an S3 one it never backs up to.
	errLogStoreNoBackups = errors.New("store makes no backups")
)

// logStoreFlushPoll is how often flush checks for unfinished writes.
const logStoreFlushPoll = time.Millisecond

var _ storeInspector = (*logStore)(nil)

//nolint:gochecknoinits // registers this store kind's file opener
func init() {
	storeFileOpeners = append(storeFileOpeners, openLogBackupFileInspector)
}

// openLogBackupFileInspector is the storeFileOpeners entry for a log store's
// backup: a self-contained one (spec E5), or a backup directory an incremental
// backup writes (E4). It restores the backup at path into a store in a new
// temporary directory, as a manager restores a missing db file from its
// backup (spec E2), so that path itself is never changed. Closing it closes
// the store and removes the directory.
func openLogBackupFileInspector(path string) (storeInspector, func() error, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, nil, err
	}

	isTar := false
	if !info.IsDir() {
		if isTar, err = hasTarMagic(path); err != nil {
			return nil, nil, err
		}
	}

	if !info.IsDir() && !isTar {
		return nil, nil, fmt.Errorf("%w: %s", errNotSelfContainedBackup, path)
	}

	dir, err := os.MkdirTemp("", "wr-store-backup-")
	if err != nil {
		return nil, nil, err
	}

	ctx := context.Background()

	s, err := openLogStoreLocked(ctx, storeConfig{
		dbFile:     filepath.Join(dir, "queue.db"),
		dbBkFile:   path,
		deployment: internal.Development,
		kind:       storeKindLog,
	})
	if err != nil {
		return nil, nil, errors.Join(err, os.RemoveAll(dir))
	}

	return s, func() error { return errors.Join(s.close(ctx), os.RemoveAll(dir)) }, nil
}

// openTestLogStore opens a log store in a new directory, closed when the
// Convey block ends, and returns it with its directory.
func openTestLogStore(t *testing.T, ctx context.Context) (*logStore, string) {
	t.Helper()

	dir := filepath.Join(t.TempDir(), "store")
	s := openLogStoreIn(ctx, dir)

	Reset(func() { So(s.close(ctx), ShouldBeNil) })

	return s, dir
}

// reopenLogStore closes s and opens its directory again, as a restart would,
// returning the new store, closed when the Convey block ends.
func reopenLogStore(ctx context.Context, s *logStore) *logStore {
	So(s.close(ctx), ShouldBeNil)

	reopened := openLogStoreIn(ctx, s.dir)

	Reset(func() { So(reopened.close(ctx), ShouldBeNil) })

	return reopened
}

// openLogStoreIn opens the log store in dir, asserting it opened. The caller
// closes it.
func openLogStoreIn(ctx context.Context, dir string) *logStore {
	s, err := openLogStore(ctx, dir)
	So(err, ShouldBeNil)

	return s
}

// liveJob returns the keyed job as its latest durable spec and state, and
// whether it is live.
func (s *logStore) liveJob(key string) (*Job, bool, error) {
	k, err := parseLogKey(key)
	if err != nil {
		return nil, false, err
	}

	jobs, err := s.liveJobsOf([]logKey{k})
	if err != nil {
		return nil, false, err
	}

	return jobs[0], jobs[0] != nil, nil
}

// completeJob returns the keyed job as its history entry holds it, and whether
// it has one.
func (s *logStore) completeJob(key string) (*Job, bool, error) {
	k, err := parseLogKey(key)
	if err != nil {
		return nil, false, err
	}

	return s.historyJob(k)
}

// count returns how many records of kind the store holds.
func (s *logStore) count(kind recordKind) (int, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	switch kind {
	case kindLive:
		return len(s.state.live), nil
	case kindComplete:
		return len(s.state.history), nil
	case kindStdOut, kindStdErr:
		stream := stdOut
		if kind == kindStdErr {
			stream = stdErr
		}

		n := 0

		for _, e := range s.state.live {
			if e.hasStd[stream] {
				n++
			}
		}

		return n, nil
	case kindEnv:
		return len(s.state.envs), nil
	case kindLimit:
		return len(s.state.limits), nil
	case kindStat:
		return s.sampleCount()
	default:
		return 0, fmt.Errorf("%w: %d", errLogStoreUnknownKind, kind)
	}
}

// forceBackup is storeInspector.forceBackup: it makes an incremental backup
// tick (spec E4) now, even if periodic backups are disabled, from when on the
// store keeps the wal files the backup may need, as a periodic store does.
func (s *logStore) forceBackup() error {
	if s.inc == nil {
		return errLogStoreNoBackups
	}

	if !s.startBackup() {
		return errDBClosed
	}

	defer s.backups.Done()

	return s.backupTick()
}

// flush waits until every write appended so far has finished, then folds
// them, so that what the folder writes, such as learned-requirement samples,
// is there too.
func (s *logStore) flush() error {
	for s.writing.Load() != 0 {
		time.Sleep(logStoreFlushPoll)
	}

	return s.foldNow()
}

// archivedDecodeCount returns how many complete jobs s has decoded in full from
// its history.
func (s *logStore) archivedDecodeCount() uint64 {
	return s.archivedDecodes.Load()
}

// reqStatCache returns s's learned requirement cache.
func (s *logStore) reqStatCache() *reqStatCache {
	return s.reqStats
}
