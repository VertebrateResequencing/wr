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

package testsuite

import (
	"bytes"
	"strings"
	"sync"
	"testing"

	. "github.com/smartystreets/goconvey/convey"
)

// activeProgress builds a progress whose render target is buf so its state
// mutators are live (active() is true) without needing a real terminal.
func activeProgress(buf *bytes.Buffer) *progress {
	return &progress{out: buf, sty: newStyle(true)}
}

func TestProgressScanner(t *testing.T) {
	Convey("The tee writes the full input through unchanged", t, func() {
		var log, sink bytes.Buffer

		writer := &runScanWriter{progress: activeProgress(&sink), inner: &log}

		input := "=== RUN   TestOne\n--- PASS: TestOne (0.00s)\nrandom line\nPASS\n"

		n, err := writer.Write([]byte(input))

		So(err, ShouldBeNil)
		So(n, ShouldEqual, len(input))
		So(log.String(), ShouldEqual, input)
	})

	Convey("The scanner records top-level RUN names and ignores subtests", t, func() {
		var log, sink bytes.Buffer

		prog := activeProgress(&sink)
		writer := &runScanWriter{progress: prog, inner: &log}

		input := "=== RUN   TestAlpha\n" +
			"=== RUN   TestAlpha/sub\n" +
			"    --- PASS: TestAlpha/sub (0.00s)\n" +
			"=== RUN   TestBeta\n"

		_, err := writer.Write([]byte(input))

		So(err, ShouldBeNil)
		So(prog.st.testsStarted, ShouldEqual, 2)
		So(prog.st.latestTest, ShouldEqual, "TestBeta")
	})

	Convey("A RUN marker split across two writes is still recognised", t, func() {
		var log, sink bytes.Buffer

		prog := activeProgress(&sink)
		writer := &runScanWriter{progress: prog, inner: &log}

		_, err1 := writer.Write([]byte("=== RUN   Test"))
		_, err2 := writer.Write([]byte("Split\n"))

		So(err1, ShouldBeNil)
		So(err2, ShouldBeNil)
		So(prog.st.testsStarted, ShouldEqual, 1)
		So(prog.st.latestTest, ShouldEqual, "TestSplit")
		So(log.String(), ShouldEqual, "=== RUN   TestSplit\n")
	})
}

func TestRenderFrame(t *testing.T) {
	Convey("The setup phase shows a spinner and the dim phase label", t, func() {
		state := progressState{phase: "compiling test binaries", lanesTotal: 10}

		rich := renderFrame(state, newStyle(true), 80)
		So(rich, ShouldContainSubstring, "compiling test binaries…")
		So(rich, ShouldContainSubstring, ansiCyan)
		So(rich, ShouldContainSubstring, ansiDim)
		So(rich, ShouldNotContainSubstring, "lanes")
	})

	Convey("The test phase shows counts and the latest test function", t, func() {
		state := progressState{
			testing:      true,
			lanesTotal:   47,
			lanesDone:    12,
			testsStarted: 142,
			latestTest:   "TestJobqueueSignal",
		}

		Convey("rich frames colour the counts and name", func() {
			rich := renderFrame(state, newStyle(true), 200)

			So(rich, ShouldContainSubstring, "142")
			So(rich, ShouldContainSubstring, "tests")
			So(rich, ShouldContainSubstring, "12/47")
			So(rich, ShouldContainSubstring, "lanes")
			So(rich, ShouldContainSubstring, ansiCyan+"TestJobqueueSignal"+ansiReset)
		})

		Convey("plain frames carry the same information without ANSI", func() {
			plain := renderFrame(state, newStyle(false), 200)

			So(plain, ShouldNotContainSubstring, "\x1b[")
			So(plain, ShouldContainSubstring, "142 tests")
			So(plain, ShouldContainSubstring, "12/47 lanes")
			So(plain, ShouldContainSubstring, "TestJobqueueSignal")
		})
	})

	Convey("A frame is truncated to the terminal width and never wraps", t, func() {
		state := progressState{
			testing:      true,
			lanesTotal:   47,
			lanesDone:    12,
			testsStarted: 142,
			latestTest:   "TestSomethingVeryLongIndeed",
		}

		plain := renderFrame(state, newStyle(false), 15)

		So(len([]rune(plain)), ShouldEqual, 15)
	})
}

func TestProgressNoOp(t *testing.T) {
	Convey("A non-terminal progress writes nothing and is safe to drive", t, func() {
		var stderr bytes.Buffer

		prog := newProgress(&stderr, 5)
		So(prog.active(), ShouldBeFalse)

		prog.start()
		prog.setPhase("compiling")
		prog.beginTesting()
		prog.laneStarted()
		prog.testStarted("TestThing")
		prog.laneFinished()
		prog.stop()
		prog.stop()

		So(stderr.Len(), ShouldEqual, 0)
	})

	Convey("A non-terminal progress returns its writers unchanged", t, func() {
		var stderr, log bytes.Buffer

		prog := newProgress(&stderr, 5)

		So(prog.bypass(&log), ShouldEqual, &log)
		So(prog.tee(&log), ShouldEqual, &log)
	})

	Convey("A nil progress is safe to use", t, func() {
		var prog *progress

		So(func() {
			prog.start()
			prog.laneStarted()
			prog.testStarted("x")
			prog.laneFinished()
			prog.stop()
		}, ShouldNotPanic)
	})

	Convey("Stop clears the spinner line so the summary is not corrupted", t, func() {
		var stderr bytes.Buffer

		prog := activeProgress(&stderr)
		prog.quit = make(chan struct{})
		prog.done = make(chan struct{})

		prog.start()
		prog.stop()

		So(stderr.String(), ShouldEndWith, clearLine)
		So(strings.Count(stderr.String(), "\n"), ShouldEqual, 0)
	})
}

func TestProgressConcurrency(t *testing.T) {
	Convey("Concurrent lane and test updates are counted safely", t, func() {
		var stderr bytes.Buffer

		prog := activeProgress(&stderr)
		prog.quit = make(chan struct{})
		prog.done = make(chan struct{})
		prog.start()

		const lanes = 20

		var wg sync.WaitGroup

		for range lanes {
			wg.Go(func() {
				prog.laneStarted()
				prog.testStarted("TestConcurrent")
				prog.laneFinished()
			})
		}

		wg.Wait()
		prog.stop()

		So(prog.st.lanesDone, ShouldEqual, lanes)
		So(prog.st.testsStarted, ShouldEqual, lanes)
	})
}
