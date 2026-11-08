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

// errExportTestWrite is the error a failingExportWriter fails with.
var errExportTestWrite = errors.New("export writer full")

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

		Convey("ExportStore of an empty directory fails with errExportNotStore, making no store there", func() {
			empty := filepath.Join(t.TempDir(), "empty")
			So(os.Mkdir(empty, 0o700), ShouldBeNil)

			So(ExportStore(empty, &bytes.Buffer{}), ShouldWrap, errExportNotStore)

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

		Convey("ExportStore of a regular file fails with errExportNotStore, leaving it as it was", func() {
			path := filepath.Join(t.TempDir(), "file.db")
			So(os.WriteFile(path, []byte("not a store"), dbFilePermission), ShouldBeNil)

			So(ExportStore(path, &bytes.Buffer{}), ShouldWrap, errExportNotStore)

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
