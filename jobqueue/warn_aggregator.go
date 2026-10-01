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
	"sync"
	"time"

	"github.com/VertebrateResequencing/wr/clog"
)

// warnRepeatedSuffix is appended to a warning's message for the line that
// summarises its repeats, so that one grep for the message finds both.
const warnRepeatedSuffix = " (repeated)"

// warnAggregateInterval is how often an aggregated warning may be logged in
// full for any one key. During prodsim round 4's 16 minutes of slow commits the
// manager logged 55k reservation warnings and 175k slow-request warnings, and
// the log handler's lock became 58% of the manager's mutex contention; at one
// line a minute plus one summary the same window logs a few dozen. It is a
// package var (not user-configurable) purely so tests can shorten it, and is
// read only when a Server is built.
//
//nolint:gochecknoglobals // internal tuning knob; a var only so tests can vary it
var warnAggregateInterval = time.Minute

// warnSummary is a warnWindow's summary line, ready to log.
type warnSummary struct {
	ctx  context.Context //nolint:containedctx // the ctx whose logger the line must use
	msg  string
	args []any
}

// warnWindow is one key's current interval.
type warnWindow struct {
	msg         string
	opened      time.Time
	repeats     int
	maxDuration time.Duration
	sampleCtx   context.Context //nolint:containedctx // the ctx whose logger the summary must use
	sample      []any
}

// summary returns the window's summary line, or nothing if the warning was not
// repeated in it.
func (w *warnWindow) summary(interval time.Duration) []warnSummary {
	if w.repeats == 0 {
		return nil
	}

	args := []any{"repeats", w.repeats, "since", w.opened, "interval", interval}
	if w.maxDuration > 0 {
		args = append(args, "maxDuration", w.maxDuration)
	}

	for i := 0; i+1 < len(w.sample); i += 2 {
		args = append(args, fmt.Sprintf("sample_%v", w.sample[i]), w.sample[i+1])
	}

	return []warnSummary{{ctx: w.sampleCtx, msg: w.msg + warnRepeatedSuffix, args: args}}
}

// warnAggregator rate-limits warnings that can fire for every job or request.
// Per key, the first occurrence in an interval is logged in full at once. The
// rest of that interval's occurrences are only counted, and logged as one
// summary line when the interval ends: the message with warnRepeatedSuffix, how
// many repeats there were, the longest duration reported, and the fields of the
// latest of them prefixed "sample_".
//
// A nil *warnAggregator logs every occurrence in full.
type warnAggregator struct {
	interval time.Duration
	now      func() time.Time

	mu      sync.Mutex
	windows map[string]*warnWindow
	timer   *time.Timer
	stopped bool
}

// newWarnAggregator returns an aggregator whose interval is the given one.
func newWarnAggregator(interval time.Duration) *warnAggregator {
	return &warnAggregator{
		interval: interval,
		now:      time.Now,
		windows:  make(map[string]*warnWindow),
	}
}

// warn logs, or counts, one occurrence of the warning msg for key, which
// should include msg if the aggregator is used for more than one warning. d is
// the occurrence's duration, if it has one worth reporting the maximum of, or 0.
func (a *warnAggregator) warn(ctx context.Context, msg, key string, d time.Duration, args ...any) {
	if a == nil {
		clog.Warn(ctx, msg, args...)

		return
	}

	a.mu.Lock()
	ended, counted := a.recordLocked(ctx, msg, key, d, args)
	a.mu.Unlock()

	if counted {
		return
	}

	logWarnSummaries(ended)
	clog.Warn(ctx, msg, args...)
}

// recordLocked counts an occurrence in key's open window, returning true, or
// else opens a new window for key (unless the aggregator has stopped),
// returning the summary of the window it replaces. a.mu must be held.
func (a *warnAggregator) recordLocked(ctx context.Context, msg, key string, d time.Duration,
	args []any) ([]warnSummary, bool) {
	w := a.windows[key]
	if w != nil && a.now().Sub(w.opened) < a.interval {
		w.repeats++
		w.maxDuration = max(w.maxDuration, d)
		w.sampleCtx = ctx
		w.sample = args

		return nil, true
	}

	var ended []warnSummary
	if w != nil {
		ended = w.summary(a.interval)
	}

	if !a.stopped {
		a.windows[key] = &warnWindow{msg: msg, opened: a.now()}
		a.scheduleFlushLocked()
	}

	return ended, false
}

// logWarnSummaries logs the given summary lines.
func logWarnSummaries(summaries []warnSummary) {
	for _, s := range summaries {
		clog.Warn(s.ctx, s.msg, s.args...)
	}
}

// scheduleFlushLocked makes sure a flush will run once the oldest window's
// interval ends. a.mu must be held.
func (a *warnAggregator) scheduleFlushLocked() {
	if a.timer != nil {
		return
	}

	a.timer = time.AfterFunc(a.interval, a.flush)
}

// flush logs the summaries of every window whose interval has ended and forgets
// those windows, rescheduling itself while any window remains.
func (a *warnAggregator) flush() {
	now := a.now()

	a.mu.Lock()

	a.timer = nil

	var ended []warnSummary

	for key, w := range a.windows {
		if now.Sub(w.opened) < a.interval {
			continue
		}

		ended = append(ended, w.summary(a.interval)...)
		delete(a.windows, key)
	}

	if len(a.windows) > 0 && !a.stopped {
		a.scheduleFlushLocked()
	}

	a.mu.Unlock()

	logWarnSummaries(ended)
}

// stop logs the summaries of every open window, stops the flush timer, and
// makes later occurrences log in full, so nothing is left uncounted once the
// server has shut down.
func (a *warnAggregator) stop() {
	if a == nil {
		return
	}

	a.mu.Lock()

	a.stopped = true

	if a.timer != nil {
		a.timer.Stop()
		a.timer = nil
	}

	var ended []warnSummary

	for key, w := range a.windows {
		ended = append(ended, w.summary(a.interval)...)
		delete(a.windows, key)
	}

	a.mu.Unlock()

	logWarnSummaries(ended)
}
