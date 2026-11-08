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

// Tests of the log store's incremental backups (spec E4).

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/VertebrateResequencing/wr/internal"
	"github.com/VertebrateResequencing/wr/jobqueue/wal"
	. "github.com/smartystreets/goconvey/convey"
	"github.com/ugorji/go/codec"
)

const (
	// incTestRepGroup is the rep group of the incremental backup tests' jobs.
	incTestRepGroup = "incremental backup"

	// incTestSyncOp is the op a recordingTarget records for a sync.
	incTestSyncOp = "sync"

	// incTestBackupName is the name of the backup beside the tests' db file.
	incTestBackupName = "db_bk"

	// incTestCrashPoints bounds how many crash points E4 test 3 tries: a tick
	// of its store makes a few dozen changes to the backup.
	incTestCrashPoints = 200

	// incTestDevTicks is how many tick intervals E4 test 4 waits through.
	incTestDevTicks = 3

	// incTestDevTickEvery is E4 test 4's tick interval: long enough for the
	// adds between ticks, short enough to wait through quickly.
	incTestDevTickEvery = 20 * time.Millisecond

	// incTestTickEvery is the tick interval of the tests of the ticker.
	incTestTickEvery = 10 * time.Millisecond

	// incTestSlowPut is how long a slowed part put takes: many intervals, so
	// a tick making one takes far longer than the interval.
	incTestSlowPut = 20 * incTestTickEvery

	// incTestTickGaps is how many gaps between slowed ticks a test measures.
	incTestTickGaps = 3

	// incTestTickWait bounds how long a test of the ticker waits for ticks:
	// far longer than the incTestTickGaps+1 slowed ticks it may wait for take,
	// each about 2*incTestSlowPut from the start of one to the next.
	incTestTickWait = 50 * incTestSlowPut

	// incTestCloseWait is how long a test waits to see that a close does not
	// return: far longer than closing a test's small store, final tick
	// included, takes.
	incTestCloseWait = incTestSlowPut

	// incTestShortWrite is how many bytes a recordingTarget writes of a put
	// it fails part-way: a page, far short of any test store's history copy.
	incTestShortWrite = 4096
)

// errIncTestCrash is what a recordingTarget returns at its failAt op, as a
// crash would end the tick there.
var errIncTestCrash = errors.New("crashed")

// errIncTestNoSpace is what a recordingTarget's put of failPartWay returns
// once it has written incTestShortWrite bytes, as a full disk would.
var errIncTestNoSpace = errors.New("no space left on device")

// incTestPaces counts backupPaceHook's calls while a recordingTarget puts.
var incTestPaces atomic.Int64 //nolint:gochecknoglobals // counted by a test hook

// countingWriter counts the bytes written through it, failing with
// errIncTestNoSpace once it has written limit bytes, if limit is not 0.
type countingWriter struct {
	w     io.Writer
	n     int64
	limit int64
}

// Write writes p to w, counting what it wrote, writing only up to limit (if
// set) and failing if p did not fit.
func (c *countingWriter) Write(p []byte) (int, error) {
	full := c.limit > 0 && c.n+int64(len(p)) > c.limit
	if full {
		p = p[:c.limit-c.n]
	}

	n, err := c.w.Write(p)
	c.n += int64(n)

	if err == nil && full {
		err = errIncTestNoSpace
	}

	return n, err
}

// recordingTarget is a backupTarget recording every change made to the
// backup it wraps, and every sync of it, failing the one at index failAt (if
// not -1) and every later one, and any that is failOn (if set), with
// errIncTestCrash without making it, failing a put of failPartWay (if set)
// with errIncTestNoSpace once it has written incTestShortWrite bytes, and
// calling afterPut, if set, after each put it makes.
type recordingTarget struct {
	backupTarget

	mu          sync.Mutex
	ops         []string
	putBytes    map[string]int64
	paces       map[string]int
	failAt      int
	failOn      string
	failPartWay string
	afterPut    func(name string)
}

// newRecordingTarget returns a recordingTarget around target failing from the
// op at index failAt on (none if -1).
func newRecordingTarget(target backupTarget, failAt int) *recordingTarget {
	return &recordingTarget{backupTarget: target, putBytes: make(map[string]int64), paces: make(map[string]int),
		failAt: failAt}
}

// recordBackupOps puts a recordingTarget around s's backup target and returns
// it.
func recordBackupOps(s *logStore) *recordingTarget {
	s.inc.tickMu.Lock()
	defer s.inc.tickMu.Unlock()

	r := newRecordingTarget(s.inc.target, -1)
	s.inc.target = r

	return r
}

// failFromNextOp makes r fail every op after those it has recorded.
func (r *recordingTarget) failFromNextOp() {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.failAt = len(r.ops)
}

// op notes op and returns errIncTestCrash if it is at or after failAt.
func (r *recordingTarget) op(op string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if (r.failAt >= 0 && len(r.ops) >= r.failAt) || (r.failOn != "" && op == r.failOn) {
		return errIncTestCrash
	}

	r.ops = append(r.ops, op)

	return nil
}

// put records "put name", the bytes put and the paces made putting them.
func (r *recordingTarget) put(name string, write func(w io.Writer) error) error {
	if err := r.op("put " + name); err != nil {
		return err
	}

	var n int64

	before := incTestPaces.Load()

	var limit int64

	r.mu.Lock()
	if name == r.failPartWay {
		limit = incTestShortWrite
	}
	r.mu.Unlock()

	err := r.backupTarget.put(name, func(w io.Writer) error {
		cw := &countingWriter{w: w, limit: limit}
		errw := write(cw)
		n = cw.n

		return errw
	})

	r.mu.Lock()
	r.putBytes[name] = n
	r.paces[name] = int(incTestPaces.Load() - before)
	afterPut := r.afterPut
	r.mu.Unlock()

	if err == nil && afterPut != nil {
		afterPut(name)
	}

	return err
}

// remove records "remove name".
func (r *recordingTarget) remove(name string) error {
	if err := r.op("remove " + name); err != nil {
		return err
	}

	return r.backupTarget.remove(name)
}

// sync records "sync".
func (r *recordingTarget) sync() error {
	if err := r.op(incTestSyncOp); err != nil {
		return err
	}

	return r.backupTarget.sync()
}

// recorded returns the ops recorded so far, and clears them.
func (r *recordingTarget) recorded() []string {
	r.mu.Lock()
	defer r.mu.Unlock()

	ops := r.ops
	r.ops = nil

	return ops
}

// partBytes returns the total bytes of the parts put in ops.
func (r *recordingTarget) partBytes(ops []string) int64 {
	r.mu.Lock()
	defer r.mu.Unlock()

	var total int64

	for _, op := range ops {
		name, ok := strings.CutPrefix(op, "put ")
		if _, _, isPart := parseBackupPart(name); ok && isPart {
			total += r.putBytes[name]
		}
	}

	return total
}

func TestLogStoreIncrementalBackup(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given a log store in production deployment that has made a backup tick", t, func() {
		withManualBackupTicks()
		smallWALSegments()

		cfg := incBackupConfig(t, internal.Production)
		s := openIncBackupStore(ctx, cfg)

		first := addIncTestJobs(ctx, s, "inc first", 100)
		So(s.backupTick(), ShouldBeNil)

		Convey("a second tick after 100 archives ships only what is new or changed, no sealed file twice, and "+
			"wal parts totalling exactly the wal bytes made durable since the first", func() {
			rec := recordBackupOps(s)
			sizesAtFirst := walFileSizes(s.dir)
			durFileAtFirst, _ := s.w.Durable()

			archiveIncTestJobs(s, first)
			So(s.flush(), ShouldBeNil)

			So(s.backupTick(), ShouldBeNil)

			ops := rec.recorded()
			sizes := walFileSizes(s.dir)

			var durable int64
			for n, size := range sizes {
				durable += size - sizesAtFirst[n]
			}

			So(durable, ShouldBeGreaterThan, 0)
			So(rec.partBytes(ops), ShouldEqual, durable)
			So(opsNamed(ops, "put", func(name string) bool { return !isPart(name) }), ShouldResemble,
				[]string{historyBoltFile})

			parts := opsNamed(ops, "put", isPart)
			So(len(parts), ShouldEqual, len(slices.Compact(slices.Sorted(slices.Values(parts)))))

			for _, name := range parts {
				n, off, _ := parseBackupPart(name)
				So(n, ShouldBeGreaterThanOrEqualTo, durFileAtFirst)

				if n == durFileAtFirst {
					So(off, ShouldEqual, sizesAtFirst[n])
				}
			}

			Convey("and a third with nothing new ships nothing", func() {
				So(s.backupTick(), ShouldBeNil)
				So(rec.recorded(), ShouldBeEmpty)
			})
		})

		Convey("after a checkpoint deleted old wal files, the next tick deletes them from the backup only after "+
			"the new checkpoint is there", func() {
			So(s.checkpoint(), ShouldBeNil)
			So(s.backupTick(), ShouldBeNil)

			oldCkpt := s.checkpointFrom()
			archiveIncTestJobs(s, first)
			addIncTestJobs(ctx, s, "inc more", 100)
			So(s.checkpoint(), ShouldBeNil)

			newCkpt := filepath.Base(checkpointPath("", s.checkpointFrom()))
			rec := recordBackupOps(s)

			So(s.backupTick(), ShouldBeNil)

			ops := rec.recorded()
			putCkpt := slices.Index(ops, "put "+newCkpt)
			So(putCkpt, ShouldBeGreaterThanOrEqualTo, 0)

			removedParts := 0

			for i, op := range ops {
				name, ok := strings.CutPrefix(op, "remove ")
				if !ok {
					continue
				}

				if isPart(name) {
					removedParts++
				}

				So(i, ShouldBeGreaterThan, putCkpt)
			}

			So(removedParts, ShouldBeGreaterThan, 0)
			So(ops, ShouldContain, "remove "+filepath.Base(checkpointPath("", oldCkpt)))
			So(backupCheckpoints(cfg.dbBkFile), ShouldResemble, []uint32{s.checkpointFrom()})
		})

		Convey("a store closed 10 archives after the last tick leaves a backup holding them complete", func() {
			archiveIncTestJobs(s, first[:10])
			So(s.close(ctx), ShouldBeNil)

			restored := restoredBackup(cfg.dbBkFile)
			So(completeWithCmd(restored, first[:10]), ShouldResemble, jobKeys(first[:10]))
		})
	})

	Convey("Given a log store in production deployment whose backup has shipped one checkpoint and archives", t,
		func() {
			withManualBackupTicks()
			smallWALSegments()

			cfg := incBackupConfig(t, internal.Production)
			s := openIncBackupStore(ctx, cfg)

			archived := addIncTestJobs(ctx, s, "inc crash archived", 50)
			archiveIncTestJobs(s, archived)
			So(s.checkpoint(), ShouldBeNil)
			So(s.backupTick(), ShouldBeNil)

			later := addIncTestJobs(ctx, s, "inc crash later", 100)
			archiveIncTestJobs(s, later[:50])
			So(s.flush(), ShouldBeNil)
			So(s.checkpoint(), ShouldBeNil)

			pre := filepath.Join(t.TempDir(), "pre")
			So(os.CopyFS(pre, os.DirFS(cfg.dbBkFile)), ShouldBeNil)

			Convey("a crash at any point in the next tick leaves a backup that opens as a store whose every "+
				"history entry is complete, holding the 50 it held complete and, live or complete, a prefix "+
				"of the 100 added since in the order they were added", func() {
				crashes := 0

				for k := range incTestCrashPoints {
					dir := filepath.Join(t.TempDir(), fmt.Sprintf("crash%d", k))
					So(os.CopyFS(dir, os.DirFS(pre)), ShouldBeNil)

					retargetBackup(s, newRecordingTarget(dirTarget{path: dir}, k))

					err := s.backupTick()
					if err == nil {
						break
					}

					So(err, ShouldWrap, errIncTestCrash)

					crashes++

					restored := restoredBackup(dir)

					complete, errc := restored.retrieveCompleteJobsByRepGroup(incTestRepGroup)
					So(errc, ShouldBeNil)
					So(len(complete), ShouldBeGreaterThanOrEqualTo, len(archived))
					So(slices.ContainsFunc(complete, func(j *Job) bool { return j.State != JobStateComplete }),
						ShouldBeFalse)
					So(completeWithCmd(restored, archived), ShouldResemble, jobKeys(archived))
					So(heldIn(restored, later), ShouldResemble, heldIn(restored, later[:heldPrefix(restored, later)]))
				}

				So(crashes, ShouldBeGreaterThan, 5)
				So(crashes, ShouldBeLessThan, incTestCrashPoints)
			})
		})

	Convey("Given a log store in production deployment that has made no checkpoint", t, func() {
		withManualBackupTicks()
		smallWALSegments()

		cfg := incBackupConfig(t, internal.Production)
		s := openIncBackupStore(ctx, cfg)
		jobs := addIncTestJobs(ctx, s, "inc no checkpoint", 300)
		So(s.checkpointFrom(), ShouldEqual, 0)

		Convey("a crash at any point in its first tick leaves a backup that either fails to restore, saying "+
			"to move it aside, or restores every job live", func() {
			base := t.TempDir()
			crashes, refused := 0, 0

			for k := range incTestCrashPoints {
				dir := filepath.Join(base, fmt.Sprintf("crash%d", k))
				retargetBackup(s, newRecordingTarget(dirTarget{path: dir}, k))

				err := s.backupTick()

				inspector, closeFiles, erro := openLogBackupFileInspector(dir)

				switch {
				case errors.Is(erro, errNotABackup):
					So(err, ShouldNotBeNil)
					So(erro.Error(), ShouldContainSubstring, "move it aside")

					refused++
				case erro != nil:
					So(erro, ShouldWrap, fs.ErrNotExist)
					So(dir, shouldNotExist)
				default:
					restored, ok := inspector.(*logStore)
					So(ok, ShouldBeTrue)

					live := liveIn(restored, jobs)

					So(closeFiles(), ShouldBeNil)
					So(live, ShouldHaveLength, len(jobs))
				}

				if err == nil {
					break
				}

				So(err, ShouldWrap, errIncTestCrash)

				crashes++
			}

			So(refused, ShouldBeGreaterThan, 1)
			So(crashes, ShouldBeLessThan, incTestCrashPoints)
		})
	})

	Convey("Given a log store in development deployment without forceBackups and a short tick interval", t, func() {
		old := logBackupEvery.Load()
		logBackupEvery.Store(int64(incTestDevTickEvery))

		Reset(func() { logBackupEvery.Store(old) })

		cfg := incBackupConfig(t, internal.Development)
		s := openIncBackupStore(ctx, cfg)

		Convey("nothing is shipped as 3 ticks pass, nor when it closes", func() {
			for i := range incTestDevTicks {
				addIncTestJobs(ctx, s, fmt.Sprintf("inc dev %d", i), 10)
				<-time.After(2 * incTestDevTickEvery)
			}

			So(s.close(ctx), ShouldBeNil)
			So(cfg.dbBkFile, shouldNotExist)
		})

		Convey("a checkpoint deletes the wal files it made redundant, keeping none for a backup", func() {
			addIncTestJobs(ctx, s, "inc dev redundant", 10)
			w, _ := s.w.Durable()
			So(s.checkpoint(), ShouldBeNil)

			addIncTestJobs(ctx, s, "inc dev redundant more", 10)
			So(s.checkpoint(), ShouldBeNil)
			So(s.checkpointFrom(), ShouldBeGreaterThan, w)
			So(walFilePath(s.dir, w), shouldNotExist)
		})
	})

	Convey("Given a log store in development deployment without forceBackups whose backup a test forced", t,
		func() {
			cfg := incBackupConfig(t, internal.Development)
			s := openIncBackupStore(ctx, cfg)

			addIncTestJobs(ctx, s, "inc dev forced early", 10)
			So(s.forceBackup(), ShouldBeNil)

			w, _ := s.w.Durable()
			ten := addIncTestJobs(ctx, s, "inc dev forced", 10)
			So(s.checkpoint(), ShouldBeNil)
			So(s.checkpointFrom(), ShouldBeGreaterThan, w)

			Convey("a checkpoint sealing wal file w before the next forced tick shipped its last 10 adds keeps "+
				"w until that tick, which deletes it, and restoring the backup yields the 10 live", func() {
				So(walFilePath(s.dir, w), shouldExist)

				So(s.forceBackup(), ShouldBeNil)
				So(walFilePath(s.dir, w), shouldNotExist)
				So(liveIn(restoredBackup(cfg.dbBkFile), ten), ShouldResemble, jobKeys(ten))
			})
		})

	Convey("Given a log store in development deployment with forceBackups and an S3 backup", t, func() {
		withManualBackupTicks()

		fake := withFakeS3Objects()

		cfg := incBackupConfig(t, internal.Development)
		cfg.dbBkFile = storeWipeS3Backup
		cfg.forceBackups = true
		s := openIncBackupStore(ctx, cfg)

		addIncTestJobs(ctx, s, "inc s3 dev", 10)
		So(s.checkpoint(), ShouldBeNil)

		Convey("a tick ships every object under <dbBkFile>.development/", func() {
			So(s.backupTick(), ShouldBeNil)

			keys := fake.keys()
			So(keys, ShouldNotBeEmpty)

			for _, key := range keys {
				So(key, ShouldStartWith, storeWipeS3Key+"/")
			}

			So(keys, ShouldContain, storeWipeS3Key+"/"+historyBoltFile)
			So(keys, ShouldContain, storeWipeS3Key+"/"+storeVersionFile)
			So(keys, ShouldContain, storeWipeS3Key+"/"+filepath.Base(checkpointPath("", s.checkpointFrom())))
			So(filepath.Join(s.dir, backupUploadName), shouldNotExist)
		})
	})

	Convey("Given a log store in production deployment with one open wal file", t, func() {
		withManualBackupTicks()

		cfg := incBackupConfig(t, internal.Production)
		s := openIncBackupStore(ctx, cfg)

		added := make([]*Job, 0, 30)

		for i := range 3 {
			added = append(added, addIncTestJobs(ctx, s, fmt.Sprintf("inc parts %d", i), 10)...)
			So(s.backupTick(), ShouldBeNil)
		}

		files, err := walFileNumbers(s.dir)
		So(err, ShouldBeNil)
		So(files, ShouldResemble, []uint32{1})

		Convey("3 ticks with 10 adds before each leave 3 parts of it that tile it from 0, and restoring the "+
			"backup yields all 30 jobs live", func() {
			parts := backupParts(cfg.dbBkFile, 1)
			So(parts, ShouldHaveLength, 3)

			var end int64
			for _, p := range parts {
				So(p.off, ShouldEqual, end)
				end = p.end()
			}

			So(end, ShouldEqual, walFileSizes(s.dir)[1])

			restored := restoredBackup(cfg.dbBkFile)
			So(liveIn(restored, added), ShouldResemble, jobKeys(added))
			So(partFilesIn(restored.dir), ShouldBeEmpty)
		})

		Convey("with the last part deleted, restoring yields the first 20 live and no error", func() {
			parts := backupParts(cfg.dbBkFile, 1)
			So(os.Remove(filepath.Join(cfg.dbBkFile, parts[len(parts)-1].name)), ShouldBeNil)

			So(liveIn(restoredBackup(cfg.dbBkFile), added), ShouldResemble, jobKeys(added[:20]))
		})

		Convey("with the middle part deleted, restoring stops at the gap, yielding the first 10 live and no "+
			"error", func() {
			parts := backupParts(cfg.dbBkFile, 1)
			So(os.Remove(filepath.Join(cfg.dbBkFile, parts[1].name)), ShouldBeNil)

			So(liveIn(restoredBackup(cfg.dbBkFile), added), ShouldResemble, jobKeys(added[:10]))
		})

		Convey("with the first two parts replaced by a whole wal file holding them, as a copy of a store "+
			"directory holds, restoring yields all 30 live", func() {
			parts := backupParts(cfg.dbBkFile, 1)

			var whole []byte

			for _, p := range parts[:2] {
				data, err := os.ReadFile(filepath.Join(cfg.dbBkFile, p.name))
				So(err, ShouldBeNil)
				So(os.Remove(filepath.Join(cfg.dbBkFile, p.name)), ShouldBeNil)

				whole = append(whole, data...)
			}

			So(os.WriteFile(walFilePath(cfg.dbBkFile, 1), whole, dbFilePermission), ShouldBeNil)

			So(liveIn(restoredBackup(cfg.dbBkFile), added), ShouldResemble, jobKeys(added))

			Convey("and the first part also left at offset 0, restoring starts from the whole file, which "+
				"that part then overlaps, yielding the first 20 live", func() {
				So(os.WriteFile(filepath.Join(cfg.dbBkFile, parts[0].name), whole[:parts[0].size],
					dbFilePermission), ShouldBeNil)

				So(liveIn(restoredBackup(cfg.dbBkFile), added), ShouldResemble, jobKeys(added[:20]))
			})
		})
	})

	Convey("Given a log store in production deployment that shipped 2 parts of its open wal file to an S3 "+
		"backup, then closed and reopened", t, func() {
		withManualBackupTicks()

		fake := withFakeS3Objects()

		cfg := incBackupConfig(t, internal.Production)
		cfg.dbBkFile = storeWipeS3Backup

		s := openIncBackupStore(ctx, cfg)
		added := addIncTestJobs(ctx, s, "inc s3 parts 1", 10)
		So(s.backupTick(), ShouldBeNil)
		added = append(added, addIncTestJobs(ctx, s, "inc s3 parts 2", 10)...)
		So(s.backupTick(), ShouldBeNil)
		So(s.close(ctx), ShouldBeNil)

		parts := slices.DeleteFunc(fake.keys(), func(key string) bool { return !isPart(path.Base(key)) })
		So(len(parts), ShouldBeGreaterThanOrEqualTo, 2)

		reopened := openIncBackupStore(ctx, cfg)
		added = append(added, addIncTestJobs(ctx, reopened, "inc s3 parts 3", 10)...)

		Convey("its first tick keeps every part it shipped before, and a store restored from the backup "+
			"holds all 30 jobs live", func() {
			So(reopened.backupTick(), ShouldBeNil)

			keys := fake.keys()
			for _, p := range parts {
				So(keys, ShouldContain, p)
			}

			So(reopened.close(ctx), ShouldBeNil)
			So(os.RemoveAll(cfg.dbFile), ShouldBeNil)

			So(liveIn(openIncBackupStore(ctx, cfg), added), ShouldResemble, jobKeys(added))
		})
	})

	Convey("Given a log store in production deployment whose backup holds a leftover .tmp file and a part "+
		"after a gap in its open wal file's parts", t, func() {
		withManualBackupTicks()

		cfg := incBackupConfig(t, internal.Production)
		s := openIncBackupStore(ctx, cfg)

		added := addIncTestJobs(ctx, s, "inc gap 0", 10)
		So(s.backupTick(), ShouldBeNil)

		tmp := filepath.Join(cfg.dbBkFile, "x"+storeTmpExt)
		stale := filepath.Join(cfg.dbBkFile, backupPartName(1, walFileSizes(s.dir)[1]+1))

		for _, path := range []string{tmp, stale} {
			So(os.WriteFile(path, []byte("left over"), dbFilePermission), ShouldBeNil)
		}

		retargetBackup(s, dirTarget{path: cfg.dbBkFile})

		Convey("the next ticks delete both, and restoring the backup yields every job live", func() {
			for i := 1; i <= 2; i++ {
				added = append(added, addIncTestJobs(ctx, s, fmt.Sprintf("inc gap %d", i), 10)...)
				So(s.backupTick(), ShouldBeNil)
			}

			So(tmp, shouldNotExist)
			So(stale, shouldNotExist)
			So(liveIn(restoredBackup(cfg.dbBkFile), added), ShouldResemble, jobKeys(added))
		})
	})

	Convey("Given a log store in production deployment whose backup holds a checkpoint and folded archives", t,
		func() {
			withManualBackupTicks()
			smallWALSegments()

			cfg := incBackupConfig(t, internal.Production)
			s := openIncBackupStore(ctx, cfg)

			twenty := addIncTestJobs(ctx, s, "inc twenty", 20)
			before := addIncTestJobs(ctx, s, "inc before", 20)
			archiveIncTestJobs(s, before)

			durFile, _ := s.w.Durable()
			addUntilFoldedPast(ctx, s, durFile)

			twentyFiles := jobWALFiles(s, twenty)
			So(twentyFiles, ShouldNotBeEmpty)

			So(s.checkpoint(), ShouldBeNil)
			So(s.backupTick(), ShouldBeNil)

			c1 := s.checkpointFrom()
			folded1 := backupHistFolded(t, cfg.dbBkFile)

			for _, n := range twentyFiles {
				So(n, ShouldBeLessThan, min(c1, folded1.file))
				So(backupParts(cfg.dbBkFile, n), ShouldBeEmpty)
			}

			// the next tick ships history.bolt, folded past these but not the
			// 20 jobs' completions
			resume := pauseLogFolder()
			others := addIncTestJobs(ctx, s, "inc others", 5)
			archiveIncTestJobs(s, others)
			So(s.foldNow(), ShouldBeNil)
			archiveIncTestJobs(s, twenty)

			Convey("a fold and a checkpoint past the 20 jobs' completions while a tick ships, between its "+
				"history and checkpoint steps, leave a backup holding them complete, before and after the "+
				"next tick", func() {
				rec := recordBackupOps(s)

				var hookErr error

				rec.afterPut = func(name string) {
					if name != historyBoltFile {
						return
					}

					rec.afterPut = nil
					hookErr = errors.Join(s.foldNow(), s.checkpoint())
				}

				So(s.backupTick(), ShouldBeNil)
				So(hookErr, ShouldBeNil)
				So(s.checkpointFrom(), ShouldBeGreaterThan, c1)

				soBackupHoldsOneCheckpoint(t, cfg.dbBkFile)
				So(completeWithCmd(restoredBackup(cfg.dbBkFile), slices.Concat(twenty, before, others)),
					ShouldResemble, jobKeys(slices.Concat(twenty, before, others)))

				resume()
				So(s.backupTick(), ShouldBeNil)

				soBackupHoldsOneCheckpoint(t, cfg.dbBkFile)
				So(backupCheckpoints(cfg.dbBkFile), ShouldResemble, []uint32{s.checkpointFrom()})
				So(completeWithCmd(restoredBackup(cfg.dbBkFile), slices.Concat(twenty, before, others)),
					ShouldResemble, jobKeys(slices.Concat(twenty, before, others)))
			})
		})

	Convey("Given a log store in production deployment with a page-sized backup pace", t, func() {
		withManualBackupTicks()
		pauseLogFolder()

		origBytes, origHook := backupCopySyncBytes, backupPaceHook
		backupCopySyncBytes = int64(os.Getpagesize())
		backupPaceHook = func() { incTestPaces.Add(1) }

		Reset(func() { backupCopySyncBytes, backupPaceHook = origBytes, origHook })

		cfg := incBackupConfig(t, internal.Production)
		s := openIncBackupStore(ctx, cfg)

		archiveIncTestJobs(s, addIncTestJobs(ctx, s, "inc pace", 200))
		So(s.foldNow(), ShouldBeNil)

		Convey("a tick paces its history.bolt copy once a page, and a second with no fold between copies none",
			func() {
				rec := recordBackupOps(s)

				So(s.backupTick(), ShouldBeNil)

				n := rec.putBytes[historyBoltFile]
				So(n, ShouldBeGreaterThan, 2*os.Getpagesize())
				So(rec.paces[historyBoltFile], ShouldBeGreaterThanOrEqualTo, int(n)/os.Getpagesize()-1)

				rec.recorded()
				So(s.backupTick(), ShouldBeNil)
				So(opsNamed(rec.recorded(), "put", func(name string) bool { return name == historyBoltFile }),
					ShouldBeEmpty)
			})
	})

	Convey("Given a log store in production deployment whose backup holds history.bolt, and whose newer "+
		"history.bolt a tick will copy with a page-sized pace while archives wait to be folded", t, func() {
		withManualBackupTicks()
		pauseLogFolder()

		cfg := incBackupConfig(t, internal.Production)
		s := openIncBackupStore(ctx, cfg)

		jobs := addIncTestJobs(ctx, s, "inc fold during copy", 200)
		archiveIncTestJobs(s, jobs[:50])
		So(s.foldNow(), ShouldBeNil)
		So(s.backupTick(), ShouldBeNil)

		archiveIncTestJobs(s, jobs[50:100])
		So(s.foldNow(), ShouldBeNil)
		archiveIncTestJobs(s, jobs[100:150])

		origBytes, origHook := backupCopySyncBytes, backupPaceHook
		backupCopySyncBytes = int64(os.Getpagesize())

		Reset(func() { backupCopySyncBytes, backupPaceHook = origBytes, origHook })

		Convey("a fold and a checkpoint during that copy are not credited to it: the tick records the "+
			"position the backup's history.bolt holds, and after the next tick the backup restores with "+
			"every job held and the 150 archived complete", func() {
			var (
				once    sync.Once
				hookErr error
			)

			backupPaceHook = func() {
				once.Do(func() { hookErr = errors.Join(s.foldNow(), s.checkpoint()) })
			}

			So(s.backupTick(), ShouldBeNil)
			So(hookErr, ShouldBeNil)
			So(s.inc.histFolded, ShouldResemble, backupHistFolded(t, cfg.dbBkFile))

			backupPaceHook = origHook

			So(s.backupTick(), ShouldBeNil)

			restored := restoredBackup(cfg.dbBkFile)
			So(heldIn(restored, jobs), ShouldHaveLength, len(jobs))
			So(completeWithCmd(restored, jobs[:150]), ShouldResemble, jobKeys(jobs[:150]))
		})
	})

	Convey("Given a log store in production deployment whose backup holds history.bolt, and newer folds", t,
		func() {
			withManualBackupTicks()
			pauseLogFolder()

			cfg := incBackupConfig(t, internal.Production)
			s := openIncBackupStore(ctx, cfg)

			jobs := addIncTestJobs(ctx, s, "inc history copy fails", 100)
			archiveIncTestJobs(s, jobs[:50])
			So(s.foldNow(), ShouldBeNil)
			So(s.backupTick(), ShouldBeNil)

			backupHistory := filepath.Join(cfg.dbBkFile, historyBoltFile)
			before := sha256Of(backupHistory)

			archiveIncTestJobs(s, jobs[50:])
			So(s.foldNow(), ShouldBeNil)

			Convey("a tick whose history.bolt copy fails part-way, as a full disk fails it, fails and leaves "+
				"the backup's earlier whole copy, with no .tmp beside it", func() {
				rec := recordBackupOps(s)
				rec.failPartWay = historyBoltFile

				err := s.backupTick()
				So(err, ShouldNotBeNil)
				So(err.Error(), ShouldContainSubstring, errIncTestNoSpace.Error())
				So(sha256Of(backupHistory), ShouldEqual, before)
				So(backupHistory+storeTmpExt, shouldNotExist)
			})
		})

	Convey("Given a log store in production deployment with a checkpoint no tick has shipped", t, func() {
		withManualBackupTicks()
		pauseLogFolder()

		cfg := incBackupConfig(t, internal.Production)
		s := openIncBackupStore(ctx, cfg)

		addIncTestJobs(ctx, s, "inc held", 10)
		So(s.backupTick(), ShouldBeNil)

		archiveIncTestJobs(s, addIncTestJobs(ctx, s, "inc held archived", 10))
		So(s.checkpoint(), ShouldBeNil)

		held := s.checkpointFrom()

		Convey("a checkpoint made while the next tick ships, which would delete it, leaves it for the tick to "+
			"ship", func() {
			rec := recordBackupOps(s)

			var hookErr error

			rec.afterPut = func(name string) {
				if name != historyBoltFile {
					return
				}

				rec.afterPut = nil
				hookErr = errors.Join(s.foldNow(), s.checkpoint())
			}

			So(s.backupTick(), ShouldBeNil)
			So(hookErr, ShouldBeNil)
			So(s.checkpointFrom(), ShouldBeGreaterThan, held)
			So(backupCheckpoints(cfg.dbBkFile), ShouldResemble, []uint32{held})
			So(checkpointPath(s.dir, held), shouldNotExist)
		})
	})

	Convey("Given a log store in production deployment and a checkpoint sealing wal file w before a tick "+
		"shipped its last 10 adds", t, func() {
		withManualBackupTicks()

		cfg := incBackupConfig(t, internal.Production)
		s := openIncBackupStore(ctx, cfg)

		addIncTestJobs(ctx, s, "inc sealed early", 10)
		So(s.backupTick(), ShouldBeNil)

		w, _ := s.w.Durable()
		ten := addIncTestJobs(ctx, s, "inc sealed", 10)
		So(s.checkpoint(), ShouldBeNil)
		So(s.checkpointFrom(), ShouldBeGreaterThan, w)

		Convey("w is kept until the next tick, which deletes it, and restoring the backup yields the 10 live",
			func() {
				So(walFilePath(s.dir, w), shouldExist)

				So(s.backupTick(), ShouldBeNil)
				So(walFilePath(s.dir, w), shouldNotExist)
				So(liveIn(restoredBackup(cfg.dbBkFile), ten), ShouldResemble, jobKeys(ten))
			})
	})

	Convey("Given a log store in production deployment closed as a crash would leave it, after a checkpoint "+
		"sealed wal file w before a tick shipped its last 10 adds", t, func() {
		withManualBackupTicks()

		cfg := incBackupConfig(t, internal.Production)
		s, err := openLogStoreLocked(ctx, cfg)
		So(err, ShouldBeNil)

		addIncTestJobs(ctx, s, "inc sealed early", 10)
		So(s.backupTick(), ShouldBeNil)

		w, _ := s.w.Durable()
		ten := addIncTestJobs(ctx, s, "inc sealed", 10)
		So(s.checkpoint(), ShouldBeNil)
		So(s.checkpointFrom(), ShouldBeGreaterThan, w)

		closeAsCrashed(ctx, s)

		Convey("w is still there once it reopens, until its first tick deletes it, and restoring the backup "+
			"yields the 10 live", func() {
			reopened := openIncBackupStore(ctx, cfg)
			So(walFilePath(cfg.dbFile, w), shouldExist)

			So(reopened.backupTick(), ShouldBeNil)
			So(walFilePath(cfg.dbFile, w), shouldNotExist)
			So(liveIn(restoredBackup(cfg.dbBkFile), ten), ShouldResemble, jobKeys(ten))
		})
	})

	Convey("Given a log store in production deployment whose tick shipped a new checkpoint and crashed before "+
		"deleting anything", t, func() {
		withManualBackupTicks()
		smallWALSegments()

		cfg := incBackupConfig(t, internal.Production)
		s, err := openLogStoreLocked(ctx, cfg)
		So(err, ShouldBeNil)

		first := addIncTestJobs(ctx, s, "inc need", 100)
		So(s.checkpoint(), ShouldBeNil)
		So(s.backupTick(), ShouldBeNil)

		archiveIncTestJobs(s, first)
		durFile, _ := s.w.Durable()
		addUntilFoldedPast(ctx, s, durFile)
		So(s.checkpoint(), ShouldBeNil)

		ckpt := filepath.Base(checkpointPath("", s.checkpointFrom()))
		rec := recordBackupOps(s)
		rec.afterPut = func(name string) {
			if name == ckpt {
				rec.failFromNextOp()
			}
		}

		So(s.backupTick(), ShouldWrap, errIncTestCrash)
		So(backupCheckpoints(cfg.dbBkFile), ShouldContain, s.checkpointFrom())
		So(s.close(ctx), ShouldBeNil)

		Convey("its first tick after it reopens deletes the local wal files below both the backup's "+
			"checkpoint's walFrom and its history's folded position's file", func() {
			reopened := openIncBackupStore(ctx, cfg)

			files, errw := walFileNumbers(cfg.dbFile)
			So(errw, ShouldBeNil)
			So(files[0], ShouldBeLessThan, slices.Max(backupCheckpoints(cfg.dbBkFile)))

			So(reopened.backupTick(), ShouldBeNil)

			need := min(slices.Max(backupCheckpoints(cfg.dbBkFile)), backupHistFolded(t, cfg.dbBkFile).file)
			So(need, ShouldBeGreaterThan, files[0])

			files, errw = walFileNumbers(cfg.dbFile)
			So(errw, ShouldBeNil)
			So(files[0], ShouldBeGreaterThanOrEqualTo, need)
		})
	})
}

// withManualBackupTicks makes periodic ticks an hour apart until the Convey
// block ends, so that a test makes every tick itself.
func withManualBackupTicks() {
	old := logBackupEvery.Load()
	logBackupEvery.Store(int64(time.Hour))

	Reset(func() { logBackupEvery.Store(old) })
}

// incBackupConfig returns the config of a log store at "queue.db" in a new
// temporary directory of deployment, its backup at "db_bk" beside it.
func incBackupConfig(t *testing.T, deployment string) storeConfig {
	t.Helper()

	dir := t.TempDir()

	return storeConfig{
		dbFile:     filepath.Join(dir, "queue.db"),
		dbBkFile:   filepath.Join(dir, incTestBackupName),
		deployment: deployment,
		kind:       storeKindLog,
	}
}

// openIncBackupStore opens the log store cfg names, under its lock, closed
// when the Convey block ends.
func openIncBackupStore(ctx context.Context, cfg storeConfig) *logStore {
	s, err := openLogStoreLocked(ctx, cfg)
	So(err, ShouldBeNil)

	Reset(func() { So(s.close(ctx), ShouldBeNil) })

	return s
}

// addIncTestJobs adds n jobs named for prefix to s and returns them.
func addIncTestJobs(ctx context.Context, s *logStore, prefix string, n int) []*Job {
	jobs := testLogJobs(prefix, incTestRepGroup, n)

	_, err := s.storeNewJobsGuarded(ctx, jobs, false, nil)
	So(err, ShouldBeNil)

	return jobs
}

// walFileSizes returns the size of each wal file in dir, by number.
func walFileSizes(dir string) map[uint32]int64 {
	files, err := walFileNumbers(dir)
	So(err, ShouldBeNil)

	sizes := make(map[uint32]int64, len(files))

	for _, n := range files {
		info, errs := os.Stat(walFilePath(dir, n))
		So(errs, ShouldBeNil)

		sizes[n] = info.Size()
	}

	return sizes
}

// archiveIncTestJobs archives each of jobs in s.
func archiveIncTestJobs(s *logStore, jobs []*Job) {
	errs := make([]error, 0, len(jobs))

	for _, job := range jobs {
		errs = append(errs, archiveBackupTestJob(s, job))
	}

	So(errors.Join(errs...), ShouldBeNil)
}

// opsNamed returns the ops of kind ("put" or "remove") on names matching
// keep.
func opsNamed(ops []string, kind string, keep func(name string) bool) []string {
	var out []string

	for _, op := range ops {
		if name, ok := strings.CutPrefix(op, kind+" "); ok && keep(name) {
			out = append(out, name)
		}
	}

	return out
}

// isPart reports whether name is a wal part's.
func isPart(name string) bool {
	_, _, ok := parseBackupPart(name)

	return ok
}

// backupCheckpoints returns the numbers of the checkpoints in the backup
// directory dir.
func backupCheckpoints(dir string) []uint32 {
	var ckpts []uint32

	for _, name := range backupNames(dir) {
		if n, ok := checkpointNumber(name); ok {
			ckpts = append(ckpts, n)
		}
	}

	return ckpts
}

// backupNames returns the names of the files in the backup directory dir.
func backupNames(dir string) []string {
	entries, err := os.ReadDir(dir)
	So(err, ShouldBeNil)

	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name())
	}

	return names
}

// restoredBackup restores the backup at path into a store in a new temporary
// directory, as E2 restores a missing db file from it, closed when the Convey
// block ends.
func restoredBackup(path string) *logStore {
	inspector, closeFiles, err := openLogBackupFileInspector(path)
	So(err, ShouldBeNil)

	Reset(func() { So(closeFiles(), ShouldBeNil) })

	restored, ok := inspector.(*logStore)
	So(ok, ShouldBeTrue)

	return restored
}

// completeWithCmd returns the keys of those of jobs s holds complete with the
// Cmd they were added with.
func completeWithCmd(s *logStore, jobs []*Job) []string {
	var keys []string

	for _, job := range jobs {
		complete, found, err := s.completeJob(job.Key())
		So(err, ShouldBeNil)

		if found && complete.State == JobStateComplete && complete.Cmd == job.Cmd {
			keys = append(keys, job.Key())
		}
	}

	return keys
}

// retargetBackup makes s's next tick back up to target as a new store's first
// tick would, reading what it holds afresh.
func retargetBackup(s *logStore, target backupTarget) {
	s.inc.tickMu.Lock()
	defer s.inc.tickMu.Unlock()

	s.inc.target = target
	s.inc.prepared, s.inc.histShipped = false, false
}

// heldIn returns the keys, sorted, of those of jobs live or complete in s.
func heldIn(s *logStore, jobs []*Job) []string {
	keys := slices.Concat(liveIn(s, jobs), completeWithCmd(s, jobs))
	slices.Sort(keys)

	return slices.Compact(keys)
}

// liveIn returns the keys of those of jobs live in s.
func liveIn(s *logStore, jobs []*Job) []string {
	var keys []string

	for _, job := range jobs {
		live, err := s.checkIfLive(job.Key())
		So(err, ShouldBeNil)

		if live {
			keys = append(keys, job.Key())
		}
	}

	return keys
}

// heldPrefix returns how many of jobs, from the first, s holds live or
// complete before the first it holds neither.
func heldPrefix(s *logStore, jobs []*Job) int {
	for i, job := range jobs {
		if len(heldIn(s, []*Job{job})) == 0 {
			return i
		}
	}

	return len(jobs)
}

// backupParts returns the parts of wal file n in the backup directory dir,
// sorted by offset.
func backupParts(dir string, n uint32) []backupPart {
	var parts []backupPart

	for _, name := range backupNames(dir) {
		file, off, ok := parseBackupPart(name)
		if !ok || file != n {
			continue
		}

		info, err := os.Stat(filepath.Join(dir, name))
		So(err, ShouldBeNil)

		parts = append(parts, backupPart{name: name, off: off, size: info.Size()})
	}

	return sortedParts(parts)
}

// partFilesIn returns the names of the wal parts in dir that are not whole
// wal files.
func partFilesIn(dir string) []string {
	var parts []string

	for _, name := range backupNames(dir) {
		if isPart(name) && !strings.HasSuffix(name, walFileExt) {
			parts = append(parts, name)
		}
	}

	return parts
}

// jobWALFiles returns the wal files holding the spec records of jobs live in
// s.
func jobWALFiles(s *logStore, jobs []*Job) []uint32 {
	s.mu.RLock()
	defer s.mu.RUnlock()

	files := make(map[uint32]bool)

	for _, job := range jobs {
		k, err := parseLogKey(job.Key())
		So(err, ShouldBeNil)

		if e := s.state.live[k]; e != nil && e.spec.File&checkpointRefFlag == 0 {
			files[e.spec.File] = true
		}
	}

	return slices.Sorted(maps.Keys(files))
}

// backupHistFolded returns the folded position of the history.bolt in the
// backup directory dir, read from a copy of it, so the backup is unchanged.
func backupHistFolded(t *testing.T, dir string) historyPos {
	t.Helper()

	copied := t.TempDir()
	copyTestFile(filepath.Join(dir, historyBoltFile), filepath.Join(copied, historyBoltFile))

	h, err := openHistoryBolt(copied, new(codec.BincHandle))
	So(err, ShouldBeNil)

	folded, err := h.foldedPos()
	So(err, ShouldBeNil)
	So(h.close(), ShouldBeNil)

	return folded
}

// soBackupHoldsOneCheckpoint asserts that the backup directory dir holds one
// checkpoint, whose histFrom is at or below its history's folded position.
func soBackupHoldsOneCheckpoint(t *testing.T, dir string) {
	t.Helper()

	ckpts := backupCheckpoints(dir)
	So(ckpts, ShouldHaveLength, 1)
	So(checkpointHistFrom(dir, ckpts[0]).after(backupHistFolded(t, dir)), ShouldBeFalse)
}

// checkpointHistFrom returns the histFrom of checkpoint n in dir.
func checkpointHistFrom(dir string, n uint32) historyPos {
	header, _, f, err := openCheckpoint(dir, n)
	So(err, ShouldBeNil)
	So(f.Close(), ShouldBeNil)

	return header.histFrom
}

func TestLogStoreIncrementalBackupOfConverted(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given a store converted from bbolt in production deployment and a bbolt file at db_bk", t, func() {
		withManualBackupTicks()

		boltFile, snapshot := boltRestoreBackup(ctx, t)

		logPath, logCtx := managerLogContext(t, ctx)
		cfg := incBackupConfig(t, internal.Production)
		copyTestFile(boltFile, cfg.dbFile)
		copyTestFile(boltFile, cfg.dbBkFile)

		boltSum := sha256Of(cfg.dbBkFile)
		pre := cfg.dbBkFile + storeBoltPreInfix + convertVersion()

		s := openIncBackupStore(logCtx, cfg)
		So(s.upgradedOnOpen(), ShouldBeTrue)

		Convey("its checkpoint's histFrom is at convertWalFrom's start, and the first tick moves the bbolt "+
			"file aside and makes db_bk a directory that opens as the store", func() {
			So(checkpointHistFrom(s.dir, convertWalFrom), ShouldResemble, historyPos{file: convertWalFrom})

			So(s.backupTick(), ShouldBeNil)

			So(sha256Of(pre), ShouldEqual, boltSum)

			info, err := os.Stat(cfg.dbBkFile)
			So(err, ShouldBeNil)
			So(info.IsDir(), ShouldBeTrue)
			So(backupCheckpoints(cfg.dbBkFile), ShouldResemble, []uint32{convertWalFrom})
			So(snapshotOf(restoredBackup(cfg.dbBkFile)), ShouldResemble, snapshot)
		})

		Convey("with db_bk.bolt-pre-dev already there, 2 ticks within a minute ship nothing, change neither "+
			"file, and log one error naming it", func() {
			So(os.WriteFile(pre, []byte("an earlier conversion's"), dbFilePermission), ShouldBeNil)
			preSum := sha256Of(pre)

			s.periodicBackupTick()
			s.periodicBackupTick()

			So(sha256Of(cfg.dbBkFile), ShouldEqual, boltSum)
			So(sha256Of(pre), ShouldEqual, preSum)

			lines := logLinesContaining(readLogFile(t, logPath), backupFailedLogMsg)
			So(lines, ShouldHaveLength, 1)
			So(lines[0], ShouldContainSubstring, "lvl=eror")
			So(lines[0], ShouldContainSubstring, pre)
			So(lines[0], ShouldContainSubstring, errBoltPreExists.Error())

			Convey("and a close within that minute logs its final tick's failure too", func() {
				So(s.close(logCtx), ShouldBeNil)

				lines = logLinesContaining(readLogFile(t, logPath), backupFailedLogMsg)
				So(lines, ShouldHaveLength, 2)
				So(lines[1], ShouldContainSubstring, errBoltPreExists.Error())
			})

			Convey("and a third tick more than a minute after the logged failure logs it again", func() {
				s.inc.logMu.Lock()
				s.inc.errTime = s.inc.errTime.Add(-backupFailedLogEvery - time.Second)
				s.inc.logMu.Unlock()

				s.periodicBackupTick()

				lines = logLinesContaining(readLogFile(t, logPath), backupFailedLogMsg)
				So(lines, ShouldHaveLength, 2)
				So(lines[1], ShouldContainSubstring, "lvl=eror")
				So(lines[1], ShouldContainSubstring, errBoltPreExists.Error())
			})
		})
	})

	Convey("Given a log store in production deployment and a regular file that is not bbolt at a local db_bk",
		t, func() {
			withManualBackupTicks()

			const notBolt = "not a bbolt file"

			cfg := incBackupConfig(t, internal.Production)
			s := openIncBackupStore(ctx, cfg)
			So(os.WriteFile(cfg.dbBkFile, []byte(notBolt), dbFilePermission), ShouldBeNil)
			addIncTestJobs(ctx, s, "inc not bolt", 5)

			Convey("a tick fails wrapping errNotABackup, leaves the file in place and ships nothing", func() {
				So(s.backupTick(), ShouldWrap, errNotABackup)
				So(cfg.dbBkFile+storeBoltPreInfix+convertVersion(), shouldNotExist)

				got, err := os.ReadFile(cfg.dbBkFile)
				So(err, ShouldBeNil)
				So(string(got), ShouldEqual, notBolt)
			})
		})

	Convey("Given a log store in production deployment and an object that is not bbolt at an S3 db_bk", t,
		func() {
			withManualBackupTicks()

			fake := withFakeS3Objects()

			cfg := incBackupConfig(t, internal.Production)
			cfg.dbBkFile = storeWipeS3Backup
			s := openIncBackupStore(ctx, cfg)

			// put after the open, whose restore (E2) would refuse it first
			fake.putData(storeWipeS3ProdKey, []byte("not a bbolt file"))
			addIncTestJobs(ctx, s, "inc s3 not bolt", 5)

			Convey("a tick fails wrapping errNotABackup, leaves the object in place and ships nothing", func() {
				So(s.backupTick(), ShouldWrap, errNotABackup)
				So(fake.keys(), ShouldResemble, []string{storeWipeS3ProdKey})
			})
		})

	Convey("Given a store converted from bbolt in production deployment and no backup", t, func() {
		withManualBackupTicks()

		boltFile, snapshot := boltRestoreBackup(ctx, t)

		cfg := incBackupConfig(t, internal.Production)
		copyTestFile(boltFile, cfg.dbFile)

		s := openIncBackupStore(ctx, cfg)
		So(s.upgradedOnOpen(), ShouldBeTrue)

		Convey("a crash at any point in its first tick leaves a backup that either fails to restore or "+
			"restores to the store", func() {
			crashes := 0
			base := t.TempDir()

			for k := range incTestCrashPoints {
				dir := filepath.Join(base, fmt.Sprintf("crash%d", k))
				retargetBackup(s, newRecordingTarget(dirTarget{path: dir}, k))

				err := s.backupTick()
				if err == nil {
					break
				}

				So(err, ShouldWrap, errIncTestCrash)

				crashes++

				inspector, closeFiles, err := openLogBackupFileInspector(dir)
				if err != nil {
					continue
				}

				restored, ok := inspector.(*logStore)
				So(ok, ShouldBeTrue)

				got := snapshotOf(restored)

				So(closeFiles(), ShouldBeNil)
				So(got, ShouldResemble, snapshot)
			}

			So(crashes, ShouldBeGreaterThan, 3)
			So(crashes, ShouldBeLessThan, incTestCrashPoints)
			So(snapshotOf(restoredBackup(filepath.Join(base, fmt.Sprintf("crash%d", crashes)))),
				ShouldResemble, snapshot)
		})
	})

	Convey("Given a store converted from bbolt in production deployment and a bbolt object at an S3 db_bk", t,
		func() {
			withManualBackupTicks()

			fake := withFakeS3Objects()
			boltFile, snapshot := boltRestoreBackup(ctx, t)

			cfg := incBackupConfig(t, internal.Production)
			cfg.dbBkFile = storeWipeS3Backup
			copyTestFile(boltFile, cfg.dbFile)
			putFileObject(fake, boltFile, storeWipeS3ProdKey)

			s := openIncBackupStore(ctx, cfg)

			Convey("the first tick copies the object to db_bk.bolt-pre-dev and ships the store under db_bk/",
				func() {
					So(s.backupTick(), ShouldBeNil)

					preKey := storeWipeS3ProdKey + storeBoltPreInfix + convertVersion()
					keys := fake.keys()
					So(keys, ShouldContain, preKey)
					So(keys, ShouldNotContain, storeWipeS3ProdKey)

					preFile := filepath.Join(t.TempDir(), "pre")
					So(fake.DownloadFile(preKey, preFile), ShouldBeNil)
					So(sha256Of(preFile), ShouldEqual, sha256Of(boltFile))

					target, err := backupTargetAt(storeWipeS3Backup, internal.Production)
					So(err, ShouldBeNil)

					dir := filepath.Join(t.TempDir(), "restored")
					So(restoreLogStore(ctx, dir, target), ShouldBeNil)

					restored := openLogStoreIn(ctx, dir)

					Reset(func() { So(restored.close(ctx), ShouldBeNil) })

					So(snapshotOf(restored), ShouldResemble, snapshot)
				})

			preKey := storeWipeS3ProdKey + storeBoltPreInfix + convertVersion()

			Convey("with db_bk.bolt-pre-dev already a copy of it, as a crash between that copy and the delete "+
				"leaves, the first tick finishes the move and ships the store under db_bk/", func() {
				putFileObject(fake, boltFile, preKey)

				So(s.backupTick(), ShouldBeNil)

				keys := fake.keys()
				So(keys, ShouldContain, preKey)
				So(keys, ShouldNotContain, storeWipeS3ProdKey)
				So(keys, ShouldContain, storeWipeS3ProdKey+"/"+storeVersionFile)
			})

			Convey("with a db_bk.bolt-pre-dev of the same size but other bytes already there, the tick fails "+
				"naming it and changes neither object", func() {
				data, err := os.ReadFile(boltFile)
				So(err, ShouldBeNil)

				data[len(data)/2] ^= 0xff
				fake.putData(preKey, data)

				err = s.backupTick()
				So(err, ShouldWrap, errBoltPreExists)
				So(err.Error(), ShouldContainSubstring, preKey)
				So(fake.keys(), ShouldResemble, []string{storeWipeS3ProdKey, preKey})
			})

			Convey("with the upload of db_bk.bolt-pre-dev failing, the tick fails with that error and leaves "+
				"the bbolt object at db_bk", func() {
				fake.failUploading(errIncTestNoSpace, preKey)

				So(s.backupTick(), ShouldWrap, errIncTestNoSpace)
				So(fake.keys(), ShouldResemble, []string{storeWipeS3ProdKey})
			})
		})
}

func TestLogStoreIncrementalBackupAfterRecoveryCut(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given a closed log store in production deployment whose last wal file n's parts were shipped", t,
		func() {
			withManualBackupTicks()
			smallWALSegments()

			cfg := incBackupConfig(t, internal.Production)
			s, err := openLogStoreLocked(ctx, cfg)
			So(err, ShouldBeNil)

			jobs := addIncTestJobs(ctx, s, "inc cut", 2000)
			So(s.backupTick(), ShouldBeNil)
			addIncTestJobs(ctx, s, "inc cut more", 5)
			So(s.close(ctx), ShouldBeNil)

			files, err := walFileNumbers(cfg.dbFile)
			So(err, ShouldBeNil)
			So(len(files), ShouldBeGreaterThan, 1)

			n := files[len(files)-1]
			So(backupParts(cfg.dbBkFile, n), ShouldNotBeEmpty)

			reopenAndShip := func() *logStore {
				reopened := openIncBackupStore(ctx, cfg)

				addIncTestJobs(ctx, reopened, "inc cut after", 5)
				So(reopened.backupTick(), ShouldBeNil)

				return reopened
			}

			Convey("with its header damaged and the rest zeroed, so recovery removes it and reuses n, a tick "+
				"makes the backup restore to what the store holds", func() {
				tornWALFile(walFilePath(cfg.dbFile, n))

				reopened := reopenAndShip()

				So(walFilePath(cfg.dbFile, n), shouldExist)
				So(tiledEnd(backupParts(cfg.dbBkFile, n)), ShouldEqual, walFileSizes(cfg.dbFile)[n])
				So(snapshotOfRepGroup(restoredBackup(cfg.dbBkFile)), ShouldResemble, snapshotOfRepGroup(reopened))
			})

			Convey("with a record damaged, so recovery truncates it, a tick makes the backup restore to what "+
				"the store holds", func() {
				size := walFileSizes(cfg.dbFile)[n]
				flipByteAt(walFilePath(cfg.dbFile, n), size/2)

				reopened := reopenAndShip()

				So(walFileSizes(cfg.dbFile)[n], ShouldBeLessThan, size)
				So(snapshotOfRepGroup(restoredBackup(cfg.dbBkFile)), ShouldResemble, snapshotOfRepGroup(reopened))
			})

			Convey("with its header damaged and the rest zeroed, and the run that removes and reuses n ending "+
				"as a crash would before its first tick, the next run's tick makes the backup restore to what the "+
				"store holds", func() {
				tornWALFile(walFilePath(cfg.dbFile, n))

				crashed, err := openLogStoreLocked(ctx, cfg)
				So(err, ShouldBeNil)

				addIncTestJobs(ctx, crashed, "inc cut crashed", 400)
				closeAsCrashed(ctx, crashed)

				reopened := reopenAndShip()

				So(snapshotOfRepGroup(restoredBackup(cfg.dbBkFile)), ShouldResemble, snapshotOfRepGroup(reopened))
			})

			Convey("with its header damaged and the rest zeroed, and the run that removes and reuses n ending "+
				"as a crash would before its first tick, then a record of the reused n damaged, so the next "+
				"recovery truncates it, the next run's tick makes the backup restore to what the store holds",
				func() {
					tornWALFile(walFilePath(cfg.dbFile, n))

					crashed, err := openLogStoreLocked(ctx, cfg)
					So(err, ShouldBeNil)

					for i := range 30 {
						addIncTestJobs(ctx, crashed, fmt.Sprintf("inc cut crashed %d", i), 3)
					}

					closeAsCrashed(ctx, crashed)

					size := walFileSizes(cfg.dbFile)[n]
					flipByteAt(walFilePath(cfg.dbFile, n), size-10)

					reopened := reopenAndShip()

					So(walFileSizes(cfg.dbFile)[n], ShouldBeLessThan, size)
					So(snapshotOfRepGroup(restoredBackup(cfg.dbBkFile)), ShouldResemble,
						snapshotOfRepGroup(reopened))
				})

			Convey("with its header damaged and the rest zeroed, the first tick after deletes backup-cuts, and "+
				"a later run's first tick deletes no part from the backup", func() {
				tornWALFile(walFilePath(cfg.dbFile, n))

				r2, err := openLogStoreLocked(ctx, cfg)
				So(err, ShouldBeNil)
				So(filepath.Join(cfg.dbFile, recoveryCutsFile), shouldExist)

				addIncTestJobs(ctx, r2, "inc cut r2", 50)
				So(r2.backupTick(), ShouldBeNil)
				So(filepath.Join(cfg.dbFile, recoveryCutsFile), shouldNotExist)
				So(r2.close(ctx), ShouldBeNil)

				r3 := openIncBackupStore(ctx, cfg)
				rec := recordBackupOps(r3)
				So(r3.backupTick(), ShouldBeNil)

				So(opsNamed(rec.recorded(), "remove", isPart), ShouldBeEmpty)
				So(snapshotOfRepGroup(restoredBackup(cfg.dbBkFile)), ShouldResemble, snapshotOfRepGroup(r3))
			})

			Convey("with a record damaged, so recovery truncates it, a first tick that fails syncing the "+
				"backup after deleting the parts past the cut leaves backup-cuts in place", func() {
				size := walFileSizes(cfg.dbFile)[n]
				flipByteAt(walFilePath(cfg.dbFile, n), size/2)

				reopened := openIncBackupStore(ctx, cfg)
				rec := recordBackupOps(reopened)
				rec.failOn = incTestSyncOp

				So(reopened.backupTick(), ShouldWrap, errIncTestCrash)
				So(filepath.Join(cfg.dbFile, recoveryCutsFile), shouldExist)
			})

			Convey("with a record damaged, and a run without backups that truncates it, then checkpoints past "+
				"it and deletes it, a later run's ticks succeed and make the backup restore to what the store "+
				"holds", func() {
				size := walFileSizes(cfg.dbFile)[n]
				flipByteAt(walFilePath(cfg.dbFile, n), size*3/4)

				unbacked := cfg
				unbacked.dbBkFile = ""

				r2, err := openLogStoreLocked(ctx, unbacked)
				So(err, ShouldBeNil)
				So(filepath.Join(cfg.dbFile, recoveryCutsFile), shouldExist)

				archiveIncTestJobs(r2, jobs)

				durFile, _ := r2.w.Durable()
				addUntilFoldedPast(ctx, r2, durFile)
				So(r2.checkpoint(), ShouldBeNil)
				So(walFilePath(cfg.dbFile, n), shouldNotExist)
				So(r2.close(ctx), ShouldBeNil)

				r3 := openIncBackupStore(ctx, cfg)
				So(r3.backupTick(), ShouldBeNil)
				So(r3.backupTick(), ShouldBeNil)
				So(filepath.Join(cfg.dbFile, recoveryCutsFile), shouldNotExist)
				So(snapshotOfRepGroup(restoredBackup(cfg.dbBkFile)), ShouldResemble, snapshotOfRepGroup(r3))
			})
		})

	Convey("Given a closed log store in production deployment whose backup holds the parts of its last wal "+
		"file n, of many records, to its end", t, func() {
		withManualBackupTicks()

		cfg := incBackupConfig(t, internal.Production)
		s, err := openLogStoreLocked(ctx, cfg)
		So(err, ShouldBeNil)

		for i := range 20 {
			addIncTestJobs(ctx, s, fmt.Sprintf("inc cut earlier %d", i), 100)
		}

		So(s.backupTick(), ShouldBeNil)
		addIncTestJobs(ctx, s, "inc cut earlier more", 5)
		So(s.backupTick(), ShouldBeNil)
		So(s.close(ctx), ShouldBeNil)

		files, err := walFileNumbers(cfg.dbFile)
		So(err, ShouldBeNil)

		n := files[len(files)-1]
		size := walFileSizes(cfg.dbFile)[n]
		So(tiledEnd(backupParts(cfg.dbBkFile, n)), ShouldEqual, size)

		Convey("with a record damaged, so recovery truncates n at a, by an open that dies before its first "+
			"tick, then a record before a damaged, so the next recovery truncates n earlier, that run's tick "+
			"succeeds and makes the backup restore to what the store holds", func() {
			flipByteAt(walFilePath(cfg.dbFile, n), size*3/4)

			died := func() error { return errRecoverRun }
			logWALTruncatedHook.Store(&died)
			Reset(func() { logWALTruncatedHook.Store(nil) })

			_, err = openLogStoreLocked(ctx, cfg)
			So(err, ShouldWrap, errRecoverRun)

			logWALTruncatedHook.Store(nil)

			a := walFileSizes(cfg.dbFile)[n]
			So(a, ShouldBeLessThan, size)
			flipByteAt(walFilePath(cfg.dbFile, n), a/2)

			reopened := openIncBackupStore(ctx, cfg)
			So(walFileSizes(cfg.dbFile)[n], ShouldBeLessThan, a)

			addIncTestJobs(ctx, reopened, "inc cut earlier after", 5)
			So(reopened.backupTick(), ShouldBeNil)
			So(snapshotOfRepGroup(restoredBackup(cfg.dbBkFile)), ShouldResemble, snapshotOfRepGroup(reopened))
		})
	})

	Convey("Given a closed log store in production deployment whose backup holds its checkpoint and the parts "+
		"of that checkpoint's walFrom file, its last, the first part just its header", t, func() {
		withManualBackupTicks()
		smallWALSegments()

		cfg := incBackupConfig(t, internal.Production)
		s, err := openLogStoreLocked(ctx, cfg)
		So(err, ShouldBeNil)

		jobs := addIncTestJobs(ctx, s, "inc torn walFrom crash", 2000)
		So(s.checkpoint(), ShouldBeNil)
		So(s.backupTick(), ShouldBeNil)
		addIncTestJobs(ctx, s, "inc torn walFrom crash more", 50)
		So(s.backupTick(), ShouldBeNil)
		addIncTestJobs(ctx, s, "inc torn walFrom crash yet more", 50)
		So(s.backupTick(), ShouldBeNil)

		walFrom := s.checkpointFrom()
		So(s.close(ctx), ShouldBeNil)

		files, err := walFileNumbers(cfg.dbFile)
		So(err, ShouldBeNil)
		So(files[len(files)-1], ShouldEqual, walFrom)
		So(backupCheckpoints(cfg.dbBkFile), ShouldResemble, []uint32{walFrom})

		parts := backupParts(cfg.dbBkFile, walFrom)
		So(len(parts), ShouldBeGreaterThan, 2)
		So(parts[1].off, ShouldEqual, wal.FrameHeader+walHeaderLen)

		tornWALFile(walFilePath(cfg.dbFile, walFrom))

		Convey("with that file torn, a crash at any point in the first tick after recovery removes and reuses "+
			"it leaves a backup that restores every job its checkpoint holds live, and the tick that completes "+
			"deletes the old parts after the first before it replaces the first, and leaves only parts of the "+
			"file that tile the reused file", func() {
			crashes := crashAtEachFirstTickOp(ctx, t, cfg, func(reopened *logStore) {
				addIncTestJobs(ctx, reopened, "inc torn walFrom crash after", 5)
			}, func(reopened *logStore, crashCfg storeConfig, ops []string, done bool) {
				So(len(liveIn(restoredBackup(crashCfg.dbBkFile), jobs)), ShouldEqual, len(jobs))

				if !done {
					return
				}

				// a put of the first part before the deletes would leave the
				// reused file's header beside the old file's later parts, the
				// second starting where the header ends: one file of both
				put := slices.Index(ops, "put "+parts[0].name)
				So(put, ShouldBeGreaterThan, 0)

				for _, p := range parts[1:] {
					So(slices.Index(ops, "remove "+p.name), ShouldBeBetween, -1, put)
				}

				So(tiledEnd(backupParts(crashCfg.dbBkFile, walFrom)), ShouldEqual,
					walFileSizes(crashCfg.dbFile)[walFrom])
			})

			So(crashes, ShouldBeGreaterThan, 2)
		})

		Convey("with that file torn, and the run that removes and reuses it adding until it seals it, then "+
			"ending as a crash would before its first tick, a crash at any point in the next run's first tick "+
			"leaves a backup that restores every job its checkpoint holds live", func() {
			crashed, err := openLogStoreLocked(ctx, cfg)
			So(err, ShouldBeNil)

			for i := range 40 {
				addIncTestJobs(ctx, crashed, fmt.Sprintf("inc torn walFrom sealed %d", i), 50)
			}

			closeAsCrashed(ctx, crashed)

			files, err := walFileNumbers(cfg.dbFile)
			So(err, ShouldBeNil)
			So(files[len(files)-1], ShouldBeGreaterThan, walFrom)

			crashes := crashAtEachFirstTickOp(ctx, t, cfg, func(*logStore) {},
				func(_ *logStore, crashCfg storeConfig, _ []string, _ bool) {
					So(len(liveIn(restoredBackup(crashCfg.dbBkFile), jobs)), ShouldEqual, len(jobs))
				})

			So(crashes, ShouldBeGreaterThan, 2)
		})
	})

	Convey("Given a closed log store in production deployment whose backup holds 2000 jobs live and 50 "+
		"complete, its history.bolt folded into the store's last wal file n", t, func() {
		withManualBackupTicks()
		smallWALSegments()

		cfg := incBackupConfig(t, internal.Production)
		s, err := openLogStoreLocked(ctx, cfg)
		So(err, ShouldBeNil)

		jobs := addIncTestJobs(ctx, s, "inc torn folded", 2000)
		So(s.checkpoint(), ShouldBeNil)
		archived := addIncTestJobs(ctx, s, "inc torn folded archived", 50)
		archiveIncTestJobs(s, archived)
		So(s.flush(), ShouldBeNil)
		So(s.foldNow(), ShouldBeNil)
		So(s.backupTick(), ShouldBeNil)
		So(s.close(ctx), ShouldBeNil)

		files, err := walFileNumbers(cfg.dbFile)
		So(err, ShouldBeNil)

		n := files[len(files)-1]
		folded := backupHistFolded(t, cfg.dbBkFile)
		So(folded.file, ShouldEqual, n)
		So(folded.off, ShouldBeGreaterThan, wal.FrameHeader+walHeaderLen)

		Convey("with n torn, a crash at any point in the first tick of a run that archives 30 of the 2000 and "+
			"adds and archives 60 more leaves a backup holding each of the 2000 live or complete, the 50 "+
			"complete, and all or none of the 60", func() {
			tornWALFile(walFilePath(cfg.dbFile, n))

			var after []*Job

			crashes := crashAtEachFirstTickOp(ctx, t, cfg, func(reopened *logStore) {
				archiveIncTestJobs(reopened, jobs[:30])
				after = addIncTestJobs(ctx, reopened, "inc torn folded after", 60)
				archiveIncTestJobs(reopened, after)
				So(reopened.flush(), ShouldBeNil)
			}, func(_ *logStore, crashCfg storeConfig, _ []string, _ bool) {
				restored := restoredBackup(crashCfg.dbBkFile)

				So(heldIn(restored, jobs), ShouldHaveLength, len(jobs))
				So(completeWithCmd(restored, archived), ShouldResemble, jobKeys(archived))
				So(len(heldIn(restored, after)), ShouldBeIn, []int{0, len(after)})
			})

			So(crashes, ShouldBeGreaterThan, 2)
		})
	})

	Convey("Given a closed log store in production deployment whose backup holds the archives of 40 jobs, "+
		"unfolded, in a part of its last wal file n after one ending at a", t, func() {
		withManualBackupTicks()
		smallWALSegments()

		cfg := incBackupConfig(t, internal.Production)
		s, err := openLogStoreLocked(ctx, cfg)
		So(err, ShouldBeNil)

		addIncTestJobs(ctx, s, "inc truncated", 2000)
		So(s.checkpoint(), ShouldBeNil)
		forty := addIncTestJobs(ctx, s, "inc truncated forty", 40)
		So(s.backupTick(), ShouldBeNil)

		files, err := walFileNumbers(cfg.dbFile)
		So(err, ShouldBeNil)

		n := files[len(files)-1]
		a := tiledEnd(backupParts(cfg.dbBkFile, n))

		archiveIncTestJobs(s, forty)
		So(s.backupTick(), ShouldBeNil)
		So(s.close(ctx), ShouldBeNil)

		files, err = walFileNumbers(cfg.dbFile)
		So(err, ShouldBeNil)
		So(files[len(files)-1], ShouldEqual, n)
		So(tiledEnd(backupParts(cfg.dbBkFile, n)), ShouldBeGreaterThan, a)

		Convey("with the record at a damaged, an open that dies once recovery has truncated n there leaves "+
			"backup-cuts holding that cut, and a crash at any point in the next run's first tick leaves a "+
			"backup holding each of the 40 live or complete", func() {
			flipByteAt(walFilePath(cfg.dbFile, n), a+wal.FrameHeader+1)

			died := func() error { return errRecoverRun }
			logWALTruncatedHook.Store(&died)
			Reset(func() { logWALTruncatedHook.Store(nil) })

			_, err = openLogStoreLocked(ctx, cfg)
			So(err, ShouldWrap, errRecoverRun)

			logWALTruncatedHook.Store(nil)

			So(walFileSizes(cfg.dbFile)[n], ShouldEqual, a)

			cuts, err := os.ReadFile(filepath.Join(cfg.dbFile, recoveryCutsFile))
			So(err, ShouldBeNil)
			So(string(cuts), ShouldEqual, fmt.Sprintf("%0*x %d\n", walFileDigits, n, a))

			crashes := crashAtEachFirstTickOp(ctx, t, cfg, func(reopened *logStore) {
				more := addIncTestJobs(ctx, reopened, "inc truncated more", 60)
				archiveIncTestJobs(reopened, more)
				So(reopened.flush(), ShouldBeNil)
			}, func(_ *logStore, crashCfg storeConfig, _ []string, _ bool) {
				So(heldIn(restoredBackup(crashCfg.dbBkFile), forty), ShouldHaveLength, len(forty))
			})

			So(crashes, ShouldBeGreaterThan, 2)
		})
	})

	Convey("Given a closed log store in production deployment whose backup holds 2000 jobs in its checkpoint "+
		"and the archives of 100 more in a part of its last wal file n", t, func() {
		withManualBackupTicks()
		smallWALSegments()

		cfg := incBackupConfig(t, internal.Production)
		s, err := openLogStoreLocked(ctx, cfg)
		So(err, ShouldBeNil)

		jobs := addIncTestJobs(ctx, s, "inc unbacked", 2000)
		So(s.checkpoint(), ShouldBeNil)
		hundred := addIncTestJobs(ctx, s, "inc unbacked hundred", 100)
		So(s.backupTick(), ShouldBeNil)

		files, err := walFileNumbers(cfg.dbFile)
		So(err, ShouldBeNil)

		n := files[len(files)-1]
		a := tiledEnd(backupParts(cfg.dbBkFile, n))

		archiveIncTestJobs(s, hundred)
		So(s.backupTick(), ShouldBeNil)
		So(s.close(ctx), ShouldBeNil)

		files, err = walFileNumbers(cfg.dbFile)
		So(err, ShouldBeNil)
		So(files[len(files)-1], ShouldEqual, n)
		So(tiledEnd(backupParts(cfg.dbBkFile, n)), ShouldBeGreaterThan, a)

		held := slices.Concat(jobs, hundred)

		runUnbackedPastN := func() {
			unbacked := cfg
			unbacked.dbBkFile = ""

			r2, errr := openLogStoreLocked(ctx, unbacked)
			So(errr, ShouldBeNil)

			durFile, _ := r2.w.Durable()
			addUntilFoldedPast(ctx, r2, durFile)
			So(r2.checkpoint(), ShouldBeNil)
			So(walFilePath(cfg.dbFile, n), shouldNotExist)
			So(r2.close(ctx), ShouldBeNil)
		}

		Convey("with the record at the start of that part damaged, and a run without backups that truncates "+
			"n there, folds past n, checkpoints and deletes n, a crash at any op of the next run's first tick "+
			"leaves a backup that either refuses to restore, saying to move it aside, or holds every job it "+
			"held, live or complete, and every history entry complete", func() {
			flipByteAt(walFilePath(cfg.dbFile, n), a+wal.FrameHeader+1)
			runUnbackedPastN()

			crashEachOpHoldsOrRefuses(ctx, t, cfg, held)
		})

		Convey("with a run without backups that folds past n, checkpoints and deletes n and the files before "+
			"it, and its checkpoint's walFrom file then cut inside its header, so the next run cannot ship that "+
			"checkpoint, a crash at any op of that run's first tick, or none, leaves a backup that either "+
			"refuses to restore, saying to move it aside, or holds every job it held", func() {
			runUnbackedPastN()

			ckpts, errc := checkpointNumbers(cfg.dbFile)
			So(errc, ShouldBeNil)
			So(ckpts, ShouldHaveLength, 1)
			So(os.Truncate(walFilePath(cfg.dbFile, ckpts[0]), int64(walLenFlagsLen+1)), ShouldBeNil)

			crashEachOpHoldsOrRefuses(ctx, t, cfg, held)
		})

		Convey("with a run without backups that folds past n, checkpoints and deletes n and the files before "+
			"it, a crash at any op of the next run's first tick leaves a backup that either refuses to "+
			"restore, saying to move it aside, or holds every job it held, live or complete; each run logs at "+
			"warn that it started the backup again, and deletes VERSION durably before shipping anything",
			func() {
				runUnbackedPastN()

				logPath, logCtx := managerLogContext(t, ctx)

				ops := crashEachOpHoldsOrRefuses(logCtx, t, cfg, held)
				So(ops, ShouldNotBeEmpty)
				So(ops[:min(3, len(ops))], ShouldResemble, []string{"remove " + storeVersionFile, incTestSyncOp,
					"put " + historyBoltFile})

				lines := logLinesContaining(readLogFile(t, logPath), backupStartedAgainLogMsg)
				So(lines, ShouldNotBeEmpty)
				So(slices.ContainsFunc(lines, func(l string) bool { return !strings.Contains(l, "lvl=warn") }),
					ShouldBeFalse)
			})

		Convey("with a run without backups that folds past n, checkpoints and deletes n and the files before "+
			"it, and a backup also holding the store's first local wal file whole, which backup-cuts says "+
			"recovery cut short, the next run's first tick deletes VERSION, durably, before it makes the "+
			"backup's copy of that file match the cut", func() {
			runUnbackedPastN()

			files, errw := walFileNumbers(cfg.dbFile)
			So(errw, ShouldBeNil)

			first := files[0]
			local, errr := os.ReadFile(walFilePath(cfg.dbFile, first))
			So(errr, ShouldBeNil)
			So(os.WriteFile(filepath.Join(cfg.dbBkFile, backupPartName(first, 0)), local, 0o600), ShouldBeNil)
			So(os.WriteFile(filepath.Join(cfg.dbFile, recoveryCutsFile),
				fmt.Appendf(nil, "%0*x %d\n", walFileDigits, first, len(local)/2), 0o600), ShouldBeNil)

			reopened := openIncBackupStore(ctx, cfg)
			rec := recordBackupOps(reopened)
			So(reopened.backupTick(), ShouldBeNil)

			ops := rec.recorded()
			matched := slices.Index(ops, "put "+backupPartName(first, 0))
			So(matched, ShouldBeGreaterThan, 1)
			So(ops[:2], ShouldResemble, []string{"remove " + storeVersionFile, incTestSyncOp})
			So(snapshotOfRepGroup(restoredBackup(cfg.dbBkFile)), ShouldResemble, snapshotOfRepGroup(reopened))
		})
	})

	Convey("Given a closed log store in production deployment whose backup holds 2050 jobs and the first bytes "+
		"of its last wal file n, and that added more to n, checkpointed past n and crashed before its next "+
		"tick, so n's last bytes were never shipped", t, func() {
		withManualBackupTicks()
		smallWALSegments()

		cfg := incBackupConfig(t, internal.Production)
		s, err := openLogStoreLocked(ctx, cfg)
		So(err, ShouldBeNil)

		jobs := addIncTestJobs(ctx, s, "inc unshipped", 2000)
		So(s.checkpoint(), ShouldBeNil)
		fifty := addIncTestJobs(ctx, s, "inc unshipped fifty", 50)
		So(s.backupTick(), ShouldBeNil)

		files, err := walFileNumbers(cfg.dbFile)
		So(err, ShouldBeNil)

		n := files[len(files)-1]
		shipped := tiledEnd(backupParts(cfg.dbBkFile, n))

		addIncTestJobs(ctx, s, "inc unshipped tail", 10)
		So(s.flush(), ShouldBeNil)
		So(s.checkpoint(), ShouldBeNil)
		closeAsCrashed(ctx, s)

		So(walFileSizes(cfg.dbFile)[n], ShouldBeGreaterThan, shipped)

		Convey("a run without backups that only opens and closes, so deletes n, and a crash at any op of the "+
			"next run's first tick, leave a backup that either refuses to restore, saying to move it aside, "+
			"or holds every job it held, live or complete", func() {
			unbacked := cfg
			unbacked.dbBkFile = ""

			r2, errr := openLogStoreLocked(ctx, unbacked)
			So(errr, ShouldBeNil)

			prevFile, prevEnd, whole, errh := r2.walHeader(n + 1)
			So(errh, ShouldBeNil)
			So(r2.close(ctx), ShouldBeNil)

			So(walFilePath(cfg.dbFile, n), shouldNotExist)
			So(whole, ShouldBeTrue)
			So(prevFile, ShouldEqual, n)
			So(prevEnd, ShouldBeGreaterThan, shipped)

			crashEachOpHoldsOrRefuses(ctx, t, cfg, slices.Concat(jobs, fifty))
		})

		Convey("a run without backups that only opens and closes, so deletes n, then VERSION gone from the "+
			"backup, as a crash just after an earlier start-again removed it leaves, let the next run's first "+
			"tick start the backup again and succeed, leaving a backup that holds every job it held", func() {
			unbacked := cfg
			unbacked.dbBkFile = ""

			r2, errr := openLogStoreLocked(ctx, unbacked)
			So(errr, ShouldBeNil)
			So(r2.close(ctx), ShouldBeNil)

			So(os.Remove(filepath.Join(cfg.dbBkFile, storeVersionFile)), ShouldBeNil)

			r3 := openIncBackupStore(ctx, cfg)
			So(r3.backupTick(), ShouldBeNil)

			So(heldIn(restoredBackup(cfg.dbBkFile), slices.Concat(jobs, fifty)), ShouldHaveLength,
				len(jobs)+len(fifty))
		})
	})

	Convey("Given a closed log store in production deployment whose backup holds 500 jobs", t, func() {
		withManualBackupTicks()
		smallWALSegments()

		cfg := incBackupConfig(t, internal.Production)
		s, err := openLogStoreLocked(ctx, cfg)
		So(err, ShouldBeNil)

		jobs := addIncTestJobs(ctx, s, "inc backed only", 500)
		So(s.checkpoint(), ShouldBeNil)
		So(s.backupTick(), ShouldBeNil)
		So(s.close(ctx), ShouldBeNil)

		Convey("a crash at any op of a later run's tick, whose local checkpoints delete the wal files the "+
			"backup is done with as it ships them, then a reopen, make a first tick that neither starts the "+
			"backup again nor deletes VERSION, and leaves a backup holding every job", func() {
			logPath, logCtx := managerLogContext(t, ctx)

			crashes, versionRemoved, refused := crashBackedTickWithLocalCheckpoints(ctx, logCtx, t, cfg, jobs)

			So(crashes, ShouldBeGreaterThan, 2)
			So(versionRemoved, ShouldEqual, 0)
			So(refused, ShouldEqual, 0)
			So(logLinesContaining(readLogFile(t, logPath), backupStartedAgainLogMsg), ShouldBeEmpty)
		})
	})

	Convey("Given a closed log store in production deployment whose backup-cuts file is damaged", t, func() {
		withManualBackupTicks()

		cfg := incBackupConfig(t, internal.Production)
		s, err := openLogStoreLocked(ctx, cfg)
		So(err, ShouldBeNil)

		addIncTestJobs(ctx, s, "inc damaged cuts", 5)
		So(s.close(ctx), ShouldBeNil)

		cuts := filepath.Join(cfg.dbFile, recoveryCutsFile)
		So(os.WriteFile(cuts, []byte("not a cut\n"), dbFilePermission), ShouldBeNil)

		Convey("opening it fails with an error naming the file and saying what to do", func() {
			_, err = openLogStoreLocked(ctx, cfg)
			So(err, ShouldNotBeNil)
			So(err.Error(), ShouldContainSubstring, cuts)
			So(err.Error(), ShouldContainSubstring, "move the backup aside")
			So(cuts, shouldExist)
		})
	})

	Convey("Given a closed log store in production deployment whose last wal file is its checkpoint's walFrom, "+
		"shipped", t, func() {
		withManualBackupTicks()

		cfg := incBackupConfig(t, internal.Production)
		s, err := openLogStoreLocked(ctx, cfg)
		So(err, ShouldBeNil)

		addIncTestJobs(ctx, s, "inc torn walFrom", 5)
		So(s.checkpoint(), ShouldBeNil)
		So(s.backupTick(), ShouldBeNil)

		walFrom := s.checkpointFrom()
		So(s.close(ctx), ShouldBeNil)
		So(backupParts(cfg.dbBkFile, walFrom), ShouldNotBeEmpty)

		Convey("cut inside its header, an open that dies once recovery has removed it, then a reopen, leave "+
			"a tick that makes the backup restore to what the store holds", func() {
			So(os.Truncate(walFilePath(cfg.dbFile, walFrom), int64(walLenFlagsLen+1)), ShouldBeNil)

			died := func() error { return errRecoverRun }
			logTornRemovedHook.Store(&died)
			Reset(func() { logTornRemovedHook.Store(nil) })

			_, err = openLogStoreLocked(ctx, cfg)
			So(err, ShouldWrap, errRecoverRun)

			logTornRemovedHook.Store(nil)

			reopened := openIncBackupStore(ctx, cfg)
			addIncTestJobs(ctx, reopened, "inc torn walFrom after", 5)
			So(reopened.backupTick(), ShouldBeNil)

			So(snapshotOfRepGroup(restoredBackup(cfg.dbBkFile)), ShouldResemble, snapshotOfRepGroup(reopened))
		})
	})

	Convey("Given a backup made by one log store and another store whose recovery cut a wal file that backup "+
		"holds parts of", t, func() {
		withManualBackupTicks()
		smallWALSegments()

		cfgA := incBackupConfig(t, internal.Production)
		a, err := openLogStoreLocked(ctx, cfgA)
		So(err, ShouldBeNil)

		addIncTestJobs(ctx, a, "inc foreign", 300)
		addIncTestJobs(ctx, a, "inc foreign more", 2000)
		So(a.backupTick(), ShouldBeNil)
		So(a.close(ctx), ShouldBeNil)

		cfgB := incBackupConfig(t, internal.Production)
		b, err := openLogStoreLocked(ctx, cfgB)
		So(err, ShouldBeNil)

		addIncTestJobs(ctx, b, "inc foreign other", 100)
		So(b.close(ctx), ShouldBeNil)

		size := walFileSizes(cfgB.dbFile)[1]
		flipByteAt(walFilePath(cfgB.dbFile, 1), size/2)

		So(os.WriteFile(filepath.Join(cfgA.dbBkFile, "x"+storeTmpExt), []byte("left over"), dbFilePermission),
			ShouldBeNil)

		before := backupSums(cfgA.dbBkFile)

		Convey("the other store's first tick with that backup fails with errBackupAhead and leaves every file "+
			"of it byte-identical", func() {
			cfgB.dbBkFile = cfgA.dbBkFile
			s := openIncBackupStore(ctx, cfgB)

			So(s.backupTick(), ShouldWrap, errBackupAhead)
			So(backupSums(cfgA.dbBkFile), ShouldResemble, before)
		})
	})

	Convey("Given a closed log store in production deployment whose backup holds parts of a wal file it has "+
		"not reached", t, func() {
		withManualBackupTicks()

		cfg := incBackupConfig(t, internal.Production)

		first, err := openLogStoreLocked(ctx, cfg)
		So(err, ShouldBeNil)

		addIncTestJobs(ctx, first, "inc ahead", 5)
		So(first.close(ctx), ShouldBeNil)

		ahead := filepath.Join(cfg.dbBkFile, backupPartName(9, 0))
		So(os.WriteFile(ahead, []byte("another store's"), dbFilePermission), ShouldBeNil)

		names := backupNames(cfg.dbBkFile)
		So(names, ShouldContain, filepath.Base(ahead))

		s := openIncBackupStore(ctx, cfg)

		Convey("a tick after it reopens ships nothing and leaves the backup as it was", func() {
			addIncTestJobs(ctx, s, "inc ahead more", 5)
			So(s.backupTick(), ShouldWrap, errBackupAhead)
			So(backupNames(cfg.dbBkFile), ShouldResemble, names)
		})
	})

	Convey("Given a closed log store in production deployment that made a backup", t, func() {
		withManualBackupTicks()

		cfg := incBackupConfig(t, internal.Production)

		first, err := openLogStoreLocked(ctx, cfg)
		So(err, ShouldBeNil)

		addIncTestJobs(ctx, first, "inc ahead", 5)
		So(first.checkpoint(), ShouldBeNil)

		walFrom := first.checkpointFrom()
		So(first.close(ctx), ShouldBeNil)

		files, err := walFileNumbers(cfg.dbFile)
		So(err, ShouldBeNil)

		soAheadUntouched := func() {
			before := backupSums(cfg.dbBkFile)
			s := openIncBackupStore(ctx, cfg)

			So(s.backupTick(), ShouldWrap, errBackupAhead)
			So(backupSums(cfg.dbBkFile), ShouldResemble, before)
		}

		Convey("a backup also holding a checkpoint after its own makes its next tick fail with errBackupAhead, "+
			"leaving the backup as it was", func() {
			newer := filepath.Join(cfg.dbBkFile, filepath.Base(checkpointPath("", walFrom+1)))
			So(os.WriteFile(newer, []byte("another store's"), dbFilePermission), ShouldBeNil)

			soAheadUntouched()
		})

		Convey("a backup also holding a part of the wal file it will write first after it reopens makes its "+
			"next tick fail with errBackupAhead, leaving the backup as it was", func() {
			next := files[len(files)-1] + 1
			part := filepath.Join(cfg.dbBkFile, backupPartName(next, 0))
			So(os.WriteFile(part, []byte("another store's"), dbFilePermission), ShouldBeNil)

			soAheadUntouched()
		})
	})
}

// tornWALFile damages the header CRC of the wal file at path and zeroes what
// follows its header, which recovery takes for a header never made durable.
func tornWALFile(path string) {
	data, err := os.ReadFile(path)
	So(err, ShouldBeNil)

	data[walLenFlagsLen] ^= 0x01
	clear(data[wal.FrameHeader+walHeaderLen:])

	So(os.WriteFile(path, data, dbFilePermission), ShouldBeNil)
}

// tiledEnd returns the end of parts, sorted by offset, if they tile from 0
// with none left over, else -1.
func tiledEnd(parts []backupPart) int64 {
	var end int64

	for _, p := range parts {
		if p.off != end {
			return -1
		}

		end = p.end()
	}

	return end
}

// snapshotOfRepGroup returns the keys and Cmds of the jobs of incTestRepGroup
// live in s, sorted.
func snapshotOfRepGroup(s *logStore) []string {
	live, _, err := s.recoverIncompleteJobs()
	So(err, ShouldBeNil)

	var out []string

	for _, job := range live {
		if job.RepGroup == incTestRepGroup {
			out = append(out, job.Key()+" "+job.Cmd)
		}
	}

	slices.Sort(out)

	return out
}

// flipByteAt flips the bits of the byte at off in the file at path.
func flipByteAt(path string, off int64) {
	data, err := os.ReadFile(path)
	So(err, ShouldBeNil)

	data[off] ^= 0xff

	So(os.WriteFile(path, data, dbFilePermission), ShouldBeNil)
}

// crashEachOpHoldsOrRefuses crashes a first tick of a copy of the closed store
// cfg names at each op in turn (see crashAtEachFirstTickOp), opening each copy
// with ctx, and requires the backup each crash leaves to either refuse to
// restore or hold each of held (see backupHoldsOrRefuses). It requires more
// than two crashes and at least one refusal, and returns the ops of the tick
// that completed.
func crashEachOpHoldsOrRefuses(ctx context.Context, t *testing.T, cfg storeConfig, held []*Job) []string {
	t.Helper()

	refused := 0

	var doneOps []string

	crashes := crashAtEachFirstTickOp(ctx, t, cfg, func(*logStore) {},
		func(_ *logStore, crashCfg storeConfig, ops []string, done bool) {
			if done {
				doneOps = ops
			}

			if backupHoldsOrRefuses(crashCfg.dbBkFile, held) {
				refused++
			}
		})

	So(crashes, ShouldBeGreaterThan, 2)
	So(refused, ShouldBeGreaterThan, 0)

	return doneOps
}

// backupHoldsOrRefuses requires the backup at path to either refuse to
// restore, saying to move it aside, or hold each of held, live or complete,
// with every history entry complete. It reports whether it refused.
func backupHoldsOrRefuses(path string, held []*Job) bool {
	inspector, closeFiles, err := openLogBackupFileInspector(path)
	if err != nil {
		So(err, ShouldWrap, errNotABackup)
		So(err.Error(), ShouldContainSubstring, "move it aside")

		return true
	}

	restored, ok := inspector.(*logStore)
	So(ok, ShouldBeTrue)

	heldKeys := heldIn(restored, held)
	complete, errc := restored.retrieveCompleteJobsByRepGroup(incTestRepGroup)

	So(closeFiles(), ShouldBeNil)
	So(heldKeys, ShouldHaveLength, len(held))
	So(errc, ShouldBeNil)
	So(slices.ContainsFunc(complete, func(j *Job) bool { return j.State != JobStateComplete }), ShouldBeFalse)

	return false
}

// backupSums returns the sha256 of each file in the backup directory dir, by
// name.
func backupSums(dir string) map[string]string {
	sums := make(map[string]string)

	for _, name := range backupNames(dir) {
		sums[name] = sha256Of(filepath.Join(dir, name))
	}

	return sums
}

func TestLogStoreIncrementalBackupShipsWhatItLacks(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given a log store in production deployment whose tick shipped its checkpoint", t, func() {
		withManualBackupTicks()
		pauseLogFolder()

		cfg := incBackupConfig(t, internal.Production)
		s := openIncBackupStore(ctx, cfg)

		addIncTestJobs(ctx, s, "inc ckpt once", 10)
		So(s.checkpoint(), ShouldBeNil)
		So(s.backupTick(), ShouldBeNil)
		So(backupCheckpoints(cfg.dbBkFile), ShouldResemble, []uint32{s.checkpointFrom()})

		Convey("a tick with nothing new ships nothing, not that checkpoint again", func() {
			rec := recordBackupOps(s)

			So(s.backupTick(), ShouldBeNil)
			So(rec.recorded(), ShouldBeEmpty)
		})
	})

	Convey("Given a log store in production deployment whose tick failed to put the last part of sealed wal "+
		"file w after a checkpoint sealed it", t, func() {
		withManualBackupTicks()

		cfg := incBackupConfig(t, internal.Production)
		s := openIncBackupStore(ctx, cfg)

		addIncTestJobs(ctx, s, "inc sealed early", 10)
		So(s.backupTick(), ShouldBeNil)

		w, _ := s.w.Durable()
		shipped := tiledEnd(backupParts(cfg.dbBkFile, w))

		addIncTestJobs(ctx, s, "inc sealed", 10)
		So(s.checkpoint(), ShouldBeNil)
		So(s.checkpointFrom(), ShouldBeGreaterThan, w)

		rec := recordBackupOps(s)
		rec.failOn = "put " + backupPartName(w, shipped)

		So(s.backupTick(), ShouldWrap, errIncTestCrash)

		Convey("a later checkpoint keeps w, whose last part the backup still lacks", func() {
			addIncTestJobs(ctx, s, "inc sealed later", 10)
			So(s.checkpoint(), ShouldBeNil)
			So(walFilePath(s.dir, w), shouldExist)
		})
	})
}

// crashAtEachFirstTickOp copies the closed store cfg names, and its backup,
// once for each op of a first tick: it opens the copy, calls run with it, then
// ticks, crashing at that op, calls check with the store, its config, the ops
// the tick made and whether it completed, and closes the store as a crash
// would. It stops once a tick completes, returning how many crashed.
func crashAtEachFirstTickOp(ctx context.Context, t *testing.T, cfg storeConfig, run func(s *logStore),
	check func(s *logStore, crashCfg storeConfig, ops []string, done bool),
) int {
	t.Helper()

	base := t.TempDir()

	for k := range incTestCrashPoints {
		crashCfg := cfg
		crashCfg.dbFile = filepath.Join(base, fmt.Sprintf("store%d", k))
		crashCfg.dbBkFile = filepath.Join(base, fmt.Sprintf("backup%d", k))
		So(os.CopyFS(crashCfg.dbFile, os.DirFS(cfg.dbFile)), ShouldBeNil)
		So(os.CopyFS(crashCfg.dbBkFile, os.DirFS(cfg.dbBkFile)), ShouldBeNil)

		s, err := openLogStoreLocked(ctx, crashCfg)
		So(err, ShouldBeNil)

		run(s)

		rec := newRecordingTarget(dirTarget{path: crashCfg.dbBkFile}, k)
		retargetBackup(s, rec)

		errt := s.backupTick()
		check(s, crashCfg, rec.recorded(), errt == nil)
		closeAsCrashed(ctx, s)

		if errt == nil {
			return k
		}

		So(errt, ShouldWrap, errIncTestCrash)
	}

	So(fmt.Sprintf("no tick completed in %d tries", incTestCrashPoints), ShouldBeEmpty)

	return incTestCrashPoints
}

// crashBackedTickWithLocalCheckpoints copies the closed store cfg names, and
// its backup, once for each op of a tick: it opens the copy, ticks, adds and
// archives jobs with a local checkpoint after each batch, then ticks, with a
// local checkpoint after each wal part it puts, crashing at that op. It closes
// the store as a crash would, reopens it with reopenCtx and ticks. It stops
// once the crashing tick completes, and returns how many crashed, how many of
// the reopened ticks deleted VERSION, and how many of the backups they left
// refused to restore; each other one must hold every job of held and those
// added (see backupHoldsOrRefuses).
func crashBackedTickWithLocalCheckpoints(ctx, reopenCtx context.Context, t *testing.T, cfg storeConfig,
	held []*Job,
) (crashes, versionRemoved, refused int) {
	t.Helper()

	base := t.TempDir()

	for k := range incTestCrashPoints {
		kc := cfg
		kc.dbFile = filepath.Join(base, fmt.Sprintf("store%d", k))
		kc.dbBkFile = filepath.Join(base, fmt.Sprintf("backup%d", k))
		So(os.CopyFS(kc.dbFile, os.DirFS(cfg.dbFile)), ShouldBeNil)
		So(os.CopyFS(kc.dbBkFile, os.DirFS(cfg.dbBkFile)), ShouldBeNil)

		s, err := openLogStoreLocked(ctx, kc)
		So(err, ShouldBeNil)
		So(s.backupTick(), ShouldBeNil)

		all := slices.Clone(held)

		for i := range 4 {
			batch := addIncTestJobs(ctx, s, fmt.Sprintf("inc backed only %d %d", k, i), 100)
			archiveIncTestJobs(s, batch[:30])
			So(s.flush(), ShouldBeNil)
			So(s.checkpoint(), ShouldBeNil)

			all = append(all, batch...)
		}

		all = append(all, addIncTestJobs(ctx, s, fmt.Sprintf("inc backed only tail %d", k), 50)...)
		So(s.flush(), ShouldBeNil)

		rec := newRecordingTarget(dirTarget{path: kc.dbBkFile}, k)
		rec.afterPut = func(name string) {
			if isPart(name) {
				So(s.checkpoint(), ShouldBeNil)
			}
		}

		s.inc.tickMu.Lock()
		s.inc.target = rec
		s.inc.tickMu.Unlock()

		errt := s.backupTick()
		closeAsCrashed(ctx, s)

		reopened, err := openLogStoreLocked(reopenCtx, kc)
		So(err, ShouldBeNil)

		reopenedRec := recordBackupOps(reopened)
		So(reopened.backupTick(), ShouldBeNil)

		if slices.Contains(reopenedRec.recorded(), "remove "+storeVersionFile) {
			versionRemoved++
		}

		So(reopened.close(ctx), ShouldBeNil)

		if backupHoldsOrRefuses(kc.dbBkFile, all) {
			refused++
		}

		if errt == nil {
			return crashes, versionRemoved, refused
		}

		So(errt, ShouldWrap, errIncTestCrash)

		crashes++
	}

	So(fmt.Sprintf("no tick completed in %d tries", incTestCrashPoints), ShouldBeEmpty)

	return crashes, versionRemoved, refused
}

// closeAsCrashed closes s as a crash before its next tick would leave it and
// its backup: its final tick fails before changing anything.
func closeAsCrashed(ctx context.Context, s *logStore) {
	retargetBackup(s, newRecordingTarget(s.inc.target, 0))
	So(s.close(ctx), ShouldBeNil)
}

// tickGapsWhileShipping adds a job to s every tick interval while its ticker
// ships each tick's new wal bytes as one part, whose put takes
// incTestSlowPut, so each tick takes longer than the interval. Once
// incTestTickGaps+1 parts are put it returns the first incTestTickGaps gaps
// from the end of one such put to the start of the next, which hold the
// ticker's whole wait between those ticks. It returns fewer if the parts are
// not put within incTestTickWait.
func tickGapsWhileShipping(ctx context.Context, s *logStore) []time.Duration {
	rec := recordBackupOps(s)

	var (
		mu     sync.Mutex
		starts []time.Time
		ends   []time.Time
	)

	noteNow := func(times *[]time.Time) {
		mu.Lock()
		defer mu.Unlock()

		*times = append(*times, time.Now())
	}

	rec.mu.Lock()
	rec.afterPut = func(name string) {
		if !isPart(name) {
			return
		}

		noteNow(&starts)
		<-time.After(incTestSlowPut)
		noteNow(&ends)
	}
	rec.mu.Unlock()

	stop := make(chan struct{})
	done := make(chan error)

	go func() {
		var errs []error

		for i := 0; ; i++ {
			select {
			case <-stop:
				done <- errors.Join(errs...)

				return
			case <-time.After(incTestTickEvery):
			}

			_, err := s.storeNewJobsGuarded(ctx, testLogJobs(fmt.Sprintf("inc spacing %d", i), incTestRepGroup, 1),
				false, nil)
			errs = append(errs, err)
		}
	}()

	deadline := time.Now().Add(incTestTickWait)

	for time.Now().Before(deadline) {
		mu.Lock()
		n := len(ends)
		mu.Unlock()

		if n > incTestTickGaps {
			break
		}

		<-time.After(incTestTickEvery)
	}

	close(stop)
	So(<-done, ShouldBeNil)

	mu.Lock()
	defer mu.Unlock()

	n := max(min(incTestTickGaps, len(starts)-1, len(ends)), 0)
	gaps := make([]time.Duration, 0, n)

	for i := range n {
		gaps = append(gaps, starts[i+1].Sub(ends[i]))
	}

	return gaps
}

func TestLogStoreIncrementalBackupKeepsCheckpoint(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given a log store in production deployment reopened with no backup after its checkpoint's walFrom "+
		"file was cut inside its header, moving history's folded position back before the checkpoint's histFrom",
		t, func() {
			withManualBackupTicks()

			logPath, logCtx := managerLogContext(t, ctx)
			cfg := incBackupConfig(t, internal.Production)

			first, err := openLogStoreLocked(logCtx, cfg)
			So(err, ShouldBeNil)

			addIncTestJobs(ctx, first, "inc kept", 5)
			So(first.checkpoint(), ShouldBeNil)

			walFrom := first.checkpointFrom()
			So(first.close(ctx), ShouldBeNil)

			So(os.RemoveAll(cfg.dbBkFile), ShouldBeNil)
			So(os.Truncate(walFilePath(cfg.dbFile, walFrom), int64(walLenFlagsLen+1)), ShouldBeNil)

			s := openIncBackupStore(logCtx, cfg)

			folded, err := s.history.foldedPos()
			So(err, ShouldBeNil)
			So(checkpointHistFrom(s.dir, walFrom).after(folded), ShouldBeTrue)

			Convey("a tick ships no checkpoint, and logs at warn why", func() {
				So(s.backupTick(), ShouldBeNil)
				So(backupCheckpoints(cfg.dbBkFile), ShouldBeEmpty)

				lines := logLinesContaining(readLogFile(t, logPath), backupCheckpointKeptLogMsg)
				So(lines, ShouldHaveLength, 1)
				So(lines[0], ShouldContainSubstring, "lvl=warn")

				_, _, err = openLogBackupFileInspector(cfg.dbBkFile)
				So(err, ShouldWrap, errNotABackup)
			})
		})
}

func TestLogStoreIncrementalBackupAfterUnswappedCheckpoint(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given a log store in production deployment whose second checkpoint failed, finding Refs below its "+
		"cut it has no copy of, so kept the older checkpoint and the wal files", t, func() {
		withManualBackupTicks()
		pauseLogFolder()

		cfg := incBackupConfig(t, internal.Production)
		s := openIncBackupStore(ctx, cfg)

		jobs := addIncTestJobs(ctx, s, "inc unswapped", 2)

		first := checkpointed(s)
		So(s.backupTick(), ShouldBeNil)

		files := walFilesAtOrAfter(s.dir, first)

		k, err := parseLogKey(jobs[0].Key())
		So(err, ShouldBeNil)

		s.mu.RLock()
		stale := *s.state.live[k]
		s.mu.RUnlock()

		So(s.deleteLiveJobs(ctx, []string{jobs[0].Key()}), ShouldBeNil)

		diverged := func() error { //nolint:unparam // a logCheckpointRenameHook, which can fail
			s.mu.Lock()
			s.state.live[k] = &stale
			s.mu.Unlock()

			return nil
		}

		logCheckpointRenameHook.Store(&diverged)

		Reset(func() {
			logCheckpointRenameHook.Store(nil)

			s.mu.Lock()
			delete(s.state.live, k)
			s.mu.Unlock()
		})

		So(s.checkpoint(), ShouldWrap, errCheckpointRefsUnswapped)
		So(checkpointFiles(s.dir)[0], ShouldEqual, first)

		Convey("the next tick deletes neither, so the job its memory still has live still reads", func() {
			So(s.backupTick(), ShouldBeNil)

			So(checkpointFiles(s.dir)[0], ShouldEqual, first)
			So(walFilesAtOrAfter(s.dir, first)[:len(files)], ShouldResemble, files)

			_, _, err = s.liveJob(jobs[0].Key())
			So(err, ShouldBeNil)

			Convey("and once memory agrees with the log again, the next checkpoint deletes the wal file the "+
				"failed one sealed, which the backup has", func() {
				logCheckpointRenameHook.Store(nil)

				s.mu.Lock()
				delete(s.state.live, k)
				s.mu.Unlock()

				So(s.checkpoint(), ShouldBeNil)
				So(walFilePath(s.dir, first), shouldNotExist)
			})
		})
	})
}

func TestLogStoreIncrementalBackupTicker(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given a log store in production deployment with a short tick interval", t, func() {
		old := logBackupEvery.Load()
		logBackupEvery.Store(int64(incTestTickEvery))

		Reset(func() { logBackupEvery.Store(old) })

		pauseLogFolder()

		cfg := incBackupConfig(t, internal.Production)
		s := openIncBackupStore(ctx, cfg)

		Convey("it backs up on its own, its backup getting VERSION within many intervals", func() {
			addIncTestJobs(ctx, s, "inc ticker", 10)

			So(waitForFile(filepath.Join(cfg.dbBkFile, storeVersionFile), incTestTickWait), ShouldBeTrue)
		})

		Convey("ticks that take longer than the interval are spaced by how long they took", func() {
			gaps := tickGapsWhileShipping(ctx, s)
			So(gaps, ShouldHaveLength, incTestTickGaps)
			So(slices.Min(gaps), ShouldBeGreaterThanOrEqualTo, incTestSlowPut)
		})

		Convey("a close begun once the ticker's timer fired, before its tick began, returns only after that "+
			"tick finished, and no file changes after it returns", func() {
			parked, release := parkNextBackupTick()

			addIncTestJobs(ctx, s, "inc ticker close", 10)
			So(closedWithin(parked, incTestTickWait), ShouldBeTrue)

			var closeErr error

			closeDone := make(chan struct{})

			go func() {
				closeErr = s.close(ctx)
				close(closeDone)
			}()

			So(closedWithin(s.inc.stop, incTestTickWait), ShouldBeTrue)
			So(closedWithin(closeDone, incTestCloseWait), ShouldBeFalse)

			release()

			So(closedWithin(closeDone, incTestTickWait), ShouldBeTrue)

			tickFinished := false

			select {
			case <-s.inc.exited:
				tickFinished = true
			default:
			}

			So(tickFinished, ShouldBeTrue)
			So(closeErr, ShouldBeNil)

			local, backup := storeDirIdentity(t, cfg.dbFile), storeDirIdentity(t, cfg.dbBkFile)

			So(closedWithin(s.inc.exited, incTestTickWait), ShouldBeTrue)
			So(storeDirIdentity(t, cfg.dbFile), ShouldResemble, local)
			So(storeDirIdentity(t, cfg.dbBkFile), ShouldResemble, backup)
		})
	})
}

// waitForFile reports whether path exists within wait, polling for it.
func waitForFile(path string, wait time.Duration) bool {
	deadline := time.Now().Add(wait)

	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return true
		}

		<-time.After(incTestTickEvery)
	}

	return false
}

// parkNextBackupTick makes the ticker's next tick wait, once its timer has
// fired and before it begins, until the returned release is called, which the
// Convey block's end also does, so that a failed test cannot hang its store's
// close. No timeout releases it, so however long the host stalls a test, the
// tick stays parked until the test releases it. parked is closed once that
// tick waits.
func parkNextBackupTick() (parked <-chan struct{}, release func()) {
	parkedCh := make(chan struct{})
	releaseCh := make(chan struct{})

	var parkOnce, releaseOnce sync.Once

	park := func() {
		parkOnce.Do(func() {
			close(parkedCh)

			<-releaseCh
		})
	}

	release = func() { releaseOnce.Do(func() { close(releaseCh) }) }

	logBackupTickHook.Store(&park)

	Reset(func() {
		logBackupTickHook.Store(nil)
		release()
	})

	return parkedCh, release
}

func TestLogStoreIncrementalBackupOfSpentWALFile(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given a log store in production deployment that has made a backup tick", t, func() {
		withManualBackupTicks()

		cfg := incBackupConfig(t, internal.Production)
		s, err := openLogStoreLocked(ctx, cfg)
		So(err, ShouldBeNil)

		first := addIncTestJobs(ctx, s, "inc spent first", 10)
		So(s.backupTick(), ShouldBeNil)

		Convey("a wal file its writer spends (spec B2) and a tick seals, holding nothing, is in the backup, "+
			"which restores with every job live", func() {
			spent := spendNextWALFile(s, nil)
			second := addAfterSpentWALFile(ctx, s)

			So(s.backupTick(), ShouldBeNil)
			So(backupParts(cfg.dbBkFile, spent), ShouldResemble,
				[]backupPart{{name: backupPartName(spent, 0)}})

			restored := restoredBackup(cfg.dbBkFile)
			So(liveIn(restored, first), ShouldResemble, jobKeys(first))
			So(liveIn(restored, second), ShouldResemble, jobKeys(second))

			So(s.close(ctx), ShouldBeNil)
		})

		Convey("a spent wal file holding a torn header, in a backup that is a copy of the store directory, "+
			"which recovery then truncates to empty, is in the backup whatever op of the first tick after "+
			"a crash ends it at, so the backup restores with every job live", func() {
			spent := spendNextWALFile(s, make([]byte, wal.FrameHeader))
			second := addAfterSpentWALFile(ctx, s)

			So(s.close(ctx), ShouldBeNil)
			So(os.RemoveAll(cfg.dbBkFile), ShouldBeNil)
			So(os.CopyFS(cfg.dbBkFile, os.DirFS(cfg.dbFile)), ShouldBeNil)
			So(walFileSizes(cfg.dbBkFile)[spent], ShouldEqual, wal.FrameHeader)

			all := slices.Concat(first, second)

			crashAtEachFirstTickOp(ctx, t, cfg, func(s *logStore) {
				So(walFileSizes(s.dir)[spent], ShouldEqual, 0)
				So(s.recoveryCuts, ShouldResemble, map[uint32]int64{spent: 0})
			}, func(_ *logStore, crashCfg storeConfig, _ []string, _ bool) {
				inspector, closeFiles, erri := openLogBackupFileInspector(crashCfg.dbBkFile)
				So(erri, ShouldBeNil)

				restored, ok := inspector.(*logStore)
				So(ok, ShouldBeTrue)

				live := liveIn(restored, all)

				So(closeFiles(), ShouldBeNil)
				So(live, ShouldResemble, jobKeys(all))
			})
		})
	})
}

// spendNextWALFile makes the next wal file s's writer opens one it spends
// (spec B2), by making a file of that number holding content first, so the
// writer's exclusive create fails. A checkpoint then has the writer try it,
// and its failure is the writer's. It returns the spent file's number.
func spendNextWALFile(s *logStore, content []byte) uint32 {
	durFile, _ := s.w.Durable()
	spent := durFile + 1

	So(os.WriteFile(walFilePath(s.dir, spent), content, dbFilePermission), ShouldBeNil)
	So(s.checkpoint(), ShouldNotBeNil)

	return spent
}

// addAfterSpentWALFile adds 10 jobs to s once its writer has moved past the
// wal file it spent, retrying while the writer's failure fails the adds, and
// returns them.
func addAfterSpentWALFile(ctx context.Context, s *logStore) []*Job {
	for i := range incTestCrashPoints {
		jobs := testLogJobs(fmt.Sprintf("inc spent second %d", i), incTestRepGroup, 10)
		if _, err := s.storeNewJobsGuarded(ctx, jobs, false, nil); err == nil {
			return jobs
		}
	}

	So("no add succeeded after the spent wal file", ShouldBeEmpty)

	return nil
}

func TestLogStoreIncrementalBackupAfterRestore(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given a log store in production deployment holding 5 jobs", t, func() {
		withManualBackupTicks()

		cfg := incBackupConfig(t, internal.Production)
		s, err := openLogStoreLocked(ctx, cfg)
		So(err, ShouldBeNil)

		before := addIncTestJobs(ctx, s, "inc restore before", 5)

		reopenedBacksUp := func() {
			reopened, erro := openLogStoreLocked(ctx, cfg)
			So(erro, ShouldBeNil)

			after := addIncTestJobs(ctx, reopened, "inc restore after", 5)
			So(reopened.close(ctx), ShouldBeNil)

			restored := restoredBackup(cfg.dbBkFile)
			So(liveIn(restored, before), ShouldResemble, jobKeys(before))
			So(liveIn(restored, after), ShouldResemble, jobKeys(after))
		}

		Convey("restored from a self-contained backup of it at its db file, with no backup, it goes on "+
			"backing up, its close leaving a backup with all 10 jobs live", func() {
			tarPath := filepath.Join(filepath.Dir(cfg.dbFile), "self-contained.tar")
			f, errc := os.Create(tarPath)
			So(errc, ShouldBeNil)
			So(s.backup(f), ShouldBeNil)
			So(f.Close(), ShouldBeNil)
			So(s.close(ctx), ShouldBeNil)
			So(os.RemoveAll(cfg.dbBkFile), ShouldBeNil)
			So(os.RemoveAll(cfg.dbFile), ShouldBeNil)
			So(os.Rename(tarPath, cfg.dbFile), ShouldBeNil)

			reopenedBacksUp()
		})

		Convey("restored from its backup for a missing db file, it goes on backing up, its close leaving a "+
			"backup with all 10 jobs live", func() {
			So(s.close(ctx), ShouldBeNil)
			So(os.RemoveAll(cfg.dbFile), ShouldBeNil)

			reopenedBacksUp()
		})

		Convey("restored from its backup for a corrupt db file, it goes on backing up, its close leaving a "+
			"backup with all 10 jobs live", func() {
			So(s.close(ctx), ShouldBeNil)
			So(os.RemoveAll(cfg.dbFile), ShouldBeNil)
			writeGarbage(cfg.dbFile)

			reopenedBacksUp()
		})
	})
}
