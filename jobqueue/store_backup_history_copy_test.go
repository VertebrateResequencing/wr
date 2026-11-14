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

import (
	"context"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/VertebrateResequencing/wr/internal"
	. "github.com/smartystreets/goconvey/convey"
)

func TestLogStoreIncrementalBackupDefersHistory(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given a log store in production deployment whose backup holds a history.bolt above the re-copy "+
		"floor", t, func() {
		withManualBackupTicks()
		pauseLogFolder()
		withHistoryRecopyFloor(1)

		logPath, logCtx := managerLogContext(t, ctx)
		cfg := incBackupConfig(t, internal.Production)
		s := openIncBackupStore(logCtx, cfg)

		jobs := addIncTestJobs(ctx, s, "inc defer", 100)
		archiveIncTestJobs(s, jobs[:50])
		So(s.foldNow(), ShouldBeNil)

		rec := recordBackupOps(s)
		So(s.backupTick(), ShouldBeNil)

		histBytes := rec.putBytes[historyBoltFile]
		So(histBytes, ShouldBeGreaterThan, 0)

		shippedSince := partBytes(rec, rec.recorded())

		Convey("ticks after folds copy it again only once the wal bytes shipped since the last copy are at "+
			"least that copy's size, twice over, "+
			"keep the backup's checkpoint without a warn meanwhile, and the backup restores every job "+
			"throughout", func() {
			r := rand.New(rand.NewSource(1)) //nolint:gosec // reproducible commands
			archived := slices.Clone(jobs[:50])
			added := slices.Clone(jobs)

			shippedBefore, copies, lastCopy := shippedSince, 0, -1

			for tick := 0; copies < 2; tick++ {
				So(tick, ShouldBeLessThan, 200)

				more := deferTestJobs(r, tick)
				_, err := s.storeNewJobsGuarded(ctx, more, false, nil)
				So(err, ShouldBeNil)

				added = append(added, more...)

				archiveIncTestJobs(s, more[:1])
				archived = append(archived, more[0])

				So(s.foldNow(), ShouldBeNil)

				if tick == 1 {
					So(s.checkpoint(), ShouldBeNil)
				}

				So(s.backupTick(), ShouldBeNil)

				ops := rec.recorded()
				copied := len(opsNamed(ops, "put", func(name string) bool { return name == historyBoltFile })) > 0

				if copied {
					So(tick, ShouldBeGreaterThan, lastCopy+2)
					So(shippedBefore, ShouldBeGreaterThanOrEqualTo, histBytes)

					copies, lastCopy = copies+1, tick
					histBytes, shippedBefore = rec.putBytes[historyBoltFile], partBytes(rec, ops)

					continue
				}

				So(shippedBefore, ShouldBeLessThan, histBytes)

				shippedBefore += partBytes(rec, ops)

				if tick == 1 {
					soDeferredBackupRestores(t, logPath, cfg.dbBkFile, nil, added, archived)
				}
			}

			soDeferredBackupRestores(t, logPath, cfg.dbBkFile, []uint32{s.checkpointFrom()}, added, archived)
			soBackupHoldsOneCheckpoint(t, cfg.dbBkFile)
		})

		Convey("a tick that does not copy it leaves a backup that restores every job, those archived since "+
			"its copy complete", func() {
			archiveIncTestJobs(s, jobs[50:])
			So(s.foldNow(), ShouldBeNil)
			So(s.backupTick(), ShouldBeNil)

			So(opsNamed(rec.recorded(), "put", func(name string) bool { return name == historyBoltFile }),
				ShouldBeEmpty)

			restored := restoredBackup(cfg.dbBkFile)
			So(heldIn(restored, jobs), ShouldHaveLength, len(jobs))
			So(completeWithCmd(restored, jobs), ShouldResemble, jobKeys(jobs))
		})
	})

	Convey("Given a log store in production deployment whose backup holds a history.bolt no bigger than the "+
		"re-copy floor", t, func() {
		withManualBackupTicks()
		pauseLogFolder()

		cfg := incBackupConfig(t, internal.Production)
		s := openIncBackupStore(ctx, cfg)

		jobs := addIncTestJobs(ctx, s, "inc floor", 100)
		archiveIncTestJobs(s, jobs[:50])
		So(s.foldNow(), ShouldBeNil)

		rec := recordBackupOps(s)
		So(s.backupTick(), ShouldBeNil)

		histBytes := rec.putBytes[historyBoltFile]
		So(histBytes, ShouldBeGreaterThan, 0)
		withHistoryRecopyFloor(histBytes)
		rec.recorded()

		Convey("the next tick after a fold copies it again", func() {
			archiveIncTestJobs(s, jobs[50:51])
			So(s.foldNow(), ShouldBeNil)
			So(s.backupTick(), ShouldBeNil)

			So(opsNamed(rec.recorded(), "put", func(name string) bool { return name == historyBoltFile }),
				ShouldHaveLength, 1)
		})
	})
}

// withHistoryRecopyFloor sets the re-copy floor of history.bolt to n bytes
// until the Convey block ends.
func withHistoryRecopyFloor(n int64) {
	old := logHistoryRecopyFloor.Load()
	logHistoryRecopyFloor.Store(n)

	Reset(func() { logHistoryRecopyFloor.Store(old) })
}

// partBytes returns the bytes of the wal parts among ops that rec put.
func partBytes(rec *recordingTarget, ops []string) int64 {
	var n int64

	for _, name := range opsNamed(ops, "put", isPart) {
		n += rec.putBytes[name]
	}

	return n
}

func TestLogStoreIncrementalBackupPacesHistoryEveryMiB(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given a log store in production deployment whose history.bolt is over 3MiB, and the default "+
		"backup pace", t, func() {
		withManualBackupTicks()
		pauseLogFolder()

		origHook := backupPaceHook
		backupPaceHook = func() { incTestPaces.Add(1) }

		Reset(func() { backupPaceHook = origHook })

		cfg := incBackupConfig(t, internal.Production)
		s := openIncBackupStore(ctx, cfg)

		r := rand.New(rand.NewSource(1)) //nolint:gosec // reproducible commands
		jobs := deferTestJobs(r, 0)

		for tick := 1; tick < 50; tick++ {
			jobs = append(jobs, deferTestJobs(r, tick)...)
		}

		_, err := s.storeNewJobsGuarded(ctx, jobs, false, nil)
		So(err, ShouldBeNil)
		archiveIncTestJobs(s, jobs)
		So(s.foldNow(), ShouldBeNil)

		Convey("a tick forces the writeback of its history.bolt copy at least once a MiB", func() {
			rec := recordBackupOps(s)

			So(s.backupTick(), ShouldBeNil)

			n := rec.putBytes[historyBoltFile]
			So(n, ShouldBeGreaterThan, 3<<20)
			So(rec.paces[historyBoltFile], ShouldBeGreaterThanOrEqualTo, int(n>>20)-1)
		})
	})
}

func TestLogStoreIncrementalBackupDropsHistoryCopyFromCache(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given a log store in production deployment whose history.bolt is over 3MiB, backing up to a "+
		"directory paced every 128KiB", t, func() {
		withManualBackupTicks()
		pauseLogFolder()

		origBytes := backupCopySyncBytes
		backupCopySyncBytes = 128 << 10

		Reset(func() { backupCopySyncBytes = origBytes })

		cfg := incBackupConfig(t, internal.Production)
		s := openIncBackupStore(ctx, cfg)

		r := rand.New(rand.NewSource(1)) //nolint:gosec // reproducible commands
		jobs := deferTestJobs(r, 0)

		for tick := 1; tick < 50; tick++ {
			jobs = append(jobs, deferTestJobs(r, tick)...)
		}

		_, err := s.storeNewJobsGuarded(ctx, jobs, false, nil)
		So(err, ShouldBeNil)
		archiveIncTestJobs(s, jobs)
		So(s.foldNow(), ShouldBeNil)

		Convey("a tick's copy of it is left mostly out of the page cache, so a large copy does not evict "+
			"pages the manager uses", func() {
			So(s.backupTick(), ShouldBeNil)

			f, err := os.Open(filepath.Join(cfg.dbBkFile, historyBoltFile))
			So(err, ShouldBeNil)

			defer f.Close()

			info, err := f.Stat()
			So(err, ShouldBeNil)
			So(info.Size(), ShouldBeGreaterThan, 3<<20)
			soResidency(t, f, info.Size(), ShouldBeLessThanOrEqualTo, 0.5)
		})
	})
}

// deferTestJobs returns 20 jobs for tick, each with 4KiB of random hex in its
// command, so each tick ships some tens of KiB of wal.
func deferTestJobs(r *rand.Rand, tick int) []*Job {
	jobs := make([]*Job, 20)
	pad := make([]byte, 4096)

	for i := range jobs {
		for j := range pad {
			pad[j] = "0123456789abcdef"[r.Intn(16)]
		}

		jobs[i] = testDBJob(fmt.Sprintf("echo defer %d %d %s", tick, i, pad), incTestRepGroup)
	}

	return jobs
}

// soDeferredBackupRestores asserts that no tick has logged
// backupCheckpointKeptLogMsg, that the backup at path holds the checkpoints
// ckpts, and that it restores with every one of added held and each of
// archived complete.
func soDeferredBackupRestores(t *testing.T, logPath, path string, ckpts []uint32, added, archived []*Job) {
	t.Helper()

	So(logLinesContaining(readLogFile(t, logPath), backupCheckpointKeptLogMsg), ShouldBeEmpty)
	So(backupCheckpoints(path), ShouldResemble, ckpts)

	restored := restoredBackup(path)
	So(heldIn(restored, added), ShouldHaveLength, len(added))
	So(completeWithCmd(restored, archived), ShouldResemble, jobKeys(archived))
}
