/*******************************************************************************
 * Copyright (c) 2017-2019, 2021, 2024-2025 Genome Research Ltd.
 *
 * Author: Sendu Bala <sb10@sanger.ac.uk>
 * Author: Ashwini Chhipa <ac55@sanger.ac.uk>
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
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	. "github.com/smartystreets/goconvey/convey"
)

func TestBehaviours(t *testing.T) {
	Convey("You can create individual Behaviour", t, func() {
		b1 := &Behaviour{When: OnExit, Do: CleanupAll}
		b2 := &Behaviour{When: OnSuccess, Do: CleanupAll}
		b3 := &Behaviour{When: OnFailure, Do: CleanupAll}
		b4 := &Behaviour{When: OnSuccess, Do: Run, Arg: "touch ../../foo && true"}
		b5 := &Behaviour{When: OnSuccess, Do: Run, Arg: "touch foo"}
		b6 := &Behaviour{When: OnSuccess, Do: Run, Arg: []string{"in", "valid"}}
		b7 := &Behaviour{When: OnSuccess, Do: CopyToManager, Arg: []string{"a.file", "b.file"}}
		b8 := &Behaviour{When: OnSuccess, Do: CopyToManager, Arg: "a.file"}
		b9 := &Behaviour{When: OnSuccess | OnFailure, Do: Cleanup}
		b10 := &Behaviour{When: 10, Do: Cleanup}
		b11 := &Behaviour{When: OnFailure, Do: Remove}

		cwd := t.TempDir()
		actualCwd := filepath.Join(testWorkSpace(cwd, "def"), "cwd")
		err := os.MkdirAll(actualCwd, os.ModePerm)
		So(err, ShouldBeNil)
		_, err = os.OpenFile(filepath.Join(actualCwd, "a.file"), os.O_RDONLY|os.O_CREATE, 0o666)
		So(err, ShouldBeNil)
		_, err = os.OpenFile(filepath.Join(actualCwd, "b.file"), os.O_RDONLY|os.O_CREATE, 0o666)
		So(err, ShouldBeNil)

		foo := filepath.Join(filepath.Dir(testWorkSpace(cwd, "def")), "foo")
		// the top of the chain wr creates below Cwd, which the upward walk
		// stops before deleting.
		adir := filepath.Join(cwd, "wr_cwd")
		job1 := &Job{Cwd: cwd, ActualCwd: actualCwd}
		job2 := &Job{Cwd: cwd}

		Convey("Individual Behaviour can be nicely stringified", func() {
			So(fmt.Sprintf("test Sprintf %s", b1), ShouldEqual, `test Sprintf {"on_exit":[{"cleanup_all":true}]}`)
			So(b1.String(), ShouldEqual, `{"on_exit":[{"cleanup_all":true}]}`)
			So(b2.String(), ShouldEqual, `{"on_success":[{"cleanup_all":true}]}`)
			So(b3.String(), ShouldEqual, `{"on_failure":[{"cleanup_all":true}]}`)
			So(b4.String(), ShouldEqual, `{"on_success":[{"run":"touch ../../foo && true"}]}`)
			So(b5.String(), ShouldEqual, `{"on_success":[{"run":"touch foo"}]}`)
			So(b6.String(), ShouldEqual, `{"on_success":[{"run":"!invalid!"}]}`)
			So(b7.String(), ShouldEqual, `{"on_success":[{"copy_to_manager":["a.file","b.file"]}]}`)
			So(b8.String(), ShouldEqual, `{"on_success":[{"copy_to_manager":["!invalid!"]}]}`)
			So(b9.String(), ShouldEqual, `{"on_failure|success":[{"cleanup":true}]}`)
			So(b10.String(), ShouldEqual, "{}")
			So(b11.String(), ShouldEqual, `{"on_failure":[{"remove":true}]}`)

			Convey("Behaviours can be nicely stringified", func() {
				bs := Behaviours{b1, b4}
				So(bs.String(), ShouldEqual, `{"on_success":[{"run":"touch ../../foo && true"}],"on_exit":[{"cleanup_all":true}]}`)

				bs = Behaviours{}
				So(bs.String(), ShouldBeEmpty)
			})
		})

		Convey("Individual Behaviour Trigger() correctly", func() {
			err = b7.Trigger(OnSuccess, job1)
			So(err, ShouldBeNil)
			err = b8.Trigger(OnSuccess, job1)
			So(err, ShouldNotBeNil)
			// *** CopyToManager not yet implemented, so no proper tests for it yet

			err = b6.Trigger(OnSuccess, job1)
			So(err, ShouldNotBeNil)
			err = b4.Trigger(OnFailure, job1)
			So(err, ShouldBeNil)
			_, err = os.Stat(foo)
			So(err, ShouldNotBeNil)
			err = b4.Trigger(OnSuccess, job1)
			So(err, ShouldBeNil)
			_, err = os.Stat(foo)
			So(err, ShouldBeNil)
			os.Remove(foo)
			_, err = os.Stat(foo)
			So(err, ShouldNotBeNil)
			err = b4.Trigger(OnExit, job1)
			So(err, ShouldBeNil)
			_, err = os.Stat(foo)
			So(err, ShouldNotBeNil)

			err = b5.Trigger(OnSuccess, job2)
			So(err, ShouldBeNil)

			foo2 := filepath.Join(cwd, "foo")
			_, err = os.Stat(foo2)
			So(err, ShouldBeNil)

			err = b1.Trigger(OnSuccess|OnFailure, job1)
			So(err, ShouldBeNil)
			_, err = os.Stat(actualCwd)
			So(err, ShouldBeNil)
			err = b1.Trigger(OnExit, job1)
			So(err, ShouldBeNil)
			_, err = os.Stat(actualCwd)
			So(err, ShouldNotBeNil)
			_, err = os.Stat(cwd)
			So(err, ShouldBeNil)
			_, err = os.Stat(adir)
			So(err, ShouldNotBeNil)

			err = b1.Trigger(OnExit, job2)
			So(err, ShouldBeNil)
			_, err = os.Stat(cwd)
			So(err, ShouldBeNil)

			err = b11.Trigger(OnExit, job2)
			So(err, ShouldBeNil)
		})

		Convey("RemovalRequested works", func() {
			bs := Behaviours{b1, b11}
			So(bs.RemovalRequested(), ShouldBeTrue)

			bs = Behaviours{b1, b2}
			So(bs.RemovalRequested(), ShouldBeFalse)
		})

		Convey("CleanupAll works when actual cwd contains root-owned files", func() {
			rootFile := filepath.Join(actualCwd, "root")

			//nolint:gosec // test-controlled path under a temp dir
			err = exec.CommandContext(context.Background(), "sh", "-c", "sudo -n touch "+rootFile).Run()
			if err != nil {
				SkipConvey("Can't do this test without ability to sudo", func() {})
			} else {
				_, err = os.Stat(rootFile)
				So(err, ShouldBeNil)

				err = b1.Trigger(OnExit, job1)
				So(err, ShouldBeNil)
				_, err = os.Stat(actualCwd)
				So(err, ShouldNotBeNil)
				_, err = os.Stat(cwd)
				So(err, ShouldBeNil)
				_, err = os.Stat(adir)
				So(err, ShouldNotBeNil)
			}
		})

		Convey("Behaviours are triggered in order b2,b4, as specified", func() {
			bs := Behaviours{b2, b4}
			err = bs.Trigger(true, job1)
			So(err, ShouldNotBeNil)
			_, err = os.Stat(adir)
			So(err, ShouldNotBeNil)
		})

		Convey("Behaviours are triggered in order b4,b2, as specified", func() {
			bs := Behaviours{b4, b2}
			err = bs.Trigger(true, job1)
			So(err, ShouldBeNil)
			_, err = os.Stat(adir)
			So(err, ShouldBeNil)
			_, err = os.Stat(foo)
			So(err, ShouldBeNil)
			_, err = os.Stat(testWorkSpace(cwd, "def"))
			So(err, ShouldNotBeNil)
		})

		Convey("OnExit triggers after others", func() {
			bs := Behaviours{b1, b4}
			err = bs.Trigger(true, job1)
			So(err, ShouldBeNil)
			_, err = os.Stat(foo)
			So(err, ShouldBeNil)
			_, err = os.Stat(testWorkSpace(cwd, "def"))
			So(err, ShouldNotBeNil)
		})

		Convey("Non-matching Behaviours are ignored during a Trigger()", func() {
			bs := Behaviours{b3, b4}
			err = bs.Trigger(true, job1)
			So(err, ShouldBeNil)
			_, err = os.Stat(foo)
			So(err, ShouldBeNil)
			_, err = os.Stat(actualCwd)
			So(err, ShouldBeNil)

			os.Remove(foo)
			_, err = os.Stat(foo)
			So(err, ShouldNotBeNil)

			bs = Behaviours{b4, b3}
			err = bs.Trigger(false, job1)
			So(err, ShouldBeNil)
			_, err = os.Stat(adir)
			So(err, ShouldNotBeNil)
		})
	})

	Convey("You can go from JSON to Behaviours", t, func() {
		//nolint:lll // exact JSON fixture asserted verbatim
		jsonStr := `[{"run":"tar -czf my.tar.bz '--include=*.err'"},{"copy_to_manager":["my.tar.bz"]},{"cleanup_all":true},{"remove":true}]`

		var bjs BehavioursViaJSON

		err := json.Unmarshal([]byte(jsonStr), &bjs)
		So(err, ShouldBeNil)
		So(len(bjs), ShouldEqual, 4)

		bs := bjs.Behaviours(OnFailure)

		So(bs[0].When, ShouldEqual, OnFailure)
		So(bs[0].Do, ShouldEqual, Run)
		So(bs[0].Arg, ShouldEqual, "tar -czf my.tar.bz '--include=*.err'")

		So(bs[1].When, ShouldEqual, OnFailure)
		So(bs[1].Do, ShouldEqual, CopyToManager)
		So(bs[1].Arg, ShouldResemble, []string{"my.tar.bz"})

		So(bs[2].When, ShouldEqual, OnFailure)
		So(bs[2].Do, ShouldEqual, CleanupAll)

		So(bs[3].When, ShouldEqual, OnFailure)
		So(bs[3].Do, ShouldEqual, Remove)

		jsonStr = `[{"cleanup":true}]`

		var bjs2 BehavioursViaJSON

		err = json.Unmarshal([]byte(jsonStr), &bjs2)
		So(err, ShouldBeNil)
		So(len(bjs2), ShouldEqual, 1)

		bs = append(bs, bjs2.Behaviours(OnSuccess)...)

		So(bs[4].When, ShouldEqual, OnSuccess)
		So(bs[4].Do, ShouldEqual, Cleanup)

		jsonStr = `[{"run":"true"}]`

		var bjs3 BehavioursViaJSON

		err = json.Unmarshal([]byte(jsonStr), &bjs3)
		So(err, ShouldBeNil)
		So(len(bjs3), ShouldEqual, 1)

		bs = append(bs, bjs3.Behaviours(OnExit)...)

		So(bs[5].When, ShouldEqual, OnExit)
		So(bs[5].Do, ShouldEqual, Run)
		So(bs[5].Arg, ShouldEqual, "true")

		Convey("You can convert back to JSON", func() {
			//nolint:lll // exact JSON fixture asserted verbatim
			So(bs.String(), ShouldEqual, `{"on_failure":[{"run":"tar -czf my.tar.bz '--include=*.err'"},{"copy_to_manager":["my.tar.bz"]},{"cleanup_all":true},{"remove":true}],"on_success":[{"cleanup":true}],"on_exit":[{"run":"true"}]}`)
		})
	})
}

// testHashDirs stand in for the hashed directory levels mkHashedDir puts
// between the <AppName>_cwd base and the unique workspace.
//
//nolint:gochecknoglobals // fixture shape shared by the cleanup tests
var testHashDirs = []string{"a", "b", "c"}

// testWorkSpace returns a workspace path of the shape AND DEPTH mkHashedDir
// really creates one at. The depth matters: cleanup refuses to treat anything
// at another depth as a workspace, because that is the one property of a
// reported ActualCwd it can check without trusting whoever reported it. A
// fixture that is too shallow is not just unrealistic, it exercises a path wr
// would never produce.
func testWorkSpace(cwd, unique string) string {
	parts := append([]string{cwd, "wr_cwd"}, testHashDirs...)

	return filepath.Join(append(parts, unique)...)
}

func TestJobUnmountEmptyDirTidyUp(t *testing.T) {
	if runnermode || servermode {
		return
	}

	Convey("Unmount's empty-dir tidy-up stays inside the workspace wr made", t, func() {
		cwd := t.TempDir()
		actualCwd := filepath.Join(testWorkSpace(cwd, "unique"), "cwd")

		// MountConfig.Mount may be an absolute path to "any directory you're
		// able to write to", so a Job can name an existing directory of the
		// user's. The tidy-up removes empty dirs and then their empty parents,
		// so letting it loose on every mount point deleted the user's dir and
		// the one above it.
		userDir := filepath.Join(cwd, "userdata")
		incoming := filepath.Join(userDir, "incoming")

		for _, dir := range []string{actualCwd, incoming} {
			So(os.MkdirAll(dir, os.ModePerm), ShouldBeNil)
		}

		job := &Job{Cwd: cwd, ActualCwd: actualCwd, MountConfigs: MountConfigs{{Mount: incoming}}}

		_, err := job.Unmount()

		soPathsExist(incoming, userDir, cwd)

		So(err, ShouldBeNil)

		Convey("and does nothing at all when ActualCwd is not one wr could have created", func() {
			// the workspace used to be filepath.Dir of the reported ActualCwd,
			// unchecked, so the v0.37.0|1 poisoning shape (ActualCwd == Cwd)
			// made it the PARENT of Cwd - and then every mount inside Cwd looked
			// like it was inside the workspace.
			poisoned := &Job{Cwd: cwd, ActualCwd: cwd, MountConfigs: MountConfigs{{Mount: incoming}}}

			_, err = poisoned.Unmount()

			soPathsExist(incoming, userDir, cwd)

			So(err, ShouldBeNil)
		})
	})
}

func TestCleanupWithRelativeCwd(t *testing.T) {
	if runnermode || servermode {
		return
	}

	Convey("A Job whose Cwd is relative still has its live mount points protected", t, func() {
		// Job.Cwd is stored exactly as the user typed it - cmd/add.go does not
		// normalise it, and it cannot, because Cwd feeds Job.Key() and so job
		// identity. A relative Cwd makes the ActualCwd built from it relative
		// too, while an absolute MountConfig.Mount stays absolute; comparing one
		// of each made filepath.Rel fail, the mount go unrecognised, and cleanup
		// delete straight through it while it was still live.
		base := t.TempDir()

		oldWd, err := os.Getwd()
		So(err, ShouldBeNil)

		So(os.Chdir(base), ShouldBeNil)

		Reset(func() { So(os.Chdir(oldWd), ShouldBeNil) })

		work := filepath.Join(base, "work")
		So(os.MkdirAll(work, os.ModePerm), ShouldBeNil)

		actualCwd, _, err := mkHashedDir(work, "0123456789abcdef0123456789abcdef")
		So(err, ShouldBeNil)

		mounted := filepath.Join(actualCwd, "mnt", "REMOTE_DATA")
		So(os.MkdirAll(filepath.Dir(mounted), os.ModePerm), ShouldBeNil)
		So(os.WriteFile(mounted, []byte("remote\n"), 0o600), ShouldBeNil)

		relCwd, err := filepath.Rel(base, work)
		So(err, ShouldBeNil)

		relActualCwd, err := filepath.Rel(base, actualCwd)
		So(err, ShouldBeNil)

		job := &Job{
			Cwd:          relCwd,
			ActualCwd:    relActualCwd,
			MountConfigs: MountConfigs{{Mount: filepath.Join(actualCwd, "mnt")}},
		}

		err = (&Behaviour{When: OnExit, Do: CleanupAll}).Trigger(OnExit, job)

		soPathsExist(mounted)

		// refused outright, rather than cleaned relative to whichever process
		// happens to be running: cleanup runs in the runner AND in the manager
		// when it declares a job lost, and filepath.Abs resolves against the
		// caller. A relative Cwd made every containment proof hold against the
		// MANAGER's directory instead, deleting whatever sat at the same
		// relative path beside it. A leaked workspace and a loud error is the
		// right way round.
		So(err, ShouldNotBeNil)
		So(errors.Is(err, errNotBelowBaseDir), ShouldBeTrue)
	})
}

func TestCleanupGuardsWithNoOtherCoverage(t *testing.T) {
	if runnermode || servermode {
		return
	}

	// both of these were found to be load-bearing but UNTESTED: mutating either
	// one left the whole suite green while a user's directory was deleted. They
	// are here so a later simplification cannot remove them silently.
	Convey("Given a user tree at exactly the depth wr creates its workspaces at", t, func() {
		cwd := t.TempDir()
		userTree := filepath.Join(cwd, "a", "b", "c", "d", "e")
		userDir := filepath.Join(userTree, "userdata")
		err := os.MkdirAll(userDir, os.ModePerm)
		So(err, ShouldBeNil)

		precious := filepath.Join(userTree, "precious.txt")
		err = os.WriteFile(precious, []byte("important\n"), 0o600)
		So(err, ShouldBeNil)

		// depth and name both satisfied, but wr never made any of it
		fabricated := filepath.Join(userTree, "cwd")

		Convey("Unmount's tidy-up will not walk it (isRealDirBelow)", func() {
			job := &Job{
				Cwd: cwd, ActualCwd: fabricated,
				MountConfigs: MountConfigs{{Mount: "../userdata"}},
			}

			_, err = job.Unmount()

			soPathsExist(userDir, userTree, cwd)
		})

		Convey("cleanup will not sweep it when the named cwd does not exist", func() {
			err = (&Behaviour{When: OnExit, Do: CleanupAll}).Trigger(OnExit, job(cwd, fabricated))

			soPathsExist(precious, userTree, cwd)

			So(err, ShouldNotBeNil)
		})
	})
}

// job builds a minimal non-cwd_matters Job for the guard tests above.
func job(cwd, actualCwd string) *Job {
	return &Job{Cwd: cwd, ActualCwd: actualCwd}
}

func TestBehaviourCleanupSafety(t *testing.T) {
	if runnermode || servermode {
		return
	}

	// testMountDir is the relative Mount of a MountConfig in these tests: the dir
	// inside the Job's ActualCwd that cleanup must leave alone.
	const testMountDir = "mnt"

	Convey("Given a Job Cwd inside a dir of precious user files", t, func() {
		parent := t.TempDir()
		cwd := filepath.Join(parent, "wr_RunCisEQTL")
		err := os.MkdirAll(cwd, os.ModePerm)
		So(err, ShouldBeNil)

		precious := filepath.Join(parent, "05_RunCisEQTL.R")

		err = os.WriteFile(precious, []byte("important\n"), 0o600)
		So(err, ShouldBeNil)

		cleanupAll := &Behaviour{When: OnExit, Do: CleanupAll}
		cleanup := &Behaviour{When: OnExit, Do: Cleanup}

		Convey("Cleanup deletes nothing when the reported workspace does not exist either", func() {
			// the must-exist check on the working dir is never reached when the
			// WORKSPACE is missing too: emptyWorkSpace returns early, and the
			// upward walk still ran, unlinking every empty directory up to Cwd.
			// One more non-existent path element was all it took.
			userTop := filepath.Join(cwd, "results")
			userMid := filepath.Join(userTop, "2024")
			err = os.MkdirAll(userMid, os.ModePerm)
			So(err, ShouldBeNil)

			job := &Job{Cwd: cwd, ActualCwd: filepath.Join(userMid, "missing", "cwd")}

			err = cleanupAll.Trigger(OnExit, job)

			soPathsExist(userMid, userTop, cwd, precious, parent)

			So(err, ShouldNotBeNil)
			So(errors.Is(err, errNotACreatedCwd), ShouldBeTrue)
		})

		Convey("Cleanup deletes nothing of a user dir that merely contains a cwd dir", func() {
			// the name check reads only the last component, so a user directory
			// owning a subdirectory called "cwd" - which is what wr itself names
			// things, and a common staging convention - passed it, and the whole
			// of that directory was swept: not just empty dirs, every file in it.
			analysis := filepath.Join(cwd, "analysis")
			script := filepath.Join(analysis, "run.R")
			deep := filepath.Join(analysis, "results", "final")
			err = os.MkdirAll(filepath.Join(analysis, "cwd"), os.ModePerm)
			So(err, ShouldBeNil)

			err = os.MkdirAll(deep, os.ModePerm)
			So(err, ShouldBeNil)

			err = os.WriteFile(script, []byte("important\n"), 0o600)
			So(err, ShouldBeNil)

			job := &Job{Cwd: cwd, ActualCwd: filepath.Join(analysis, "cwd")}

			err = cleanupAll.Trigger(OnExit, job)

			soPathsExist(script, deep, analysis, cwd, precious, parent)

			So(err, ShouldNotBeNil)
		})

		Convey("Cleanup deletes nothing when the reported cwd does not exist", func() {
			// the name check alone reads only the last component, so appending
			// "/cwd" to any directory of the user's inside Cwd satisfies it -
			// and the named dir did not have to exist for its PARENT to be
			// swept as a workspace. This is the same attack as the Convey below
			// with one path element added, so the two belong together.
			scripts := filepath.Join(cwd, "scripts")
			script := filepath.Join(scripts, "05_RunCisEQTL.R")
			err = os.MkdirAll(scripts, os.ModePerm)
			So(err, ShouldBeNil)

			err = os.WriteFile(script, []byte("important\n"), 0o600)
			So(err, ShouldBeNil)

			job := &Job{Cwd: cwd}
			applyLiveSnapshot(job, &JobEndState{Cwd: filepath.Join(scripts, "cwd")})

			err = cleanup.Trigger(OnExit, job)

			soPathsExist(script, scripts, cwd, precious, parent)

			So(err, ShouldNotBeNil)
		})

		Convey("Cleanup deletes nothing when the reported cwd is not one wr created", func() {
			// the runner reports ActualCwd, and this is what a buggy or tampered
			// one can report: a real directory strictly inside the Job's own
			// Cwd, but one the USER made rather than one wr made. Containment
			// says yes to it, and its parent would then be swept as the
			// disposable workspace - the exact shape of the incident this PR
			// exists for, arriving through a different field than last time.
			userDir := filepath.Join(cwd, "userdata")
			results := filepath.Join(userDir, "results")
			err = os.MkdirAll(results, os.ModePerm)
			So(err, ShouldBeNil)

			notes := filepath.Join(userDir, "notes.txt")
			err = os.WriteFile(notes, []byte("precious\n"), 0o600)
			So(err, ShouldBeNil)

			job := &Job{Cwd: cwd}
			applyLiveSnapshot(job, &JobEndState{Cwd: results})

			// the value is stored - it is what wr shows for the job - but its
			// last component is not the name wr gives the working dirs it
			// creates, so cleanup will not treat its parent as a workspace.
			So(job.ActualCwd, ShouldEqual, results)

			err = cleanup.Trigger(OnExit, job)

			// survival first: a broken guard shows up as the deletion it is.
			soPathsExist(notes, results, userDir, cwd, precious)

			So(err, ShouldNotBeNil)
			So(errors.Is(err, errNotACreatedCwd), ShouldBeTrue)
		})

		Convey("Cleanup of a CwdMatters Job does nothing, even if it has an ActualCwd", func() {
			// wr <= v0.37.1 could persist ActualCwd == Cwd on such a Job, and
			// deleting the parent of that destroyed the user's own files.
			job := &Job{Cwd: cwd, CwdMatters: true, ActualCwd: cwd}

			So(cleanupAll.Trigger(OnExit, job), ShouldBeNil)
			So(cleanup.Trigger(OnExit, job), ShouldBeNil)

			soPathsExist(precious, parent, cwd)
		})

		Convey("Cleanup of a Job with an ActualCwd equal to its Cwd deletes nothing and errors", func() {
			job := &Job{Cwd: cwd, ActualCwd: cwd}

			err = cleanupAll.Trigger(OnExit, job)
			So(err, ShouldNotBeNil)
			So(errors.Is(err, errNotBelowBaseDir), ShouldBeTrue)

			soPathsExist(precious, parent, cwd)
		})

		Convey("Cleanup of a Job with an ActualCwd outside its Cwd deletes nothing and errors", func() {
			// this is the shape a stale ActualCwd takes after the Job's Cwd is
			// modified to somewhere else.
			stale := filepath.Join(testWorkSpace(filepath.Join(parent, "old_cwd"), "unique"), "cwd")
			err = os.MkdirAll(stale, os.ModePerm)
			So(err, ShouldBeNil)

			job := &Job{Cwd: cwd, ActualCwd: stale}

			err = cleanup.Trigger(OnExit, job)
			So(err, ShouldNotBeNil)
			So(errors.Is(err, errNotBelowBaseDir), ShouldBeTrue)

			soPathsExist(precious, stale, filepath.Join(parent, "old_cwd"))
		})

		Convey("Cleanup of a Job whose Cwd was modified deletes nothing, silently", func() {
			old := filepath.Join(parent, "old_cwd")
			ranIn := filepath.Join(testWorkSpace(old, "unique"), "cwd")
			err = os.MkdirAll(ranIn, os.ModePerm)
			So(err, ShouldBeNil)

			job := &Job{Cwd: old, ActualCwd: ranIn}

			jm := NewJobModifer()
			jm.SetCwd(cwd)
			jm.applyTo(job)

			So(cleanup.Trigger(OnExit, job), ShouldBeNil)
			So(cleanupAll.Trigger(OnExit, job), ShouldBeNil)

			soPathsExist(precious, parent, cwd, old, ranIn)
		})

		Convey("Cleanup of a Job with an ActualCwd that leaves Cwd via a symlink deletes nothing and errors", func() {
			err = os.Symlink(parent, filepath.Join(cwd, "escape"))
			So(err, ShouldBeNil)

			job := &Job{Cwd: cwd, ActualCwd: filepath.Join(cwd, "escape", "cwd")}

			err = cleanupAll.Trigger(OnExit, job)
			So(err, ShouldNotBeNil)
			So(errors.Is(err, errNotBelowBaseDir), ShouldBeTrue)

			soPathsExist(precious, parent, cwd)
		})

		Convey("Given a workspace that had already gone when cleanup checked it", func() {
			// the sibling Convey below swaps a component that DID exist at proof
			// time; this one covers the case where the proof found nothing at
			// the leaf, which is a normal outcome - another Job's cleanup, or a
			// re-run, can have removed the workspace already. The proof still
			// says "ok, nothing to do", and the upward walk still runs to tidy
			// empty parents, so the components above the missing leaf are just
			// as exposed to being swapped as the sibling's are.
			wrCwd := filepath.Join(cwd, "wr_cwd")
			err = os.Mkdir(wrCwd, os.ModePerm)
			So(err, ShouldBeNil)

			// no "unique" below it: the workspace is already gone.
			actualCwd := filepath.Join(wrCwd, "unique", "cwd")

			victim := filepath.Join(cwd, "userdata", "unique")
			err = os.MkdirAll(victim, os.ModePerm)
			So(err, ShouldBeNil)

			job := &Job{Cwd: cwd, ActualCwd: actualCwd}

			cleanupProvenHook = func() {
				cleanupProvenHook = nil

				So(os.Remove(wrCwd), ShouldBeNil)
				So(os.Symlink("userdata", wrCwd), ShouldBeNil)
			}

			Reset(func() { cleanupProvenHook = nil })

			Convey("Cleanup deletes nothing through a component swapped after the check", func() {
				err = cleanupAll.Trigger(OnExit, job)

				// survival first, so a lost race shows up as the deletion it is.
				soPathsExist(victim, filepath.Join(cwd, "userdata"), cwd, precious, parent)
			})
		})

		Convey("Given a workspace whose path is swapped for a symlink after cleanup has checked it", func() {
			// this is the race that the deletions are made through a handle on
			// Cwd to survive. A proof is about a path string, and every syscall
			// re-resolves that string, so a directory component checked to be
			// real can be a symlink by the time a deletion walks through it.
			// The hook puts the test in exactly that moment, which is otherwise
			// not reachable reliably.
			workSpace := testWorkSpace(cwd, "unique")
			actualCwd := filepath.Join(workSpace, "cwd")
			err = os.MkdirAll(actualCwd, os.ModePerm)
			So(err, ShouldBeNil)

			job := &Job{Cwd: cwd, ActualCwd: actualCwd}

			swapWrCwdFor := func(target string) {
				cleanupProvenHook = func() {
					cleanupProvenHook = nil
					wrCwd := filepath.Join(cwd, "wr_cwd")

					So(os.Rename(wrCwd, filepath.Join(cwd, "moved")), ShouldBeNil)
					So(os.Symlink(target, wrCwd), ShouldBeNil)
				}
			}

			Reset(func() { cleanupProvenHook = nil })

			Convey("Cleanup deletes nothing through a symlink leading out of Cwd", func() {
				decoy := filepath.Join(parent, "decoy", "unique", "cwd")
				err = os.MkdirAll(decoy, os.ModePerm)
				So(err, ShouldBeNil)

				swapWrCwdFor(filepath.Join("..", "decoy"))

				err = cleanupAll.Trigger(OnExit, job)

				// survival is asserted before the error, so that a lost race
				// shows up as the deletion it is, not as a missing error value.
				soPathsExist(decoy, precious, parent, cwd)

				So(err, ShouldNotBeNil)
			})

			Convey("Cleanup deletes nothing through a symlink leading elsewhere inside Cwd", func() {
				// staying inside Cwd is not enough on its own: the os.Root
				// handle permits a relative symlink that does, so the dir the
				// handle opens is also proven to be the dir that was checked.
				userData := filepath.Join(cwd, "userdata", "unique", "cwd")
				err = os.MkdirAll(userData, os.ModePerm)
				So(err, ShouldBeNil)

				swapWrCwdFor("userdata")

				err = cleanupAll.Trigger(OnExit, job)

				soPathsExist(userData, precious, parent, cwd)

				So(err, ShouldNotBeNil)
			})
		})

		Convey("Cleanup deletes nothing outside Cwd when the workspace it checked had already gone", func() {
			// with no workspace there to lstat, there is no inode for the open
			// to be checked against, so the handle on Cwd is all that stands
			// between the upward walk and the empty dirs it would unlink.
			victim := filepath.Join(parent, "victim", "unique")
			err = os.MkdirAll(victim, os.ModePerm)
			So(err, ShouldBeNil)

			wrCwd := filepath.Join(cwd, "wr_cwd")
			err = os.Mkdir(wrCwd, os.ModePerm)
			So(err, ShouldBeNil)

			cleanupProvenHook = func() {
				cleanupProvenHook = nil

				So(os.Remove(wrCwd), ShouldBeNil)
				So(os.Symlink(filepath.Join("..", "victim"), wrCwd), ShouldBeNil)
			}

			Reset(func() { cleanupProvenHook = nil })

			job := &Job{Cwd: cwd, ActualCwd: filepath.Join(wrCwd, "unique", "cwd")}

			err = cleanupAll.Trigger(OnExit, job)

			soPathsExist(victim, precious, parent, cwd)

			So(err, ShouldNotBeNil)
		})

		Convey("Cleanup of a mounting Job wipes its workspace but keeps the mount dir", func() {
			workSpace := testWorkSpace(cwd, "unique")
			actualCwd := filepath.Join(workSpace, "cwd")
			mounted := filepath.Join(actualCwd, testMountDir, "data.txt")
			err = os.MkdirAll(filepath.Dir(mounted), os.ModePerm)
			So(err, ShouldBeNil)

			err = os.WriteFile(mounted, []byte("mounted\n"), 0o600)
			So(err, ShouldBeNil)

			output := filepath.Join(actualCwd, "out.txt")
			err = os.WriteFile(output, []byte("output\n"), 0o600)
			So(err, ShouldBeNil)

			tmpDir := filepath.Join(workSpace, "tmp")
			err = os.Mkdir(tmpDir, os.ModePerm)
			So(err, ShouldBeNil)

			job := &Job{Cwd: cwd, ActualCwd: actualCwd, MountConfigs: MountConfigs{{Mount: testMountDir}}}

			So(cleanup.Trigger(OnExit, job), ShouldBeNil)

			soPathsExist(mounted, actualCwd, workSpace, cwd, precious)
			soPathsGone(output, tmpDir)
		})

		Convey("Given a mounting Job with a mount point in its workspace, outside its ActualCwd", func() {
			// sharing one mount between the Jobs of a Cwd means giving a
			// relative MountConfig.Mount that climbs out of the Job's own
			// ActualCwd, which lands it in the workspace that cleanup sweeps.
			// The mount is still live when cleanup runs (Job.Unmount comes
			// after it in client.go), and os.RemoveAll has no mount awareness,
			// so deleting it would recurse into the user's remote filesystem.
			workSpace := testWorkSpace(cwd, "unique")
			actualCwd := filepath.Join(workSpace, "cwd")
			err = os.MkdirAll(actualCwd, os.ModePerm)
			So(err, ShouldBeNil)

			output := filepath.Join(actualCwd, "out.txt")
			err = os.WriteFile(output, []byte("output\n"), 0o600)
			So(err, ShouldBeNil)

			tmpDir := filepath.Join(workSpace, "tmp")
			err = os.Mkdir(tmpDir, os.ModePerm)
			So(err, ShouldBeNil)

			Convey("Cleanup keeps a mount one level out of ActualCwd, and the mount inside it", func() {
				shared := filepath.Join(workSpace, "shared")
				remote := filepath.Join(shared, "remote.txt")
				err = os.MkdirAll(shared, os.ModePerm)
				So(err, ShouldBeNil)

				err = os.WriteFile(remote, []byte("remote\n"), 0o600)
				So(err, ShouldBeNil)

				mounted := filepath.Join(actualCwd, testMountDir, "data.txt")
				err = os.MkdirAll(filepath.Dir(mounted), os.ModePerm)
				So(err, ShouldBeNil)

				err = os.WriteFile(mounted, []byte("mounted\n"), 0o600)
				So(err, ShouldBeNil)

				job := &Job{
					Cwd:          cwd,
					ActualCwd:    actualCwd,
					MountConfigs: MountConfigs{{Mount: "../shared"}, {Mount: testMountDir}},
				}

				err = cleanup.Trigger(OnExit, job)

				// survival is asserted before the error, since a leaf stops at
				// its first failed So and the deletion is the evidence that
				// matters.
				soPathsExist(remote, shared, mounted, actualCwd, workSpace, cwd, precious)
				soPathsGone(output, tmpDir)

				So(err, ShouldBeNil)
			})

			Convey("Cleanup keeps a mount nested deeper out of ActualCwd, and the dirs above it", func() {
				nested := filepath.Join(workSpace, "a", "b")
				remote := filepath.Join(nested, "remote.txt")
				err = os.MkdirAll(nested, os.ModePerm)
				So(err, ShouldBeNil)

				err = os.WriteFile(remote, []byte("remote\n"), 0o600)
				So(err, ShouldBeNil)

				job := &Job{Cwd: cwd, ActualCwd: actualCwd, MountConfigs: MountConfigs{{Mount: "../a/b"}}}

				err = cleanup.Trigger(OnExit, job)

				soPathsExist(remote, nested, filepath.Join(workSpace, "a"), workSpace, cwd, precious)
				soPathsGone(output, tmpDir)

				So(err, ShouldBeNil)
			})

			Convey("Cleanup leaves an absolute mount outside the workspace alone, and still cleans", func() {
				outside := filepath.Join(parent, "shared_mount")
				remote := filepath.Join(outside, "remote.txt")
				err = os.MkdirAll(outside, os.ModePerm)
				So(err, ShouldBeNil)

				err = os.WriteFile(remote, []byte("remote\n"), 0o600)
				So(err, ShouldBeNil)

				job := &Job{Cwd: cwd, ActualCwd: actualCwd, MountConfigs: MountConfigs{{Mount: outside}}}

				err = cleanup.Trigger(OnExit, job)

				// nothing of the mount is inside the workspace, so the whole
				// workspace goes, as it does for a Job with no mounts at all.
				soPathsExist(remote, outside, cwd, precious)
				soPathsGone(output, tmpDir, actualCwd, workSpace)

				So(err, ShouldBeNil)
			})

			Convey("Cleanup of a Job with an absolute mount point inside its ActualCwd keeps the mount", func() {
				// MountConfig.Mount may be given as an absolute path ("in any
				// directory you're able to write to"), and an absolute path can
				// still land inside the Job's own ActualCwd. The mount is live
				// when cleanup runs (Job.Unmount comes after it in client.go),
				// so deleting through it would recurse into the user's remote
				// filesystem - exactly the hazard the relative case above
				// guards against.
				absMount := filepath.Join(actualCwd, "absmnt")
				mounted := filepath.Join(absMount, "data.txt")

				err = os.MkdirAll(absMount, os.ModePerm)
				So(err, ShouldBeNil)

				err = os.WriteFile(mounted, []byte("mounted\n"), 0o600)
				So(err, ShouldBeNil)

				job := &Job{Cwd: cwd, ActualCwd: actualCwd, MountConfigs: MountConfigs{{Mount: absMount}}}

				err = cleanup.Trigger(OnExit, job)

				soPathsExist(mounted, absMount, actualCwd, workSpace, cwd, precious)
				soPathsGone(output, tmpDir)

				So(err, ShouldBeNil)
			})

			Convey("Cleanup of a Job that mounts on its ActualCwd keeps that, but not the workspace extras", func() {
				job := &Job{Cwd: cwd, ActualCwd: actualCwd, MountConfigs: MountConfigs{{}}}

				err = cleanup.Trigger(OnExit, job)

				soPathsExist(output, actualCwd, workSpace, cwd, precious)
				soPathsGone(tmpDir)

				So(err, ShouldBeNil)
			})
		})

		Convey("Cleanup of a mounting Job still tidies empty parents when its workspace has gone", func() {
			// cleanup runs more than once for the same Job: the runner does it
			// (client.go), and for a lost job the server does it again
			// (killLostJobAndTriggerBehaviours). In between, Job.Unmount's
			// rmEmptyDirs deletes the workspace the first cleanup emptied, so
			// the second cleanup finds nothing there. That must not stop it
			// tidying the empty parents that Unmount's walk could not.
			workSpace := testWorkSpace(cwd, "unique")
			hashDir := filepath.Dir(workSpace)
			actualCwd := filepath.Join(workSpace, "cwd")
			err = os.MkdirAll(filepath.Join(actualCwd, testMountDir), os.ModePerm)
			So(err, ShouldBeNil)

			err = os.Mkdir(filepath.Join(workSpace, "tmp"), os.ModePerm)
			So(err, ShouldBeNil)

			// another Job of the same Cwd is running from a sibling dir, which
			// stops Unmount's upward walk at hashDir.
			sibling := filepath.Join(hashDir, "other")
			err = os.Mkdir(sibling, os.ModePerm)
			So(err, ShouldBeNil)

			job := &Job{Cwd: cwd, ActualCwd: actualCwd, MountConfigs: MountConfigs{{Mount: testMountDir}}}

			So(cleanup.Trigger(OnExit, job), ShouldBeNil)

			_, err = job.Unmount()
			So(err, ShouldBeNil)
			soPathsGone(workSpace)

			// the other Job then finishes and removes its own dir, leaving
			// hashDir and wr_cwd empty for our second cleanup to tidy.
			err = os.RemoveAll(sibling)
			So(err, ShouldBeNil)

			err = cleanup.Trigger(OnExit, job)

			// the tidying is asserted before the error, since a leaf stops at
			// its first failed So and the leaked dirs are the evidence that
			// matters.
			soPathsGone(hashDir, filepath.Join(cwd, "wr_cwd"))
			soPathsExist(cwd, precious)

			So(err, ShouldBeNil)
		})

		Convey("Cleanup of a mounting Job whose ActualCwd is a symlink out of Cwd deletes nothing there", func() {
			// a Job's own Cmd can replace its working directory with a symlink,
			// and the mounts branch reaches ActualCwd through os.ReadDir, which
			// follows one; proving only its parent would not notice.
			outside := filepath.Join(parent, "outside")
			err = os.Mkdir(outside, os.ModePerm)
			So(err, ShouldBeNil)

			outsideFile := filepath.Join(outside, "precious.txt")
			err = os.WriteFile(outsideFile, []byte("important\n"), 0o600)
			So(err, ShouldBeNil)

			workSpace := testWorkSpace(cwd, "unique")
			err = os.MkdirAll(workSpace, os.ModePerm)
			So(err, ShouldBeNil)

			actualCwd := filepath.Join(workSpace, "cwd")
			err = os.Symlink(outside, actualCwd)
			So(err, ShouldBeNil)

			job := &Job{Cwd: cwd, ActualCwd: actualCwd, MountConfigs: MountConfigs{{Mount: testMountDir}}}

			err = cleanup.Trigger(OnExit, job)

			// survival is asserted before the error, since a leaf stops at its
			// first failed So and the deletion is the evidence that matters.
			soPathsExist(outsideFile, outside, precious, parent, cwd)

			So(err, ShouldNotBeNil)
			So(errors.Is(err, errNotBelowBaseDir), ShouldBeTrue)
		})

		Convey("Given an ActualCwd that a Job's Cmd replaced with a symlink to elsewhere inside Cwd", func() {
			// wr made ActualCwd itself, so a symlink there means the Job's own
			// Cmd swapped it. Proving the symlink's target instead would make
			// the workspace out to be the target's parent - unrelated user data
			// - and delete that, while the real workspace survived.
			userDir := filepath.Join(cwd, "userdata")
			results := filepath.Join(userDir, "results")
			err = os.MkdirAll(results, os.ModePerm)
			So(err, ShouldBeNil)

			sibling := filepath.Join(userDir, "sibling.txt")
			err = os.WriteFile(sibling, []byte("mine\n"), 0o600)
			So(err, ShouldBeNil)

			workSpace := testWorkSpace(cwd, "unique")
			err = os.MkdirAll(filepath.Join(workSpace, "tmp"), os.ModePerm)
			So(err, ShouldBeNil)

			actualCwd := filepath.Join(workSpace, "cwd")
			err = os.Symlink(results, actualCwd)
			So(err, ShouldBeNil)

			// with no way to tell which dir wr is entitled to delete, cleanup
			// deletes nothing at all: leaving a workspace behind is recoverable,
			// deleting the user's own dir is not.
			Convey("Cleanup deletes nothing and errors", func() {
				job := &Job{Cwd: cwd, ActualCwd: actualCwd}

				err = cleanup.Trigger(OnExit, job)

				// survival is asserted before the error, since a leaf stops at
				// its first failed So and the deletion is the evidence that
				// matters.
				soPathsExist(sibling, results, userDir, workSpace, actualCwd, cwd, precious)

				So(err, ShouldNotBeNil)
				So(errors.Is(err, errNotBelowBaseDir), ShouldBeTrue)
			})

			Convey("Cleanup of a mounting Job deletes nothing and errors", func() {
				job := &Job{Cwd: cwd, ActualCwd: actualCwd, MountConfigs: MountConfigs{{Mount: testMountDir}}}

				err = cleanup.Trigger(OnExit, job)

				soPathsExist(sibling, results, userDir, workSpace, actualCwd, cwd, precious)

				So(err, ShouldNotBeNil)
				So(errors.Is(err, errNotBelowBaseDir), ShouldBeTrue)
			})
		})

		Convey("Cleanup of a mounting Job with an ActualCwd ending in .. deletes nothing and errors", func() {
			// an unclean ActualCwd, as a rogue client or a corrupt database
			// could supply: its parent proves fine, but cleaning the raw string
			// gives Cwd itself, which is what the incident destroyed.
			workSpace := filepath.Join(cwd, "wr_cwd")
			err = os.MkdirAll(filepath.Join(workSpace, "unique"), os.ModePerm)
			So(err, ShouldBeNil)

			script := filepath.Join(cwd, "04_RunCisEQTL.R")
			err = os.WriteFile(script, []byte("important\n"), 0o600)
			So(err, ShouldBeNil)

			job := &Job{
				Cwd:          cwd,
				ActualCwd:    workSpace + string(filepath.Separator) + "..",
				MountConfigs: MountConfigs{{Mount: testMountDir}},
			}

			err = cleanup.Trigger(OnExit, job)

			soPathsExist(script, workSpace, cwd, precious, parent)

			So(err, ShouldNotBeNil)
			So(errors.Is(err, errNotBelowBaseDir), ShouldBeTrue)
		})

		Convey("Given a mounting Job whose ActualCwd holds a mount dir and an output file", func() {
			// a relative MountConfig.Mount is whatever the user typed for `wr
			// add --mounts`, so it can name somewhere outside the dir being
			// cleared. Walking up from such a dir never reaches that dir, and
			// used to walk past the filesystem root forever, hanging both the
			// runner and the manager goroutine that cleans up a lost job.
			const cleanupTimeout = 5 * time.Second

			workSpace := testWorkSpace(cwd, "unique")
			actualCwd := filepath.Join(workSpace, "cwd")
			mounted := filepath.Join(actualCwd, testMountDir, "data.txt")
			err = os.MkdirAll(filepath.Dir(mounted), os.ModePerm)
			So(err, ShouldBeNil)

			err = os.WriteFile(mounted, []byte("mounted\n"), 0o600)
			So(err, ShouldBeNil)

			output := filepath.Join(actualCwd, "out.txt")
			err = os.WriteFile(output, []byte("output\n"), 0o600)
			So(err, ShouldBeNil)

			Convey("Cleanup keeps the whole ActualCwd when a Mount of \".\" names it", func() {
				// "." resolves to ActualCwd itself, exactly as "" does, so this
				// is the same case as the mounts-on-its-ActualCwd Convey above
				// and must be treated the same way: the mount is live when
				// cleanup runs, so deleting ActualCwd's contents would delete
				// through it. Reading MountConfig.Mount as a raw string instead
				// of resolving it used to make "." and "" mean opposite things
				// here.
				job := &Job{
					Cwd:          cwd,
					ActualCwd:    actualCwd,
					MountConfigs: MountConfigs{{Mount: "."}, {Mount: testMountDir}},
				}

				returned, cleanupErr := triggerWithin(cleanup, job, cleanupTimeout)

				So(returned, ShouldBeTrue)
				soPathsExist(mounted, output, actualCwd, workSpace, cwd, precious)

				So(cleanupErr, ShouldBeNil)
			})

			for _, escape := range []string{"../evil", ".."} {
				Convey("Cleanup finishes and still keeps the real mount dir, despite a Mount of "+escape, func() {
					job := &Job{
						Cwd:          cwd,
						ActualCwd:    actualCwd,
						MountConfigs: MountConfigs{{Mount: escape}, {Mount: testMountDir}},
					}

					returned, cleanupErr := triggerWithin(cleanup, job, cleanupTimeout)

					// termination and the kept mount are asserted before the
					// error, since a leaf stops at its first failed So and a
					// hang is the failure that matters.
					So(returned, ShouldBeTrue)
					soPathsExist(mounted, actualCwd, workSpace, cwd, precious)
					soPathsGone(output)

					So(cleanupErr, ShouldBeNil)
				})
			}
		})
	})
}

// soPathsExist asserts that each of the given paths still exists.
func soPathsExist(paths ...string) {
	for _, path := range paths {
		_, err := os.Stat(path)
		So(err, ShouldBeNil)
	}
}

// soPathsGone asserts that each of the given paths has been deleted.
func soPathsGone(paths ...string) {
	for _, path := range paths {
		_, err := os.Stat(path)
		So(os.IsNotExist(err), ShouldBeTrue)
	}
}

// triggerWithin runs the given Behaviour on the given Job in a goroutine and
// returns whether it finished within d, along with the error it gave if it did.
// Cleanup of a Job can hang forever, and calling it directly would then take
// out the whole test binary instead of failing this one assertion.
func triggerWithin(b *Behaviour, j *Job, d time.Duration) (bool, error) {
	errCh := make(chan error, 1)

	go func() { errCh <- b.Trigger(OnExit, j) }()

	select {
	case err := <-errCh:
		return true, err
	case <-time.After(d):
		return false, nil
	}
}
