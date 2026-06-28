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
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"
)

const (
	summaryIndent = "  "
	skipIndent    = "      "
	minColumnGap  = 2
	ruleWidth     = 44
)

// laneSummaryInput is the pure-function view of one lane needed to build the
// success summary: its kind, the package(s) it covers, and its captured log.
type laneSummaryInput struct {
	name string
	kind LaneKind
	pkg  string
	pkgs []string
	log  string
}

// summarizeLanes builds the success summary printed once at the end of a green
// run: a header, a sorted, aligned per-package "<name> N passed[ M skipped]"
// line with skip descriptions listed underneath, a rule, a grand-total line
// (including the total elapsed wall-clock time), then the PASSED marker.
func summarizeLanes(module string, lanes []laneSummaryInput, colourize bool, elapsed time.Duration) string {
	outcomes := make(map[string]*packageOutcome)

	for _, lane := range lanes {
		for _, segment := range laneSegments(lane) {
			accumulateSegment(outcomes, segment.pkg, segment.text)
		}
	}

	return renderSummary(module, outcomes, colourize, elapsed)
}

func accumulateSegment(outcomes map[string]*packageOutcome, pkgPath string, text string) {
	outcome := outcomes[pkgPath]
	if outcome == nil {
		outcome = &packageOutcome{}
		outcomes[pkgPath] = outcome
	}

	outcome.passed += countTopLevelPasses(text)

	for _, description := range segmentSkips(text) {
		outcome.addSkip(description)
	}
}

func countTopLevelPasses(text string) int {
	count := 0

	for line := range strings.Lines(text) {
		if strings.HasPrefix(line, "--- PASS: ") {
			count++
		}
	}

	return count
}

// segmentSkips returns one description per skipped behaviour in the segment:
// the reason of each top-level "--- SKIP:" function and the Title of each
// GoConvey scope that holds a Skipped assertion.
func segmentSkips(text string) []string {
	skips := topLevelSkipReasons(text)

	return append(skips, conveyScopeSkips(text)...)
}

func topLevelSkipReasons(text string) []string {
	skips := make([]string, 0)
	lastReason := ""

	for line := range strings.Lines(text) {
		if reason, ok := skipReasonLine(line); ok {
			lastReason = reason

			continue
		}

		name, ok := topLevelSkipName(line)
		if !ok {
			continue
		}

		skips = append(skips, skipDescription(name, lastReason))
		lastReason = ""
	}

	return skips
}

// skipReasonLine matches the indented "    file_test.go:NN: reason" line that
// "go test -v" prints immediately before a "--- SKIP:" marker.
func skipReasonLine(line string) (string, bool) {
	if !strings.HasPrefix(line, "    ") {
		return "", false
	}

	trimmed := strings.TrimSpace(strings.TrimRight(line, "\n"))

	file, rest, ok := strings.Cut(trimmed, ":")
	if !ok || !strings.HasSuffix(file, ".go") {
		return "", false
	}

	_, reason, ok := strings.Cut(rest, ":")
	if !ok {
		return "", false
	}

	return strings.TrimSpace(reason), true
}

func topLevelSkipName(line string) (string, bool) {
	rest, ok := strings.CutPrefix(strings.TrimRight(line, "\n"), "--- SKIP: ")
	if !ok {
		return "", false
	}

	name, _, _ := strings.Cut(rest, " (")

	return name, true
}

func skipDescription(name string, reason string) string {
	if reason != "" {
		return reason
	}

	return name
}

// conveyScopeSkips returns the Title of every GoConvey scope that contains a
// Skipped assertion, which corresponds to a SkipConvey("<description>", ...).
func conveyScopeSkips(text string) []string {
	_, blocks, ok := extractConveyJSON(text)
	if !ok || len(blocks) == 0 {
		return nil
	}

	scopes, err := parseConveyScopes(blocks)
	if err != nil {
		return nil
	}

	skips := make([]string, 0)

	for _, scope := range scopes {
		if scopeIsSkipped(scope) {
			skips = append(skips, scope.Title)
		}
	}

	return skips
}

func scopeIsSkipped(scope conveyScope) bool {
	for _, assertion := range scope.Assertions {
		if assertion.Skipped {
			return true
		}
	}

	return false
}

// summaryLayout holds the column widths needed to align every package line: the
// widest package name and the widest passed/skipped counts across all packages.
type summaryLayout struct {
	nameWidth    int
	passedWidth  int
	skippedWidth int
}

func renderSummary(module string, outcomes map[string]*packageOutcome, colourize bool, elapsed time.Duration) string {
	sty := newStyle(colourize)
	packages := sortedPackages(outcomes)
	layout := computeLayout(module, outcomes, packages)

	var out strings.Builder

	writeHeader(&out, sty, len(outcomes))

	totalPassed := 0
	totalSkipped := 0

	for _, pkgPath := range packages {
		outcome := outcomes[pkgPath]
		totalPassed += outcome.passed
		totalSkipped += outcome.skipped()

		writePackageLine(&out, sty, relativePackage(module, pkgPath), outcome, layout)
	}

	writeRule(&out, sty)
	writeGrandTotal(&out, sty, totalPassed, totalSkipped, len(outcomes), elapsed)
	out.WriteString("\n")
	out.WriteString(summaryIndent)
	out.WriteString(finalMarker(true, colourize))

	return out.String()
}

func computeLayout(module string, outcomes map[string]*packageOutcome, packages []string) summaryLayout {
	var layout summaryLayout

	for _, pkgPath := range packages {
		outcome := outcomes[pkgPath]
		layout.nameWidth = max(layout.nameWidth, len(relativePackage(module, pkgPath)))
		layout.passedWidth = max(layout.passedWidth, len(strconv.Itoa(outcome.passed)))

		if skipped := outcome.skipped(); skipped > 0 {
			layout.skippedWidth = max(layout.skippedWidth, len(strconv.Itoa(skipped)))
		}
	}

	return layout
}

func sortedPackages(outcomes map[string]*packageOutcome) []string {
	packages := make([]string, 0, len(outcomes))
	for pkgPath := range outcomes {
		packages = append(packages, pkgPath)
	}

	slices.Sort(packages)

	return packages
}

func writeHeader(out *strings.Builder, sty style, packages int) {
	out.WriteString(summaryIndent)
	out.WriteString(sty.bold("wr test suite"))
	out.WriteString(" ")
	out.WriteString(sty.dim(sty.bullet() + " " + pluralPackages(packages)))
	out.WriteString("\n\n")
}

func writePackageLine(out *strings.Builder, sty style, relPkg string, outcome *packageOutcome, layout summaryLayout) {
	marker := sty.green(sty.pass())
	if outcome.skipped() > 0 {
		marker = sty.yellow(sty.skip())
	}

	out.WriteString(summaryIndent)
	out.WriteString(markerField(sty, marker))
	out.WriteString(relPkg)
	out.WriteString(strings.Repeat(" ", layout.nameWidth-len(relPkg)+minColumnGap))
	out.WriteString(passedField(sty, outcome.passed, layout.passedWidth))
	writeSkippedField(out, sty, outcome.skipped(), layout.skippedWidth)
	out.WriteByte('\n')

	for _, description := range outcome.skipOrder {
		writeSkipDescription(out, sty, description, outcome.skipCounts[description])
	}
}

// markerField renders the per-package marker glyph followed by a single space,
// keeping a constant two-column field so names stay aligned even in plain mode,
// where the glyph is empty.
func markerField(sty style, marker string) string {
	if !sty.rich {
		return "  "
	}

	return marker + " "
}

func passedField(sty style, passed int, width int) string {
	padded := strings.Repeat(" ", width-len(strconv.Itoa(passed))) + strconv.Itoa(passed)

	return sty.green(padded + " passed")
}

func writeSkippedField(out *strings.Builder, sty style, skipped int, width int) {
	if skipped == 0 {
		return
	}

	padded := strings.Repeat(" ", width-len(strconv.Itoa(skipped))) + strconv.Itoa(skipped)

	out.WriteString(strings.Repeat(" ", minColumnGap+1))
	out.WriteString(sty.yellow(padded + " skipped"))
}

func writeSkipDescription(out *strings.Builder, sty style, description string, count int) {
	line := sty.skipArrow() + " " + description
	if count > 1 {
		line += " " + sty.times() + strconv.Itoa(count)
	}

	out.WriteString(skipIndent)
	out.WriteString(sty.dim(line))
	out.WriteString("\n")
}

func relativePackage(module string, pkgPath string) string {
	if pkgPath == module {
		return "."
	}

	if rest, ok := strings.CutPrefix(pkgPath, module+"/"); ok {
		return rest
	}

	return pkgPath
}

func writeRule(out *strings.Builder, sty style) {
	out.WriteString(summaryIndent)
	out.WriteString(sty.dim(strings.Repeat(sty.rule(), ruleWidth)))
	out.WriteByte('\n')
}

func writeGrandTotal(out *strings.Builder, sty style, passed int, skipped int, packages int, elapsed time.Duration) {
	sep := " " + sty.dim(sty.bullet()) + " "

	parts := []string{sty.bold(strconv.Itoa(passed)) + " passed"}

	if skipped > 0 {
		parts = append(parts, sty.bold(strconv.Itoa(skipped))+" skipped")
	}

	parts = append(parts, sty.bold(pluralPackages(packages)), sty.dim(formatElapsed(elapsed)))

	out.WriteString(summaryIndent)
	out.WriteString(strings.Join(parts, sep))
	out.WriteString("\n")
}

// formatElapsed renders a wall-clock duration compactly: whole seconds as "52s"
// and sub-second runs as one decimal place, e.g. "0.3s".
func formatElapsed(elapsed time.Duration) string {
	if elapsed < time.Second {
		return strconv.FormatFloat(elapsed.Round(100*time.Millisecond).Seconds(), 'f', 1, 64) + "s"
	}

	return elapsed.Round(time.Second).String()
}

func pluralPackages(count int) string {
	if count == 1 {
		return "1 package"
	}

	return fmt.Sprintf("%d packages", count)
}

// finalMarker returns the trailing PASSED/FAILED marker with its glyph, coloured
// (bold green or bold red) only when colourize is set, plain otherwise.
func finalMarker(passed bool, colourize bool) string {
	sty := newStyle(colourize)

	if passed {
		return strings.TrimLeft(sty.boldGreen(sty.pass()+" PASSED"), " ") + "\n"
	}

	return strings.TrimLeft(sty.boldRed(sty.fail()+" FAILED"), " ") + "\n"
}

func laneSegments(lane laneSummaryInput) []segment {
	if lane.kind == LaneKindGoTest {
		return goTestSegments(lane.log)
	}

	return []segment{{pkg: lane.pkg, text: lane.log}}
}

// goTestSegments splits a multi-package "go test -v" log into one segment per
// package, using the trailing "ok/FAIL/? <pkg>" status lines as delimiters.
func goTestSegments(log string) []segment {
	segments := make([]segment, 0)

	var current strings.Builder

	for line := range strings.Lines(log) {
		pkgPath, tested, ok := goTestStatusPackage(line)
		if !ok {
			current.WriteString(line)

			continue
		}

		if tested {
			segments = append(segments, segment{pkg: pkgPath, text: current.String()})
		}

		current.Reset()
	}

	return segments
}

// goTestStatusPackage reports the package named on a "go test" status line, one
// of "ok  <pkg> <time>", "FAIL <pkg> ...", or "?   <pkg> [no test files]". The
// tested result is false for the "?" form, which has no tests to attribute.
func goTestStatusPackage(line string) (pkgPath string, tested bool, ok bool) {
	trimmed := strings.TrimRight(line, "\n")

	prefixes := map[string]bool{"ok  \t": true, "FAIL\t": true, "?   \t": false}

	for prefix, tested := range prefixes {
		if rest, found := strings.CutPrefix(trimmed, prefix); found {
			fields := strings.Fields(rest)
			if len(fields) > 0 {
				return fields[0], tested, true
			}
		}
	}

	return "", false, false
}

// packageOutcome accumulates the passing and skipped behaviours seen for one
// package across every lane that exercised it.
type packageOutcome struct {
	passed     int
	skipCounts map[string]int
	skipOrder  []string
}

func (o *packageOutcome) addSkip(description string) {
	if o.skipCounts == nil {
		o.skipCounts = make(map[string]int)
	}

	if _, seen := o.skipCounts[description]; !seen {
		o.skipOrder = append(o.skipOrder, description)
	}

	o.skipCounts[description]++
}

func (o *packageOutcome) skipped() int {
	total := 0
	for _, count := range o.skipCounts {
		total += count
	}

	return total
}

// segment pairs a package path with the slice of lane output attributed to it.
type segment struct {
	pkg  string
	text string
}

// summarizeFailureLog renders a failed lane's log for the focused error output:
// it drops the verbose-mode noise (run/pause/cont/name markers, passing or
// skipped result lines, and passing-package status lines) while keeping
// failures, panics, t.Log output, and the GoConvey failure context produced by
// formatLaneLog. Blank runs are collapsed last, after formatLaneLog has removed
// the JSON blocks that would otherwise leave gaps behind.
func summarizeFailureLog(raw string) string {
	return collapseBlankRuns(formatLaneLog(stripVerboseNoise(raw)))
}

func collapseBlankRuns(text string) string {
	var out strings.Builder

	blankRun := false

	for line := range strings.Lines(text) {
		blank := strings.TrimSpace(line) == ""
		if blank && blankRun {
			continue
		}

		blankRun = blank

		out.WriteString(line)
	}

	return out.String()
}

func stripVerboseNoise(raw string) string {
	var out strings.Builder

	for line := range strings.Lines(raw) {
		if isVerboseNoiseLine(line) {
			continue
		}

		out.WriteString(line)
	}

	return out.String()
}

// isVerboseNoiseLine reports whether a line is verbose-mode success noise that
// only distracts from a failure: the run/pause/cont/name markers, passing or
// skipped result lines, the bare PASS line, and the "ok"/"no test files" status
// lines of passing packages. Failure markers (FAIL, "--- FAIL:", "FAIL <pkg>")
// are always kept.
func isVerboseNoiseLine(line string) bool {
	trimmed := strings.TrimRight(line, "\n")
	stripped := strings.TrimLeft(trimmed, " \t")

	for _, prefix := range []string{"=== RUN", "=== PAUSE", "=== CONT", "=== NAME", "--- PASS:", "--- SKIP:"} {
		if strings.HasPrefix(stripped, prefix) {
			return true
		}
	}

	if stripped == "PASS" {
		return true
	}

	if _, _, ok := goTestStatusPackage(trimmed); ok && !strings.HasPrefix(trimmed, "FAIL") {
		return true
	}

	return false
}
