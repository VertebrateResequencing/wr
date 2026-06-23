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
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/VertebrateResequencing/wr/jobqueue/scheduler"
	. "github.com/smartystreets/goconvey/convey"
)

const testExecuteOp = "Execute"

func TestCgroupOOMKillAttribution(t *testing.T) {
	Convey("Given fabricated cgroup v2 memory events, OOM kills are attributed by counter increases", t, func() {
		tmp := t.TempDir()
		proc := filepath.Join(tmp, "proc")
		cgroups := filepath.Join(tmp, "cgroup")
		pid := "123"
		jobCgroup := filepath.Join(cgroups, "jobs", "abc")
		So(os.MkdirAll(filepath.Join(proc, pid), 0755), ShouldBeNil)
		So(os.MkdirAll(jobCgroup, 0755), ShouldBeNil)
		So(os.WriteFile(filepath.Join(proc, pid, "cgroup"), []byte("0::/jobs/abc\n"), 0600), ShouldBeNil)
		err := os.WriteFile(
			filepath.Join(jobCgroup, "memory.events"),
			[]byte("low 0\nhigh 0\noom 1\noom_kill 2\n"),
			0600,
		)
		So(err, ShouldBeNil)

		monitor, err := newCgroupOOMMonitor(123, proc, cgroups)
		So(err, ShouldBeNil)
		So(monitor.oomKillIncreased(), ShouldBeFalse)

		err = os.WriteFile(
			filepath.Join(jobCgroup, "memory.events"),
			[]byte("low 0\nhigh 0\noom 2\noom_kill 3\n"),
			0600,
		)
		So(err, ShouldBeNil)
		So(monitor.oomKillIncreased(), ShouldBeTrue)
	})

	Convey("Given fabricated cgroup v1 memory.oom_control, OOM kills are attributed by counter increases", t, func() {
		tmp := t.TempDir()
		proc := filepath.Join(tmp, "proc")
		cgroups := filepath.Join(tmp, "cgroup")
		pid := "456"
		jobCgroup := filepath.Join(cgroups, "memory", "batch", "one")
		So(os.MkdirAll(filepath.Join(proc, pid), 0755), ShouldBeNil)
		So(os.MkdirAll(jobCgroup, 0755), ShouldBeNil)

		err := os.WriteFile(
			filepath.Join(proc, pid, "cgroup"),
			[]byte("7:cpu,cpuacct:/batch/one\n8:memory:/batch/one\n"),
			0600,
		)
		So(err, ShouldBeNil)
		err = os.WriteFile(
			filepath.Join(jobCgroup, "memory.oom_control"),
			[]byte("under_oom 0\noom_kill 4\n"),
			0600,
		)
		So(err, ShouldBeNil)

		monitor, err := newCgroupOOMMonitor(456, proc, cgroups)
		So(err, ShouldBeNil)
		So(monitor.oomKillIncreased(), ShouldBeFalse)

		err = os.WriteFile(
			filepath.Join(jobCgroup, "memory.oom_control"),
			[]byte("under_oom 0\noom_kill 5\n"),
			0600,
		)
		So(err, ShouldBeNil)
		So(monitor.oomKillIncreased(), ShouldBeTrue)
	})
}

func TestMemoryFailureAttribution(t *testing.T) {
	Convey("A high-memory non-SIGKILL exit is not attributed to RAM", t, func() {
		status := syscall.WaitStatus(1 << 8)

		So(attributedMemoryDeath(false, false, status, 200, 100, "local", true), ShouldBeFalse)

		err := Error{testExecuteOp, "job", FailReasonExit}
		annotated := appendHighMemoryNote(err, 200, 100)
		So(annotated.Error(), ShouldContainSubstring, FailReasonExit)
		So(annotated.Error(), ShouldContainSubstring, "note: "+FailReasonRAM)

		var jqerr Error
		So(errors.As(annotated, &jqerr), ShouldBeTrue)
		So(jqerr.Err, ShouldEqual, FailReasonExit)
	})

	Convey("Local and cloud schedulers use SIGKILL plus high peak as the memory fallback", t, func() {
		status := syscall.WaitStatus(syscall.SIGKILL)
		shellSIGKILL := syscall.WaitStatus((shellSignalExitCodeOffset + int(syscall.SIGKILL)) << 8)

		So(attributedMemoryDeath(false, false, status, 200, 100, "local", true), ShouldBeTrue)
		So(attributedMemoryDeath(false, false, status, 200, 100, "openstack", true), ShouldBeTrue)
		So(attributedMemoryDeath(false, false, shellSIGKILL, 200, 100, "local", true), ShouldBeTrue)
		So(attributedMemoryDeath(false, false, status, 100, 100, "local", true), ShouldBeTrue)
		So(attributedMemoryDeath(false, false, shellSIGKILL, 50, 100, "local", true), ShouldBeFalse)
		So(attributedMemoryDeath(false, false, status, 50, 100, "local", true), ShouldBeFalse)
		So(attributedMemoryDeath(false, false, status, 200, 100, "lsf", true), ShouldBeFalse)
	})

	Convey("Scheduler SIGKILL fallback is suppressed after server contact is lost", t, func() {
		status := syscall.WaitStatus(syscall.SIGKILL)
		shellSIGKILL := syscall.WaitStatus((shellSignalExitCodeOffset + int(syscall.SIGKILL)) << 8)

		So(attributedMemoryDeath(false, false, status, 200, 100, "local", false), ShouldBeFalse)
		So(attributedMemoryDeath(false, false, shellSIGKILL, 200, 100, "local", false), ShouldBeFalse)
		So(attributedMemoryDeath(false, true, status, 50, 100, "local", false), ShouldBeTrue)
	})

	Convey("A non-SIGKILL signal with high peak is not attributed to RAM", t, func() {
		status := syscall.WaitStatus(syscall.SIGTERM)

		So(attributedMemoryDeath(false, false, status, 200, 100, "local", true), ShouldBeFalse)

		err := Error{testExecuteOp, "job", FailReasonSignal}
		annotated := appendHighMemoryNote(err, 200, 100)
		So(annotated.Error(), ShouldContainSubstring, FailReasonSignal)
		So(annotated.Error(), ShouldContainSubstring, "note: "+FailReasonRAM)

		var jqerr Error
		So(errors.As(annotated, &jqerr), ShouldBeTrue)
		So(jqerr.Err, ShouldEqual, FailReasonSignal)
	})

	Convey("A cgroup OOM counter increase is authoritative for OOM-compatible exits", t, func() {
		sigkill := syscall.WaitStatus(syscall.SIGKILL)
		shellSIGKILL := syscall.WaitStatus((shellSignalExitCodeOffset + int(syscall.SIGKILL)) << 8)
		normalExit := syscall.WaitStatus(1 << 8)

		So(attributedMemoryDeath(false, true, sigkill, 50, 100, "lsf", true), ShouldBeTrue)
		So(attributedMemoryDeath(false, true, sigkill, 50, 100, "local", false), ShouldBeTrue)
		So(attributedMemoryDeath(false, true, shellSIGKILL, 50, 100, "openstack", false), ShouldBeTrue)
		So(attributedMemoryDeath(false, true, shellSIGKILL, 50, 100, "", false), ShouldBeTrue)
		So(attributedMemoryDeath(false, true, normalExit, 200, 100, "local", true), ShouldBeFalse)
	})

	Convey("wr's own memory kill still requires SIGKILL plus high peak", t, func() {
		normalExit := syscall.WaitStatus(1 << 8)
		sigkill := syscall.WaitStatus(syscall.SIGKILL)
		shellSIGKILL := syscall.WaitStatus((shellSignalExitCodeOffset + int(syscall.SIGKILL)) << 8)

		So(attributedMemoryDeath(true, false, normalExit, 200, 100, "lsf", true), ShouldBeFalse)
		So(attributedMemoryDeath(true, false, sigkill, 200, 100, "lsf", true), ShouldBeTrue)
		So(attributedMemoryDeath(true, false, shellSIGKILL, 200, 100, "lsf", true), ShouldBeTrue)
		So(attributedMemoryDeath(true, false, sigkill, 50, 100, "lsf", true), ShouldBeFalse)
		So(attributedMemoryDeath(true, false, shellSIGKILL, 50, 100, "lsf", true), ShouldBeFalse)
	})
}

func TestHighPeakMemoryRetryGrowth(t *testing.T) {
	Convey("An automatic retry grows RAM after a high peak, regardless of fail reason", t, func() {
		job := &Job{
			Requirements: &scheduler.Requirements{RAM: 100, Time: time.Minute, Cores: 1},
			PeakRAM:      200,
			FailReason:   FailReasonExit,
			State:        JobStateDelayed,
		}

		So(shouldIncreaseJobRAMAfterHighPeak(job), ShouldBeTrue)

		updateJobRequirementsForRetry(job, jobOverridePreferSystemReqs, nil)

		So(job.Requirements.RAM, ShouldEqual, 1200)
		So(job.FailReason, ShouldEqual, FailReasonExit)
	})

	Convey("A RAM failure grows RAM even when sampled peak did not exceed the request", t, func() {
		job := &Job{
			Requirements: &scheduler.Requirements{RAM: 100, Time: time.Minute, Cores: 1},
			PeakRAM:      90,
			FailReason:   FailReasonRAM,
		}

		So(shouldIncreaseJobRAMAfterHighPeak(job), ShouldBeTrue)

		updateJobRequirementsForRetry(job, jobOverridePreferSystemReqs, nil)

		So(job.Requirements.RAM, ShouldEqual, 1100)
		So(job.RequirementsOrig.RAM, ShouldEqual, 100)
	})

	Convey("Recommended RAM is applied before non-RAM high-peak retry growth is reconsidered", t, func() {
		job := &Job{
			Requirements: &scheduler.Requirements{RAM: 100, Time: time.Minute, Cores: 1},
			PeakRAM:      200,
			FailReason:   FailReasonExit,
			State:        JobStateDelayed,
		}
		recommendedReq := &scheduler.Requirements{RAM: 300}

		So(shouldIncreaseJobRAMAfterHighPeak(job), ShouldBeTrue)

		updateJobRequirementsForRetry(job, jobOverridePreferSystemReqs, recommendedReq)

		So(job.Requirements.RAM, ShouldEqual, 300)
		So(job.RequirementsOrig.RAM, ShouldEqual, 100)
		So(shouldIncreaseJobRAMAfterHighPeak(job), ShouldBeFalse)
	})

	Convey("A high peak without a failure reason does not grow RAM", t, func() {
		job := &Job{
			Requirements: &scheduler.Requirements{RAM: 100, Time: time.Minute, Cores: 1},
			PeakRAM:      200,
		}

		So(shouldIncreaseJobRAMAfterHighPeak(job), ShouldBeFalse)
		So(job.Requirements.RAM, ShouldEqual, 100)
	})

	Convey("A kicked non-RAM failure does not grow RAM from stale peak usage", t, func() {
		job := &Job{
			Requirements: &scheduler.Requirements{RAM: 100, Time: time.Minute, Cores: 1},
			PeakRAM:      200,
			FailReason:   FailReasonExit,
			State:        JobStateReady,
		}

		So(shouldIncreaseJobRAMAfterHighPeak(job), ShouldBeFalse)
		So(job.Requirements.RAM, ShouldEqual, 100)
	})

	Convey("A kicked RAM failure still grows RAM from peak usage", t, func() {
		job := &Job{
			Requirements: &scheduler.Requirements{RAM: 100, Time: time.Minute, Cores: 1},
			PeakRAM:      200,
			FailReason:   FailReasonRAM,
			UntilBuried:  1,
		}

		So(shouldIncreaseJobRAMAfterHighPeak(job), ShouldBeTrue)
	})
}
