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
const (
	// slowReservesUnit is the figure whose presence on both trees puts a
	// benchmark under the window-aware rule.
	slowReservesUnit = "slow-reserves/op"

	// underLoadUnit marks a benchmark as one the window-aware rule is for, so
	// it gets a note when both trees ran it but the rule cannot judge it.
	underLoadUnit = "reserve-p99-us"

	statusOK         = "ok"
	statusRegression = "REGRESSION"
)

// informationalUnits are reported for a benchmark under the window-aware rule
// but never fail it: they depend on how many reserves the window held.
//
//nolint:gochecknoglobals // A fixed table.
var informationalUnits = []string{"reserve-p99-us", "reserve-max-us", "reserves/op"}

// criterion is one figure the window-aware rule judges. (c)'s figure
// approximates the longest queue-lock hold the probe saw: it can fall short by
// up to the probe's period, or miss a short hold, and it adds the probe's own
// scheduling delay. It is close for an add's hold of about 15ms, and mostly
// that delay for the microsecond holds of per-item operations.
type criterion struct {
	id, what, unit string
}

//nolint:gochecknoglobals // A fixed table.
var windowCriteria = []criterion{
	{"(a)", "operation duration", "sec/op"},
	{"(b)", "slow reserves per op", slowReservesUnit},
	{"(c)", "longest queue-lock hold", "max-qlock-us"},
	{"(d)", "median slow-reserve wait", "slow-reserve-p50-us"},
	{"(e)", "median reserve wait", "reserve-p50-us"},
}

func isWindowUnit(unit string) bool {
	return slices.Contains(informationalUnits, unit) ||
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
// under-load benchmark that is not.
func judge(figures []figure, threshold, alpha float64) string {
	benches := groupBenchmarks(figures)
	regressions := rawRegressions(figures, benches, threshold, alpha)

	var details []string

	for _, key := range benchmarkOrder(figures) {
		b := benches[key]

		switch {
		case b.windowed():
			block, failed := windowJudgement(b, threshold, alpha)
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
// criterion it fails.
func windowJudgement(b benchmark, threshold, alpha float64) ([]string, []string) {
	var regressions, failedIDs []string

	lines := make([]string, 0, len(windowCriteria)+len(informationalUnits))

	for _, c := range windowCriteria {
		label := fmt.Sprintf("%s %s, %s", c.id, c.what, c.unit)
		result, failed := judgeCriterion(b.figures[c.unit], threshold, alpha)

		status := statusOK
		if failed {
			status = statusRegression

			failedIDs = append(failedIDs, c.id)
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
