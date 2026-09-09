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
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/VertebrateResequencing/wr/clog"
	"github.com/VertebrateResequencing/wr/fs/file"
	. "github.com/smartystreets/goconvey/convey"
)

const dirMode os.FileMode = 0755

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
		cmd := DockerRunCmd("alpine", cmdFile, filepath.Base(uniqueDir), mounts, nil)

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

		cmd := SingularityRunCmd("docker://alpine", cmdFile, mounts)

		actual, err := realTestTryCmd(cmd, homeDir)
		So(err, ShouldBeNil)
		So(actual, ShouldEqual, homeDir+"\nhome.file\na.file\nb.file\n")
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
		cmd := DockerRunCmd("myimage", "/path/to/cmds", "uniqueID", nil, nil)

		So(cmd, ShouldEqual, "cat /path/to/cmds | docker run --rm --name uniqueID"+
			" --label uk.ac.sanger.wr.job-key=uniqueID"+
			` -w "$PWD" --mount type=bind,source="$PWD",target="$PWD" -i myimage /bin/sh`)

		cmd = DockerRunCmd("myimage", "/path/to/cmds", "uniqueID",
			[]string{"/foo/bar:/bar", "/foo/car"}, []string{"A", "B"})

		So(cmd, ShouldEqual, "cat /path/to/cmds | docker run --rm --name uniqueID"+
			" --label uk.ac.sanger.wr.job-key=uniqueID"+
			` -w "$PWD" --mount type=bind,source="$PWD",target="$PWD"`+
			" --mount type=bind,source=/foo/bar,target=/bar --mount type=bind,source=/foo/car,target=/foo/car"+
			" -e A -e B -i myimage /bin/sh")
	})

	Convey("DockerRunCmd quotes values containing spaces and shell metacharacters", t, func() {
		cmd := DockerRunCmd("my image", "/path to/cmds", "unique ID",
			[]string{"/foo/b ar;rm -rf x:/b ar", "/foo/car"}, []string{"A B"})

		So(cmd, ShouldEqual, "cat '/path to/cmds' | docker run --rm --name 'unique ID'"+
			" --label 'uk.ac.sanger.wr.job-key=unique ID'"+
			` -w "$PWD" --mount type=bind,source="$PWD",target="$PWD"`+
			" --mount 'type=bind,source=/foo/b ar;rm -rf x,target=/b ar'"+
			" --mount type=bind,source=/foo/car,target=/foo/car"+
			" -e 'A B' -i 'my image' /bin/sh")
	})
}

func TestRunSingularity(t *testing.T) {
	Convey("SingularityRunCmd formulates the correct command line", t, func() {
		cmd := SingularityRunCmd("myimage", "/path/to/cmds", nil)

		So(cmd, ShouldEqual, "cat /path/to/cmds | singularity shell myimage")

		cmd = SingularityRunCmd("myimage", "/path/to/cmds", []string{"/foo/bar:/bar", "/foo/car"})

		So(cmd, ShouldEqual, "cat /path/to/cmds | singularity shell -B /foo/bar:/bar -B /foo/car myimage")
	})

	Convey("SingularityRunCmd quotes values containing spaces and shell metacharacters", t, func() {
		cmd := SingularityRunCmd("my image", "/path to/cmds", []string{"/foo/b ar;rm -rf x:/b ar", "/foo/car"})

		So(cmd, ShouldEqual, "cat '/path to/cmds' | singularity shell"+
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
		cmd := DockerRunCmd("alpine", cmdFile, filepath.Base(uniqueDir), mounts, []string{"FOO", "OOF"})

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

		cmd := SingularityRunCmd("docker://alpine", cmdFile, mounts)

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
	cmdLine = "set -o pipefail; " + cmdLine
	cmd := exec.CommandContext(context.Background(), "/bin/bash", "-c", cmdLine)
	cmd.Dir = homeDir
	cmd.Env = os.Environ()

	out, err := cmd.Output()

	return string(out), err
}
