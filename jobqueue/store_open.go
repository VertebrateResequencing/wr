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

// This file holds what openStore does before it reads a store: the lock beside
// dbFile that keeps two managers off one store and the VERSION check that
// refuses a store written by a newer wr (spec C7), the development wipe (E3),
// and the open order and restore from backup (E2).

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
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

const (
	// storeTarMagicOffset and storeTarMagic say where a tar header, and so a
	// self-contained backup, holds its "ustar" magic.
	storeTarMagicOffset = 257
	storeTarMagic       = "ustar"
)

// restorePoint is a point in a restore (spec E2) that logRestoreHook is
// called at.
type restorePoint uint8

const (
	restoreAtCopied    restorePoint = iota // in step 1, after each file is copied
	restoreAtRecovered                     // after step 2
	restoreAtSetAside                      // in step 3, after dbFile is renamed to ".corrupt"
	restoreAtReplaced                      // in step 3, after ".restoring" is renamed to dbFile
)

var (
	// logRestoreHook, a test seam nil outside tests, is called at each
	// restorePoint of a restore; an error it returns ends the restore there as
	// a crash would, leaving what the restore made so far for the next open.
	logRestoreHook atomic.Pointer[func(at restorePoint) error] //nolint:gochecknoglobals // a test seam

	// errRestoreHalted wraps an error of logRestoreHook's.
	errRestoreHalted = errors.New("restore halted")

	// errNotABackup is a restore's error for a backup it cannot read as a
	// store's files, a self-contained backup or a bbolt file.
	errNotABackup = errors.New("not a store backup, self-contained backup or bbolt file")

	// errNotStoreOrFile is the error of a dbFile that is neither a directory
	// nor a regular file.
	errNotStoreOrFile = errors.New("the db file is neither a store directory nor a regular file")

	// errNotSelfContainedBackup is the error of a self-contained backup that
	// is not a tar of regular files named without a directory.
	errNotSelfContainedBackup = errors.New("not a self-contained backup")

	// errCorruptBesideDBFile is the error of a dbFile that is not a store
	// directory with a dbFile + ".corrupt" beside it, which only something
	// other than a restore can leave, so either may be the only copy of the
	// data.
	errCorruptBesideDBFile = errors.New("a db file that is not a store directory has a set-aside .corrupt beside it")
)

// dbFileForm is what is at dbFile once any conversion is done.
type dbFileForm uint8

const (
	dbFileAbsent        dbFileForm = iota
	dbFileStore                    // a directory
	dbFileSelfContained            // a regular file that is a tar
	dbFileUnrecognised             // any other regular file
)

// callRestoreHook returns the error of logRestoreHook at at, if it is set,
// wrapping errRestoreHalted.
func callRestoreHook(at restorePoint) error {
	hook := logRestoreHook.Load()
	if hook == nil {
		return nil
	}

	if err := (*hook)(at); err != nil {
		return fmt.Errorf("%w: %w", errRestoreHalted, err)
	}

	return nil
}

// dbFileFormOf says what is at dbFile, following a symlink. Something that is
// neither a directory nor a regular file is an error, not unrecognised, so
// that restore never replaces it.
func dbFileFormOf(dbFile string) (dbFileForm, error) {
	info, err := os.Stat(dbFile)

	switch {
	case errors.Is(err, fs.ErrNotExist):
		return dbFileAbsent, nil
	case err != nil:
		return dbFileAbsent, unreadableOr(err, dbFile)
	case info.IsDir():
		return dbFileStore, nil
	case !info.Mode().IsRegular():
		return dbFileAbsent, fmt.Errorf("%w: %s; it has been left untouched", errNotStoreOrFile, dbFile)
	}

	isTar, err := hasTarMagic(dbFile)
	if err != nil || isTar {
		return dbFileSelfContained, err
	}

	return dbFileUnrecognised, nil
}

// openDBFileForm is openLogStoreAt once any conversion is done, whether one
// was, converted.
func openDBFileForm(ctx context.Context, cfg storeConfig, converted bool) (*logStore, string, error) {
	form, err := dbFileFormOf(cfg.dbFile)

	switch {
	case err != nil:
		return nil, "", err
	case form == dbFileAbsent:
		return openMissingLogStore(ctx, cfg)
	case form == dbFileSelfContained:
		return openSelfContainedLogStore(ctx, cfg)
	case form == dbFileUnrecognised:
		return restoreCorruptLogStore(ctx, cfg, fmt.Errorf("%w: %s is not a store directory, bbolt file or "+
			"self-contained backup", errStoreCorrupt, cfg.dbFile))
	}

	s, err := openLogStoreFor(ctx, cfg)
	if errors.Is(err, errStoreCorrupt) {
		return restoreCorruptLogStore(ctx, cfg, err)
	}

	if err != nil {
		return nil, "", unreadableOr(err, cfg.dbFile)
	}

	s.upgraded = converted

	return s, "", nil
}

// openMissingLogStore opens the log store at an absent dbFile: restored from
// cfg's backup if it has one, else a new empty one.
func openMissingLogStore(ctx context.Context, cfg storeConfig) (*logStore, string, error) {
	backup, err := backupToRestore(cfg)
	if err != nil {
		return nil, "", err
	}

	if backup == nil {
		s, erro := openLogStoreFor(ctx, cfg)

		return s, "created new empty db file " + cfg.dbFile, erro
	}

	msg := "recreated missing db file " + cfg.dbFile + " from " + backup.describe()

	if err = restoreLogStore(ctx, cfg.dbFile, backup); err != nil {
		return nil, msg, err
	}

	s, err := openLogStoreFor(ctx, cfg)

	return s, msg, err
}

// backupToRestore returns the backup cfg names, or nil if there is none. An
// S3 backup is restored from only by a manager that makes backups there: one
// in production deployment, or with forceBackups; a local one by any, as
// initDB restores.
func backupToRestore(cfg storeConfig) (backupTarget, error) {
	if !restoresFrom(cfg) {
		return nil, nil //nolint:nilnil // no backup is not an error
	}

	backup, err := backupTargetAt(cfg.dbBkFile, cfg.deployment)
	if err != nil {
		return nil, err
	}

	form, err := backup.form()
	if err != nil || form == backupNone {
		return nil, err
	}

	return backup, nil
}

// restoresFrom reports whether a manager of cfg restores from its dbBkFile.
func restoresFrom(cfg storeConfig) bool {
	if cfg.dbBkFile == "" {
		return false
	}

	return !internal.InS3(cfg.dbBkFile) || cfg.deployment == internal.Production || cfg.forceBackups
}

// openLogStoreFor opens the log store at cfg.dbFile with the incremental
// backups cfg asks for (see newIncBackup).
func openLogStoreFor(ctx context.Context, cfg storeConfig) (*logStore, error) {
	inc, err := newIncBackup(cfg)
	if err != nil {
		return nil, err
	}

	return openBackedLogStore(ctx, cfg.dbFile, inc)
}

// restoreLogStore replaces dbFile with a store restored from backup (spec
// E2), never touching dbFile until the restored store has recovered: it is
// built and recovered in dbFile + ".restoring", which on any error is
// removed, then dbFile, if there, is renamed to dbFile + ".corrupt", the
// restored store renamed to dbFile, and ".corrupt" removed. A crash at any
// point leaves dbFile as it was, set aside as ".corrupt" with no dbFile, which
// the next open renames back (see undoRestoreLeftovers), or the restored store;
// the next open removes the rest of what this one left, and restores again if
// it still needs to.
func restoreLogStore(ctx context.Context, dbFile string, backup backupTarget) error {
	restoring := dbFile + storeRestoringSuffix

	if err := prepareRestore(ctx, dbFile, restoring, backup); err != nil {
		if !errors.Is(err, errRestoreHalted) {
			err = errors.Join(err, os.RemoveAll(restoring))
		}

		return err
	}

	return replaceWithRestored(dbFile, restoring)
}

// prepareRestore is restore steps 1 and 2: it fills restoring with a store
// from backup, then recovers it, closes it and makes it durable.
func prepareRestore(ctx context.Context, dbFile, restoring string, backup backupTarget) error {
	if err := os.RemoveAll(restoring); err != nil {
		return err
	}

	if err := os.Mkdir(restoring, logStoreDirPerm); err != nil {
		return err
	}

	if err := fillRestoring(ctx, dbFile, restoring, backup); err != nil {
		return err
	}

	if _, err := os.Stat(filepath.Join(restoring, storeVersionFile)); err != nil {
		return fmt.Errorf("%w: backup %s holds no %s, as a first backup, or one started again, cut short "+
			"before it was whole does not; move it aside to let the store start without it: %w", errNotABackup,
			backup.location(), storeVersionFile, err)
	}

	s, err := openLogStore(ctx, restoring)
	if err != nil {
		return err
	}

	if err = errors.Join(s.close(ctx), syncTree(restoring)); err != nil {
		return err
	}

	return callRestoreHook(restoreAtRecovered)
}

// fillRestoring copies backup's files into restoring: a store backup's files,
// a self-contained backup's tar members, or a store built from an old bbolt
// backup, read-only.
func fillRestoring(ctx context.Context, dbFile, restoring string, backup backupTarget) error {
	form, err := backup.form()
	if err != nil {
		return err
	}

	if form == backupStore {
		return copyStoreBackup(restoring, backup)
	}

	path, done, err := backup.fetchFile(restoring)
	if err != nil {
		return err
	}

	return errors.Join(fillFromFile(ctx, dbFile, restoring, path), done())
}

// copyStoreBackup copies every file of the store backup into restoring, then
// rebuilds each wal file from the parts an incremental backup holds of it (see
// assembleWALParts). A backup holding a name that leads out of it, as an S3
// object's can, is errNotABackup, and nothing is copied.
func copyStoreBackup(restoring string, backup backupTarget) error {
	entries, err := backup.list()
	if err != nil {
		return err
	}

	leadsOut := func(e backupEntry) bool { return !filepath.IsLocal(filepath.FromSlash(e.name)) }
	if i := slices.IndexFunc(entries, leadsOut); i >= 0 {
		return fmt.Errorf("%w: backup %s holds %q, a name leading out of it", errNotABackup, backup.location(),
			entries[i].name)
	}

	for _, entry := range entries {
		if err = backup.download(entry.name, filepath.Join(restoring, filepath.FromSlash(entry.name))); err != nil {
			return err
		}

		if err = callRestoreHook(restoreAtCopied); err != nil {
			return err
		}
	}

	return assembleWALParts(restoring)
}

// assembleWALParts rebuilds, in dir, each wal file whose parts an incremental
// backup holds (spec E4, "Location, lock, ..."): it concatenates them in
// offset order, from a whole wal file of that number if there is one, and
// stops at the first gap, leaving what follows to recovery as a torn tail;
// then it removes the parts.
func assembleWALParts(dir string) error {
	parts, err := walPartsIn(dir)
	if err != nil {
		return err
	}

	for n, ps := range parts {
		if err = assembleWALFile(dir, n, sortedParts(ps)); err != nil {
			return err
		}
	}

	return nil
}

// walPartsIn returns the wal parts and whole wal files in dir, by number.
func walPartsIn(dir string) (map[uint32][]backupPart, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}

	parts := make(map[uint32][]backupPart)

	for _, entry := range entries {
		n, off, ok := parseBackupPart(entry.Name())
		if !ok || !entry.Type().IsRegular() {
			continue
		}

		info, erri := entry.Info()
		if erri != nil {
			return nil, erri
		}

		parts[n] = append(parts[n], backupPart{name: entry.Name(), off: off, size: info.Size()})
	}

	return parts, nil
}

// assembleWALFile appends to wal file n in dir, created if need be, each of
// parts, sorted by offset, that starts where the file ends, until one does
// not, then removes every part but the whole file. Parts holding nothing from
// offset 0 make no file.
func assembleWALFile(dir string, n uint32, parts []backupPart) error {
	whole := filepath.Base(walFilePath(dir, n))

	var end int64

	if parts[0].name == whole {
		end = parts[0].size
		parts = parts[1:]
	}

	tiled := 0

	for _, p := range parts {
		if p.off != end {
			break
		}

		end += p.size
		tiled++
	}

	var err error
	if tiled > 0 {
		err = appendParts(dir, walFilePath(dir, n), parts[:tiled])
	}

	for _, p := range parts {
		err = errors.Join(err, os.Remove(filepath.Join(dir, p.name)))
	}

	return err
}

// appendParts appends the content of each of parts, files in dir, to the file
// at path, creating it if need be, and fsyncs it.
func appendParts(dir, path string, parts []backupPart) error {
	out, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_APPEND, dbFilePermission)
	if err != nil {
		return err
	}

	for _, p := range parts {
		if err = appendFileTo(out, filepath.Join(dir, p.name)); err != nil {
			return errors.Join(err, out.Close())
		}
	}

	return errors.Join(out.Sync(), out.Close())
}

// appendFileTo copies the file at path to out.
func appendFileTo(out io.Writer, path string) error {
	in, err := os.Open(path)
	if err != nil {
		return err
	}

	_, err = io.Copy(out, in)

	return errors.Join(err, in.Close())
}

// fillFromFile fills restoring from the one-file backup at path: the members
// of a self-contained backup, or a store built from a bbolt file, once
// dbFile's filesystem is known to have room for it.
func fillFromFile(ctx context.Context, dbFile, restoring, path string) error {
	isTar, err := hasTarMagic(path)
	if err != nil {
		return err
	}

	if isTar {
		return extractStoreBackup(path, restoring)
	}

	isBolt, err := hasBoltMagic(path)
	if err != nil {
		return err
	}

	if !isBolt {
		return fmt.Errorf("%w: %s", errNotABackup, path)
	}

	if err = checkFreeSpaceFor(path, filepath.Dir(dbFile)); err != nil {
		return err
	}

	upgrade := newDBUpgradeReporter(ctx, dbFile)
	err = buildStoreFromBolt(ctx, path, restoring, upgrade)
	upgrade.finish(err)

	return err
}

// hasTarMagic reports whether the regular file at path holds a tar header's
// magic. A read error that openErrorThatIsNotCorruption claims is returned as
// it words it; a file too short to hold the magic is not a tar.
func hasTarMagic(path string) (bool, error) {
	magic, err := readFileAt(path, storeTarMagicOffset, len(storeTarMagic))
	if err != nil || magic == nil {
		return false, err
	}

	return string(magic) == storeTarMagic, nil
}

// readFileAt returns n bytes of the file at path from offset off, or nil if
// it ends before them. Any other error is returned as
// openErrorThatIsNotCorruption words it, if it claims it.
func readFileAt(path string, off int64, n int) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, unreadableOr(err, path)
	}

	buf := make([]byte, n)
	_, err = f.ReadAt(buf, off)

	if errc := f.Close(); err == nil {
		err = errc
	}

	switch {
	case errors.Is(err, io.EOF):
		return nil, nil
	case err != nil:
		return nil, unreadableOr(err, path)
	}

	return buf, nil
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
		if !isBackupMember(hdr) {
			return fmt.Errorf("%w: %s holds %q", errNotSelfContainedBackup, path, hdr.Name)
		}

		if err = extractBackupMember(tr, root, path, hdr.Name); err != nil {
			return err
		}
	}

	if errors.Is(err, io.EOF) {
		return nil
	}

	return fmt.Errorf("%w: %s: %w", errNotSelfContainedBackup, path, err)
}

// isBackupMember reports whether hdr can be a member of a self-contained
// backup: a regular file named without a directory. "." and ".." name
// directories, so are refused.
func isBackupMember(hdr *tar.Header) bool {
	return hdr.Typeflag == tar.TypeReg && filepath.Base(hdr.Name) == hdr.Name && hdr.Name != "." && hdr.Name != ".."
}

// extractBackupMember writes the rest of tr's current member, of the tar at
// path, to name in root, refusing a name an earlier member had.
func extractBackupMember(tr *tar.Reader, root *os.Root, path, name string) error {
	out, err := root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, dbFilePermission)
	if errors.Is(err, fs.ErrExist) {
		return fmt.Errorf("%w: %s holds %q more than once: %w", errNotSelfContainedBackup, path, name, err)
	}

	if err != nil {
		return err
	}

	_, err = io.Copy(out, tr)
	if err = errors.Join(err, out.Close()); err != nil {
		return err
	}

	return callRestoreHook(restoreAtCopied)
}

// syncTree fsyncs every regular file and directory under dir, dir included.
func syncTree(dir string) error {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return err
	}

	err = fs.WalkDir(root.FS(), ".", func(path string, entry fs.DirEntry, err error) error {
		if err != nil || (!entry.IsDir() && !entry.Type().IsRegular()) {
			return err
		}

		f, err := root.Open(path)
		if err != nil {
			return err
		}

		return errors.Join(f.Sync(), f.Close())
	})

	return errors.Join(err, root.Close())
}

// replaceWithRestored is restore step 3: it sets dbFile aside as
// dbFile + ".corrupt", if it is there, renames restoring to dbFile, durably,
// then removes ".corrupt".
func replaceWithRestored(dbFile, restoring string) error {
	corrupt := dbFile + storeCorruptSuffix

	err := setAside(dbFile, corrupt)
	if err != nil {
		return err
	}

	if err = os.Rename(restoring, dbFile); err != nil {
		return err
	}

	if err = syncDir(filepath.Dir(dbFile)); err != nil {
		return err
	}

	if err = callRestoreHook(restoreAtReplaced); err != nil {
		return err
	}

	return os.RemoveAll(corrupt)
}

// setAside renames dbFile, if it is there, to corrupt.
func setAside(dbFile, corrupt string) error {
	_, err := os.Lstat(dbFile)

	switch {
	case errors.Is(err, fs.ErrNotExist):
		return nil
	case err != nil:
		return err
	}

	if err = os.Rename(dbFile, corrupt); err != nil {
		return err
	}

	return callRestoreHook(restoreAtSetAside)
}

// openSelfContainedLogStore restores the self-contained backup at cfg.dbFile
// into a store directory there, removing the file, and opens it. If the file
// cannot be restored it is left as it was.
func openSelfContainedLogStore(ctx context.Context, cfg storeConfig) (*logStore, string, error) {
	if err := restoreLogStore(ctx, cfg.dbFile, dirTarget{path: cfg.dbFile}); err != nil {
		return nil, "", err
	}

	s, err := openLogStoreFor(ctx, cfg)

	return s, "restored db directory " + cfg.dbFile + " from self-contained backup file", err
}

// restoreCorruptLogStore restores the log store at cfg.dbFile, which cause
// says is corrupt, from cfg's backup and opens it, with the messages initDB
// gives for a corrupt bbolt file. With no backup, or one that cannot be
// restored, it returns cause, and dbFile is left as it was.
func restoreCorruptLogStore(ctx context.Context, cfg storeConfig, cause error) (*logStore, string, error) {
	backup, err := backupToRestore(cfg)
	if err != nil || backup == nil {
		return nil, "", errors.Join(cause, err)
	}

	if err = restoreLogStore(ctx, cfg.dbFile, backup); err != nil {
		return nil, fmt.Sprintf("tried to recreate corrupt (?) db file %s from %s (error with original db file "+
			"was: %s)", cfg.dbFile, backup.describe(), cause), errors.Join(cause, err)
	}

	s, err := openLogStoreFor(ctx, cfg)

	return s, fmt.Sprintf("recreated corrupt (?) db file %s from backup file %s (error with original db file "+
		"was: %s)", cfg.dbFile, backup.location(), cause), err
}

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

// unreadableOr returns err as openErrorThatIsNotCorruption words it, if it is
// one of its errors, else err unchanged.
func unreadableOr(err error, dbFile string) error {
	if unreadable := openErrorThatIsNotCorruption(err, dbFile); unreadable != nil {
		return unreadable
	}

	return err
}

// openLogStoreInOrder opens the log store at cfg.dbFile in spec E2's order,
// after openStoreKind has taken lock and made any development wipe: it puts
// back or removes what an interrupted restore left beside dbFile (see
// undoRestoreLeftovers), resumes or makes a
// conversion of a bbolt file there (E1), then opens what dbFile is: nothing
// (see openMissingLogStore), a self-contained backup (see
// openSelfContainedLogStore) or a store. A corrupt store, a corrupt bbolt
// file, or anything else at dbFile is restored from backup (see
// restoreCorruptLogStore). The store holds lock until it closes; if it cannot
// be opened, lock is released.
func openLogStoreInOrder(ctx context.Context, cfg storeConfig, lock *storeLock) (*logStore, string, error) {
	s, msg, err := openLogStoreAt(ctx, cfg)
	if err != nil {
		return nil, msg, errors.Join(err, lock.release())
	}

	s.lock = lock

	return s, msg, nil
}

// openLogStoreAt is openLogStoreInOrder without the lock.
func openLogStoreAt(ctx context.Context, cfg storeConfig) (*logStore, string, error) {
	if err := undoRestoreLeftovers(cfg.dbFile); err != nil {
		return nil, "", unreadableOr(err, cfg.dbFile)
	}

	converted, err := convertOnOpen(ctx, cfg.dbFile)
	if errors.Is(err, errStoreCorrupt) {
		return restoreCorruptLogStore(ctx, cfg, err)
	}

	if err != nil {
		return nil, "", err
	}

	return openDBFileForm(ctx, cfg, converted)
}

// undoRestoreLeftovers puts back what a restore interrupted between its two
// renames set aside (spec E2 order, step 2): with no dbFile, a dbFile +
// ".corrupt" is renamed back to dbFile, durably, as it may be the only copy of
// the data, such as a self-contained backup restored in place. Then it removes
// any dbFile + ".restoring", and any ".corrupt" beside a store directory, the
// restored store a restore's rename to dbFile leaves, so the restore, if still
// needed, runs again from the original dbFile. A ".corrupt" beside a dbFile
// that is not a directory was not left by a restore, so it returns
// errCorruptBesideDBFile, removing nothing.
func undoRestoreLeftovers(dbFile string) error {
	corrupt := dbFile + storeCorruptSuffix

	info, err := os.Lstat(dbFile)

	switch {
	case errors.Is(err, fs.ErrNotExist):
		err = putBack(corrupt, dbFile)
	case err == nil && !info.IsDir():
		err = refuseCorruptBeside(dbFile, corrupt)
	}

	if err != nil {
		return err
	}

	return errors.Join(os.RemoveAll(dbFile+storeRestoringSuffix), os.RemoveAll(corrupt))
}

// putBack renames corrupt, if it is there, to dbFile, durably.
func putBack(corrupt, dbFile string) error {
	err := os.Rename(corrupt, dbFile)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}

	if err != nil {
		return err
	}

	return syncDir(filepath.Dir(dbFile))
}

// refuseCorruptBeside returns errCorruptBesideDBFile, naming dbFile and
// corrupt, if corrupt is there.
func refuseCorruptBeside(dbFile, corrupt string) error {
	_, err := os.Lstat(corrupt)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}

	if err != nil {
		return err
	}

	return fmt.Errorf("%w: %s and %s; both have been left untouched, so keep the one holding the data, "+
		"remove the other, and start wr again", errCorruptBesideDBFile, dbFile, corrupt)
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
