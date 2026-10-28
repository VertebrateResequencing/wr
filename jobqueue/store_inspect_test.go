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
	"encoding/json"
	"errors"
	"fmt"
	"go/build/constraint"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/VertebrateResequencing/wr/internal"
	jqs "github.com/VertebrateResequencing/wr/jobqueue/scheduler"
	. "github.com/smartystreets/goconvey/convey"
)

// testStoreEnv names the env var that picks the store kind forEachStore runs.
const testStoreEnv = "WR_TEST_STORE"

// testStoreBackupFile is the name of the backup file openTestStore configures.
const testStoreBackupFile = "queue.db.bak"

// boltOnlyTests are the test files Phase 2 deletes with bbolt (spec A2): every
// test file that reaches bbolt directly (acceptance test 3), and the few that
// test only bbolt's own machinery without matching boltReachPattern. Every other
// test file inspects stored state through storeInspector or the store API. A
// behavioural test that needs bbolt for one step lives in its own
// *_bolt_test.go file, so that deleting it leaves the rest of its file's tests
// running on every store. TestBoltOnlyTests proves the package still builds
// without these files.
var boltOnlyTests = []string{ //nolint:gochecknoglobals
	// the bbolt storeInspector.
	"store_bolt_inspect_test.go",

	// writer turns, coalescing, the archive fold and its log line, held
	// writers and the transactions they make.
	"archive_before_start_bolt_test.go",
	"archive_stall_bolt_test.go",
	"db_bench_test.go",
	"depgranularity_txcost_bolt_test.go",
	"nohold_write_turns_bench_test.go",
	"nohold_write_turns_test.go",
	"readd_queued_bolt_test.go",
	"release_durability_resend_bolt_test.go",
	"release_durability_test.go",
	"release_std_bolt_test.go",
	"reliable4_add_coalesce_test.go",
	"reliable4_add_foldcap_test.go",
	"reliable4_add_tx_test.go",
	"reliable4_addstorm_test.go",
	"reliable4_archive_coalesce_test.go",
	"reliable4_archive_fold_log_test.go",
	"reliable4_besteffort_writer_test.go",
	"reliable4_dependency_tx_bolt_test.go",
	"reliable4_writestorm_freeze_test.go",
	"reliable4_writestorm_test.go",
	"reqstat_cache_bolt_test.go",
	"reserve_durability_bolt_test.go",
	"reserve_ttr_test.go",
	"resume_order_bolt_test.go",
	"start_durability_test.go",
	"warn_storm_bolt_test.go",

	// backup pacing, remap, freelist, prefetch, cold start and compaction.
	"db_backup_remap_test.go",
	"db_coldstart_test.go",
	"db_compact_std_test.go",
	"dbstart_probe_test.go",
	"prodsim_freelist_test.go",
	"prodsim_remap_stall_test.go",
	"reliable4_archive_ceiling_test.go",
	"reliable4_archive_rate_test.go",
	"reliable4_backup_repro_test.go",
	"reliable4_backup_stall_test.go",

	// the bbolt file lock, bucket layout, run-state records, record encoding,
	// schema and its upgrades.
	"db_bolt_test.go",
	"db_runstate_bolt_test.go",
	"db_schema_test.go",
	"depgranularity_dblock_test.go",
	"depgranularity_fixture_bolt_test.go",
	"depgroups_bolt_test.go",
	"dup_breakdown_bolt_test.go",
	"reliable2_dbcompat_test.go",
	"reliable4_upgrade_log_test.go",
	"server_startup_upgrade_bolt_test.go",

	// fixtures written as raw bbolt records, at a scale the store API would
	// take minutes to reach, or copied between bbolt files.
	"reliable2_startup_scale_bolt_test.go",
	"reliable4_history_seed_test.go",
}

// errNoLiveRecord is rewriteStoredLiveJob's error for a key with no live record.
var errNoLiveRecord = errors.New("no live record")

// errNoStoreFileOpener is openStoreFileInspector's error when no store kind
// has registered an opener.
var errNoStoreFileOpener = errors.New("no store file opener is registered")

// storeInspectTestFile is this file, which spec A2 says is not in
// boltOnlyTests.
const storeInspectTestFile = "store_inspect_test.go"

// boltWritesFile is the listed file that defines boltWrites, which the build
// check's own test calls from an unlisted file.
const boltWritesFile = "db_bench_test.go"

// boltOnlyVetTimeout bounds vetWithoutBoltOnlyTests' go vet. It is a hang
// detector: with a warm build cache the vet takes a few seconds.
const boltOnlyVetTimeout = 5 * time.Minute

// boltOnlyVetTags are the build tag sets this package's tests are built with
// (make test, make race, and the reliability and reliability_repro tests), each
// of which must still build without boltOnlyTests. Every tag a test file's
// //go:build line uses, other than platformBuildTags, must be in one of them.
var boltOnlyVetTags = []string{ //nolint:gochecknoglobals
	"netgo",
	"netgo,race",
	"netgo,reliability",
	"netgo,reliability_repro",
	"netgo,reliability_repro,reliability",
}

// platformBuildTags are the //go:build tags of test files that the platform,
// not a -tags flag, decides.
var platformBuildTags = []string{osLinux, "windows"} //nolint:gochecknoglobals

// boltReachPattern is how spec A2 acceptance test 3 finds a test file that
// reaches bbolt directly.
var boltReachPattern = regexp.MustCompile(`\.bolt\.|\*bolt\.Tx|bolt\.Open`)

// seedArchivers is how many archives seedArchivedJobs has in flight at once, so
// that the store can fold them into few writes.
const seedArchivers = 512

// storeInspector is how a test looks at what a store holds, whatever the store,
// so that one test can run against every store kind (see forEachStore).
type storeInspector interface {
	liveJob(key string) (*Job, bool, error)     // latest durable spec+state
	completeJob(key string) (*Job, bool, error) // from history (+overlay)
	count(kind recordKind) (int, error)
	forceBackup() error // synchronous; returns after the backup is written
	flush() error       // waits until every queued write is durable
}

// storeFileOpeners each open the files of one store kind at a path, read-only,
// returning an inspector over them and a func that closes them, or an error if
// the files are not of that kind. Each kind's inspector file adds its opener in
// an init func, so that openStoreFileInspector, and the tests that use it,
// build without any one kind's file.
var storeFileOpeners []func(path string) (storeInspector, func() error, error) //nolint:gochecknoglobals

// recordKind is a kind of stored record that storeInspector.count counts.
type recordKind uint8

const (
	kindLive recordKind = iota
	kindComplete
	kindStdOut
	kindStdErr
	kindEnv
	kindLimit
	kindStat
)

// inspectedCount is inspector.count(kind), asserting it succeeded.
func inspectedCount(inspector storeInspector, kind recordKind) int {
	n, err := inspector.count(kind)
	So(err, ShouldBeNil)

	return n
}

// inspectorOf returns s as a storeInspector. Every store kind is one in tests.
func inspectorOf(s store) storeInspector {
	inspector, ok := s.(storeInspector)
	if !ok {
		panic(fmt.Sprintf("store %T is not a storeInspector", s))
	}

	return inspector
}

// openStoreFileInspector opens the store files at path, such as a stopped
// manager's database or a backup of one, read-only, and returns an inspector
// over them along with a func that closes them. Close it before anything else
// needs to open path. It tries each store kind's opener in turn, so the files
// pick the kind.
func openStoreFileInspector(path string) (storeInspector, func() error, error) {
	if len(storeFileOpeners) == 0 {
		return nil, nil, errNoStoreFileOpener
	}

	errs := make([]error, 0, len(storeFileOpeners))

	for _, open := range storeFileOpeners {
		inspector, closeFiles, err := open(path)
		if err == nil {
			return inspector, closeFiles, nil
		}

		errs = append(errs, err)
	}

	return nil, nil, errors.Join(errs...)
}

func TestStoreInspector(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	forEachStore(t, func(kind storeKind) {
		Convey(fmt.Sprintf("Given a fresh %s store with 3 jobs added and 1 of them archived", storeKindName(kind)),
			t, func() {
				s, dir := openTestStore(t, ctx, kind)
				inspector := inspectorOf(s)

				jobs := []*Job{
					testDBJob("echo inspect 1", "inspect"),
					testDBJob("echo inspect 2", "inspect"),
					testDBJob("echo inspect 3", "inspect"),
				}
				_, err := s.storeNewJobsGuarded(ctx, jobs, false, nil)
				So(err, ShouldBeNil)

				archived := jobs[0]
				archived.State = JobStateComplete
				archived.Exited = true
				archived.EndTime = time.Now()
				_, err = s.archiveCompletion(archived.Key(), archived)
				So(err, ShouldBeNil)
				So(inspector.flush(), ShouldBeNil)

				Convey("it counts 2 live and 1 complete, and the archived key is not live", func() {
					So(inspectedCount(inspector, kindLive), ShouldEqual, 2)
					So(inspectedCount(inspector, kindComplete), ShouldEqual, 1)

					_, live, errl := inspector.liveJob(archived.Key())
					So(errl, ShouldBeNil)
					So(live, ShouldBeFalse)

					stored, live, errl := inspector.liveJob(jobs[1].Key())
					So(errl, ShouldBeNil)
					So(live, ShouldBeTrue)
					So(stored.Cmd, ShouldEqual, jobs[1].Cmd)

					complete, found, errc := inspector.completeJob(archived.Key())
					So(errc, ShouldBeNil)
					So(found, ShouldBeTrue)
					So(complete.Cmd, ShouldEqual, archived.Cmd)
					So(complete.State, ShouldEqual, JobStateComplete)

					_, found, errc = inspector.completeJob(jobs[1].Key())
					So(errc, ShouldBeNil)
					So(found, ShouldBeFalse)
				})

				Convey("a forced backup holds what the store held when it was made", func() {
					So(inspector.forceBackup(), ShouldBeNil)

					backup, closeBackup, errb := openStoreFileInspector(filepath.Join(dir, testStoreBackupFile))
					So(errb, ShouldBeNil)

					defer func() { So(closeBackup(), ShouldBeNil) }()

					So(inspectedCount(backup, kindLive), ShouldEqual, 2)
					So(inspectedCount(backup, kindComplete), ShouldEqual, 1)
				})
			})

		Convey(fmt.Sprintf("Given a %s store, a job that exited 1 with both std streams and one with only stdout",
			storeKindName(kind)), t, func() {
			s, _ := openTestStore(t, ctx, kind)
			inspector := inspectorOf(s)

			job := testDBJob("echo inspect std", "inspect")
			outOnly := testDBJob("echo inspect stdout only", "inspect")
			_, err := s.storeNewJobsGuarded(ctx, []*Job{job, outOnly}, false, nil)
			So(err, ShouldBeNil)

			for _, failed := range []*Job{job, outOnly} {
				failed.State = JobStateBuried
				failed.Exited = true
				failed.Exitcode = 1
			}

			So(s.updateJobAfterExitDurable(job, []byte("o"), []byte("e"), false), ShouldBeNil)
			So(s.updateJobAfterExitDurable(outOnly, []byte("o2"), nil, false), ShouldBeNil)

			Convey("once flushed, 2 stdouts and 1 stderr are stored and retrieveJobStd returns both streams", func() {
				So(inspector.flush(), ShouldBeNil)
				So(inspectedCount(inspector, kindStdOut), ShouldEqual, 2)
				So(inspectedCount(inspector, kindStdErr), ShouldEqual, 1)

				stdo, stde := s.retrieveJobStd(ctx, job.Key())
				So(string(stdo), ShouldEqual, "o")
				So(string(stde), ShouldEqual, "e")
			})
		})
	})
}

// forEachStore runs fn once for the store kind named by the WR_TEST_STORE env
// var ("bolt", the default until the log store exists), or for every kind when
// it is "all". An unknown kind, or one not yet implemented, fails the test.
func forEachStore(t *testing.T, fn func(kind storeKind)) {
	t.Helper()

	for _, kind := range testStoreKinds(t) {
		fn(kind)
	}
}

// testStoreKinds returns the store kinds WR_TEST_STORE selects.
func testStoreKinds(t *testing.T) []storeKind {
	t.Helper()

	switch name := os.Getenv(testStoreEnv); name {
	case "", "bolt", "all":
		return []storeKind{storeKindBolt}
	case "log":
		t.Fatalf("%s=log: the log store is not implemented yet", testStoreEnv)
	default:
		t.Fatalf("%s=%q: want bolt, log or all", testStoreEnv, name)
	}

	return nil
}

// storeKindName returns the WR_TEST_STORE name of kind.
func storeKindName(kind storeKind) string {
	if kind == storeKindLog {
		return "log"
	}

	return "bolt"
}

// openTestStore opens a fresh store of the given kind in a temporary directory,
// closed when the Convey block ends, and returns it with its directory.
func openTestStore(t *testing.T, ctx context.Context, kind storeKind) (store, string) {
	t.Helper()

	dir := t.TempDir()
	s := openStoreIn(ctx, kind, dir)

	Reset(func() { So(s.close(ctx), ShouldBeNil) })

	return s, dir
}

// openStoreIn opens a store of the given kind on the database files in dir,
// creating them if they do not exist. The caller closes it.
func openStoreIn(ctx context.Context, kind storeKind, dir string) store {
	s, _, err := openStore(ctx, storeConfig{
		dbFile:     filepath.Join(dir, "queue.db"),
		dbBkFile:   filepath.Join(dir, testStoreBackupFile),
		deployment: internal.Development,
		kind:       kind,
	})
	So(err, ShouldBeNil)

	return s
}

func testDBJob(cmd, repGroup string) *Job {
	return &Job{
		Cmd:      cmd,
		Cwd:      testCwd,
		ReqGroup: "db_test",
		Requirements: &jqs.Requirements{
			RAM:   10,
			Time:  time.Second,
			Cores: 1,
		},
		RepGroup: repGroup,
	}
}

// inspector returns s's store as a storeInspector.
func (s *Server) inspector() storeInspector {
	return inspectorOf(s.db)
}

// storedLiveJobState is the State of storedLiveJob.
func storedLiveJobState(t *testing.T, inspector storeInspector, key string) JobState {
	t.Helper()

	return storedLiveJob(t, inspector, key).State
}

// storedLiveJob returns the job stored live under key, with its latest durable
// state, as recovery would read it, failing the test if there is none.
func storedLiveJob(t *testing.T, inspector storeInspector, key string) *Job {
	t.Helper()

	job, found, err := inspector.liveJob(key)
	if err != nil {
		t.Fatalf("could not read the live job: %v", err)
	}

	if !found {
		t.Fatalf("no live job stored under %s", key)
	}

	return job
}

// TestBoltOnlyTests covers spec A2 acceptance test 3 and what it is for: once
// Phase 2 deletes the files in boltOnlyTests, the rest of the package still
// builds, so no test that should carry over to the log store goes with them.
func TestBoltOnlyTests(t *testing.T) {
	if runnermode || servermode {
		return
	}

	Convey("Only the files listed in boltOnlyTests reach bbolt directly, and this file is not one", t, func() {
		paths, err := filepath.Glob("*_test.go")
		So(err, ShouldBeNil)
		So(paths, ShouldNotBeEmpty)

		var unlisted []string

		for _, path := range paths {
			content, errr := os.ReadFile(path)
			So(errr, ShouldBeNil)

			if boltReachPattern.Match(content) && !slices.Contains(boltOnlyTests, path) {
				unlisted = append(unlisted, path)
			}
		}

		So(unlisted, ShouldBeEmpty)
		So(boltOnlyTests, ShouldNotContain, storeInspectTestFile)

		content, err := os.ReadFile(storeInspectTestFile)
		So(err, ShouldBeNil)
		So(boltReachPattern.Match(content), ShouldBeFalse)

		var missing []string

		for _, listed := range boltOnlyTests {
			if !slices.Contains(paths, listed) {
				missing = append(missing, listed)
			}
		}

		So(missing, ShouldBeEmpty)
	})

	Convey("Without the files in boltOnlyTests, the package and its other tests still build with every tag set",
		t, func() {
			So(testBuildTags(t), ShouldBeEmpty)

			outs := make([]string, len(boltOnlyVetTags))
			errs := make([]error, len(boltOnlyVetTags))

			var wg sync.WaitGroup

			for i, tags := range boltOnlyVetTags {
				wg.Go(func() { outs[i], errs[i] = vetWithoutBoltOnlyTests(t, tags, nil) })
			}

			wg.Wait()

			for i, tags := range boltOnlyVetTags {
				So(tags+": "+outs[i], ShouldEqual, tags+": ")
				So(errs[i], ShouldBeNil)
			}
		})

	Convey("The build check fails for an unlisted test calling boltWrites, which only a listed file defines", t,
		func() {
			So(boltOnlyTests, ShouldContain, boltWritesFile)

			content, err := os.ReadFile(boltWritesFile)
			So(err, ShouldBeNil)
			So(string(content), ShouldContainSubstring, "\nfunc boltWrites(")

			userFile := filepath.Join(t.TempDir(), "user_test.go")
			So(os.WriteFile(userFile, []byte("package jobqueue\n\n"+
				"func usesBoltWrites(s *Server) int64 { return boltWrites(s.boltDB()) }\n"), 0o600), ShouldBeNil)

			out, err := vetWithoutBoltOnlyTests(t, boltOnlyVetTags[0],
				map[string]string{"zz_uses_bolt_writes_test.go": userFile})
			So(err, ShouldNotBeNil)
			So(out, ShouldContainSubstring, "undefined: boltWrites")
		})
}

// testBuildTags returns the tags in this package's test files' //go:build lines
// that neither boltOnlyVetTags nor platformBuildTags cover.
func testBuildTags(t *testing.T) []string {
	t.Helper()

	paths, err := filepath.Glob("*_test.go")
	So(err, ShouldBeNil)

	covered := slices.Clone(platformBuildTags)
	for _, tags := range boltOnlyVetTags {
		covered = append(covered, strings.Split(tags, ",")...)
	}

	var uncovered []string

	for _, path := range paths {
		content, errr := os.ReadFile(path)
		So(errr, ShouldBeNil)

		for line := range strings.Lines(string(content)) {
			if !constraint.IsGoBuild(line) {
				continue
			}

			expr, errp := constraint.Parse(line)
			So(errp, ShouldBeNil)

			expr.Eval(func(tag string) bool {
				if !slices.Contains(covered, tag) {
					uncovered = append(uncovered, path+": "+tag)
				}

				return true
			})
		}
	}

	return uncovered
}

// vetWithoutBoltOnlyTests runs go vet with the given build tags on this
// package, tests included, as it would be with every file in boltOnlyTests
// deleted, using go's -overlay so no real file is touched. added maps the name
// of a file to add to the package to the file holding its content. It returns
// go vet's combined output and error. It makes no assertions, so it can run in
// its own goroutine.
func vetWithoutBoltOnlyTests(t *testing.T, tags string, added map[string]string) (string, error) {
	t.Helper()

	goBin, err := exec.LookPath("go")
	if err != nil {
		return "", err
	}

	pkgDir, err := os.Getwd()
	if err != nil {
		return "", err
	}

	replace := make(map[string]string, len(boltOnlyTests)+len(added))

	for _, listed := range boltOnlyTests {
		replace[filepath.Join(pkgDir, listed)] = ""
	}

	for name, from := range added {
		replace[filepath.Join(pkgDir, name)] = from
	}

	overlay, err := json.Marshal(map[string]map[string]string{"Replace": replace})
	if err != nil {
		return "", err
	}

	overlayFile := filepath.Join(t.TempDir(), "overlay.json")
	if err = os.WriteFile(overlayFile, overlay, 0o600); err != nil {
		return "", err
	}

	ctx, cancel := context.WithTimeout(context.Background(), boltOnlyVetTimeout)
	defer cancel()

	out, err := exec.CommandContext(ctx, goBin, "vet", "-tags", tags, "-overlay", overlayFile, ".").
		CombinedOutput()

	return string(out), err
}

// rewriteStoredLiveJob reads the live job stored under key as recovery would,
// lets change alter it, and stores the result as the job's full live record
// through the server's store, without touching the job the server holds in
// memory: what a crash between two of the job's writes can leave on disk.
func rewriteStoredLiveJob(s *Server, key string, change func(*Job)) error {
	job, live, err := s.inspector().liveJob(key)
	if err != nil {
		return err
	}

	if !live {
		return fmt.Errorf("%w: %s", errNoLiveRecord, key)
	}

	change(job)

	return s.db.updateJobAfterChangeDurable(job)
}

// seedArchivedJobs gives config's database an archived history: it opens the
// store there, adds jobs, calls afterAdd (if not nil) to change any of them,
// archives each of them as it then is, and closes the store.
func seedArchivedJobs(ctx context.Context, config ServerConfig, jobs []*Job, afterAdd func()) {
	s, _, err := openStore(ctx, storeConfig{
		dbFile: config.DBFile, dbBkFile: config.DBFileBackup, deployment: internal.Development,
	})
	So(err, ShouldBeNil)

	_, err = s.storeNewJobsGuarded(ctx, jobs, false, nil)
	So(err, ShouldBeNil)

	if afterAdd != nil {
		afterAdd()
	}

	So(archiveJobsInStore(s, jobs), ShouldBeNil)
	So(s.close(ctx), ShouldBeNil)
}

// archiveJobsInStore archives each of jobs in s as it is, seedArchivers at a
// time.
func archiveJobsInStore(s store, jobs []*Job) error {
	work := make(chan *Job)
	errs := make(chan error, seedArchivers)

	for range seedArchivers {
		go func() {
			var first error

			for job := range work {
				if _, err := s.archiveCompletion(job.Key(), job); err != nil && first == nil {
					first = err
				}
			}

			errs <- first
		}()
	}

	for _, job := range jobs {
		work <- job
	}

	close(work)

	var first error

	for range seedArchivers {
		if err := <-errs; err != nil && first == nil {
			first = err
		}
	}

	return first
}

// testDBArchivedJob builds a job ready to be archived: it has its end state
// (StartTime, EndTime, exit status and peak usage) populated so archiveJob can
// record stats and the end-time index.
func testDBArchivedJob(cmd, repGroup string, endTime time.Time) *Job {
	job := testDBJob(cmd, repGroup)
	job.State = JobStateComplete
	job.Exited = true
	job.StartTime = endTime.Add(-time.Second)
	job.EndTime = endTime
	job.PeakRAM = 10
	job.PeakDisk = 1
	job.CPUtime = time.Second

	return job
}
