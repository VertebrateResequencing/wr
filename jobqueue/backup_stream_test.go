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

// Tests that Client.BackupDB streams the backup from the manager in chunks
// (spec E5, delivery-queue row 25), so that the manager never holds a whole
// backup in memory.

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"os"
	"path/filepath"
	"runtime"
	"runtime/metrics"
	"sync"
	"testing"
	"time"

	jqs "github.com/VertebrateResequencing/wr/jobqueue/scheduler"
	. "github.com/smartystreets/goconvey/convey"
)

const (
	// backupStreamTestJobs jobs of backupStreamTestCmdBytes each make a
	// backup of at least 64MiB, many times backupStreamTestChunk.
	backupStreamTestJobs     = 1024
	backupStreamTestCmdBytes = 64 << 10

	// backupStreamTestAddBatch is how many of those jobs one Add sends.
	backupStreamTestAddBatch = 64

	// backupStreamTestChunk is the chunk size the tests stream with: small,
	// so that a manager holding a few chunks at once is still far below one
	// holding the whole backup.
	backupStreamTestChunk = 256 << 10

	// backupStreamTestMinChunks is the fewest chunks a backup of those jobs
	// must take, so it is "much larger than one chunk".
	backupStreamTestMinChunks = 128

	// backupStreamTestHeapShare is the most of the backup's size that the
	// heap may grow by while it is made and fetched: a manager and client
	// holding a few chunks each stay well below it, as does the store's own
	// work on a loaded host (up to a quarter of the backup's size seen), while
	// one holding the whole backup (and the client its whole reply) grows by
	// more than the backup's size, several times it.
	backupStreamTestHeapShare = 2

	// backupStreamTestBaseSamples is how many samples of the live heap give
	// its base before a backup: its highest, so that its own ups and downs
	// while no backup runs do not count as growth.
	backupStreamTestBaseSamples = 20

	// backupStreamTestSmallChunk is the chunk size the tests of a backup's
	// lifecycle stream with, so that a backup of one job is several chunks.
	backupStreamTestSmallChunk = 1024

	// backupStreamTestFailAfter is how many bytes a failing backup writes
	// before it fails: more than two chunks, ending part way through a third.
	backupStreamTestFailAfter = 5 * backupStreamTestSmallChunk / 2

	// backupStreamTestWritePiece is how many bytes each write of a backup of
	// known bytes carries: not a divisor of backupStreamTestSmallChunk, so
	// chunks are read across writes.
	backupStreamTestWritePiece = 1000

	// backupStreamTestIdle is the idle timeout of the abandon test, and
	// backupStreamTestAbandonWait how long it leaves a backup unfetched: many
	// times that timeout, so a timer firing late on a loaded host still fires
	// within it.
	backupStreamTestIdle        = 100 * time.Millisecond
	backupStreamTestAbandonWait = 20 * backupStreamTestIdle

	// backupStreamTestLongIdle is an idle timeout no test waits out, and
	// backupStreamTestStopWait how long a manager may take to stop: stopping
	// takes seconds, far less than that timeout, so a stop that waits for an
	// unfetched backup to be abandoned fails.
	backupStreamTestLongIdle = 10 * time.Minute
	backupStreamTestStopWait = time.Minute
)

var (
	errBackupStreamTestStore = errors.New("the store's backup failed")
	errBackupStreamTestWrite = errors.New("no space left")
)

// backupStreamFailingWriter fails every write with err, as a full disk would.
type backupStreamFailingWriter struct {
	err error
}

func (w backupStreamFailingWriter) Write([]byte) (int, error) {
	return 0, w.err
}

func TestBackupStreamLifecycle(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	forEachStore(t, func(kind storeKind) {
		Convey("Given a manager on the "+storeKindName(kind)+" store holding one job", t, func() {
			config, serverConfig, addr, standardReqs, clientConnectTime := jobqueueTestInit(false)
			serverConfig.storeKind = kind

			setBackupChunkSize(backupStreamTestSmallChunk)
			setBackupStreamIdleTimeout(backupStreamTestLongIdle)

			server, _, token, err := serve(ctx, serverConfig)
			So(err, ShouldBeNil)

			defer server.Stop(ctx, true)

			jq, err := Connect(addr, config.ManagerCAFile, config.ManagerCertDomain, token, clientConnectTime)
			So(err, ShouldBeNil)

			defer disconnect(jq)

			backupTmpAddJob(jq, standardReqs)

			path := filepath.Join(t.TempDir(), "mydb.db")

			Convey("a backup left unfetched past its idle timeout is abandoned: fetching more of it fails, "+
				"and another backup then completes", func() {
				id := backupStreamStart(jq)
				backupStreamFetchChunk(jq, id, 0)
				backupStreamShortenIdle(server, id)
				backupStreamFetchChunk(jq, id, backupStreamTestSmallChunk)

				time.Sleep(backupStreamTestAbandonWait)

				_, err := backupStreamRequest(jq, id, 2*backupStreamTestSmallChunk)
				So(err, ShouldNotBeNil)
				So(err.Error(), ShouldContainSubstring, errBackupStreamUnknown.Error())

				So(jq.BackupDB(path), ShouldBeNil)
				assertStoreFileLiveJobs(path, 1)
			})

			Convey("Client.BackupDB writes exactly the bytes the store's backup wrote", func() {
				Convey("when they end part way through a chunk", func() {
					backupStreamAssertExactFetch(jq, path, backupStreamTestFailAfter)
				})

				Convey("when they are a whole number of chunks, so the last reply is empty", func() {
					backupStreamAssertExactFetch(jq, path, 3*backupStreamTestSmallChunk)
				})

				Convey("when they are less than a chunk", func() {
					backupStreamAssertExactFetch(jq, path, backupStreamTestSmallChunk/2)
				})

				Convey("when there are none", func() {
					backupStreamAssertExactFetch(jq, path, 0)
				})
			})

			Convey("a backup whose store fails after writing more than a chunk fails Client.BackupDB, "+
				"which leaves no file at its path nor a staged one", func() {
				dir := t.TempDir()

				Convey("when the store fails with a plain error", func() {
					backupStreamAssertFailedFetch(jq, dir, errBackupStreamTestStore)
				})

				Convey("when the store fails with an error wrapping io.EOF, as copying a file "+
					"shorter than it was would", func() {
					backupStreamAssertFailedFetch(jq, dir, fmt.Errorf("copy history: %w", io.EOF))
				})
			})

			Convey("a backup the client fails to write is ended at once, so it no longer counts "+
				"towards maxBackupStreams", func() {
				So(jq.fetchBackup(backupStreamFailingWriter{err: errBackupStreamTestWrite}), ShouldEqual,
					errBackupStreamTestWrite)

				backupStreamAssertNoneHeld(jq)
			})

			Convey("a manager stops promptly while a backup is part fetched", func() {
				backupStreamFetchChunk(jq, backupStreamStart(jq), 0)

				stopped := make(chan struct{})

				go func() {
					defer close(stopped)

					server.Stop(ctx, true)
				}()

				select {
				case <-stopped:
				case <-time.After(backupStreamTestStopWait):
					So("manager stop waited for the unfetched backup", ShouldBeEmpty)
				}
			})

			Convey("no more than maxBackupStreams backups can be fetched at once", func() {
				for range maxBackupStreams {
					backupStreamStart(jq)
				}

				_, err := backupStreamRequest(jq, "", 0)
				So(err, ShouldNotBeNil)
				So(err.Error(), ShouldContainSubstring, errBackupStreamsFull.Error())
			})

			Convey("a chunk asked for again, as a resent request would, ends the backup", func() {
				id := backupStreamStart(jq)
				backupStreamFetchChunk(jq, id, 0)

				_, err := backupStreamRequest(jq, id, 0)
				So(err, ShouldNotBeNil)
				So(err.Error(), ShouldContainSubstring, errBackupStreamOutOfStep.Error())

				_, err = backupStreamRequest(jq, id, backupStreamTestSmallChunk)
				So(err, ShouldNotBeNil)
				So(err.Error(), ShouldContainSubstring, errBackupStreamUnknown.Error())

				So(jq.BackupDB(path), ShouldBeNil)
				assertStoreFileLiveJobs(path, 1)
			})

			Convey("a chunk asked for while another request is reading it, as a resent request would be, "+
				"ends the backup, failing both requests", func() {
				jq2, err := Connect(addr, config.ManagerCAFile, config.ManagerCertDomain, token, clientConnectTime)
				So(err, ShouldBeNil)

				defer disconnect(jq2)

				release := backupStreamHoldProducer()
				id := backupStreamStart(jq)

				firstErr := backupStreamRequestAsync(jq, id, 0)
				So(backupStreamWaitBusy(server, id), ShouldBeTrue)

				secondErr := backupStreamRequestAsync(jq2, id, 0)
				So(backupStreamAwaitErr(secondErr), ShouldContainSubstring, errBackupStreamOutOfStep.Error())

				release()
				So(backupStreamAwaitErr(firstErr), ShouldContainSubstring, errBackupStreamOutOfStep.Error())
			})
		})
	})
}

// setBackupChunkSize makes the manager stream backups in chunks of size
// bytes until the Convey block ends. It must be called before the block's
// manager starts: the Reset runs after the block's deferred Stop, so no
// request reads the size as it is put back.
func setBackupChunkSize(size int) {
	orig := backupChunkSize

	Reset(func() { backupChunkSize = orig })

	backupChunkSize = size
}

// setBackupStreamIdleTimeout makes the manager abandon a backup left unfetched
// for timeout until the Convey block ends; see setBackupChunkSize.
func setBackupStreamIdleTimeout(timeout time.Duration) {
	orig := backupStreamIdleTimeout

	Reset(func() { backupStreamIdleTimeout = orig })

	backupStreamIdleTimeout = timeout
}

// backupStreamStart has jq's manager start a backup, returning its id.
func backupStreamStart(jq *Client) string {
	resp, err := backupStreamRequest(jq, "", 0)
	So(err, ShouldBeNil)
	So(resp.BackupID, ShouldNotBeEmpty)

	return resp.BackupID
}

// backupStreamRequest sends jq's manager a request for the chunk of backup id
// at offset, or to start a backup if id is "".
func backupStreamRequest(jq *Client, id string, offset int64) (*serverResponse, error) {
	return jq.request(&clientRequest{Method: requestMethodBackupChunk, BackupID: id, BackupOffset: offset})
}

// backupStreamFetchChunk fetches the chunk of backup id at offset, which must
// be a whole chunk and not its last.
func backupStreamFetchChunk(jq *Client, id string, offset int64) {
	resp, err := backupStreamRequest(jq, id, offset)
	So(err, ShouldBeNil)
	So(resp.DB, ShouldHaveLength, backupStreamTestSmallChunk)
	So(resp.BackupDone, ShouldBeFalse)
}

// backupStreamShortenIdle makes server's idle timeout for backup id
// backupStreamTestIdle. It takes effect when the next chunk of it is fetched,
// as the manager restarts its idle timer then; it is called after the backup's
// first chunk is fetched, so that a slow start on a loaded host does not
// abandon the backup before then.
func backupStreamShortenIdle(server *Server, id string) {
	server.backupStreams.mu.Lock()
	defer server.backupStreams.mu.Unlock()

	st := server.backupStreams.streams[id]
	So(st, ShouldNotBeNil)

	st.idleTimeout = backupStreamTestIdle
}

// backupStreamAssertNoneHeld asserts that jq's manager holds no backup that
// has ended, by starting maxBackupStreams backups, which must all start: one
// still held would take one of their places until its idle timeout.
func backupStreamAssertNoneHeld(jq *Client) {
	for range maxBackupStreams {
		backupStreamStart(jq)
	}
}

// backupStreamRequestAsync sends jq's manager a request for the chunk of
// backup id at offset in its own goroutine, returning a channel its error is
// sent on.
func backupStreamRequestAsync(jq *Client, id string, offset int64) <-chan error {
	errCh := make(chan error, 1)

	go func() {
		_, err := backupStreamRequest(jq, id, offset)
		errCh <- err
	}()

	return errCh
}

// backupStreamWaitBusy waits for a request to be reading backup id from
// server, returning whether one was within backupStreamTestStopWait.
func backupStreamWaitBusy(server *Server, id string) bool {
	deadline := time.Now().Add(backupStreamTestStopWait)

	for time.Now().Before(deadline) {
		server.backupStreams.mu.Lock()
		st := server.backupStreams.streams[id]
		busy := st != nil && st.busy
		server.backupStreams.mu.Unlock()

		if busy {
			return true
		}

		time.Sleep(time.Millisecond)
	}

	return false
}

// backupStreamAwaitErr returns the text of the error sent on errCh, which must
// come well inside a request's own timeout and not be nil.
func backupStreamAwaitErr(errCh <-chan error) string {
	select {
	case err := <-errCh:
		So(err, ShouldNotBeNil)

		return err.Error()
	case <-time.After(ClientMinRequestTimeout / 2):
		So("the chunk request got no reply", ShouldBeEmpty)

		return ""
	}
}

// backupStreamAssertExactFetch makes the manager's backups write size
// pseudo-random bytes, in writes that do not line up with chunks, and asserts
// that Client.BackupDB to path then writes exactly those bytes.
func backupStreamAssertExactFetch(jq *Client, path string, size int) {
	want := make([]byte, size)
	_, err := rand.NewChaCha8([32]byte{2}).Read(want)
	So(err, ShouldBeNil)

	produce := func(w io.Writer) error {
		for rest := want; len(rest) > 0; {
			piece := rest[:min(len(rest), backupStreamTestWritePiece)]
			if _, errw := w.Write(piece); errw != nil {
				return errw
			}

			rest = rest[len(piece):]
		}

		return nil
	}

	Reset(func() { backupProduceHook.Store(nil) })
	backupProduceHook.Store(&produce)

	So(jq.BackupDB(path), ShouldBeNil)

	got, err := os.ReadFile(path)
	So(err, ShouldBeNil)
	So(len(got), ShouldEqual, size)
	So(bytes.Equal(got, want), ShouldBeTrue)

	backupStreamAssertNoneHeld(jq)
}

// backupStreamAssertFailedFetch makes the manager's backups write
// backupStreamTestFailAfter bytes then fail with failure, and asserts that
// Client.BackupDB to a file in dir then fails, leaving dir empty.
func backupStreamAssertFailedFetch(jq *Client, dir string, failure error) {
	produce := func(w io.Writer) error {
		if _, err := w.Write(bytes.Repeat([]byte{'x'}, backupStreamTestFailAfter)); err != nil {
			return err
		}

		return failure
	}

	Reset(func() { backupProduceHook.Store(nil) })
	backupProduceHook.Store(&produce)

	So(jq.BackupDB(filepath.Join(dir, "mydb.db")), ShouldNotBeNil)

	entries, err := os.ReadDir(dir)
	So(err, ShouldBeNil)
	So(entries, ShouldBeEmpty)

	backupStreamAssertNoneHeld(jq)
}

// backupStreamHoldProducer makes the manager's backups write nothing until the
// returned func is called, then write more than two chunks. The func is also
// called when the Convey block ends, so no backup is left waiting.
func backupStreamHoldProducer() func() {
	hold := make(chan struct{})
	release := sync.OnceFunc(func() { close(hold) })

	produce := func(w io.Writer) error {
		<-hold

		_, err := w.Write(bytes.Repeat([]byte{'x'}, backupStreamTestFailAfter))

		return err
	}

	Reset(func() {
		release()
		backupProduceHook.Store(nil)
	})
	backupProduceHook.Store(&produce)

	return release
}

func TestBackupStreams(t *testing.T) {
	Convey("Given no backups being fetched", t, func() {
		var b backupStreams

		defer b.close()

		Convey("a backup fetched to its end is forgotten", func() {
			id, err := b.start(func(w io.Writer) error {
				_, errw := w.Write([]byte("0123456789"))

				return errw
			})
			So(err, ShouldBeNil)

			var got []byte

			for {
				data, done, errc := b.chunk(id, int64(len(got)))
				So(errc, ShouldBeNil)

				got = append(got, data...)

				if done {
					break
				}
			}

			So(string(got), ShouldEqual, "0123456789")
			So(backupStreamsHeld(&b), ShouldEqual, 0)
		})

		Convey("a backup whose store fails is forgotten once a chunk request reads the failure", func() {
			id, err := b.start(func(io.Writer) error { return errBackupStreamTestStore })
			So(err, ShouldBeNil)

			_, _, err = b.chunk(id, 0)
			So(err, ShouldNotBeNil)
			So(err.Error(), ShouldEqual, errBackupStreamTestStore.Error())
			So(backupStreamsHeld(&b), ShouldEqual, 0)
		})

		Convey("once closed, no backup starts", func() {
			b.close()

			_, err := b.start(func(io.Writer) error { return nil })
			So(err, ShouldEqual, errBackupStreamsClosed)
		})

		Convey("abandon leaves a backup in place", func() {
			id, err := b.start(func(w io.Writer) error {
				_, errw := w.Write([]byte("0123456789"))

				return errw
			})
			So(err, ShouldBeNil)

			b.mu.Lock()
			st := b.streams[id]
			b.mu.Unlock()

			Convey("when it has not been idle for its idle timeout", func() {
				b.abandon(id, st)
				So(backupStreamsHeld(&b), ShouldEqual, 1)
			})

			Convey("when a chunk request is reading it, however long it was idle before", func() {
				b.mu.Lock()
				st.busy = true
				st.idleSince = time.Now().Add(-time.Hour)
				b.mu.Unlock()

				b.abandon(id, st)
				So(backupStreamsHeld(&b), ShouldEqual, 1)
			})
		})
	})
}

// backupStreamsHeld returns how many backups b holds.
func backupStreamsHeld(b *backupStreams) int {
	b.mu.Lock()
	defer b.mu.Unlock()

	return len(b.streams)
}

func TestClientBackupDBStreams(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	forEachStore(t, func(kind storeKind) {
		Convey("Given a manager on the "+storeKindName(kind)+" store whose backup is many chunks long", t, func() {
			config, serverConfig, addr, standardReqs, clientConnectTime := jobqueueTestInit(false)
			serverConfig.storeKind = kind

			setBackupChunkSize(backupStreamTestChunk)

			server, _, token, err := serve(ctx, serverConfig)
			So(err, ShouldBeNil)

			defer server.Stop(ctx, true)

			jq, err := Connect(addr, config.ManagerCAFile, config.ManagerCertDomain, token, clientConnectTime)
			So(err, ShouldBeNil)

			defer disconnect(jq)

			backupStreamAddBigJobs(jq, standardReqs)

			dir := t.TempDir()

			Convey("Client.BackupDB writes the whole backup, which holds every job", func() {
				path := filepath.Join(dir, "mydb.db")

				So(jq.BackupDB(path), ShouldBeNil)

				info, err := os.Stat(path)
				So(err, ShouldBeNil)
				So(info.Size(), ShouldBeGreaterThan, backupStreamTestMinChunks*backupStreamTestChunk)
				assertStoreFileLiveJobs(path, backupStreamTestJobs)
			})

			Convey("the heap grows by far less than the backup's size while Client.BackupDB runs", func() {
				warm := filepath.Join(dir, "warm.db")
				So(jq.BackupDB(warm), ShouldBeNil)

				path := filepath.Join(dir, "mydb.db")

				var errb error

				growth := peakHeapGrowthDuring(func() { errb = jq.BackupDB(path) })

				So(errb, ShouldBeNil)

				info, err := os.Stat(path)
				So(err, ShouldBeNil)

				size := uint64(info.Size()) //nolint:gosec // a file's size is never negative
				So(growth, ShouldBeLessThan, size/backupStreamTestHeapShare)
			})
		})
	})
}

// backupStreamAddBigJobs adds backupStreamTestJobs jobs through jq, each with
// a command of backupStreamTestCmdBytes random, poorly compressible
// characters, so that a backup holding them is large.
func backupStreamAddBigJobs(jq *Client, reqs *jqs.Requirements) {
	rng := rand.NewChaCha8([32]byte{1})
	raw := make([]byte, base64.StdEncoding.DecodedLen(backupStreamTestCmdBytes))

	for batch := range backupStreamTestJobs / backupStreamTestAddBatch {
		jobs := make([]*Job, 0, backupStreamTestAddBatch)

		for i := range backupStreamTestAddBatch {
			_, err := rng.Read(raw)
			So(err, ShouldBeNil)

			jobs = append(jobs, &Job{
				Cmd:          fmt.Sprintf("echo %d.%d %s", batch, i, base64.StdEncoding.EncodeToString(raw)),
				Cwd:          defaultUploadDir,
				ReqGroup:     "backup_stream",
				RepGroup:     "backup_stream",
				Requirements: reqs,
			})
		}

		added, existed, err := jq.Add(jobs, os.Environ(), true)
		So(err, ShouldBeNil)
		So(added, ShouldEqual, backupStreamTestAddBatch)
		So(existed, ShouldEqual, 0)
	}
}

// peakHeapGrowthDuring runs fn, returning how far the live heap grew while fn
// ran, at its highest, above its highest in backupStreamTestBaseSamples
// samples before fn. Each sample collects garbage and reads the heap the
// collection found live, so that it counts buffers fn holds, not ones it
// dropped, nor ones it allocated after the collection.
func peakHeapGrowthDuring(fn func()) uint64 {
	var base uint64
	for range backupStreamTestBaseSamples {
		base = max(base, sampleLiveHeap())
	}

	stop := make(chan struct{})
	peakCh := make(chan uint64, 1)

	go func() {
		var peak uint64

		for {
			select {
			case <-stop:
				peakCh <- peak

				return
			default:
			}

			peak = max(peak, sampleLiveHeap())
		}
	}()

	fn()
	close(stop)

	peak := <-peakCh
	if peak <= base {
		return 0
	}

	return peak - base
}

// sampleLiveHeap collects garbage and returns the bytes of heap that
// collection marked live.
func sampleLiveHeap() uint64 {
	runtime.GC()

	sample := []metrics.Sample{{Name: "/gc/heap/live:bytes"}}
	metrics.Read(sample)

	return sample[0].Value.Uint64()
}

func TestBackupStreamStopWithQueuedBackup(t *testing.T) {
	if runnermode || servermode {
		return
	}

	Convey("Given a log store manager with one backup part fetched and a second's chunk request waiting "+
		"for the first to end", t, func() {
		ctx, logs := cmdLogSyncCapture(context.Background())
		config, serverConfig, addr, standardReqs, clientConnectTime := jobqueueTestInit(false)
		serverConfig.storeKind = storeKindLog

		setBackupChunkSize(backupStreamTestSmallChunk)
		setBackupStreamIdleTimeout(backupStreamTestLongIdle)

		server, _, token, err := serve(ctx, serverConfig)
		So(err, ShouldBeNil)

		defer server.Stop(ctx, true)

		jq, err := Connect(addr, config.ManagerCAFile, config.ManagerCertDomain, token, clientConnectTime)
		So(err, ShouldBeNil)

		defer disconnect(jq)

		jq2, err := Connect(addr, config.ManagerCAFile, config.ManagerCertDomain, token, clientConnectTime)
		So(err, ShouldBeNil)

		defer disconnect(jq2)

		backupTmpAddJob(jq, standardReqs)

		backupStreamFetchChunk(jq, backupStreamStart(jq), 0)

		second := backupStreamStart(jq2)
		secondErr := make(chan error, 1)

		go func() {
			_, errr := backupStreamRequest(jq2, second, 0)
			secondErr <- errr
		}()

		So(backupStreamWaitBusy(server, second), ShouldBeTrue)

		Convey("stopping the manager replies to that request that the manager is stopping", func() {
			server.Stop(ctx, true)

			select {
			case errr := <-secondErr:
				So(errr, ShouldNotBeNil)
				So(errr.Error(), ShouldContainSubstring, ErrClosedStop)
			case <-time.After(ClientMinRequestTimeout / 2): // well inside the request's own timeout
				So("the waiting chunk request got no reply", ShouldBeEmpty)
			}

			So(logs.String(), ShouldNotContainSubstring, "timed out waiting for client handling")
		})
	})
}
