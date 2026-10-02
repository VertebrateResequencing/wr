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
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"syscall"
	"testing"

	. "github.com/smartystreets/goconvey/convey"
)

const (
	peakRAMTestHighWaterMB = 512
	envPeakRAMChild        = "WR_TEST_PEAK_RAM_CHILD"
	peakRAMChildOK         = "peak RAM child ok"
)

// TestExecutePeakRAMChild is the child of TestExecutePeakRAMUnderLargeParent.
func TestExecutePeakRAMChild(t *testing.T) {
	if os.Getenv(envPeakRAMChild) == "" {
		t.Skip("child of TestExecutePeakRAMUnderLargeParent")
	}

	Convey("A brief command's peak is recorded", t, func() {
		client := newLiveExecuteCaptureClient(&liveTouchCapture{})
		cmdMB := peakRAMTestHighWaterMB / 2
		job := liveExecuteJob(client, liveExecuteCwd(t),
			fmt.Sprintf(`perl -e 'my $x = "A" x (%d * 1024 * 1024)'`, cmdMB))

		So(client.Execute(context.Background(), job, "/bin/sh"), ShouldBeNil)
		So(job.Exitcode, ShouldEqual, 0)
		t.Logf("child recorded PeakRAM %dMB for a %dMB command", job.PeakRAM, cmdMB)
		So(job.PeakRAM, ShouldBeGreaterThanOrEqualTo, cmdMB)
	})

	if !t.Failed() {
		t.Log(peakRAMChildOK)
	}
}

func TestExecutePeakRAMExcludesRunnerHighWater(t *testing.T) {
	if runnermode || servermode {
		return
	}

	if runtime.GOOS != osLinux {
		SkipConvey("A command's peak RAM excludes the runner's earlier peak (linux only)", t, func() {})

		return
	}

	Convey("A brief command that peaks above the runner's own peak still records that peak", t, func() {
		if _, err := exec.LookPath("perl"); err != nil {
			SkipSo("perl is not installed")

			return
		}

		capture := &liveTouchCapture{}
		client := newLiveExecuteCaptureClient(capture)
		cmdMB := int(ownPeakRSS()/kbPerMB) + peakRAMTestHighWaterMB/2
		job := liveExecuteJob(client, liveExecuteCwd(t),
			fmt.Sprintf(`perl -e 'my $x = "A" x (%d * 1024 * 1024)'`, cmdMB))

		So(client.Execute(context.Background(), job, "/bin/sh"), ShouldBeNil)
		So(job.Exitcode, ShouldEqual, 0)
		So(job.PeakRAM, ShouldBeGreaterThanOrEqualTo, cmdMB)
	})

	Convey("A small command run by a runner that once used a lot of memory records a small peak RAM", t, func() {
		raiseOwnHighWaterRSS(peakRAMTestHighWaterMB)

		capture := &liveTouchCapture{}
		client := newLiveExecuteCaptureClient(capture)
		job := liveExecuteJob(client, liveExecuteCwd(t), "sleep 1.5")

		So(client.Execute(context.Background(), job, "/bin/sh"), ShouldBeNil)
		So(job.Exitcode, ShouldEqual, 0)

		sampled := 0
		for _, state := range capture.matching(func(*JobEndState) bool { return true }) {
			sampled = max(sampled, state.PeakRAM)
		}

		ownMB, err := ownMemoryMB()
		So(err, ShouldBeNil)
		t.Logf("recorded PeakRAM %dMB; max live-sampled PeakRAM %dMB; runner own Pss %dMB; runner high water >= %dMB",
			job.PeakRAM, sampled, ownMB, peakRAMTestHighWaterMB)

		So(job.PeakRAM, ShouldBeGreaterThan, 0)
		So(job.PeakRAM, ShouldBeLessThan, ownMB+peakRAMTestHighWaterMB/2)
	})
}

// TestExecutePeakRAMUnderLargeParent runs a child runner from this process
// after this process has peaked high, as a large manager starts runners under
// the local scheduler, and checks the child still records the peak of a brief
// command that peaks above the child's own peak but below ours.
func TestExecutePeakRAMUnderLargeParent(t *testing.T) {
	if runnermode || servermode {
		return
	}

	if runtime.GOOS != osLinux {
		t.Skip("the inherited peak RSS is linux behaviour")
	}

	if _, err := exec.LookPath("perl"); err != nil {
		t.Skip("perl is not installed")
	}

	Convey("A runner started by a process with a large peak still records its command's own peak", t, func() {
		raiseOwnHighWaterRSS(peakRAMTestHighWaterMB)

		cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.run", "^TestExecutePeakRAMChild$", //nolint:gosec
			"-test.v")

		cmd.Env = append(os.Environ(), envPeakRAMChild+"=1")

		out, err := cmd.CombinedOutput()
		So(string(out), ShouldContainSubstring, peakRAMChildOK)
		So(err, ShouldBeNil)
	})
}

// raiseOwnHighWaterRSS makes this process's peak RSS at least mb MB without
// keeping that memory: it maps, touches and unmaps anonymous memory, so the
// kernel's high-water mark stays raised while the process's current memory
// (which Execute legitimately counts as the runner's own) drops back.
func raiseOwnHighWaterRSS(mb int) {
	size := mb * bytesPerKB * kbPerMB

	mem, err := syscall.Mmap(-1, 0, size, syscall.PROT_READ|syscall.PROT_WRITE,
		syscall.MAP_ANON|syscall.MAP_PRIVATE)
	So(err, ShouldBeNil)

	pageSize := os.Getpagesize()
	for i := 0; i < size; i += pageSize {
		mem[i] = 1
	}

	So(syscall.Munmap(mem), ShouldBeNil)
}
