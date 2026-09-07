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

package cmd

import (
	"errors"
	"testing"

	"github.com/VertebrateResequencing/wr/jobqueue"
	jqs "github.com/VertebrateResequencing/wr/jobqueue/scheduler"
	. "github.com/smartystreets/goconvey/convey"
)

// selectionEssenceImage is the container image the containerised jobs below are
// added with. Nothing runs them, so no container runtime is involved.
const selectionEssenceImage = "ubuntu:latest"

// TestSelectionByCmdLine drives the real -l selection path of a queue command
// for the kinds of job whose key -l has to reproduce: one added with a cwd but
// without CwdMatters, and one added with a container image. It also proves that
// the cwd fallback cannot reach a job the user did not name.
func TestSelectionByCmdLine(t *testing.T) {
	Convey("-l with -c finds a job that was added with a cwd but not cwd_matters", t, func() {
		withQueueCommandTestServer(t, func(jq *jobqueue.Client, reqs *jqs.Requirements, _ jobqueue.ServerConfig) {
			cwd := t.TempDir()
			job := newQueueCommandJob("echo cwd does not matter", "rg-select-cwd", reqs)
			job.Cwd = cwd
			So(job.CwdMatters, ShouldBeFalse)
			addQueueCommandJobs(jq, job)

			output, err := runSuspendForTest(t, "-l", "echo cwd does not matter", "-c", cwd)
			So(err, ShouldBeNil)
			So(output, ShouldEqual, "Suspended 1 queued commands (out of 1 matching)\n")
			assertStatusPlainStateCount(t, jobqueue.JobStateSuspended, 1,
				"-l", "echo cwd does not matter", "-c", cwd, "-o", "plain")
			So(jobStateByEssence(jq, job), ShouldEqual, jobqueue.JobStateSuspended)
		})
	})

	Convey("-l with a different -c matches no non-cwd_matters job", t, func() {
		withQueueCommandTestServer(t, func(jq *jobqueue.Client, reqs *jqs.Requirements, _ jobqueue.ServerConfig) {
			job := newQueueCommandJob("echo wrong cwd", "rg-select-wrong-cwd", reqs)
			job.Cwd = t.TempDir()
			addQueueCommandJobs(jq, job)

			output, err := runSuspendForTest(t, "-l", "echo wrong cwd", "-c", t.TempDir())
			So(err, ShouldEqual, errSelectedJobsNoMatch)
			So(output, ShouldBeEmpty)
			So(jobStateByEssence(jq, job), ShouldEqual, jobqueue.JobStateReady)
		})
	})

	Convey("-l with -c prefers the cwd_matters job with that cwd", t, func() {
		withQueueCommandTestServer(t, func(jq *jobqueue.Client, reqs *jqs.Requirements, _ jobqueue.ServerConfig) {
			const cmdLine = "echo shared by both"

			matters := newQueueCommandJob(cmdLine, "rg-select-both", reqs)
			matters.Cwd = t.TempDir()
			matters.CwdMatters = true
			doesNot := newQueueCommandJob(cmdLine, "rg-select-both", reqs)
			doesNot.Cwd = t.TempDir()
			addQueueCommandJobs(jq, matters, doesNot)

			output, err := runSuspendForTest(t, "-l", cmdLine, "-c", matters.Cwd)
			So(err, ShouldBeNil)
			So(output, ShouldEqual, "Suspended 1 queued commands (out of 1 matching)\n")
			So(jobStateByEssence(jq, matters), ShouldEqual, jobqueue.JobStateSuspended)
			So(jobStateByEssence(jq, doesNot), ShouldEqual, jobqueue.JobStateReady)
		})
	})

	Convey("-l with --with_docker finds a containerised job", t, func() {
		withQueueCommandTestServer(t, func(jq *jobqueue.Client, reqs *jqs.Requirements, _ jobqueue.ServerConfig) {
			job := newQueueCommandJob("echo containerised", "rg-select-docker", reqs)
			job.WithDocker = selectionEssenceImage
			addQueueCommandJobs(jq, job)

			output, err := runSuspendForTest(t, "-l", "echo containerised",
				"--with_docker", selectionEssenceImage)
			So(err, ShouldBeNil)
			So(output, ShouldEqual, "Suspended 1 queued commands (out of 1 matching)\n")
			assertStatusPlainStateCount(t, jobqueue.JobStateSuspended, 1, "-l", "echo containerised",
				"--with_docker", selectionEssenceImage, "-o", "plain")
			So(jobStateByEssence(jq, job), ShouldEqual, jobqueue.JobStateSuspended)
		})
	})

	Convey("-l with --with_docker and -c finds a containerised non-cwd_matters job", t, func() {
		withQueueCommandTestServer(t, func(jq *jobqueue.Client, reqs *jqs.Requirements, _ jobqueue.ServerConfig) {
			job := newQueueCommandJob("echo containerised in cwd", "rg-select-docker-cwd", reqs)
			job.WithDocker = selectionEssenceImage
			job.ContainerMounts = "/mnt"
			addQueueCommandJobs(jq, job)

			output, err := runSuspendForTest(t, "-l", "echo containerised in cwd",
				"--with_docker", selectionEssenceImage, "--container_mounts", "/mnt", "-c", job.Cwd)
			So(err, ShouldBeNil)
			So(output, ShouldEqual, "Suspended 1 queued commands (out of 1 matching)\n")
			So(jobStateByEssence(jq, job), ShouldEqual, jobqueue.JobStateSuspended)
		})
	})

	Convey("-l with the wrong container options matches no containerised job", t, func() {
		withQueueCommandTestServer(t, func(jq *jobqueue.Client, reqs *jqs.Requirements, _ jobqueue.ServerConfig) {
			job := newQueueCommandJob("echo container mismatch", "rg-select-mismatch", reqs)
			job.WithDocker = selectionEssenceImage
			addQueueCommandJobs(jq, job)

			var wronglyMatched []string

			for _, tc := range []struct {
				name string
				args []string
			}{
				{
					name: "wrong container_mounts",
					args: []string{"--with_docker", selectionEssenceImage, "--container_mounts", "/mnt"},
				},
				{
					name: "wrong image",
					args: []string{"--with_docker", "alpine:latest"},
				},
				{
					name: "no image",
					args: nil,
				},
			} {
				output, err := runSuspendForTest(t,
					append([]string{"-l", "echo container mismatch"}, tc.args...)...)
				if !errors.Is(err, errSelectedJobsNoMatch) || output != "" {
					wronglyMatched = append(wronglyMatched, tc.name)
				}
			}

			So(wronglyMatched, ShouldBeEmpty)
			So(jobStateByEssence(jq, job), ShouldEqual, jobqueue.JobStateReady)
		})
	})

	Convey("selection commands reject both container images at once", t, func() {
		output, err := runSuspendForTest(t, "-l", "echo both images",
			"--with_docker", selectionEssenceImage, "--with_singularity", "image.sif")
		So(err, ShouldEqual, errSelectionContainerExclusive)
		So(output, ShouldBeEmpty)

		output, err = runResumeForTest(t, "-l", "echo both images",
			"--with_docker", selectionEssenceImage, "--with_singularity", "image.sif")
		So(err, ShouldEqual, errSelectionContainerExclusive)
		So(output, ShouldBeEmpty)
	})
}
