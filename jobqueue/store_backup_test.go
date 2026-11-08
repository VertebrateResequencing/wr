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

// Tests of the log store's self-contained backup (spec E5).

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	. "github.com/smartystreets/goconvey/convey"
)

const (
	// backupTestWriterCount is how many goroutines keep writing while E5 test
	// 1's backup is made.
	backupTestWriterCount = 4

	// backupTestWarmWrites is how many jobs each of those adds before the
	// backup is called, so that the backup has acknowledged writes of every
	// writer to hold, spread over several wal files of smallWALSegments' size,
	// and is made while they keep writing.
	backupTestWarmWrites = 100

	// backupTestWarmWait bounds the wait for those writes: each is one small
	// durable write, a few milliseconds even on a loaded host.
	backupTestWarmWait = time.Minute

	// backupTestCheckpointWait is how long a test lets a checkpoint or close
	// that a backup should hold back run before the backup goes on: either
	// takes milliseconds on a store this small, so one held back for this long
	// is waiting for the backup.
	backupTestCheckpointWait = time.Second

	// backupTestFoldBatch is how many jobs addUntilFoldedPast adds at a time,
	// and backupTestFoldBatches the most batches it adds: about 300 jobs fill
	// a wal file of smallWALSegments' size.
	backupTestFoldBatch   = 50
	backupTestFoldBatches = 100

	// tarMagicOffset is where a tar header holds "ustar", which is how open
	// tells a self-contained backup from other files (spec "Location, lock,
	// conversion, restore and backup").
	tarMagicOffset = 257

	// tarMagic is what a tar header holds at tarMagicOffset.
	tarMagic = "ustar"

	// tarEndLen is the length of the two zero blocks that end a whole tar.
	tarEndLen = 2 * 512
)

// errBackupTestNoResult is what awaitBackupTestResult returns when no result
// arrives within backupTestWarmWait.
var errBackupTestNoResult = errors.New("did not finish within backupTestWarmWait")

func TestClientBackupDBSelfContained(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given a manager on the log store holding one job", t, func() {
		config, serverConfig, addr, standardReqs, clientConnectTime := jobqueueTestInit(false)
		serverConfig.storeKind = storeKindLog

		server, _, token, err := serve(ctx, serverConfig)
		So(err, ShouldBeNil)

		defer server.Stop(ctx, true)

		jq, err := Connect(addr, config.ManagerCAFile, config.ManagerCertDomain, token, clientConnectTime)
		So(err, ShouldBeNil)

		defer disconnect(jq)

		backupTmpAddJob(jq, standardReqs)

		Convey("Client.BackupDB writes one regular file, a tar that a manager started with it as its db file "+
			"restores as a store with that job", func() {
			path := filepath.Join(t.TempDir(), "mydb.db")

			So(jq.BackupDB(path), ShouldBeNil)

			info, err := os.Lstat(path)
			So(err, ShouldBeNil)
			So(info.Mode().IsRegular(), ShouldBeTrue)

			data, err := os.ReadFile(path)
			So(err, ShouldBeNil)
			So(len(data), ShouldBeGreaterThan, tarMagicOffset+len(tarMagic))
			So(string(data[tarMagicOffset:tarMagicOffset+len(tarMagic)]), ShouldEqual, tarMagic)

			server.Stop(ctx, true)

			serverConfig.DBFile = path
			serverConfig.dontWipeDevDB = true

			restarted, msg, _, err := serve(ctx, serverConfig)
			So(err, ShouldBeNil)

			defer restarted.Stop(ctx, true)

			So(msg, ShouldContainSubstring, "restored db directory "+path+" from self-contained backup file")

			info, err = os.Stat(path)
			So(err, ShouldBeNil)
			So(info.IsDir(), ShouldBeTrue)
			So(waitUntilRecovered(restarted), ShouldBeTrue)
			So(inspectedCount(restarted.inspector(), kindLive), ShouldEqual, 1)
		})
	})
}

// backupTestAcks are the writes the backup test's writers have had
// acknowledged: the jobs added, and those archived.
type backupTestAcks struct {
	added, archived []*Job
}

// backupTestWriters are goroutines adding jobs to a store, and archiving every
// other one, until stopped, noting each write acknowledged.
type backupTestWriters struct {
	stopCh   chan struct{}
	stopOnce sync.Once
	wg       sync.WaitGroup
	warmed   sync.WaitGroup

	mu    sync.Mutex
	acks  backupTestAcks
	first error
}

// startBackupTestWriters starts backupTestWriterCount goroutines writing to s.
func startBackupTestWriters(ctx context.Context, s *logStore) *backupTestWriters {
	w := &backupTestWriters{stopCh: make(chan struct{})}

	w.warmed.Add(backupTestWriterCount)

	for i := range backupTestWriterCount {
		w.wg.Go(func() { w.write(ctx, s, i) })
	}

	return w
}

// write is writer i's loop: it adds a job, archives every other one, and notes
// each acknowledged write, until stopped or a write fails.
func (w *backupTestWriters) write(ctx context.Context, s *logStore, i int) {
	var warmOnce sync.Once

	defer warmOnce.Do(w.warmed.Done)

	for n := 0; ; n++ {
		select {
		case <-w.stopCh:
			return
		default:
		}

		if n == backupTestWarmWrites {
			warmOnce.Do(w.warmed.Done)
		}

		job := testDBJob(fmt.Sprintf("echo backup writer %d %d", i, n), "backup writers")

		if _, err := s.storeNewJobsGuarded(ctx, []*Job{job}, false, nil); err != nil {
			w.fail(err)

			return
		}

		w.note(&w.acks.added, job)

		if n%2 == 1 {
			continue
		}

		if err := archiveBackupTestJob(s, job); err != nil {
			w.fail(err)

			return
		}

		w.note(&w.acks.archived, job)
	}
}

// archiveBackupTestJob archives job, which must leave it no longer live.
func archiveBackupTestJob(s *logStore, job *Job) error {
	job.Lock()
	job.State = JobStateComplete
	job.Exited = true
	job.EndTime = time.Now()
	job.Unlock()

	outcome, err := s.archiveCompletion(job.Key(), job)
	if err == nil && outcome != archiveRemovedLive {
		err = fmt.Errorf("archive of %s: outcome %d", job.Key(), outcome) //nolint:err113 // a test failure
	}

	return err
}

// note appends job to acks under the writers' lock.
func (w *backupTestWriters) note(acks *[]*Job, job *Job) {
	w.mu.Lock()
	defer w.mu.Unlock()

	*acks = append(*acks, job)
}

// fail notes err, if it is the first write to fail.
func (w *backupTestWriters) fail(err error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	if w.first == nil {
		w.first = err
	}
}

// warm waits up to backupTestWarmWait for every writer to have made
// backupTestWarmWrites writes, or ended, reporting whether they did.
func (w *backupTestWriters) warm() bool {
	done := make(chan struct{})

	go func() {
		w.warmed.Wait()
		close(done)
	}()

	select {
	case <-done:
		return true
	case <-time.After(backupTestWarmWait):
		return false
	}
}

// acknowledged returns a copy of the writes acknowledged so far.
func (w *backupTestWriters) acknowledged() backupTestAcks {
	w.mu.Lock()
	defer w.mu.Unlock()

	return backupTestAcks{added: slices.Clone(w.acks.added), archived: slices.Clone(w.acks.archived)}
}

// stop stops the writers and waits for them to end.
func (w *backupTestWriters) stop() {
	w.stopOnce.Do(func() { close(w.stopCh) })
	w.wg.Wait()
}

// err returns the first write that failed, if any.
func (w *backupTestWriters) err() error {
	w.mu.Lock()
	defer w.mu.Unlock()

	return w.first
}

func TestLogStoreSelfContainedBackup(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given a log store with jobs live only in its checkpoint, complete only in its history, and more in its "+
		"wal", t, func() {
		smallWALSegments()

		s, _ := openTestLogStore(t, ctx)

		checkpointed := testLogJobs("backup checkpointed", "backup", 3)
		folded := testLogJobs("backup folded", "backup", 3)
		_, err := s.storeNewJobsGuarded(ctx, slices.Concat(checkpointed, folded), false, nil)
		So(err, ShouldBeNil)

		start := time.Now()
		for _, job := range folded {
			archiveTestJob(s, job, start, start.Add(time.Second))
		}

		So(s.checkpoint(), ShouldBeNil)

		logged := testLogJobs("backup logged", "backup", 3)
		_, err = s.storeNewJobsGuarded(ctx, logged, false, nil)
		So(err, ShouldBeNil)
		archiveTestJob(s, logged[0], start, start.Add(time.Second))

		Convey("a backup made while 4 goroutines keep writing is a tar of VERSION, one checkpoint, wal files and "+
			"history.bolt that holds every write acknowledged before it was called", func() {
			writers := startBackupTestWriters(ctx, s)
			Reset(writers.stop)

			So(writers.warm(), ShouldBeTrue)

			acked := writers.acknowledged()

			var buf bytes.Buffer

			err := s.backup(&buf)

			writers.stop()
			So(err, ShouldBeNil)
			So(writers.err(), ShouldBeNil)

			data := buf.Bytes()
			So(len(data), ShouldBeGreaterThan, tarMagicOffset+len(tarMagic))
			So(string(data[tarMagicOffset:tarMagicOffset+len(tarMagic)]), ShouldEqual, tarMagic)

			So(data[len(data)-tarEndLen:], ShouldResemble, make([]byte, tarEndLen))

			members := backupMemberKinds(data)
			So(members[storeVersionFile], ShouldEqual, 1)
			So(members[checkpointFileExt], ShouldEqual, 1)
			So(members[historyBoltFile], ShouldEqual, 1)
			So(members[walFileExt], ShouldBeGreaterThan, 1)
			So(slices.Sorted(maps.Keys(members)), ShouldResemble,
				[]string{checkpointFileExt, walFileExt, storeVersionFile, historyBoltFile})

			// opened as spec E2 opens it: restored as a missing db file's
			// backup.
			restored := openBackupBytes(t, data)

			So(backupLiveOrComplete(restored, slices.Concat(checkpointed, logged[1:])), ShouldBeEmpty)
			So(backupComplete(restored, append(slices.Clone(folded), logged[0])), ShouldBeEmpty)
			So(len(acked.added), ShouldBeGreaterThanOrEqualTo, backupTestWriterCount*backupTestWarmWrites)
			So(len(acked.archived), ShouldBeGreaterThan, 0)
			So(backupLiveOrComplete(restored, acked.added), ShouldBeEmpty)
			So(backupComplete(restored, acked.archived), ShouldBeEmpty)
		})

		Convey("a backup made once history has folded past the checkpoint's walFrom file holds the jobs added "+
			"since the checkpoint", func() {
			walFrom := s.checkpointFrom()
			added := addUntilFoldedPast(ctx, s, walFrom)

			var buf bytes.Buffer
			So(s.backup(&buf), ShouldBeNil)

			restored := openBackupBytes(t, buf.Bytes())
			So(backupLiveOrComplete(restored, slices.Concat(checkpointed, logged[1:], added)), ShouldBeEmpty)
			So(backupComplete(restored, append(slices.Clone(folded), logged[0])), ShouldBeEmpty)
		})

		Convey("a checkpoint due during a backup waits for it, and the backup holds every job acknowledged before "+
			"it was called", func() {
			ckptDone := make(chan error, 1)
			ckptDuringBackup := false

			hook := func() {
				go func() { ckptDone <- s.checkpoint() }()

				select {
				case err := <-ckptDone:
					ckptDuringBackup = true

					ckptDone <- err
				case <-time.After(backupTestCheckpointWait):
				}
			}
			logBackupWALHook.Store(&hook)
			Reset(func() { logBackupWALHook.Store(nil) })

			var buf bytes.Buffer

			err := s.backup(&buf)

			logBackupWALHook.Store(nil)
			So(err, ShouldBeNil)

			So(awaitBackupTestResult(ckptDone), ShouldBeNil)

			restored := openBackupBytes(t, buf.Bytes())
			So(backupLiveOrComplete(restored, slices.Concat(checkpointed, logged[1:])), ShouldBeEmpty)
			So(backupComplete(restored, append(slices.Clone(folded), logged[0])), ShouldBeEmpty)
			So(ckptDuringBackup, ShouldBeFalse)
		})

		Convey("a close during a backup waits for it, keeping the store's history and lock, and the backup "+
			"fails with errDBClosed", func() {
			held, dbFile := openHeldLogStore(t, ctx)

			withStoreLockOpenTimeout()

			closeDone := make(chan error, 1)

			var (
				closedEarly         bool
				historyErr, lockErr error
				lockDuringBackup    *storeLock
			)

			hook := func() {
				go func() { closeDone <- held.close(ctx) }()

				select {
				case <-held.ckptExited:
				case <-time.After(backupTestWarmWait):
					return
				}

				select {
				case <-closeDone:
					closedEarly = true
				case <-time.After(backupTestCheckpointWait):
				}

				_, historyErr = held.history.backup(tar.NewWriter(io.Discard))
				lockDuringBackup, lockErr = lockStore(ctx, dbFile)
			}
			logBackupWALHook.Store(&hook)
			Reset(func() { logBackupWALHook.Store(nil) })

			err := held.backup(io.Discard)

			logBackupWALHook.Store(nil)
			So(lockDuringBackup.release(), ShouldBeNil)
			So(err, ShouldWrap, errDBClosed)
			So(closedEarly, ShouldBeFalse)
			So(historyErr, ShouldBeNil)
			So(lockErr, ShouldNotBeNil)
			So(lockErr, ShouldWrap, ErrDBLocked)
			So(awaitBackupTestResult(closeDone), ShouldBeNil)

			lock, err := lockStore(ctx, dbFile)
			So(err, ShouldBeNil)
			So(lock.release(), ShouldBeNil)
		})

		Convey("backups started as it closes each succeed or fail with errDBClosed, and close waits for those "+
			"that started", func() {
			var (
				wg       sync.WaitGroup
				mu       sync.Mutex
				outcomes []error
			)

			for range backupTestWriterCount {
				wg.Go(func() {
					for {
						err := s.backup(io.Discard)

						mu.Lock()

						outcomes = append(outcomes, err)
						mu.Unlock()

						if err != nil {
							return
						}
					}
				})
			}

			So(s.close(ctx), ShouldBeNil)
			wg.Wait()

			So(len(outcomes), ShouldBeGreaterThanOrEqualTo, backupTestWriterCount)

			others := slices.DeleteFunc(slices.Clone(outcomes), func(err error) bool {
				return err == nil || errors.Is(err, errDBClosed)
			})
			So(others, ShouldBeEmpty)
		})

		Convey("a backup once it is closed fails with errDBClosed", func() {
			So(s.close(ctx), ShouldBeNil)
			So(s.backup(io.Discard), ShouldWrap, errDBClosed)
		})
	})
}

// backupMemberKinds returns how many members of the tar data hold each kind of
// store file: VERSION and history.bolt by name, checkpoints and wal files by
// extension, and anything else by its whole name. A member that is not a
// regular file at the top level counts under its name with a "!" in front.
func backupMemberKinds(data []byte) map[string]int {
	kinds := make(map[string]int)

	tr := tar.NewReader(bytes.NewReader(data))

	hdr, err := tr.Next()
	for ; err == nil; hdr, err = tr.Next() {
		kind := hdr.Name

		switch ext := filepath.Ext(hdr.Name); {
		case hdr.Typeflag != tar.TypeReg || strings.Contains(hdr.Name, "/"):
			kind = "!" + hdr.Name
		case ext == checkpointFileExt || ext == walFileExt:
			kind = ext
		}

		kinds[kind]++
	}

	So(err, ShouldWrap, io.EOF)

	return kinds
}

// openBackupBytes opens the self-contained backup data as a store, through
// openStoreFileInspector, returning an inspector closed when the Convey block
// ends.
func openBackupBytes(t *testing.T, data []byte) storeInspector {
	t.Helper()

	path := filepath.Join(t.TempDir(), "backup.tar")
	So(os.WriteFile(path, data, dbFilePermission), ShouldBeNil)

	inspector, closeFiles, err := openStoreFileInspector(path)
	So(err, ShouldBeNil)

	Reset(func() { So(closeFiles(), ShouldBeNil) })

	return inspector
}

// backupLiveOrComplete returns the keys of those of jobs that inspector holds
// neither live nor complete.
func backupLiveOrComplete(inspector storeInspector, jobs []*Job) []string {
	return backupMissing(jobs, func(key string) (bool, error) {
		_, live, err := inspector.liveJob(key)
		if live || err != nil {
			return live, err
		}

		_, complete, err := inspector.completeJob(key)

		return complete, err
	})
}

// backupMissing returns the keys of those of jobs for which held is false,
// asserting it never failed.
func backupMissing(jobs []*Job, held func(key string) (bool, error)) []string {
	var (
		missing []string
		errs    []error
	)

	for _, job := range jobs {
		found, err := held(job.Key())
		if err != nil {
			errs = append(errs, err)
		}

		if !found {
			missing = append(missing, job.Key())
		}
	}

	So(errors.Join(errs...), ShouldBeNil)

	return missing
}

// backupComplete returns the keys of those of jobs that inspector does not hold
// complete.
func backupComplete(inspector storeInspector, jobs []*Job) []string {
	return backupMissing(jobs, func(key string) (bool, error) {
		_, complete, err := inspector.completeJob(key)

		return complete, err
	})
}

// addUntilFoldedPast adds jobs to s, folding after each batch, until history
// has folded past wal file walFrom, asserting it did, and returns the jobs.
func addUntilFoldedPast(ctx context.Context, s *logStore, walFrom uint32) []*Job {
	var added []*Job

	for i := range backupTestFoldBatches {
		if foldedFile(s) > walFrom {
			break
		}

		jobs := testLogJobs(fmt.Sprintf("backup fold %d", i), "backup", backupTestFoldBatch)

		_, err := s.storeNewJobsGuarded(ctx, jobs, false, nil)
		So(err, ShouldBeNil)
		So(s.flush(), ShouldBeNil)

		added = append(added, jobs...)
	}

	So(foldedFile(s), ShouldBeGreaterThan, walFrom)

	return added
}

// foldedFile returns the wal file history has folded to.
func foldedFile(s *logStore) uint32 {
	s.mu.RLock()
	defer s.mu.RUnlock()

	return s.state.folded.file
}

// awaitBackupTestResult returns the result results receives, or
// errBackupTestNoResult if none arrives within backupTestWarmWait.
func awaitBackupTestResult(results <-chan error) error {
	select {
	case err := <-results:
		return err
	case <-time.After(backupTestWarmWait):
		return errBackupTestNoResult
	}
}

// openHeldLogStore opens a log store at a new dbFile holding its store lock, as
// a manager does, closed when the Convey block ends, and returns it with
// dbFile.
func openHeldLogStore(t *testing.T, ctx context.Context) (*logStore, string) {
	t.Helper()

	cfg, _ := logStoreOpenConfig(t)

	s, err := openLogStoreLocked(ctx, cfg)
	So(err, ShouldBeNil)

	Reset(func() { So(s.close(ctx), ShouldBeNil) })

	return s, cfg.dbFile
}
