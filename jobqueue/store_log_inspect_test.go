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
	"archive/tar"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	. "github.com/smartystreets/goconvey/convey"
)

var (
	// errLogStoreUnknownKind is count's error for a kind it does not know.
	errLogStoreUnknownKind = errors.New("unknown record kind")

	// errLogStoreNoForcedBackup is forceBackup's error: the log store makes no
	// incremental backups until item 1f.5.
	errLogStoreNoForcedBackup = errors.New("the log store makes no incremental backups yet")

	// errNotSelfContainedBackup is openLogBackupFileInspector's error for a
	// path that is not a self-contained backup's tar.
	errNotSelfContainedBackup = errors.New("not a self-contained backup")
)

// logStoreFlushPoll is how often flush checks for unfinished writes.
const logStoreFlushPoll = time.Millisecond

var _ storeInspector = (*logStore)(nil)

//nolint:gochecknoinits // registers this store kind's file opener
func init() {
	storeFileOpeners = append(storeFileOpeners, openLogBackupFileInspector)
}

// openLogBackupFileInspector is the storeFileOpeners entry for a log store's
// self-contained backup (spec E5): it extracts the tar at path into a new
// temporary directory and opens the store there, so that path itself is never
// changed. Closing it closes the store and removes the directory.
func openLogBackupFileInspector(path string) (storeInspector, func() error, error) {
	dir, err := os.MkdirTemp("", "wr-store-backup-")
	if err != nil {
		return nil, nil, err
	}

	if err = extractStoreBackup(path, dir); err != nil {
		return nil, nil, errors.Join(err, os.RemoveAll(dir))
	}

	ctx := context.Background()

	s, err := openLogStore(ctx, dir)
	if err != nil {
		return nil, nil, errors.Join(err, os.RemoveAll(dir))
	}

	return s, func() error { return errors.Join(s.close(ctx), os.RemoveAll(dir)) }, nil
}

// extractStoreBackup writes each member of the self-contained backup at path
// into dir, refusing a file that is not a tar of regular files named without
// a directory.
func extractStoreBackup(path, dir string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}

	root, err := os.OpenRoot(dir)
	if err != nil {
		return errors.Join(err, f.Close())
	}

	return errors.Join(extractTar(tar.NewReader(f), root, path), root.Close(), f.Close())
}

// extractTar writes each member of tr, the tar at path, into root.
func extractTar(tr *tar.Reader, root *os.Root, path string) error {
	hdr, err := tr.Next()
	if err != nil {
		return fmt.Errorf("%w: %s: %w", errNotSelfContainedBackup, path, err)
	}

	for ; err == nil; hdr, err = tr.Next() {
		if hdr.Typeflag != tar.TypeReg || filepath.Base(hdr.Name) != hdr.Name {
			return fmt.Errorf("%w: %s holds %q", errNotSelfContainedBackup, path, hdr.Name)
		}

		if err = extractBackupMember(tr, root, hdr.Name); err != nil {
			return err
		}
	}

	if errors.Is(err, io.EOF) {
		return nil
	}

	return err
}

// extractBackupMember writes the rest of tr's current member to name in root.
func extractBackupMember(tr *tar.Reader, root *os.Root, name string) error {
	out, err := root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, dbFilePermission)
	if err != nil {
		return err
	}

	_, err = io.Copy(out, tr)

	return errors.Join(err, out.Close())
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

	job, live, err := s.liveJobOf(k)

	return job, live && err == nil, err
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

// forceBackup returns errLogStoreNoForcedBackup: the log store makes no
// incremental backups yet.
func (s *logStore) forceBackup() error {
	return errLogStoreNoForcedBackup
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
