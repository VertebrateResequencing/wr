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

//nolint:goconst // Test cases repeat lane names and log fragments to document summary behaviour.
package testsuite

import (
	"strings"
	"testing"

	. "github.com/smartystreets/goconvey/convey"
)

const summaryTestModule = "github.com/VertebrateResequencing/wr"

func TestSummarizeFailure(t *testing.T) {
	Convey("Failure rendering strips verbose noise but keeps failures and convey context", t, func() {
		raw := `=== RUN   TestKept
=== PAUSE TestKept
=== CONT  TestKept
--- PASS: TestEarlier (0.00s)
--- SKIP: TestSkipped (0.00s)
>->->OPEN-JSON->->->
{
  "Title": "Once a server is up",
  "File": "/tmp/test.go",
  "Line": 10,
  "Depth": 1,
  "Assertions": []
},{
  "Title": "You can add a job",
  "File": "/tmp/test.go",
  "Line": 20,
  "Depth": 2,
  "Assertions": [
    {
      "File": "/tmp/test.go",
      "Line": 25,
      "Expected": "nil",
      "Actual": "'boom'",
      "Failure": "Expected: nil\nActual:   'boom'",
      "Error": null,
      "StackTrace": "",
      "Skipped": false
    }
  ]
},
<-<-<-CLOSE-JSON<-<-<
    some_test.go:30: helpful t.Log context
--- FAIL: TestKept (0.01s)
FAIL
`

		out := summarizeFailureLog(raw)

		So(out, ShouldContainSubstring, "--- FAIL: TestKept")
		So(out, ShouldContainSubstring, "helpful t.Log context")
		So(out, ShouldContainSubstring, "You can add a job")
		So(out, ShouldNotContainSubstring, "=== RUN")
		So(out, ShouldNotContainSubstring, "=== PAUSE")
		So(out, ShouldNotContainSubstring, "=== CONT")
		So(out, ShouldNotContainSubstring, "--- PASS:")
		So(out, ShouldNotContainSubstring, "--- SKIP:")
	})

	Convey("Failure rendering drops passing-package noise but keeps the failing package", t, func() {
		raw := "=== RUN   TestGood\n--- PASS: TestGood (0.00s)\nPASS\n" +
			"ok  \t" + pkg(summaryTestModule, "good") + "\t0.01s\n" +
			"?   \t" + pkg(summaryTestModule, "notests") + "\t[no test files]\n" +
			"=== RUN   TestBad\n    bad_test.go:9: boom\n--- FAIL: TestBad (0.00s)\nFAIL\n" +
			"FAIL\t" + pkg(summaryTestModule, "bad") + "\t0.02s\n"

		out := summarizeFailureLog(raw)

		So(out, ShouldContainSubstring, "--- FAIL: TestBad")
		So(out, ShouldContainSubstring, "boom")
		So(out, ShouldContainSubstring, "FAIL\t"+pkg(summaryTestModule, "bad"))
		So(out, ShouldNotContainSubstring, "ok  \t"+pkg(summaryTestModule, "good"))
		So(out, ShouldNotContainSubstring, "[no test files]")
		So(out, ShouldNotContainSubstring, "--- PASS:")
		// Long runs of blank lines left by stripped noise are collapsed.
		So(out, ShouldNotContainSubstring, "\n\n\n")
	})

	Convey("The final marker is red FAILED only when colourized", t, func() {
		So(finalMarker(false, false), ShouldEqual, "FAILED\n")
		So(finalMarker(false, true), ShouldEqual, "\x1b[31mFAILED\x1b[0m\n")
		So(finalMarker(true, false), ShouldEqual, "PASSED\n")
		So(finalMarker(true, true), ShouldEqual, "\x1b[32mPASSED\x1b[0m\n")
	})
}

func TestSummarizeLanes(t *testing.T) {
	Convey("A binary lane attributes results to its package", t, func() {
		log := "=== RUN   TestFoo\n--- PASS: TestFoo (0.00s)\n" +
			"=== RUN   TestBar\n--- PASS: TestBar (0.00s)\nPASS\n"

		lanes := []laneSummaryInput{
			{name: "foo", kind: LaneKindBinary, pkg: pkg(summaryTestModule, "container"), log: log},
		}

		out := summarizeLanes(summaryTestModule, lanes, false)

		So(out, ShouldContainSubstring, "container: 2 passed")
		So(out, ShouldContainSubstring, "PASSED")
		So(out, ShouldNotContainSubstring, "FAILED")
	})

	Convey("A SkipConvey scope counts as one skip and lists its description", t, func() {
		log := "=== RUN   TestRunReal\n" +
			skipConveyJSON("DockerRunCmd's command really works",
				"Can't really test the docker command line: docker not found") +
			"--- PASS: TestRunReal (0.00s)\nPASS\n"

		lanes := []laneSummaryInput{
			{name: "container", kind: LaneKindBinary, pkg: pkg(summaryTestModule, "container"), log: log},
		}

		out := summarizeLanes(summaryTestModule, lanes, false)

		So(out, ShouldContainSubstring, "container: 1 passed, 1 skipped")
		So(out, ShouldContainSubstring, "Can't really test the docker command line: docker not found")
		// The skip stack-trace noise must never leak into the summary.
		So(out, ShouldNotContainSubstring, "goroutine 13")
	})

	Convey("Counts and skips from binary lanes sharing a package are merged", t, func() {
		logA := "--- PASS: TestA (0.00s)\nPASS\n"
		logB := "--- PASS: TestB (0.00s)\n" +
			skipConveyJSON("scope", "shared package skip") + "PASS\n"

		lanes := []laneSummaryInput{
			{name: "a", kind: LaneKindBinary, pkg: pkg(summaryTestModule, "jobqueue"), log: logA},
			{name: "b", kind: LaneKindBinary, pkg: pkg(summaryTestModule, "jobqueue"), log: logB},
		}

		out := summarizeLanes(summaryTestModule, lanes, false)

		So(out, ShouldContainSubstring, "jobqueue: 2 passed, 1 skipped")
		So(out, ShouldContainSubstring, "shared package skip")
	})

	Convey("A go-test lane attributes results per package by ok/FAIL/? delimiters", t, func() {
		log := "=== RUN   TestOne\n--- PASS: TestOne (0.00s)\nPASS\n" +
			"ok  \t" + pkg(summaryTestModule, "alpha") + "\t0.01s\n" +
			"=== RUN   TestTwo\n--- PASS: TestTwo (0.00s)\n" +
			skipConveyJSON("scope", "beta only skip") +
			"=== RUN   TestThree\n--- PASS: TestThree (0.00s)\nPASS\n" +
			"ok  \t" + pkg(summaryTestModule, "beta") + "\t0.02s\n" +
			"?   \t" + pkg(summaryTestModule, "gamma") + "\t[no test files]\n"

		lanes := []laneSummaryInput{
			{name: "other", kind: LaneKindGoTest,
				pkgs: []string{pkg(summaryTestModule, "alpha"), pkg(summaryTestModule, "beta")}, log: log},
		}

		out := summarizeLanes(summaryTestModule, lanes, false)

		So(out, ShouldContainSubstring, "alpha: 1 passed")
		So(out, ShouldContainSubstring, "beta: 2 passed, 1 skipped")
		So(out, ShouldContainSubstring, "beta only skip")
		// A package with no tests is not reported.
		So(out, ShouldNotContainSubstring, "gamma")
		// alpha's single behaviour must not bleed into beta's segment.
		So(out, ShouldNotContainSubstring, "alpha: 1 passed, 1 skipped")
	})

	Convey("A t.Skip function counts as a skip and lists its reason", t, func() {
		log := "=== RUN   TestSkippy\n    s_test.go:3: not on this platform\n" +
			"--- SKIP: TestSkippy (0.00s)\n" +
			"=== RUN   TestOK\n--- PASS: TestOK (0.00s)\nPASS\n"

		lanes := []laneSummaryInput{
			{name: "pkg", kind: LaneKindBinary, pkg: pkg(summaryTestModule, "delta"), log: log},
		}

		out := summarizeLanes(summaryTestModule, lanes, false)

		So(out, ShouldContainSubstring, "delta: 1 passed, 1 skipped")
		So(out, ShouldContainSubstring, "not on this platform")
	})

	Convey("Repeated skip descriptions are deduped with a count", t, func() {
		log := skipConveyJSON("scopeA", "same reason") +
			skipConveyJSON("scopeB", "same reason") + "PASS\n"

		lanes := []laneSummaryInput{
			{name: "pkg", kind: LaneKindBinary, pkg: pkg(summaryTestModule, "epsilon"), log: log},
		}

		out := summarizeLanes(summaryTestModule, lanes, false)

		So(out, ShouldContainSubstring, "epsilon: 0 passed, 2 skipped")
		So(out, ShouldContainSubstring, "same reason (x2)")
	})

	Convey("Subtest PASS/SKIP lines are not counted at the top level", t, func() {
		log := "=== RUN   TestParent\n--- PASS: TestParent (0.00s)\n" +
			"    --- PASS: TestParent/sub (0.00s)\n" +
			"    --- SKIP: TestParent/skipped (0.00s)\nPASS\n"

		lanes := []laneSummaryInput{
			{name: "pkg", kind: LaneKindBinary, pkg: pkg(summaryTestModule, "zeta"), log: log},
		}

		out := summarizeLanes(summaryTestModule, lanes, false)

		So(out, ShouldContainSubstring, "zeta: 1 passed")
		So(out, ShouldNotContainSubstring, "zeta: 1 passed, 1 skipped")
	})

	Convey("A grand total line summarises all packages", t, func() {
		lanes := []laneSummaryInput{
			{name: "a", kind: LaneKindBinary, pkg: pkg(summaryTestModule, "alpha"),
				log: "--- PASS: TestA (0.00s)\nPASS\n"},
			{name: "b", kind: LaneKindBinary, pkg: pkg(summaryTestModule, "beta"),
				log: "--- PASS: TestB (0.00s)\n" + skipConveyJSON("s", "a skip") + "PASS\n"},
		}

		out := summarizeLanes(summaryTestModule, lanes, false)

		So(out, ShouldContainSubstring, "2 passed")
		So(out, ShouldContainSubstring, "1 skipped")
		So(out, ShouldContainSubstring, "2 packages")
	})

	Convey("Colourize wraps PASSED in green only when requested", t, func() {
		lanes := []laneSummaryInput{
			{name: "a", kind: LaneKindBinary, pkg: pkg(summaryTestModule, "alpha"),
				log: "--- PASS: TestA (0.00s)\nPASS\n"},
		}

		plain := summarizeLanes(summaryTestModule, lanes, false)
		So(plain, ShouldNotContainSubstring, "\x1b[")
		So(plain, ShouldContainSubstring, "PASSED")

		coloured := summarizeLanes(summaryTestModule, lanes, true)
		So(coloured, ShouldContainSubstring, "\x1b[32mPASSED\x1b[0m")
	})

	Convey("Packages are listed in sorted order", t, func() {
		lanes := []laneSummaryInput{
			{name: "z", kind: LaneKindBinary, pkg: pkg(summaryTestModule, "zeta"),
				log: "--- PASS: TestZ (0.00s)\nPASS\n"},
			{name: "a", kind: LaneKindBinary, pkg: pkg(summaryTestModule, "alpha"),
				log: "--- PASS: TestA (0.00s)\nPASS\n"},
		}

		out := summarizeLanes(summaryTestModule, lanes, false)

		So(strings.Index(out, "alpha:"), ShouldBeLessThan, strings.Index(out, "zeta:"))
	})
}

func skipConveyJSON(parentTitle string, skipTitle string) string {
	return `>->->OPEN-JSON->->->
{
  "Title": "` + parentTitle + `",
  "File": "/x/foo_test.go",
  "Line": 10,
  "Depth": 1,
  "Assertions": [],
  "Output": ""
},{
  "Title": "` + skipTitle + `",
  "File": "/x/foo_test.go",
  "Line": 12,
  "Depth": 2,
  "Assertions": [
    {
      "File": "/x/foo_test.go",
      "Line": 12,
      "Expected": "",
      "Actual": "",
      "Failure": "",
      "Error": null,
      "StackTrace": "goroutine 13 [running]:\nlots\nof\nnoise",
      "Skipped": true
    }
  ],
  "Output": ""
},
<-<-<-CLOSE-JSON<-<-<
`
}
