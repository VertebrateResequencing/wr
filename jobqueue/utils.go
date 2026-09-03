/*******************************************************************************
 * Copyright (c) 2016-2019, 2021-2022, 2024-2026 Genome Research Ltd.
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

// This file contains some general utility functions for use by client and
// server.

import (
	"bufio"
	"bytes"
	"compress/zlib"
	"context"
	crand "crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"maps"
	"math/rand"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/VertebrateResequencing/wr/internal"
	"github.com/VertebrateResequencing/wr/jobqueue/scheduler"
	"github.com/dgryski/go-farm"
	multierror "github.com/hashicorp/go-multierror"
	"github.com/jpillora/backoff"
	"github.com/shirou/gopsutil/v4/process"
)

// AppName gets used in certain places like naming the base directory of created
// working directories during Client.Execute().
var AppName = "jobqueue" //nolint:gochecknoglobals // configurable package-wide default

// mkHashedLevels is the number of directory levels we create in mkHashedDirs.
const mkHashedLevels = 4

// createdCwdBaseSuffix ends the name of the base directory mkHashedDir puts
// every working directory it creates below, and so ends the first component
// below Cwd of every ActualCwd wr has ever produced.
//
// It is a SUFFIX rather than the whole name deliberately: the name is
// AppName+this, and AppName is a package var that cmd/runner.go sets to "wr"
// while the manager leaves it "jobqueue", so recognising the whole name would
// refuse every runner-made workspace in the manager - which is where cleanup of
// a lost job runs. The suffix is agnostic about which of the two made the
// directory while still being something wr chose.
const createdCwdBaseSuffix = "_cwd"

// createdCwdDepth is how many path components below a Job's Cwd the working
// directory wr creates for it sits: the <AppName>_cwd base, then the
// mkHashedLevels-1 hashed dirs, then the MkdirTemp leaf, then cwd itself.
// TestCreatedCwdDepthMatchesMkHashedDir pins it against what mkHashedDir
// produces, because a wrong value here would quietly stop every cleanup rather
// than fail loudly.
const createdCwdDepth = mkHashedLevels + 2

// tokenLength is the fixed size of our authentication token, and
// tokenRandBytes is the number of random bytes that base64-encode to it.
const (
	tokenLength    = 43
	tokenRandBytes = 32
)

const (
	reqSchedSpecialRAM = 924
	reqSchedExtraRAM   = 100
	reqSchedTimeRound  = 30 * time.Minute
)

const (
	// bytesPerKB and bytesPerMB are used to convert reported byte/kB sizes to
	// MB.
	bytesPerKB = 1024
	bytesPerMB = 1024 * 1024

	// mkHashedDirMaxTries is how many times we retry creating a hashed dir when
	// it conflicts with a concurrent rmEmptyDirsIn.
	mkHashedDirMaxTries = 3
)

// pss is the smaps line prefix scanned by scanSmapsPss.
var pss = []byte("Pss:") //nolint:gochecknoglobals // immutable byte-slice constant

// cr, lf and ellipses get used by stdFilter().
//
//nolint:gochecknoglobals // immutable byte-slice constants
var (
	cr       = []byte("\r")
	lf       = []byte("\n")
	ellipses = []byte("[...]\n")
)

const (
	liveStdRawTailLimit    = 64 * 1024
	liveStdCompressedLimit = 4096
	binarySearchDivisor    = 2
)

var liveTailCompressor = compressedLiveTail //nolint:gochecknoglobals // Test hook for FlushCompressed contention.

// generateToken creates a cryptographically secure pseudorandom URL-safe base64
// encoded string 43 bytes long. Used by the server to create a token passed to
// the caller for subsequent client authentication. If the given file exists
// and contains a single 43 byte string, then that is used as the token instead.
func generateToken(tokenFile string) ([]byte, error) {
	if token, err := os.ReadFile(tokenFile); err == nil && len(token) == tokenLength {
		return token, nil
	}

	b := make([]byte, tokenRandBytes)

	_, err := crand.Read(b)
	if err != nil {
		return nil, err
	}

	token := make([]byte, tokenLength)
	base64.URLEncoding.WithPadding(base64.NoPadding).Encode(token, b)

	return token, err
}

// tokenMatches compares a token supplied by a client with a server token (eg.
// generated by generateToken()) and tells you if they match. Does so in a
// cryptographically secure way (avoiding timing attacks).
func tokenMatches(input, expected []byte) bool {
	result := subtle.ConstantTimeCompare(input, expected)

	return result == 1
}

// byteKey calculates a unique key that describes a byte slice.
func byteKey(b []byte) string {
	l, h := farm.Hash128(b)

	return newHexKey(l, h)
}

// newHexKey returns the 32-character lowercase zero-padded hex form of the two
// uint64 halves, written big-endian with l first then h. This is byte-for-byte
// identical to fmt.Sprintf("%016x%016x", l, h) for all uint64 values, but
// avoids the reflection-based formatting on this hot path. The output is used
// as BoltDB keys, lookup-index keys and Go map keys, so it must never diverge.
func newHexKey(l, h uint64) string {
	var hash [16]byte

	binary.BigEndian.PutUint64(hash[0:8], l)
	binary.BigEndian.PutUint64(hash[8:16], h)

	return hex.EncodeToString(hash[:])
}

// foldCloseErr combines an error returned by closing a resource (named in what,
// eg. "source") into a prior error, returning the result. It is intended for
// use in deferred closers via err = foldCloseErr(err, c.Close(), "name").
func foldCloseErr(prior, closeErr error, what string) error {
	if closeErr == nil {
		return prior
	}

	if prior == nil {
		return closeErr
	}

	return fmt.Errorf("%w (and closing %s failed: %w)", prior, what, closeErr)
}

// copy a file *** should be updated to handle source being on a different
// machine or in an S3-style object store.
func copyFile(source string, dest string) (err error) {
	in, err := os.Open(source)
	if err != nil {
		return err
	}
	defer func() {
		err = foldCloseErr(err, in.Close(), "source")
	}()

	out, err := os.Create(dest)
	if err != nil {
		return err
	}
	defer func() {
		err = foldCloseErr(err, out.Close(), "dest")
	}()

	_, err = io.Copy(out, in)

	return err
}

// compress uses zlib to compress stuff, for transferring big stuff like
// stdout, stderr and environment variables over the network, and for storing
// of same on disk.
func compress(data []byte) ([]byte, error) {
	var compressed bytes.Buffer

	w, err := zlib.NewWriterLevel(&compressed, zlib.BestCompression)
	if err != nil {
		return nil, err
	}

	_, err = w.Write(data)
	if err != nil {
		return nil, err
	}

	err = w.Close()
	if err != nil {
		return nil, err
	}

	return compressed.Bytes(), nil
}

// decompress uses zlib to decompress stuff compressed by compress().
func decompress(compressed []byte) ([]byte, error) {
	b := bytes.NewReader(compressed)

	r, err := zlib.NewReader(b)
	if err != nil {
		return nil, err
	}

	buf := new(bytes.Buffer)

	_, err = buf.ReadFrom(r)
	if err != nil {
		return nil, err
	}

	return buf.Bytes(), err
}

// get the current memory usage of a pid and all its children, relying on modern
// linux /proc/*/smaps (based on http://stackoverflow.com/a/31881979/675083).
func currentMemory(pid int) (int, error) {
	kb, err := scanSmapsPss(pid)
	if err != nil {
		return 0, err
	}

	// convert kB to MB
	mem := int(kb / bytesPerKB) //nolint:gosec // a process's memory in MB comfortably fits in an int

	// recurse for children
	childMem, err := sumChildrenMemory(pid)
	if err != nil {
		return mem, err
	}

	return mem + childMem, nil
}

// ownMemoryMB returns this process's own Pss in MB, excluding any child
// processes. Unlike currentMemory it does NOT walk the process tree (no
// gopsutil Children()/whole-/proc scan), so it is cheap to call on a busy host;
// it is used on the per-job hot path after a job command has already exited,
// where the child sum would be both useless and expensive.
func ownMemoryMB() (int, error) {
	kb, err := scanSmapsPss(os.Getpid())
	if err != nil {
		return 0, err
	}

	// convert kB to MB
	return int(kb / bytesPerKB), nil //nolint:gosec // a process's memory in MB comfortably fits in an int
}

// scanSmapsPss reads /proc/<pid>/smaps and sums the Pss (proportional set size)
// values, in kB.
func scanSmapsPss(pid int) (kb uint64, err error) {
	f, err := os.Open(filepath.Clean(fmt.Sprintf("/proc/%d/smaps", pid)))
	if err != nil {
		return 0, err
	}
	defer func() {
		err = foldCloseErr(err, f.Close(), "smaps")
	}()

	r := bufio.NewScanner(f)
	for r.Scan() {
		line := r.Bytes()
		if !bytes.HasPrefix(line, pss) {
			continue
		}

		var size uint64
		if _, err = fmt.Sscanf(string(line[len(pss):]), "%d", &size); err != nil {
			return 0, err
		}

		kb += size
	}

	return kb, r.Err()
}

// sumChildrenMemory returns the total currentMemory of all the child processes
// of pid (child memory-read failures are ignored).
func sumChildrenMemory(pid int) (int, error) {
	p, err := process.NewProcess(int32(pid)) //nolint:gosec // a pid always fits in an int32
	if err != nil {
		return 0, err
	}

	children, err := p.Children()
	if err != nil && !errors.Is(err, process.ErrorNoChildren) {
		return 0, err
	}

	var mem int

	for _, child := range children {
		childMem, errr := currentMemory(int(child.Pid))
		if errr != nil {
			continue
		}

		mem += childMem
	}

	return mem, nil
}

// get the current disk usage within a directory, in MBs. Optionally, provide a
// map of absolute paths to dirs (within path) that should not be checked.
func currentDisk(path string, ignore ...map[string]bool) (int64, error) {
	var disk int64

	skip := make(map[string]bool)
	if len(ignore) == 1 && len(ignore[0]) > 0 {
		skip = ignore[0]
	}

	dir, err := openManaged(path)
	if err != nil {
		return disk, err
	}
	defer func() {
		err = dir.Close()
	}()

	files, err := dir.Readdir(-1)
	if err != nil {
		return disk, err
	}

	for _, file := range files {
		used, errd := diskForFile(path, file, skip, ignore)
		if errd != nil {
			return disk, errd
		}

		disk += used
	}

	return disk, err
}

// diskForFile returns the disk usage in MB contributed by file (which lives in
// dirPath). Directories are recursed into via currentDisk unless they are in
// skip.
func diskForFile(dirPath string, file os.FileInfo, skip map[string]bool, ignore []map[string]bool) (int64, error) {
	if !file.IsDir() {
		return file.Size() / bytesPerMB, nil
	}

	abs := filepath.Join(dirPath, file.Name())
	if skip[abs] {
		return 0, nil
	}

	return currentDisk(abs, ignore...)
}

// getChildProcesses gets the child processes of the given pid, recursively.
func getChildProcesses(pid int32) ([]*process.Process, error) {
	var children []*process.Process

	p, err := process.NewProcess(pid)
	if err != nil {
		// we ignore errors, since we allow for working on processes that we're in
		// the process of killing
		//nolint:nilerr // deliberately ignore the error for processes being killed
		return children, nil
	}

	children, err = p.Children()
	if err != nil && !errors.Is(err, process.ErrorNoChildren) {
		return children, err
	}

	for _, child := range children {
		theseKids, errk := getChildProcesses(child.Pid)
		if errk != nil {
			continue
		}

		if len(theseKids) > 0 {
			children = append(children, theseKids...)
		}
	}

	return children, nil
}

// this prefixSuffixSaver-related code is taken from os/exec, since they are not
// exported. prefixSuffixSaver is an io.Writer which retains the first N bytes
// and the last N bytes written to it. The Bytes() methods reconstructs it with
// a pretty error message.
type prefixSuffixSaver struct {
	N         int
	prefix    []byte
	suffix    []byte
	suffixOff int
	skipped   int64
}

func (w *prefixSuffixSaver) Write(p []byte) (int, error) {
	lenp := len(p)

	p = w.fill(&w.prefix, p)
	if overage := len(p) - w.N; overage > 0 {
		p = p[overage:]
		w.skipped += int64(overage)
	}

	p = w.fill(&w.suffix, p)
	for len(p) > 0 { // 0, 1, or 2 iterations.
		n := copy(w.suffix[w.suffixOff:], p)
		p = p[n:]
		w.skipped += int64(n)

		w.suffixOff += n
		if w.suffixOff == w.N {
			w.suffixOff = 0
		}
	}

	return lenp, nil
}

func (w *prefixSuffixSaver) fill(dst *[]byte, p []byte) []byte {
	if remain := w.N - len(*dst); remain > 0 {
		add := minInt(len(p), remain)
		*dst = append(*dst, p[:add]...)
		p = p[add:]
	}

	return p
}

func (w *prefixSuffixSaver) Bytes() []byte {
	if w.suffix == nil {
		return w.prefix
	}

	if w.skipped == 0 {
		return append(w.prefix, w.suffix...)
	}

	const omittingMsgLen = 50 // approx length of the "... omitting N bytes ..." message

	var buf bytes.Buffer
	buf.Grow(len(w.prefix) + len(w.suffix) + omittingMsgLen)
	buf.Write(w.prefix)
	buf.WriteString("\n... omitting ")
	buf.WriteString(strconv.FormatInt(w.skipped, 10))
	buf.WriteString(" bytes ...\n")
	buf.Write(w.suffix[w.suffixOff:])
	buf.Write(w.suffix[:w.suffixOff])

	return buf.Bytes()
}

func minInt(a, b int) int {
	if a < b {
		return a
	}

	return b
}

type liveTailSaver struct {
	sync.Mutex
	tail  []byte
	dirty bool
}

func (w *liveTailSaver) Write(p []byte) (int, error) {
	w.Lock()
	defer w.Unlock()

	lenp := len(p)
	if lenp == 0 {
		return 0, nil
	}

	w.dirty = true
	if lenp >= liveStdRawTailLimit {
		w.tail = append(w.tail[:0], p[lenp-liveStdRawTailLimit:]...)

		return lenp, nil
	}

	if overage := len(w.tail) + lenp - liveStdRawTailLimit; overage > 0 {
		copy(w.tail, w.tail[overage:])
		w.tail = w.tail[:len(w.tail)-overage]
	}

	w.tail = append(w.tail, p...)

	return lenp, nil
}

func (w *liveTailSaver) FlushCompressed() []byte {
	w.Lock()

	if !w.dirty {
		w.Unlock()

		return nil
	}

	tail := append([]byte(nil), w.tail...)
	w.tail = w.tail[:0]
	w.dirty = false
	w.Unlock()

	return liveTailCompressor(tail)
}

func compressedLiveTail(tail []byte) []byte {
	compressed, err := compress(tail)
	if err != nil {
		return nil
	}

	if len(compressed) <= liveStdCompressedLimit {
		return compressed
	}

	return compressedLiveTailSuffix(tail)
}

// stdFilter keeps only the first and last line of any contiguous block of \r
// terminated lines (to mostly eliminate progress bars), intended for use with
// stdout/err streaming input, outputting to a prefixSuffixSaver. Because you
// must finish reading from the input before continuing, it returns a channel
// that you should wait to receive an error from (nil if everything workd).
func stdFilter(std io.Reader, out io.Writer) chan error {
	reader := bufio.NewReader(std)
	done := make(chan error)

	go func() {
		var merr *multierror.Error

		for {
			p, err := reader.ReadBytes('\n')

			writeFilteredBlock(out, bytes.Split(p, cr), &merr)

			if err != nil {
				break
			}
		}

		done <- merr.ErrorOrNil()
	}()

	return done
}

// stdFilter constants for interpreting a \r-split block of input: blocks of
// more than 1 \r-terminated line have their first and last lines kept, and
// blocks of more than 2 also get an ellipses to show lines were dropped.
const (
	stdFilterKeepLastMin = 2
	stdFilterEllipsesMin = 3
)

// writeFilteredBlock writes the kept lines of a single \r-split block to out,
// appending any write errors to merr. It keeps only the first and last line of
// the block (see stdFilter).
func writeFilteredBlock(out io.Writer, lines [][]byte, merr **multierror.Error) {
	writeStd(out, lines[0], merr)

	if len(lines) <= stdFilterKeepLastMin {
		return
	}

	writeStd(out, lf, merr)

	if len(lines) > stdFilterEllipsesMin {
		writeStd(out, ellipses, merr)
	}

	writeStd(out, lines[len(lines)-2], merr)
	writeStd(out, lf, merr)
}

// writeStd writes b to out, appending any error to merr.
func writeStd(out io.Writer, b []byte, merr **multierror.Error) {
	if _, err := out.Write(b); err != nil {
		*merr = multierror.Append(*merr, err)
	}
}

// envOverride deals with values you get from os.Environ, overriding one set
// with values from another. Returns the new slice of environment variables.
func envOverride(orig []string, over []string) []string {
	override := make(map[string]string)

	for _, envvar := range over {
		pair := strings.Split(envvar, "=")
		override[pair[0]] = envvar
	}

	env := orig
	for i, envvar := range env {
		pair := strings.Split(envvar, "=")
		if replace, do := override[pair[0]]; do {
			env[i] = replace

			delete(override, pair[0])
		}
	}

	for _, envvar := range override {
		env = append(env, envvar)
	}

	return env
}

func compressedLiveTailSuffix(tail []byte) []byte {
	low := 1
	high := len(tail)

	var best []byte

	for low <= high {
		mid := low + (high-low)/binarySearchDivisor

		compressed, err := compress(tail[len(tail)-mid:])
		if err != nil {
			return nil
		}

		if len(compressed) <= liveStdCompressedLimit {
			best = compressed
			low = mid + 1

			continue
		}

		high = mid - 1
	}

	return best
}

// calculateHashedDir returns the hashed directory structure corresponding to
// a given string. Returns dirs rooted at baseDir, and a leaf name.
func calculateHashedDir(baseDir, tohash string) (string, string) {
	dirs := strings.SplitN(tohash, "", mkHashedLevels)
	dirs, leaf := dirs[0:mkHashedLevels-1], dirs[mkHashedLevels-1]
	dirs = append([]string{baseDir}, dirs...)

	return filepath.Join(dirs...), leaf
}

// mkHashedDir uses tohash (which should be a 32 char long string from
// byteKey()) to create a folder nested within baseDir, and in that folder
// creates 2 folders called cwd and tmp, which it returns. Returns an error if
// there were problems making the directories.
func mkHashedDir(baseDir, tohash string) (cwd, tmpDir string, err error) {
	dir, leaf := calculateHashedDir(filepath.Join(baseDir, AppName+createdCwdBaseSuffix), tohash)

	holdFile := filepath.Join(dir, ".hold")
	defer func() {
		err = removeHoldFile(holdFile, err)
	}()

	if err = mkHeldDir(dir, holdFile); err != nil {
		return cwd, tmpDir, err
	}

	// if tohash is a job key then we expect that only 1 of that job is
	// running at any one time per jobqueue, but there could be multiple users
	// running the same cmd, or this user could be running the same command in
	// multiple queues, so we must still create a unique dir at the leaf of our
	// hashed dir structure, to avoid any conflict of multiple processes using
	// the same working directory
	dir, err = os.MkdirTemp(dir, leaf)
	if err != nil {
		return cwd, tmpDir, err
	}

	return mkCwdAndTmp(dir)
}

// holdFilePerm is the permission used for the hold file dropped by mkHeldDir.
const holdFilePerm = 0o600

// removeHoldFile removes the hold file created by mkHeldDir, folding any removal
// error into the given prior error.
func removeHoldFile(holdFile string, prior error) error {
	errr := removeManaged(holdFile)
	if errr == nil || os.IsNotExist(errr) {
		return prior
	}

	if prior == nil {
		return errr
	}

	return fmt.Errorf("%w (and removing the hold file failed: %w)", prior, errr)
}

// mkHeldDir creates dir (retrying a few times in case a concurrent rmEmptyDirsIn
// conflicts with us) and drops a hold file in it so rmEmptyDirsIn will not
// immediately remove it.
func mkHeldDir(dir, holdFile string) error {
	tries := 0

	for {
		retry, err := tryMkHeldDir(dir, holdFile, &tries)
		if err != nil {
			return err
		}

		if !retry {
			return nil
		}
	}
}

// The *Managed helpers wrap os file operations on paths that the jobqueue
// itself created and manages (a Job's working/cache dirs, our own /proc
// entries). The paths are trusted by design and cleaned before use, which also
// satisfies gosec's path-traversal analysis without per-call suppressions.

// removeManaged is os.Remove for a jobqueue-managed path.
func removeManaged(path string) error {
	return os.Remove(filepath.Clean(path))
}

// openManaged is os.Open for a jobqueue-managed path.
func openManaged(path string) (*os.File, error) {
	return os.Open(filepath.Clean(path))
}

// mkdirManaged is os.Mkdir for a jobqueue-managed path.
func mkdirManaged(path string, perm os.FileMode) error {
	return os.Mkdir(filepath.Clean(path), perm)
}

// mkdirAllManaged is os.MkdirAll for a jobqueue-managed path.
func mkdirAllManaged(path string, perm os.FileMode) error {
	return os.MkdirAll(filepath.Clean(path), perm)
}

// openFileManaged is os.OpenFile for a jobqueue-managed path.
func openFileManaged(path string, flag int, perm os.FileMode) (*os.File, error) {
	return os.OpenFile(filepath.Clean(path), flag, perm)
}

// tryMkHeldDir makes one attempt to create dir and its hold file. It returns
// retry=true if mkHeldDir should loop again, or an error if we've run out of
// retries. retry=false with a nil error means success.
func tryMkHeldDir(dir, holdFile string, tries *int) (retry bool, err error) {
	if err = mkdirAllManaged(dir, os.ModePerm); err != nil {
		return retryOrFail(tries, err)
	}

	*tries = 0

	f, err := openFileManaged(holdFile, os.O_RDONLY|os.O_CREATE, holdFilePerm)
	if err != nil {
		return retryOrFail(tries, err)
	}

	return false, f.Close()
}

// retryOrFail increments *tries and reports whether the caller should retry
// (still within mkHashedDirMaxTries). When not retrying it returns err so the
// caller can return it.
func retryOrFail(tries *int, err error) (bool, error) {
	*tries++
	if *tries <= mkHashedDirMaxTries {
		return true, nil
	}

	return false, err
}

// createdCwdName is what mkCwdAndTmp calls the working directory it makes, and
// so the last component of every ActualCwd wr has ever created.
const createdCwdName = "cwd"

// createdTmpName is what mkCwdAndTmp calls the dir it makes beside a Job's
// working directory to be that Job's TMPDIR. It is a const because removing that
// dir names it to an open handle on the workspace, rather than re-resolving the
// path string the Job was given; see jobWorkSpaceSnapshot.removeTmpDir.
const createdTmpName = "tmp"

// mkCwdAndTmp creates "cwd" and "tmp" dirs within dir, returning their paths.
func mkCwdAndTmp(dir string) (cwd, tmpDir string, err error) {
	cwd = filepath.Join(dir, createdCwdName)
	if err = mkdirManaged(cwd, os.ModePerm); err != nil {
		return cwd, tmpDir, err
	}

	tmpDir = filepath.Join(dir, createdTmpName)

	return cwd, tmpDir, mkdirManaged(tmpDir, os.ModePerm)
}

// errNotBelowBaseDir is returned when we're asked to delete a directory that is
// not inside the base directory that bounds the deletion.
var errNotBelowBaseDir = errors.New("dir is not below the base dir")

// openBaseRoot opens baseDir as an os.Root: a handle on the directory itself,
// through which every deletion below it is done with a path relative to the
// handle. That is what closes the gap between proving a path may be deleted and
// deleting it, since a proof is about a path string that every syscall
// re-resolves, while a relative operation on a root cannot leave that root.
func openBaseRoot(baseDir string) (*os.Root, error) {
	if baseDir == "" {
		return nil, fmt.Errorf("%w: no base dir was given", errNotBelowBaseDir)
	}

	absBase, err := filepath.Abs(baseDir)
	if err != nil {
		return nil, err
	}

	return os.OpenRoot(absBase)
}

// rmEmptyDirsIn deletes leafDir and its parent directories if they are empty,
// stopping before it reaches the dir baseRoot holds open (leaving that, and
// everything above it, undeleted). It's ok if leafDir doesn't exist.
//
// leafDir must be a proper descendant of baseRoot's dir, with no symlink among
// the components leading to it; otherwise nothing at all is deleted and
// errNotBelowBaseDir is returned. There is no safe upward walk to do from
// anywhere else: with leafDir being baseRoot's own dir the first parent
// considered would already be above it, and the walk would delete the empty
// ancestors of the tree it was supposed to stay inside.
//
// It takes the base dir as an open HANDLE rather than as a path so that the
// caller's proof of its way inside that dir travels with it. Do not add a
// path-taking twin, which would let a caller walk somewhere it had proven
// nothing about.
func rmEmptyDirsIn(baseRoot *os.Root, leafDir string) error {
	proven, ok := realDirBelow(baseRoot, leafDir)
	if !ok {
		return fmt.Errorf("%w: %s vs %s", errNotBelowBaseDir, leafDir, baseRoot.Name())
	}

	chain, err := proven.openChain()
	if err != nil {
		return err
	}
	defer chain.closeAll()

	return chain.removeUpward()
}

// provenDirs is a directory proven fit for deletion, paired with the open root
// that bounds every deletion made with it: rel is strictly inside root, and no
// component of it is a symlink, so deleting rel and walking up from it can
// neither leave root nor delete something a link merely points at. realDirBelow
// is the only way to make one.
//
// The type exists so that the deletion helpers cannot be handed two arbitrary
// path strings: the proof travels with the paths, in the type.
type provenDirs struct {
	// root is the open handle every deletion goes through. It is owned by
	// whoever opened it, not by this value: the chain openChain returns shares
	// the same handle, and neither closes it.
	root *os.Root

	// rel is the proven dir, relative to root: cleaned, and never "." or above.
	rel string

	// leaf is the absolute form of the proven dir, for error messages and for
	// deriving other paths. Deletions use root and rel, which cannot be
	// re-resolved somewhere else between the proof and the deletion.
	leaf string

	// infos is what the proof lstat'ed at each component of rel, in order, so
	// infos[i] belongs to the i'th component. It is short when the proof ran out
	// of path that exists, and empty when even the first component was gone.
	//
	// There is one per component, not just one for the leaf, because the descent
	// re-resolves the path a component at a time and has to prove every level it
	// opens; the upward walk deletes through those levels.
	infos []os.FileInfo
}

// openChain descends from dirs.root to the proven dir, keeping a handle on every
// directory on the way down; see dirChain.
//
// A component that has gone since the proof ends the descent, which is not a
// failure in itself; the chain then knows it is incomplete. Any other failure to
// open a component is returned, having closed whatever had been opened.
//
// The caller must closeAll the returned chain.
func (dirs provenDirs) openChain() (dirChain, error) {
	chain := dirChain{
		names: strings.Split(dirs.rel, string(filepath.Separator)),
		leaf:  dirs.leaf,
	}
	chain.roots = append(make([]*os.Root, 0, len(chain.names)), dirs.root)

	// what the proof found at the leaf, left nil if it had already gone by then.
	if len(dirs.infos) >= len(chain.names) {
		chain.info = dirs.infos[len(chain.names)-1]
	}

	for i, name := range chain.names[:len(chain.names)-1] {
		if i >= len(dirs.infos) {
			// the proof stopped here because nothing below existed, so there is
			// nothing deeper to open and nothing deeper to delete.
			return chain, nil
		}

		dirRoot, err := openVerifiedDir(chain.deepest(), name, dirs.infos[i])
		if err != nil {
			if os.IsNotExist(err) {
				return chain, nil
			}

			chain.closeAll()

			return dirChain{}, err
		}

		chain.roots = append(chain.roots, dirRoot)
	}

	return chain, nil
}

// dirChain is an open handle on the directory that each component of a proven
// path lives in: roots[i] is the handle names[i] is an entry of, so roots[0] is
// the base the descent started from, and each handle after it was opened on the
// name before it.
//
// It exists for the deletion walk. Removing the leaf and then each of its
// parents by a shrinking path relative to the base would re-walk the whole path
// every time, which is O(depth^2) metadata lookups on the shared filesystems
// jobs run on; keeping the handles from the one descent makes it one lookup per
// level. The handles also pin their directories, so every removal happens in the
// directory the descent opened, whatever is done to the names above it.
type dirChain struct {
	// roots holds the base handle followed by the handles the descent opened.
	// It is shorter than names when a component had already gone, and never
	// covers the leaf itself: that is opened by openLeaf, which proves its
	// identity as well.
	roots []*os.Root

	// names is the proven dir's path relative to the base, split into its
	// components, so the last of them is the leaf's own name.
	names []string

	// leaf is the absolute path of the proven dir, for error messages, and info
	// is what the proof lstat'ed there, or nil if nothing was there then.
	leaf string
	info os.FileInfo
}

// deepest is the handle on the deepest directory the descent opened.
func (c dirChain) deepest() *os.Root {
	return c.roots[len(c.roots)-1]
}

// complete says if the descent reached the leaf's own parent, ie. every
// directory above the leaf was still there.
func (c dirChain) complete() bool {
	return len(c.roots) == len(c.names)
}

// closeAll closes the handles the descent opened. roots[0] belongs to whoever
// opened it, so it is left alone.
func (c dirChain) closeAll() {
	for i := len(c.roots) - 1; i > 0; i-- {
		c.roots[i].Close()
	}
}

// openLeaf opens the proven dir as a root of its own, so that everything deleted
// inside it is named relative to that handle rather than by a string resolved
// afresh each time.
//
// It also proves the handle refers to the directory the proof lstat'ed, which an
// os.Root alone does not give: a root refuses absolute symlinks and any escape
// from itself, but follows a relative symlink that stays inside it.
//
// A dir that has gone since it was proven gives an os.IsNotExist error.
func (c dirChain) openLeaf() (*os.Root, error) {
	if c.info == nil || !c.complete() {
		return nil, &os.PathError{Op: "open", Path: c.leaf, Err: os.ErrNotExist}
	}

	last := len(c.names) - 1

	return openVerifiedDir(c.roots[last], c.names[last], c.info)
}

// removeUpward removes the proven dir and then each of its parents in turn,
// stopping before it reaches the base (leaving that, and everything above it,
// undeleted) and stopping early at a dir it cannot remove - which is expected
// when another Job is running from the same base, so that is not an error.
//
// Each removal names a single entry of the pinned handle on the directory that
// entry lives in, so no part of the path is left to be resolved again, and like
// os.Remove it only succeeds on an empty directory and unlinks a symlink rather
// than following it.
//
// A leaf that has already gone is not a failure: its parents are still ours to
// tidy, which is the ordinary state on the second cleanup of a Job.
func (c dirChain) removeUpward() error {
	if !c.complete() {
		return nil
	}

	last := len(c.names) - 1

	err := c.roots[last].Remove(c.names[last])
	if err != nil && !os.IsNotExist(err) {
		if errIsDirNotEmpty(err) {
			return nil
		}

		return err
	}

	c.removeEmptyParents()

	return nil
}

// removeEmptyParents removes the empty parent directories of the chain's leaf,
// from the deepest up, stopping before the base and at the first dir that will
// not go.
//
// The chain is what makes it safe to remove these without re-proving each level:
// every one is an ancestor of a leaf already proven to be inside the base, and
// each removal is made in the directory the descent opened, which cannot be
// outside the base however the names resolve now.
func (c dirChain) removeEmptyParents() {
	parents := c.names[:len(c.names)-1]

	for i := len(parents) - 1; i >= 0; i-- {
		if c.roots[i].Remove(parents[i]) != nil {
			return
		}
	}
}

// proveSameDir reports whether now and checked are the same inode, ie. whether
// the directory just opened is the one an earlier lstat identified. It is
// deliberately the ONE place that comparison is made, and both consumers of a
// Job's working directory make it: `run` through the opens below, cleanup
// through jobWorkSpace.actualCwdNow.
//
// checked must have come from an lstat of a DIRECTORY, since nothing here
// re-checks that. A nil or non-directory checked makes os.SameFile false, so it
// refuses everything rather than accepting anything.
func proveSameDir(now, checked os.FileInfo, name string) error {
	if !os.SameFile(now, checked) {
		return fmt.Errorf("%w: %s is no longer the dir that was checked", errNotBelowBaseDir, name)
	}

	return nil
}

// openVerifiedDirFile opens name, a path relative to parent, as an open FILE on
// the directory, and confirms it is the same inode info describes - the same
// check openVerifiedDir makes, for a caller that needs a descriptor it can name
// to something else rather than a root to work within. info must satisfy
// proveSameDir's requirements of it.
//
// The caller must Close the returned file.
func openVerifiedDirFile(parent *os.Root, name string, info os.FileInfo) (*os.File, error) {
	f, err := parent.Open(name)
	if err != nil {
		return nil, err
	}

	opened, err := f.Stat()
	if err == nil {
		err = proveSameDir(opened, info, f.Name())
	}

	if err != nil {
		f.Close()

		return nil, err
	}

	return f, nil
}

// openVerifiedDir opens name, a path relative to parent, as a root of its own,
// and confirms it is the same inode info describes. info must satisfy
// proveSameDir's requirements of it, or nothing is checked.
func openVerifiedDir(parent *os.Root, name string, info os.FileInfo) (*os.Root, error) {
	dirRoot, err := parent.OpenRoot(name)
	if err != nil {
		return nil, err
	}

	opened, err := dirRoot.Stat(".")
	if err == nil {
		err = proveSameDir(opened, info, dirRoot.Name())
	}

	if err != nil {
		dirRoot.Close()

		return nil, err
	}

	return dirRoot, nil
}

// errIsDirNotEmpty says if err is a directory removal that failed because the
// directory still has entries in it, which is not a problem: it just means there
// is nothing of ours left to delete there.
//
// Linux reports ENOTEMPTY for this; POSIX allows EEXIST instead, so both count.
// The errnos are compared rather than the error message, which is not part of
// any contract.
func errIsDirNotEmpty(err error) bool {
	return errors.Is(err, syscall.ENOTEMPTY) || errors.Is(err, syscall.EEXIST)
}

// realDirBelow proves that dir is a proper descendant of baseRoot's dir, ie.
// strictly inside it, and that dir is the directory that would be deleted,
// rather than something a symlink points at. It returns dir as a path relative
// to baseRoot for the deletion to use. ok is false if that could not be proven,
// in which case nothing may be deleted.
//
// The check is lexical first, which costs no syscalls: dir is made absolute and
// cleaned, and an escape via ".." or a dir equal to the base fails. Then every
// component of dir below the base is lstat'ed to confirm it is a real directory,
// which proves dir can only be reached by descending inside the base, whatever
// the base itself resolves to.
//
// A symlinked component fails even when it stays inside the base, because the
// deletion helpers disagree about symlinks: os.RemoveAll unlinks a final one,
// while os.ReadDir follows it and deletes the target's contents instead. There is
// deliberately no resolve-the-symlink fallback, because containment says yes to a
// link leading somewhere else inside the base: only refusing to follow it stops a
// Job's Cmd aiming cleanup at a directory of the user's.
func realDirBelow(baseRoot *os.Root, dir string) (provenDirs, bool) {
	if dir == "" {
		return provenDirs{}, false
	}

	absBase := baseRoot.Name()

	absDir, err := filepath.Abs(dir)
	if err != nil {
		return provenDirs{}, false
	}

	rel, err := filepath.Rel(absBase, absDir)
	if err != nil || !relIsBelow(rel) {
		return provenDirs{}, false
	}

	infos, ok := componentsAreRealDirs(absDir, absBase)
	if !ok {
		return provenDirs{}, false
	}

	return provenDirs{root: baseRoot, rel: rel, leaf: absDir, infos: infos}, true
}

// componentsAreRealDirs tells you if every path component of absDir below absBase
// (which must be the base dir absDir is inside) is a real directory, rather than
// a symlink or a file. A component that doesn't exist counts as fine, since
// nothing can be deleted through it, and means there is nothing deeper to check.
//
// It returns what it lstat'ed at each component, so that a caller opening them
// can prove the directories it gets are these ones.
func componentsAreRealDirs(absDir, absBase string) ([]os.FileInfo, bool) {
	var infos []os.FileInfo

	for i := len(absBase) + 1; i <= len(absDir); i++ {
		if i < len(absDir) && absDir[i] != filepath.Separator {
			continue
		}

		info, err := os.Lstat(absDir[:i])
		if err != nil {
			// the infos gathered so far are returned, not discarded: the
			// descent still opens those components to walk up from, so it still
			// needs to prove each of them is the directory checked here.
			return infos, os.IsNotExist(err)
		}

		if !info.IsDir() {
			return nil, false
		}

		infos = append(infos, info)
	}

	return infos, true
}

// relIsJobCreatedCwd tells you if rel, a path relative to a Job's Cwd, is the
// working directory mkHashedDir built for a Job with the given key. It is the
// ONLY thing that licenses deleting below a Job's Cwd, or executing a `run`
// behaviour's command somewhere other than that Cwd.
//
// It is built by asking calculateHashedDir - the function that laid the path
// down - what it would produce, so recogniser and builder cannot drift apart.
// The whole shape it accepts is <something>_cwd/k0/k1/k2/<k3..><digits>/cwd,
// where k0-k2 are the first three characters of the key and k3.. is the rest.
//
// The base component is checked by SUFFIX and never by the name this process
// would build, for the reason createdCwdBaseSuffix gives: AppName differs between
// runner and manager, and cleanup runs in both.
//
// Everything else about the path is the key's, and that is what stops one Job of
// a Cwd destroying ANOTHER's: every Job of a Cwd works below the same *_cwd base
// at the same depth with the same leaf name, so nothing short of the key
// distinguishes a sibling's live working directory from this Job's own.
//
// Only three characters of the key are cheap to grind out, but what they buy is
// bounded by the rest: the ground path still has to exist, below a *_cwd base
// inside the Job's own Cwd, with a leaf called cwd under the unique dir
// os.MkdirTemp named for the REST of that same key.
func relIsJobCreatedCwd(rel, key string) bool {
	names := strings.Split(rel, string(filepath.Separator))

	// the depth is both a check and a precondition. A path ONE LEVEL TOO DEEP
	// whose leaf is still called cwd - which a Job's own Cmd can make inside the
	// directory wr gave it - satisfies every other condition here, and treating
	// it as a working directory would sweep the directory wr gave the Job as a
	// workspace. Every index below is also fixed, so a rel of any other length
	// would read the wrong components or run off the end of the slice.
	if len(names) != createdCwdDepth {
		return false
	}

	if !strings.HasSuffix(names[0], createdCwdBaseSuffix) || names[len(names)-1] != createdCwdName {
		return false
	}

	hashed, leaf := calculateHashedDir(names[0], key)
	if hashed != filepath.Join(names[:createdCwdDepth-2]...) {
		return false
	}

	return isMkTempName(names[createdCwdDepth-2], leaf)
}

// isMkTempName tells you if name is what os.MkdirTemp creates for the given
// prefix: the prefix followed by a non-empty run of digits.
func isMkTempName(name, prefix string) bool {
	suffix, ok := strings.CutPrefix(name, prefix)
	if !ok || suffix == "" {
		return false
	}

	return strings.IndexFunc(suffix, func(r rune) bool { return r < '0' || r > '9' }) < 0
}

// relIsBelow tells you if a relative path produced by filepath.Rel describes
// somewhere strictly inside the dir it is relative to.
func relIsBelow(rel string) bool {
	return rel != "." && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// readDirIn reads the entries of dir, a path relative to dirRoot, through the
// root handle, so the read cannot be redirected outside it.
func readDirIn(dirRoot *os.Root, dir string) ([]os.DirEntry, error) {
	f, err := dirRoot.Open(dir)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	return f.ReadDir(-1)
}

// removeAllExcept deletes the contents of dirRoot's own directory, except for
// the given folders (paths relative to it), and except for whatever
// removeEntryWithExceptions refuses to touch at all.
//
// An exception that doesn't land strictly inside the directory is skipped rather
// than treated as an error. A MountConfig.Mount is whatever the user typed for
// `wr add --mounts`, so it can be ".", ".." or "../evil". Skipping is safe
// because only descendants of the directory are ever deleted here, so an
// exception outside it was protecting nothing, whereas erroring would abandon the
// job's workspace for no gain.
func removeAllExcept(dirRoot *os.Root, exceptions []string) error {
	info, err := dirRoot.Lstat(".")
	if err != nil {
		return err
	}

	return removeWithExceptions(dirRoot, ".", info, exceptionDirs(exceptions))
}

// removeAllGuarded deletes the entry of dirRoot called name, and everything
// below it. It stands in for os.Root.RemoveAll everywhere a Job's cleanup
// deletes inside the workspace wr made for it, and differs from it only in what
// it refuses to touch; see removeEntryWithExceptions.
//
// Whether name is the Job's to delete at all is decided long before this, by
// jobWorkSpace; all this bounds is how far a deletion already licensed goes.
func removeAllGuarded(dirRoot *os.Root, name string) error {
	info, err := dirRoot.Lstat(".")
	if err != nil {
		return err
	}

	return removeEntryWithExceptions(dirRoot, name, info, nil)
}

// exceptionDirs turns removeAllExcept's exceptions into the set of dirs to keep,
// as paths relative to the dir being emptied.
//
// A path that does not land strictly inside that dir is left out rather than
// stopping anything: only descendants of the dir are ever deleted, so such an
// exception was protecting nothing. There is no second set naming the dirs to
// recurse INTO to reach these, because the sweep recurses into every dir it does
// not leave alone.
func exceptionDirs(exceptions []string) map[string]bool {
	keepDirs := make(map[string]bool, len(exceptions))

	for _, dir := range exceptions {
		rel := filepath.Join(".", dir)
		if !relIsBelow(rel) {
			continue
		}

		keepDirs[rel] = true
	}

	return keepDirs
}

// removeWithExceptions deletes the contents of dir, a path relative to dirRoot,
// keeping the dirs named in keepDirs. dirInfo must be the lstat of dir itself,
// since that is what each entry's mount boundary is judged against, and the
// entry's side of that comparison is an Lstat as well: a device number read
// through a symlink would describe a filesystem that is not the one being swept.
func removeWithExceptions(dirRoot *os.Root, dir string, dirInfo os.FileInfo, keepDirs map[string]bool) error {
	entries, err := readDirIn(dirRoot, dir)
	if err != nil {
		return err
	}

	for _, entry := range entries {
		err = removeEntryWithExceptions(dirRoot, filepath.Join(dir, entry.Name()), dirInfo, keepDirs)
		if err != nil {
			return err
		}
	}

	return nil
}

// removeEntryWithExceptions deletes the entry of dirRoot at rel, and everything
// below it, unless it is one of the three things a sweep inside a Job's
// workspace must leave alone: a dir the Job's own keep set claims, the base
// another Job's workspaces sit below, or something across a mount boundary.
//
// dirInfo is the lstat of the directory rel is an entry of.
//
// A dir is descended into and then removed, rather than handed to
// os.Root.RemoveAll, because RemoveAll asks none of those questions of what it
// finds on the way down, and the deeper entries are exactly the ones that are
// another Job's live output or the user's remote objects behind a live mount.
func removeEntryWithExceptions(dirRoot *os.Root, rel string, dirInfo os.FileInfo, keepDirs map[string]bool) error {
	if keepDirs[rel] || nestedWorkSpaceBase(rel) {
		return nil
	}

	info, err := dirRoot.Lstat(rel)
	if err != nil {
		return ignoreGone(err)
	}

	if crossesMountBoundary(dirInfo, info) {
		return nil
	}

	if info.IsDir() {
		return removeSweptDir(dirRoot, rel, info, keepDirs)
	}

	return ignoreGone(dirRoot.Remove(rel))
}

// removeSweptDir empties dir - an entry of dirRoot the sweep has decided it may
// delete - and then removes the emptied dir itself.
//
// A dir that will not go because something inside it survived is not a failure:
// keeping another Job's workspace, or a live mount, necessarily keeps every
// directory above it too. That is the leak nestedWorkSpaceBase describes, and it
// is reported as success because there is nothing wrong and nothing to retry.
func removeSweptDir(dirRoot *os.Root, dir string, dirInfo os.FileInfo, keepDirs map[string]bool) error {
	if err := removeWithExceptions(dirRoot, dir, dirInfo, keepDirs); err != nil {
		return err
	}

	err := dirRoot.Remove(dir)
	if err == nil || os.IsNotExist(err) || errIsDirNotEmpty(err) {
		return nil
	}

	return err
}

// nestedWorkSpaceBase says whether rel - an entry of a directory a Job's cleanup
// is sweeping - is a base directory wr created working directories below, and so
// holds the workspace of some OTHER Job.
//
// Nothing wr made for the Job being swept can be inside such an entry: that
// Job's own base component is ABOVE its workspace, and the sweep never goes
// above it. So the entry is another Job's, and wr's own defaults are what put it
// there: a Job that adds jobs (the documented `wr add --bsubs` pattern, and
// `wr bsub`) hands its children a Cwd of os.Getwd(), which IS the working
// directory wr gave it, so their workspaces get built inside the tree its
// default `--on_exit [{"cleanup":true}]` sweeps - while they are still running
// in them.
//
// The name is matched by SUFFIX rather than against AppName+createdCwdBaseSuffix
// for the reason createdCwdBaseSuffix gives: AppName is "jobqueue" in the
// manager and "wr" in the runner, and cleanup runs in both, so an equality check
// would silently protect nothing in one of them. The suffix also spares a
// directory of the user's own whose name merely ends in _cwd, which is the safe
// direction to be wrong in.
//
// What this costs is a LEAK: a parent whose children left workspaces behind can
// no longer reclaim its own working directory or the workspace holding it,
// because the child tree is inside them and keeps them non-empty. That is
// deliberate, and not to be "fixed" by deleting the tree anyway: a workspace
// left behind is recoverable, another Job's live output is not.
func nestedWorkSpaceBase(rel string) bool {
	return strings.HasSuffix(filepath.Base(rel), createdCwdBaseSuffix)
}

// crossesMountBoundary reports whether entry is on a different device from the
// directory it is an entry of, ie. whether deleting it, or descending into it,
// would cross a mount boundary.
//
// It is RESOLVE_NO_XDEV done by hand. An os.Root gives RESOLVE_BENEATH, which
// does not imply it, so a deletion bounded by a root still recurses through a
// live mount inside that root and unlinks the objects behind it. The Job's keep
// set cannot cover those, because it is built from the Job's own MountConfigs
// while the mount may have been raised by the Job's own Cmd (sshfs, s3fs) or by a
// nested wr, appearing in no config wr has ever seen.
//
// A host that gives us no *syscall.Stat_t can tell us no devices, and the answer
// is then "no boundary": that leaves the sweep exactly as it was, whereas
// refusing on an unknown would strand every workspace on such a host.
func crossesMountBoundary(dir, entry os.FileInfo) bool {
	dirStat, dirOK := dir.Sys().(*syscall.Stat_t)
	entryStat, entryOK := entry.Sys().(*syscall.Stat_t)

	if !dirOK || !entryOK {
		return false
	}

	return dirStat.Dev != entryStat.Dev
}

// ignoreGone drops a not-exist error, which a deletion has no reason to report:
// the sweep reads a directory, then lstats an entry, then deletes it, and
// something that has gone in between needs no deleting.
func ignoreGone(err error) error {
	if os.IsNotExist(err) {
		return nil
	}

	return err
}

// compressFile reads the content of the given file then compresses that. Since
// this happens in memory, only suitable for small files!
func compressFile(path string) ([]byte, error) {
	path = internal.TildaToHome(path)

	content, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	return compress(content)
}

// reqForScheduler takes a job's Requirements and returns a possibly modified
// version if using less than 924MB memory to have +100MB memory to allow some
// leeway in case the job scheduler calculates used memory differently, and for
// other memory usage vagaries. It also rounds up the Time to the nearest half
// hour.
func reqForScheduler(req *scheduler.Requirements) *scheduler.Requirements {
	ram := req.RAM
	if ram < reqSchedSpecialRAM {
		ram += reqSchedExtraRAM
	}

	d := req.Time.Round(reqSchedTimeRound)
	if d < req.Time {
		d += reqSchedTimeRound
	}

	out := &scheduler.Requirements{
		RAM:   ram,
		Time:  d,
		Cores: req.Cores,
		Disk:  req.Disk,
	}

	if len(req.Other) > 0 {
		out.Other = make(map[string]string, len(req.Other))
		maps.Copy(out.Other, req.Other)
	}

	return out
}

// calculateItemDelay returns a delay based on a backoff and the number of
// previous delays. delayMin is the minimum delay (the server's resolved
// ReleaseDelayMin).
func calculateItemDelay(numPreviousDelays int, delayMin time.Duration) time.Duration {
	b := &backoff.Backoff{
		Min:    delayMin,
		Max:    ClientReleaseDelayMax,
		Factor: ClientReleaseDelayStepFactor,
		Jitter: false, // don't like the behaviour of it's jitter
	}

	d := b.ForAttempt(float64(numPreviousDelays))
	d -= time.Duration(rand.Float64()*float64(delayMin) - float64(delayMin)) // #nosec

	if d < 0 {
		d = ClientReleaseDelayMax
	}

	return d
}

// fqdn returns the fully qualified domain name of the current host, or
// "localhost" or just the hostname on error.
func fqdn(ctx context.Context) string {
	hostname, err := os.Hostname()
	if err != nil {
		return localhost
	}

	fqdn, err := net.DefaultResolver.LookupCNAME(ctx, hostname)
	if err != nil {
		fqdn = hostname
	}

	return strings.TrimSuffix(fqdn, ".")
}
