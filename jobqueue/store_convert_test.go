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
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/VertebrateResequencing/wr/internal"
	"github.com/VertebrateResequencing/wr/jobqueue/wal"
	"github.com/VertebrateResequencing/wr/limiter"
	"github.com/gofrs/uuid/v5"
	. "github.com/smartystreets/goconvey/convey"
	"github.com/ugorji/go/codec"
	bolt "go.etcd.io/bbolt"
	"golang.org/x/sys/unix"
)

// downgradeBoltOpenTimeout is the bbolt open timeout C7 test 4 uses, as a
// downgraded wr's initDB would bound its open.
const downgradeBoltOpenTimeout = time.Second

const (
	// convertRG1, convertRG2 and convertDupRG are the rep groups of the
	// conversion fixture's jobs, convertDupRG being one its jobs are looked up
	// under again; convertNeverRG is never added.
	convertRG1     = "convert-rg1"
	convertRG2     = "convert-rg2"
	convertDupRG   = "convert-rg-dup"
	convertNeverRG = "convert-rg-never"

	// convertDepGroup is the dep group of the fixture's parent job, which its
	// child depends on, and convertNeverDepGroup one never added.
	convertDepGroup      = "convert-dg"
	convertNeverDepGroup = "convert-dg-never"

	// convertLimitGroup is the fixture's limit group.
	convertLimitGroup = "convert-limit"

	// convertNeverKey is a job key the fixture never holds.
	convertNeverKey = "0123456789abcdef0123456789abcdef"

	// convertProgressJobs is how many complete jobs the progress fixture holds:
	// enough that the sidecar is written at progress points between 0 and the
	// total, as it is every dbUpgradeProgressEntries jobs.
	convertProgressJobs = 2*dbUpgradeProgressEntries + 1

	// convertBufferedLiveJobs is how many live jobs the live write error
	// fixture holds: enough that their records overflow the checkpoint's write
	// buffer many times over, so a write error surfaces while step 4 adds them
	// rather than when it flushes the checkpoint after.
	convertBufferedLiveJobs = 3000

	// convertPreSuffix follows dbFile in what E1 names its pre name in tests,
	// which build with no ServerVersion.
	convertPreSuffix = storeBoltPreInfix + "dev"

	// convertDBName is the name of the conversion tests' dbFile.
	convertDBName = "queue.db"

	// convertPreName and convertLockName are the names of the conversion
	// tests' dbFile's pre name and lock.
	convertPreName  = convertDBName + convertPreSuffix
	convertLockName = convertDBName + storeLockSuffix
)

const (
	// these are the upgrade lines an operator must be able to find in the manager
	// log at the default log level.
	dbUpgradeStartLogMatch     = `msg="database upgrade started"`
	dbUpgradeStepStartLogMatch = `msg="database upgrade step started"`
	dbUpgradeStepDoneLogMatch  = `msg="database upgrade step complete"`
	dbUpgradeDoneLogMatch      = `msg="database upgrade complete"`

	// dbUpgradeProgressLogMatch is the per-progress-point line, which must NOT
	// reach that log for every point: a real rebuild processes millions of
	// entries.
	dbUpgradeProgressLogMatch = `msg="database upgrade progress"`

	// dbUpgradeStillRunningLogMatch is the line that does reach it while a phase
	// is running, at most once per dbUpgradeLogInterval. It carries its own
	// message so that a default-level log cannot be mistaken for one line per
	// progress point.
	dbUpgradeStillRunningLogMatch = `msg="database upgrade step still running"`

	// dbUpgradeRateLimitPoints is how many progress points
	// TestDBUpgradeProgressLogRateLimit drives through the reporter, and
	// dbUpgradeRateLimitPointsPerInterval is how many of them it packs into each
	// dbUpgradeLogInterval. At dbUpgradeProgressEntries entries per point that is
	// a million-entry rebuild, the scale at which the difference between "once per
	// interval" and "once per point" is the difference between a readable log and
	// a flooded one.
	dbUpgradeRateLimitPoints            = 100
	dbUpgradeRateLimitPointsPerInterval = 4
)

// fifoOpenWait bounds the wait for an open of a FIFO at dbFile, which
// returns at once unless it blocks opening the FIFO for a writer that never
// comes.
const fifoOpenWait = 10 * time.Second

// errConvertTestCrash is what a convert hook returns to stop a conversion
// where it is, as a crash there would.
var errConvertTestCrash = errors.New("test crash")

// dbcompat fixture constants and helpers, shared by reliable2_dbcompat_test.go
// and the conversion tests below, which F5 keeps.
const (
	// dbcompatFixture is the committed golden DB produced by pre-removal code.
	dbcompatFixture = "testdata/dbcompat/db.golden"

	// dbcompatCompleteRepGroup holds the two archived (complete) fixture jobs.
	dbcompatCompleteRepGroup = "reliable2-dbcompat-complete"

	// dbcompatIncompleteRepGroup holds the two incomplete fixture jobs left in
	// jobslive, each carrying non-empty WaitingForDepGroups and
	// LimitGroupsForDisplay.
	dbcompatIncompleteRepGroup = "reliable2-dbcompat-incomplete"

	// dbcompatCompleteCount and dbcompatIncompleteCount are the known job counts
	// baked into the fixture.
	dbcompatCompleteCount   = 2
	dbcompatIncompleteCount = 2

	// dbcompatIncompleteCmd1 is the Cmd of the first incomplete fixture job (see
	// testdata/dbcompat/gen.go). Because those jobs set neither CwdMatters nor any
	// mounts, Cmd alone fixes their Job.Key(), so it lets a test address a key
	// that recovery has not yet restored during the recovery window.
	dbcompatIncompleteCmd1 = "true 3"
)

func TestStoreDowngradedWrRefusesStoreDirectory(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given a closed log store directory", t, func() {
		cfg, _ := logStoreOpenConfig(t)
		s, _ := seededLogStore(ctx, cfg)
		So(s.close(ctx), ShouldBeNil)

		before := storeDirIdentity(t, cfg.dbFile)

		Convey("opening it as bbolt, as a downgraded wr does, is refused as unreadable, changing nothing", func() {
			opened, err := bolt.Open(cfg.dbFile, dbFilePermission, &bolt.Options{Timeout: downgradeBoltOpenTimeout})
			if opened != nil {
				So(opened.Close(), ShouldBeNil)
			}

			So(opened, ShouldBeNil)
			So(err, ShouldWrap, syscall.EISDIR)
			So(openErrorThatIsNotCorruption(err, cfg.dbFile), ShouldWrap, ErrDBUnreadable)
			So(storeDirIdentity(t, cfg.dbFile), ShouldResemble, before)
		})
	})
}

// convertFixture is what writeConvertFixture stored, for asking both stores
// about it.
type convertFixture struct {
	keys      []string
	envKeys   []string
	reqGroups []string
	t0        time.Time
}

// writeConvertFixture stores, through the bbolt store at dbFile, live jobs (a
// dep group parent and its child, one reserved with a run-state record, one
// running with a rerun mark, one failed with std), completed jobs (one kept
// live to run again), an env, a limit, learned-requirement samples and rep
// group lookups of a live and a complete job under another rep group, and
// closes it.
func writeConvertFixture(ctx context.Context, dbFile string) convertFixture {
	s := openStoreOf(ctx, dbFile, storeKindBolt)
	f := convertFixture{t0: time.Unix(1_750_000_000, 0)}

	parent := testDBJob("echo convert parent", convertRG1)
	parent.DepGroups = []string{convertDepGroup}
	child := testDBJob("echo convert child", convertRG2)
	child.Dependencies = Dependencies{NewDepGroupDependency(convertDepGroup)}
	reserved := testDBJob("echo convert reserved", convertRG1)
	running := testDBJob("echo convert running", convertRG1)
	failed := testDBJob("echo convert failed", convertRG2)
	done1 := testDBJob("echo convert done1", convertRG1)
	done2 := testDBJob("echo convert done2", convertRG2)
	kept := testDBJob("echo convert kept", convertRG2)
	jobs := []*Job{parent, child, reserved, running, failed, done1, done2, kept}

	_, err := s.storeNewJobsGuarded(ctx, jobs, false, nil)
	So(err, ShouldBeNil)

	for _, job := range jobs {
		f.keys = append(f.keys, job.Key())
	}

	f.keys = append(f.keys, convertNeverKey)
	f.reqGroups = []string{parent.ReqGroup, "convert-never-reqgroup"}

	reserved.State = JobStateReserved
	reserved.ReservedBy = uuid.Must(uuid.NewV4())
	reserved.Attempts = 1
	commit, err := s.reserveRunState(reserved)
	So(err, ShouldBeNil)

	done, err := commit.WaitWithin(time.Minute)
	So(done, ShouldBeTrue)
	So(err, ShouldBeNil)

	running.State = JobStateRunning
	running.StartTime = f.t0
	running.Host = "convert-host"
	running.Pid = 42
	So(s.updateJobRunStateDurable(running), ShouldBeNil)

	running.RerunAfterRun = true
	So(s.storeRunningRerunMarks([]*Job{running}), ShouldBeNil)

	failed.State = JobStateBuried
	failed.Exited = true
	failed.Exitcode = 1
	failed.Attempts = 1
	failed.FailReason = FailReasonExit
	failed.PeakRAM = 99
	So(s.updateJobAfterExitDurable(failed, []byte("convert out"), []byte("convert err"), false), ShouldBeNil)

	done1.PeakDisk = 7
	done2.PeakDisk = 9

	archiveTestJob(s, done1, f.t0, f.t0.Add(10*time.Second))
	archiveTestJob(s, done2, f.t0.Add(time.Second), f.t0.Add(20*time.Second))

	kept.RerunAfterRun = true
	archiveTestJob(s, kept, f.t0.Add(2*time.Second), f.t0.Add(30*time.Second))

	So(s.storeRepGroupLookups(ctx, []*Job{
		testDBJob("echo convert parent", convertDupRG), testDBJob("echo convert done1", convertDupRG),
	}), ShouldBeNil)

	envKey, err := s.storeEnv([]byte("convert env"))
	So(err, ShouldBeNil)

	f.envKeys = []string{envKey, "convert-never-env"}

	_, _, err = s.storeLimitGroups(map[string]*limiter.GroupData{convertLimitGroup: limiter.NewCountGroupData(3)})
	So(err, ShouldBeNil)
	So(s.close(ctx), ShouldBeNil)

	return f
}

func TestStoreConvert(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given a bbolt DB built by the bbolt store with live, run-state, rerun-marked, failed, complete, "+
		"kept-live and looked-up jobs, an env, a limit and samples", t, func() {
		dir := t.TempDir()
		dbFile := filepath.Join(dir, convertDBName)
		f := writeConvertFixture(ctx, dbFile)
		original := sha256Of(dbFile)
		fromBolt := boltAnswersOfCopy(t, ctx, dbFile, f)

		Convey("when the manager starts, dbFile becomes a store answering every query as bbolt does, and the "+
			"bbolt file is kept at its pre name unchanged (E1 tests 1 and 6)", func() {
			So(convertedAnswers(ctx, dbFile, f), ShouldResemble, fromBolt)
			soHistoryBucketsCopied(dbFile+convertPreSuffix, dbFile)

			info, err := os.Stat(dbFile)
			So(err, ShouldBeNil)
			So(info.IsDir(), ShouldBeTrue)
			So(sha256Of(dbFile+convertPreSuffix), ShouldEqual, original)

			_, err = os.Stat(filepath.Join(dbFile, storeConvertSourceFile))
			So(err, ShouldWrap, os.ErrNotExist)
			So(besideDBFile(dbFile), ShouldResemble, []string{convertDBName, convertPreName, convertLockName})

			Convey("and a second start opens the store without converting again", func() {
				So(convertedAnswers(ctx, dbFile, f), ShouldResemble, fromBolt)
				So(sha256Of(dbFile+convertPreSuffix), ShouldEqual, original)
			})
		})

		conveyUnlessRoot("when dbFile is read-only, the manager opens it read-only, converts it, and keeps it "+
			"at its pre name unchanged", func() {
			So(os.Chmod(dbFile, 0o444), ShouldBeNil)

			So(convertedAnswers(ctx, dbFile, f), ShouldResemble, fromBolt)
			So(sha256Of(dbFile+convertPreSuffix), ShouldEqual, original)
			So(besideDBFile(dbFile), ShouldResemble, []string{convertDBName, convertPreName, convertLockName})
		})

		Convey("when the conversion stops at a crash point and the manager starts again, the store is as an "+
			"uninterrupted conversion's and the bbolt file is unchanged (E1 test 3)", func() {
			for _, c := range []struct {
				name  string
				point convertPoint
				nth   int
			}{
				{"in step 2 before SOURCE is written", convertAtCreating, 1},
				{"after step 2", convertAtCreated, 1},
				{"after step 3", convertAtRenamed, 1},
				{"mid step 4", convertAtJob, 4},
				{"before step 5", convertAtBuilt, 1},
				{"in step 5 before the rename", convertAtPublishing, 1},
				{"after step 5's rename, before SOURCE is deleted", convertAtPublished, 1},
			} {
				Convey(c.name, func() {
					crashConvertAt(c.point, c.nth)

					_, _, err := openStore(ctx, convertStoreConfig(dbFile, storeKindLog))
					So(err, ShouldWrap, errConvertTestCrash)
					So(errors.Is(err, errStoreCorrupt), ShouldBeFalse)

					logConvertHook.Store(nil)

					So(convertedAnswers(ctx, dbFile, f), ShouldResemble, fromBolt)
					So(sha256Of(dbFile+convertPreSuffix), ShouldEqual, original)
					So(besideDBFile(dbFile), ShouldResemble,
						[]string{convertDBName, convertPreName, convertLockName})

					_, err = os.Stat(filepath.Join(dbFile, storeConvertSourceFile))
					So(err, ShouldWrap, os.ErrNotExist)
				})
			}
		})

		Convey("when the conversion crashes in step 5 before the rename, SOURCE is still in .converting beside "+
			"the kept bbolt file, and the next start converts (E1 test 3)", func() {
			crashConvertAt(convertAtPublishing, 1)

			_, _, err := openStore(ctx, convertStoreConfig(dbFile, storeKindLog))
			So(err, ShouldWrap, errConvertTestCrash)
			soResumable(dbFile, err)

			logConvertHook.Store(nil)

			So(convertedAnswers(ctx, dbFile, f), ShouldResemble, fromBolt)
			So(sha256Of(dbFile+convertPreSuffix), ShouldEqual, original)
		})

		Convey("when the manager was built with a ServerVersion, the bbolt file is kept at a pre name holding "+
			"that version", func() {
			previous := ServerVersion

			Reset(func() { ServerVersion = previous })

			ServerVersion = "v9.9.9"

			So(convertedAnswers(ctx, dbFile, f), ShouldResemble, fromBolt)
			So(sha256Of(dbFile+storeBoltPreInfix+"v9.9.9"), ShouldEqual, original)

			_, err := os.Stat(dbFile + convertPreSuffix)
			So(err, ShouldWrap, os.ErrNotExist)
		})

		Convey("when step 4 fails for a reason of the conversion's own, the source is not corrupt: starting "+
			"fails, leaving the conversion for the next start to resume, which converts", func() {
			Convey("a panic in the converter's code", func() {
				setConvertHook(func(at convertPoint) error {
					if at == convertAtJob {
						panic("a converter fault")
					}

					return nil
				})

				_, _, err := openStore(ctx, convertStoreConfig(dbFile, storeKindLog))
				So(err, ShouldNotBeNil)
				So(err.Error(), ShouldContainSubstring, "a converter fault")
				So(err.Error(), ShouldNotContainSubstring, "goroutine ")
				soResumable(dbFile, err)
			})

			Convey("its context cancelled between jobs", func() {
				cctx, cancel := context.WithCancel(ctx)
				Reset(cancel)

				setConvertHook(func(at convertPoint) error {
					if at == convertAtJob {
						cancel()
					}

					return nil
				})

				_, _, err := openStore(cctx, convertStoreConfig(dbFile, storeKindLog))
				So(err, ShouldWrap, context.Canceled)
				soResumable(dbFile, err)
			})

			Convey("a write error on the new store", func() {
				setConvertHook(func(at convertPoint) error {
					if at == convertAtCounting {
						return failWritesTo(checkpointPath(dbFile+storeConvertingSuffix, convertWalFrom))
					}

					return nil
				})

				_, _, err := openStore(ctx, convertStoreConfig(dbFile, storeKindLog))
				So(err, ShouldWrap, syscall.ENOSPC)
				soResumable(dbFile, err)
			})

			logConvertHook.Store(nil)

			So(convertedAnswers(ctx, dbFile, f), ShouldResemble, fromBolt)
			So(sha256Of(dbFile+convertPreSuffix), ShouldEqual, original)
		})

		Convey("when the conversion crashes after step 3 and its resumption crashes mid step 4, the next start "+
			"still converts as an uninterrupted run (E1 test 3)", func() {
			crashConvertAt(convertAtRenamed, 1)

			_, _, err := openStore(ctx, convertStoreConfig(dbFile, storeKindLog))
			So(err, ShouldWrap, errConvertTestCrash)

			crashConvertAt(convertAtJob, 2)

			_, _, err = openStore(ctx, convertStoreConfig(dbFile, storeKindLog))
			So(err, ShouldWrap, errConvertTestCrash)

			logConvertHook.Store(nil)

			So(convertedAnswers(ctx, dbFile, f), ShouldResemble, fromBolt)
			So(sha256Of(dbFile+convertPreSuffix), ShouldEqual, original)
		})

		Convey("when the conversion crashes after step 3, a restart that would crash re-creating .converting "+
			"never does: it resumes on its SOURCE and converts (E1 test 3)", func() {
			crashConvertAt(convertAtRenamed, 1)

			_, _, err := openStore(ctx, convertStoreConfig(dbFile, storeKindLog))
			So(err, ShouldWrap, errConvertTestCrash)

			crashConvertAt(convertAtCreating, 1)

			So(convertedAnswers(ctx, dbFile, f), ShouldResemble, fromBolt)
			So(sha256Of(dbFile+convertPreSuffix), ShouldEqual, original)
		})

		Convey("when free space is 1 byte less than the bbolt file's size, starting fails with "+
			"errNotEnoughSpace and changes nothing (E1 test 4)", func() {
			info, err := os.Stat(dbFile)
			So(err, ShouldBeNil)

			var asked string

			hook := func(dir string) (uint64, error) { //nolint:unparam // the hook's type
				asked = dir

				return uint64(info.Size()) - 1, nil //nolint:gosec // a file size
			}
			storeFreeSpaceHook.Store(&hook)
			Reset(func() { storeFreeSpaceHook.Store(nil) })

			_, _, err = openStore(ctx, convertStoreConfig(dbFile, storeKindLog))
			So(err, ShouldWrap, errNotEnoughSpace)
			So(err.Error(), ShouldContainSubstring, strconv.FormatInt(info.Size(), 10))
			So(err.Error(), ShouldContainSubstring, strconv.FormatInt(info.Size()-1, 10))
			So(asked, ShouldEqual, dir)
			So(sha256Of(dbFile), ShouldEqual, original)
			So(besideDBFile(dbFile), ShouldResemble, []string{convertDBName, convertLockName})

			Convey("and with exactly its size free, it converts", func() {
				hook = func(string) (uint64, error) { return uint64(info.Size()), nil } //nolint:gosec,unparam // a size

				So(convertedAnswers(ctx, dbFile, f), ShouldResemble, fromBolt)
			})
		})

		Convey("when dbFile's pre name already exists, starting fails with errBoltPreExists naming it and "+
			"changes nothing (E1 test 8)", func() {
			pre := dbFile + convertPreSuffix
			So(os.WriteFile(pre, []byte("an earlier conversion's kept file"), dbFilePermission), ShouldBeNil)

			preSum := sha256Of(pre)

			_, _, err := openStore(ctx, convertStoreConfig(dbFile, storeKindLog))
			So(err, ShouldWrap, errBoltPreExists)
			So(err.Error(), ShouldContainSubstring, pre)
			So(sha256Of(dbFile), ShouldEqual, original)
			So(sha256Of(pre), ShouldEqual, preSum)
			So(besideDBFile(dbFile), ShouldResemble, []string{convertDBName, convertPreName, convertLockName})
		})

		Convey("when the conversion crashes after step 2 and dbFile's pre name is then taken, the resumed "+
			"conversion fails with errBoltPreExists and never overwrites it (E1 test 8)", func() {
			crashConvertAt(convertAtCreated, 1)

			_, _, err := openStore(ctx, convertStoreConfig(dbFile, storeKindLog))
			So(err, ShouldWrap, errConvertTestCrash)

			logConvertHook.Store(nil)

			pre := dbFile + convertPreSuffix
			So(os.WriteFile(pre, []byte("another kept file"), dbFilePermission), ShouldBeNil)

			preSum := sha256Of(pre)

			_, _, err = openStore(ctx, convertStoreConfig(dbFile, storeKindLog))
			So(err, ShouldWrap, errBoltPreExists)
			So(err.Error(), ShouldContainSubstring, pre)
			So(sha256Of(pre), ShouldEqual, preSum)
			So(sha256Of(dbFile), ShouldEqual, original)
		})

		Convey("when the conversion crashes after step 3 and a corrupt source cannot be renamed back to dbFile, "+
			"starting fails, not as corrupt, and keeps SOURCE naming the bbolt file at its pre name", func() {
			zeroBoltBucketRoot(dbFile, bucketStdO)

			damaged := sha256Of(dbFile)

			crashConvertAt(convertAtRenamed, 1)

			_, _, err := openStore(ctx, convertStoreConfig(dbFile, storeKindLog))
			So(err, ShouldWrap, errConvertTestCrash)

			logConvertHook.Store(nil)

			So(os.MkdirAll(filepath.Join(dbFile, "occupant"), logStoreDirPerm), ShouldBeNil)

			_, _, err = openStore(ctx, convertStoreConfig(dbFile, storeKindLog))
			So(err, ShouldNotBeNil)
			So(errors.Is(err, errStoreCorrupt), ShouldBeFalse)
			So(sha256Of(dbFile+convertPreSuffix), ShouldEqual, damaged)

			source, err := os.ReadFile(filepath.Join(dbFile+storeConvertingSuffix, storeConvertSourceFile))
			So(err, ShouldBeNil)
			So(string(source), ShouldEqual, convertPreName)
		})

		Convey("when a live job's record cannot be decoded, the source is not corrupt but cannot be converted: "+
			"starting fails with errBoltUnconvertible naming its key, the bbolt file is back at dbFile unchanged "+
			"and nothing is left beside it", func() {
			putBoltValue(dbFile, bucketJobsLive, convertNeverKey, []byte("not a job"))

			damaged := sha256Of(dbFile)

			_, _, err := openStore(ctx, convertStoreConfig(dbFile, storeKindLog))
			So(err, ShouldWrap, errBoltUnconvertible)
			So(errors.Is(err, errStoreCorrupt), ShouldBeFalse)
			So(err.Error(), ShouldContainSubstring, convertNeverKey)
			So(sha256Of(dbFile), ShouldEqual, damaged)
			So(besideDBFile(dbFile), ShouldResemble, []string{convertDBName, convertLockName})

			Convey("and found unconvertible again on a resumed conversion, after step 3", func() {
				crashConvertAt(convertAtRenamed, 1)

				_, _, err = openStore(ctx, convertStoreConfig(dbFile, storeKindLog))
				So(err, ShouldWrap, errConvertTestCrash)
				So(besideDBFile(dbFile), ShouldResemble,
					[]string{convertPreName, convertDBName + storeConvertingSuffix, convertLockName})

				logConvertHook.Store(nil)

				_, _, err = openStore(ctx, convertStoreConfig(dbFile, storeKindLog))
				So(err, ShouldWrap, errBoltUnconvertible)
				So(errors.Is(err, errStoreCorrupt), ShouldBeFalse)
				So(sha256Of(dbFile), ShouldEqual, damaged)
				So(besideDBFile(dbFile), ShouldResemble, []string{convertDBName, convertLockName})
			})

			Convey("and a crash after the bbolt file is renamed back, before .converting is removed, finds it "+
				"unconvertible again on the next start", func() {
				crashConvertAt(convertAtRestored, 1)

				_, _, err = openStore(ctx, convertStoreConfig(dbFile, storeKindLog))
				So(err, ShouldWrap, errConvertTestCrash)
				So(sha256Of(dbFile), ShouldEqual, damaged)

				logConvertHook.Store(nil)

				_, _, err = openStore(ctx, convertStoreConfig(dbFile, storeKindLog))
				So(err, ShouldWrap, errBoltUnconvertible)
				So(errors.Is(err, errStoreCorrupt), ShouldBeFalse)
				So(sha256Of(dbFile), ShouldEqual, damaged)
				So(besideDBFile(dbFile), ShouldResemble, []string{convertDBName, convertLockName})
			})
		})

		Convey("when an older manager holds the bbolt file, starting fails with ErrDBLocked, not as corrupt, "+
			"and changes nothing", func() {
			held, err := bolt.Open(dbFile, dbFilePermission, nil)
			So(err, ShouldBeNil)
			Reset(func() { So(held.Close(), ShouldBeNil) })

			previous := managerDBOpenTimeout

			Reset(func() { managerDBOpenTimeout = previous })

			managerDBOpenTimeout = 100 * time.Millisecond

			_, _, err = openStore(ctx, convertStoreConfig(dbFile, storeKindLog))
			So(err, ShouldWrap, ErrDBLocked)
			So(sha256Of(dbFile), ShouldEqual, original)
			So(besideDBFile(dbFile), ShouldResemble, []string{convertDBName, convertLockName})
		})

		Convey("when another manager holds the kept bbolt file of a conversion that crashed after step 3, "+
			"starting fails with ErrDBLocked, not as corrupt, and leaves the conversion to resume", func() {
			crashConvertAt(convertAtRenamed, 1)

			_, _, err := openStore(ctx, convertStoreConfig(dbFile, storeKindLog))
			So(err, ShouldWrap, errConvertTestCrash)

			logConvertHook.Store(nil)

			held, err := bolt.Open(dbFile+convertPreSuffix, dbFilePermission, nil)
			So(err, ShouldBeNil)
			Reset(func() { So(held.Close(), ShouldBeNil) })

			previous := managerDBOpenTimeout

			Reset(func() { managerDBOpenTimeout = previous })

			managerDBOpenTimeout = 100 * time.Millisecond

			_, _, err = openStore(ctx, convertStoreConfig(dbFile, storeKindLog))
			So(err, ShouldWrap, ErrDBLocked)
			soResumable(dbFile, err)

			So(held.Close(), ShouldBeNil)

			managerDBOpenTimeout = previous

			So(convertedAnswers(ctx, dbFile, f), ShouldResemble, fromBolt)
		})

		conveyUnlessRoot("when the SOURCE of a conversion that crashed after step 3 cannot be read, starting fails "+
			"with ErrDBUnreadable and changes nothing", func() {
			crashConvertAt(convertAtRenamed, 1)

			_, _, err := openStore(ctx, convertStoreConfig(dbFile, storeKindLog))
			So(err, ShouldWrap, errConvertTestCrash)

			logConvertHook.Store(nil)

			source := filepath.Join(dbFile+storeConvertingSuffix, storeConvertSourceFile)
			So(os.Chmod(source, 0), ShouldBeNil)
			Reset(func() { So(os.Chmod(source, dbFilePermission), ShouldBeNil) })

			_, _, err = openStore(ctx, convertStoreConfig(dbFile, storeKindLog))
			So(err, ShouldWrap, ErrDBUnreadable)
			So(errors.Is(err, errStoreCorrupt), ShouldBeFalse)
			So(besideDBFile(dbFile), ShouldResemble,
				[]string{convertPreName, convertDBName + storeConvertingSuffix, convertLockName})
			So(sha256Of(dbFile+convertPreSuffix), ShouldEqual, original)
		})

		Convey("when an interrupted conversion's SOURCE names a file outside dbFile's pre names, every start "+
			"fails with errBadConvertSource, not errStoreCorrupt, and changes nothing", func() {
			converting := dbFile + storeConvertingSuffix
			So(os.Mkdir(converting, logStoreDirPerm), ShouldBeNil)
			So(os.WriteFile(filepath.Join(converting, historyBoltFile), []byte("a partial build"),
				dbFilePermission), ShouldBeNil)

			for _, name := range []string{"../elsewhere", convertPreName + "/../elsewhere"} {
				So(os.WriteFile(filepath.Join(converting, storeConvertSourceFile), []byte(name),
					dbFilePermission), ShouldBeNil)

				for range 2 {
					_, _, err := openStore(ctx, convertStoreConfig(dbFile, storeKindLog))
					So(err, ShouldWrap, errBadConvertSource)
					So(errors.Is(err, errStoreCorrupt), ShouldBeFalse)
					So(err.Error(), ShouldContainSubstring, name)
					So(sha256Of(dbFile), ShouldEqual, original)
					So(besideDBFile(dbFile), ShouldResemble,
						[]string{convertDBName, convertDBName + storeConvertingSuffix, convertLockName})
					So(dirEntryNames(converting), ShouldResemble, []string{storeConvertSourceFile, historyBoltFile})
				}
			}
		})

		for _, other := range []string{"other.db", "other.db" + convertPreSuffix, convertDBName + ".old"} {
			Convey("when an interrupted conversion's SOURCE names a bbolt file beside dbFile that is not one of "+
				"its pre names, "+other+", every start fails with errBadConvertSource and never converts it", func() {
				So(os.Rename(dbFile, filepath.Join(dir, other)), ShouldBeNil)

				converting := dbFile + storeConvertingSuffix
				So(os.Mkdir(converting, logStoreDirPerm), ShouldBeNil)
				So(os.WriteFile(filepath.Join(converting, storeConvertSourceFile), []byte(other),
					dbFilePermission), ShouldBeNil)

				beside := []string{convertDBName + storeConvertingSuffix, convertLockName}
				if strings.HasPrefix(other, convertDBName) {
					beside = []string{convertDBName + storeConvertingSuffix, convertLockName, other}
				}

				for range 2 {
					_, _, err := openStore(ctx, convertStoreConfig(dbFile, storeKindLog))
					So(err, ShouldWrap, errBadConvertSource)
					So(errors.Is(err, errStoreCorrupt), ShouldBeFalse)
					So(err.Error(), ShouldContainSubstring, other)
					So(sha256Of(filepath.Join(dir, other)), ShouldEqual, original)
					So(besideDBFile(dbFile), ShouldResemble, beside)
					So(dirEntryNames(converting), ShouldResemble, []string{storeConvertSourceFile})
				}
			})
		}

		Convey("when converted, its records take seqs 1 to the highest in the checkpoint header, and a "+
			"change of a converted live job and a re-archive of a converted complete job outrank them", func() {
			var seqs []uint64

			setConvertHook(func(at convertPoint) error {
				if at == convertAtBuilt {
					seqs = convertedSeqs(dbFile + storeConvertingSuffix)
				}

				return nil
			})

			s := openStoreOf(ctx, dbFile, storeKindLog)

			want := make([]uint64, len(seqs))
			for i := range want {
				want[i] = uint64(i + 1)
			}

			So(seqs, ShouldNotBeEmpty)
			So(seqs, ShouldResemble, want)

			again := convertedOutranked(ctx, s, f)
			So(s.close(ctx), ShouldBeNil)

			reopened := openStoreOf(ctx, dbFile, storeKindLog)
			defer func() { So(reopened.close(ctx), ShouldBeNil) }()

			soConvertedOutranked(reopened, f, again)
		})

		Convey("when converted, the sidecar's Processed reaches its Total, which counts the live and complete "+
			"jobs but not their run-state records (E1 test 5)", func() {
			var atBuilt internal.DBUpgradeStatus

			setConvertHook(func(at convertPoint) error {
				if at != convertAtBuilt {
					return nil
				}

				var err error

				atBuilt, _, err = internal.ReadDBUpgradeStatus(dbFile)

				return err
			})

			s := openStoreOf(ctx, dbFile, storeKindLog)
			So(s.close(ctx), ShouldBeNil)

			So(atBuilt.State, ShouldEqual, internal.DBUpgradeConvertState)
			So(atBuilt.Total, ShouldBeGreaterThan, 0)
			So(atBuilt.Processed, ShouldEqual, atBuilt.Total)
		})

		Convey("when a page of the stdout bucket is damaged, which step 4 first reads for a live job, bbolt "+
			"panics and the source is corrupt: starting fails with errStoreCorrupt and the bbolt file is back at "+
			"dbFile unchanged", func() {
			zeroBoltBucketRoot(dbFile, bucketStdO)

			damaged := sha256Of(dbFile)

			_, _, err := openStore(ctx, convertStoreConfig(dbFile, storeKindLog))
			So(err, ShouldWrap, errStoreCorrupt)
			So(err, ShouldWrap, errBoltPanicked)
			So(sha256Of(dbFile), ShouldEqual, damaged)
			So(besideDBFile(dbFile), ShouldResemble, []string{convertDBName, convertLockName})
		})

		Convey("when a page of the run-state bucket is damaged, which step 4 reads with the live jobs, bbolt "+
			"panics and the source is corrupt: starting fails with errStoreCorrupt and the bbolt file is back at "+
			"dbFile unchanged", func() {
			zeroBoltBucketRoot(dbFile, bucketJobRunState)

			damaged := sha256Of(dbFile)

			_, _, err := openStore(ctx, convertStoreConfig(dbFile, storeKindLog))
			So(err, ShouldWrap, errStoreCorrupt)
			So(err, ShouldWrap, errBoltPanicked)
			So(sha256Of(dbFile), ShouldEqual, damaged)
			So(besideDBFile(dbFile), ShouldResemble, []string{convertDBName, convertLockName})
		})

		Convey("when a page of the live jobs bucket is damaged, which step 4 counts before it adds any job, bbolt "+
			"panics and the source is corrupt: starting fails with errStoreCorrupt and the bbolt file is back at "+
			"dbFile unchanged", func() {
			zeroBoltBucketRoot(dbFile, bucketJobsLive)

			damaged := sha256Of(dbFile)

			_, _, err := openStore(ctx, convertStoreConfig(dbFile, storeKindLog))
			So(err, ShouldWrap, errStoreCorrupt)
			So(err, ShouldWrap, errBoltPanicked)
			So(sha256Of(dbFile), ShouldEqual, damaged)
			So(besideDBFile(dbFile), ShouldResemble, []string{convertDBName, convertLockName})
		})

		Convey("when the page of bbolt's top-level root bucket is damaged, bbolt panics reading the schema "+
			"stamp and the source is corrupt from step 0, changing nothing", func() {
			zeroBoltTopRoot(dbFile)

			damaged := sha256Of(dbFile)

			_, _, err := openStore(ctx, convertStoreConfig(dbFile, storeKindLog))
			So(err, ShouldWrap, errStoreCorrupt)
			So(sha256Of(dbFile), ShouldEqual, damaged)
			So(besideDBFile(dbFile), ShouldResemble, []string{convertDBName, convertLockName})
		})

		Convey("when a leaf of bbolt's top-level root bucket that does not hold the schema stamp is damaged, "+
			"bbolt panics looking up a bucket in step 4 and the source is corrupt: starting fails with "+
			"errStoreCorrupt and the bbolt file is back at dbFile unchanged", func() {
			zeroBoltTopLeafWithoutMeta(dbFile)

			damaged := sha256Of(dbFile)

			_, _, err := openStore(ctx, convertStoreConfig(dbFile, storeKindLog))
			So(err, ShouldWrap, errStoreCorrupt)
			So(sha256Of(dbFile), ShouldEqual, damaged)
			So(besideDBFile(dbFile), ShouldResemble, []string{convertDBName, convertLockName})
		})

		Convey("when bbolt's meta pages are both damaged, the source is corrupt from step 0", func() {
			damageBoltMetaPages(dbFile)

			damaged := sha256Of(dbFile)

			_, _, err := openStore(ctx, convertStoreConfig(dbFile, storeKindLog))
			So(err, ShouldWrap, errStoreCorrupt)
			So(sha256Of(dbFile), ShouldEqual, damaged)
			So(besideDBFile(dbFile), ShouldResemble, []string{convertDBName, convertLockName})
		})

		Convey("when bbolt reads a record the conversion cannot, the source is not corrupt: starting fails with "+
			"errBoltUnconvertible naming the problem and changes nothing", func() {
			job := encodedTestJob(testDBJob("echo convert misfiled", convertRG1))

			for _, c := range []struct {
				name   string
				bucket []byte
				key    string
				value  []byte
				says   string
			}{
				{"a complete record that cannot be decoded", bucketJobsComplete, convertNeverKey,
					[]byte("not a job"), convertNeverKey},
				{"a complete record whose key is not a job key", bucketJobsComplete, "not a job key", job,
					string(bucketJobsComplete) + " key"},
				{"a live record whose key is not a job key", bucketJobsLive, "not a job key", job,
					string(bucketJobsLive) + " key"},
				{"a limit of fewer than 8 bytes", bucketLGs, convertLimitGroup, []byte{0, 0, 3}, convertLimitGroup},
				{"a limit of 7 bytes", bucketLGs, convertLimitGroup, make([]byte, limitGroupBytes-1), convertLimitGroup},
			} {
				Convey(c.name, func() {
					putBoltValue(dbFile, c.bucket, c.key, c.value)

					damaged := sha256Of(dbFile)

					_, _, err := openStore(ctx, convertStoreConfig(dbFile, storeKindLog))
					So(err, ShouldWrap, errBoltUnconvertible)
					So(errors.Is(err, errStoreCorrupt), ShouldBeFalse)
					So(err.Error(), ShouldContainSubstring, c.says)
					So(sha256Of(dbFile), ShouldEqual, damaged)
					So(besideDBFile(dbFile), ShouldResemble, []string{convertDBName, convertLockName})
				})
			}
		})

		Convey("when dbFile's pre name is a dangling symlink, starting fails with errBoltPreExists and leaves it",
			func() {
				pre := dbFile + convertPreSuffix
				nowhere := filepath.Join(dir, "nowhere")
				So(os.Symlink(nowhere, pre), ShouldBeNil)

				_, _, err := openStore(ctx, convertStoreConfig(dbFile, storeKindLog))
				So(err, ShouldWrap, errBoltPreExists)
				So(sha256Of(dbFile), ShouldEqual, original)

				target, err := os.Readlink(pre)
				So(err, ShouldBeNil)
				So(target, ShouldEqual, nowhere)
			})

		Convey("when a regular file is at .converting, no conversion left it: it is removed and dbFile "+
			"converts", func() {
			So(os.WriteFile(dbFile+storeConvertingSuffix, []byte("stray"), dbFilePermission), ShouldBeNil)

			So(convertedAnswers(ctx, dbFile, f), ShouldResemble, fromBolt)
			So(besideDBFile(dbFile), ShouldResemble, []string{convertDBName, convertPreName, convertLockName})
		})

		Convey("when dbFile is a FIFO, it is not taken for bbolt, and opening it returns rather than blocks", func() {
			So(os.Remove(dbFile), ShouldBeNil)
			So(syscall.Mkfifo(dbFile, dbFilePermission), ShouldBeNil)

			opened := make(chan error, 1)

			go func() {
				_, _, err := openStore(ctx, convertStoreConfig(dbFile, storeKindLog))
				opened <- err
			}()

			select {
			case err := <-opened:
				So(err, ShouldNotBeNil)
				So(besideDBFile(dbFile), ShouldResemble, []string{convertDBName, convertLockName})
			case <-time.After(fifoOpenWait):
				So("the open blocked", ShouldBeEmpty)
			}
		})

		Convey("when the source cannot be read, the error is unreadable, not corrupt, and nothing changes", func() {
			So(os.Chmod(dbFile, 0), ShouldBeNil)
			Reset(func() { So(os.Chmod(dbFile, dbFilePermission), ShouldBeNil) })

			_, _, err := openStore(ctx, convertStoreConfig(dbFile, storeKindLog))
			So(err, ShouldWrap, ErrDBUnreadable)
			So(besideDBFile(dbFile), ShouldResemble, []string{convertDBName, convertLockName})
		})
	})
}

// sha256Of returns the hex sha256 of the file at path.
func sha256Of(path string) string {
	content, err := os.ReadFile(path)
	So(err, ShouldBeNil)

	sum := sha256.Sum256(content)

	return hex.EncodeToString(sum[:])
}

// soHistoryBucketsCopied asserts that the history.bolt of the closed store at
// dbFile holds the entries of the history buckets of the bbolt file at
// boltFile (other than jobscomplete, whose std a conversion drops), and
// keyRepgroups holds repgroupToKey's reversed.
func soHistoryBucketsCopied(boltFile, dbFile string) {
	history := filepath.Join(dbFile, historyBoltFile)

	for _, name := range [][]byte{bucketRTK, bucketRGs, bucketDTK, bucketRDTK, bucketEndTimeToKey,
		bucketRGEndTime, bucketJobRAM, bucketJobDisk, bucketJobSecs} {
		So(string(name)+": "+fmt.Sprint(boltBucketEntries(history, name)), ShouldEqual,
			string(name)+": "+fmt.Sprint(boltBucketEntries(boltFile, name)))
	}

	lookups := boltBucketKeys(boltFile, bucketRTK)
	reversed := make([]string, 0, len(lookups))

	for _, lookup := range lookups {
		rg, key, _ := strings.Cut(lookup, dbDelimiter)
		reversed = append(reversed, key+dbDelimiter+rg)
	}

	slices.Sort(reversed)
	So(boltBucketKeys(history, bucketKeyRGs), ShouldResemble, reversed)

	if seen := boltBucketKeys(boltFile, bucketDepGroups); len(seen) > 0 {
		So(boltBucketKeys(history, bucketDepGroups), ShouldResemble, seen)
	}
}

// boltBucketEntries returns the key=value entries of the bucket name in the
// closed bbolt file at path, in key order; none if it has no such bucket.
func boltBucketEntries(path string, name []byte) []string {
	b, err := bolt.Open(path, dbFilePermission, &bolt.Options{ReadOnly: true})
	So(err, ShouldBeNil)

	defer func() { So(b.Close(), ShouldBeNil) }()

	var entries []string

	So(b.View(func(tx *bolt.Tx) error {
		bucket := tx.Bucket(name)
		if bucket == nil {
			return nil
		}

		return bucket.ForEach(func(k, v []byte) error {
			entries = append(entries, fmt.Sprintf("%q=%q", k, v))

			return nil
		})
	}), ShouldBeNil)

	return entries
}

// boltBucketKeys returns the keys of the bucket name in the closed bbolt file
// at path, in key order.
func boltBucketKeys(path string, name []byte) []string {
	entries := boltBucketEntries(path, name)
	keys := make([]string, 0, len(entries))

	for _, entry := range entries {
		quoted, _, _ := strings.Cut(entry, "=")

		key, err := strconv.Unquote(quoted)
		So(err, ShouldBeNil)

		keys = append(keys, key)
	}

	return keys
}

// besideDBFile returns the names of the entries in dbFile's directory that
// start with dbFile's name, sorted.
func besideDBFile(dbFile string) []string {
	entries, err := os.ReadDir(filepath.Dir(dbFile))
	So(err, ShouldBeNil)

	var names []string

	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), filepath.Base(dbFile)) {
			names = append(names, entry.Name())
		}
	}

	return names
}

// crashConvertAt makes the nth conversion hook call at point fail with
// errConvertTestCrash, as a crash there would end the conversion, until the
// Convey block ends.
func crashConvertAt(point convertPoint, nth int) {
	calls := 0

	setConvertHook(func(at convertPoint) error {
		if at != point {
			return nil
		}

		calls++
		if calls == nth {
			return errConvertTestCrash
		}

		return nil
	})
}

// setConvertHook makes fn the convert hook until the Convey block ends.
func setConvertHook(fn func(at convertPoint) error) {
	logConvertHook.Store(&fn)
	Reset(func() { logConvertHook.Store(nil) })
}

// convertStoreConfig is the storeConfig of a store of kind at dbFile, with no
// development wipe.
func convertStoreConfig(dbFile string, kind storeKind) storeConfig {
	return storeConfig{dbFile: dbFile, dbBkFile: dbFile + ".bk", deployment: internal.Development, kind: kind}
}

// soResumable asserts that err, a failed start's, is not a corrupt store's,
// and that the start left the conversion of the bbolt file at dbFile for the
// next to resume: no dbFile, the bbolt file at its pre name, and .converting
// holding SOURCE, naming it.
func soResumable(dbFile string, err error) {
	So(err, ShouldNotBeNil)
	So(errors.Is(err, errStoreCorrupt), ShouldBeFalse)
	So(besideDBFile(dbFile), ShouldResemble,
		[]string{convertPreName, convertDBName + storeConvertingSuffix, convertLockName})

	source, err := os.ReadFile(filepath.Join(dbFile+storeConvertingSuffix, storeConvertSourceFile))
	So(err, ShouldBeNil)
	So(string(source), ShouldEqual, convertPreName)
}

// failWritesTo makes writes to the file at path, which this process has
// open, fail with ENOSPC, as on a full disk, by putting /dev/full in place of
// each of its descriptors open on it.
func failWritesTo(path string) error {
	full, err := os.OpenFile("/dev/full", os.O_WRONLY, 0)
	if err != nil {
		return err
	}

	defer full.Close()

	entries, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		return err
	}

	replaced := 0

	for _, entry := range entries {
		target, errl := os.Readlink(filepath.Join("/proc/self/fd", entry.Name()))
		if errl != nil || target != path {
			continue
		}

		fd, errc := strconv.Atoi(entry.Name())
		if errc != nil {
			return errc
		}

		if err = unix.Dup3(int(full.Fd()), fd, unix.O_CLOEXEC); err != nil {
			return err
		}

		replaced++
	}

	if replaced == 0 {
		return fmt.Errorf("%w: no descriptor of this process is open on %s", os.ErrNotExist, path)
	}

	return nil
}

// zeroBoltBucketRoot pads the bucket name of the closed bbolt file at path
// with values under keys no job has, until it has pages of its own, then
// zeroes its root page.
func zeroBoltBucketRoot(path string, name []byte) {
	b, err := bolt.Open(path, dbFilePermission, nil)
	So(err, ShouldBeNil)

	pageSize := int64(b.Info().PageSize)

	var root int64

	So(b.Update(func(tx *bolt.Tx) error {
		bucket := tx.Bucket(name)

		for i := range 8 {
			if errp := bucket.Put(fmt.Appendf(nil, "zz-padding-%d", i), make([]byte, pageSize/2)); errp != nil {
				return errp
			}
		}

		return nil
	}), ShouldBeNil)
	So(b.View(func(tx *bolt.Tx) error {
		root = int64(tx.Bucket(name).Root()) //nolint:gosec // a page id

		return nil
	}), ShouldBeNil)
	So(b.Close(), ShouldBeNil)
	So(root, ShouldBeGreaterThan, 1)

	zeroBoltPage(path, root, pageSize)
}

// zeroBoltPage zeroes page id, of pageSize bytes, of the closed bbolt file at
// path.
func zeroBoltPage(path string, id, pageSize int64) {
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	So(err, ShouldBeNil)

	_, err = f.WriteAt(make([]byte, pageSize), id*pageSize)
	So(err, ShouldBeNil)
	So(f.Close(), ShouldBeNil)
}

// putBoltValue puts value under key in bucket of the closed bbolt file at
// path, first deleting key from jobscomplete and jobslive.
func putBoltValue(path string, bucket []byte, key string, value []byte) {
	b, err := bolt.Open(path, dbFilePermission, nil)
	So(err, ShouldBeNil)

	So(b.Update(func(tx *bolt.Tx) error {
		err := errors.Join(tx.Bucket(bucketJobsComplete).Delete([]byte(key)),
			tx.Bucket(bucketJobsLive).Delete([]byte(key)))

		return errors.Join(err, tx.Bucket(bucket).Put([]byte(key), value))
	}), ShouldBeNil)
	So(b.Close(), ShouldBeNil)
}

// dirEntryNames returns the names of the entries in dir, sorted.
func dirEntryNames(dir string) []string {
	entries, err := os.ReadDir(dir)
	So(err, ShouldBeNil)

	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name())
	}

	return names
}

// convertedSeqs returns the seq of each record of the checkpoint of the
// converted store in dir and of each of its history.bolt completeSeqs, sorted,
// asserting the checkpoint header's max seq is the highest and its counts of
// live jobs and records are its spec records and records.
func convertedSeqs(dir string) []uint64 {
	header, offsets, f, err := openCheckpoint(dir, convertWalFrom)
	So(err, ShouldBeNil)

	defer func() { So(f.Close(), ShouldBeNil) }()

	var (
		seqs  []uint64
		specs uint64
	)

	So(eachCheckpointRecord(f, offsets, convertWalFrom|checkpointRefFlag, func(_ wal.Ref, rec []byte) error {
		parsed, errp := parseLogRecord(rec)
		seqs = append(seqs, parsed.seq)

		if parsed.typ == logRecSpec {
			specs++
		}

		return errp
	}), ShouldBeNil)

	So(header.live, ShouldEqual, specs)
	So(header.records, ShouldEqual, len(seqs))

	b, err := bolt.Open(filepath.Join(dir, historyBoltFile), dbFilePermission, &bolt.Options{ReadOnly: true})
	So(err, ShouldBeNil)

	defer func() { So(b.Close(), ShouldBeNil) }()

	So(b.View(func(tx *bolt.Tx) error {
		So(historyMaxSeq(tx), ShouldBeGreaterThan, 0)

		return tx.Bucket(bucketCompleteSeq).ForEach(func(_, v []byte) error {
			seqs = append(seqs, binary.BigEndian.Uint64(v))

			return nil
		})
	}), ShouldBeNil)

	slices.Sort(seqs)
	So(header.maxSeq, ShouldEqual, seqs[len(seqs)-1])

	return seqs
}

// openStoreOf opens the store of kind at dbFile, with no development wipe, and
// asserts it opened. The caller closes it.
func openStoreOf(ctx context.Context, dbFile string, kind storeKind) store {
	s, _, err := openStore(ctx, convertStoreConfig(dbFile, kind))
	So(err, ShouldBeNil)

	return s
}

// zeroBoltTopRoot zeroes the page of the closed bbolt file at path that holds
// its top-level root bucket, so that looking up any bucket, the meta bucket
// with the schema stamp first, panics.
func zeroBoltTopRoot(path string) {
	pageSize, root := boltTopRoot(path)
	zeroBoltPage(path, root, pageSize)
}

// boltTopRoot returns the page size of the closed bbolt file at path and the
// page id of its top-level root bucket, which holds every named bucket.
func boltTopRoot(path string) (pageSize, root int64) {
	b, err := bolt.Open(path, dbFilePermission, &bolt.Options{ReadOnly: true})
	So(err, ShouldBeNil)

	So(b.View(func(tx *bolt.Tx) error {
		root = int64(tx.Cursor().Bucket().Root()) //nolint:gosec // a page id

		return nil
	}), ShouldBeNil)

	pageSize = int64(b.Info().PageSize)
	So(b.Close(), ShouldBeNil)
	So(root, ShouldBeGreaterThan, 1)

	return pageSize, root
}

// zeroBoltTopLeafWithoutMeta pads the inline buckets of the closed bbolt file
// at path until its top-level root bucket splits into a branch page over
// leaves, then zeroes a leaf that does not hold the meta bucket, so the schema
// stamp still reads but looking up some other bucket panics.
func zeroBoltTopLeafWithoutMeta(path string) {
	b, err := bolt.Open(path, dbFilePermission, nil)
	So(err, ShouldBeNil)

	So(b.Update(func(tx *bolt.Tx) error {
		for _, name := range [][]byte{bucketEnvs, bucketLGs, bucketRGs, bucketDTK, bucketRDTK, bucketJobRAM,
			bucketJobDisk, bucketJobSecs} {
			bucket := tx.Bucket(name)
			if bucket == nil {
				continue
			}

			for i := range 3 {
				if errp := bucket.Put(fmt.Appendf(nil, "zz-padding-%d", i), make([]byte, 200)); errp != nil {
					return errp
				}
			}
		}

		return nil
	}), ShouldBeNil)
	So(b.Close(), ShouldBeNil)

	pageSize, root := boltTopRoot(path)
	children := boltBranchChildren(path, root, pageSize)
	So(len(children), ShouldBeGreaterThan, 1)

	metaChild := 0

	for i, child := range children {
		if bytes.Compare(child.first, bucketMeta) <= 0 {
			metaChild = i
		}
	}

	victim := children[0].id
	if metaChild == 0 {
		victim = children[1].id
	}

	zeroBoltPage(path, victim, pageSize)
}

// boltBranchChildren returns the children of branch page id, of pageSize
// bytes, of the closed bbolt file at path, asserting it is a branch page.
func boltBranchChildren(path string, id, pageSize int64) []boltBranchChild {
	const (
		pageHeaderSize    = 16
		branchElementSize = 16
		branchPageFlag    = 0x01
	)

	f, err := os.Open(path)
	So(err, ShouldBeNil)

	page := make([]byte, pageSize)
	_, err = f.ReadAt(page, id*pageSize)
	So(err, ShouldBeNil)
	So(f.Close(), ShouldBeNil)

	So(binary.LittleEndian.Uint16(page[8:]), ShouldEqual, branchPageFlag)

	count := int(binary.LittleEndian.Uint16(page[10:]))
	children := make([]boltBranchChild, count)

	for i := range count {
		at := pageHeaderSize + i*branchElementSize
		pos := int(binary.LittleEndian.Uint32(page[at:]))
		size := int(binary.LittleEndian.Uint32(page[at+4:]))
		children[i] = boltBranchChild{
			id:    int64(binary.LittleEndian.Uint64(page[at+8:])), //nolint:gosec // a page id
			first: page[at+pos : at+pos+size],
		}
	}

	return children
}

// damageBoltMetaPages flips a byte inside each of the bbolt file's two meta
// pages, past their magic, so both fail their checksum.
func damageBoltMetaPages(path string) {
	b, err := os.OpenFile(path, os.O_RDWR, 0)
	So(err, ShouldBeNil)

	pageSize := int64(os.Getpagesize())

	for page := range int64(2) {
		at := page*pageSize + boltMetaMagicOffset + 8

		one := make([]byte, 1)
		_, err = b.ReadAt(one, at)
		So(err, ShouldBeNil)

		one[0] ^= 0xff
		_, err = b.WriteAt(one, at)
		So(err, ShouldBeNil)
	}

	So(b.Close(), ShouldBeNil)
}

// encodedTestJob is job's bbolt record.
func encodedTestJob(job *Job) []byte {
	var encoded []byte

	So(codec.NewEncoderBytes(&encoded, new(codec.BincHandle)).Encode(job), ShouldBeNil)

	return encoded
}

// boltAnswersOfCopy copies the bbolt file at path into a new directory, opens
// the copy as bbolt and returns convertAnswers of it.
func boltAnswersOfCopy(t *testing.T, ctx context.Context, path string, f convertFixture) map[string][32]byte {
	t.Helper()

	dbFile := filepath.Join(t.TempDir(), convertDBName)
	copyTestFile(path, dbFile)

	s := openStoreOf(ctx, dbFile, storeKindBolt)
	defer func() { So(s.close(ctx), ShouldBeNil) }()

	return convertAnswers(ctx, s, f)
}

// copyTestFile copies the file at src to dst.
func copyTestFile(src, dst string) {
	in, err := os.Open(src)
	So(err, ShouldBeNil)

	defer func() { So(in.Close(), ShouldBeNil) }()

	out, err := os.Create(dst)
	So(err, ShouldBeNil)

	_, err = io.Copy(out, in)
	So(err, ShouldBeNil)
	So(out.Close(), ShouldBeNil)
}

// convertedAnswers opens dbFile as the log store, converting it, and returns
// convertAnswers of it.
func convertedAnswers(ctx context.Context, dbFile string, f convertFixture) map[string][32]byte {
	s := openStoreOf(ctx, dbFile, storeKindLog)
	defer func() { So(s.close(ctx), ShouldBeNil) }()

	return convertAnswers(ctx, s, f)
}

// convertAnswers asks s every read of the store interface (the A5 query set)
// about f's keys, rep groups, dep groups, envs, limits and req groups, and
// returns each answer's storeDigest by query.
func convertAnswers(ctx context.Context, s store, f convertFixture) map[string][32]byte {
	a := make(map[string][32]byte)

	jobs, _, err := s.recoverIncompleteJobs()
	a["recoverIncompleteJobs"] = storeDigest(jobs, err)

	for _, key := range f.keys {
		live, errl := s.checkIfLive(key)
		a["checkIfLive "+key] = storeDigest(live, errl)
		complete, errc := s.checkIfComplete(key)
		a["checkIfComplete "+key] = storeDigest(complete, errc)
		stdo, stde := s.retrieveJobStd(ctx, key)
		a["retrieveJobStd "+key] = storeDigest(stdo, stde)
	}

	byKeys, err := s.retrieveCompleteJobsByKeys(f.keys)
	a["retrieveCompleteJobsByKeys"] = storeDigest(byKeys, err)

	rgs := []string{convertRG1, convertRG2, convertDupRG, convertNeverRG}
	for _, rg := range rgs {
		convertRepGroupAnswers(s, rg, a)
	}

	for name, cutoff := range map[string]time.Time{"every": {}, "later": f.t0.Add(15 * time.Second)} {
		recent, errr := s.retrieveCompleteJobsRecent(cutoff)
		a["retrieveCompleteJobsRecent "+name] = storeDigest(recent, errr)
	}

	last, err := s.retrieveLastCompletionTimeByRepGroup(rgs)
	a["retrieveLastCompletionTimeByRepGroup"] = storeDigest(last, err)

	repGroups, err := s.retrieveRepGroups()
	a["retrieveRepGroups"] = storeDigest(repGroups, err)

	for _, envKey := range f.envKeys {
		a["retrieveEnv "+envKey] = storeDigest(s.retrieveEnv(ctx, envKey))
	}

	limitGroups := []string{convertLimitGroup, "convert-never-limit"}
	for _, group := range limitGroups {
		a["retrieveLimitGroup "+group] = storeDigest(s.retrieveLimitGroup(ctx, group))
	}

	limits, err := s.retrieveStoredLimits(limitGroups)
	a["retrieveStoredLimits"] = storeDigest(limits, err)

	seen, err := s.depGroupsEverSeen([]string{convertDepGroup, convertNeverDepGroup})
	a["depGroupsEverSeen"] = storeDigest(seen, err)

	for _, reqGroup := range f.reqGroups {
		a["refreshReqGroupRecs "+reqGroup] = storeDigest(s.refreshReqGroupRecs(reqGroup))
	}

	return a
}

// convertRepGroupAnswers adds to a the digests of the rep-group reads of rg.
func convertRepGroupAnswers(s store, rg string, a map[string][32]byte) {
	byRG, err := s.retrieveCompleteJobsByRepGroup(rg)
	a["retrieveCompleteJobsByRepGroup "+rg] = storeDigest(byRG, err)

	page, err := s.retrieveOldestCompleteJobsByRepGroup(rg, completeJobsQuery{
		group: func(f *archivedJobFacets) (string, bool) { return fmt.Sprint(f.State), true },
		limit: func(string) int { return 2 },
	})
	So(err, ShouldBeNil)

	a["retrieveOldestCompleteJobsByRepGroup "+rg] = storeDigest(page.jobs, page.counted, page.fetched)

	for _, details := range []bool{false, true} {
		status, errs := s.retrieveCompleteJobStatusByRepGroup(rg, details)
		So(errs, ShouldBeNil)

		a[fmt.Sprintf("retrieveCompleteJobStatusByRepGroup %s %t", rg, details)] = storeDigest(
			historyStatusSummaryOf(status))
	}

	a["repGroupHasHistory "+rg] = storeDigest(s.repGroupHasHistory(rg))

	budget := newArchivedBytesBudget()
	err = s.spendArchivedBytesByRepGroup(rg, budget)
	a["spendArchivedBytesByRepGroup "+rg] = storeDigest(budget.priced, err)
}

// historyStatusSummaryOf is a RepGroupStatus as a string, its times as
// instants, so that statuses whose times differ only in location compare.
func historyStatusSummaryOf(status *RepGroupStatus) string {
	return fmt.Sprintf("%v %+v %+v %+v %+v %d %d", status.Counts, status.Memory, status.Disk, status.Walltime,
		status.CPUtime, status.StartTime.UnixNano(), status.EndTime.UnixNano())
}

// convertedOutranked changes the state of f's reserved job, which s converted
// live, and adds and archives again f's done1 job, which s converted complete,
// with a later end time, returning the archived job.
func convertedOutranked(ctx context.Context, s store, f convertFixture) *Job {
	jobs, _, err := s.recoverIncompleteJobs()
	So(err, ShouldBeNil)

	var reserved *Job

	for _, job := range jobs {
		if job.Key() == f.keys[2] {
			reserved = job
		}
	}

	So(reserved, ShouldNotBeNil)

	reserved.State = JobStateRunning
	reserved.Host = "convert-later-host"
	So(s.updateJobRunStateDurable(reserved), ShouldBeNil)

	again := testDBJob("echo convert done1", convertRG1)
	_, err = s.storeNewJobsGuarded(ctx, []*Job{again}, false, nil)
	So(err, ShouldBeNil)

	archiveTestJob(s, again, f.t0.Add(time.Minute), f.t0.Add(2*time.Minute))

	return again
}

// soConvertedOutranked asserts s holds what convertedOutranked wrote.
func soConvertedOutranked(s store, f convertFixture, again *Job) {
	jobs, _, err := s.recoverIncompleteJobs()
	So(err, ShouldBeNil)

	hosts := make(map[string]string)
	for _, job := range jobs {
		hosts[job.Key()] = job.Host
	}

	So(hosts[f.keys[2]], ShouldEqual, "convert-later-host")

	complete, err := s.retrieveCompleteJobsByKeys([]string{again.Key()})
	So(err, ShouldBeNil)
	So(complete, ShouldHaveLength, 1)
	So(complete[0].EndTime.Equal(f.t0.Add(2*time.Minute)), ShouldBeTrue)
}

func TestServeReportsPostConversionStartupUntilTokenReady(t *testing.T) {
	Convey("Serve on the log store keeps a live startup-progress sidecar after converting a bbolt DB until the "+
		"token is ready", t, func() {
		ctx := context.Background()
		_, serverConfig, _, _, _ := jobqueueTestInit(true)
		serverConfig.dontWipeDevDB = true
		serverConfig.storeKind = storeKindLog

		writeConvertFixture(ctx, serverConfig.DBFile)
		initialTokenWritten := prepareFIFOBackedToken(t, serverConfig.TokenFile)

		serveResult := make(chan serveStartupResult, 1)

		go func() {
			server, _, token, err := Serve(ctx, serverConfig)
			serveResult <- serveStartupResult{server: server, token: token, err: err}
		}()

		So(waitForFIFOInitialToken(initialTokenWritten), ShouldBeNil)

		status, found := waitForDBUpgradeStatusState(serverConfig.DBFile, internal.DBStartupRecoveryState,
			2*time.Second)

		tokenInfo, statErr := os.Stat(serverConfig.TokenFile)
		So(statErr, ShouldBeNil)
		So(tokenInfo.Size(), ShouldEqual, 0)
		So(found, ShouldBeTrue)
		So(status.State, ShouldEqual, internal.DBStartupRecoveryState)
		So(status.PID, ShouldEqual, os.Getpid())

		tokenRead := readFIFOAsync(serverConfig.TokenFile)

		result := waitForServeStartup(t, serveResult)
		if result.server != nil {
			defer result.server.Stop(ctx, true)
		}

		So(result.err, ShouldBeNil)
		So(result.server.db.upgradedOnOpen(), ShouldBeTrue)

		tokenReadResult := waitForFIFORead(t, serverConfig.TokenFile, tokenRead)
		So(tokenReadResult.err, ShouldBeNil)
		So(tokenReadResult.payload, ShouldResemble, result.token)

		So(waitForServing(result.server), ShouldBeTrue)

		_, _, err := internal.ReadDBUpgradeStatus(serverConfig.DBFile)
		So(os.IsNotExist(err), ShouldBeTrue)
	})
}

// boltBranchChild is a child of a bbolt branch page: the page id it points to
// and the first key it holds.
type boltBranchChild struct {
	id    int64
	first []byte
}

// depGroupAnswer is what a store answers about dependencies on dep groups d,
// e, f, x and x + dbDelimiter + y: whether each is ever seen, and the
// dependency keys and WaitingForDepGroups resolved for a job depending on
// each.
type depGroupAnswer struct {
	Seen    map[string]bool
	Deps    map[string][]string
	Waiting map[string][]string
}

// depGroupAnswers returns s's depGroupAnswer, with no live dep group member.
func depGroupAnswers(ctx context.Context, s store) depGroupAnswer {
	groups := []string{"d", "e", "f", "x", "x" + dbDelimiter + "y"}
	a := depGroupAnswer{Deps: map[string][]string{}, Waiting: map[string][]string{}}

	var err error

	a.Seen, err = s.depGroupsEverSeen(groups)
	So(err, ShouldBeNil)

	for _, group := range groups {
		job := testDBJob("echo convert depends on "+group, convertRG2)
		job.Dependencies = Dependencies{NewDepGroupDependency(group)}

		resolved, errr := s.resolveDependencies(ctx, []*Job{job}, newDepGroupMembers())
		So(errr, ShouldBeNil)
		So(resolved, ShouldHaveLength, 1)

		a.Deps[group] = resolved[0].deps
		a.Waiting[group] = job.WaitingForDepGroups
	}

	return a
}

// depGroupAnswersOfCopy is depGroupAnswers of a copy of the bbolt file at
// path opened as bbolt.
func depGroupAnswersOfCopy(t *testing.T, ctx context.Context, path string) depGroupAnswer {
	t.Helper()

	dbFile := filepath.Join(t.TempDir(), convertDBName)
	copyTestFile(path, dbFile)

	s := openStoreOf(ctx, dbFile, storeKindBolt)
	defer func() { So(s.close(ctx), ShouldBeNil) }()

	return depGroupAnswers(ctx, s)
}

func TestStoreConvertSchemas(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()
	end := time.Unix(1_750_000_000, 0)

	Convey("Given bbolt DBs whose complete records hold StdOutC and StdErrC (E1 test 2)", t, func() {
		withStd := completedTestJob("echo convert std", end)
		withStd.StdOutC = []byte("kept out")
		withStd.StdErrC = []byte("kept err")
		without := completedTestJob("echo convert no std", end)

		complete := map[string][]byte{withStd.Key(): encodedTestJob(withStd), without.Key(): encodedTestJob(without)}

		for _, stamp := range [][]byte{nil, schemaStamp(1), schemaStamp(dbSchemaVersionRunState)} {
			Convey(fmt.Sprintf("stamped %x, when converted, no history record holds std and the complete count is "+
				"unchanged", stamp), func() {
				dbFile := filepath.Join(t.TempDir(), convertDBName)
				writeRawBoltDB(dbFile, stamp, map[string]map[string][]byte{string(bucketJobsComplete): complete})

				s := openStoreOf(ctx, dbFile, storeKindLog)
				for _, key := range []string{withStd.Key(), without.Key()} {
					done, err := s.checkIfComplete(key)
					So(err, ShouldBeNil)
					So(done, ShouldBeTrue)
				}

				So(s.close(ctx), ShouldBeNil)

				records := historyBoltRecords(dbFile)
				So(records, ShouldHaveLength, len(complete))

				for _, job := range records {
					So(job.StdOutC, ShouldBeEmpty)
					So(job.StdErrC, ShouldBeEmpty)
				}
			})
		}
	})

	Convey("Given a schema 0 bbolt DB with no jobslive and no depgroups bucket, whose depgroupToKey holds "+
		"groups d and e, each with one complete member (E1 test 2)", t, func() {
		d := completedTestJob("echo convert d", end)
		d.DepGroups = []string{"d"}
		e := completedTestJob("echo convert e", end)
		e.DepGroups = []string{"e"}

		dbFile := filepath.Join(t.TempDir(), convertDBName)
		writeRawBoltDB(dbFile, nil, map[string]map[string][]byte{
			string(bucketJobsComplete): {d.Key(): encodedTestJob(d), e.Key(): encodedTestJob(e)},
			string(bucketDTK): {
				"d" + dbDelimiter + d.Key():                     nil,
				"e" + dbDelimiter + e.Key():                     nil,
				"x" + dbDelimiter + "y" + dbDelimiter + e.Key(): nil,
			},
		})

		fromBolt := depGroupAnswersOfCopy(t, ctx, dbFile)

		Convey("when converted, jobs depending on d, e and never-seen f resolve, and those groups are seen, as "+
			"on a copy opened by initDB", func() {
			s := openStoreOf(ctx, dbFile, storeKindLog)
			So(depGroupAnswers(ctx, s), ShouldResemble, fromBolt)
			So(fromBolt.Seen, ShouldResemble, map[string]bool{"d": true, "e": true, "f": false, "x": true,
				"x" + dbDelimiter + "y": false})
			So(s.close(ctx), ShouldBeNil)

			soHistoryBucketsCopied(dbFile+convertPreSuffix, dbFile)
			So(boltBucketKeys(filepath.Join(dbFile, historyBoltFile), bucketDepGroups), ShouldResemble,
				[]string{"d", "e", "x"})
		})
	})
}

// completedTestJob returns a job of cmd in convertRG1, complete at end.
func completedTestJob(cmd string, end time.Time) *Job {
	job := testDBJob(cmd, convertRG1)
	job.State = JobStateComplete
	job.Exited = true
	job.StartTime = end.Add(-time.Second)
	job.EndTime = end

	return job
}

// schemaStamp is a schema version stamp of version.
func schemaStamp(version uint64) []byte {
	return binary.BigEndian.AppendUint64(nil, version)
}

// writeRawBoltDB writes a new bbolt file at path holding buckets: each named
// bucket with its keys and values. A nil stamp leaves it unversioned.
func writeRawBoltDB(path string, stamp []byte, buckets map[string]map[string][]byte) {
	b, err := bolt.Open(path, dbFilePermission, nil)
	So(err, ShouldBeNil)

	So(b.Update(func(tx *bolt.Tx) error {
		if stamp != nil {
			buckets[string(bucketMeta)] = map[string][]byte{string(metaKeySchemaVersion): stamp}
		}

		for name, entries := range buckets {
			bucket, errc := tx.CreateBucket([]byte(name))
			if errc != nil {
				return errc
			}

			for k, v := range entries {
				if errp := bucket.Put([]byte(k), v); errp != nil {
					return errp
				}
			}
		}

		return nil
	}), ShouldBeNil)
	So(b.Close(), ShouldBeNil)
}

// historyBoltRecords returns the decoded records of history.bolt's
// jobscomplete in the closed store at dbFile.
func historyBoltRecords(dbFile string) []*Job {
	b, err := bolt.Open(filepath.Join(dbFile, historyBoltFile), dbFilePermission, &bolt.Options{ReadOnly: true})
	So(err, ShouldBeNil)

	defer func() { So(b.Close(), ShouldBeNil) }()

	var jobs []*Job

	So(b.View(func(tx *bolt.Tx) error {
		return tx.Bucket(bucketJobsComplete).ForEach(func(_, v []byte) error {
			job, errd := decodeStoredJob(v, new(codec.BincHandle))
			jobs = append(jobs, job)

			return errd
		})
	}), ShouldBeNil)

	return jobs
}

func TestStoreConvertRefusesSchema(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	for _, c := range []struct {
		name  string
		stamp []byte
		want  error
		says  string
	}{
		{"stamped one past the current schema", schemaStamp(currentDBSchemaVersion + 1), errDBSchemaTooNew,
			"use a newer wr"},
		{"whose stamp is 4 bytes", []byte{0, 0, 0, 1}, errBadDBSchemaVersion, "4 bytes"},
	} {
		Convey("Given a bbolt DB "+c.name+" and a backup, when the manager starts, it is refused, converting, "+
			"renaming and restoring nothing (E1 test 7)", t, func() {
			dbFile := filepath.Join(t.TempDir(), convertDBName)
			writeRawBoltDB(dbFile, c.stamp, map[string]map[string][]byte{string(bucketJobsComplete): {}})
			copyTestFile(dbFile, dbFile+".bk")

			original := sha256Of(dbFile)

			_, _, err := openStore(ctx, convertStoreConfig(dbFile, storeKindLog))
			So(err, ShouldWrap, c.want)
			So(err.Error(), ShouldContainSubstring, c.says)
			So(sha256Of(dbFile), ShouldEqual, original)
			So(sha256Of(dbFile+".bk"), ShouldEqual, original)
			So(besideDBFile(dbFile), ShouldResemble, []string{convertDBName, convertDBName + ".bk", convertLockName})
		})
	}
}

func TestStoreConvertProgress(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given a conversion of a bbolt DB of many jobs in progress (E1 test 5)", t, func() {
		dbFile := filepath.Join(t.TempDir(), convertDBName)
		writeProgressFixture(dbFile)

		var (
			read    []internal.DBUpgradeStatus
			atBuilt internal.DBUpgradeStatus
		)

		setConvertHook(func(at convertPoint) error {
			if at != convertAtJob && at != convertAtBuilt {
				return nil
			}

			status, _, err := internal.ReadDBUpgradeStatus(dbFile)
			if err != nil {
				return err
			}

			if at == convertAtBuilt {
				atBuilt = status
			} else if len(read) == 0 || read[len(read)-1] != status {
				read = append(read, status)
			}

			return nil
		})

		s := openStoreOf(ctx, dbFile, storeKindLog)
		So(s.upgradedOnOpen(), ShouldBeTrue)
		So(s.close(ctx), ShouldBeNil)

		Convey("when the sidecar is read, its State is the conversion's, an upgrade state, and Processed rises to "+
			"Total", func() {
			So(internal.IsDBUpgradeState(internal.DBUpgradeConvertState), ShouldBeTrue)
			So(internal.DBUpgradeConvertState, ShouldEqual, "convert database")

			processed := make([]int, len(read))
			for i, status := range read {
				So(status.State, ShouldEqual, internal.DBUpgradeConvertState)
				So(status.Total, ShouldEqual, convertProgressJobs)

				processed[i] = status.Processed
			}

			So(slices.IsSorted(processed), ShouldBeTrue)
			So(processed[0], ShouldEqual, 0)
			So(slices.ContainsFunc(processed, func(p int) bool { return p > 0 && p < convertProgressJobs }),
				ShouldBeTrue)
			So(atBuilt.State, ShouldEqual, internal.DBUpgradeConvertState)
			So(atBuilt.Processed, ShouldEqual, convertProgressJobs)
			So(atBuilt.Total, ShouldEqual, convertProgressJobs)
		})

		Convey("once the store has opened, the sidecar is gone, and a reopen reports no upgrade", func() {
			_, _, err := internal.ReadDBUpgradeStatus(dbFile)
			So(err, ShouldWrap, os.ErrNotExist)

			reopened := openStoreOf(ctx, dbFile, storeKindLog)
			So(reopened.upgradedOnOpen(), ShouldBeFalse)
			So(reopened.close(ctx), ShouldBeNil)
		})
	})
}

func TestStoreConvertShowsStartBeforeCounting(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given a bbolt DB of many jobs, a manager start converting it shows the conversion has started, in "+
		"the sidecar and the log, before it counts the jobs, whose Total it shows once counted", t, func() {
		dbFile := filepath.Join(t.TempDir(), convertDBName)
		writeProgressFixture(dbFile)

		logPath, serverCtx := managerLogContext(t, ctx)

		var (
			counting, firstJob internal.DBUpgradeStatus
			logged             string
			jobs               int
		)

		setConvertHook(func(at convertPoint) error {
			var err error

			switch {
			case at == convertAtCounting:
				counting, _, err = internal.ReadDBUpgradeStatus(dbFile)
				logged = readLogFile(t, logPath)
			case at == convertAtJob && jobs == 0:
				jobs++
				firstJob, _, err = internal.ReadDBUpgradeStatus(dbFile)
			}

			return err
		})

		s := openStoreOf(serverCtx, dbFile, storeKindLog)
		So(s.close(ctx), ShouldBeNil)

		So(counting.State, ShouldEqual, internal.DBUpgradeConvertState)
		So(counting.Total, ShouldEqual, 0)
		So(logLinesContaining(logged, dbUpgradeStartLogMatch), ShouldHaveLength, 1)
		So(logLinesForPhase(logged, dbUpgradeStepStartLogMatch, internal.DBUpgradeConvertState), ShouldHaveLength, 1)
		So(firstJob.State, ShouldEqual, internal.DBUpgradeConvertState)
		So(firstJob.Total, ShouldEqual, convertProgressJobs)
	})
}

func TestStoreConvertPanickingSource(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given a bbolt DB whose meta pages are valid but a branch page of its complete jobs is zeroed, "+
		"so bbolt panics reading it in step 4, the source is corrupt: starting fails with errStoreCorrupt and "+
		"the bbolt file is back at dbFile unchanged", t, func() {
		dbFile := filepath.Join(t.TempDir(), convertDBName)
		writeProgressFixture(dbFile)
		zeroBoltBranchPage(dbFile)

		damaged := sha256Of(dbFile)

		_, _, err := openStore(ctx, convertStoreConfig(dbFile, storeKindLog))
		So(err, ShouldWrap, errStoreCorrupt)
		So(err.Error(), ShouldContainSubstring, "assertion failed")
		So(sha256Of(dbFile), ShouldEqual, damaged)
		So(besideDBFile(dbFile), ShouldResemble, []string{convertDBName, convertLockName})
	})
}

func TestStoreConvertLogsAsUpgrade(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given a bbolt DB of many jobs, a default manager start converting it logs the conversion as a "+
		"database upgrade, without a line per progress point, and a restart logs no upgrade", t, func() {
		dbFile := filepath.Join(t.TempDir(), convertDBName)
		writeProgressFixture(dbFile)

		logged := openStoreLoggingAsManager(t, ctx, dbFile)

		So(logLinesContaining(logged, dbUpgradeStartLogMatch), ShouldHaveLength, 1)
		So(logLinesForPhase(logged, dbUpgradeStepStartLogMatch, internal.DBUpgradeConvertState),
			ShouldHaveLength, 1)

		done := logLinesForPhase(logged, dbUpgradeStepDoneLogMatch, internal.DBUpgradeConvertState)
		So(done, ShouldHaveLength, 1)
		So(logLineValue(done[0], "processed"), ShouldEqual, strconv.Itoa(convertProgressJobs))
		So(logLinesContaining(logged, dbUpgradeDoneLogMatch), ShouldHaveLength, 1)
		So(logLinesContaining(logged, dbUpgradeProgressLogMatch), ShouldBeEmpty)

		So(openStoreLoggingAsManager(t, ctx, dbFile), ShouldNotContainSubstring, "database upgrade")
	})
}

func TestStoreLongConvertReportsProgress(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("A conversion that outlasts the log interval reports its progress in a default manager's log", t, func() {
		previousInterval := dbUpgradeLogInterval

		Reset(func() { dbUpgradeLogInterval = previousInterval })

		dbUpgradeLogInterval = 0

		dbFile := filepath.Join(t.TempDir(), convertDBName)
		writeProgressFixture(dbFile)

		progress := logLinesContaining(openStoreLoggingAsManager(t, ctx, dbFile), dbUpgradeStillRunningLogMatch)
		So(len(progress), ShouldBeGreaterThanOrEqualTo, convertProgressJobs/dbUpgradeProgressEntries)
		So(progress[0], ShouldContainSubstring, `state="`+internal.DBUpgradeConvertState+`"`)
		So(logLineValue(progress[len(progress)-1], "processed"),
			ShouldEqual, strconv.Itoa(convertProgressJobs/dbUpgradeProgressEntries*dbUpgradeProgressEntries))
	})
}

// writeProgressFixture writes a bbolt file at dbFile holding
// convertProgressJobs complete jobs, as raw records.
func writeProgressFixture(dbFile string) {
	complete := make(map[string][]byte, convertProgressJobs)
	end := time.Unix(1_750_000_000, 0)

	for i := range convertProgressJobs {
		job := completedTestJob(fmt.Sprintf("echo convert progress %d", i), end)
		complete[job.Key()] = encodedTestJob(job)
	}

	writeRawBoltDB(dbFile, schemaStamp(currentDBSchemaVersion),
		map[string]map[string][]byte{string(bucketJobsComplete): complete})
}

// openStoreLoggingAsManager opens the log store at dbFile with logging as a
// default `wr manager start` configures it, closes it, and returns the
// manager log's content.
func openStoreLoggingAsManager(t *testing.T, ctx context.Context, dbFile string) string {
	t.Helper()

	logPath, serverCtx := managerLogContext(t, ctx)

	s := openStoreOf(serverCtx, dbFile, storeKindLog)
	So(s.close(ctx), ShouldBeNil)

	return readLogFile(t, logPath)
}

// logLinesForPhase returns the log lines containing both the given message
// substring and the given upgrade phase state.
func logLinesForPhase(logged, msgMatch, state string) []string {
	var lines []string

	for _, line := range logLinesContaining(logged, msgMatch) {
		if strings.Contains(line, `state="`+state+`"`) {
			lines = append(lines, line)
		}
	}

	return lines
}

// zeroBoltBranchPage zeroes the first branch page of the closed bbolt file at
// path.
func zeroBoltBranchPage(path string) {
	b, err := bolt.Open(path, dbFilePermission, nil)
	So(err, ShouldBeNil)

	pageSize := int64(b.Info().PageSize)
	branch := int64(-1)

	So(b.View(func(tx *bolt.Tx) error {
		for id := 2; branch < 0; id++ {
			info, errp := tx.Page(id)
			if errp != nil || info == nil {
				return errp
			}

			if info.Type == "branch" {
				branch = int64(id)
			}
		}

		return nil
	}), ShouldBeNil)
	So(b.Close(), ShouldBeNil)
	So(branch, ShouldBeGreaterThan, 1)

	zeroBoltPage(path, branch, pageSize)
}

func TestStoreConvertLiveJobWriteError(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given a bbolt DB of many live jobs, whose records fill the new store's write buffer while step 4 "+
		"adds them", t, func() {
		dbFile := filepath.Join(t.TempDir(), convertDBName)

		live := make(map[string][]byte, convertBufferedLiveJobs)
		for i := range convertBufferedLiveJobs {
			job := testDBJob(fmt.Sprintf("echo convert live %d with a command long enough to fill blocks", i),
				convertRG1)
			live[job.Key()] = encodedTestJob(job)
		}

		writeRawBoltDB(dbFile, schemaStamp(currentDBSchemaVersion),
			map[string]map[string][]byte{string(bucketJobsLive): live})

		Convey("a write error on the new store while adding them is the conversion's own, not an unconvertible "+
			"source: starting fails, leaving the conversion for the next start to resume, which converts", func() {
			jobs := 0

			setConvertHook(func(at convertPoint) error {
				if at != convertAtJob {
					return nil
				}

				jobs++
				if jobs == 1 {
					return failWritesTo(checkpointPath(dbFile+storeConvertingSuffix, convertWalFrom))
				}

				return nil
			})

			_, _, err := openStore(ctx, convertStoreConfig(dbFile, storeKindLog))
			So(err, ShouldWrap, syscall.ENOSPC)
			So(errors.Is(err, errBoltUnconvertible), ShouldBeFalse)
			soResumable(dbFile, err)

			logConvertHook.Store(nil)

			s := openStoreOf(ctx, dbFile, storeKindLog)
			So(s.close(ctx), ShouldBeNil)
		})
	})
}

func TestStoreConvertKeyChangingModify(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given a converted bbolt DB holding a job of rep group A completed and put back live", t, func() {
		pauseLogFolder()

		dbFile := filepath.Join(t.TempDir(), convertDBName)
		bdb := openStoreOf(ctx, dbFile, storeKindBolt)

		job := testDBJob("echo convert modify lookups", "convert-rgA")
		_, err := bdb.storeNewJobsGuarded(ctx, []*Job{job}, false, nil)
		So(err, ShouldBeNil)

		job.State = JobStateComplete
		job.EndTime = time.Now()
		_, err = bdb.archiveCompletion(job.Key(), job)
		So(err, ShouldBeNil)

		job.State = JobStateReady
		So(bdb.storeLiveForRerun([]*Job{job}), ShouldBeNil)
		So(bdb.close(ctx), ShouldBeNil)

		ls, err := openLogStoreLocked(ctx, convertStoreConfig(dbFile, storeKindLog))
		So(err, ShouldBeNil)

		Convey("modified to a new key in rep group B, once the modify is folded it is still complete, A no "+
			"longer finds it, and A is still a rep group ever added", func() {
			moved := modifiedCopy(job, "echo convert modify lookups moved")
			moved.RepGroup = "convert-rgB"
			So(ls.modifyLiveJobs(ctx, []string{job.Key()}, []*Job{moved}), ShouldBeNil)
			So(ls.foldNow(), ShouldBeNil)

			droppedFromA := func(st *logStore) {
				complete, errc := st.checkIfComplete(job.Key())
				So(errc, ShouldBeNil)
				So(complete, ShouldBeTrue)

				byRG, errr := st.retrieveCompleteJobsByRepGroup("convert-rgA")
				So(errr, ShouldBeNil)
				So(byRG, ShouldBeEmpty)

				rgs, errg := st.retrieveRepGroups()
				So(errg, ShouldBeNil)
				So(rgs, ShouldContain, "convert-rgA")
			}

			droppedFromA(ls)
			droppedFromA(reopenLogStore(ctx, ls))
		})
	})
}

func TestStoreConvertPreUpgradeDB(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given a bbolt DB from before the dep group upgrades, with a live parent in a dep group, its "+
		"child, a retired dep group index entry and no seen dep groups or reverse lookups", t, func() {
		dbFile := filepath.Join(t.TempDir(), convertDBName)
		bdb := openStoreOf(ctx, dbFile, storeKindBolt)

		parent := testDBJob("echo convert pre-upgrade parent", convertRG1)
		parent.DepGroups = []string{convertDepGroup}
		child := testDBJob("echo convert pre-upgrade child", convertRG2)
		child.Dependencies = Dependencies{NewDepGroupDependency(convertDepGroup)}

		_, err := bdb.storeNewJobsGuarded(ctx, []*Job{parent, child}, false, nil)
		So(err, ShouldBeNil)
		So(bdb.close(ctx), ShouldBeNil)

		b, err := bolt.Open(dbFile, dbFilePermission, nil)
		So(err, ShouldBeNil)
		So(b.Update(func(tx *bolt.Tx) error {
			errp := tx.Bucket(bucketDTK).Put([]byte(convertDepGroup+dbDelimiter+parent.Key()), nil)

			return errors.Join(errp, tx.DeleteBucket(bucketDepGroups), tx.DeleteBucket(bucketJobLookupEntries))
		}), ShouldBeNil)
		So(b.Close(), ShouldBeNil)

		modified := testDBJob("echo convert pre-upgrade parent modified", convertRG1)
		modified.DepGroups = []string{convertDepGroup}

		// answers resolves child with parent, then modified, as the dep
		// group's live member, and modifies parent's key to modified's.
		answers := func(s store) [][]string {
			resolved := make([][]string, 0, 8) // 4 resolutions, then the recovered jobs

			for _, member := range []*Job{parent, modified} {
				groups := newDepGroupMembers()
				groups.add(member.DepGroups, member.Key())

				deps, waiting, errd := child.Dependencies.dependencyKeys(s, groups)
				So(errd, ShouldBeNil)

				resolved = append(resolved, deps, waiting)

				if member == parent {
					So(s.modifyLiveJobs(ctx, []string{parent.Key()}, []*Job{modified}), ShouldBeNil)
				}
			}

			jobs, _, errr := s.recoverIncompleteJobs()
			So(errr, ShouldBeNil)

			for _, job := range jobs {
				resolved = append(resolved, []string{job.Key()})
			}

			slices.SortFunc(resolved[4:], slices.Compare)

			return resolved
		}

		boltCopy := filepath.Join(t.TempDir(), convertDBName)
		copyTestFile(dbFile, boltCopy)

		reopened := openStoreOf(ctx, boltCopy, storeKindBolt)
		So(reopened.upgradedOnOpen(), ShouldBeTrue)

		fromBolt := answers(reopened)
		So(reopened.close(ctx), ShouldBeNil)

		Convey("when converted, the child resolves and the key-changing modify works as on bbolt", func() {
			s := openStoreOf(ctx, dbFile, storeKindLog)
			defer func() { So(s.close(ctx), ShouldBeNil) }()

			So(s.upgradedOnOpen(), ShouldBeTrue)
			So(answers(s), ShouldResemble, fromBolt)
			So(fromBolt[0], ShouldResemble, []string{depGroupDependencyKey(convertDepGroup)})
		})
	})
}

func TestStoreConvertedDBCompatRecoveryWindow(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("On the converted golden bbolt fixture, a recovery-window j* call for a not-yet-restored key returns "+
		"ErrRecovering not ErrBadJob", t, func() {
		_, serverConfig, _, _, _ := jobqueueTestInit(true)
		convertedDBCompatConfig(t, &serverConfig)

		server, _, release := pausedRecoveringFixtureServer(ctx, serverConfig)

		defer dgsCleanup(ctx, server, release)()

		So(server.isRecovering(), ShouldBeTrue)

		restored, total := server.recoveryProgress()
		So(total, ShouldEqual, dbcompatIncompleteCount)
		So(restored, ShouldEqual, 0)

		cr := &clientRequest{Job: dbcompatIncompleteJob(dbcompatIncompleteCmd1)}

		_, _, errStr := server.getij(cr, true)
		So(errStr, ShouldEqual, ErrRecovering)
	})

	Convey("On the converted golden bbolt fixture, prior incomplete jobs are recovered and become reservable "+
		"once the recovery window finishes, and the complete jobs are complete", t, func() {
		config, serverConfig, addr, _, clientConnectTime := jobqueueTestInit(true)
		convertedDBCompatConfig(t, &serverConfig)

		server, token, release := pausedRecoveringFixtureServer(ctx, serverConfig)

		defer dgsCleanup(ctx, server, release)()

		So(server.isRecovering(), ShouldBeTrue)
		So(server.q.Stats().Items, ShouldEqual, 0)

		release()
		So(waitUntilRecovered(server), ShouldBeTrue)
		So(server.isRecovering(), ShouldBeFalse)
		So(dgsWaitServing(server), ShouldBeTrue)

		restored, total := server.recoveryProgress()
		So(total, ShouldEqual, dbcompatIncompleteCount)
		So(restored, ShouldEqual, dbcompatIncompleteCount)

		jq, errc := Connect(addr, config.ManagerCAFile, config.ManagerCertDomain, token, clientConnectTime)
		So(errc, ShouldBeNil)

		defer disconnect(jq)

		summaries, serr := jq.GetStatusByRepGroupMatch(dbcompatCompleteRepGroup, RepGroupMatchExact, nil, true, false)
		So(serr, ShouldBeNil)
		So(summaries[dbcompatCompleteRepGroup], ShouldNotBeNil)
		So(summaries[dbcompatCompleteRepGroup].Counts[JobStateComplete], ShouldEqual, dbcompatCompleteCount)

		reservedRepGroups := reserveIncompleteJobs(jq)
		So(len(reservedRepGroups), ShouldEqual, dbcompatIncompleteCount)

		for _, rg := range reservedRepGroups {
			So(rg, ShouldEqual, dbcompatIncompleteRepGroup)
		}
	})
}

// convertedDBCompatConfig points serverConfig at a fresh copy of the golden
// dbcompat bbolt fixture, unversioned and uncompacted, which the log store
// converts when the server starts.
func convertedDBCompatConfig(t *testing.T, serverConfig *ServerConfig) {
	t.Helper()

	serverConfig.DBFile = copyFixtureToTempDB(t, serverConfig.DBFile)
	serverConfig.DBFileBackup = serverConfig.DBFile + "_bk"
	serverConfig.dontWipeDevDB = true
	serverConfig.storeKind = storeKindLog
}

// copyFixtureToTempDB copies the committed golden fixture into a fresh file in
// t.TempDir() (BoltDB needs an exclusive read-write open, so tests never open
// the committed file in place) and returns the copy's path. The suggestedName's
// base name is reused so the on-disk name is unsurprising.
func copyFixtureToTempDB(t *testing.T, suggestedName string) string {
	t.Helper()

	dstPath := filepath.Join(t.TempDir(), filepath.Base(suggestedName))
	copyTestFile(dbcompatFixture, dstPath)

	return dstPath
}

// dbcompatIncompleteJob rebuilds one of the two incomplete fixture jobs so its
// Key() matches the key recovery will restore. The fixture jobs set neither
// CwdMatters nor any mounts/container image, so Cmd alone determines Key(); the
// remaining fields mirror the fixture (see testdata/dbcompat/gen.go) for
// clarity. A reconnecting runner reconstructs its job this way to reference a
// key that recovery has not yet restored into the queue.
func dbcompatIncompleteJob(cmd string) *Job {
	return &Job{
		Cmd: cmd, Cwd: defaultUploadDir, RepGroup: dbcompatIncompleteRepGroup, ReqGroup: "reliable2-dbcompat",
	}
}

// reserveIncompleteJobs reserves the recovered incomplete jobs and returns each
// distinct job's RepGroup. Each reserved job is immediately Started with this
// process's (alive) PID so a TTR expiry parks it Lost in the run sub-queue
// rather than recycling it back to ready and being re-reserved (which would
// otherwise inflate the count under the test's short TTR). Reserving is bounded
// and stops once a Reserve yields no job.
func reserveIncompleteJobs(jq *Client) []string {
	seen := make(map[string]string)

	for range dbcompatIncompleteCount + 1 {
		job, err := jq.Reserve(2 * time.Second)
		So(err, ShouldBeNil)

		if job == nil {
			break
		}

		So(jq.Started(job, os.Getpid()), ShouldBeNil)

		seen[job.Key()] = job.RepGroup
	}

	repGroups := make([]string, 0, len(seen))
	for _, rg := range seen {
		repGroups = append(repGroups, rg)
	}

	return repGroups
}

// TestDBUpgradeProgressLogRateLimit pins the rate limit itself: the
// default-level output of a long phase is proportional to how long it runs, not
// to how many entries it processes. Without this, a single elapsed interval
// latches promotion on and every remaining progress point reaches the default
// log - a million-entry rebuild going from 7 lines to 87 - which is the flood
// this whole guard exists to prevent, and which every log-file test above still
// passes through.
//
// It drives the reporter directly, with its clock injected, because the cadence
// cannot be measured through the log file without either a rebuild that really
// takes minutes or a wall-clock race on a loaded host. The log file remains the
// boundary for what gets logged; this covers only how often.
func TestDBUpgradeProgressLogRateLimit(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("A long phase promotes one progress line per log interval, not one per progress point", t, func() {
		clock := time.Now()
		reporter := newDBUpgradeReporter(ctx, filepath.Join(t.TempDir(), "queue.db"))
		reporter.now = func() time.Time { return clock }

		var everyPoint, promoted []string

		reporter.info = recordLoggedArgs(&everyPoint, "database upgrade progress")
		reporter.warn = recordLoggedArgs(&promoted, "database upgrade step still running")

		reporter.startPhase(internal.DBUpgradeConvertState, "converting database")

		for point := range dbUpgradeRateLimitPoints {
			clock = clock.Add(dbUpgradeLogInterval / dbUpgradeRateLimitPointsPerInterval)
			processed := (point + 1) * dbUpgradeProgressEntries

			reporter.writeProgress(internal.DBUpgradeConvertState,
				fmt.Sprintf("converting database (%d jobs processed)", processed), processed)
		}

		// every point is still recorded for --debug, and exactly one point per
		// interval is promoted to the default log.
		So(len(everyPoint), ShouldEqual, dbUpgradeRateLimitPoints)
		So(len(promoted), ShouldEqual, dbUpgradeRateLimitPoints/dbUpgradeRateLimitPointsPerInterval)

		// and the promoted line is a progress report, not a bare "still alive": it
		// carries how far the phase has got.
		So(logLineValue(promoted[len(promoted)-1], "processed"),
			ShouldEqual, strconv.Itoa(dbUpgradeRateLimitPoints*dbUpgradeProgressEntries))
	})
}

// recordLoggedArgs returns a dbUpgradeReporter log function that appends the
// arguments of each call made with the given message to lines, rendered in the
// same key=value form the manager log uses, so logLineValue reads them.
func recordLoggedArgs(lines *[]string, msg string) func(string, ...any) {
	return func(logged string, args ...any) {
		if logged != msg {
			return
		}

		var line strings.Builder

		for i := 0; i+1 < len(args); i += 2 {
			fmt.Fprintf(&line, " %v=%v", args[i], args[i+1])
		}

		*lines = append(*lines, line.String())
	}
}
