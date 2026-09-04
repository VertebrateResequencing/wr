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
	"runtime"
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
		job1 := &Job{Cwd: cwd}
		actualCwd, workSpace, _ := realWorkSpace(job1)
		_, err := os.OpenFile(filepath.Join(actualCwd, "a.file"), os.O_RDONLY|os.O_CREATE, 0o666)
		So(err, ShouldBeNil)
		_, err = os.OpenFile(filepath.Join(actualCwd, "b.file"), os.O_RDONLY|os.O_CREATE, 0o666)
		So(err, ShouldBeNil)

		foo := filepath.Join(filepath.Dir(workSpace), "foo")
		// the top of the chain wr creates below Cwd, which the upward walk
		// stops before deleting.
		adir := filepath.Join(cwd, AppName+createdCwdBaseSuffix)
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

			// job2 is not CwdMatters and has reported no ActualCwd, so its Cmd
			// never ran in Cwd and the behaviour must not run there either; see
			// jobWorkSpaceSnapshot.cwdRunDir.
			err = b5.Trigger(OnSuccess, job2)
			So(err, ShouldNotBeNil)

			foo2 := filepath.Join(cwd, "foo")
			_, err = os.Stat(foo2)
			So(err, ShouldNotBeNil)

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
			_, err = os.Stat(workSpace)
			So(err, ShouldNotBeNil)
		})

		Convey("OnExit triggers after others", func() {
			bs := Behaviours{b1, b4}
			err = bs.Trigger(true, job1)
			So(err, ShouldBeNil)
			_, err = os.Stat(foo)
			So(err, ShouldBeNil)
			_, err = os.Stat(workSpace)
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

// testWorkSpace returns the workspace path mkHashedDir would build for job below
// its own Cwd, with digits standing in for the ones os.MkdirTemp appends to the
// unique dir. It creates nothing: these are the fixtures for the cases where what
// is on disk is NOT what wr built - a symlinked component, a missing level, a
// directory of the user's in its place - which realWorkSpace cannot produce.
//
// It has to be the path THIS job's key builds, because nothing else licenses
// cleanup to touch it, so the JOB has to be settled first: its Cmd and its
// MountConfigs both reach Key().
func testWorkSpace(job *Job, digits string) string {
	return testWorkSpaceUnder(filepath.Join(job.Cwd, AppName+createdCwdBaseSuffix), job, digits)
}

// testWorkSpaceUnder is testWorkSpace with the base component named explicitly,
// for the fixtures whose base is a symlink out of Cwd.
func testWorkSpaceUnder(base string, job *Job, digits string) string {
	dir, leaf := calculateHashedDir(base, job.Key())

	return filepath.Join(dir, leaf+digits)
}

// testActualCwd is testWorkSpace's working directory: what mkCwdAndTmp makes
// inside the workspace.
func testActualCwd(job *Job, digits string) string {
	return filepath.Join(testWorkSpace(job, digits), createdCwdName)
}

func TestJobUnmountEmptyDirTidyUp(t *testing.T) {
	if runnermode || servermode {
		return
	}

	Convey("Unmount's empty-dir tidy-up stays inside the workspace wr made", t, func() {
		cwd := t.TempDir()

		// MountConfig.Mount may be an absolute path to any directory the user can
		// write to, including an existing one of their own, and the tidy-up
		// removes empty dirs and then their empty parents.
		userDir := filepath.Join(cwd, "userdata")
		incoming := filepath.Join(userDir, "incoming")
		So(os.MkdirAll(incoming, os.ModePerm), ShouldBeNil)

		job := &Job{Cwd: cwd, MountConfigs: MountConfigs{{Mount: incoming}}}
		realWorkSpace(job)

		_, err := job.Unmount()

		soPathsExist(incoming, userDir, cwd)

		So(err, ShouldBeNil)

		Convey("and does nothing at all when ActualCwd is not one wr could have created", func() {
			// the v0.37.0|1 poisoning shape: an ActualCwd equal to Cwd, whose
			// unchecked parent would be the parent of Cwd, making every mount
			// inside Cwd look like it was inside the workspace.
			poisoned := &Job{Cwd: cwd, ActualCwd: cwd, MountConfigs: MountConfigs{{Mount: incoming}}}

			_, err = poisoned.Unmount()

			soPathsExist(incoming, userDir, cwd)

			So(err, ShouldBeNil)
		})
	})
}

func TestJobUnmountTidyUpCostsNothingWithoutMounts(t *testing.T) {
	if runnermode || servermode {
		return
	}

	// Client.Execute unmounts at the end of EVERY run, and the tidy-up walks up
	// from the Job's mount points, so a Job with no mounts has nothing to tidy.
	// Resolving its workspace to discover that costs the hash Key() makes of the
	// Job and an lstat per component of the path below Cwd, on the exit path of
	// the overwhelming majority of Jobs. The result is the same either way, so
	// only the resolutions made show it.
	Convey("Given a count of the workspace resolutions Unmount makes", t, func() {
		resolutions := 0

		workSpaceResolveHook = func() { resolutions++ }

		Reset(func() { workSpaceResolveHook = nil })

		cwd := t.TempDir()

		Convey("Unmount resolves no workspace at all for a Job with no mounts", func() {
			job := &Job{Cwd: cwd, Cmd: testWSCmd}
			realWorkSpace(job)

			_, err := job.Unmount()
			So(err, ShouldBeNil)
			So(resolutions, ShouldEqual, 0)
		})

		Convey("Unmount still resolves it, and tidies up, for a Job with a mount", func() {
			job := &Job{Cwd: cwd, Cmd: testWSCmd, MountConfigs: MountConfigs{{Mount: testWSMount}}}
			actualCwd, _, _ := realWorkSpace(job)
			mount := filepath.Join(actualCwd, testWSMount)
			So(os.MkdirAll(mount, os.ModePerm), ShouldBeNil)

			_, err := job.Unmount()
			So(err, ShouldBeNil)

			// the count first, since this test is about the work done: the tidy-up
			// below then shows the resolution it paid for was used.
			So(resolutions, ShouldEqual, 1)

			soPathsGone(mount)
			soPathsExist(cwd)
		})
	})
}

func TestCleanupWithRelativeCwd(t *testing.T) {
	if runnermode || servermode {
		return
	}

	Convey("A Job whose Cwd is relative still has its live mount points protected", t, func() {
		// Job.Cwd is stored exactly as the user typed it, since it feeds
		// Job.Key(), so a relative Cwd makes the ActualCwd built from it relative
		// too while an absolute MountConfig.Mount stays absolute: comparing one of
		// each makes filepath.Rel fail and the live mount go unrecognised.
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
		// when it declares a job lost, so a relative Cwd would make every
		// containment proof hold against the MANAGER's directory instead.
		So(err, ShouldNotBeNil)
		So(errors.Is(err, errNotBelowBaseDir), ShouldBeTrue)
	})
}

func TestCleanupGuardsWithNoOtherCoverage(t *testing.T) {
	if runnermode || servermode {
		return
	}

	// each of these guards is load-bearing: without it a user's own directory is
	// deleted. They are pinned here so a later simplification cannot remove one
	// silently.
	Convey("Given the workspace wr really made, whose working dir has been swapped", t, func() {
		// these two guards are about what wr finds where it PUT its own working
		// directory, so the fixture has to be wr's own: a hand-made tree is
		// refused by the identity check and neither guard is reached. A Job's own
		// Cmd is what swaps the directory, and it can point the link at a tree of
		// the user's.
		cwd := t.TempDir()
		userDir := filepath.Join(cwd, "userdata")
		empty := filepath.Join(userDir, "results")
		So(os.MkdirAll(empty, os.ModePerm), ShouldBeNil)

		swappedFor := func(mount string, swap func(actualCwd string)) *Job {
			j := &Job{Cwd: cwd, Cmd: testWSCmd, MountConfigs: MountConfigs{{Mount: mount}}}
			actualCwd, _, _ := realWorkSpace(j)

			So(os.Remove(actualCwd), ShouldBeNil)
			swap(actualCwd)

			return j
		}

		Convey("Unmount's tidy-up will not walk it when the working dir is a symlink", func() {
			// proving it is a real dir is what stops the tidy-up walking the
			// user's tree and unlinking their empty dirs, and then the empty dirs
			// above those, all the way up to Cwd.
			j := swappedFor("../../../../../userdata/results", func(actualCwd string) {
				So(os.Symlink(userDir, actualCwd), ShouldBeNil)
			})

			_, err := j.Unmount()

			soPathsExist(empty, userDir, cwd)
			So(err, ShouldBeNil)
		})

		Convey("cleanup will not sweep it when the working dir is a symlink", func() {
			j := swappedFor("mnt", func(actualCwd string) {
				So(os.Symlink(userDir, actualCwd), ShouldBeNil)
			})

			err := (&Behaviour{When: OnExit, Do: CleanupAll}).Trigger(OnExit, j)

			soPathsExist(empty, userDir, cwd)

			So(err, ShouldNotBeNil)
			So(errors.Is(err, errNotBelowBaseDir), ShouldBeTrue)
		})

		Convey("cleanup will not sweep it when the working dir has become a file", func() {
			j := swappedFor("mnt", func(actualCwd string) {
				So(os.WriteFile(actualCwd, []byte("not a dir\n"), 0o600), ShouldBeNil)
			})

			err := (&Behaviour{When: OnExit, Do: CleanupAll}).Trigger(OnExit, j)

			soPathsExist(empty, userDir, cwd)

			So(err, ShouldNotBeNil)
			So(errors.Is(err, errNotBelowBaseDir), ShouldBeTrue)
		})

		Convey("a run behaviour refuses a working dir that has become a file", func() {
			// the resolution has to say no to this itself, since nothing after it
			// does: openVerifiedDirFile proves only that what it opened is what
			// was lstat'ed, and a regular file is. So the refusal is asserted to
			// be the resolution's own.
			j := swappedFor("mnt", func(actualCwd string) {
				So(os.WriteFile(actualCwd, []byte("not a dir\n"), 0o600), ShouldBeNil)
			})

			err := runBehaviour().Trigger(OnExit, j)

			So(err, ShouldNotBeNil)
			So(errors.Is(err, errNotBelowBaseDir), ShouldBeTrue)
			So(err.Error(), ShouldNotContainSubstring, "chdir")
		})
	})

	Convey("Given a user tree at exactly the depth wr creates its workspaces at", t, func() {
		// the tree's top component is named the way wr names the base of its own,
		// so that what refuses it is the part of the identity check each case is
		// about rather than the base name.
		cwd := t.TempDir()
		userTree := filepath.Join(cwd, "user"+createdCwdBaseSuffix, "b", "c", "d", "e")
		userDir := filepath.Join(userTree, "userdata")
		err := os.MkdirAll(userDir, os.ModePerm)
		So(err, ShouldBeNil)

		precious := filepath.Join(userTree, "precious.txt")
		err = os.WriteFile(precious, []byte("important\n"), 0o600)
		So(err, ShouldBeNil)

		// depth and name both satisfied, but wr never made any of it
		fabricated := filepath.Join(userTree, "cwd")

		Convey("Unmount's tidy-up will not walk it", func() {
			job := &Job{
				Cwd: cwd, ActualCwd: fabricated,
				MountConfigs: MountConfigs{{Mount: "../userdata"}},
			}

			_, err = job.Unmount()

			soPathsExist(userDir, userTree, cwd)
		})

		Convey("cleanup will not sweep it", func() {
			err = (&Behaviour{When: OnExit, Do: CleanupAll}).Trigger(OnExit, job(cwd, fabricated))

			soPathsExist(precious, userTree, cwd)

			So(err, ShouldNotBeNil)
			So(errors.Is(err, errNotACreatedCwd), ShouldBeTrue)
		})

		Convey("cleanup will not sweep a real dir of the right name at the wrong depth", func() {
			// every working directory wr creates sits at one depth, so a real
			// directory of the user's called cwd one level short of it was never
			// made by wr, and treating it as one would sweep its PARENT: here, a
			// directory of theirs holding their data.
			shallow := filepath.Join(cwd, "user"+createdCwdBaseSuffix, "b", "c")
			named := filepath.Join(shallow, createdCwdName)
			So(os.MkdirAll(named, os.ModePerm), ShouldBeNil)

			counts := filepath.Join(shallow, "counts.tsv")
			So(os.WriteFile(counts, []byte("counted\n"), 0o600), ShouldBeNil)

			err = (&Behaviour{When: OnExit, Do: CleanupAll}).Trigger(OnExit, job(cwd, named))

			soPathsExist(counts, named, shallow, cwd)

			So(err, ShouldNotBeNil)
			So(errors.Is(err, errNotACreatedCwd), ShouldBeTrue)
		})

		Convey("cleanup will not sweep a real dir at that depth that is not named cwd", func() {
			// depth alone is not enough. Every directory wr creates for a Job is
			// called "cwd", so a real directory of the user's sitting at exactly
			// the right depth under any other name was never made by wr - and
			// treating it as a working directory would sweep its PARENT, which
			// here is the whole of the user's tree.
			outputs := filepath.Join(userTree, "outputs")
			results := filepath.Join(outputs, "counts.tsv")
			err = os.MkdirAll(outputs, os.ModePerm)
			So(err, ShouldBeNil)

			err = os.WriteFile(results, []byte("counted\n"), 0o600)
			So(err, ShouldBeNil)

			err = (&Behaviour{When: OnExit, Do: CleanupAll}).Trigger(OnExit, job(cwd, outputs))

			soPathsExist(results, outputs, precious, userDir, userTree, cwd)

			So(err, ShouldNotBeNil)
			So(errors.Is(err, errNotACreatedCwd), ShouldBeTrue)
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
			// the must-exist check on the working dir is deliberately not
			// reached when the WORKSPACE is missing too - there is then nothing
			// inside it to identify or to delete - so the shape check on the
			// reported path is the only thing that stops the upward walk
			// unlinking every empty directory up to Cwd. One more non-existent
			// path element was all it took.
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
			// owning a subdirectory called "cwd" - what wr itself names things,
			// and a common staging convention - would otherwise have the whole of
			// that directory swept: not just empty dirs, every file in it.
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
			// a name check alone reads only the last component, so appending
			// "/cwd" to any directory of the user's inside Cwd satisfies it, and
			// the named dir need not exist for its PARENT to be swept as a
			// workspace. Same shape as the Convey below, one element deeper.
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
			// one can report: a real directory strictly inside the Job's own Cwd,
			// but one the USER made rather than one wr made. Containment says yes
			// to it, and its parent would then be swept as the workspace.
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
			old := filepath.Join(parent, "old_cwd")
			job := &Job{Cwd: old}
			stale, _, _ := realWorkSpace(job)
			job.Cwd = cwd

			err = cleanup.Trigger(OnExit, job)
			So(err, ShouldNotBeNil)
			So(errors.Is(err, errNotBelowBaseDir), ShouldBeTrue)

			soPathsExist(precious, stale, old)
		})

		Convey("Cleanup of a Job whose Cwd was modified deletes nothing, silently", func() {
			old := filepath.Join(parent, "old_cwd")
			job := &Job{Cwd: old, Cmd: testWSCmd}
			ranIn, _, _ := realWorkSpace(job)

			jm := NewJobModifer()
			jm.SetCwd(cwd)
			jm.applyTo(job)

			So(cleanup.Trigger(OnExit, job), ShouldBeNil)
			So(cleanupAll.Trigger(OnExit, job), ShouldBeNil)

			soPathsExist(precious, parent, cwd, old, ranIn)
		})

		Convey("Cleanup of a Job whose Cmd was modified deletes nothing, silently", func() {
			// the working directory wr made is named for the Job's key, and the
			// key covers the Cmd, the mounts and the container image. A `wr mod`
			// that changes any of them leaves a path the current definition cannot
			// build, so the modification clears it (JobModifier.applyTo) and
			// cleanup then has nothing it may touch: the workspace is leaked
			// rather than swept.
			job := &Job{Cwd: cwd, Cmd: testWSCmd}
			ranIn, workSpace, _ := realWorkSpace(job)

			jm := NewJobModifer()
			jm.SetCmd("echo something else")
			jm.applyTo(job)

			So(job.ActualCwd, ShouldBeBlank)
			So(cleanup.Trigger(OnExit, job), ShouldBeNil)
			So(cleanupAll.Trigger(OnExit, job), ShouldBeNil)

			soPathsExist(precious, cwd, ranIn, workSpace)
		})

		Convey("Cleanup of a Job whose Cmd was NOT modified still sweeps its workspace", func() {
			// the other half: a modification that changes no key leaves the
			// workspace recognisable, or the check would silently disable cleanup
			// for every modified job.
			job := &Job{Cwd: cwd, Cmd: testWSCmd}
			ranIn, workSpace, _ := realWorkSpace(job)

			jm := NewJobModifer()
			jm.SetPriority(7)
			jm.applyTo(job)

			So(cleanupAll.Trigger(OnExit, job), ShouldBeNil)

			soPathsGone(ranIn, workSpace, filepath.Join(cwd, AppName+createdCwdBaseSuffix))
			soPathsExist(precious, cwd)
		})

		Convey("Cleanup of a Job with an ActualCwd that leaves Cwd via a symlink deletes nothing and errors", func() {
			// the reported path is EXACTLY the one mkHashedDir builds for this
			// job, base component aside, so the lexical checks all pass it and
			// the symlinked component is what has to stop it. A fixture the
			// identity check refuses never reaches this guard.
			base := filepath.Join(cwd, "escape"+createdCwdBaseSuffix)
			err = os.Symlink(parent, base)
			So(err, ShouldBeNil)

			job := &Job{Cwd: cwd, Cmd: testWSCmd}
			job.ActualCwd = filepath.Join(testWorkSpaceUnder(base, job, "0"), createdCwdName)

			err = cleanupAll.Trigger(OnExit, job)
			So(err, ShouldNotBeNil)
			So(errors.Is(err, errNotBelowBaseDir), ShouldBeTrue)

			soPathsExist(precious, parent, cwd)
		})

		Convey("Cleanup deletes nothing when the workspace itself is a symlink inside Cwd", func() {
			// the deletion helpers disagree about symlinks - os.RemoveAll unlinks
			// a final one, os.ReadDir follows it - so proving every component is a
			// REAL dir is a distinct property from staying inside Cwd, which a
			// relative link like this one satisfies. Lstat'ing the working
			// directory says nothing here, because the link is resolved on the way
			// to it and what gets checked is the target's own "cwd" entry.
			userDir := filepath.Join(cwd, "userdata")
			target := filepath.Join(userDir, "cwd")
			notes := filepath.Join(target, "notes.txt")
			err = os.MkdirAll(target, os.ModePerm)
			So(err, ShouldBeNil)

			err = os.WriteFile(notes, []byte("precious\n"), 0o600)
			So(err, ShouldBeNil)

			job := &Job{Cwd: cwd, Cmd: testWSCmd}
			workSpace := testWorkSpace(job, "0")
			job.ActualCwd = testActualCwd(job, "0")

			err = os.MkdirAll(filepath.Dir(workSpace), os.ModePerm)
			So(err, ShouldBeNil)

			rel, errr := filepath.Rel(filepath.Dir(workSpace), userDir)
			So(errr, ShouldBeNil)

			err = os.Symlink(rel, workSpace)
			So(err, ShouldBeNil)

			err = cleanupAll.Trigger(OnExit, job)

			soPathsExist(notes, target, userDir, cwd, precious, parent)

			So(err, ShouldNotBeNil)
			So(errors.Is(err, errNotBelowBaseDir), ShouldBeTrue)
		})

		Convey("Given a workspace that had already gone when cleanup checked it", func() {
			// the sibling Convey below swaps a component that DID exist at proof
			// time; this one covers the proof finding nothing at the leaf, which is
			// a normal outcome. The upward walk still runs to tidy empty parents,
			// so the components above the missing leaf are just as exposed.
			wrCwd := filepath.Join(cwd, AppName+createdCwdBaseSuffix)
			err = os.Mkdir(wrCwd, os.ModePerm)
			So(err, ShouldBeNil)

			// nothing below it: the workspace is already gone.
			job := &Job{Cwd: cwd, Cmd: testWSCmd}
			job.ActualCwd = testActualCwd(job, "0")

			victim := filepath.Join(cwd, "userdata", "unique")
			err = os.MkdirAll(victim, os.ModePerm)
			So(err, ShouldBeNil)

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
			// this is the race that the deletions are made through a handle on Cwd
			// to survive: a component checked to be real can be a symlink by the
			// time a deletion walks through it. The hook puts the test in exactly
			// that moment.
			job := &Job{Cwd: cwd, Cmd: testWSCmd}
			realWorkSpace(job)

			swapWrCwdFor := func(target string) {
				cleanupProvenHook = func() {
					cleanupProvenHook = nil
					wrCwd := filepath.Join(cwd, AppName+createdCwdBaseSuffix)

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

			wrCwd := filepath.Join(cwd, AppName+createdCwdBaseSuffix)
			err = os.Mkdir(wrCwd, os.ModePerm)
			So(err, ShouldBeNil)

			cleanupProvenHook = func() {
				cleanupProvenHook = nil

				So(os.Remove(wrCwd), ShouldBeNil)
				So(os.Symlink(filepath.Join("..", "victim"), wrCwd), ShouldBeNil)
			}

			Reset(func() { cleanupProvenHook = nil })

			job := &Job{Cwd: cwd, Cmd: testWSCmd}
			job.ActualCwd = testActualCwd(job, "0")

			err = cleanupAll.Trigger(OnExit, job)

			soPathsExist(victim, precious, parent, cwd)

			So(err, ShouldNotBeNil)
		})

		Convey("Cleanup of a mounting Job wipes its workspace but keeps the mount dir", func() {
			job := &Job{Cwd: cwd, Cmd: testWSCmd, MountConfigs: MountConfigs{{Mount: testMountDir}}}
			actualCwd, workSpace, tmpDir := realWorkSpace(job)

			mounted := writeFileIn(filepath.Join(actualCwd, testMountDir), "data.txt")
			output := writeFileIn(actualCwd, "out.txt")

			So(cleanup.Trigger(OnExit, job), ShouldBeNil)

			soPathsExist(mounted, actualCwd, workSpace, cwd, precious)
			soPathsGone(output, tmpDir)
		})

		Convey("Given a mounting Job with a mount point in its workspace, outside its ActualCwd", func() {
			// sharing one mount between the Jobs of a Cwd means giving a relative
			// MountConfig.Mount that climbs out of the Job's own ActualCwd, which
			// lands it in the workspace that cleanup sweeps. The mount is still
			// live then (Job.Unmount comes after cleanup in client.go), and
			// os.RemoveAll has no mount awareness, so deleting it would recurse
			// into the user's remote filesystem.
			//
			// Each case builds its OWN workspace, because MountConfigs reach
			// Job.Key() and the workspace is named for the key.
			type mountingJob struct {
				job       *Job
				actualCwd string
				workSpace string
				output    string
				tmpDir    string
			}

			mounting := func(mounts ...MountConfig) mountingJob {
				job := &Job{Cwd: cwd, Cmd: testWSCmd, MountConfigs: mounts}
				actualCwd, workSpace, tmpDir := realWorkSpace(job)

				return mountingJob{
					job: job, actualCwd: actualCwd, workSpace: workSpace, tmpDir: tmpDir,
					output: writeFileIn(actualCwd, "out.txt"),
				}
			}

			Convey("Cleanup keeps a mount one level out of ActualCwd, and the mount inside it", func() {
				mj := mounting(MountConfig{Mount: "../shared"}, MountConfig{Mount: testMountDir})

				shared := filepath.Join(mj.workSpace, "shared")
				remote := writeFileIn(shared, "remote.txt")
				mounted := writeFileIn(filepath.Join(mj.actualCwd, testMountDir), "data.txt")

				err = cleanup.Trigger(OnExit, mj.job)

				// survival is asserted before the error, since a leaf stops at
				// its first failed So and the deletion is the evidence that
				// matters.
				soPathsExist(remote, shared, mounted, mj.actualCwd, mj.workSpace, cwd, precious)
				soPathsGone(mj.output, mj.tmpDir)

				So(err, ShouldBeNil)
			})

			Convey("Cleanup keeps a mount nested deeper out of ActualCwd, and the dirs above it", func() {
				mj := mounting(MountConfig{Mount: "../a/b"})

				nested := filepath.Join(mj.workSpace, "a", "b")
				remote := writeFileIn(nested, "remote.txt")

				err = cleanup.Trigger(OnExit, mj.job)

				soPathsExist(remote, nested, filepath.Join(mj.workSpace, "a"), mj.workSpace, cwd, precious)
				soPathsGone(mj.output, mj.tmpDir)

				So(err, ShouldBeNil)
			})

			Convey("Cleanup leaves an absolute mount outside the workspace alone, and still cleans", func() {
				outside := filepath.Join(parent, "shared_mount")
				remote := writeFileIn(outside, "remote.txt")

				mj := mounting(MountConfig{Mount: outside})

				err = cleanup.Trigger(OnExit, mj.job)

				// nothing of the mount is inside the workspace, so the whole
				// workspace goes, as it does for a Job with no mounts at all.
				soPathsExist(remote, outside, cwd, precious)
				soPathsGone(mj.output, mj.tmpDir, mj.actualCwd, mj.workSpace)

				So(err, ShouldBeNil)
			})

			// there is no case here for an absolute MountConfig.Mount naming
			// something inside the Job's OWN workspace, because there is no such
			// Job: MountConfigs.Key() covers every Mount string and the workspace
			// is named for that key, so writing the path would mean naming a
			// directory whose name depends on the string being written. What the
			// guard handles - a mount point resolving to an absolute path at or
			// inside the working directory - is reached by every relative Mount
			// above, since workSpacePaths.mountPoints resolves them all before
			// anything is classified, and by the absolute CacheDir case in
			// TestCleanupKeepsMountCaches, which CacheDir's absence from the key
			// makes constructible.

			Convey("Cleanup of a Job that mounts on its ActualCwd keeps that, but not the workspace extras", func() {
				mj := mounting(MountConfig{})

				err = cleanup.Trigger(OnExit, mj.job)

				soPathsExist(mj.output, mj.actualCwd, mj.workSpace, cwd, precious)
				soPathsGone(mj.tmpDir)

				So(err, ShouldBeNil)
			})

			Convey("Cleanup of a Job that mounts on its workspace keeps the whole workspace", func() {
				// MountConfig.Mount is resolved against the working directory, so
				// ".." names the workspace itself: the disposable directory
				// cleanup exists to sweep IS the live mount, and everything inside
				// it is then the user's remote objects, read through a mount
				// Job.Unmount has not got to yet.
				mj := mounting(MountConfig{Mount: ".."})

				remote := writeFileIn(mj.workSpace, "remote.txt")

				err = cleanup.Trigger(OnExit, mj.job)

				soPathsExist(remote, mj.output, mj.actualCwd, mj.tmpDir, mj.workSpace, cwd, precious)

				So(err, ShouldBeNil)
			})

			Convey("Cleanup keeps the workspace for a mount above it spelled through a symlink", func() {
				// the same directory "../.." names, reached by a different route:
				// a symlink to the Job's Cwd, then the name every Job of that Cwd
				// puts its workspaces under. Only the levels above the workspace
				// can be named this way, since their names do not depend on the
				// key. Same directory, so the same verdict.
				link := filepath.Join(parent, "cwd_link")
				So(os.Symlink(cwd, link), ShouldBeNil)

				mj := mounting(MountConfig{Mount: filepath.Join(link, AppName+createdCwdBaseSuffix)})

				remote := writeFileIn(mj.workSpace, "remote.txt")

				err = cleanup.Trigger(OnExit, mj.job)

				soPathsExist(remote, mj.output, mj.actualCwd, mj.tmpDir, mj.workSpace, cwd, precious)

				So(err, ShouldBeNil)
			})

			Convey("Cleanup keeps the workspace when the JOB's Cwd is the symlinked spelling", func() {
				// the same two spellings the other way round: the mount is named
				// by the real path, and the Job's Cwd - so its whole workspace -
				// by a symlink to it. Job.Cwd is stored exactly as the user typed
				// it, because it feeds Job.Key(), so this is the spelling wr is
				// given rather than one it chose.
				link := filepath.Join(parent, "job_cwd_link")
				So(os.Symlink(cwd, link), ShouldBeNil)

				job := &Job{
					Cwd: link, Cmd: testWSCmd,
					MountConfigs: MountConfigs{{Mount: filepath.Join(cwd, AppName+createdCwdBaseSuffix)}},
				}
				actualCwd, workSpace, tmpDir := realWorkSpace(job)

				output := writeFileIn(actualCwd, "out.txt")
				remote := writeFileIn(workSpace, "remote.txt")

				err = cleanup.Trigger(OnExit, job)

				soPathsExist(remote, output, actualCwd, tmpDir, workSpace, cwd, precious)

				So(err, ShouldBeNil)
			})

			Convey("Cleanup of a Job whose mount is ABOVE its workspace keeps the workspace too", func() {
				// the same thing one level further out: everything wr made for
				// the job is inside the mount, so there is nothing below it wr
				// may delete while it is live.
				mj := mounting(MountConfig{Mount: "../.."})

				remote := writeFileIn(mj.workSpace, "remote.txt")

				err = cleanup.Trigger(OnExit, mj.job)

				soPathsExist(remote, mj.output, mj.actualCwd, mj.tmpDir, mj.workSpace, cwd, precious)

				So(err, ShouldBeNil)
			})
		})

		Convey("Cleanup of a mounting Job still tidies empty parents when its workspace has gone", func() {
			// cleanup runs more than once for the same Job: the runner does it
			// (client.go), and for a lost job the server does it again
			// (killLostJobAndTriggerBehaviours). In between, Job.Unmount deletes
			// the workspace the first cleanup emptied, so the second finds nothing
			// there - which must not stop it tidying the empty parents.
			//
			// The workspace is built by the real mkHashedDir, because tolerating
			// its absence is exactly what the proof of origin licenses: a
			// hand-made path of the right shape is refused.
			job := &Job{Cwd: cwd, Cmd: "second cleanup", MountConfigs: MountConfigs{{Mount: testMountDir}}}
			actualCwd, workSpace, _ := realWorkSpace(job)
			hashDir := filepath.Dir(workSpace)

			err = os.MkdirAll(filepath.Join(actualCwd, testMountDir), os.ModePerm)
			So(err, ShouldBeNil)

			// another Job of the same Cwd is running from a sibling dir, which
			// stops Unmount's upward walk at hashDir.
			sibling := filepath.Join(hashDir, "other")
			err = os.Mkdir(sibling, os.ModePerm)
			So(err, ShouldBeNil)

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
			soPathsGone(hashDir, filepath.Join(cwd, AppName+"_cwd"))
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

			job := &Job{Cwd: cwd, Cmd: testWSCmd, MountConfigs: MountConfigs{{Mount: testMountDir}}}
			actualCwd, _, _ := realWorkSpace(job)

			So(os.Remove(actualCwd), ShouldBeNil)
			So(os.Symlink(outside, actualCwd), ShouldBeNil)

			err = cleanup.Trigger(OnExit, job)

			// survival is asserted before the error, since a leaf stops at its
			// first failed So and the deletion is the evidence that matters.
			soPathsExist(outsideFile, outside, precious, parent, cwd)

			So(err, ShouldNotBeNil)
			So(errors.Is(err, errNotBelowBaseDir), ShouldBeTrue)
		})

		Convey("Given an ActualCwd that a Job's Cmd replaced with a symlink to elsewhere inside Cwd", func() {
			// wr made ActualCwd itself, so a symlink there means the Job's own Cmd
			// swapped it. Proving the symlink's target instead would make the
			// workspace out to be the target's parent - unrelated user data - and
			// delete that, while the real workspace survived.
			userDir := filepath.Join(cwd, "userdata")
			results := filepath.Join(userDir, "results")
			err = os.MkdirAll(results, os.ModePerm)
			So(err, ShouldBeNil)

			sibling := writeFileIn(userDir, "sibling.txt")

			// the workspace is the real one for whichever Job the case builds,
			// since MountConfigs reach the key that names it, and only then is
			// the working directory swapped for the link.
			swapped := func(mounts MountConfigs) (job *Job, actualCwd, workSpace string) {
				job = &Job{Cwd: cwd, Cmd: testWSCmd, MountConfigs: mounts}
				actualCwd, workSpace, _ = realWorkSpace(job)

				So(os.Remove(actualCwd), ShouldBeNil)
				So(os.Symlink(results, actualCwd), ShouldBeNil)

				return job, actualCwd, workSpace
			}

			// with no way to tell which dir wr is entitled to delete, cleanup
			// deletes nothing at all: leaving a workspace behind is recoverable,
			// deleting the user's own dir is not.
			Convey("Cleanup deletes nothing and errors", func() {
				job, actualCwd, workSpace := swapped(nil)

				err = cleanup.Trigger(OnExit, job)

				// survival is asserted before the error, since a leaf stops at
				// its first failed So and the deletion is the evidence that
				// matters.
				soPathsExist(sibling, results, userDir, workSpace, actualCwd, cwd, precious)

				So(err, ShouldNotBeNil)
				So(errors.Is(err, errNotBelowBaseDir), ShouldBeTrue)
			})

			Convey("Cleanup of a mounting Job deletes nothing and errors", func() {
				job, actualCwd, workSpace := swapped(MountConfigs{{Mount: testMountDir}})

				err = cleanup.Trigger(OnExit, job)

				soPathsExist(sibling, results, userDir, workSpace, actualCwd, cwd, precious)

				So(err, ShouldNotBeNil)
				So(errors.Is(err, errNotBelowBaseDir), ShouldBeTrue)
			})
		})

		Convey("Cleanup of a mounting Job with an ActualCwd ending in .. deletes nothing and errors", func() {
			// an unclean ActualCwd, as a rogue client or a corrupt database could
			// supply: its parent proves fine, but cleaning the raw string gives
			// Cwd itself.
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
			// a relative MountConfig.Mount is whatever the user typed for `wr add
			// --mounts`, so it can name somewhere outside the dir being cleared,
			// and walking up from such a dir never reaches that dir: unless the
			// walk terminates some other way it runs past the filesystem root
			// forever, hanging the runner and the manager alike.
			const cleanupTimeout = 5 * time.Second

			// each case builds its own workspace, since the mounts it is about
			// reach the key the workspace is named for.
			mountingIn := func(mounts MountConfigs) (job *Job, actualCwd, workSpace, mounted, output string) {
				job = &Job{Cwd: cwd, Cmd: testWSCmd, MountConfigs: mounts}
				actualCwd, workSpace, _ = realWorkSpace(job)

				return job, actualCwd, workSpace,
					writeFileIn(filepath.Join(actualCwd, testMountDir), "data.txt"),
					writeFileIn(actualCwd, "out.txt")
			}

			Convey("Cleanup keeps the whole ActualCwd when a Mount of \".\" names it", func() {
				// "." resolves to ActualCwd itself, exactly as "" does, so this is
				// the same case as the mounts-on-its-ActualCwd Convey above: the
				// mount is live when cleanup runs, so deleting ActualCwd's
				// contents would delete through it. Reading MountConfig.Mount as a
				// raw string rather than resolving it makes "." and "" mean
				// opposite things here.
				job, actualCwd, workSpace, mounted, output := mountingIn(
					MountConfigs{{Mount: "."}, {Mount: testMountDir}})

				returned, cleanupErr := triggerWithin(cleanup, job, cleanupTimeout)

				So(returned, ShouldBeTrue)
				soPathsExist(mounted, output, actualCwd, workSpace, cwd, precious)

				So(cleanupErr, ShouldBeNil)
			})

			Convey("Cleanup finishes and still keeps the real mount dir, despite a Mount of ../evil", func() {
				job, actualCwd, workSpace, mounted, output := mountingIn(
					MountConfigs{{Mount: "../evil"}, {Mount: testMountDir}})

				returned, cleanupErr := triggerWithin(cleanup, job, cleanupTimeout)

				// termination and the kept mount are asserted before the error,
				// since a leaf stops at its first failed So and a hang is the
				// failure that matters.
				So(returned, ShouldBeTrue)
				soPathsExist(mounted, actualCwd, workSpace, cwd, precious)
				soPathsGone(output)

				So(cleanupErr, ShouldBeNil)
			})

			Convey("Cleanup finishes and deletes nothing at all, given a Mount of ..", func() {
				// ".." is not an escape at all: it resolves to the WORKSPACE, so
				// the disposable directory this whole sweep is about is itself the
				// live mount, and the job's own output inside it is the user's
				// remote data. Only keptDirs.wholeWorkSpace can say so, since a
				// mount point that is not below the workspace has no path within
				// it and no entry leading to it for the keep set to record.
				job, actualCwd, workSpace, mounted, output := mountingIn(
					MountConfigs{{Mount: ".."}, {Mount: testMountDir}})

				returned, cleanupErr := triggerWithin(cleanup, job, cleanupTimeout)

				So(returned, ShouldBeTrue)
				soPathsExist(mounted, output, actualCwd, workSpace, cwd, precious)

				So(cleanupErr, ShouldBeNil)
			})
		})
	})
}

// testRunMarker is the file a `run` behaviour's command creates in whatever
// directory it is given, so the tests can say where it actually ran.
const testRunMarker = "ran_here"

// runBehaviour is a Run Behaviour whose command drops testRunMarker in its
// working directory.
func runBehaviour() *Behaviour {
	return &Behaviour{When: OnExit, Do: Run, Arg: "touch " + testRunMarker}
}

// TestCleanupKeepsMountsNestedBelowTheWorkingDir pins the mapping between the
// single-component names the working-directory sweep makes its syscalls with and
// the paths relative to that directory the keep set is spelled by. Getting the
// two the wrong way round leaves every keep below the top level unmatched, which
// a test whose mount point is a direct entry of the working directory cannot
// see: there the entry's own name and its path relative to that directory are
// the same string.
func TestCleanupKeepsMountsNestedBelowTheWorkingDir(t *testing.T) {
	if runnermode || servermode {
		return
	}

	Convey("Given a Job with a mount point two dirs below its working directory", t, func() {
		cwd := t.TempDir()

		job := &Job{Cwd: cwd, Cmd: testWSCmd, MountConfigs: MountConfigs{{Mount: "a/b/" + testWSMount}}}
		actualCwd, workSpace, tmpDir := realWorkSpace(job)

		nested := filepath.Join(actualCwd, "a", "b")
		mountPoint := filepath.Join(nested, testWSMount)
		mounted := writeFileIn(mountPoint, "remote.txt")

		sibling := filepath.Join(nested, "sibling")
		siblingFile := writeFileIn(sibling, "out.txt")
		output := writeFileIn(actualCwd, "out.txt")

		Convey("cleanup keeps it, and still deletes an unkept dir beside it", func() {
			err := (&Behaviour{When: OnExit, Do: Cleanup}).Trigger(OnExit, job)

			// survival is asserted before the error, since a leaf stops at its
			// first failed So and the deletion is the evidence that matters.
			soPathsExist(mounted, mountPoint, nested, filepath.Join(actualCwd, "a"),
				actualCwd, workSpace, cwd)
			soPathsGone(siblingFile, sibling, output, tmpDir)

			So(err, ShouldBeNil)
		})
	})
}

func TestCleanupSweptDirSwappedForASymlinkAfterItWasChecked(t *testing.T) {
	if runnermode || servermode {
		return
	}

	Convey("Given a Job whose workspace holds another Job's workspaces beside a dir of its own", t, func() {
		// the sweep asks what an entry IS with an lstat and then opens it to
		// descend, and an os.Root follows a relative symlink that stays inside
		// itself: a directory entry swapped for a link to a sibling in between
		// would move the whole descent into a directory the sweep never
		// device-checked, never classified and never decided it could delete.
		// The sibling here is the base another Job's workspaces sit below, which
		// nestedWorkSpaceBase keeps without ever looking inside, so a redirected
		// descent deletes that Job's live output - the loss this guard exists to
		// prevent.
		cwd := t.TempDir()
		job := &Job{Cwd: cwd, Cmd: testWSCmd}
		actualCwd, workSpace, _ := realWorkSpace(job)

		output := writeFileIn(actualCwd, "out.txt")

		nestedBase := filepath.Join(workSpace, "nested"+createdCwdBaseSuffix)
		nestedCwd := filepath.Join(nestedBase, "child")
		nestedOutput := writeFileIn(nestedCwd, "child.txt")

		stray := filepath.Join(workSpace, "logs")
		strayFile := writeFileIn(stray, "log.txt")

		Reset(func() { sweptDirCheckedHook = nil })

		Convey("cleanup deletes nothing through a swept dir swapped for a symlink to them", func() {
			sweptDirCheckedHook = func(name string) {
				// the sweep descends into the working directory and into every
				// dir below it too, so only the one entry this test made is
				// swapped; anything else would depend on readdir order.
				if name != filepath.Base(stray) {
					return
				}

				sweptDirCheckedHook = nil

				So(os.Rename(stray, stray+".moved"), ShouldBeNil)
				So(os.Symlink(filepath.Base(nestedBase), stray), ShouldBeNil)
			}

			err := (&Behaviour{When: OnExit, Do: Cleanup}).Trigger(OnExit, job)

			// survival is asserted before the error, since a leaf stops at its
			// first failed So and the deletion is the evidence that matters.
			soPathsExist(nestedOutput, nestedCwd, nestedBase, workSpace, cwd,
				filepath.Join(stray+".moved", filepath.Base(strayFile)))

			// what the sweep was licensed to delete it still deleted: the
			// working directory is emptied and removed before the workspace's
			// own entries are swept, so this says nothing about readdir order.
			soPathsGone(output, actualCwd)

			So(err, ShouldNotBeNil)
			So(errors.Is(err, errNotBelowBaseDir), ShouldBeTrue)
		})
	})
}

func TestBehaviourRunDir(t *testing.T) {
	if runnermode || servermode {
		return
	}

	// a `run` behaviour executes an arbitrary shell command, so where it runs
	// decides what that command destroys. The directory therefore has to be
	// answered by the same resolution that licenses a deletion, from the same
	// fields, rather than by reading the raw ActualCwd a runner reported.
	Convey("Given a Job Cwd holding directories of the user's own", t, func() {
		cwd := t.TempDir()
		scripts := filepath.Join(cwd, "scripts")
		So(os.MkdirAll(scripts, os.ModePerm), ShouldBeNil)

		run := runBehaviour()

		ranIn := func(dir string) bool {
			_, err := os.Stat(filepath.Clean(filepath.Join(dir, testRunMarker)))

			return err == nil
		}

		Convey("run executes in the working directory wr really created", func() {
			job := &Job{Cwd: cwd, Cmd: testWSCmd}
			actualCwd, _, _ := realWorkSpace(job)

			So(run.Trigger(OnExit, job), ShouldBeNil)

			So(ranIn(actualCwd), ShouldBeTrue)
			So(ranIn(scripts), ShouldBeFalse)
		})

		Convey("run refuses to fall back to Cwd for a Job whose Cmd never ran there", func() {
			// a Job that is not CwdMatters ran in a working directory wr made for
			// it, and a blank ActualCwd means only that THIS process never learned
			// which one: the manager learns it from a Touch, and a manager with no
			// web port never gets one (liveJTouchEnabled). Falling back to the
			// user's own Cwd would run `--on_exit '{"run":"rm -f *.tmp"}'`
			// somewhere the Job had never been.
			job := &Job{Cwd: cwd, Cmd: testWSCmd}

			err := run.Trigger(OnExit, job)

			So(ranIn(cwd), ShouldBeFalse)
			So(err, ShouldNotBeNil)
			So(errors.Is(err, errNotACreatedCwd), ShouldBeTrue)
		})

		Convey("run executes in Cwd for a CwdMatters Job, whatever its ActualCwd says", func() {
			// wr <= v0.37.1 persisted ActualCwd == Cwd on such a Job, and a runner
			// can report anything; wr creates no directory for one, so the Cmd ran
			// in the user's own Cwd and the behaviour must too. CwdMatters is the
			// one thing that makes Cwd the directory the Cmd really ran in.
			job := &Job{Cwd: cwd, Cmd: testWSCmd, CwdMatters: true, ActualCwd: scripts}

			So(run.Trigger(OnExit, job), ShouldBeNil)

			So(ranIn(cwd), ShouldBeTrue)
			So(ranIn(scripts), ShouldBeFalse)
		})

		Convey("run refuses an ActualCwd wr could not have created", func() {
			// this arrives off the wire: JobEndState.Cwd -> applyLiveSnapshot ->
			// TriggerBehaviours in the MANAGER, where accepting it would execute
			// the user's command in a directory of their own.
			job := &Job{Cwd: cwd, Cmd: testWSCmd}
			applyLiveSnapshot(job, &JobEndState{Cwd: scripts})

			err := run.Trigger(OnExit, job)

			So(ranIn(scripts), ShouldBeFalse)
			So(err, ShouldNotBeNil)
			So(errors.Is(err, errNotACreatedCwd), ShouldBeTrue)
		})

		Convey("run refuses an ActualCwd that leaves Cwd through a symlink", func() {
			// the identity check is lexical, so a path that IS the one wr would
			// build for this Job passes it even when a component of it is a
			// symlink; only proving every component is a real dir keeps the
			// command inside the Job's own Cwd. The link stands where the
			// workspace base does, so the path is otherwise exactly right.
			link := "escape" + createdCwdBaseSuffix
			outside := t.TempDir()
			job := &Job{Cwd: cwd, Cmd: testWSCmd}

			escaped := filepath.Join(testWorkSpaceUnder(outside, job, "0"), createdCwdName)
			So(os.MkdirAll(escaped, os.ModePerm), ShouldBeNil)
			So(os.Symlink(outside, filepath.Join(cwd, link)), ShouldBeNil)

			viaLink := testWorkSpaceUnder(filepath.Join(cwd, link), job, "0")
			job.ActualCwd = filepath.Join(viaLink, createdCwdName)

			err := run.Trigger(OnExit, job)

			So(ranIn(escaped), ShouldBeFalse)
			So(err, ShouldNotBeNil)
			So(errors.Is(err, errNotBelowBaseDir), ShouldBeTrue)
		})

		Convey("run executes in the directory that was proven, not in what the name means later", func() {
			// exec.Cmd takes a NAME and the child resolves it once more when the
			// command starts, so everything proved about the path is proved about
			// a string that gets looked up again. The hook puts the test in
			// exactly that moment, which holding the directory open closes.
			job := &Job{Cwd: cwd, Cmd: testWSCmd}
			actualCwd, _, _ := realWorkSpace(job)

			moved := filepath.Join(cwd, "moved")
			elsewhere := filepath.Join(cwd, "elsewhere")
			So(os.MkdirAll(elsewhere, os.ModePerm), ShouldBeNil)

			runProvenHook = func() {
				runProvenHook = nil

				So(os.Rename(actualCwd, moved), ShouldBeNil)
				So(os.Symlink(elsewhere, actualCwd), ShouldBeNil)
			}

			Reset(func() { runProvenHook = nil })

			So(run.Trigger(OnExit, job), ShouldBeNil)

			if runtime.GOOS != osLinux {
				// exec can only be given the handle where /proc names it, so
				// everywhere else the name is resolved again and this window is
				// the residual recorded on runDir.
				return
			}

			So(ranIn(elsewhere), ShouldBeFalse)
			So(ranIn(moved), ShouldBeTrue)
		})

		Convey("run refuses a working directory swapped for another after the proof", func() {
			// the proof is about a path and the open resolves that path again.
			// The link is RELATIVE and stays inside Cwd, because an os.Root
			// refuses an absolute one outright and follows this one - so what
			// catches it is proving that what was opened is the directory that
			// was checked.
			job := &Job{Cwd: cwd, Cmd: testWSCmd}
			actualCwd, _, _ := realWorkSpace(job)

			elsewhere := filepath.Join(cwd, "elsewhere")
			So(os.MkdirAll(elsewhere, os.ModePerm), ShouldBeNil)

			link, err := filepath.Rel(filepath.Dir(actualCwd), elsewhere)
			So(err, ShouldBeNil)

			runResolvedHook = func() {
				runResolvedHook = nil

				So(os.Rename(actualCwd, filepath.Join(cwd, "moved")), ShouldBeNil)
				So(os.Symlink(link, actualCwd), ShouldBeNil)
			}

			Reset(func() { runResolvedHook = nil })

			err = run.Trigger(OnExit, job)

			So(ranIn(elsewhere), ShouldBeFalse)
			So(err, ShouldNotBeNil)
			So(errors.Is(err, errNotBelowBaseDir), ShouldBeTrue)
		})

		Convey("run leaves no descriptor behind for the directory it held open", func() {
			// the handle has to outlive the resolution, since the command being
			// started is what uses it, so it is the behaviour that closes it: a
			// run behaviour that leaked one would exhaust a long-lived manager.
			if runtime.GOOS != osLinux {
				return
			}

			job := &Job{Cwd: cwd, Cmd: testWSCmd}
			realWorkSpace(job)

			So(run.Trigger(OnExit, job), ShouldBeNil)

			before := openDescriptors()

			for range 5 {
				So(run.Trigger(OnExit, job), ShouldBeNil)
			}

			So(openDescriptors(), ShouldBeLessThanOrEqualTo, before+1)
		})

		Convey("run refuses a working directory that is not there", func() {
			// cleanup TOLERATES this absence, and has to: the Job's own Cmd or a
			// previous cleanup may have deleted the directory, and cleanup runs
			// twice for a lost job. For `run` there is nothing to tolerate - a
			// command cannot execute in a directory that is not there - and handing
			// the name back anyway leaves exec.Cmd to resolve it a second time,
			// when whatever creates it in the meantime chooses where the user's
			// command runs. The two consumers share one resolution, so the
			// difference is stated rather than inherited.
			job := &Job{Cwd: cwd, Cmd: testWSCmd}
			actualCwd, _, _ := realWorkSpace(job)
			So(os.RemoveAll(actualCwd), ShouldBeNil)

			err := run.Trigger(OnExit, job)

			soPathsGone(actualCwd)
			So(err, ShouldNotBeNil)
			So(errors.Is(err, errNotBelowBaseDir), ShouldBeTrue)

			// and the refusal is the resolution's own, made because it proved the
			// directory absent, rather than an open of it failing further down: a
			// rule that only holds because the next syscall happens to fail is a
			// rule nobody knows is there.
			So(errors.Is(err, os.ErrNotExist), ShouldBeFalse)
		})

		Convey("run refuses when the Job's Cwd has itself gone", func() {
			// an empty cmd.Dir is not "nowhere", it is the directory of whatever
			// process is running the behaviour - the manager, for a lost job.
			gone := filepath.Join(cwd, "gone")
			So(os.MkdirAll(gone, os.ModePerm), ShouldBeNil)

			job := &Job{Cwd: gone, Cmd: testWSCmd}
			realWorkSpace(job)

			So(os.RemoveAll(gone), ShouldBeNil)

			err := run.Trigger(OnExit, job)

			So(err, ShouldNotBeNil)
			So(errors.Is(err, errNotBelowBaseDir), ShouldBeTrue)
		})
	})

	Convey("Given a process working directory a relative path would resolve against", t, func() {
		// cleanup and behaviours run in TWO processes with different working
		// directories: the runner, and the manager when it declares a job lost.
		// exec.Cmd resolves a relative Dir against the process running it, so a
		// relative path reported over the wire aims the user's command at whatever
		// sits beside the MANAGER.
		base := t.TempDir()
		beside := filepath.Join(base, "beside")
		So(os.MkdirAll(beside, os.ModePerm), ShouldBeNil)

		oldWd, err := os.Getwd()
		So(err, ShouldBeNil)

		So(os.Chdir(base), ShouldBeNil)

		Reset(func() { So(os.Chdir(oldWd), ShouldBeNil) })

		cwd := filepath.Join(base, "jobcwd")
		So(os.MkdirAll(cwd, os.ModePerm), ShouldBeNil)

		run := runBehaviour()

		Convey("run refuses a relative ActualCwd", func() {
			job := &Job{Cwd: cwd, Cmd: testWSCmd, ActualCwd: "beside"}

			err = run.Trigger(OnExit, job)

			soPathsGone(filepath.Join(beside, testRunMarker))
			So(err, ShouldNotBeNil)
			So(errors.Is(err, errNotBelowBaseDir), ShouldBeTrue)
		})

		Convey("run refuses a relative Cwd", func() {
			// CwdMatters, because Cwd is only ever the directory a `run` runs in
			// for such a Job: it is the one path onto absJobDir that a Job with
			// no working directory of its own still reaches.
			job := &Job{Cwd: "beside", Cmd: testWSCmd, CwdMatters: true}

			err = run.Trigger(OnExit, job)

			soPathsGone(filepath.Join(beside, testRunMarker))
			So(err, ShouldNotBeNil)
			So(errors.Is(err, errNotBelowBaseDir), ShouldBeTrue)
		})
	})
}

// openDescriptors is how many file descriptors this process has open, on a
// system that says. It is compared against itself, so what it counts besides
// the ones a test is about does not matter.
func openDescriptors() int {
	entries, err := os.ReadDir("/proc/self/fd")
	So(err, ShouldBeNil)

	return len(entries)
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
