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

// This file holds what openStore does before it reads a store (spec C7): the
// lock beside dbFile that keeps two managers off one store, and the VERSION
// check that refuses a store written by a newer wr.

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/VertebrateResequencing/wr/internal"
)

const (
	// storeLockSuffix makes dbFile's lock file name. The lock is beside the
	// store directory, not in it, because restore and the dev wipe remove or
	// rename dbFile while the lock is held (spec "Location, lock, ...").
	storeLockSuffix = ".lock"

	// storeLockRetry is how often a held lock is tried again, as bbolt
	// retries its own flock.
	storeLockRetry = 50 * time.Millisecond

	// storeVersionFile names the file in a store directory saying which
	// format it is in.
	storeVersionFile = "VERSION"

	// storeVersionPrefix starts storeVersionFile's one line, which ends in
	// the format's number and a newline.
	storeVersionPrefix = "wr-store "

	// storeVersion is the store format this wr reads and writes.
	storeVersion = 1

	// storeVersionQuoted is how many bytes of a VERSION naming no store format
	// its errStoreCorrupt quotes, so a large garbage file makes a short error.
	storeVersionQuoted = 64

	// storeTmpExt ends the name of a file still being written.
	storeTmpExt = ".tmp"

	// storeConvertingSuffix, storeRestoringSuffix and storeCorruptSuffix make
	// the names of the directories a conversion (spec E1) and a restore (E2)
	// build or set aside beside dbFile.
	storeConvertingSuffix = ".converting"
	storeRestoringSuffix  = ".restoring"
	storeCorruptSuffix    = ".corrupt"

	// storeBoltPreInfix follows dbFile or dbBkFile in the name of a bbolt
	// file a conversion keeps for rollback, and precedes the version that
	// wrote it.
	storeBoltPreInfix = ".bolt-pre-"
)

// storeLock is a held flock on dbFile + storeLockSuffix.
type storeLock struct {
	f *os.File
}

// lockStore takes the exclusive lock on dbFile's store, which openStore does
// before anything else, so a manager that cannot take it has changed nothing.
// A lock another holds is tried again until managerDBOpenTimeout, then
// ErrDBLocked. The path is dbFile as given, not resolved.
//
// The lock is flock(2), as bbolt's is: Linux NFS clients carry it to the
// server as a whole-file POSIX lock, so it excludes managers on other hosts.
func lockStore(ctx context.Context, dbFile string) (*storeLock, error) {
	path := dbFile + storeLockSuffix

	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, dbFilePermission)
	if err != nil {
		return nil, unreadableOr(err, dbFile)
	}

	if err = flockWithin(ctx, f, managerDBOpenTimeout); err != nil {
		return nil, errors.Join(lockFailure(err, path, dbFile), f.Close())
	}

	return &storeLock{f: f}, nil
}

// release unlocks and closes the lock file, leaving it in place. A nil lock
// releases nothing.
func (l *storeLock) release() error {
	if l == nil {
		return nil
	}

	return l.f.Close()
}

// openConvertedLogStore is openLogStoreHolding, first converting dbFile if it
// is a bbolt file or resuming a conversion a crash interrupted (see
// convertOnOpen), after which the store reports it upgraded on open.
func openConvertedLogStore(ctx context.Context, dbFile string, lock *storeLock) (*logStore, error) {
	converted, err := convertOnOpen(ctx, dbFile)
	if err != nil {
		return nil, errors.Join(err, lock.release())
	}

	s, err := openLogStoreHolding(ctx, dbFile, lock)
	if err != nil {
		return nil, err
	}

	s.upgraded = converted

	return s, nil
}

// openLogStoreHolding opens the log store at dbFile, giving it lock, the store
// lock its caller took, to hold until it closes. If it cannot open the store
// it releases lock.
func openLogStoreHolding(ctx context.Context, dbFile string, lock *storeLock) (*logStore, error) {
	s, err := openLogStore(ctx, dbFile)
	if err != nil {
		return nil, errors.Join(unreadableOr(err, dbFile), lock.release())
	}

	s.lock = lock

	return s, nil
}

// unreadableOr returns err as openErrorThatIsNotCorruption words it, if it is
// one of its errors, else err unchanged.
func unreadableOr(err error, dbFile string) error {
	if unreadable := openErrorThatIsNotCorruption(err, dbFile); unreadable != nil {
		return unreadable
	}

	return err
}

// wipeDevStore, if cfg asks for a development wipe, removes everything the DB
// at cfg.dbFile owns, so that a development manager starts from an empty DB
// (spec E3): dbFile, file or directory, the directories a conversion or
// restore left beside it, the bbolt files a conversion kept for rollback, and
// the backup at cfg.dbBkFile (see wipeDevBackup). The ".upgrade" sidecar is
// left, as is the ".lock" file, which the caller holds.
func wipeDevStore(cfg storeConfig) error {
	if !cfg.wipeDevDB || cfg.deployment != internal.Development {
		return nil
	}

	err := removeOwned(cfg.dbFile, storeConvertingSuffix, storeRestoringSuffix, storeCorruptSuffix)
	if err != nil {
		return fmt.Errorf("wipe development db %s: %w", cfg.dbFile, err)
	}

	if err = wipeDevBackup(cfg.dbBkFile, cfg.forceBackups); err != nil {
		return fmt.Errorf("wipe development db backup %s: %w", cfg.dbBkFile, err)
	}

	return nil
}

// removeOwned removes path, file or directory, path + each of suffixes, and
// every sibling of path whose name is path's followed by storeBoltPreInfix. An
// empty path owns nothing, so nothing is removed.
func removeOwned(path string, suffixes ...string) error {
	if path == "" {
		return nil
	}

	paths, err := boltPreSiblings(path)
	if err != nil {
		return err
	}

	paths = append(paths, path)

	for _, suffix := range suffixes {
		paths = append(paths, path+suffix)
	}

	for _, p := range paths {
		if err = os.RemoveAll(p); err != nil {
			return err
		}
	}

	return nil
}

// boltPreSiblings returns the paths beside path whose names are path's
// followed by storeBoltPreInfix.
func boltPreSiblings(path string) ([]string, error) {
	dir := filepath.Dir(path)

	entries, err := os.ReadDir(dir)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}

	pre := filepath.Base(path) + storeBoltPreInfix

	var paths []string

	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), pre) {
			paths = append(paths, filepath.Join(dir, entry.Name()))
		}
	}

	return paths, nil
}

// wipeDevBackup removes a development manager's backup at dbBkFile and the
// bbolt files a conversion kept beside it. An S3 backup is removed only when
// forceBackups, as a development manager otherwise makes none.
func wipeDevBackup(dbBkFile string, forceBackups bool) error {
	if internal.InS3(dbBkFile) && !forceBackups {
		return nil
	}

	backup, err := backupTargetAt(dbBkFile, internal.Development)
	if err != nil {
		return err
	}

	return backup.removeAll()
}

// flockWithin takes an exclusive flock on f, trying again every
// storeLockRetry while another holds it, until timeout has passed or ctx is
// done.
func flockWithin(ctx context.Context, f *os.File, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)

	for {
		err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil || !errors.Is(err, syscall.EWOULDBLOCK) || !time.Now().Before(deadline) {
			return err
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(storeLockRetry):
		}
	}
}

// lockFailure is lockStore's error for a flock that failed with err.
func lockFailure(err error, path, dbFile string) error {
	if errors.Is(err, syscall.EWOULDBLOCK) {
		return fmt.Errorf("%w: %s", ErrDBLocked, dbFile)
	}

	return unreadableOr(fmt.Errorf("lock %s: %w", path, err), dbFile)
}

// prepareStoreDir makes sure dir is a store this wr can read before anything
// in it is read or changed: its VERSION is checked, or, for an absent or
// empty dir, written, making a new store.
func prepareStoreDir(dir string) error {
	version, err := os.ReadFile(filepath.Join(dir, storeVersionFile))
	if err == nil {
		return checkStoreVersion(dir, version)
	}

	if !errors.Is(err, fs.ErrNotExist) {
		return err
	}

	return createStoreDir(dir)
}

// checkStoreVersion returns errStoreVersionTooNew for a VERSION naming a
// format after storeVersion, and errStoreCorrupt for one that names none.
func checkStoreVersion(dir string, version []byte) error {
	line, ok := bytes.CutSuffix(version, []byte("\n"))
	number, hasPrefix := strings.CutPrefix(string(line), storeVersionPrefix)

	v, err := strconv.ParseUint(number, 10, 32)

	switch {
	case !ok || !hasPrefix || err != nil || v == 0:
		return fmt.Errorf("%w: %s starts %q, not a store version", errStoreCorrupt,
			filepath.Join(dir, storeVersionFile), version[:min(len(version), storeVersionQuoted)])
	case v > storeVersion:
		return fmt.Errorf("store %s is format %d, but this wr reads up to %d: %w", dir, v,
			storeVersion, errStoreVersionTooNew)
	}

	return nil
}

// createStoreDir makes dir, creating it if it is absent, a new store by
// durably writing its VERSION and dir's entry in its parent. A dir already
// holding anything but files being written has lost its VERSION, which is
// errStoreCorrupt.
func createStoreDir(dir string) error {
	if err := os.MkdirAll(dir, logStoreDirPerm); err != nil {
		return err
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}

	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), storeTmpExt) {
			return fmt.Errorf("%w: %s has no %s", errStoreCorrupt, dir, storeVersionFile)
		}
	}

	if err = writeStoreVersion(dir); err != nil {
		return err
	}

	return syncDir(filepath.Dir(dir))
}

// writeStoreVersion durably writes dir's VERSION.
func writeStoreVersion(dir string) error {
	return writeFileDurably(filepath.Join(dir, storeVersionFile), fmt.Appendf(nil, "%s%d\n", storeVersionPrefix,
		storeVersion))
}

// writeFileDurably writes content to path through a .tmp file, fsynced then
// renamed into place, and fsyncs path's directory, so that path is never seen
// partly written.
func writeFileDurably(path string, content []byte) error {
	tmp := path + storeTmpExt

	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, dbFilePermission)
	if err != nil {
		return err
	}

	_, err = f.Write(content)
	if err == nil {
		err = f.Sync()
	}

	if err = errors.Join(err, f.Close()); err != nil {
		return err
	}

	if err = os.Rename(tmp, path); err != nil {
		return err
	}

	return syncDir(filepath.Dir(path))
}

// syncDir fsyncs dir, making renames and creations in it durable.
func syncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}

	return errors.Join(d.Sync(), d.Close())
}
