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
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"
)

// The window-aware rule judges a benchmark that times a background operation
// while reservers reserve (jobqueue's *UnderReserveLoad benchmarks). A raw
// percentile of the reserves taken during the operation is not judged: a
// faster operation's shorter window holds fewer reserves, so the same few slow
// ones make a larger share of them and the percentile rises with no reserve
// worse off. The rule instead judges, per operation, the figures below, each
// by the raw rule (worse by more than the threshold at p < alpha). The median
// reserve wait is among them: it is a median over thousands of reserves per
// operation, which a shorter window does not skew the way it skews the tail,
// and it is the one figure that catches a uniform sub-millisecond slowdown of
// every reserve, which delays none past the slow-reserve threshold.
//
// (d) judges how long the slow reserves wait, by figures that removing
// reserves cannot raise, not by their median: a faster operation that removes
// the short lock waits which made up most of base's slow reserves leaves only
// the rarer, longer stalls base also has, so the median rises with no reserve
// worse off (bugfix 261119 item D). (d)'s total wait per operation rises when
// the slow reserves wait longer at the same count, but it is count times
// mean, so when (b) falls to 1/k of base's each remaining reserve can wait up
// to k times as long before it rises. (d)'s counts per operation of reserves
// over each of tailThresholds close most of that gap: removing a reserve
// cannot raise them and lengthening one can only raise them, so a head whose
// reserves wait past a threshold more often per operation than base's fails,
// however few slow reserves it has in all.
//
// Each count compares head's reserves over 1.1 times the threshold T (the
// harness's reserves-over-<T>ms-tol/op, which speed.sh gives the plain name
// in a second benchstat run, the tolerant CSV) with base's over T. A count at
// one threshold amplifies a small change in waits clustered just under it:
// an add whose hold of the queue's lock delays reserves 15 to 17ms, held 5%
// longer, nearly doubles the count over 16ms (testdata
// addmany-hold-5pc-longer). With the tolerance, a head whose every wait is at
// most 10% longer than base's has no more reserves over 1.1T than base has
// over T, so a slowdown within the gate's 10% cannot fail a count.
//
// What (d) still lets through:
//   - A wait that lengthens without crossing 1.1 times the next threshold up,
//     while its count per operation stays within 10% of base's count over the
//     threshold below and the total falls: from just over 1ms ((b)'s
//     threshold) to just under 1.375ms, 1.375 times; from just over 1.25,
//     2.5, 5, 10, 20 or 40ms, 1.32 times (the next is 1.2 times higher); from
//     just over 2, 4, 8, 16 or 32ms, 1.375 times (1.25 times higher); from
//     just over 1.5, 3, 6, 12, 24 or 48ms, 1.47 times (1.33 times higher);
//     and past 64ms without limit, where only the total can see it.
//   - Sparse long stalls, as 6 rounds rarely make a count of 0.1 per
//     operation significant.
//   - Stalls at a constant rate per second, which a faster operation's
//     shorter window holds fewer of (a fixed-length observation window per
//     operation would close that).
//   - Reserves still waiting when the operation ends, which the harness drops.
const (
	// slowReservesUnit is the figure whose presence on both trees puts a
	// benchmark under the window-aware rule.
	slowReservesUnit = "slow-reserves/op"

	// underLoadUnit marks a benchmark as one the window-aware rule is for, so
	// it gets a note when both trees ran it but the rule cannot judge it.
	underLoadUnit = "reserve-p99-us"

	// slowWaitID is (d)'s id, which its figures share.
	slowWaitID = "(d)"

	// tailUnitPrefix starts the unit of each of (d)'s counts.
	tailUnitPrefix = "reserves-over-"

	// toleranceFactor is how much higher than base's threshold head's
	// reserves must wait to count against a (d) count.
	toleranceFactor = 1.1

	statusOK         = "ok"
	statusRegression = "REGRESSION"
)

// informationalUnits are reported for a benchmark under the window-aware rule
// but never fail it: they depend on how many reserves the window held, or, for
// the slow reserves' median, on which of them the operation removed.
//
//nolint:gochecknoglobals // A fixed table.
var informationalUnits = []string{"reserve-p99-us", "reserve-max-us", "reserves/op", "slow-reserve-p50-us"}

// tailThresholds are the thresholds, in ms, of (d)'s counts: jobqueue's
// slowReserveTailThresholds.
//
//nolint:gochecknoglobals // A fixed table.
var tailThresholds = []string{
	"1.25", "1.5", "2", "2.5", "3", "4", "5", "6", "8", "10", "12", "16", "20", "24", "32", "40", "48", "64",
}

// criterion is one figure the window-aware rule judges. (c)'s figure
// approximates the longest queue-lock hold the probe saw: it can fall short by
// up to the probe's period, or miss a short hold, and it adds the probe's own
// scheduling delay. It is close for an add's hold of about 15ms, and mostly
// that delay for the microsecond holds of per-item operations.
// tolerant criteria are judged on the tolerant CSV.
type criterion struct {
	id, what, unit string
	tolerant       bool
}

//nolint:gochecknoglobals // A fixed table.
var windowCriteria = slices.Concat([]criterion{
	{"(a)", "operation duration", "sec/op", false},
	{"(b)", "slow reserves per op", slowReservesUnit, false},
	{"(c)", "longest queue-lock hold", "max-qlock-us", false},
	{slowWaitID, "total slow-reserve wait", "slow-reserve-wait-us/op", false},
}, tailCriteria(), []criterion{
	{"(e)", "median reserve wait", "reserve-p50-us", false},
})

// tailCriteria returns (d)'s count criterion for each of tailThresholds,
// judged on the tolerant CSV.
func tailCriteria() []criterion {
	criteria := make([]criterion, len(tailThresholds))

	for i, ms := range tailThresholds {
		t, err := strconv.ParseFloat(ms, 64)
		if err != nil {
			panic(err)
		}

		criteria[i] = criterion{
			slowWaitID,
			fmt.Sprintf("reserves over %sms per op (head's over %sms)", ms,
				strconv.FormatFloat(t*toleranceFactor, 'g', 4, 64)),
			tailUnitPrefix + ms + "ms/op",
			true,
		}
	}

	return criteria
}

// figure returns c's figure of b, or of tolerant, b in the tolerant CSV, if c
// is tolerant.
func (c criterion) figure(b, tolerant benchmark) figure {
	if c.tolerant {
		return tolerant.figures[c.unit]
	}

	return b.figures[c.unit]
}

// isWindowUnit says if the window-aware rule, not the raw rule, owns unit:
// its criteria, its informational figures, and every count of reserves over
// a threshold, the 1.1 times ones included.
func isWindowUnit(unit string) bool {
	return slices.Contains(informationalUnits, unit) || strings.HasPrefix(unit, tailUnitPrefix) ||
		slices.ContainsFunc(windowCriteria, func(c criterion) bool { return c.unit == unit })
}

// benchmark is one benchmark's figures, by unit.
type benchmark struct {
	name    string
	figures map[string]figure
}

func (b benchmark) has(unit string) (hasBase, hasHead bool) {
	f, ok := b.figures[unit]

	return ok && f.hasBase, ok && f.hasHead
}

// windowed says if the window-aware rule judges b: both trees report its slow
// reserves.
func (b benchmark) windowed() bool {
	hasBase, hasHead := b.has(slowReservesUnit)

	return hasBase && hasHead
}

// isUnderLoad says if b is a benchmark the window-aware rule is for that both
// trees ran.
func (b benchmark) isUnderLoad() bool {
	hasBase, hasHead := b.has(underLoadUnit)

	return hasBase && hasHead
}

// note says why an under-load benchmark was judged by its raw figures.
func (b benchmark) note() string {
	hasBase, hasHead := b.has(slowReservesUnit)

	who := "neither tree reports"

	switch {
	case hasHead:
		who = "base does not report"
	case hasBase:
		who = "head does not report"
	}

	return fmt.Sprintf("NOTE %s: %s %s, so the raw rule judged it, reserve percentiles included; "+
		`DEVELOPERS.md "Performance gate" says how to give a base the window figures`, b.name, who, slowReservesUnit)
}

// informational returns a line for each of b's informationalUnits figures
// that both trees report.
func (b benchmark) informational() []string {
	var lines []string

	for _, unit := range informationalUnits {
		if f, ok := b.figures[unit]; ok && f.compared {
			lines = append(lines, fmt.Sprintf("  informational, %s: %s (p=%s)", unit, describeChange(f), f.pText))
		}
	}

	return lines
}

func describeChange(f figure) string {
	c := change(f)
	if math.IsInf(c, 0) {
		return "from 0 to " + strconv.FormatFloat(f.head, 'g', 4, 64)
	}

	return fmt.Sprintf("%+.2f%%", c)
}

// judge returns the judgement of figures: a REGRESSION line per failure, then
// a WINDOW block per benchmark under the window-aware rule and a NOTE per
// under-load benchmark that is not. The rule judges (d)'s counts on
// tolerant, the figures of the tolerant CSV.
func judge(figures, tolerant []figure, threshold, alpha float64) string {
	benches := groupBenchmarks(figures)
	tolerantBenches := groupBenchmarks(tolerant)
	regressions := rawRegressions(figures, benches, threshold, alpha)

	var details []string

	for _, key := range benchmarkOrder(figures) {
		b := benches[key]

		switch {
		case b.windowed():
			block, failed := windowJudgement(b, tolerantBenches[key], threshold, alpha)
			details = append(details, block...)
			regressions = append(regressions, failed...)
		case b.isUnderLoad():
			details = append(details, b.note())
		}
	}

	sections := make([]string, 0, 2) //nolint:mnd

	for _, lines := range [][]string{regressions, details} {
		if len(lines) > 0 {
			sections = append(sections, strings.Join(lines, "\n")+"\n")
		}
	}

	return strings.Join(sections, "\n")
}

// benchmarkOrder returns each benchmark's key once, in the order benchstat
// first wrote it.
func benchmarkOrder(figures []figure) []string {
	var keys []string

	for _, f := range figures {
		if !slices.Contains(keys, f.key()) {
			keys = append(keys, f.key())
		}
	}

	return keys
}

func groupBenchmarks(figures []figure) map[string]benchmark {
	benches := make(map[string]benchmark)

	for _, f := range figures {
		b, ok := benches[f.key()]
		if !ok {
			b = benchmark{name: f.bench, figures: make(map[string]figure)}
			benches[f.key()] = b
		}

		b.figures[f.unit] = f
	}

	return benches
}

// rawRegressions returns a REGRESSION line for each figure that fails the raw
// rule, leaving out the figures the window-aware rule judges.
func rawRegressions(figures []figure, benches map[string]benchmark, threshold, alpha float64) []string {
	var regressions []string

	for _, f := range figures {
		if benches[f.key()].windowed() && isWindowUnit(f.unit) {
			continue
		}

		if regresses(f, threshold, alpha) {
			regressions = append(regressions,
				fmt.Sprintf("REGRESSION %s %s: %s (p=%s)", f.bench, f.unit, describeChange(f), f.pText))
		}
	}

	return regressions
}

// regresses says if f got worse by more than threshold percent at p < alpha.
func regresses(f figure, threshold, alpha float64) bool {
	return f.compared && worsening(f) > threshold && f.p < alpha
}

// worsening returns change(f) signed so that worse is positive.
func worsening(f figure) float64 {
	if higherIsBetter(f.unit) {
		return -change(f)
	}

	return change(f)
}

// higherIsBetter says if a bigger figure in unit is better: rates and
// archive-ceiling's throughput factor.
func higherIsBetter(unit string) bool {
	return strings.HasSuffix(unit, "/s") || unit == "throughput-factor"
}

// change returns head's change from base in percent, infinite from a base of
// 0.
func change(f figure) float64 {
	if f.base == 0 {
		if f.head == 0 {
			return 0
		}

		return math.Inf(int(math.Copysign(1, f.head)))
	}

	return (f.head - f.base) / f.base * 100 //nolint:mnd
}

// windowJudgement returns b's WINDOW block and the REGRESSION line of each
// criterion it fails, judging tolerant criteria on tolerant, b in the
// tolerant CSV.
func windowJudgement(b, tolerant benchmark, threshold, alpha float64) ([]string, []string) {
	var regressions, failedIDs []string

	lines := make([]string, 0, len(windowCriteria)+len(informationalUnits))

	for _, c := range windowCriteria {
		label := fmt.Sprintf("%s %s, %s", c.id, c.what, c.unit)
		result, failed := judgeCriterion(c.figure(b, tolerant), threshold, alpha)

		status := statusOK
		if failed {
			status = statusRegression

			if !slices.Contains(failedIDs, c.id) {
				failedIDs = append(failedIDs, c.id)
			}

			regressions = append(regressions, fmt.Sprintf("REGRESSION %s %s: %s", b.name, label, result))
		}

		lines = append(lines, fmt.Sprintf("  %s: %s %s", label, result, status))
	}

	lines = append(lines, b.informational()...)

	verdict := "PASS"
	if len(failedIDs) > 0 {
		verdict = "FAIL " + strings.Join(failedIDs, " ")
	}

	return append([]string{fmt.Sprintf("WINDOW %s: %s", b.name, verdict)}, lines...), regressions
}

// judgeCriterion describes one criterion's figure and says if it fails. A
// figure either tree does not report fails, so a harness that stops reporting
// one cannot pass by omission.
func judgeCriterion(f figure, threshold, alpha float64) (string, bool) {
	switch {
	case !f.hasBase && !f.hasHead:
		return "not measured on base or head", true
	case !f.hasBase:
		return "not measured on base", true
	case !f.hasHead:
		return "not measured on head", true
	case !f.compared:
		return "not compared", true
	}

	return fmt.Sprintf("%s (p=%s)", describeChange(f), f.pText), regresses(f, threshold, alpha)
}
