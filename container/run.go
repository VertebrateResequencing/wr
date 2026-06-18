/*******************************************************************************
 * Copyright (c) 2021 Genome Research Ltd.
 *
 * Author: Sendu Bala <sb10@sanger.ac.uk>
 *
 * Permission is hereby granted, free of charge, to any person obtaining a copy
 * of this software and associated documentation files (the "Software"), to
 * deal in the Software without restriction, including without limitation the
 * rights to use, copy, modify, merge, publish, distribute, sublicense, and/or
 * sell copies of the Software, and to permit persons to whom the Software is
 * furnished to do so, subject to the following conditions:
 *
 * The above copyright notice and this permission notice shall be included in
 * all copies or substantial portions of the Software.
 *
 * THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
 * IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
 * FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
 * AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
 * LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING
 * FROM, OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS
 * IN THE SOFTWARE.
 ******************************************************************************/

package container

// This file contains some convienience methods for constructing command lines
// for running of commands in Docker and Singularity containers. We don't use
// Docker's GO API to do this because we want consistency with Singularity which
// doesn't offer an API, and we want it to be easy to integrate in to an
// existing system that uses exec.Command() for non-container commands.

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/VertebrateResequencing/wr/clog"
)

// dockerMountParts is the number of parts we expect to see after splitting
// mount args on a colon.
const dockerMountParts = 2

// PrepareCmdFile creates a temporary file containing the given command and
// returns its path, as well as a method you can defer that will delete the
// file.
//
// File deletion errors in the returned method are logged using clog and the
// given ctx.
func PrepareCmdFile(ctx context.Context, cmd string) (string, func(), error) {
	f, cleanup, err := createTmpFileAndCleanupMethod(ctx)
	if err != nil {
		return "", nil, err
	}

	err = writeStringToFile(f, cmd, cleanup)

	return f.Name(), cleanup, err
}

// createTmpFileAndCleanupMethod creates a tmp file and a method to delete it
// afterwards, logging deletion errors using clog.
func createTmpFileAndCleanupMethod(ctx context.Context) (*os.File, func(), error) {
	f, err := os.CreateTemp("", "container.cmd")
	if err != nil {
		return nil, nil, err
	}

	return f, func() {
		errr := os.Remove(f.Name())
		if errr != nil {
			clog.Warn(ctx, "container command file could not be deleted", "err", errr)
		}
	}, nil
}

// writeStringToFile writes the given string to the given opened file, appending
// it with a newline, and closing the file after a successful write. If the
// write fails, the given cleanup method will be called.
func writeStringToFile(f *os.File, content string, cleanup func()) error {
	if _, err := f.WriteString(content + "\n"); err != nil {
		cleanup()

		return err
	}

	return f.Close()
}

// DockerRunCmd returns a `docker run` command line that will:
//
// * Pull the given image if it is missing.
// * Create a container with the given name.
//   * That will mount the current working directory inside the container and
//     use it as the workdir.
//   * That will also mount any given disk locations, in the format
//     "/local/path:/inside/container/path" (the colon and inside path being
//     optional if the same as local path).
//   * That will set the given environment variables inside the container to
//     their values outside the container.
//   * That will run the command in the given file (by piping the file contents
//     to /bin/sh); use PrepareCmdFile() to create one.
// * Automatically remove the container when it exits.
func DockerRunCmd(image, cmdFile, name string, mounts, env []string) string {
	mountArgs := dockerMounts(mounts)
	envArgs := dockerEnv(env)

	return fmt.Sprintf("cat %s | docker run --rm --name %s%s%s -i %s /bin/sh",
		cmdFile, name, mountArgs, envArgs, image)
}

// dockerMounts takes a list of "/local/path[:/inside/container/path]" values
// and converts them in to a series of `docker run --mount` args.
//
// It always returns a mount for $PWD and sets -w to that as well.
func dockerMounts(mounts []string) string {
	args := " -w $PWD --mount type=bind,source=$PWD,target=$PWD"

	for _, spec := range mounts {
		parts := strings.Split(spec, ":")
		out := parts[0]
		in := parts[0]

		if len(parts) == dockerMountParts {
			in = parts[1]
		}

		args += fmt.Sprintf(" --mount type=bind,source=%s,target=%s", out, in)
	}

	return args
}

// dockerEnv takes a list of environment variable names and converts them in to
// a series of `docker run -e` args.
func dockerEnv(names []string) string {
	return listToPrefixedString(names, " -e ")
}

// listToPrefixedString creates a single string comprising vals concatenated
// together with prefix.
func listToPrefixedString(vals []string, prefix string) string {
	var str string

	if len(vals) > 0 {
		str = prefix + strings.Join(vals, prefix)
	}

	return str
}

// SingularityRunCmd returns a `singularity shell` command line that will:
//
// * Pull the given image if it is missing, creating a sif image if it's a
//   docker image.
// * Create a container.
//   * That will run the command in the given file (by piping the file contents
//     to the container's shell); use PrepareCmdFile() to create one.
//   * That will mount the given disk locations, in the format
//     "/local/path:/inside/container/path" (the colon and inside path being
//     optional if the same as local path). The CWD is always mounted at / in
//     container.
//   * That will have all environment variables outside the container
//     replicated inside the container.
// * Automatically remove the container when it exits.
func SingularityRunCmd(image, cmdFile string, mounts []string) string {
	mountArgs := singularityMounts(mounts)

	return fmt.Sprintf("cat %s | singularity shell%s %s", cmdFile, mountArgs, image)
}

// singularityMounts takes a list of "/local/path[:/inside/container/path]"
// values and converts them in to a series of `singularity shell -B` args.
func singularityMounts(mounts []string) string {
	return listToPrefixedString(mounts, " -B ")
}
