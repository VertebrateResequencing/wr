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
	"io"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/term"
)

const (
	spinnerFrames    = "⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏"
	spinnerInterval  = 100 * time.Millisecond
	clearLine        = "\r\x1b[2K"
	fallbackWidth    = 80
	runLinePrefix    = "=== RUN   "
	defaultPhaseText = "starting"
)

// progressState is the immutable snapshot the renderer turns into a frame. It is
// copied out under the progress mutex so renderFrame stays a pure function of
// its inputs and is therefore unit-testable in isolation.
type progressState struct {
	phase        string
	testing      bool
	spinnerIndex int
	lanesTotal   int
	lanesDone    int
	testsStarted int
	latestTest   string
}

// progress draws an ephemeral, animated one-line status indicator on a terminal
// while the suite runs. It is constructed around the stderr writer and is a
// complete no-op (no goroutine, no bytes written) when that writer is not a
// terminal, so pipes, files and CI logs stay clean.
type progress struct {
	out     io.Writer
	mu      sync.Mutex
	st      progressState
	quit    chan struct{}
	done    chan struct{}
	sty     style
	started bool
	stopped bool
}

// newProgress builds a progress bound to stderr. When stderr is not a terminal
// the returned value is an inert no-op: start, the state mutators and stop all
// do nothing and the writer wrappers pass through untouched.
func newProgress(stderr io.Writer, lanesTotal int) *progress {
	if !isTerminal(stderr) {
		return &progress{}
	}

	return &progress{
		out: stderr,
		st: progressState{
			phase:      defaultPhaseText,
			lanesTotal: lanesTotal,
		},
		quit: make(chan struct{}),
		done: make(chan struct{}),
		sty:  newStyle(true),
	}
}

// active reports whether this progress actually renders (stderr was a terminal).
func (p *progress) active() bool {
	return p != nil && p.out != nil
}

// start launches the background render loop. It is idempotent (a second call is
// a no-op, so only one loop ever runs) and safe to call on a no-op progress,
// where it returns immediately.
func (p *progress) start() {
	if !p.active() {
		return
	}

	p.mu.Lock()
	if p.started {
		p.mu.Unlock()

		return
	}

	p.started = true
	p.mu.Unlock()

	go p.loop()
}

func (p *progress) loop() {
	defer close(p.done)

	ticker := time.NewTicker(spinnerInterval)
	defer ticker.Stop()

	p.render()

	for {
		select {
		case <-p.quit:
			return
		case <-ticker.C:
			p.advance()
			p.render()
		}
	}
}

func (p *progress) advance() {
	p.mu.Lock()
	p.st.spinnerIndex++
	p.mu.Unlock()
}

func (p *progress) render() {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.draw()
}

// draw writes the current frame. The caller must hold the mutex; it is shared
// with bypass so an erase/redraw never interleaves with a real write.
func (p *progress) draw() {
	frame := renderFrame(p.st, p.sty, terminalWidth(p.out))
	_, _ = io.WriteString(p.out, clearLine+frame) //nolint:errcheck // best-effort terminal rendering.
}

// setPhase updates the label shown during the setup/compile phase.
func (p *progress) setPhase(phase string) {
	if !p.active() {
		return
	}

	p.mu.Lock()
	p.st.phase = phase
	p.mu.Unlock()
}

// beginTesting switches the indicator from the setup phase to the per-test
// phase that reports counts and the latest test function.
func (p *progress) beginTesting() {
	if !p.active() {
		return
	}

	p.mu.Lock()
	p.st.testing = true
	p.mu.Unlock()
}

// laneStarted records that one more lane is now running. The first lane to start
// also flips the indicator into the test phase, in case beginTesting was not
// called explicitly.
func (p *progress) laneStarted() {
	if !p.active() {
		return
	}

	p.mu.Lock()
	p.st.testing = true
	p.mu.Unlock()
}

// laneFinished records that one lane has completed.
func (p *progress) laneFinished() {
	if !p.active() {
		return
	}

	p.mu.Lock()
	p.st.lanesDone++
	p.mu.Unlock()
}

// testStarted records a newly started top-level test function, bumping the total
// and remembering its name for display.
func (p *progress) testStarted(name string) {
	if !p.active() {
		return
	}

	p.mu.Lock()
	p.st.testsStarted++
	p.st.latestTest = name
	p.mu.Unlock()
}

// stop halts the render loop and clears the line, leaving the cursor at column 0
// so the stdout summary printed next is not corrupted. It is idempotent and safe
// to call in any order: before start (nothing to wait on), repeatedly, or on a
// no-op progress.
func (p *progress) stop() {
	if !p.active() {
		return
	}

	p.mu.Lock()
	wait := p.started && !p.stopped
	p.stopped = true
	p.mu.Unlock()

	if !wait {
		return
	}

	close(p.quit)
	<-p.done

	_, _ = io.WriteString(p.out, clearLine) //nolint:errcheck // best-effort terminal rendering.
}

// bypass wraps an output writer so that anything written through it (such as
// compiler errors) first erases the spinner line; the next tick redraws it. When
// progress is a no-op the original writer is returned unchanged.
func (p *progress) bypass(writer io.Writer) io.Writer {
	if !p.active() {
		return writer
	}

	return &bypassWriter{progress: p, inner: writer}
}

// tee wraps a lane's log writer so the log receives every byte unchanged while
// complete lines are scanned for top-level "=== RUN" markers that feed the
// indicator. When progress is a no-op the original writer is returned unchanged,
// for a zero-overhead pass-through.
func (p *progress) tee(writer io.Writer) io.Writer {
	if !p.active() {
		return writer
	}

	return &runScanWriter{progress: p, inner: writer}
}

// bypassWriter erases the spinner line before each real write and writes
// through under the progress mutex, so compiler output is never garbled by an
// interleaved spinner redraw; the next tick repaints the line.
type bypassWriter struct {
	progress *progress
	inner    io.Writer
}

func (w *bypassWriter) Write(data []byte) (int, error) {
	w.progress.mu.Lock()
	defer w.progress.mu.Unlock()

	_, _ = io.WriteString(w.progress.out, clearLine) //nolint:errcheck // best-effort terminal rendering.

	return w.inner.Write(data)
}

// runScanWriter writes every byte through to the underlying log unchanged and,
// as a side effect, scans completed lines for top-level "=== RUN" markers to
// feed the indicator. The exec command uses the same instance for stdout and
// stderr, so os/exec serialises writes and the line buffer needs no extra lock.
type runScanWriter struct {
	progress *progress
	inner    io.Writer
	partial  []byte
}

func (w *runScanWriter) Write(data []byte) (int, error) {
	n, err := w.inner.Write(data)

	w.scan(data[:n])

	if err != nil {
		return n, err
	}

	return n, nil
}

// scan appends the written bytes to the partial-line buffer and reports the
// top-level test name of every complete line, keeping any trailing partial line
// for the next write so a "=== RUN" split across writes is still recognised.
func (w *runScanWriter) scan(data []byte) {
	w.partial = append(w.partial, data...)

	for {
		index := indexByte(w.partial, '\n')
		if index < 0 {
			return
		}

		line := string(w.partial[:index])
		w.partial = w.partial[index+1:]

		if name, ok := topLevelRunName(line); ok {
			w.progress.testStarted(name)
		}
	}
}

// topLevelRunName returns the function name of a top-level "=== RUN   <name>"
// line, rejecting subtests (whose name contains a "/") so only the parent
// functions are counted and displayed.
func topLevelRunName(line string) (string, bool) {
	rest, ok := strings.CutPrefix(strings.TrimRight(line, "\r"), runLinePrefix)
	if !ok {
		return "", false
	}

	name := strings.TrimSpace(rest)
	if name == "" || strings.Contains(name, "/") {
		return "", false
	}

	return name, true
}

func indexByte(data []byte, target byte) int {
	for i, b := range data {
		if b == target {
			return i
		}
	}

	return -1
}

// renderFrame turns a state snapshot into the single status line, truncated to
// width columns so it never wraps. It is a pure function: the ticker simply
// passes it the latest snapshot.
func renderFrame(state progressState, sty style, width int) string {
	spinner := sty.cyan(spinnerFrame(state.spinnerIndex))

	var body string
	if state.testing {
		body = renderTestingBody(state, sty)
	} else {
		body = sty.dim(state.phase + "…")
	}

	return truncateToWidth(spinner+" "+body, width)
}

func renderTestingBody(state progressState, sty style) string {
	sep := " " + sty.dim(sty.bullet()) + " "

	parts := []string{
		sty.bold(strconv.Itoa(state.testsStarted)) + sty.dim(" tests"),
		sty.bold(strconv.Itoa(state.lanesDone)+"/"+strconv.Itoa(state.lanesTotal)) + sty.dim(" lanes"),
	}

	body := strings.Join(parts, sep)

	if state.latestTest != "" {
		body += sep + sty.cyan(state.latestTest)
	}

	return body
}

func spinnerFrame(index int) string {
	frames := []rune(spinnerFrames)

	return string(frames[index%len(frames)])
}

// truncateToWidth limits text to width visible columns, ignoring ANSI escape
// sequences (which occupy no columns) so colour never eats into the budget.
func truncateToWidth(text string, width int) string {
	if width <= 0 {
		return text
	}

	var (
		out     strings.Builder
		columns int
		inEsc   bool
	)

	for _, r := range text {
		visible := visibleRune(r, &inEsc)

		if visible && columns >= width {
			continue
		}

		out.WriteRune(r)

		if visible {
			columns++
		}
	}

	return out.String()
}

// visibleRune advances the ANSI-escape state through inEsc and reports whether r
// is a normal visible rune that consumes a display column; runes that form an
// escape sequence are not visible and so never count against the width budget.
func visibleRune(r rune, inEsc *bool) bool {
	if *inEsc {
		*inEsc = r != 'm'

		return false
	}

	if r == '\x1b' {
		*inEsc = true

		return false
	}

	return true
}

func terminalWidth(writer io.Writer) int {
	file, ok := writer.(*os.File)
	if !ok {
		return fallbackWidth
	}

	width, _, err := term.GetSize(int(file.Fd()))
	if err != nil || width <= 0 {
		return fallbackWidth
	}

	return width
}
