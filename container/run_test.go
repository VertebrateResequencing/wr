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

package container

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"

	"github.com/VertebrateResequencing/wr/clog"
	"github.com/VertebrateResequencing/wr/fs/file"
	. "github.com/smartystreets/goconvey/convey"
)

const dirMode os.FileMode = 0755

// rootUID and rootGID are the uid and gid of the user alpine (like most images)
// specifies, which is who a container's processes run as when DockerRunCmd is
// told to leave --user off.
const (
	rootUID = 0
	rootGID = 0
)

// ownershipCmd creates a file and a directory in the container's working
// directory, so that a test can see who ends up owning what a containerised
// command makes in the caller's own Cwd.
const ownershipCmd = "touch created.file && mkdir created.dir"

// workDirCmd reports the container's working directory and lists what the
// caller's own Cwd holds, so a test can see if the containerised command
// really runs in that Cwd.
const workDirCmd = "pwd && ls *.file"

// singularityImage is the image the real singularity tests run, in the
// docker:// form that makes singularity convert a docker image to a sif.
const singularityImage = "docker://alpine"

// expectedSingularityWorkDirArgs is the bind of the working directory, and the
// choice of it as the container's cwd, that SingularityRunCmd always emits.
const expectedSingularityWorkDirArgs = ` -B "$PWD" --pwd "$PWD"`

// staleContainerSleepSecs is how long the container standing in for one that
// outlived its `docker run` client sleeps for. It only has to outlast the test
// that removes it.
const staleContainerSleepSecs = 300

// dockerShortIDLen is how many characters of a container id `docker ps` shows
// when not asked for the full one, which is what our removal message names.
const dockerShortIDLen = 12

// expectedStaleRemoval is the removal of a stale container named "uniqueID"
// that DockerRunCmd always emits before its `docker run`, so that a container
// a lost run left behind under that name cannot make this run fail before it
// starts.
const expectedStaleRemoval = `for c in $(docker ps --all --quiet --filter name=^uniqueID\$ ` +
	`--filter label=uk.ac.sanger.wr.job-key=uniqueID); do ` +
	`echo "wr: removing container $c, left behind by a lost run of this same command" >&2; ` +
	`docker rm --force "$c" >/dev/null; done; `

// mountNoColon is a mount spec with no ":/inside/container/path" part, so its
// path is used on both sides of the mount. Several of the command-line tests
// mount it alongside one that does name an inside path.
const mountNoColon = "/foo/car"

// realTestDirNames are the base names of the directories realTestSetup
// creates: the one that becomes the working directory, and the 2 that get
// mounted inside the container.
type realTestDirNames struct {
	home   string
	mountA string
	mountB string
}

func plainTestDirNames() realTestDirNames {
	return realTestDirNames{home: "home", mountA: "mntA", mountB: "mntB"}
}

// awkwardTestDirNames contain a space and shell metacharacters, which must
// survive being interpolated in to a command line that a shell then executes.
func awkwardTestDirNames() realTestDirNames {
	return realTestDirNames{home: "home dir", mountA: "mnt A", mountB: "mnt B;rm -rf x&*'q'"}
}

func TestRunRealAwkwardPaths(t *testing.T) {
	containerCmd := "pwd && ls *.file && ls /mntA && ls /mntB"

	Convey("DockerRunCmd's command really works when the paths contain spaces and metacharacters", t, func() {
		cmdFile, homeDir, mounts, cleanup, err := realTestSetup(t, "docker", containerCmd, awkwardTestDirNames())
		if err != nil {
			SkipConvey(fmt.Sprintf("Can't really test the docker command line: %s", err), nil)

			return
		}

		defer cleanup()

		uniqueDir := filepath.Dir(homeDir)
		cmd := DockerRunCmd("alpine", cmdFile, filepath.Base(uniqueDir), mounts, nil, false)

		actual, err := realTestTryCmd(cmd, homeDir)
		So(err, ShouldBeNil)

		// the space-containing working directory really is the container's
		// cwd, and the space and metacharacter containing directories really
		// are mounted where they were asked for.
		So(actual, ShouldContainSubstring, homeDir+"\n")
		So(actual, ShouldContainSubstring, "home.file\na.file\nb.file\n")
	})

	Convey("SingularityRunCmd's command really works when the paths contain spaces and metacharacters", t, func() {
		cmdFile, homeDir, mounts, cleanup, err := realTestSetup(t, "singularity", containerCmd, awkwardTestDirNames())
		if err != nil {
			SkipConvey(fmt.Sprintf("Can't really test the singularity command line: %s", err), nil)

			return
		}

		defer cleanup()

		cmd := SingularityRunCmd(singularityImage, cmdFile, mounts)

		actual, err := realTestTryCmd(cmd, homeDir)
		So(err, ShouldBeNil)
		So(actual, ShouldEqual, homeDir+"\nhome.file\na.file\nb.file\n")
	})
}

// realTestTryCmdStd is realTestTryCmd, also returning STDERR, which is where
// docker puts its reason for refusing to run and where our own command line
// announces anything it destroyed.
func realTestTryCmdStd(cmdLine, homeDir string) (stdout, stderr string, err error) {
	cmdLine = "set -o pipefail; " + cmdLine
	cmd := exec.CommandContext(context.Background(), "/bin/bash", "-c", cmdLine)
	cmd.Dir = homeDir
	cmd.Env = os.Environ()

	var outBuf, errBuf bytes.Buffer

	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf

	err = cmd.Run()

	return outBuf.String(), errBuf.String(), err
}

func TestRunRealFileOwnership(t *testing.T) {
	Convey("What DockerRunCmd's command creates in the working dir really belongs to the calling user", t, func() {
		cmdFile, homeDir, _, cleanup, err := realTestSetup(t, "docker", ownershipCmd, plainTestDirNames())
		if err != nil {
			SkipConvey(fmt.Sprintf("Can't really test docker file ownership: %s", err), nil)

			return
		}

		defer cleanup()

		So(realOwnershipTestRun(t, cmdFile, homeDir, false), ShouldBeNil)
		soOwnedBy(t, homeDir, os.Getuid(), os.Getgid())
	})

	Convey("With imageUser it really belongs to the user the image specifies instead", t, func() {
		cmdFile, homeDir, _, cleanup, err := realTestSetup(t, "docker", ownershipCmd, plainTestDirNames())
		if err != nil {
			SkipConvey(fmt.Sprintf("Can't really test docker file ownership: %s", err), nil)

			return
		}

		defer cleanup()

		So(realOwnershipTestRun(t, cmdFile, homeDir, true), ShouldBeNil)
		soOwnedBy(t, homeDir, rootUID, rootGID)
	})
}

// realOwnershipTestRun really runs the ownershipCmd already written to cmdFile
// in a docker container built by DockerRunCmd with the given imageUser, using
// homeDir as the working directory, and returns the error of that run.
//
// A `docker run` that fails is a failure of the thing under test - docker
// rejecting --user, or the command line quoting wrongly - so the caller asserts
// this is nil, exactly as its TestRunReal siblings do. Only realTestSetup's
// error means docker was unavailable and the caller should skip.
func realOwnershipTestRun(t *testing.T, cmdFile, homeDir string, imageUser bool) error {
	t.Helper()

	uniqueDir := filepath.Dir(homeDir)
	cmd := DockerRunCmd("alpine", cmdFile, filepath.Base(uniqueDir), nil, nil, imageUser)
	t.Logf("cmdline: %s", cmd)

	out, err := realTestTryCmd(cmd, homeDir)
	t.Logf("output: %q", out)

	return err
}

// soOwnedBy asserts that each of the things ownershipCmd created in dir is owned
// by the given uid and gid. Ownership comes from os.Stat, not from parsing `ls`.
func soOwnedBy(t *testing.T, dir string, uid, gid int) {
	t.Helper()

	for _, base := range []string{"created.file", "created.dir"} {
		info, err := os.Stat(filepath.Join(dir, base))
		So(err, ShouldBeNil)

		stat, ok := info.Sys().(*syscall.Stat_t)
		So(ok, ShouldBeTrue)

		t.Logf("%s uid=%d gid=%d; calling user uid=%d gid=%d",
			base, stat.Uid, stat.Gid, os.Getuid(), os.Getgid())

		So(int(stat.Uid), ShouldEqual, uid)
		So(int(stat.Gid), ShouldEqual, gid)
	}
}

func TestRunRealDockerStaleContainer(t *testing.T) {
	Convey("DockerRunCmd's command really runs when a lost run of the same job left a container "+
		"holding its name", t, func() {
		cmdFile, homeDir, _, cleanup, err := realTestSetup(t, "docker", workDirCmd, plainTestDirNames())
		if err != nil {
			SkipConvey(fmt.Sprintf("Can't really test the docker stale container: %s", err), nil)

			return
		}

		defer cleanup()

		name := realTestContainerName(homeDir)
		defer removeTestContainer(t, name)

		// this stands in for the container of a run whose `docker run` client
		// was SIGKILLed: still running, still holding the name, and still
		// carrying the label wr gave it.
		id, err := startTestContainer(t, name, JobKeyLabel+"="+name)
		So(err, ShouldBeNil)
		So(testContainerExists(t, name), ShouldBeTrue)

		cmd := DockerRunCmd("alpine", cmdFile, name, nil, nil, false)
		t.Logf("cmdline: %s", cmd)

		stdout, stderr, err := realTestTryCmdStd(cmd, homeDir)

		// the working directory workDirCmd reports names the container that
		// really ran, and STDERR carries both docker's reason for refusing to
		// run and our own account of what we removed.
		t.Logf("stdout: %q, stderr: %q, err: %v", stdout, stderr, err)

		So(err, ShouldBeNil)
		So(stdout, ShouldEqual, homeDir+"\nhome.file\n")
		So(stderr, ShouldEqual, "wr: removing container "+id[:dockerShortIDLen]+
			", left behind by a lost run of this same command\n")
	})

	Convey("But it never removes a container of that name that is not this job's", t, func() {
		cmdFile, homeDir, _, cleanup, err := realTestSetup(t, "docker", workDirCmd, plainTestDirNames())
		if err != nil {
			SkipConvey(fmt.Sprintf("Can't really test the docker stale container: %s", err), nil)

			return
		}

		defer cleanup()

		name := realTestContainerName(homeDir)
		defer removeTestContainer(t, name)

		// a co-tenant's container that happens to have our name carries no
		// claim from us, so it is not ours to destroy, even though it is in
		// our way.
		_, err = startTestContainer(t, name, "")
		So(err, ShouldBeNil)

		cmd := DockerRunCmd("alpine", cmdFile, name, nil, nil, false)

		stdout, stderr, err := realTestTryCmdStd(cmd, homeDir)
		t.Logf("stdout: %q, stderr: %q, err: %v", stdout, stderr, err)

		So(err, ShouldNotBeNil)
		So(stderr, ShouldNotContainSubstring, "wr: removing container")
		So(stderr, ShouldContainSubstring, "is already in use by container")
		So(testContainerExists(t, name), ShouldBeTrue)
	})
}

// realTestContainerName is a container name unique to this test run, derived
// from the temporary directory realTestSetup made for it, so that these tests
// only ever create and destroy containers of their own.
func realTestContainerName(homeDir string) string {
	return filepath.Base(filepath.Dir(homeDir))
}

// removeTestContainer force-removes the container this test run created with
// the given name, if it is still there. The name is unique to the test run, so
// nothing else can be removed by it.
func removeTestContainer(t *testing.T, name string) {
	t.Helper()

	out, err := exec.CommandContext(context.Background(), "docker", "rm", "--force", name).CombinedOutput()
	if err != nil {
		t.Logf("docker rm --force %s failed: %s [%s]", name, err, out)
	}
}

// startTestContainer starts a detached container with the given name, and the
// given label if it is not blank, that sleeps until something removes it. It
// stands in for a container that outlived the `docker run` client that created
// it, and its id is returned.
func startTestContainer(t *testing.T, name, label string) (string, error) {
	t.Helper()

	args := []string{"run", "--detach", "--name", name}
	if label != "" {
		args = append(args, "--label", label)
	}

	args = append(args, "alpine", "sleep", strconv.Itoa(staleContainerSleepSecs))

	cmd := exec.CommandContext(context.Background(), "docker", args...)

	var errBuf bytes.Buffer

	// the id is read from STDOUT alone: docker announces any pull of a missing
	// image on STDERR, so merging the two would make the "id" that pull's
	// progress on a machine that has not cached the image.
	cmd.Stderr = &errBuf

	out, err := cmd.Output()
	t.Logf("docker %v: %s [stderr: %s]", args, out, errBuf.String())

	return strings.TrimSpace(string(out)), err
}

// testContainerExists says if a container of the given name exists, running or
// not.
func testContainerExists(t *testing.T, name string) bool {
	t.Helper()

	nameFilter := "name=^" + name + "$"

	out, err := exec.CommandContext(context.Background(), "docker", "ps", "--all", "--quiet",
		"--filter", nameFilter).Output()
	if err != nil {
		t.Logf("docker ps failed: %s", err)

		return false
	}

	return len(strings.TrimSpace(string(out))) > 0
}

func TestRunRealSingularityWorkDir(t *testing.T) {
	Convey("SingularityRunCmd's command really runs in the working directory, "+
		"even at a site that binds nothing useful", t, func() {
		cmdFile, homeDir, _, cleanup, err := realTestSetup(t, "singularity", workDirCmd, plainTestDirNames())
		if err != nil {
			SkipConvey(fmt.Sprintf("Can't really test the singularity working directory: %s", err), nil)

			return
		}

		defer cleanup()

		// singularity reads this the way it reads its --no-mount option, so it
		// stands in for a singularity.conf that binds neither the user's home
		// directory, nor the current working directory, nor /tmp.
		t.Setenv("SINGULARITY_NO_MOUNT", "cwd,home,tmp")

		cmd := SingularityRunCmd(singularityImage, cmdFile, nil)
		t.Logf("cmdline: %s", cmd)

		actual, err := realTestTryCmd(cmd, homeDir)

		// workDirCmd's `pwd` names the directory the container actually
		// started in, so log it before asserting: if the bind or the cwd is
		// missing, `ls` fails and the error alone says only "exit status 1".
		t.Logf("output: %q", actual)

		So(err, ShouldBeNil)
		So(actual, ShouldEqual, homeDir+"\nhome.file\n")
	})
}

func TestRunPrepare(t *testing.T) {
	ctx := context.Background()

	Convey("You can prepare a temporary command file", t, func() {
		envCmd := "export FOO=bar; echo $FOO && echo $FOO"
		path, cleanup, err := PrepareCmdFile(ctx, envCmd)
		So(err, ShouldBeNil)
		So(path, ShouldNotBeBlank)

		So(cleanup, ShouldNotBeNil)
		defer cleanup()

		So(fileExists(path), ShouldBeTrue)

		content, err := file.ToString(path)
		So(err, ShouldBeNil)
		So(content, ShouldEqual, envCmd+"\n")

		Convey("After calling the cleanup method, the command file is deleted", func() {
			buff := clog.ToBufferAtLevel("debug")

			defer clog.ToDefault()

			cleanup()
			So(fileDoesNotExist(path), ShouldBeTrue)
			So(buff.String(), ShouldBeBlank)

			cleanup()
			So(buff.String(), ShouldContainSubstring, "lvl=warn msg=\"container command file could not be deleted\"")
		})
	})

	Convey("Issues with the tmp dir will prevent command file creation", t, func() {
		tmpdir := os.Getenv("TMPDIR")

		os.Setenv("TMPDIR", "/asdf")
		defer os.Setenv("TMPDIR", tmpdir)

		_, _, err := PrepareCmdFile(ctx, "foo")
		So(err, ShouldNotBeNil)
	})

	Convey("Write issues during PrepareCmdFile() would be detected and delete the file", t, func() {
		f, cleanup, err := createTmpFileAndCleanupMethod(ctx)
		So(err, ShouldBeNil)

		So(fileExists(f.Name()), ShouldBeTrue)

		f.Close()

		err = writeStringToFile(f, "foo", cleanup)
		So(err, ShouldNotBeNil)
		So(fileDoesNotExist(f.Name()), ShouldBeTrue)
	})
}

func TestRunDocker(t *testing.T) {
	Convey("DockerRunCmd formulates the correct command line", t, func() {
		cmd := DockerRunCmd("myimage", "/path/to/cmds", "uniqueID", nil, nil, false)

		So(cmd, ShouldEqual, expectedStaleRemoval+"cat /path/to/cmds | docker run --rm --name uniqueID"+
			" --label uk.ac.sanger.wr.job-key=uniqueID"+
			` --user "$(id -u):$(id -g)"`+
			` -w "$PWD" --mount type=bind,source="$PWD",target="$PWD" -i myimage /bin/sh`)

		cmd = DockerRunCmd("myimage", "/path/to/cmds", "uniqueID",
			[]string{"/foo/bar:/bar", mountNoColon}, []string{"A", "B"}, false)

		So(cmd, ShouldEqual, expectedStaleRemoval+"cat /path/to/cmds | docker run --rm --name uniqueID"+
			" --label uk.ac.sanger.wr.job-key=uniqueID"+
			` --user "$(id -u):$(id -g)"`+
			` -w "$PWD" --mount type=bind,source="$PWD",target="$PWD"`+
			" --mount type=bind,source=/foo/bar,target=/bar --mount type=bind,source=/foo/car,target=/foo/car"+
			" -e A -e B -i myimage /bin/sh")
	})

	Convey("DockerRunCmd leaves --user off when told to run as the image's user", t, func() {
		cmd := DockerRunCmd("myimage", "/path/to/cmds", "uniqueID", nil, nil, true)

		So(cmd, ShouldEqual, expectedStaleRemoval+"cat /path/to/cmds | docker run --rm --name uniqueID"+
			" --label uk.ac.sanger.wr.job-key=uniqueID"+
			` -w "$PWD" --mount type=bind,source="$PWD",target="$PWD" -i myimage /bin/sh`)
	})

	Convey("DockerRunCmd escapes regex metacharacters in the name it filters stale containers by", t, func() {
		// docker matches the name filter as a regular expression, so an
		// unescaped "." here would have this job remove a container named
		// "wrrevXG", which is somebody else's.
		cmd := DockerRunCmd("myimage", "/path/to/cmds", "wrrev.G", nil, nil, true)

		So(cmd, ShouldEqual, `for c in $(docker ps --all --quiet --filter name=^wrrev\\.G\$ `+
			`--filter label=uk.ac.sanger.wr.job-key=wrrev.G); do `+
			`echo "wr: removing container $c, left behind by a lost run of this same command" >&2; `+
			`docker rm --force "$c" >/dev/null; done; `+
			"cat /path/to/cmds | docker run --rm --name wrrev.G"+
			" --label uk.ac.sanger.wr.job-key=wrrev.G"+
			` -w "$PWD" --mount type=bind,source="$PWD",target="$PWD" -i myimage /bin/sh`)
	})

	Convey("DockerRunCmd quotes values containing spaces and shell metacharacters", t, func() {
		cmd := DockerRunCmd("my image", "/path to/cmds", "unique ID",
			[]string{"/foo/b ar;rm -rf x:/b ar", mountNoColon}, []string{"A B"}, false)

		So(cmd, ShouldEqual, `for c in $(docker ps --all --quiet --filter 'name=^unique ID$' `+
			`--filter 'label=uk.ac.sanger.wr.job-key=unique ID'); do `+
			`echo "wr: removing container $c, left behind by a lost run of this same command" >&2; `+
			`docker rm --force "$c" >/dev/null; done; `+
			"cat '/path to/cmds' | docker run --rm --name 'unique ID'"+
			" --label 'uk.ac.sanger.wr.job-key=unique ID'"+
			` --user "$(id -u):$(id -g)"`+
			` -w "$PWD" --mount type=bind,source="$PWD",target="$PWD"`+
			" --mount 'type=bind,source=/foo/b ar;rm -rf x,target=/b ar'"+
			" --mount type=bind,source=/foo/car,target=/foo/car"+
			" -e 'A B' -i 'my image' /bin/sh")
	})
}

func TestRunSingularity(t *testing.T) {
	Convey("SingularityRunCmd formulates the correct command line", t, func() {
		cmd := SingularityRunCmd("myimage", "/path/to/cmds", nil)

		So(cmd, ShouldEqual, "cat /path/to/cmds | singularity shell"+
			expectedSingularityWorkDirArgs+" myimage")

		cmd = SingularityRunCmd("myimage", "/path/to/cmds", []string{"/foo/bar:/bar", mountNoColon})

		So(cmd, ShouldEqual, "cat /path/to/cmds | singularity shell"+
			expectedSingularityWorkDirArgs+" -B /foo/bar:/bar -B /foo/car myimage")
	})

	Convey("SingularityRunCmd quotes values containing spaces and shell metacharacters", t, func() {
		cmd := SingularityRunCmd("my image", "/path to/cmds", []string{"/foo/b ar;rm -rf x:/b ar", mountNoColon})

		So(cmd, ShouldEqual, "cat '/path to/cmds' | singularity shell"+
			expectedSingularityWorkDirArgs+
			" -B '/foo/b ar;rm -rf x:/b ar' -B /foo/car 'my image'")
	})
}

func TestRunReal(t *testing.T) {
	t.Setenv("FOO", "bar")
	t.Setenv("OOF", "rab")

	containerCmd := "export FOO=car; echo $FOO && echo $OOF && ls *.file && ls /mntA && ls /mntB"
	expected := "car\nrab\nhome.file\na.file\nb.file\n"

	Convey("DockerRunCmd's command really works", t, func() {
		cmdFile, homeDir, mounts, cleanup, err := realTestSetup(t, "docker", containerCmd, plainTestDirNames())
		if err != nil {
			SkipConvey(fmt.Sprintf("Can't really test the docker command line: %s", err), nil)

			return
		}

		defer cleanup()

		uniqueDir := filepath.Dir(homeDir)
		cmd := DockerRunCmd("alpine", cmdFile, filepath.Base(uniqueDir), mounts, []string{"FOO", "OOF"}, false)

		actual, err := realTestTryCmd(cmd, homeDir)
		So(err, ShouldBeNil)
		So(actual, ShouldContainSubstring, expected)
	})

	Convey("SingularityRunCmd's command really works", t, func() {
		cmdFile, homeDir, mounts, cleanup, err := realTestSetup(t, "singularity", containerCmd, plainTestDirNames())
		if err != nil {
			SkipConvey(fmt.Sprintf("Can't really test the singularity command line: %s", err), nil)

			return
		}

		defer cleanup()

		cmd := SingularityRunCmd(singularityImage, cmdFile, mounts)

		actual, err := realTestTryCmd(cmd, homeDir)
		So(err, ShouldBeNil)
		So(actual, ShouldEqual, expected)
	})
}

func fileExists(path string) bool {
	_, err := os.Stat(path)

	return err == nil
}

func fileDoesNotExist(path string) bool {
	_, err := os.Stat(path)

	return os.IsNotExist(err)
}

func realTestSetup(t *testing.T, exe, containerCmd string, names realTestDirNames) (cmdFile, homeDir string,
	mounts []string, cleanup func(), err error) {
	t.Helper()

	if _, err = exec.LookPath(exe); err != nil {
		return cmdFile, homeDir,
			mounts, cleanup, err
	}

	rootDir, homeDir, mountADir, mountBDir, err := createRealTestDirs(names)
	if err != nil {
		return cmdFile, homeDir,
			mounts, cleanup, err
	}

	if err = createRealTestFiles(homeDir, mountADir, mountBDir); err != nil {
		removeTestRootDir(t, rootDir)

		return cmdFile, homeDir,
			mounts, cleanup, err
	}

	cmdFile, cmdFileCleanup, err := PrepareCmdFile(context.Background(), containerCmd)
	if err != nil {
		removeTestRootDir(t, rootDir)

		return cmdFile, homeDir,
			mounts, cleanup, err
	}

	cleanup = func() {
		cmdFileCleanup()
		removeTestRootDir(t, rootDir)
	}

	mounts = []string{mountADir + ":/mntA", mountBDir + ":/mntB"}

	return cmdFile, homeDir, mounts, cleanup, err
}

func removeTestRootDir(t *testing.T, dir string) {
	t.Helper()

	if err := os.RemoveAll(dir); err != nil {
		t.Logf("RemoveAll failed: %s", err)
	}
}

func createRealTestDirs(names realTestDirNames) (root, home, mountA, mountB string, err error) {
	root, err = os.MkdirTemp("", "container_run_test")
	if err != nil {
		return root, home, mountA, mountB, err
	}

	home = filepath.Join(root, names.home)

	if err = os.Mkdir(home, dirMode); err != nil {
		return root, home, mountA, mountB, err
	}

	mountA = filepath.Join(root, names.mountA)

	if err = os.Mkdir(mountA, dirMode); err != nil {
		return root, home, mountA, mountB, err
	}

	mountB = filepath.Join(root, names.mountB)
	err = os.Mkdir(mountB, dirMode)

	return root, home, mountA, mountB, err
}

func createRealTestFiles(homeDir, mountADir, mountBDir string) error {
	if err := createRealTestFile(homeDir, "home.file"); err != nil {
		return err
	}

	if err := createRealTestFile(mountADir, "a.file"); err != nil {
		return err
	}

	if err := createRealTestFile(mountBDir, "b.file"); err != nil {
		return err
	}

	return nil
}

func createRealTestFile(dir, baseName string) error {
	f, err := os.Create(filepath.Join(dir, baseName))
	if err != nil {
		return err
	}

	return f.Close()
}

func realTestTryCmd(cmdLine, homeDir string) (string, error) {
	stdout, _, err := realTestTryCmdStd(cmdLine, homeDir)

	return stdout, err
}
