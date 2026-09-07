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
	"github.com/kballard/go-shellquote"
)

// dockerMountParts is the number of parts we expect to see after splitting
// mount args on a colon.
const dockerMountParts = 2

// workDirMountArgs bind mounts the current working directory inside the
// container and makes it the workdir.
//
// $PWD stays a shell expansion so that the shell we build the command line for
// resolves it at run time, which rules out shellquote.Join(): that would emit
// '$PWD' and kill the expansion. The double quotes keep the expansion while
// still making each of these a single argument when the working directory
// contains a space.
const workDirMountArgs = ` -w "$PWD" --mount type=bind,source="$PWD",target="$PWD"`

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
//   - That will mount the current working directory inside the container and
//     use it as the workdir.
//   - That will also mount any given disk locations, in the format
//     "/local/path:/inside/container/path" (the colon and inside path being
//     optional if the same as local path).
//   - That will set the given environment variables inside the container to
//     their values outside the container.
//   - That will run the command in the given file (by piping the file contents
//     to /bin/sh); use PrepareCmdFile() to create one.
//
// * Automatically remove the container when it exits.
func DockerRunCmd(image, cmdFile, name string, mounts, env []string) string {
	mountArgs := dockerMounts(mounts)
	envArgs := dockerEnv(env)

	return fmt.Sprintf("cat %s | docker run --rm --name %s%s%s -i %s /bin/sh",
		shellquote.Join(cmdFile), shellquote.Join(name), mountArgs, envArgs, shellquote.Join(image))
}

// dockerMounts takes a list of "/local/path[:/inside/container/path]" values
// and converts them in to a series of `docker run --mount` args.
//
// It always returns a mount for $PWD and sets -w to that as well.
func dockerMounts(mounts []string) string {
	var args strings.Builder

	args.WriteString(workDirMountArgs)

	for _, spec := range mounts {
		out, in := MountSpecPaths(spec)

		fmt.Fprintf(&args, " --mount %s", shellquote.Join("type=bind,source="+out+",target="+in))
	}

	return args.String()
}

// MountSpecPaths splits a mount specification in the form
// "/local/path[:/inside/container/path]" in to its local path and its path
// inside the container.
//
// The 2 are the same when the spec has no colon, and - preserving the behaviour
// of the code this was extracted from - also when it has 2 or more, so "/a:/b:ro"
// gives ("/a", "/a") rather than ("/a", "/b"). One consequence is that
// jobqueue's containerMountsMessage then checks the local path twice and never
// inspects the in-container one. What a 3-part spec should mean is a user-facing
// format question, so the behaviour is left as it was.
func MountSpecPaths(spec string) (local, inContainer string) {
	parts := strings.Split(spec, ":")

	if len(parts) == dockerMountParts {
		return parts[0], parts[1]
	}

	return parts[0], parts[0]
}

// dockerEnv takes a list of environment variable names and converts them in to
// a series of `docker run -e` args.
func dockerEnv(names []string) string {
	return listToPrefixedString(shellQuoteEach(names), " -e ")
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
//   - Pull the given image if it is missing, creating a sif image if it's a
//     docker image.
//   - Create a container.
//   - That will run the command in the given file (by piping the file contents
//     to the container's shell); use PrepareCmdFile() to create one.
//   - That will mount the given disk locations, in the format
//     "/local/path:/inside/container/path" (the colon and inside path being
//     optional if the same as local path). The CWD is always mounted at / in
//     container.
//   - That will have all environment variables outside the container
//     replicated inside the container.
//   - Automatically remove the container when it exits.
func SingularityRunCmd(image, cmdFile string, mounts []string) string {
	mountArgs := singularityMounts(mounts)

	return fmt.Sprintf("cat %s | singularity shell%s %s",
		shellquote.Join(cmdFile), mountArgs, shellquote.Join(image))
}

// singularityMounts takes a list of "/local/path[:/inside/container/path]"
// values and converts them in to a series of `singularity shell -B` args.
func singularityMounts(mounts []string) string {
	return listToPrefixedString(shellQuoteEach(mounts), " -B ")
}

// shellQuoteEach shell quotes each of the given values, so that each stays a
// single word once the command line built from them is executed by a shell.
func shellQuoteEach(vals []string) []string {
	quoted := make([]string, len(vals))

	for i, val := range vals {
		quoted[i] = shellquote.Join(val)
	}

	return quoted
}
