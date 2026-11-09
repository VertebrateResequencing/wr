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

// Tests of exporting a stopped store as a self-contained file for cloud deploy
// (spec E7).

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime/debug"
	"slices"
	"testing"
	"time"

	"github.com/VertebrateResequencing/wr/internal"
	. "github.com/smartystreets/goconvey/convey"
)

const (
	// exportLiveJobs and exportCompleteJobs are how many live and complete
	// jobs E7's source store holds.
	exportLiveJobs     = 10
	exportCompleteJobs = 10

	// exportLockedTimeout is how long ExportStore waits for a lock a manager
	// holds in E7 test 2: the manager never releases it, so any wait fails.
	exportLockedTimeout = 100 * time.Millisecond

	// exportWriterRoom is how many bytes a failingExportWriter takes before
	// it fails.
	exportWriterRoom = 512
)

var (
	// errExportTestWrite is the error a failingExportWriter fails with.
	errExportTestWrite = errors.New("export writer full")

	// errReadStoreTestFn is the error a ReadStore test's fn fails with.
	errReadStoreTestFn = errors.New("fn failed")
)

// readStoreReservedCmd is the command of the job reservedCrashImage reserves.
const readStoreReservedCmd = "echo read store reserved"

// readStoreNothing is a ReadStore fn that does nothing.
func readStoreNothing(bool, *Job) error { return nil }

// failingExportWriter takes room bytes, then fails with errExportTestWrite,
// as a file on a full disk does.
type failingExportWriter struct {
	room, written int
}

// Write implements io.Writer.
func (w *failingExportWriter) Write(p []byte) (int, error) {
	n := min(len(p), w.room-w.written)
	w.written += n

	if n < len(p) {
		return n, errExportTestWrite
	}

	return n, nil
}

func TestExportStore(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given a stopped store with 10 live and 10 complete jobs", t, func() {
		dbDir, want := exportSourceStore(ctx, t)
		So(want.live, ShouldHaveLength, exportLiveJobs)
		So(want.complete, ShouldHaveLength, exportCompleteJobs)

		Convey("ExportStore writes a file that a manager started with it as its db file restores with the "+
			"same live and complete jobs", func() {
			path := filepath.Join(t.TempDir(), "exported.db")
			exportStoreTo(dbDir, path)

			server, msg := serveExported(ctx, path, internal.Development)

			So(msg, ShouldContainSubstring, "restored db directory "+path+" from self-contained backup file")
			So(snapshotOf(server.db), ShouldResemble, want)
		})

		Convey("ExportStore's file placed at a fresh remote deployment's .wr_<deployment>/db is restored "+
			"there by that deployment's first manager start", func() {
			remoteDir := filepath.Join(t.TempDir(), ".wr_"+internal.Production)
			So(os.Mkdir(remoteDir, 0o700), ShouldBeNil)

			path := filepath.Join(remoteDir, "db")
			exportStoreTo(dbDir, path)

			server, msg := serveExported(ctx, path, internal.Production)

			So(msg, ShouldContainSubstring, "restored db directory "+path+" from self-contained backup file")

			info, err := os.Stat(path)
			So(err, ShouldBeNil)
			So(info.IsDir(), ShouldBeTrue)
			So(snapshotOf(server.db), ShouldResemble, want)
		})

		Convey("ExportStore, while a manager runs on the store, fails with ErrDBLocked", func() {
			_, serverConfig, _, _, _ := jobqueueTestInit(false)
			serverConfig.storeKind = storeKindLog
			serverConfig.DBFile = dbDir
			serverConfig.dontWipeDevDB = true

			server, _, _, err := serve(ctx, serverConfig)
			So(err, ShouldBeNil)

			Reset(func() { server.Stop(ctx, true) })

			shortenDBOpenTimeout()

			var out bytes.Buffer

			So(ExportStore(dbDir, &out), ShouldWrap, ErrDBLocked)
			So(out.Len(), ShouldEqual, 0)
		})

		Convey("ExportStore to a writer that fails returns its error", func() {
			w := &failingExportWriter{room: exportWriterRoom}

			So(ExportStore(dbDir, w), ShouldWrap, errExportTestWrite)
			So(w.written, ShouldEqual, exportWriterRoom)
		})

		Convey("ExportStore leaves the store free, so it can be exported again at once", func() {
			shortenDBOpenTimeout()

			// with GC off, a leaked lock file's finalizer cannot release the
			// lock for ExportStore, so only ExportStore's own release can.
			gcPercent := debug.SetGCPercent(-1)

			Reset(func() { debug.SetGCPercent(gcPercent) })

			var first, second bytes.Buffer

			So(ExportStore(dbDir, &first), ShouldBeNil)
			So(ExportStore(dbDir, &second), ShouldBeNil)
			So(second.Len(), ShouldBeGreaterThan, 0)
		})

		Convey("ExportStore of a store with a corrupt VERSION fails with errStoreCorrupt, leaving the store "+
			"free, so it fails the same way again at once", func() {
			shortenDBOpenTimeout()
			So(os.WriteFile(filepath.Join(dbDir, storeVersionFile), []byte("garbage\n"), dbFilePermission),
				ShouldBeNil)

			So(ExportStore(dbDir, &bytes.Buffer{}), ShouldWrap, errStoreCorrupt)
			So(ExportStore(dbDir, &bytes.Buffer{}), ShouldWrap, errStoreCorrupt)
		})

		Convey("ExportStore of an empty directory fails with errNotStoreDir, making no store there", func() {
			empty := filepath.Join(t.TempDir(), "empty")
			So(os.Mkdir(empty, 0o700), ShouldBeNil)

			So(ExportStore(empty, &bytes.Buffer{}), ShouldWrap, errNotStoreDir)

			entries, err := os.ReadDir(empty)
			So(err, ShouldBeNil)
			So(entries, ShouldBeEmpty)
			So(empty+storeLockSuffix, shouldNotExist)
		})

		Convey("ExportStore of a path with no store fails, creating nothing there", func() {
			missing := filepath.Join(t.TempDir(), "absent")

			So(ExportStore(missing, &bytes.Buffer{}), ShouldWrap, os.ErrNotExist)
			So(missing, shouldNotExist)
			So(missing+storeLockSuffix, shouldNotExist)
		})

		Convey("ExportStore of a regular file fails with errNotStoreDir, leaving it as it was", func() {
			path := filepath.Join(t.TempDir(), "file.db")
			So(os.WriteFile(path, []byte("not a store"), dbFilePermission), ShouldBeNil)

			So(ExportStore(path, &bytes.Buffer{}), ShouldWrap, errNotStoreDir)

			content, err := os.ReadFile(path)
			So(err, ShouldBeNil)
			So(string(content), ShouldEqual, "not a store")
		})
	})
}

// exportSourceStore makes a closed log store in a new temporary directory
// holding exportLiveJobs live and exportCompleteJobs complete jobs, and
// returns its path and snapshot.
func exportSourceStore(ctx context.Context, t *testing.T) (string, storeSnapshot) {
	t.Helper()

	dbDir := filepath.Join(t.TempDir(), "db")

	s, err := openLogStoreLocked(ctx, storeConfig{dbFile: dbDir, deployment: internal.Development, kind: storeKindLog})
	So(err, ShouldBeNil)

	jobs := make([]*Job, exportLiveJobs+exportCompleteJobs)
	for i := range jobs {
		jobs[i] = testDBJob(fmt.Sprintf("echo export %d", i), storeRestoreRepGroup)
	}

	_, err = s.storeNewJobsGuarded(ctx, jobs, false, nil)
	So(err, ShouldBeNil)
	So(archiveJobsInStore(s, jobs[:exportCompleteJobs]), ShouldBeNil)

	want := snapshotOf(s)
	So(s.close(ctx), ShouldBeNil)

	return dbDir, want
}

// exportStoreTo writes ExportStore's file of the store at dbDir to path.
func exportStoreTo(dbDir, path string) {
	f, err := os.Create(path)
	So(err, ShouldBeNil)
	So(ExportStore(dbDir, f), ShouldBeNil)
	So(f.Close(), ShouldBeNil)
}

// serveExported starts a log-store manager of deployment whose db file is
// path, as a first start with nothing else of its own, and returns it, once
// recovered, with its startup message. It stops when the Convey block ends.
func serveExported(ctx context.Context, path, deployment string) (*Server, string) {
	_, serverConfig, _, _, _ := jobqueueTestInit(false) //nolint:dogsled
	serverConfig.storeKind = storeKindLog
	serverConfig.DBFile = path
	serverConfig.DBFileBackup = path + "_bk"
	serverConfig.Deployment = deployment
	serverConfig.dontWipeDevDB = true

	server, msg, _, err := serve(ctx, serverConfig)
	So(err, ShouldBeNil)

	Reset(func() { server.Stop(ctx, true) })

	So(waitUntilRecovered(server), ShouldBeTrue)

	return server, msg
}

// shortenDBOpenTimeout makes managerDBOpenTimeout exportLockedTimeout until
// the Convey block ends, so a lock left held fails a test at once.
func shortenDBOpenTimeout() {
	original := managerDBOpenTimeout

	Reset(func() { managerDBOpenTimeout = original })

	managerDBOpenTimeout = exportLockedTimeout
}

// readStoreResult is what ReadStore passed fn: the key and command of each
// live and each complete job of storeRestoreRepGroup, and whether each call,
// in order, was of a live job.
type readStoreResult struct {
	live, complete []string
	order          []bool
}

// readStoreChunked returns what ReadStore of the store at dbDir passes fn when
// it reads complete jobs chunk at a time.
func readStoreChunked(dbDir string, chunk int) readStoreResult {
	original := readStoreChunk
	readStoreChunk = chunk

	defer func() { readStoreChunk = original }()

	var read readStoreResult

	So(ReadStore(dbDir, func(live bool, job *Job) error {
		described := job.Key() + " " + job.Cmd
		if live {
			read.live = append(read.live, described)
		} else {
			read.complete = append(read.complete, described)
		}

		read.order = append(read.order, live)

		return nil
	}), ShouldBeNil)

	return read
}

func TestReadStore(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given a stopped store with 10 live and 10 complete jobs", t, func() {
		dbDir, want := exportSourceStore(ctx, t)

		Convey("ReadStore calls fn for every live job, then every complete job, each in key order, "+
			"however many chunks it reads the complete jobs in", func() {
			for _, chunk := range []int{readStoreChunk, 3, 1} {
				read := readStoreChunked(dbDir, chunk)

				So(read.live, ShouldResemble, want.live)
				So(read.complete, ShouldResemble, want.complete)
				So(read.order, ShouldResemble, slices.Concat(slices.Repeat([]bool{true}, exportLiveJobs),
					slices.Repeat([]bool{false}, exportCompleteJobs)))
			}
		})

		Convey("ReadStore stops at the first error fn returns and returns it, leaving the store free, "+
			"so it can be read again at once", func() {
			shortenDBOpenTimeout()

			calls := 0

			So(ReadStore(dbDir, func(bool, *Job) error {
				calls++

				return errReadStoreTestFn
			}), ShouldWrap, errReadStoreTestFn)
			So(calls, ShouldEqual, 1)

			read := readStoreChunked(dbDir, readStoreChunk)
			So(read.live, ShouldResemble, want.live)
		})

		Convey("ReadStore, while a manager runs on the store, fails with ErrDBLocked, calling fn for nothing", func() {
			_, serverConfig, _, _, _ := jobqueueTestInit(false)
			serverConfig.storeKind = storeKindLog
			serverConfig.DBFile = dbDir
			serverConfig.dontWipeDevDB = true

			server, _, _, err := serve(ctx, serverConfig)
			So(err, ShouldBeNil)

			Reset(func() { server.Stop(ctx, true) })

			shortenDBOpenTimeout()

			calls := 0

			So(ReadStore(dbDir, func(bool, *Job) error {
				calls++

				return nil
			}), ShouldWrap, ErrDBLocked)
			So(calls, ShouldEqual, 0)
		})

		Convey("ReadStore of an empty directory fails with errNotStoreDir, making no store there", func() {
			empty := filepath.Join(t.TempDir(), "empty")
			So(os.Mkdir(empty, 0o700), ShouldBeNil)

			So(ReadStore(empty, readStoreNothing), ShouldWrap, errNotStoreDir)

			entries, err := os.ReadDir(empty)
			So(err, ShouldBeNil)
			So(entries, ShouldBeEmpty)
			So(empty+storeLockSuffix, shouldNotExist)
		})

		Convey("ReadStore of a path with no store fails, creating nothing there", func() {
			missing := filepath.Join(t.TempDir(), "absent")

			So(ReadStore(missing, readStoreNothing), ShouldWrap, os.ErrNotExist)
			So(missing, shouldNotExist)
			So(missing+storeLockSuffix, shouldNotExist)
		})
	})

	Convey("Given a stopped store with a job completed, then put back live to be re-run", t, func() {
		job := testDBJob("echo read store rerun", storeRestoreRepGroup)
		dbDir := stoppedStoreOf(ctx, t, func(s *logStore) {
			_, err := s.storeNewJobsGuarded(ctx, []*Job{job}, false, nil)
			So(err, ShouldBeNil)

			archiveTestJob(s, job, time.Now().Add(-time.Minute), time.Now())

			job.RerunAfterRun = true
			So(s.storeLiveForRerun([]*Job{job}), ShouldBeNil)
		})

		Convey("ReadStore calls fn with it once as a live job, then once as a complete job", func() {
			read := readStoreChunked(dbDir, readStoreChunk)

			described := job.Key() + " " + job.Cmd
			So(read.live, ShouldResemble, []string{described})
			So(read.complete, ShouldResemble, []string{described})
			So(read.order, ShouldResemble, []bool{true, false})
		})
	})

	Convey("Given a stopped store with no live jobs and 3 complete jobs", t, func() {
		dbDir := stoppedStoreOf(ctx, t, func(s *logStore) {
			jobs := make([]*Job, 3)
			for i := range jobs {
				jobs[i] = testDBJob(fmt.Sprintf("echo read store complete %d", i), storeRestoreRepGroup)
			}

			_, err := s.storeNewJobsGuarded(ctx, jobs, false, nil)
			So(err, ShouldBeNil)
			So(archiveJobsInStore(s, jobs), ShouldBeNil)
		})

		Convey("ReadStore stops at the first error fn returns for a complete job and returns it, "+
			"however many chunks it reads the complete jobs in", func() {
			original := readStoreChunk

			Reset(func() { readStoreChunk = original })

			for _, chunk := range []int{original, 2, 1} {
				readStoreChunk = chunk
				calls := 0

				So(ReadStore(dbDir, func(live bool, _ *Job) error {
					calls++

					if live {
						return nil
					}

					return errReadStoreTestFn
				}), ShouldWrap, errReadStoreTestFn)
				So(calls, ShouldEqual, 1)
			}
		})
	})

	Convey("Given a crash image of a log-store manager's store with a job reserved but not started", t, func() {
		dbDir, hostname := reservedCrashImage(ctx, t)

		Convey("ReadStore reports the job live, reserved on this host", func() {
			var jobs []*Job

			So(ReadStore(dbDir, func(live bool, job *Job) error {
				if live {
					jobs = append(jobs, job)
				}

				return nil
			}), ShouldBeNil)

			So(jobs, ShouldHaveLength, 1)
			So(jobs[0].Cmd, ShouldEqual, readStoreReservedCmd)
			So(jobs[0].State, ShouldEqual, JobStateReserved)
			So(jobs[0].Host, ShouldEqual, hostname)
		})
	})
}

// stoppedStoreOf returns the directory of a new log store, stopped after
// setup has been called with it open.
func stoppedStoreOf(ctx context.Context, t *testing.T, setup func(s *logStore)) string {
	t.Helper()

	dbDir := filepath.Join(t.TempDir(), "db")
	s := openLogStoreIn(ctx, dbDir)
	setup(s)
	So(s.close(ctx), ShouldBeNil)

	return dbDir
}

// reservedCrashImage starts a log-store manager, adds and reserves a job of
// readStoreReservedCmd, takes a backup, and stops the manager. It returns the
// directory the backup is extracted into, the store as the manager left it at
// the reservation, and this host's name.
func reservedCrashImage(ctx context.Context, t *testing.T) (string, string) {
	t.Helper()

	config, serverConfig, addr, standardReqs, clientConnectTime := jobqueueTestInit(false)
	serverConfig.storeKind = storeKindLog

	server, _, token, err := serve(ctx, serverConfig)
	So(err, ShouldBeNil)

	defer server.Stop(ctx, true)

	jq, err := Connect(addr, config.ManagerCAFile, config.ManagerCertDomain, token, clientConnectTime)
	So(err, ShouldBeNil)

	defer disconnect(jq)

	job := &Job{Cmd: readStoreReservedCmd, Cwd: t.TempDir(), ReqGroup: "readstore", RepGroup: "readstore",
		Requirements: standardReqs}
	added, _, err := jq.Add([]*Job{job}, os.Environ(), true)
	So(err, ShouldBeNil)
	So(added, ShouldEqual, 1)

	reserved, err := jq.Reserve(clientConnectTime)
	So(err, ShouldBeNil)
	So(reserved, ShouldNotBeNil)

	image := filepath.Join(t.TempDir(), "crash.tar")
	f, err := os.Create(image)
	So(err, ShouldBeNil)
	So(server.BackupDB(f), ShouldBeNil)
	So(f.Close(), ShouldBeNil)

	dbDir := filepath.Join(t.TempDir(), "db")
	So(os.Mkdir(dbDir, 0o700), ShouldBeNil)
	So(extractStoreBackup(image, dbDir), ShouldBeNil)

	hostname, err := os.Hostname()
	So(err, ShouldBeNil)

	return dbDir, hostname
}
