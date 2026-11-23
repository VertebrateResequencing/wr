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

//nolint:goconst // Test cases repeat (d)'s thresholds to document each count's verdict.
package main

import (
	"bytes"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	. "github.com/smartystreets/goconvey/convey"
)

const (
	testThreshold = 10
	testAlpha     = 0.05
	addManyNorc   = "AddManyUnderReserveLoad/norc-8"
	addManyRc     = "AddManyUnderReserveLoad/rc-8"
	kickBench     = "KickUnderReserveLoad-8"
	resumeBench   = "ResumeUnderReserveLoad-8"

	unchanged = "+0.00% (p=1.000) ok"
	allGone   = "-100.00% (p=0.002) ok"
	// sparseGone is a count that falls to 0 from a few rounds' worth.
	sparseGone = "-100.00% (p=0.182) ok"
)

// tailMs are the thresholds, in ms, of (d)'s counts of reserves over them.
//
//nolint:gochecknoglobals // A fixed table.
var tailMs = []string{
	"1.25", "1.5", "2", "2.5", "3", "4", "5", "6", "8", "10", "12", "16", "20", "24", "32", "40", "48", "64",
}

// tolerantMs are 1.1 times each of tailMs: the thresholds over which head's
// reserves count against base's over tailMs.
//
//nolint:gochecknoglobals // A fixed table.
var tolerantMs = map[string]string{
	"1.25": "1.375", "1.5": "1.65", "2": "2.2", "2.5": "2.75", "3": "3.3", "4": "4.4", "5": "5.5", "6": "6.6",
	"8": "8.8", "10": "11", "12": "13.2", "16": "17.6", "20": "22", "24": "26.4", "32": "35.2", "40": "44",
	"48": "52.8", "64": "70.4",
}

// tailUnits are the units of (d)'s counts.
//
//nolint:gochecknoglobals // A fixed table.
var tailUnits = func() []string {
	units := make([]string, len(tailMs))
	for i, ms := range tailMs {
		units[i] = tailUnit(ms)
	}

	return units
}()

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

		So(run(strings.NewReader(csv), noTolerant(), &out, testThreshold, testAlpha), ShouldBeNil)

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

			So(run(strings.NewReader(csv), noTolerant(), &out, testThreshold, testAlpha), ShouldBeNil)

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
			noTolerant(), &out, testThreshold, testAlpha), ShouldBeNil)

		Convey("its regression shows the value to 4 significant figures", func() {
			So(out.String(), ShouldEqual, "REGRESSION X-8 sec/op: from 0 to 2.35 (p=0.002)\n")
		})
	})
}

func TestRunRejectsInputThatIsNotBenchstatCSV(t *testing.T) {
	Convey("A row before any table header is an error, not a silent pass", t, func() {
		var out strings.Builder

		err := run(strings.NewReader("pkg: x\nKickUnderReserveLoad-8,1,2%,2,2%,+100%,p=0.002 n=6\n"),
			noTolerant(), &out, testThreshold, testAlpha)
		So(err, ShouldNotBeNil)
	})

	Convey("A value that is not a number is an error, not a skipped figure", t, func() {
		var out strings.Builder

		err := run(strings.NewReader("pkg: x\n"+benchstatTable("X-8", "sec/op", "abc", "2", "p=0.002 n=6")),
			noTolerant(), &out, testThreshold, testAlpha)
		So(err, ShouldNotBeNil)
	})

	Convey("A p-value that is not a number is an error, not an uncompared figure", t, func() {
		var out strings.Builder

		err := run(strings.NewReader("pkg: x\n"+benchstatTable("X-8", "sec/op", "1", "2", "p=abc n=6")),
			noTolerant(), &out, testThreshold, testAlpha)
		So(err, ShouldNotBeNil)
	})
}

func TestBenchmarksAreTheirPackages(t *testing.T) {
	Convey("Given two packages with a benchmark of the same name, only one with the window figures", t, func() {
		table := func(unit, base, head string) string {
			return benchstatTable("Load-8", unit, base, head, "p=0.002 n=6")
		}

		var windowed strings.Builder

		windowed.WriteString("pkg: a\n" + table("sec/op", "1", "1") + table("slow-reserves/op", "10", "10") +
			table("max-qlock-us", "10", "10") + table("slow-reserve-wait-us/op", "10", "10") +
			table("reserve-p50-us", "10", "10") + table("reserve-p99-us", "10", "20"))

		var tolerant strings.Builder

		tolerant.WriteString("pkg: a\n")

		for _, unit := range tailUnits {
			tolerant.WriteString(table(unit, "1", "1"))
		}

		raw := "pkg: b\n" + table("reserve-p99-us", "10", "20")

		var out strings.Builder

		So(run(strings.NewReader(windowed.String()+raw), strings.NewReader(tolerant.String()), &out,
			testThreshold, testAlpha), ShouldBeNil)

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

			block := windowBlock(lines, addManyNorc)
			So(block, ShouldHaveLength, 5+len(tailMs)+5)
			So(slices.Concat(block[:5], block[5+len(tailMs):]), ShouldResemble, []string{
				"WINDOW " + addManyNorc + ": PASS",
				"  (a) operation duration, sec/op: -48.00% (p=0.002) ok",
				"  (b) slow reserves per op, slow-reserves/op: +5.00% (p=0.515) ok",
				"  (c) longest queue-lock hold, max-qlock-us: -3.00% (p=0.071) ok",
				"  (d) total slow-reserve wait, slow-reserve-wait-us/op: +2.00% (p=0.699) ok",
				"  (e) median reserve wait, reserve-p50-us: +0.00% (p=1.000) ok",
				"  informational, reserve-p99-us: +20.00% (p=0.002)",
				"  informational, reserve-max-us: +15.00% (p=0.002)",
				"  informational, reserves/op: -40.00% (p=0.002)",
				"  informational, slow-reserve-p50-us: -3.00% (p=0.071)",
			})
		})

		Convey("its unchanged waits give counts over 1.1 times each threshold no higher than base's over it", func() {
			block := windowBlock(lines, addManyNorc)
			So(block[5:5+len(tailMs)], ShouldResemble, tailLines(map[string]string{
				"1.25": "-21.84% (p=0.015) ok",
				"1.5":  "-21.84% (p=0.015) ok",
				"2":    "-13.74% (p=0.132) ok",
				"2.5":  "-13.73% (p=0.132) ok",
				"3":    "-20.52% (p=0.015) ok",
				"4":    "-15.04% (p=0.065) ok",
				"5":    "-15.03% (p=0.065) ok",
				"6":    "-14.43% (p=0.093) ok",
				"8":    "-11.32% (p=0.180) ok",
				"10":   "-11.29% (p=0.180) ok",
				"12":   "-12.60% (p=0.132) ok",
				"16":   allGone,
			}))
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

			want := make([]string, 0, 2*(len(tailMs)+2))

			for _, bench := range []string{addManyNorc, addManyRc} {
				want = append(want, "REGRESSION "+bench+
					" (d) total slow-reserve wait, slow-reserve-wait-us/op: not measured on base or head")

				for _, ms := range tailMs {
					want = append(want, "REGRESSION "+bench+" (d) "+tailWhat(ms)+", "+tailUnit(ms)+
						": not measured on base or head")
				}

				want = append(want, "REGRESSION "+bench+
					" (e) median reserve wait, reserve-p50-us: not measured on base or head")
			}

			So(withPrefix(lines, "REGRESSION "), ShouldResemble, want)
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
			" (d) total slow-reserve wait, slow-reserve-wait-us/op: +50.00% (p=0.002)"},
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

		Convey("(b) and (d)'s total and counts up to 2ms regress from 0", func() {
			So(withPrefix(lines, "REGRESSION "), ShouldResemble, []string{
				"REGRESSION " + addManyNorc + " (b) slow reserves per op, slow-reserves/op: from 0 to 2.05 (p=0.002)",
				"REGRESSION " + addManyNorc + " (d) total slow-reserve wait, slow-reserve-wait-us/op: from 0 to 3075 (p=0.002)",
				tailRegression(addManyNorc, "1.25", "from 0 to 0.5125 (p=0.002)"),
				tailRegression(addManyNorc, "1.5", "from 0 to 0.5125 (p=0.002)"),
				tailRegression(addManyNorc, "2", "from 0 to 0.5125 (p=0.002)"),
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
			benchstatTable("Load-8", "slow-reserve-wait-us/op", "10", "10", "p=1.000 n=6") +
			benchstatTable("Load-8", "reserve-p50-us", "10", "10", "p=1.000 n=6") +
			",base,\n,reserve-max-us,CI\nLoad-8,10,1%\n\n"

		var tolerant strings.Builder

		tolerant.WriteString("pkg: a\n")

		for _, unit := range tailUnits {
			tolerant.WriteString(benchstatTable("Load-8", unit, "1", "1", "p=1.000 n=6"))
		}

		var out strings.Builder

		So(run(strings.NewReader(csv), strings.NewReader(tolerant.String()), &out, testThreshold, testAlpha), ShouldBeNil)

		Convey("that criterion fails as not compared, and a figure only base has is not reported", func() {
			So(out.String(), ShouldEqual,
				"REGRESSION Load-8 (c) longest queue-lock hold, max-qlock-us: not compared\n\n"+
					"WINDOW Load-8: FAIL (c)\n"+
					"  (a) operation duration, sec/op: +0.00% (p=1.000) ok\n"+
					"  (b) slow reserves per op, slow-reserves/op: +0.00% (p=1.000) ok\n"+
					"  (c) longest queue-lock hold, max-qlock-us: not compared REGRESSION\n"+
					"  (d) total slow-reserve wait, slow-reserve-wait-us/op: +0.00% (p=1.000) ok\n"+
					strings.Join(tailLines(nil), "\n")+"\n"+
					"  (e) median reserve wait, reserve-p50-us: +0.00% (p=1.000) ok\n")
		})
	})

	Convey("Given both trees report slow reserves but head lacks their wait", t, func() {
		lines := judgeCase("head-without-slow-wait")

		Convey("(d) fails as not measured on head", func() {
			So(withPrefix(lines, "REGRESSION "), ShouldResemble, []string{
				"REGRESSION " + addManyNorc + " (d) total slow-reserve wait, slow-reserve-wait-us/op: not measured on head",
			})
		})
	})
}

func TestWindowRuleJudgesTheTotalSlowWaitNotItsMedian(t *testing.T) {
	Convey("Given a twice-as-fast op that removed the short lock waits which set base's median slow wait", t, func() {
		lines := judgeCase("short-waits-vanish")

		Convey("it passes on the 93% fall in total slow wait, with the 40% higher median informational", func() {
			So(withPrefix(lines, "REGRESSION "), ShouldBeEmpty)

			block := windowBlock(lines, addManyNorc)
			So(block, ShouldNotBeEmpty)
			So(block[0], ShouldEqual, "WINDOW "+addManyNorc+": PASS")
			So(block, ShouldContain, "  (d) total slow-reserve wait, slow-reserve-wait-us/op: -93.00% (p=0.002) ok")
			So(block, ShouldContain, "  informational, slow-reserve-p50-us: +40.00% (p=0.002)")
		})
	})

	Convey("Given slow reserves whose median rises 50% while their total wait is unchanged", t, func() {
		lines := judgeCase("slow-median-only")

		Convey("the median alone does not fail", func() {
			So(withPrefix(lines, "REGRESSION "), ShouldBeEmpty)
			So(windowBlock(lines, addManyNorc), ShouldContain, "  informational, slow-reserve-p50-us: +50.00% (p=0.002)")
		})
	})

	Convey("Given an op that got genuinely slower, holding the queue's lock more often and longer", t, func() {
		lines := judgeCase("slower-op-longer-delays")

		Convey("it fails on its duration and on every delay figure", func() {
			So(windowBlock(lines, addManyNorc)[0], ShouldEqual, "WINDOW "+addManyNorc+": FAIL (a) (b) (c) (d)")
			So(withPrefix(lines, "REGRESSION "), ShouldResemble, []string{
				"REGRESSION " + addManyNorc + " (a) operation duration, sec/op: +30.00% (p=0.002)",
				"REGRESSION " + addManyNorc + " (b) slow reserves per op, slow-reserves/op: +50.00% (p=0.002)",
				"REGRESSION " + addManyNorc + " (c) longest queue-lock hold, max-qlock-us: +30.00% (p=0.002)",
				"REGRESSION " + addManyNorc + " (d) total slow-reserve wait, slow-reserve-wait-us/op: +65.00% (p=0.002)",
				tailRegression(addManyNorc, "1.25", "+50.00% (p=0.002)"),
				tailRegression(addManyNorc, "1.5", "+50.00% (p=0.002)"),
				tailRegression(addManyNorc, "2", "+50.00% (p=0.002)"),
				tailRegression(addManyNorc, "2.5", "+50.00% (p=0.002)"),
				tailRegression(addManyNorc, "3", "+50.00% (p=0.002)"),
				tailRegression(addManyNorc, "4", "+50.00% (p=0.002)"),
				tailRegression(addManyNorc, "5", "+49.99% (p=0.002)"),
				tailRegression(addManyNorc, "6", "+50.00% (p=0.002)"),
				tailRegression(addManyNorc, "8", "+50.00% (p=0.002)"),
				tailRegression(addManyNorc, "10", "+50.03% (p=0.002)"),
				tailRegression(addManyNorc, "12", "+50.00% (p=0.002)"),
				tailRegression(addManyNorc, "16", "+50.00% (p=0.002)"),
			})
		})
	})

	Convey("Given item D's A/B of kick and resume against d1a4b6d7, totals summed from its per-reserve logs", t, func() {
		lines := judgeCase("itemD-ab")

		Convey("both pass, (d) falling with the slow reserves HEAD removed", func() {
			So(withPrefix(lines, "REGRESSION "), ShouldBeEmpty)

			for _, bench := range []string{kickBench, resumeBench} {
				block := windowBlock(lines, bench)
				So(block, ShouldNotBeEmpty)
				So(block[0], ShouldEqual, "WINDOW "+bench+": PASS")
			}

			So(windowBlock(lines, kickBench), ShouldContain,
				"  (d) total slow-reserve wait, slow-reserve-wait-us/op: -98.30% (p=0.002) ok")
			So(windowBlock(lines, resumeBench), ShouldContain,
				"  (d) total slow-reserve wait, slow-reserve-wait-us/op: -97.20% (p=0.002) ok")
		})

		Convey("the tail counts fall or stay at 0, the rare base reserves over 8ms not counting as a fall", func() {
			So(windowBlock(lines, kickBench)[5:5+len(tailMs)], ShouldResemble, tailLines(map[string]string{
				"1.25": "-98.09% (p=0.002) ok",
				"1.5":  "-98.90% (p=0.002) ok",
				"2":    allGone,
				"2.5":  allGone,
				"3":    allGone,
				"4":    allGone,
				"5":    allGone,
				"6":    "-100.00% (p=0.015) ok",
			}))
			So(windowBlock(lines, resumeBench)[5:5+len(tailMs)], ShouldResemble, tailLines(map[string]string{
				"1.25": "-96.29% (p=0.002) ok",
				"1.5":  "-97.31% (p=0.002) ok",
				"2":    "-98.78% (p=0.002) ok",
				"2.5":  allGone,
				"3":    allGone,
				"4":    allGone,
				"5":    allGone,
				"6":    allGone,
				"8":    sparseGone,
			}))
		})
	})
}

func TestWindowRuleOnItemDRedRunsWithoutTolerantCounts(t *testing.T) {
	for _, c := range []struct{ name, resumeTotal string }{
		{"itemD-red-tails", "-94.88% (p=0.002) ok"},
		{"itemD-red-r3", "-94.55% (p=0.002) ok"},
	} {
		Convey("Given item D's red command rerun "+c.name+", whose harness predates the counts over 1.1 times "+
			"each threshold", t, func() {
			lines := judgeCase(c.name)

			Convey("every other figure falls, and every count fails as not measured", func() {
				for _, bench := range []string{kickBench, resumeBench} {
					So(windowBlock(lines, bench)[0], ShouldEqual, "WINDOW "+bench+": FAIL (d)")
					So(withPrefix(lines, "REGRESSION "+bench+" "), ShouldResemble, notMeasuredCounts(bench))
				}

				So(windowBlock(lines, resumeBench), ShouldContain,
					"  (d) total slow-reserve wait, slow-reserve-wait-us/op: "+c.resumeTotal)
			})
		})
	}
}

func TestWindowRuleOnItemDRedRunWithTolerantCounts(t *testing.T) {
	Convey("Given item D's red command rerun with the counts over each threshold and 1.1 times it", t, func() {
		lines := judgeCase("itemD-red-r4")

		Convey("both pass, every count falling or staying at 0", func() {
			So(withPrefix(lines, "REGRESSION "), ShouldBeEmpty)
			So(windowBlock(lines, kickBench)[0], ShouldEqual, "WINDOW "+kickBench+": PASS")
			So(windowBlock(lines, resumeBench)[0], ShouldEqual, "WINDOW "+resumeBench+": PASS")
			So(windowBlock(lines, kickBench), ShouldContain,
				"  (d) total slow-reserve wait, slow-reserve-wait-us/op: -96.80% (p=0.002) ok")
			So(windowBlock(lines, kickBench), ShouldContain, tailLine("1.5", "-94.50% (p=0.002) ok"))
			So(windowBlock(lines, resumeBench), ShouldContain, tailLine("1.5", "-91.28% (p=0.002) ok"))
			So(windowBlock(lines, resumeBench), ShouldContain, tailLine("8", "+0.00% (p=0.455) ok"))
		})
	})
}

func TestWindowRuleCountsReservesOverTailThresholds(t *testing.T) {
	Convey("Given a twice-as-fast kick whose slow reserves fall from 35 to 3 per op, "+
		"each now about 12ms off the queue's lock", t, func() {
		lines := judgeCase("fewer-much-longer-offlock")

		Convey("its 20% lower total passes, but more reserves per op wait over 4 to 10ms, so it fails (d)", func() {
			block := windowBlock(lines, kickBench)
			So(block, ShouldNotBeEmpty)
			So(block[0], ShouldEqual, "WINDOW "+kickBench+": FAIL (d)")
			So(block, ShouldContain, "  (c) longest queue-lock hold, max-qlock-us: +0.00% (p=1.000) ok")
			So(block, ShouldContain, "  (d) total slow-reserve wait, slow-reserve-wait-us/op: -20.00% (p=0.015) ok")
			So(withPrefix(lines, "REGRESSION "), ShouldResemble, []string{
				tailRegression(kickBench, "4", "+42.86% (p=0.002)"),
				tailRegression(kickBench, "5", "+198.63% (p=0.002)"),
				tailRegression(kickBench, "6", "+445.45% (p=0.002)"),
				tailRegression(kickBench, "8", "+2900.00% (p=0.002)"),
				tailRegression(kickBench, "10", "+5713.95% (p=0.002)"),
			})
		})
	})

	Convey("Given a few rounds of each tree with one reserve over 8 or 16ms in 10 ops, head's more often", t, func() {
		lines := judgeCase("sparse-tail-noise")

		Convey("the rise from a median of 0 is not significant, so it passes", func() {
			So(withPrefix(lines, "REGRESSION "), ShouldBeEmpty)
			So(windowBlock(lines, addManyNorc), ShouldContain, tailLine("8", "from 0 to 0.1 (p=0.470) ok"))
		})
	})
}

func TestWindowRuleCatchesAWaitLengthenedToJustUnderAThreshold(t *testing.T) {
	for _, c := range []struct {
		given, name, bench string
		want               []string
	}{
		{
			"a twice-as-fast kick whose slow reserves are 2 per op of 7.9ms, off the queue's lock",
			"offlock-7.9ms-x2", kickBench, []string{
				tailRegression(kickBench, "5", "+99.08% (p=0.002)"),
				tailRegression(kickBench, "6", "+263.64% (p=0.002)"),
			},
		},
		{
			"a twice-as-fast kick whose slow reserves are 8 per op of 3.9ms, off the queue's lock",
			"offlock-3.9ms-x8", kickBench, []string{
				tailRegression(kickBench, "2.5", "+68.61% (p=0.002)"),
				tailRegression(kickBench, "3", "+142.42% (p=0.002)"),
			},
		},
		{
			"a twice-as-fast kick whose slow reserves are 20 per op of 1.95ms, off the queue's lock",
			"offlock-1.95ms-x20", kickBench, []string{
				tailRegression(kickBench, "1.5", "+92.31% (p=0.002)"),
			},
		},
		{
			"a twice-as-fast add whose one slow reserve per add is a 25ms stall, where base's were 15 to 17ms",
			"addmany-stall-25ms", addManyNorc, []string{
				tailRegression(addManyNorc, "20", "from 0 to 1.025 (p=0.002)"),
			},
		},
	} {
		Convey("Given "+c.given+", which every other figure passes", t, func() {
			lines := judgeCase(c.name)

			Convey("the counts over the thresholds the waits now cross by more than 10% fail it", func() {
				So(windowBlock(lines, c.bench)[0], ShouldEqual, "WINDOW "+c.bench+": FAIL (d)")
				So(withPrefix(lines, "REGRESSION "), ShouldResemble, c.want)
			})
		})
	}
}

func TestWindowRuleToleratesWaitsUpTo10PercentLonger(t *testing.T) {
	for _, c := range []struct{ name, pct, hold, total, over16 string }{
		{"addmany-hold-5pc-longer", "5", "+5.00% (p=0.026)", "+5.00% (p=0.515)", "-76.19% (p=0.002)"},
		{"addmany-hold-9pc-longer", "9", "+9.00% (p=0.002)", "+9.00% (p=0.394)", "-14.68% (p=0.093)"},
	} {
		Convey("Given an add that holds the queue's lock "+c.pct+"% longer, its delayed reserves' waits "+
			"crossing 16ms", t, func() {
			lines := judgeCase(c.name)

			Convey("no reserve waits more than 10% longer, so every count passes, as do the hold and total", func() {
				So(withPrefix(lines, "REGRESSION "), ShouldBeEmpty)

				block := windowBlock(lines, addManyNorc)
				So(block, ShouldNotBeEmpty)
				So(block[0], ShouldEqual, "WINDOW "+addManyNorc+": PASS")
				So(block, ShouldContain, "  (c) longest queue-lock hold, max-qlock-us: "+c.hold+" ok")
				So(block, ShouldContain, "  (d) total slow-reserve wait, slow-reserve-wait-us/op: "+c.total+" ok")
				So(block, ShouldContain, tailLine("16", c.over16+" ok"))
			})
		})
	}

	Convey("Given an add that holds the queue's lock 15% longer", t, func() {
		lines := judgeCase("addmany-hold-15pc-longer")

		Convey("it fails on the hold and on the counts its waits now cross by more than 10%", func() {
			So(windowBlock(lines, addManyNorc)[0], ShouldEqual, "WINDOW "+addManyNorc+": FAIL (c) (d)")
			So(withPrefix(lines, "REGRESSION "), ShouldResemble, []string{
				"REGRESSION " + addManyNorc + " (c) longest queue-lock hold, max-qlock-us: +15.00% (p=0.002)",
				tailRegression(addManyNorc, "2.5", "+25.00% (p=0.015)"),
				tailRegression(addManyNorc, "5", "+50.00% (p=0.002)"),
				tailRegression(addManyNorc, "16", "+69.57% (p=0.002)"),
			})
		})
	})
}

func TestWindowRuleOnItemDRedRun(t *testing.T) {
	Convey("Given item D's red command rerun with the total slow wait in the harness, "+
		"which the median rule failed on Resume", t, func() {
		old, err := os.ReadFile(filepath.Join("testdata", "itemD-red", "old-rule-judgement.txt"))
		So(err, ShouldBeNil)
		So(string(old), ShouldContainSubstring, "REGRESSION ResumeUnderReserveLoad-8 (d) median slow-reserve wait, "+
			"slow-reserve-p50-us: +29.34% (p=0.043)")

		lines := judgeCase("itemD-red")

		Convey("the median's rise is informational and the total's fall passes, "+
			"but the tail counts its harness predates fail as not measured", func() {
			for _, bench := range []string{kickBench, resumeBench} {
				So(windowBlock(lines, bench)[0], ShouldEqual, "WINDOW "+bench+": FAIL (d)")

				want := make([]string, 0, len(tailUnits))
				for _, unit := range tailUnits {
					want = append(want, unit+": not measured on base or head")
				}

				var got []string
				for _, l := range withPrefix(lines, "REGRESSION "+bench+" ") {
					got = append(got, l[strings.LastIndex(l, ", ")+2:])
				}

				So(got, ShouldResemble, want)
			}

			notMeasured := make([]string, len(tailMs))
			for i, ms := range tailMs {
				notMeasured[i] = tailLine(ms, "not measured on base or head REGRESSION")
			}

			So(windowBlock(lines, resumeBench), ShouldResemble, slices.Concat([]string{
				"WINDOW " + resumeBench + ": FAIL (d)",
				"  (a) operation duration, sec/op: -48.85% (p=0.002) ok",
				"  (b) slow reserves per op, slow-reserves/op: -93.53% (p=0.002) ok",
				"  (c) longest queue-lock hold, max-qlock-us: -61.96% (p=0.026) ok",
				"  (d) total slow-reserve wait, slow-reserve-wait-us/op: -93.94% (p=0.002) ok",
			}, notMeasured, []string{
				"  (e) median reserve wait, reserve-p50-us: -2.01% (p=0.818) ok",
				"  informational, reserve-p99-us: -74.79% (p=0.002)",
				"  informational, reserve-max-us: -62.12% (p=0.002)",
				"  informational, reserves/op: -16.34% (p=0.004)",
				"  informational, slow-reserve-p50-us: +29.34% (p=0.043)",
			}))
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
			So(windowBlock(lines, kickBench), ShouldResemble, slices.Concat([]string{
				"WINDOW KickUnderReserveLoad-8: FAIL (e)",
				"  (a) operation duration, sec/op: -73.80% (p=0.002) ok",
				"  (b) slow reserves per op, slow-reserves/op: +0.00% (p=1.000) ok",
				"  (c) longest queue-lock hold, max-qlock-us: +0.00% (p=1.000) ok",
				"  (d) total slow-reserve wait, slow-reserve-wait-us/op: +0.00% (p=1.000) ok",
			}, tailLines(nil), []string{
				"  (e) median reserve wait, reserve-p50-us: +678.20% (p=0.002) REGRESSION",
				"  informational, reserve-p99-us: +121.47% (p=0.002)",
				"  informational, reserve-max-us: -21.38% (p=0.589)",
				"  informational, reserves/op: -83.26% (p=0.002)",
			}))

			for _, bench := range []string{kickBench, resumeBench,
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
				"WINDOW " + kickBench + ": PASS",
				"WINDOW ResumeUnderReserveLoad-8: PASS",
				"WINDOW SuspendUnderReserveLoad-8: PASS",
				"WINDOW " + addManyNorc + ": PASS",
				"WINDOW " + addManyRc + ": PASS",
			})
		})
	})
}

// judgeWindowed1g4 judges testdata/<name>/benchstat.csv with, appended, its
// under-load benchmarks' (b) to (d) figures, the same on both trees, (d)'s
// counts in the tolerant CSV.
func judgeWindowed1g4(name string) []string {
	b, err := os.ReadFile(filepath.Join("testdata", name, "benchstat.csv"))
	So(err, ShouldBeNil)

	const pkg = "pkg: github.com/VertebrateResequencing/wr/jobqueue\n"

	var csv, tolerant strings.Builder

	csv.Write(b)
	csv.WriteString("\n" + pkg)
	tolerant.WriteString(pkg)

	benches := []string{kickBench, resumeBench, "SuspendUnderReserveLoad-8", addManyNorc, addManyRc}

	for _, unit := range []string{"slow-reserves/op", "max-qlock-us", "slow-reserve-wait-us/op"} {
		for _, bench := range benches {
			csv.WriteString(benchstatTable(bench, unit, "0", "0", "p=1.000 n=6"))
		}
	}

	for _, unit := range tailUnits {
		for _, bench := range benches {
			tolerant.WriteString(benchstatTable(bench, unit, "0", "0", "p=1.000 n=6"))
		}
	}

	var out strings.Builder

	So(run(strings.NewReader(csv.String()), strings.NewReader(tolerant.String()), &out, testThreshold, testAlpha),
		ShouldBeNil)

	return strings.Split(strings.TrimRight(out.String(), "\n"), "\n")
}

func TestRuleBoundaries(t *testing.T) {
	Convey("Given a figure exactly the threshold worse at p well below alpha", t, func() {
		csv := benchstatTable("Load-8", "sec/op", "4", "5", "p=0.002 n=6")

		Convey("it does not regress at that threshold, but does just below it", func() {
			var out strings.Builder

			So(run(strings.NewReader(csv), noTolerant(), &out, 25, testAlpha), ShouldBeNil)
			So(out.String(), ShouldBeEmpty)

			out.Reset()
			So(run(strings.NewReader(csv), noTolerant(), &out, 24.9, testAlpha), ShouldBeNil)
			So(out.String(), ShouldEqual, "REGRESSION Load-8 sec/op: +25.00% (p=0.002)\n")
		})
	})

	Convey("Given a figure well beyond the threshold at p exactly alpha", t, func() {
		csv := benchstatTable("Load-8", "sec/op", "4", "8", "p=0.050 n=6")

		Convey("it does not regress at that alpha, but does just above it", func() {
			var out strings.Builder

			So(run(strings.NewReader(csv), noTolerant(), &out, testThreshold, 0.05), ShouldBeNil)
			So(out.String(), ShouldBeEmpty)

			out.Reset()
			So(run(strings.NewReader(csv), noTolerant(), &out, testThreshold, 0.051), ShouldBeNil)
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

// notMeasuredCounts returns the REGRESSION line of each of bench's (d) counts
// for a run without the counts over 1.1 times each threshold.
func notMeasuredCounts(bench string) []string {
	lines := make([]string, len(tailMs))
	for i, ms := range tailMs {
		lines[i] = tailRegression(bench, ms, "not measured on base or head")
	}

	return lines
}

// judgeCase runs the judge over testdata/<name>/benchstat.csv and its
// benchstat-tol.csv at speed.sh's default threshold and alpha, and returns its
// output lines. A recorded case from before the counts over 1.1 times each
// threshold has no benchstat-tol.csv, so the judge gets an empty one.
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

// tailLines returns tailLine for each of tailMs, with results[ms] or, for a
// threshold results lacks, unchanged.
func tailLines(results map[string]string) []string {
	lines := make([]string, len(tailMs))

	for i, ms := range tailMs {
		result, ok := results[ms]
		if !ok {
			result = unchanged
		}

		lines[i] = tailLine(ms, result)
	}

	return lines
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

// noTolerant returns an empty tolerant CSV.
func noTolerant() io.Reader {
	return strings.NewReader("")
}

// tailRegression returns the REGRESSION line for bench's (d) count over ms
// with the given result.
func tailRegression(bench, ms, result string) string {
	return "REGRESSION " + bench + " (d) " + tailWhat(ms) + ", " + tailUnit(ms) + ": " + result
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

	So(run(f, tolerantCSV(name), &out, threshold, alpha), ShouldBeNil)

	return strings.Split(strings.TrimRight(out.String(), "\n"), "\n")
}

// tailLine returns a WINDOW block's line for (d)'s count over ms with the
// given result.
func tailLine(ms, result string) string {
	return "  (d) " + tailWhat(ms) + ", " + tailUnit(ms) + ": " + result
}

// tailWhat describes (d)'s count over ms.
func tailWhat(ms string) string {
	return "reserves over " + ms + "ms per op (head's over " + tolerantMs[ms] + "ms)"
}

func tailUnit(ms string) string {
	return "reserves-over-" + ms + "ms/op"
}

// tolerantCSV returns testdata/<name>/benchstat-tol.csv, or nothing if it has
// none.
func tolerantCSV(name string) io.Reader {
	b, err := os.ReadFile(filepath.Join("testdata", name, "benchstat-tol.csv"))
	if errors.Is(err, fs.ErrNotExist) {
		return strings.NewReader("")
	}

	So(err, ShouldBeNil)

	return bytes.NewReader(b)
}
