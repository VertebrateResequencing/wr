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
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	jqs "github.com/VertebrateResequencing/wr/jobqueue/scheduler"
	. "github.com/smartystreets/goconvey/convey"
)

// TestContainerCmdFileInJobTmpDir covers item 10 of .docs/bugfixes/260903-9.md:
// the file holding a container job's Cmd was made in the runner's own tmp dir,
// not the TMPDIR wr makes for the job, so a runner killed mid-job left it
// behind outside the job's workspace.
func TestContainerCmdFileInJobTmpDir(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()
	config, serverConfig, addr, _, clientConnectTime := jobqueueTestInit(false)

	Convey("Given a live manager and a reserved --with_singularity job", t, func() {
		server, _, token, err := serve(ctx, serverConfig)
		So(err, ShouldBeNil)

		defer server.Stop(ctx, true)

		jq, err := Connect(addr, config.ManagerCAFile, config.ManagerCertDomain, token, clientConnectTime)
		So(err, ShouldBeNil)

		defer disconnect(jq)

		runnerTmp := t.TempDir()
		t.Setenv("TMPDIR", runnerTmp)

		// a stand-in singularity that records what is in the TMPDIR the job's
		// command line runs with, while the cmd file is being read
		binDir := t.TempDir()
		listing := filepath.Join(t.TempDir(), "listing")
		fake := "#!/bin/sh\ncat >/dev/null\nls \"$TMPDIR\" > '" + listing + "'\n"
		So(os.WriteFile(filepath.Join(binDir, "singularity"), []byte(fake), 0o700), ShouldBeNil) //nolint:gosec

		env := append(os.Environ(), "PATH="+binDir+":"+os.Getenv("PATH"))

		const repGroup = "container_cmd_file"

		job := &Job{
			Cmd: testTrueCmd, Cwd: t.TempDir(), RepGroup: repGroup, ReqGroup: repGroup,
			WithSingularity: "image.sif",
			Requirements:    &jqs.Requirements{RAM: 10, Time: time.Minute, Cores: 0, Other: make(map[string]string)},
		}

		added, _, err := jq.Add([]*Job{job}, env, true)
		So(err, ShouldBeNil)
		So(added, ShouldEqual, 1)

		reserved, err := jq.Reserve(2 * time.Second)
		So(err, ShouldBeNil)
		So(reserved, ShouldNotBeNil)

		Convey("Execute makes its cmd file in the job's TMPDIR, not the runner's", func() {
			So(jq.Execute(ctx, reserved, "bash"), ShouldBeNil)

			content, errr := os.ReadFile(listing)
			So(errr, ShouldBeNil)
			So(string(content), ShouldContainSubstring, "container.cmd")

			entries, errr := os.ReadDir(runnerTmp)
			So(errr, ShouldBeNil)

			var leftInRunnerTmp []string

			for _, entry := range entries {
				if strings.HasPrefix(entry.Name(), "container.cmd") {
					leftInRunnerTmp = append(leftInRunnerTmp, entry.Name())
				}
			}

			So(leftInRunnerTmp, ShouldBeEmpty)
		})
	})
}
