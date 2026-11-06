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

// This file converts a bbolt database at dbFile into a log store the first
// time a manager opens it (spec E1), crash-safely: the store is built beside
// dbFile in dbFile + ".converting", whose SOURCE file names where the bbolt
// file is kept for rollback, and renamed into place once whole.
//
// Phase 1 form: the store is a checkpoint of the live jobs, envs, limits and
// dep groups, and history.bolt holding the bbolt history buckets, stats
// included.

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime/debug"
	"slices"
	"strings"
	"sync/atomic"
	"syscall"

	"github.com/VertebrateResequencing/wr/clog"
	"github.com/VertebrateResequencing/wr/internal"
	"github.com/VertebrateResequencing/wr/jobqueue/wal"
	"github.com/ugorji/go/codec"
	bolt "go.etcd.io/bbolt"
	berrors "go.etcd.io/bbolt/errors"
	"golang.org/x/sys/unix"
)

const (
	// storeConvertSourceFile names the file in dbFile + ".converting", and
	// in a store a crash left it in, naming the bbolt file being converted.
	storeConvertSourceFile = "SOURCE"

	// boltMetaMagicOffset is where a bbolt file's first meta page's magic
	// is, after its page header.
	boltMetaMagicOffset = 16

	// convertWalFrom is the cut of a converted store's checkpoint, and the
	// wal file of its history.bolt's folded position: the store's first wal
	// file, which its first open starts.
	convertWalFrom uint32 = 1

	// convertDevVersion is the version in a pre name when ServerVersion is
	// empty, as it is for a wr not built by the Makefile.
	convertDevVersion = "dev"
)

var (
	// errNotEnoughSpace is a conversion's error when dbFile's filesystem has
	// less free space than the bbolt file's size.
	errNotEnoughSpace = errors.New("not enough free space to convert the database")

	// errBoltPreExists is a conversion's error when the name it would keep
	// the bbolt file under for rollback is taken.
	errBoltPreExists = errors.New("the name a database conversion keeps the bbolt file under is taken")

	// errConvertSourceRead marks an error reading the bbolt file being
	// converted, which means it is corrupt unless openErrorThatIsNotCorruption
	// says otherwise.
	errConvertSourceRead = errors.New("could not read the bbolt database being converted")

	// errBoltUnconvertible is a conversion's error when the bbolt file holds
	// a record that bbolt reads but the conversion cannot: the file is left at
	// dbFile as it was, and is not taken for corrupt.
	errBoltUnconvertible = errors.New("the bbolt database holds a record that cannot be converted")

	// errBadConvertSource is the error of a start finding an interrupted
	// conversion whose SOURCE names no file a conversion keeps a bbolt file
	// under.
	errBadConvertSource = errors.New("an interrupted database conversion names no bbolt file it kept")

	// errBoltPanicked is the cause of an errConvertSourceRead that was a
	// panic of bbolt's reading the bbolt file being converted.
	errBoltPanicked = errors.New("bbolt panicked")

	// errConvertPanicked is the error of a conversion that panicked other
	// than in bbolt's reading the bbolt file.
	errConvertPanicked = errors.New("the database conversion panicked")
)

// boltMetaMagic is the magic of a bbolt file's meta pages.
const boltMetaMagic uint32 = 0xED0CDAED

// convertPoint is a point in a conversion that logConvertHook is called at.
type convertPoint uint8

const (
	convertAtCreating   convertPoint = iota // step 2, before SOURCE is written
	convertAtCreated                        // after step 2
	convertAtRenamed                        // after step 3
	convertAtCounting                       // in step 4, before the jobs are counted
	convertAtJob                            // in step 4, after each job
	convertAtBuilt                          // after step 4, before step 5
	convertAtPublishing                     // in step 5, before the rename
	convertAtPublished                      // in step 5, before SOURCE is deleted
	convertAtRestored                       // abandoning, before .converting is removed
)

// Test seams, nil outside tests.
var (
	// logConvertHook is called at each convertPoint of a conversion; an error
	// it returns ends the conversion there, as a crash would.
	logConvertHook atomic.Pointer[func(at convertPoint) error] //nolint:gochecknoglobals // a test seam

	// storeFreeSpaceHook, if set, replaces freeSpace's look at a directory's
	// filesystem.
	storeFreeSpaceHook atomic.Pointer[func(dir string) (uint64, error)] //nolint:gochecknoglobals // a test seam
)

// boltConversion is one conversion of a bbolt file at dbFile: where it
// builds the store, the pre name it keeps the bbolt file under, where the
// bbolt file is now, and whether it resumes one a crash interrupted.
type boltConversion struct {
	dbFile, converting, pre, source string
	resumed                         bool
}

// pendingConversion returns the conversion to make at open, or nil if there
// is none: a resumed one if dbFile + ".converting" holds SOURCE, whose other
// files it removes, else one of dbFile if it is a bbolt file. A
// ".converting" without SOURCE, left by a crash before SOURCE was written, is
// removed.
func pendingConversion(dbFile string) (*boltConversion, error) {
	c := &boltConversion{dbFile: dbFile, converting: dbFile + storeConvertingSuffix}

	name, err := os.ReadFile(filepath.Join(c.converting, storeConvertSourceFile))
	switch {
	case err == nil:
		return c.resume(string(name))
	case !errors.Is(err, fs.ErrNotExist) && !errors.Is(err, syscall.ENOTDIR):
		return nil, unreadableOr(err, dbFile)
	}

	if err = os.RemoveAll(c.converting); err != nil {
		return nil, err
	}

	isBolt, err := isBoltFile(dbFile)
	if err != nil || !isBolt {
		return nil, err
	}

	c.pre = dbFile + storeBoltPreInfix + convertVersion()
	c.source = dbFile

	return c, nil
}

// resume sets c to resume the interrupted conversion whose SOURCE names name:
// its bbolt file is at dbFile if step 3 did not run, else at name. It removes
// everything else in c's ".converting", to build afresh.
func (c *boltConversion) resume(name string) (*boltConversion, error) {
	kept := strings.HasPrefix(name, filepath.Base(c.dbFile)+storeBoltPreInfix)
	if !kept || strings.ContainsRune(name, os.PathSeparator) {
		return nil, fmt.Errorf("%w: %s names %q, not a bbolt file kept beside %s; it, %s and %s are left as "+
			"they are", errBadConvertSource, filepath.Join(c.converting, storeConvertSourceFile), name, c.dbFile,
			c.converting, c.dbFile)
	}

	c.pre = filepath.Join(filepath.Dir(c.dbFile), name)
	c.source = c.pre
	c.resumed = true

	if err := clearConverting(c.converting); err != nil {
		return nil, err
	}

	isBolt, err := isBoltFile(c.dbFile)
	if isBolt {
		c.source = c.dbFile
	}

	return c, err
}

// clearConverting removes every file in dir but SOURCE.
func clearConverting(dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}

	for _, entry := range entries {
		if entry.Name() == storeConvertSourceFile {
			continue
		}

		if err = os.RemoveAll(filepath.Join(dir, entry.Name())); err != nil {
			return err
		}
	}

	return nil
}

// isBoltFile reports whether path is a regular file starting with a bbolt
// meta page's magic. An absent path is not.
func isBoltFile(path string) (bool, error) {
	info, err := os.Stat(path)
	if errors.Is(err, fs.ErrNotExist) || (err == nil && !info.Mode().IsRegular()) {
		return false, nil
	}

	if err != nil {
		return false, unreadableOr(err, path)
	}

	return hasBoltMagic(path)
}

// validate is step 0: it opens the bbolt file read-only, which checks its
// meta pages, and refuses a malformed or too new schema stamp.
func (c *boltConversion) validate() error {
	version, err := boltSourceSchema(c.source)
	if err != nil {
		return err
	}

	return checkDBSchemaVersion(c.dbFile, version)
}

// boltSourceSchema returns the schema version of the bbolt file at path,
// opened read-only. An error opening or reading it, or a panic of bbolt's,
// wraps errConvertSourceRead.
func boltSourceSchema(path string) (version uint64, err error) {
	src, err := openBoltSource(path)
	if err != nil {
		return 0, err
	}

	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("%w: %s: %v", errConvertSourceRead, path, r)
		}

		err = errors.Join(err, src.Close())
	}()

	return dbFileSchemaVersion(src)
}

// failed returns err, a step's error, as the conversion's. One of bbolt's
// reading the bbolt file that openErrorThatIsNotCorruption does not claim
// means the file is corrupt: the conversion is abandoned (see abandon) and
// the error wraps errStoreCorrupt. A claimed one is returned as that function
// words it. One wrapping errBoltUnconvertible is abandoned too, but the file
// is not corrupt, so the error does not say it is. Any other, such as a write
// error on the new store or a panic of the conversion's own, is returned
// unchanged, for a later open to resume from.
func (c *boltConversion) failed(err error) error {
	switch {
	case errors.Is(err, errConvertSourceRead):
		if claimed := openErrorThatIsNotCorruption(err, c.source); claimed != nil {
			return claimed
		}

		return c.abandon(err, fmt.Errorf("%w: bbolt database %s: %w", errStoreCorrupt, c.dbFile, err))
	case errors.Is(err, errBoltUnconvertible):
		return c.abandon(err, fmt.Errorf("bbolt database %s, left as it was: %w", c.dbFile, err))
	}

	return err
}

// abandon undoes the conversion of a bbolt file that failed for cause: it
// renames the file back to dbFile if step 3 moved it, then removes
// ".converting", so that a crash between the two resumes on the file at
// dbFile, which fails the same way again, and the next open never loops. It
// returns failure, or cause and the error undoing the conversion.
func (c *boltConversion) abandon(cause, failure error) error {
	if err := c.renameBack(); err != nil {
		return errors.Join(cause, err)
	}

	if err := callConvertHook(convertAtRestored); err != nil {
		return err
	}

	if err := os.RemoveAll(c.converting); err != nil {
		return errors.Join(cause, err)
	}

	return failure
}

// callConvertHook returns the error of logConvertHook at at, if it is set.
func callConvertHook(at convertPoint) error {
	if hook := logConvertHook.Load(); hook != nil {
		return (*hook)(at)
	}

	return nil
}

// renameBack renames the bbolt file back to dbFile, durably, if step 3 moved
// it to its pre name.
func (c *boltConversion) renameBack() error {
	if c.source != c.pre {
		return nil
	}

	if err := os.Rename(c.pre, c.dbFile); err != nil {
		return err
	}

	return syncDir(filepath.Dir(c.dbFile))
}

// checkRoom is step 1: a bbolt file still at dbFile is refused if its pre
// name is taken, and the conversion if dbFile's filesystem has less free
// space than the bbolt file's size.
func (c *boltConversion) checkRoom() error {
	if err := c.checkPreFree(); err != nil {
		return err
	}

	info, err := os.Stat(c.source)
	if err != nil {
		return err
	}

	dir := filepath.Dir(c.dbFile)

	free, err := freeSpace(dir)
	if err != nil {
		return err
	}

	if need := uint64(info.Size()); free < need { //nolint:gosec // a file size
		return fmt.Errorf("%w: converting %s needs %d bytes free in %s, which has %d", errNotEnoughSpace,
			c.source, need, dir, free)
	}

	return nil
}

// freeSpace returns how many bytes are free to this process on dir's
// filesystem.
func freeSpace(dir string) (uint64, error) {
	if hook := storeFreeSpaceHook.Load(); hook != nil {
		return (*hook)(dir)
	}

	var st unix.Statfs_t
	if err := unix.Statfs(dir, &st); err != nil {
		return 0, err
	}

	return uint64(st.Bavail) * uint64(st.Bsize), nil //nolint:gosec,unconvert // types vary by platform
}

// checkPreFree returns errBoltPreExists, naming the pre name, if the bbolt
// file is still at dbFile and its pre name is taken.
func (c *boltConversion) checkPreFree() error {
	if c.source != c.dbFile {
		return nil
	}

	_, err := os.Lstat(c.pre)
	if err == nil {
		return fmt.Errorf("%w: %s", errBoltPreExists, c.pre)
	}

	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}

	return err
}

// begin is step 2: it creates ".converting" holding SOURCE, which names the
// pre name, durably. A resumed conversion has it already.
func (c *boltConversion) begin() error {
	if c.resumed {
		return nil
	}

	if err := os.Mkdir(c.converting, logStoreDirPerm); err != nil {
		return err
	}

	if err := callConvertHook(convertAtCreating); err != nil {
		return err
	}

	err := writeFileDurably(filepath.Join(c.converting, storeConvertSourceFile), []byte(filepath.Base(c.pre)))
	if err == nil {
		err = syncDir(filepath.Dir(c.dbFile))
	}

	if err != nil {
		return err
	}

	return callConvertHook(convertAtCreated)
}

// setAside is step 3: it renames a bbolt file still at dbFile to its pre
// name, durably.
func (c *boltConversion) setAside() error {
	if c.source != c.dbFile {
		return nil
	}

	if err := os.Rename(c.dbFile, c.pre); err != nil {
		return err
	}

	c.source = c.pre

	if err := syncDir(filepath.Dir(c.dbFile)); err != nil {
		return err
	}

	return callConvertHook(convertAtRenamed)
}

// publish is step 5: it makes ".converting" durable, renames it to dbFile,
// durably, then deletes SOURCE from it, which recovery deletes if a crash
// leaves it.
func (c *boltConversion) publish() error {
	if err := syncDir(c.converting); err != nil {
		return err
	}

	if err := callConvertHook(convertAtPublishing); err != nil {
		return err
	}

	if err := os.Rename(c.converting, c.dbFile); err != nil {
		return err
	}

	if err := syncDir(filepath.Dir(c.dbFile)); err != nil {
		return err
	}

	if err := callConvertHook(convertAtPublished); err != nil {
		return err
	}

	return removeConvertSource(c.dbFile)
}

// removeConvertSource deletes SOURCE from the store directory dir, if it is
// there (spec "Recovery", step 1).
func removeConvertSource(dir string) error {
	err := os.Remove(filepath.Join(dir, storeConvertSourceFile))
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}

	return err
}

// convertBoltDB converts the bbolt file at dbFile into a store at dbFile,
// keeping the bbolt file at its pre name, or resumes a conversion a crash
// interrupted (spec E1). It does nothing if dbFile is not a bbolt file and no
// conversion was interrupted. A corrupt bbolt file is put back at dbFile, and
// the error wraps errStoreCorrupt.
func convertBoltDB(ctx context.Context, dbFile string, upgrade *dbUpgradeReporter) error {
	c, err := pendingConversion(dbFile)
	if err != nil || c == nil {
		return err
	}

	if err = c.validate(); err != nil {
		return c.failed(err)
	}

	steps := []func() error{
		c.checkRoom,
		c.begin,
		c.setAside,
		func() error { return c.failed(buildStoreFromBolt(ctx, c.source, c.converting, upgrade)) },
		func() error { return callConvertHook(convertAtBuilt) },
		c.publish,
	}

	for _, step := range steps {
		if err = step(); err != nil {
			return err
		}
	}

	return nil
}

// buildStoreFromBolt is step 4: it reads boltFile read-only and writes a
// store into the empty directory dir: its VERSION, history.bolt from the
// bbolt history buckets, a checkpoint of the live jobs, overlaid with their
// run-state records, their std, and the envs, limits and dep groups, and the
// first wal file, holding only its header.
// Its records take seqs 1, 2, ..., completions first, so the store's first
// open starts its counter above them. An error or panic of bbolt's reading
// boltFile wraps errConvertSourceRead, and a record bbolt reads that the
// build cannot errBoltUnconvertible; any other error is the build's own,
// such as errConvertPanicked for a panic of its own reading boltFile.
func buildStoreFromBolt(ctx context.Context, boltFile, dir string, upgrade *dbUpgradeReporter) (err error) {
	src, err := openBoltSource(boltFile)
	if err != nil {
		return err
	}

	defer func() { err = errors.Join(err, src.Close()) }()

	if err = os.MkdirAll(dir, logStoreDirPerm); err != nil {
		return err
	}

	if err = writeStoreVersion(dir); err != nil {
		return err
	}

	b, err := newStoreBuild(ctx, dir, upgrade)
	if err != nil {
		return err
	}

	err = errors.Join(b.read(src), b.close())
	if err != nil {
		return err
	}

	return startConvertedWAL(dir)
}

// openBoltSource opens the bbolt file at path read-only, so the open writes
// nothing. An error, or a panic of bbolt's, wraps errConvertSourceRead.
func openBoltSource(path string) (src *bolt.DB, err error) {
	defer func() {
		if r := recover(); r != nil {
			src, err = nil, fmt.Errorf("%w: %s: %v", errConvertSourceRead, path, r)
		}
	}()

	src, err = bolt.Open(path, dbFilePermission,
		&bolt.Options{ReadOnly: true, Timeout: managerDBOpenTimeout, FreelistType: bolt.FreelistMapType})
	if err != nil {
		return nil, fmt.Errorf("%w: %s: %w", errConvertSourceRead, path, err)
	}

	return src, nil
}

// newStoreBuild opens history.bolt and the checkpoint in dir for writing.
func newStoreBuild(ctx context.Context, dir string, upgrade *dbUpgradeReporter) (*storeBuild, error) {
	b := &storeBuild{ctx: ctx, upgrade: upgrade, ch: new(codec.BincHandle),
		header: checkpointHeader{walFrom: convertWalFrom, histFrom: historyPos{file: convertWalFrom}}}

	history, err := openHistoryBolt(dir, b.ch)
	if err != nil {
		return nil, err
	}

	b.history = history

	tx, err := history.bolt.Begin(true)
	if err != nil {
		return nil, errors.Join(err, history.close())
	}

	b.hw = &stdStrippingCopier{dst: history.bolt, tx: tx, txMaxSize: compactTxMaxSize, ch: b.ch}

	b.ckpt, err = os.OpenFile(checkpointPath(dir, convertWalFrom), os.O_WRONLY|os.O_CREATE|os.O_TRUNC,
		dbFilePermission)
	if err != nil {
		return nil, errors.Join(err, tx.Rollback(), history.close())
	}

	b.cw = &checkpointWriter{w: bufio.NewWriter(b.ckpt), refFile: convertWalFrom | checkpointRefFlag}

	if err = b.cw.frame(b.header.append(nil)); err != nil {
		return nil, errors.Join(err, b.close())
	}

	return b, nil
}

// startConvertedWAL writes the header of the converted store's wal file
// convertWalFrom, which recovery requires as its checkpoint's cut, durably,
// and nothing else.
func startConvertedWAL(dir string) error {
	w, err := wal.NewWriter(dir, convertWalFrom, walSegmentSize, func(error) {})
	if err != nil {
		return err
	}

	return w.Close()
}

// storeBuild is buildStoreFromBolt's writing of a store: history.bolt,
// through a copier that commits every compactTxMaxSize bytes and strips std
// from complete records, and the checkpoint.
type storeBuild struct {
	ctx     context.Context //nolint:containedctx // the build's, checked between jobs
	upgrade *dbUpgradeReporter
	ch      codec.Handle

	history *historyBolt
	hw      *stdStrippingCopier

	ckpt    *os.File
	cw      *checkpointWriter
	header  checkpointHeader
	seq     uint64
	histSeq uint64

	processed, total int
}

// close rolls back history.bolt's transaction if it was not committed, and
// closes history.bolt and the checkpoint.
func (b *storeBuild) close() error {
	err := b.hw.tx.Rollback()
	if errors.Is(err, berrors.ErrTxClosed) {
		err = nil
	}

	return errors.Join(err, b.history.close(), b.ckpt.Close())
}

// read reads src in one transaction, writing history.bolt then the
// checkpoint, after starting the conversion's phase, as counting the jobs
// first takes most of a large conversion's time.
//
// bbolt reads src through sourceBucket, eachSource and sourceGet, which turn
// a panic of bbolt's into an error wrapping errConvertSourceRead. The
// exception is eachLiveJobTx, called by addLiveJobs, which walks jobslive and
// jobrunstate unguarded; countJobs walks both through eachSource first, in the
// same transaction, so a damaged page in either is found there. Any other
// panic is the build's own, and read returns it wrapping errConvertPanicked.
func (b *storeBuild) read(src *bolt.DB) (err error) {
	defer b.recoverPanic(&err)

	b.upgrade.startPhase(internal.DBUpgradeConvertState, "counting the jobs to convert")

	tx, err := src.Begin(false)
	if err != nil {
		return sourceReadError(err)
	}

	defer func() { err = errors.Join(err, tx.Rollback()) }()

	if err = callConvertHook(convertAtCounting); err != nil {
		return err
	}

	if b.total, err = countJobs(tx); err != nil {
		return err
	}

	b.upgrade.setPhaseTotal(b.progressDetail(), b.total)

	if err = b.write(tx); err != nil {
		return err
	}

	b.upgrade.completePhase(internal.DBUpgradeConvertState, b.progressDetail(), b.processed)

	return nil
}

// sourceReadError wraps err, an error of bbolt's reading the bbolt file being
// converted, in errConvertSourceRead.
func sourceReadError(err error) error {
	return fmt.Errorf("%w: %w", errConvertSourceRead, err)
}

// countJobs returns how many values, not nested buckets, tx's jobslive and
// jobscomplete hold. It also walks jobrunstate, counting nothing, so that
// every bucket eachLiveJobTx walks has been read through eachSource first.
func countJobs(tx *bolt.Tx) (int, error) {
	n := 0

	for _, name := range [][]byte{bucketJobsLive, bucketJobsComplete, bucketJobRunState} {
		counted := !bytes.Equal(name, bucketJobRunState)

		err := eachSource(tx, name, func(_, v []byte) error {
			if counted && v != nil {
				n++
			}

			return nil
		})
		if err != nil {
			return 0, err
		}
	}

	return n, nil
}

// recoverPanic, deferred by read, sets *err to a panic of the build's own,
// wrapping errConvertPanicked, and logs the stack it panicked on.
func (b *storeBuild) recoverPanic(err *error) {
	if r := recover(); r != nil {
		clog.Warn(b.ctx, "the database conversion panicked", "panic", r, "stack", string(debug.Stack()))

		*err = fmt.Errorf("%w: %v", errConvertPanicked, r)
	}
}

// write writes history.bolt then the checkpoint from tx.
func (b *storeBuild) write(tx *bolt.Tx) error {
	steps := []func() error{
		func() error { return b.copyCompletes(tx) },
		func() error { return b.copyHistory(tx) },
		b.commitHistory,
		func() error { return b.addLiveJobs(tx) },
		func() error { return b.addUnreferenced(tx) },
		b.writeCheckpoint,
	}

	for _, step := range steps {
		if err := step(); err != nil {
			return err
		}
	}

	return nil
}

// progressDetail is the conversion's status detail.
func (b *storeBuild) progressDetail() string {
	return fmt.Sprintf("converting database (%d of %d jobs processed)", b.processed, b.total)
}

// jobDone counts a job converted, reporting progress when due, and stops the
// build if its context is done.
func (b *storeBuild) jobDone() error {
	b.processed++

	if b.upgrade.progressDue(b.processed) {
		b.upgrade.writeProgress(internal.DBUpgradeConvertState, b.progressDetail(), b.processed)
	}

	if err := b.ctx.Err(); err != nil {
		return err
	}

	return callConvertHook(convertAtJob)
}

// nextSeq returns the next record's seq.
func (b *storeBuild) nextSeq() uint64 {
	b.seq++

	return b.seq
}

// copyCompletes copies each complete record into history.bolt, without its
// StdOutC and StdErrC (which wr before v0.37.3 kept, and which every schema
// drops, at the cost of decoding each record), with its key's completeSeq, the
// seq of its complete record. Unlike compaction, it does not copy a record it
// cannot decode as it is, as it could not then drop its std.
func (b *storeBuild) copyCompletes(tx *bolt.Tx) error {
	return eachSource(tx, bucketJobsComplete, func(k, v []byte) error {
		if v == nil {
			return nil
		}

		return b.copyComplete(k, v)
	})
}

// eachSource calls fn with each key and value of tx's bucket name, tx being of
// the bbolt file being converted, in key order, until fn returns an error;
// none if tx has no such bucket. A panic of bbolt's finding or moving through
// the bucket wraps errConvertSourceRead; fn's errors and panics are its own.
func eachSource(tx *bolt.Tx, name []byte, fn func(k, v []byte) error) error {
	bucket, err := sourceBucket(tx, name)
	if err != nil || bucket == nil {
		return err
	}

	c := bucket.Cursor()

	k, v, err := sourceMove(c, true)
	for ; err == nil && k != nil; k, v, err = sourceMove(c, false) {
		if err = fn(k, v); err != nil {
			return err
		}
	}

	return err
}

// copyComplete is copyCompletes of the complete record v of key k.
func (b *storeBuild) copyComplete(k, v []byte) error {
	if _, err := parseLogKey(string(k)); err != nil {
		return unconvertibleError(fmt.Errorf("%s key: %w", bucketJobsComplete, err))
	}

	b.histSeq = b.nextSeq()

	if err := b.hw.copyValue([][]byte{bucketJobsComplete}, k, v, true); err != nil {
		return err
	}

	if b.hw.result.unreadable > 0 {
		return fmt.Errorf("%w: %s record of %q cannot be decoded", errBoltUnconvertible, bucketJobsComplete, k)
	}

	if err := b.hw.put([][]byte{bucketCompleteSeq}, k, binary.BigEndian.AppendUint64(nil, b.histSeq)); err != nil {
		return err
	}

	return b.jobDone()
}

// unconvertibleError wraps err, about a record of the bbolt file being
// converted that the conversion cannot read, in errBoltUnconvertible.
func unconvertibleError(err error) error {
	return fmt.Errorf("%w: %w", errBoltUnconvertible, err)
}

// copyHistory copies the history buckets other than jobscomplete into
// history.bolt, with keyRepgroups as repgroupToKey's reverse, and the dep
// groups ever added (see depGroups).
func (b *storeBuild) copyHistory(tx *bolt.Tx) error {
	for _, name := range [][]byte{bucketRTK, bucketRGs, bucketDTK, bucketRDTK, bucketEndTimeToKey,
		bucketRGEndTime, bucketJobRAM, bucketJobDisk, bucketJobSecs} {
		if err := b.copyBucket(tx, name); err != nil {
			return err
		}
	}

	reversed, err := reverseLookups(tx)
	if err != nil {
		return err
	}

	if err = b.putKeys(bucketKeyRGs, reversed); err != nil {
		return err
	}

	groups, err := depGroups(tx)
	if err != nil {
		return err
	}

	return b.putKeys(bucketDepGroups, groups)
}

// reverseLookups returns the keyRepgroups key, job key then rep group, of
// each of tx's repgroupToKey keys, rep group then job key. It holds them all,
// so that they can be put in key order, as bbolt puts in random order slowly;
// a conversion of a production database of 2.27M jobs (7.9GB) peaked at
// about 1.3GB of heap and RSS, once.
func reverseLookups(tx *bolt.Tx) ([][]byte, error) {
	var keys [][]byte

	err := eachSource(tx, bucketRTK, func(k, _ []byte) error {
		jobKey := lookupEntryJobKey(k)
		if jobKey == nil {
			return nil
		}

		repGroup := k[:len(k)-len(jobKey)-len(dbDelimiter)]
		keys = append(keys, slices.Concat(jobKey, []byte(dbDelimiter), repGroup))

		return nil
	})

	return keys, err
}

// depGroups returns the dep groups ever added: depgroups' keys, or, if tx
// has no depgroups bucket, each dep group named in depgroupToKey, as initDB
// back-fills depgroups from it.
func depGroups(tx *bolt.Tx) ([][]byte, error) {
	seen, err := sourceBucket(tx, bucketDepGroups)
	if err != nil {
		return nil, err
	}

	if seen != nil {
		return sourceKeys(tx, bucketDepGroups)
	}

	var groups [][]byte

	err = eachSource(tx, bucketDTK, func(k, _ []byte) error {
		if idx := bytes.Index(k, []byte(dbDelimiter)); idx > 0 {
			groups = append(groups, slices.Clone(k[:idx]))
		}

		return nil
	})

	return slices.CompactFunc(groups, bytes.Equal), err
}

// copyBucket copies the entries of tx's bucket name, which no wr nests
// buckets in, into history.bolt's bucket name. A missing bucket copies
// nothing.
func (b *storeBuild) copyBucket(tx *bolt.Tx, name []byte) error {
	path := [][]byte{name}

	return eachSource(tx, name, func(k, v []byte) error {
		return b.hw.put(path, k, v)
	})
}

// putKeys puts each of keys, sorted, with no value, in history.bolt's bucket
// name.
func (b *storeBuild) putKeys(name []byte, keys [][]byte) error {
	slices.SortFunc(keys, bytes.Compare)

	path := [][]byte{name}

	for _, k := range keys {
		if err := b.hw.put(path, k, nil); err != nil {
			return err
		}
	}

	return nil
}

// commitHistory records history.bolt's folded position, convertWalFrom's
// start, and its highest complete seq, and commits it.
func (b *storeBuild) commitHistory() error {
	if err := putHistoryMeta(b.hw.tx, historyPos{file: convertWalFrom}, b.histSeq); err != nil {
		return err
	}

	return b.hw.tx.Commit()
}

// addLiveJobs adds to the checkpoint each live job, overlaid with its
// run-state record, as recovery reads it: its spec and state records, and a
// std record for each std stream it has.
func (b *storeBuild) addLiveJobs(tx *bolt.Tx) error {
	live, err := sourceBucket(tx, bucketJobsLive)
	if err != nil || live == nil {
		return err
	}

	std, err := sourceStdBuckets(tx)
	if err != nil {
		return err
	}

	var addErr error

	reader := &db{ch: b.ch}

	_, err = reader.eachLiveJobTx(tx, &runStateRecovery{}, func(key []byte, job *Job) error {
		addErr = b.addLiveJob(std, key, job)

		return addErr
	})

	switch {
	case addErr != nil:
		return addErr
	case err != nil:
		return unconvertibleError(err)
	}

	return nil
}

// sourceBucket returns tx's bucket name, or nil if tx has none, tx being of
// the bbolt file being converted. A panic of bbolt's wraps
// errConvertSourceRead.
func sourceBucket(tx *bolt.Tx, name []byte) (bucket *bolt.Bucket, err error) {
	defer recoverSourcePanic(&err)

	return tx.Bucket(name), nil
}

// sourceStdBuckets returns tx's stdout and stderr buckets, either nil if tx
// has none, tx being of the bbolt file being converted.
func sourceStdBuckets(tx *bolt.Tx) ([2]*bolt.Bucket, error) {
	var std [2]*bolt.Bucket

	for stream, name := range [][]byte{bucketStdO, bucketStdE} {
		bucket, err := sourceBucket(tx, name)
		if err != nil {
			return std, err
		}

		std[stream] = bucket
	}

	return std, nil
}

// addLiveJob adds job, live under key, with its std from the buckets std, to
// the checkpoint.
func (b *storeBuild) addLiveJob(std [2]*bolt.Bucket, key []byte, job *Job) error {
	k, err := parseLogKey(string(key))
	if err != nil {
		return unconvertibleError(fmt.Errorf("%s key: %w", bucketJobsLive, err))
	}

	spec, err := appendFlatSpec(nil, job, b.ch)
	if err != nil {
		return err
	}

	rs := newLogRunState(job)
	recs := [][]byte{
		appendKeyedRecord(nil, logRecSpec, b.nextSeq(), k, spec),
		appendKeyedRecord(nil, logRecState, b.nextSeq(), k, appendFlatRunState(nil, &rs)),
	}

	if recs, err = b.appendStdRecords(recs, std, key, k); err != nil {
		return err
	}

	if err = b.addRecords(recs); err != nil {
		return err
	}

	b.header.live++

	return b.jobDone()
}

// appendStdRecords appends to recs a std record of k for each of the buckets
// std, stdout then stderr, holding output under key.
func (b *storeBuild) appendStdRecords(recs [][]byte, std [2]*bolt.Bucket, key []byte, k logKey) ([][]byte, error) {
	for stream, bucket := range std {
		out, err := sourceGet(bucket, key)
		if err != nil {
			return nil, err
		}

		if len(out) > 0 {
			recs = append(recs, appendStdRecord(nil, b.nextSeq(), k, uint8(stream), out))
		}
	}

	return recs, nil
}

// sourceGet returns key's value in bucket, a bucket of the bbolt file being
// converted, or nil if bucket is nil. A panic of bbolt's wraps
// errConvertSourceRead.
func sourceGet(bucket *bolt.Bucket, key []byte) (v []byte, err error) {
	if bucket == nil {
		return nil, nil
	}

	defer recoverSourcePanic(&err)

	return bucket.Get(key), nil
}

// addRecords adds each of recs to the checkpoint.
func (b *storeBuild) addRecords(recs [][]byte) error {
	for _, rec := range recs {
		if _, err := b.cw.add(rec); err != nil {
			return err
		}

		b.header.records++
	}

	return nil
}

// addUnreferenced adds to the checkpoint an env record of each env, a limit
// record of each limit and a depgroup record of each dep group ever added.
func (b *storeBuild) addUnreferenced(tx *bolt.Tx) error {
	var recs [][]byte

	err := eachSource(tx, bucketEnvs, func(k, v []byte) error {
		recs = append(recs, appendEnvRecord(nil, b.nextSeq(), string(k), v))

		return nil
	})
	if err != nil {
		return err
	}

	if recs, err = b.appendLimitRecords(recs, tx); err != nil {
		return err
	}

	groups, err := depGroups(tx)
	if err != nil {
		return err
	}

	for _, group := range groups {
		recs = append(recs, appendDepGroupRecord(nil, b.nextSeq(), string(group)))
	}

	return b.addRecords(recs)
}

// appendLimitRecords appends to recs a limit record of each limit in tx's
// limit groups bucket, reading each as bbolt does, from its first 8 bytes.
func (b *storeBuild) appendLimitRecords(recs [][]byte, tx *bolt.Tx) ([][]byte, error) {
	err := eachSource(tx, bucketLGs, func(k, v []byte) error {
		if len(v) < limitGroupBytes {
			return fmt.Errorf("%w: limit group %q has a %d byte limit", errBoltUnconvertible, k, len(v))
		}

		recs = append(recs, appendLimitRecord(nil, b.nextSeq(), string(k),
			int64(binary.BigEndian.Uint64(v)), false)) //nolint:gosec // stored limits are never negative

		return nil
	})

	return recs, err
}

// writeCheckpoint writes the checkpoint's last block and trailer, then its
// header, now that it knows its counts and highest seq, over the placeholder
// newStoreBuild wrote, and syncs it.
func (b *storeBuild) writeCheckpoint() error {
	b.header.maxSeq = b.seq

	steps := []func() error{
		b.cw.flush,
		func() error { return b.cw.frame(checkpointTrailer(b.cw.offsets)) },
		b.cw.w.Flush,
		func() error {
			_, err := b.ckpt.WriteAt(wal.AppendFrame(nil, b.header.append(nil)), 0)

			return err
		},
		b.ckpt.Sync,
	}

	for _, step := range steps {
		if err := step(); err != nil {
			return err
		}
	}

	return nil
}

// sourceKeys returns a copy of each key of tx's bucket name, tx being of the
// bbolt file being converted, in key order.
func sourceKeys(tx *bolt.Tx, name []byte) ([][]byte, error) {
	var keys [][]byte

	err := eachSource(tx, name, func(k, _ []byte) error {
		keys = append(keys, slices.Clone(k))

		return nil
	})

	return keys, err
}

// sourceMove moves c, a cursor of the bbolt file being converted, to its first
// entry if first is set, else its next, returning the entry. A panic of
// bbolt's wraps errConvertSourceRead.
func sourceMove(c *bolt.Cursor, first bool) (k, v []byte, err error) {
	defer recoverSourcePanic(&err)

	if first {
		k, v = c.First()
	} else {
		k, v = c.Next()
	}

	return k, v, nil
}

// recoverSourcePanic, deferred by a read of the bbolt file being converted,
// sets *err to a panic of bbolt's reading it, wrapping errConvertSourceRead.
func recoverSourcePanic(err *error) {
	if r := recover(); r != nil {
		*err = sourceReadError(fmt.Errorf("%w: %v", errBoltPanicked, r))
	}
}

// hasBoltMagic reports whether the regular file at path starts with a bbolt
// meta page's magic.
func hasBoltMagic(path string) (bool, error) {
	f, err := os.Open(path)
	if err != nil {
		return false, unreadableOr(err, path)
	}

	magic := make([]byte, boltMetaMagicOffset+4) //nolint:mnd // the magic's 4 bytes
	_, err = f.ReadAt(magic, 0)

	if errc := f.Close(); err == nil {
		err = errc
	}

	if err != nil {
		return false, nil //nolint:nilerr // too short to be bbolt, so not bbolt
	}

	return binary.NativeEndian.Uint32(magic[boltMetaMagicOffset:]) == boltMetaMagic, nil
}

// convertOnOpen converts dbFile to a store if it is a bbolt file or a
// conversion of one was interrupted (spec E2 order, steps 3 and 6), reporting
// progress in its upgrade sidecar, and reports whether it converted.
func convertOnOpen(ctx context.Context, dbFile string) (bool, error) {
	upgrade := newDBUpgradeReporter(ctx, dbFile)
	err := convertBoltDB(ctx, dbFile, upgrade)
	converted := upgrade.active() && err == nil

	upgrade.finish(err)

	return converted, err
}

// convertVersion is the version in a pre name: ServerVersion, or "dev".
func convertVersion() string {
	if ServerVersion == "" {
		return convertDevVersion
	}

	return ServerVersion
}
