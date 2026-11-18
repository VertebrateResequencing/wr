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

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	. "github.com/smartystreets/goconvey/convey"
)

const (
	testThreshold = 10
	testAlpha     = 0.05
	addManyNorc   = "AddManyUnderReserveLoad/norc-8"
	addManyRc     = "AddManyUnderReserveLoad/rc-8"
)

func TestRawRuleKnowsWhichWayIsBetter(t *testing.T) {
	Convey("Given rates and a throughput factor, which are better higher, and a time, which is better lower", t, func() {
		csv := ",base,,head,,,\n" +
			",jobs/s,CI,jobs/s,CI,vs base,P\n" +
			"ScenarioReportStorm,100,1%,80,1%,-20.00%,p=0.002 n=6\n" +
			"ScenarioOther,100,1%,120,1%,+20.00%,p=0.002 n=6\n" +
			"\n,base,,head,,,\n" +
			",throughput-factor,CI,throughput-factor,CI,vs base,P\n" +
			"ScenarioArchiveCeiling,40,1%,30,1%,-25.00%,p=0.002 n=6\n" +
			"\n,base,,head,,,\n" +
			",wall-sec,CI,wall-sec,CI,vs base,P\n" +
			"ScenarioReportStorm,10,1%,8,1%,-20.00%,p=0.002 n=6\n"

		var out strings.Builder

		So(run(strings.NewReader(csv), &out, testThreshold, testAlpha), ShouldBeNil)

		Convey("only the falls in rate and factor regress", func() {
			So(out.String(), ShouldEqual,
				"REGRESSION ScenarioReportStorm jobs/s: -20.00% (p=0.002)\n"+
					"REGRESSION ScenarioArchiveCeiling throughput-factor: -25.00% (p=0.002)\n")
		})
	})
}

func TestNoNoteForABenchmarkOneTreeLacks(t *testing.T) {
	for _, tree := range []string{"base", "head"} {
		Convey("Given an under-load benchmark only "+tree+" ran", t, func() {
			csv := "," + tree + ",\n,reserve-p99-us,CI\nAddManyUnderReserveLoad/norc-8,150,4%\n"

			var out strings.Builder

			So(run(strings.NewReader(csv), &out, testThreshold, testAlpha), ShouldBeNil)

			Convey("nothing is compared, so nothing is judged or noted", func() {
				So(out.String(), ShouldBeEmpty)
			})
		})
	}
}

func TestOneTreeBenchmarksInASharedTable(t *testing.T) {
	Convey("Given a sec/op table holding a head-only, a base-only and a slower compared benchmark", t, func() {
		lines := judgeCase("one-tree-benchmarks")

		Convey("only the benchmark both ran regresses", func() {
			So(withPrefix(lines, "REGRESSION "), ShouldResemble, []string{
				"REGRESSION Same-8 sec/op: +25.00% (p=0.002)",
			})
		})
	})
}

func TestChangeFromZeroIsReadable(t *testing.T) {
	Convey("Given a figure that rises from 0 to a value with float noise", t, func() {
		var out strings.Builder

		So(run(strings.NewReader("pkg: x\n"+benchstatTable("X-8", "sec/op", "0", "2.3499999999999996", "p=0.002 n=6")),
			&out, testThreshold, testAlpha), ShouldBeNil)

		Convey("its regression shows the value to 4 significant figures", func() {
			So(out.String(), ShouldEqual, "REGRESSION X-8 sec/op: from 0 to 2.35 (p=0.002)\n")
		})
	})
}

func TestRunRejectsInputThatIsNotBenchstatCSV(t *testing.T) {
	Convey("A row before any table header is an error, not a silent pass", t, func() {
		var out strings.Builder

		err := run(strings.NewReader("pkg: x\nKickUnderReserveLoad-8,1,2%,2,2%,+100%,p=0.002 n=6\n"),
			&out, testThreshold, testAlpha)
		So(err, ShouldNotBeNil)
	})

	Convey("A value that is not a number is an error, not a skipped figure", t, func() {
		var out strings.Builder

		err := run(strings.NewReader("pkg: x\n"+benchstatTable("X-8", "sec/op", "abc", "2", "p=0.002 n=6")),
			&out, testThreshold, testAlpha)
		So(err, ShouldNotBeNil)
	})

	Convey("A p-value that is not a number is an error, not an uncompared figure", t, func() {
		var out strings.Builder

		err := run(strings.NewReader("pkg: x\n"+benchstatTable("X-8", "sec/op", "1", "2", "p=abc n=6")),
			&out, testThreshold, testAlpha)
		So(err, ShouldNotBeNil)
	})
}

func TestBenchmarksAreTheirPackages(t *testing.T) {
	Convey("Given two packages with a benchmark of the same name, only one with the window figures", t, func() {
		table := func(unit, base, head string) string {
			return benchstatTable("Load-8", unit, base, head, "p=0.002 n=6")
		}
		windowed := "pkg: a\n" + table("sec/op", "1", "1") + table("slow-reserves/op", "10", "10") +
			table("max-qlock-us", "10", "10") + table("slow-reserve-p50-us", "10", "10") +
			table("reserve-p50-us", "10", "10") + table("reserve-p99-us", "10", "20")
		raw := "pkg: b\n" + table("reserve-p99-us", "10", "20")

		var out strings.Builder

		So(run(strings.NewReader(windowed+raw), &out, testThreshold, testAlpha), ShouldBeNil)

		Convey("each is judged by the rule its own figures allow", func() {
			lines := strings.Split(out.String(), "\n")
			So(withPrefix(lines, "REGRESSION "), ShouldResemble, []string{
				"REGRESSION Load-8 reserve-p99-us: +100.00% (p=0.002)",
			})
			So(withPrefix(lines, "WINDOW "), ShouldResemble, []string{"WINDOW Load-8: PASS"})
			So(withPrefix(lines, "NOTE "), ShouldHaveLength, 1)
		})
	})
}

func TestWindowRuleIgnoresAShrunkWindow(t *testing.T) {
	Convey("Given an add that runs twice as fast, so its window's raw reserve-p99 rises 20%", t, func() {
		lines := judgeCase("window-shrank")

		Convey("nothing regresses, and the raw tail percentiles are reported as informational", func() {
			So(withPrefix(lines, "REGRESSION "), ShouldBeEmpty)
			So(windowBlock(lines, addManyNorc), ShouldResemble, []string{
				"WINDOW " + addManyNorc + ": PASS",
				"  (a) operation duration, sec/op: -48.00% (p=0.002) ok",
				"  (b) slow reserves per op, slow-reserves/op: +5.00% (p=0.515) ok",
				"  (c) longest queue-lock hold, max-qlock-us: -3.00% (p=0.071) ok",
				"  (d) median slow-reserve wait, slow-reserve-p50-us: -3.00% (p=0.071) ok",
				"  (e) median reserve wait, reserve-p50-us: +0.00% (p=1.000) ok",
				"  informational, reserve-p99-us: +20.00% (p=0.002)",
				"  informational, reserve-max-us: +15.00% (p=0.002)",
				"  informational, reserves/op: -40.00% (p=0.002)",
			})
		})
	})

	Convey("Given the instrumented 1g.4 adds (review round 2 of rl-addmany-tail.txt), "+
		"which have no slow-reserve wait", t, func() {
		lines := judgeCase("1g4-addmany-tail")

		Convey("(a) to (c) pass on both, the raw p99 rise is informational, and the missing (d) and (e) fail", func() {
			for _, bench := range []string{addManyNorc, addManyRc} {
				block := windowBlock(lines, bench)
				So(block, ShouldNotBeEmpty)
				So(block[0], ShouldEqual, "WINDOW "+bench+": FAIL (d) (e)")
				So(strings.Join(block, "\n"), ShouldContainSubstring, "  (a) operation duration, sec/op: -")
				So(strings.Join(block, "\n"), ShouldNotContainSubstring, ") REGRESSION\n")
			}

			So(windowBlock(lines, addManyNorc), ShouldContain,
				"  informational, reserve-p99-us: +11.28% (p=0.029)")
			So(windowBlock(lines, addManyRc), ShouldContain,
				"  informational, reserve-p99-us: +22.51% (p=0.029)")
			So(withPrefix(lines, "REGRESSION "), ShouldResemble, []string{
				"REGRESSION " + addManyNorc + " (d) median slow-reserve wait, slow-reserve-p50-us: not measured on base or head",
				"REGRESSION " + addManyNorc + " (e) median reserve wait, reserve-p50-us: not measured on base or head",
				"REGRESSION " + addManyRc + " (d) median slow-reserve wait, slow-reserve-p50-us: not measured on base or head",
				"REGRESSION " + addManyRc + " (e) median reserve wait, reserve-p50-us: not measured on base or head",
			})
		})
	})
}

func TestWindowRuleFailsEachCriterionAlone(t *testing.T) {
	for _, c := range []struct{ name, criterion, want string }{
		{"slower-op", "(a)", "REGRESSION " + addManyNorc + " (a) operation duration, sec/op: +25.00% (p=0.002)"},
		{"more-slow-reserves", "(b)", "REGRESSION " + addManyNorc +
			" (b) slow reserves per op, slow-reserves/op: +60.00% (p=0.002)"},
		{"longer-hold", "(c)", "REGRESSION " + addManyNorc + " (c) longest queue-lock hold, max-qlock-us: +40.00% (p=0.002)"},
		{"longer-slow-waits", "(d)", "REGRESSION " + addManyNorc +
			" (d) median slow-reserve wait, slow-reserve-p50-us: +50.00% (p=0.002)"},
		{"slower-median", "(e)", "REGRESSION " + addManyNorc + " (e) median reserve wait, reserve-p50-us: +30.00% (p=0.002)"},
	} {
		Convey("Given a head whose only worse judged figure is "+c.criterion+" ("+c.name+")", t, func() {
			lines := judgeCase(c.name)

			Convey("it regresses on that criterion alone, and says which", func() {
				So(withPrefix(lines, "REGRESSION "), ShouldResemble, []string{c.want})

				block := windowBlock(lines, addManyNorc)
				So(block, ShouldNotBeEmpty)
				So(block[0], ShouldEqual, "WINDOW "+addManyNorc+": FAIL "+c.criterion)
			})
		})
	}

	Convey("Given a base with no reserve over the threshold and a head with some", t, func() {
		lines := judgeCase("slow-reserves-from-none")

		Convey("(b) and (d) regress from 0", func() {
			So(withPrefix(lines, "REGRESSION "), ShouldResemble, []string{
				"REGRESSION " + addManyNorc + " (b) slow reserves per op, slow-reserves/op: from 0 to 2.05 (p=0.002)",
				"REGRESSION " + addManyNorc + " (d) median slow-reserve wait, slow-reserve-p50-us: from 0 to 1515 (p=0.002)",
			})
		})
	})

	Convey("Given both trees report slow reserves but base lacks the lock hold", t, func() {
		lines := judgeCase("base-without-hold")

		Convey("(c) fails as not measured on base", func() {
			So(withPrefix(lines, "REGRESSION "), ShouldResemble, []string{
				"REGRESSION " + addManyNorc + " (c) longest queue-lock hold, max-qlock-us: not measured on base",
			})
			So(windowBlock(lines, addManyNorc)[0], ShouldEqual, "WINDOW "+addManyNorc+": FAIL (c)")
		})
	})

	Convey("Given both trees report a criterion's figure but benchstat gives no p-value for it", t, func() {
		csv := "pkg: a\n" + benchstatTable("Load-8", "sec/op", "1", "1", "p=1.000 n=6") +
			benchstatTable("Load-8", "slow-reserves/op", "10", "10", "p=1.000 n=6") +
			benchstatTable("Load-8", "max-qlock-us", "10", "10", "") +
			benchstatTable("Load-8", "slow-reserve-p50-us", "10", "10", "p=1.000 n=6") +
			benchstatTable("Load-8", "reserve-p50-us", "10", "10", "p=1.000 n=6") +
			",base,\n,reserve-max-us,CI\nLoad-8,10,1%\n"

		var out strings.Builder

		So(run(strings.NewReader(csv), &out, testThreshold, testAlpha), ShouldBeNil)

		Convey("that criterion fails as not compared, and a tail figure only base has is not reported", func() {
			So(out.String(), ShouldEqual,
				"REGRESSION Load-8 (c) longest queue-lock hold, max-qlock-us: not compared\n\n"+
					"WINDOW Load-8: FAIL (c)\n"+
					"  (a) operation duration, sec/op: +0.00% (p=1.000) ok\n"+
					"  (b) slow reserves per op, slow-reserves/op: +0.00% (p=1.000) ok\n"+
					"  (c) longest queue-lock hold, max-qlock-us: not compared REGRESSION\n"+
					"  (d) median slow-reserve wait, slow-reserve-p50-us: +0.00% (p=1.000) ok\n"+
					"  (e) median reserve wait, reserve-p50-us: +0.00% (p=1.000) ok\n")
		})
	})

	Convey("Given both trees report slow reserves but head lacks their wait", t, func() {
		lines := judgeCase("head-without-slow-wait")

		Convey("(d) fails as not measured on head", func() {
			So(withPrefix(lines, "REGRESSION "), ShouldResemble, []string{
				"REGRESSION " + addManyNorc + " (d) median slow-reserve wait, slow-reserve-p50-us: not measured on head",
			})
		})
	})
}

func TestWindowRuleLeavesOtherFiguresToTheRawRule(t *testing.T) {
	Convey("Given a benchmark under the window-aware rule whose allocations rise 30%", t, func() {
		lines := judgeCase("more-allocs")

		Convey("the allocations regress by the raw rule, and the window rule passes", func() {
			So(withPrefix(lines, "REGRESSION "), ShouldResemble, []string{
				"REGRESSION " + addManyNorc + " allocs/op: +30.00% (p=0.002)",
			})
			So(windowBlock(lines, addManyNorc)[0], ShouldEqual, "WINDOW "+addManyNorc+": PASS")
		})
	})
}

func TestWindowRuleOn1g4UnderLoadRuns(t *testing.T) {
	// The recorded 1g.4 trees predate the window figures, so these give each
	// under-load benchmark (b) to (d) unchanged on both trees, leaving (a) and
	// (e) to decide it from the recorded figures.
	Convey("Given 1g.4's fix round 4, whose per-item ops made every reserve wait several times longer", t, func() {
		lines := judgeWindowed1g4("1g4-fix4")

		Convey("Kick, Resume and Suspend fail (e) alone, and AddMany passes", func() {
			So(windowBlock(lines, "KickUnderReserveLoad-8"), ShouldResemble, []string{
				"WINDOW KickUnderReserveLoad-8: FAIL (e)",
				"  (a) operation duration, sec/op: -73.80% (p=0.002) ok",
				"  (b) slow reserves per op, slow-reserves/op: +0.00% (p=1.000) ok",
				"  (c) longest queue-lock hold, max-qlock-us: +0.00% (p=1.000) ok",
				"  (d) median slow-reserve wait, slow-reserve-p50-us: +0.00% (p=1.000) ok",
				"  (e) median reserve wait, reserve-p50-us: +678.20% (p=0.002) REGRESSION",
				"  informational, reserve-p99-us: +121.47% (p=0.002)",
				"  informational, reserve-max-us: -21.38% (p=0.589)",
				"  informational, reserves/op: -83.26% (p=0.002)",
			})

			for _, bench := range []string{"KickUnderReserveLoad-8", "ResumeUnderReserveLoad-8",
				"SuspendUnderReserveLoad-8"} {
				So(windowBlock(lines, bench)[0], ShouldEqual, "WINDOW "+bench+": FAIL (e)")
			}

			for _, bench := range []string{addManyNorc, addManyRc} {
				So(windowBlock(lines, bench)[0], ShouldEqual, "WINDOW "+bench+": PASS")
			}

			So(withPrefix(lines, "REGRESSION "), ShouldResemble, []string{
				"REGRESSION KickUnderReserveLoad-8 allocs/op: +11.45% (p=0.041)",
				"REGRESSION KickUnderReserveLoad-8 (e) median reserve wait, reserve-p50-us: +678.20% (p=0.002)",
				"REGRESSION ResumeUnderReserveLoad-8 (e) median reserve wait, reserve-p50-us: +244.21% (p=0.002)",
				"REGRESSION SuspendUnderReserveLoad-8 (e) median reserve wait, reserve-p50-us: +505.51% (p=0.002)",
			})
		})
	})

	Convey("Given 1g.4's final reserve-lock round, whose median reserve waits did not rise", t, func() {
		lines := judgeWindowed1g4("1g4-rl-final")

		Convey("every under-load benchmark passes", func() {
			So(withPrefix(lines, "WINDOW "), ShouldResemble, []string{
				"WINDOW KickUnderReserveLoad-8: PASS",
				"WINDOW ResumeUnderReserveLoad-8: PASS",
				"WINDOW SuspendUnderReserveLoad-8: PASS",
				"WINDOW " + addManyNorc + ": PASS",
				"WINDOW " + addManyRc + ": PASS",
			})
		})
	})
}

// judgeWindowed1g4 judges testdata/<name>/benchstat.csv with, appended, its
// under-load benchmarks' (b) to (d) figures, the same on both trees.
func judgeWindowed1g4(name string) []string {
	b, err := os.ReadFile(filepath.Join("testdata", name, "benchstat.csv"))
	So(err, ShouldBeNil)

	var csv strings.Builder

	csv.Write(b)
	csv.WriteString("\npkg: github.com/VertebrateResequencing/wr/jobqueue\n")

	for _, unit := range []string{"slow-reserves/op", "max-qlock-us", "slow-reserve-p50-us"} {
		for _, bench := range []string{"KickUnderReserveLoad-8", "ResumeUnderReserveLoad-8",
			"SuspendUnderReserveLoad-8", addManyNorc, addManyRc} {
			csv.WriteString(benchstatTable(bench, unit, "0", "0", "p=1.000 n=6"))
		}
	}

	var out strings.Builder

	So(run(strings.NewReader(csv.String()), &out, testThreshold, testAlpha), ShouldBeNil)

	return strings.Split(strings.TrimRight(out.String(), "\n"), "\n")
}

func TestRuleBoundaries(t *testing.T) {
	Convey("Given a figure exactly the threshold worse at p well below alpha", t, func() {
		csv := benchstatTable("Load-8", "sec/op", "4", "5", "p=0.002 n=6")

		Convey("it does not regress at that threshold, but does just below it", func() {
			var out strings.Builder

			So(run(strings.NewReader(csv), &out, 25, testAlpha), ShouldBeNil)
			So(out.String(), ShouldBeEmpty)

			out.Reset()
			So(run(strings.NewReader(csv), &out, 24.9, testAlpha), ShouldBeNil)
			So(out.String(), ShouldEqual, "REGRESSION Load-8 sec/op: +25.00% (p=0.002)\n")
		})
	})

	Convey("Given a figure well beyond the threshold at p exactly alpha", t, func() {
		csv := benchstatTable("Load-8", "sec/op", "4", "8", "p=0.050 n=6")

		Convey("it does not regress at that alpha, but does just above it", func() {
			var out strings.Builder

			So(run(strings.NewReader(csv), &out, testThreshold, 0.05), ShouldBeNil)
			So(out.String(), ShouldBeEmpty)

			out.Reset()
			So(run(strings.NewReader(csv), &out, testThreshold, 0.051), ShouldBeNil)
			So(out.String(), ShouldEqual, "REGRESSION Load-8 sec/op: +100.00% (p=0.050)\n")
		})
	})
}

func TestRawRuleWithoutWindowFigures(t *testing.T) {
	Convey("Given a base with the window figures, against a head from before them", t, func() {
		lines := judgeCase("head-without-window")

		Convey("the benchmark is judged by its raw figures, with a note saying head lacks them", func() {
			So(withPrefix(lines, "REGRESSION "), ShouldBeEmpty)
			So(withPrefix(lines, "WINDOW "), ShouldBeEmpty)

			notes := withPrefix(lines, "NOTE ")
			So(notes, ShouldHaveLength, 1)
			So(notes[0], ShouldStartWith, "NOTE "+addManyNorc+": head does not report slow-reserves/op")
		})
	})

	Convey("Given a base from before the window figures, against a head with them", t, func() {
		lines := judgeCase("base-without-window")

		Convey("the benchmark is judged by its raw figures, p99 included, with a note saying why", func() {
			So(withPrefix(lines, "REGRESSION "), ShouldResemble, []string{
				"REGRESSION " + addManyNorc + " reserve-p99-us: +20.00% (p=0.002)",
			})
			So(withPrefix(lines, "WINDOW "), ShouldBeEmpty)

			notes := withPrefix(lines, "NOTE ")
			So(notes, ShouldHaveLength, 1)
			So(notes[0], ShouldStartWith, "NOTE "+addManyNorc+": base does not report slow-reserves/op")
		})
	})

	for _, name := range []string{"1g4-rl", "1g4-rl-r1", "1g4-rl-final", "1g4-rl-r2", "1g4-fix4"} {
		Convey("Given the recorded 1g.4 run "+name+", whose trees predate the window figures", t, func() {
			lines := judgeCase(name)

			Convey("its regressions are exactly those speed.sh recorded, and each under-load benchmark has a note", func() {
				So(withPrefix(lines, "REGRESSION "), ShouldResemble, recordedRegressions(name))
				So(withPrefix(lines, "NOTE "), ShouldHaveLength, 5)
				So(withPrefix(lines, "NOTE ")[0], ShouldStartWith,
					"NOTE KickUnderReserveLoad-8: neither tree reports slow-reserves/op")
			})
		})
	}

	for _, name := range []string{"1g4-run2-speed-full", "1g4-run3-scenarios"} {
		Convey("Given the recorded 1g.4 run "+name+", with every other benchmark and scenario", t, func() {
			lines := judgeCase(name)

			Convey("its regressions are exactly those speed.sh recorded, with no window rule or note", func() {
				So(withPrefix(lines, "REGRESSION "), ShouldResemble, recordedRegressions(name))
				So(withPrefix(lines, "WINDOW "), ShouldBeEmpty)
				So(withPrefix(lines, "NOTE "), ShouldBeEmpty)
			})
		})
	}

	Convey("Given 1g.4's recorded runs", t, func() {
		Convey("a lower threshold or a higher alpha changes what fails", func() {
			So(withPrefix(judgeCaseAt("1g4-run3-scenarios", 20, testAlpha), "REGRESSION "), ShouldResemble, []string{
				"REGRESSION ScenarioDepGranularity add-sec: +620.79% (p=0.002)",
			})
			So(withPrefix(judgeCaseAt("1g4-run3-scenarios", testThreshold, 0.5), "REGRESSION "), ShouldResemble, []string{
				"REGRESSION ScenarioDepGranularity peak-rss-B: +18.38% (p=0.002)",
				"REGRESSION ScenarioDepGranularity recovery-sec: +10.03% (p=0.236)",
				"REGRESSION ScenarioDepGranularity add-sec: +620.79% (p=0.002)",
			})
		})
	})
}

// judgeCase runs the judge over testdata/<name>/benchstat.csv at speed.sh's
// default threshold and alpha, and returns its output lines.
func judgeCase(name string) []string {
	return judgeCaseAt(name, testThreshold, testAlpha)
}

// windowBlock returns the WINDOW line for bench and the indented lines after
// it.
func windowBlock(lines []string, bench string) []string {
	for i, l := range lines {
		if !strings.HasPrefix(l, "WINDOW "+bench+": ") {
			continue
		}

		block := []string{l}

		for _, next := range lines[i+1:] {
			if !strings.HasPrefix(next, "  ") {
				break
			}

			block = append(block, next)
		}

		return block
	}

	return nil
}

// benchstatTable returns a benchstat CSV table of one unit comparing base and head
// for bench, with the given last (p-value) column.
func benchstatTable(bench, unit, base, head, p string) string {
	return ",base,,head,,,\n," + unit + ",CI," + unit + ",CI,vs base,P\n" +
		bench + "," + base + ",1%," + head + ",1%,~," + p + "\n\n"
}

// recordedRegressions returns the REGRESSION lines speed.sh's awk rule wrote
// for testdata/<name>.
func recordedRegressions(name string) []string {
	b, err := os.ReadFile(filepath.Join("testdata", name, "regressions.txt"))
	So(err, ShouldBeNil)

	return withPrefix(strings.Split(string(b), "\n"), "REGRESSION ")
}

// withPrefix returns the lines that start with prefix.
func withPrefix(lines []string, prefix string) []string {
	var got []string

	for _, l := range lines {
		if strings.HasPrefix(l, prefix) {
			got = append(got, l)
		}
	}

	return got
}

func judgeCaseAt(name string, threshold, alpha float64) []string {
	f, err := os.Open(filepath.Join("testdata", name, "benchstat.csv"))
	So(err, ShouldBeNil)

	defer f.Close()

	var out strings.Builder

	So(run(f, &out, threshold, alpha), ShouldBeNil)

	return strings.Split(strings.TrimRight(out.String(), "\n"), "\n")
}
