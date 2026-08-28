/*******************************************************************************
 * Copyright (c) 2016-2026 Genome Research Ltd.
 *
 * Author: Sendu Bala <sb10@sanger.ac.uk>
 * Author: [Theo Barber-Bany] <theobarberbany@gmail.com>
 * Author: Ashwini Chhipa <ac55@sanger.ac.uk>
 * Author: Rosie Kern
 * Author: Michael Woolnough <mw31@sanger.ac.uk>
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

package scheduler

// This file contains a scheduleri implementation for 'lsf': running jobs
// via IBM's (ne Platform's) Load Sharing Facility.

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"os/exec"
	"os/user"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/VertebrateResequencing/wr/backoff"
	"github.com/VertebrateResequencing/wr/bsubresource"
	"github.com/VertebrateResequencing/wr/clog"
	"github.com/VertebrateResequencing/wr/cloud"
	"github.com/VertebrateResequencing/wr/internal"
	"github.com/mattn/go-shellwords"
)

// scanBufferSize is used when scanning bjobs -w output. The default buffer size
// is 65536, but bjob names can be much bigger, so we allow for a larger buffer.
const scanBufferSize = 1000 * bufio.MaxScanTokenSize

const ErrInvalidBsubOpts = "invalid lsf bsub options"

// errBadLSFConfig is the Error message used when initialize() is not given a
// *ConfigLSF.
const errBadLSFConfig = "SchedulerConfig must be *ConfigLSF"

// queue criteria keys, used both as map keys when ranking queues and when
// applying default values.
const (
	criterionHosts     = "hosts"
	criterionChunkSize = "chunk_size"
	criterionNumUsers  = "num_users"
	criterionMax       = "max"
	criterionMaxUser   = "max_user"
	criterionMemlimit  = "memlimit"
	criterionRunlimit  = "runlimit"
	criterionUsers     = "users"
)

// memLimitMultiplier values for the different units that LSF_UNIT_FOR_LIMITS can
// report memlimit (bsub -M) values in. 'K' (KB) is our default.
const (
	kbMemLimitMultiplier float32 = 1000
	gbMemLimitMultiplier float32 = 0.001
)

// op* are the Op names used in scheduler Errors raised by the named methods.
const (
	opParseBjobs     = "parseBjobs"
	opDetermineQueue = "determineQueue"
	opParseUserArgs  = "parseUserArgs"
)

const (
	// reMatchOneGroup is the length of a FindStringSubmatch result for a regex
	// with a single capturing group (the full match plus the group).
	reMatchOneGroup = 2

	// reMatchTwoGroups is the length of a FindStringSubmatch result for a regex
	// with two capturing groups.
	reMatchTwoGroups = 3

	// secsPerMinute is the number of seconds in a minute.
	secsPerMinute = 60

	// manyUsers is the count used for a queue usable by everyone.
	manyUsers = 10000

	// oneYearInSeconds is the default runlimit used for queues that don't
	// specify one.
	oneYearInSeconds = 31536000

	// hugeQueueLimit is a "very large" default for queue criteria that have no
	// real limit, so they don't unduly affect queue ranking.
	hugeQueueLimit = 10000000

	// bjobsMinJobLineLen is the minimum length of a bjobs -w line for a
	// submitted job to be considered as having appeared in bjobs output.
	bjobsMinJobLineLen = 46

	// bjobsMinFields is the minimum number of whitespace-separated fields a
	// bjobs -w output line must have for us to parse the job id, status and
	// name from it.
	bjobsMinFields = 7

	// bmgroupMinFields is the minimum number of whitespace-separated fields a
	// bmgroup -w output line must have (a group name plus at least one host).
	bmgroupMinFields = 2

	// defaultBjobsAppearTimeout is the default bound on how long, after a
	// successful bsub, we wait for the submitted job to appear in bjobs output
	// before giving up. See bjobsAppearTimeout.
	defaultBjobsAppearTimeout = 10 * time.Second

	// bjobsAppearPollFreq is how often we poll bjobs while waiting for a
	// submitted job to appear.
	bjobsAppearPollFreq = 100 * time.Millisecond

	// defaultMaxBsubArraySize is the conservative default cap on the number of
	// elements wr puts in a single bsub job array (the common LSF MAX_JOB_ARRAY
	// default). See maxBsubArraySize.
	defaultMaxBsubArraySize = 1000

	// defaultBsubExecTimeout is the default bound on how long a single bsub exec
	// may take before it is killed and turned into a retryable error. See
	// bsubExecTimeout.
	defaultBsubExecTimeout = 5 * time.Minute

	// defaultBsubPipeCloseGrace is the default bound on how long a bsub exec may
	// take to close its output pipes after bsub itself is done. It is ADDED to
	// defaultBsubExecTimeout, not contained by it: a bsub the timeout killed whose
	// orphan still holds the pipe returns after the two together, so at a fifth of
	// the timeout it lengthens that worst case by 20% and no more. Meanwhile an
	// ordinary successful bsub, whose descendant on prod held the pipe past 2s,
	// stays nowhere near it. See bsubPipeCloseGrace.
	defaultBsubPipeCloseGrace = 1 * time.Minute

	// defaultMaxBkillBatchSize is the conservative default cap on the number of
	// LSF element ids wr hands a single bkill. It matches
	// defaultMaxBsubArraySize: an excess-runner kill is the mirror of the array
	// submission that created those elements, so the same batch size bounds both
	// sides with one obvious knob, keeps a single argv small (~12KB, far under
	// any ARG_MAX) and bounds how much work one bkill asks mbatchd for, which is
	// what makes bkillExecTimeout meaningful. See maxBkillBatchSize.
	defaultMaxBkillBatchSize = 1000

	// defaultBkillExecTimeout is the default bound on how long a single bkill
	// exec may take before it is abandoned. It is much shorter than
	// defaultBsubExecTimeout because the kill path runs inside a scheduling pass:
	// a bkill of at most defaultMaxBkillBatchSize ids that has not returned in a
	// minute is wedged, and waiting longer only delays excess-runner
	// reclamation. See bkillExecTimeout.
	defaultBkillExecTimeout = 1 * time.Minute

	// defaultBkillPipeCloseGrace is the default bound on how long a bkill exec may
	// take to close its output pipes after bkill itself is done. As with
	// defaultBsubPipeCloseGrace it is ADDED to the exec timeout, not contained by
	// it, so a wedged batch costs defaultBkillExecTimeout plus this; at a quarter of
	// that timeout the worst case stays the same order as the timeout the kill path
	// is already designed to tolerate inside a scheduling pass, while leaving many
	// times the 2s that proved too short on the bsub side ample room for the
	// descendant an ordinary bkill leaves behind. See bkillPipeCloseGrace.
	defaultBkillPipeCloseGrace = 15 * time.Second

	// defaultBjobsExecTimeout is the default bound on how long a single `bjobs -w`
	// exec may take before it is killed and turned into a retryable error. That
	// query lists every job in the invoking user's LSF account, not just wr's, so
	// how long it takes is set by a shared farm's queue depth rather than by
	// anything wr caps (the production incident this bound came from had 22,500+
	// foreign pending jobs) - and it runs inside every scheduling pass, so
	// whatever it costs is paid before that pass can submit or reclaim anything.
	// Three times bjobsAppearTimeout (which already assumes a bjobs round trip
	// finishes in well under 10s) leaves an honestly slow mbatchd ample room,
	// while half of defaultBkillExecTimeout keeps a wedged one from costing a pass
	// more than the kill path in the same pass already may. With
	// defaultBjobsPipeCloseGrace added, the worst case stays under the 60s
	// ClientMinRequestTimeout floor a stalled request is measured against. A stale
	// count for one pass is far cheaper than a stalled pass: schedule() turns the
	// error into a retry under the group's existing backoff. See bjobsExecTimeout.
	defaultBjobsExecTimeout = 30 * time.Second

	// defaultBjobsPipeCloseGrace is the default bound on how long a `bjobs -w`
	// exec may take to close its output pipe after bjobs itself is done. As with
	// defaultBsubPipeCloseGrace it is ADDED to the exec timeout, not contained by
	// it, so a wedged query costs defaultBjobsExecTimeout plus this; at a third of
	// that timeout the two together still fit under the 60s
	// ClientMinRequestTimeout floor, while leaving many times the 2s that proved
	// too short on the bsub side for the descendant a bjobs may leave behind. See
	// bjobsPipeCloseGrace.
	defaultBjobsPipeCloseGrace = 10 * time.Second

	// defaultKillBackoffMin is the default minimum interval that must pass before
	// wr re-issues a bkill for an element it has already asked LSF to kill. It is
	// comfortably longer than the few seconds bjobs takes to stop reporting a
	// killed element, so the normal case costs no repeat kill at all, while an
	// element that really is still there is retried promptly. See killBackoffMin.
	defaultKillBackoffMin = 30 * time.Second

	// defaultKillBackoffMax is the default ceiling of the re-kill interval. An
	// element that survives repeated kills means LSF is not acting on them, so wr
	// stops asking every cycle - but it never stops asking, so an over-provisioned
	// runner cannot be stranded un-reclaimed. See killBackoffMax.
	defaultKillBackoffMax = 5 * time.Minute

	// killBackoffFactor is the multiplier applied to the re-kill interval each
	// time a kill has to be repeated.
	killBackoffFactor = 2

	// killDeferralRetentionCeilings is how many killBackoffMax intervals past its
	// next-attempt deadline a deferral is kept before being swept: long enough
	// that an element which keeps needing killing keeps its escalation, short
	// enough that elements LSF has stopped reporting cannot accumulate.
	killDeferralRetentionCeilings = 2

	// bkillSummarySampleIDs is how many element ids the bounded kill summary log
	// includes as a sample. The whole id list is never logged: at prod scale that
	// was ~26KB per warn line and 75KB/min of manager log.
	bkillSummarySampleIDs = 3
)

// maxBsubArraySize caps the number of elements wr places in a single bsub job
// array (-J name[1-N]). A same-requirement batch larger than this is split
// across several arrays submitted in one scheduling pass, so an oversized
// single array (which some LSF installations accept but then hang on for
// minutes) is never emitted. It is a package var so tests can lower it.
var maxBsubArraySize = defaultMaxBsubArraySize //nolint:gochecknoglobals

// bsubExecTimeout bounds how long a single bsub invocation may run before it is
// killed, turning a hung/pathological bsub into a logged, retryable error
// rather than a silent indefinite block. It is deliberately applied via a
// context derived from context.Background() (not the scheduling context), so
// scheduling-context cancellation cannot abort an in-flight submission. It is a
// package var so tests can lower it.
var bsubExecTimeout = defaultBsubExecTimeout //nolint:gochecknoglobals

// bsubPipeCloseGrace bounds how long we wait for bsub's output pipes to be closed
// once bsub itself is done, before force-closing them so the exec returns.
//
// It is the exec.Cmd.WaitDelay of EVERY bsub, not only one the exec timeout
// killed: Go starts that timer "when either the associated Context is done or a
// call to Wait observes that the child process has exited, whichever occurs
// first", and when it fires on a bsub that "otherwise exited with a successful
// status", Output returns exec.ErrWaitDelay instead of nil. So this is a hard cap
// on how long a bsub that worked may take to close its stdout, which is why the
// value alone cannot make it safe: submitToQueue must (and does) accept an
// ErrWaitDelay whose captured output names the array LSF took. At 2s, a real farm
// bsub whose descendant held the pipe past that had wr reporting accepted array
// submissions as failed schedules (.docs/bugfixes/260824-1.md).
//
// It must stay non-zero: with WaitDelay unset, "I/O pipes will be read until EOF,
// which might not occur until orphaned subprocesses of the command have also
// closed their descriptors", so a killed bsub with an orphan on the pipe would
// block anyway and bsubExecTimeout would bound nothing
// (.docs/bugfixes/260722-1.md). It is a package var so tests can lower it.
var bsubPipeCloseGrace = defaultBsubPipeCloseGrace //nolint:gochecknoglobals

// maxBkillBatchSize caps the number of LSF element ids wr hands a single bkill
// (DEVELOPERS.md rule 7). A kill cycle with more excess elements than this is
// split into as many batches as needed, each its own bkill exec, so an unbounded
// argv (~1,900 ids measured on the live production manager, proportionally larger
// at higher limits) is never emitted. It is a package var so tests can lower it.
var maxBkillBatchSize = defaultMaxBkillBatchSize //nolint:gochecknoglobals

// bkillExecTimeout bounds how long a single bkill invocation may run before it is
// killed, so a hung bkill cannot block excess-runner reclamation indefinitely. As
// with bsubExecTimeout it is applied via a context derived from
// context.Background() rather than the scheduling context, so scheduling-context
// cancellation cannot abort a kill LSF has already been asked for. It is a
// package var so tests can lower it.
var bkillExecTimeout = defaultBkillExecTimeout //nolint:gochecknoglobals

// bkillPipeCloseGrace bounds how long we wait for bkill's output pipes to be
// closed once bkill itself is done, before force-closing them so the exec returns.
// As bsubPipeCloseGrace explains in full, this is the WaitDelay of EVERY bkill
// (Go starts the timer when the child exits, not only when the exec context is
// done), so a successful bkill whose descendant holds the pipes open comes back as
// exec.ErrWaitDelay - which killSummary.ranToCompletion therefore treats as the
// completed kill it is. It must stay non-zero for bkillExecTimeout to bound
// anything. It is a package var so tests can lower it.
var bkillPipeCloseGrace = defaultBkillPipeCloseGrace //nolint:gochecknoglobals

// bjobsExecTimeout bounds how long a single `bjobs -w` invocation may run before
// it is killed, so a wedged or pathologically slow mbatchd cannot stall a
// scheduling pass indefinitely. As with bsubExecTimeout it is applied via a
// context derived from context.Background() rather than the scheduling context,
// so scheduling-context cancellation cannot abort a query in flight.
//
// It bounds EVERY `bjobs -w` exec: the list query in parseBjobs, and the
// per-poll `bjobs -w <id>` appearance check in bjobAppeared, which had no bound
// at all until .docs/bugfixes/260827-2.md. It is a package var so tests can lower
// it.
var bjobsExecTimeout = defaultBjobsExecTimeout //nolint:gochecknoglobals

// bjobsPipeCloseGrace bounds how long we wait for `bjobs -w`'s output pipe to be
// closed once bjobs itself is done, before force-closing it so the exec returns.
// As bsubPipeCloseGrace explains in full, this is the WaitDelay of EVERY bjobs
// exec (Go starts the timer when the child exits, not only when the exec context
// is done), so a bjobs that delivered its whole list and then left a descendant
// on the pipe comes back as exec.ErrWaitDelay - which parseBjobs therefore treats
// as the complete read it is.
//
// It must stay non-zero, AND every bjobs exec must keep giving os/exec pipes it
// owns: only those does os/exec force-close, and without the force-close
// bjobsExecTimeout bounds nothing at all on the path this matters most - a bjobs
// whose descendant holds the pipe open (.docs/bugfixes/260722-1.md,
// .docs/bugfixes/260827-1.md). parseBjobs does that with an io.Writer Stdout (see
// bjobsLineParser) rather than Cmd.StdoutPipe, and bjobAppeared with
// CombinedOutput, whose pipes are os/exec's own. It is a package var so tests can
// lower it.
var bjobsPipeCloseGrace = defaultBjobsPipeCloseGrace //nolint:gochecknoglobals

// bjobsAppearTimeout bounds how long waitForBjob waits for a just-submitted job
// to appear in bjobs output before giving up, and so bounds the whole time
// submitToQueue - and the scheduling pass behind it - spends on that wait.
//
// waitForBjob enforces it on its OWN select rather than only on its polling
// goroutine's, because a poll already running cannot be interrupted: with the
// deadline only inside the goroutine, an appearance check that outlived the
// window extended the window by however long it took, which for an unbounded
// `bjobs -w <id>` was forever (.docs/bugfixes/260827-2.md). It is a package var so
// tests can lower it.
var bjobsAppearTimeout = defaultBjobsAppearTimeout //nolint:gochecknoglobals

// killBackoffMin and killBackoffMax bound the jittered exponential interval that
// must pass before wr re-issues a bkill for an element it has already asked LSF
// to kill (DEVELOPERS.md rules 7 and 8). They are package vars so tests can
// lower them.
var (
	killBackoffMin = defaultKillBackoffMin //nolint:gochecknoglobals
	killBackoffMax = defaultKillBackoffMax //nolint:gochecknoglobals
)

// bkillLineKind is the kind of per-element outcome a bkill output line reports.
type bkillLineKind int

const (
	bkillLineUnknown bkillLineKind = iota
	bkillLineKilled
	bkillLineGone
)

// bkillLineOutcome returns the element id a bkill output line reports on, and
// what it says happened to that element. The id is empty if the line reports
// neither.
func bkillLineOutcome(reID *regexp.Regexp, line string) (string, bkillLineKind) {
	kind := classifyBkillLine(line)
	if kind == bkillLineUnknown {
		return "", bkillLineUnknown
	}

	match := reID.FindStringSubmatch(line)
	if len(match) != reMatchOneGroup {
		return "", bkillLineUnknown
	}

	return match[1], kind
}

// classifyBkillLine says whether a bkill output line reports an element as being
// killed, as already gone, or as neither. The phrases must stay spelled exactly as
// LSF spells them (hence the nolint on LSF's American spelling below).
func classifyBkillLine(line string) bkillLineKind {
	switch {
	case strings.Contains(line, "No matching job found"),
		strings.Contains(line, "already finished"),
		strings.Contains(line, "is not found"):
		return bkillLineGone
	case strings.Contains(line, "is being terminated"),
		strings.Contains(line, "is being signaled"), //nolint:misspell // LSF's own message
		strings.Contains(line, "is being requeued"),
		strings.Contains(line, "has been sent"):
		return bkillLineKilled
	default:
		return bkillLineUnknown
	}
}

// lsfHost adapts the throwaway cloud.Server that lsf.getHost dials for a single
// confirm-dead ssh command so that Close() actually drops that server's ssh
// connection. Each getHost call makes a FRESH server; without closing it the
// dialled ssh client (its background goroutines and socket) would leak per check.
// (cloud.Server's own Close is a deliberate no-op, correct for the cached, shared
// servers cloud schedulers reuse, so a throwaway server needs this wrapper.)
type lsfHost struct {
	server *cloud.Server
}

// RunCmd runs the command on the underlying throwaway server.
func (h *lsfHost) RunCmd(ctx context.Context, cmd string, background bool) (stdout, stderr string, err error) {
	return h.server.RunCmd(ctx, cmd, background)
}

// Close drops the throwaway server's ssh connection so it does not leak.
func (h *lsfHost) Close(ctx context.Context) {
	h.server.CloseSSHConnections(ctx)
}

// killSummary is what one kill cycle achieved, so that it can be reported in one
// bounded log line instead of the whole id list.
type killSummary struct {
	requested   int    // elements handed to a bkill
	killed      int    // elements bkill is terminating (or accepted silently)
	alreadyGone int    // elements LSF no longer knows about, so nothing to reclaim
	unaccounted int    // elements bkill neither killed nor explained
	retried     int    // due elements wr had already asked LSF to kill
	deferred    int    // excess elements the back-off left alone this cycle
	abandoned   int    // due elements not asked about, because a bkill did not complete
	batches     int    // bkill invocations
	lingered    bool   // a bkill exited cleanly but left a descendant on its pipes
	failure     string // why a bkill could not be run to completion
	exit        string // a bkill's non-zero exit status, or its lingering-pipe error
	out         string // a bounded excerpt of a failing bkill's output
}

// account classifies one bkill's output against the ids it was given. LSF reports
// what happened per element ("Job <id> is being terminated", "Job <id>: No
// matching job found"), so elements actually killed can be distinguished from
// elements that were already gone. Elements bkill said nothing about count as
// killed if it exited cleanly (it accepted the request; bkill -b can be silent)
// and as unaccounted otherwise - which is what stops a "No matching job found"
// hiding un-reclaimed over-provisioned runners. A bkill that exited cleanly and
// then lingered on its pipes (see lingeredOnPipes) still exited cleanly, and bkill
// exits non-zero if ANY element it was given was already gone, so its elements are
// credited as killed even though the output that would have said so may have been
// cut short.
func (k *killSummary) account(ids []string, out string, err error) {
	unexplained := make(map[string]bool, len(ids))
	for _, id := range ids {
		unexplained[id] = true
	}

	k.accountLines(out, unexplained)

	if err == nil || lingeredOnPipes(err) {
		k.killed += len(unexplained)
	} else {
		k.unaccounted += len(unexplained)
	}
}

// lingeredOnPipes reports whether err says an LSF command did its job and then
// left a descendant on its output pipes. Go returns exec.ErrWaitDelay only when
// the command "otherwise exited with a successful status" and no cancellation
// happened, so the work it was asked to do was accepted; all this error adds is
// that something it spawned still held the inherited pipes open when the
// pipe-close grace expired, so os/exec closed them (see bsubPipeCloseGrace).
// Callers must therefore treat it as a success, or they report submissions and
// kills that really happened as failures.
func lingeredOnPipes(err error) bool {
	return errors.Is(err, exec.ErrWaitDelay)
}

// accountLines credits every element that a line of the given bkill output reports
// on, removing it from unexplained as it goes.
func (k *killSummary) accountLines(out string, unexplained map[string]bool) {
	reID := regexp.MustCompile(`Job <([^>]+)>`)

	for line := range strings.SplitSeq(out, "\n") {
		id, kind := bkillLineOutcome(reID, line)
		if id == "" || !unexplained[id] {
			continue
		}

		delete(unexplained, id)

		if kind == bkillLineKilled {
			k.killed++
		} else {
			k.alreadyGone++
		}
	}
}

// fail records that a bkill could not be run to completion, with a bounded
// excerpt of whatever it had said.
func (k *killSummary) fail(reason, out string) {
	if k.failure == "" {
		k.failure = reason
		k.out = loggableProcessOutput(out)
	}
}

// log reports the cycle in ONE bounded line: counts, a few sample ids and (only
// when something went wrong) a capped excerpt of bkill's output. It escalates to
// warn when something was left unexplained - elements bkill did not account for,
// batches abandoned by a bkill that never completed, a bkill that could not be
// run, or elements wr had to ask LSF about again (an element still reported as
// excess after wr already had it killed is the "lost slots never reclaimed"
// symptom, and the back-off means this can only be logged once per interval, not
// once per cycle). Everything else - including the non-zero exit LSF returns when
// some elements were simply already gone - is benign, and logged at debug.
func (k *killSummary) log(ctx context.Context, bkillExe string, due []string) {
	if !k.needsAttention() {
		clog.Debug(ctx, "checkCmd bkilled excess runners", k.logArgs(bkillExe, due)...)

		return
	}

	clog.Warn(ctx, "checkCmd bkill did not reclaim all excess runners", k.logArgs(bkillExe, due)...)
}

// needsAttention reports whether this cycle left something unexplained, and so
// should be logged at warn rather than debug. A bkill that lingered on its pipes
// counts: it is not a failure (its kills stand), but wr force-closed the pipes of
// a live descendant of an LSF command and may have read only part of what bkill
// said, so an operator running at the default log level has to be able to see it -
// and with the grace at defaultBkillPipeCloseGrace it can only happen rarely.
func (k *killSummary) needsAttention() bool {
	return k.unaccounted > 0 || k.abandoned > 0 || k.retried > 0 || k.failure != "" || k.lingered
}

// logArgs returns the bounded key/value pairs describing this cycle: counts, a
// sample of the ids, and (only when something needs attention) the reason and a
// capped excerpt of bkill's output. The full id list and the full output are never
// included.
func (k *killSummary) logArgs(bkillExe string, due []string) []any {
	args := []any{
		"cmd", bkillExe,
		"requested", k.requested,
		"killed", k.killed,
		"alreadyGone", k.alreadyGone,
		"unaccounted", k.unaccounted,
		"retried", k.retried,
		"deferred", k.deferred,
		"abandoned", k.abandoned,
		"batches", k.batches,
		"sample", bkillSample(due),
	}

	if k.exit != "" {
		args = append(args, "exit", k.exit)
	}

	if !k.needsAttention() {
		return args
	}

	if k.failure != "" {
		args = append(args, "err", k.failure)
	}

	if k.out != "" {
		args = append(args, "out", k.out)
	}

	return args
}

// bkillSample returns a bounded, readable sample of the given element ids: the
// first bkillSummarySampleIDs of them plus how many more there were, so a summary
// can name some ids without ever dumping the whole list.
func bkillSample(ids []string) string {
	if len(ids) <= bkillSummarySampleIDs {
		return strings.Join(ids, " ")
	}

	return fmt.Sprintf("%s +%d more", strings.Join(ids[:bkillSummarySampleIDs], " "),
		len(ids)-bkillSummarySampleIDs)
}

// ranToCompletion records how a bkill exec ended - execErr being its exec
// context's error (non-nil if the timeout fired) and err the error from running
// it - and reports whether bkill actually ran to completion.
func (k *killSummary) ranToCompletion(execErr, err error, out string) bool {
	if execErr != nil {
		k.fail(fmt.Sprintf("bkill timed out after %s", bkillExecTimeout), out)

		return false
	}

	if err == nil {
		return true
	}

	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) && !lingeredOnPipes(err) {
		k.fail(err.Error(), out)

		return false
	}

	// bkill exits non-zero if ANY element it was given was already gone, which is
	// normal; the counts say whether that is all it was. A lingering pipe is not a
	// failure either (see lingeredOnPipes) - the kills bkill reported still
	// happened, so the cycle's remaining batches must still be issued - but it is
	// recorded, and escalates the summary to warn, because the output it cut short
	// is the output those counts came from. Like the counts, the flag is sticky
	// across the cycle's batches: one batch that lingered must not be forgotten
	// because a later one exited normally.
	k.lingered = k.lingered || lingeredOnPipes(err)
	k.exit = err.Error()
	k.out = loggableProcessOutput(out)

	return true
}

// deferredSleeper is a backoff.Sleeper that records the duration it is asked to
// sleep for instead of sleeping, so a poll-driven caller can turn the house
// backoff's jittered exponential into a deadline. See lsf.nextKillInterval.
type deferredSleeper struct {
	nanos atomic.Int64
}

// Sleep records d and returns immediately.
func (d *deferredSleeper) Sleep(_ context.Context, dur time.Duration) {
	d.nanos.Store(int64(dur))
}

// recorded returns the duration most recently passed to Sleep.
func (d *deferredSleeper) recorded() time.Duration {
	return time.Duration(d.nanos.Load())
}

// bjobsLineParser is the Stdout of parseBjobs' bjobs exec: it splits the bytes
// os/exec copies out of bjobs into whole lines and hands each to parseBjobsLine
// as it arrives, so a long job list is never held in memory in full (every
// scheduler group can have its own bjobs -w in flight at once, and the list
// covers every job in the account).
//
// It is an io.Writer rather than a bufio.Scanner over Cmd.StdoutPipe because
// os/exec force-closes only the pipes it owns, and it owns them only when Stdout
// is not already an *os.File. With StdoutPipe, a scan blocked on a descendant
// that inherited the pipe is interrupted by neither the exec context nor
// WaitDelay (measured: a descendant holding the pipe for 30s outlasted a 2s exec
// context and a 0.5s WaitDelay in full), so the timeout would bound nothing on
// exactly the path it is there for. See bjobsPipeCloseGrace.
type bjobsLineParser struct {
	jobPrefix string
	callback  bjobsCB

	// partial holds the bytes of a line that the current chunk ended part-way
	// through, to be prepended to the next one.
	partial []byte

	// lines counts the lines parsed, for the lingering-pipe log.
	lines int

	// tooLong records that a single line exceeded scanBufferSize, the cap the
	// bufio.Scanner this replaced enforced. Parsing stops there, as the scanner's
	// bufio.ErrTooLong did, and parseBjobs fails.
	tooLong bool
}

// Write implements io.Writer for os/exec's copy out of bjobs' stdout. It never
// returns an error, so the copy is never cut short by us; an over-long line is
// recorded in tooLong for parseBjobs to fail on.
func (b *bjobsLineParser) Write(p []byte) (int, error) {
	n := len(p)

	for !b.tooLong {
		i := bytes.IndexByte(p, '\n')
		if i < 0 {
			b.partial = append(b.partial, p...)
			b.tooLong = len(b.partial) > scanBufferSize

			break
		}

		b.emit(p[:i])
		p = p[i+1:]
	}

	return n, nil
}

// emit hands one whole line, with any bytes buffered from earlier Writes
// prepended, to parseBjobsLine.
func (b *bjobsLineParser) emit(line []byte) {
	if len(b.partial) > 0 {
		b.partial = append(b.partial, line...)
		line = b.partial
	}

	b.lines++

	parseBjobsLine(string(line), b.jobPrefix, b.callback)

	b.partial = b.partial[:0]
}

// flush parses any final line bjobs did not terminate with a newline, as the
// bufio.Scanner this replaced would have. Only call it once the exec has
// completed with the whole list read, so a partial line is never mistaken for a
// whole one.
func (b *bjobsLineParser) flush() {
	if len(b.partial) == 0 {
		return
	}

	b.emit(nil)
}

// lsf is our implementer of scheduleri.
type lsf struct {
	config             *ConfigLSF
	months             map[string]int
	dateRegex          *regexp.Regexp
	bsubRegex          *regexp.Regexp
	memLimitMultiplier float32
	queues             map[string]map[string]int
	sortedqs           []string
	bsubExe            string
	bjobsExe           string
	bkillExe           string
	privateKey         string
	// reservedElements holds the scheduler element ids (jobid[index] form) that
	// wr has handed a job reservation to, so killExcessCmds never bkills them as
	// excess even before bjobs reports them as RUN. Guarded by reservedMu.
	reservedElements map[string]bool
	reservedMu       sync.Mutex
	// killDeferred holds, per element id wr has asked bkill to kill, the earliest
	// time wr may ask LSF to kill it again, so an identical failing kill is not
	// re-issued every scheduling cycle. killBackoff (with killSleeper) is the
	// house backoff that calculates those intervals. All are guarded by killMu and
	// only used by the excess-runner kill path.
	killDeferred map[string]time.Time
	killBackoff  *backoff.Backoff
	killSleeper  *deferredSleeper
	killSwept    time.Time // when killDeferred was last swept of long-expired entries
	killMu       sync.Mutex
}

// ConfigLSF represents the configuration options required by the LSF scheduler.
// All are required with no usable defaults.
type ConfigLSF struct {
	// Deployment is one of "development" or "production".
	Deployment string

	// Shell is the shell to use to run the commands to interact with your job
	// scheduler; 'bash' is recommended.
	Shell string

	// PrivateKeyPath is the path to your private key that can be used to ssh
	// to LSF farm nodes to check on jobs if they become non-responsive.
	PrivateKeyPath string
}

// initialize finds out about lsf's hosts and queues.
func (s *lsf) initialize(ctx context.Context, config any) error {
	conf, ok := config.(*ConfigLSF)
	if !ok {
		return Error{lsfScheduler, opInitialize, errBadLSFConfig}
	}

	s.config = conf

	// find the real paths to the main LSF exes, since thanks to wr's LSF
	// compatibility mode, might not be the first in $PATH
	s.bsubExe = internal.Which("bsub")
	s.bjobsExe = internal.Which("bjobs")
	s.bkillExe = internal.Which("bkill")

	s.setupMonthsAndRegexes()

	if err := s.detectMemLimitMultiplier(); err != nil {
		return err
	}

	//nolint:contextcheck // internal.Username manages its own context by design
	highest, err := s.parseBqueues()
	if err != nil {
		return err
	}

	s.rankQueues(highest)

	// if a job becomes lost, scheduler needs to ssh to the host to check on the
	// process, so we store our private key
	s.loadPrivateKey(ctx)

	return nil
}

// loadPrivateKey reads the configured private key (used to ssh to a job's host to
// confirm a lost job's process is really dead) into s.privateKey. A read failure
// is non-fatal - lost-job death-confirmation will simply fail - but when a key
// path was configured it is logged at warn (rather than silently swallowed), so
// an unreadable or mis-pathed key is diagnosable instead of leaving every ssh
// check to fail invisibly.
func (s *lsf) loadPrivateKey(ctx context.Context) {
	if s.config.PrivateKeyPath == "" {
		return
	}

	content, err := os.ReadFile(internal.TildaToHome(s.config.PrivateKeyPath))
	if err != nil {
		clog.Warn(ctx, "could not read the private key needed to confirm lost jobs are dead via ssh",
			"path", s.config.PrivateKeyPath, "err", err)

		return
	}

	s.privateKey = string(content)
}

// setupMonthsAndRegexes sets up what should be global vars, but we don't really
// want these taking up space if the user never uses LSF.
func (s *lsf) setupMonthsAndRegexes() {
	months := []string{"Jan", "Feb", "Mar", "Apr", "May", "Jun", "Jul", "Aug", "Sep", "Oct", "Nov", "Dec"}

	s.months = make(map[string]int, len(months))
	for i, month := range months {
		s.months[month] = i + 1
	}

	s.dateRegex = regexp.MustCompile(`(\w+)\s+(\d+) (\d+):(\d+):(\d+)`)
	s.bsubRegex = regexp.MustCompile(`^Job <(\d+)>`)
}

// detectMemLimitMultiplier uses lsadmin to see what units memlimit (bsub -M) is
// in, and sets s.memLimitMultiplier accordingly.
func (s *lsf) detectMemLimitMultiplier() error {
	s.memLimitMultiplier = kbMemLimitMultiplier // by default assume it's KB

	//nolint:gosec,noctx // LSF management command; must complete regardless of scheduling ctx cancellation
	cmdout, err := exec.Command(s.config.Shell, "-c", "lsadmin showconf lim | grep LSF_UNIT_FOR_LIMITS").Output()
	if err != nil {
		return Error{
			lsfScheduler, opInitialize,
			fmt.Sprintf("failed to run [lsadmin showconf lim | grep LSF_UNIT_FOR_LIMITS]: %s", err),
		}
	}

	if len(cmdout) == 0 {
		return nil
	}

	uflRegex := regexp.MustCompile(`=\s*(\w)`)

	unit := uflRegex.FindStringSubmatch(string(cmdout))
	if len(unit) == reMatchOneGroup && unit[1] != "" {
		switch unit[1] {
		case "M":
			s.memLimitMultiplier = 1
		case "G":
			s.memLimitMultiplier = gbMemLimitMultiplier
			// 'K' is our default
		}
	}

	return nil
}

// reserved records that the given scheduler element id (an LSF "jobid[index]")
// has been handed a wr job reservation, so killExcessCmds must never bkill it as
// excess, even before bjobs reports it as RUN.
func (s *lsf) reserved(schedulerID string) {
	s.reservedMu.Lock()
	defer s.reservedMu.Unlock()

	if s.reservedElements == nil {
		s.reservedElements = make(map[string]bool)
	}

	s.reservedElements[schedulerID] = true
}

// snapshotReserved returns a copy of the currently reserved element ids, safe to
// read without holding reservedMu.
func (s *lsf) snapshotReserved() map[string]bool {
	s.reservedMu.Lock()
	defer s.reservedMu.Unlock()

	snapshot := make(map[string]bool, len(s.reservedElements))
	for id := range s.reservedElements {
		snapshot[id] = true
	}

	return snapshot
}

// pruneReserved drops any reserved element ids not present in the given full
// snapshot of currently-known LSF element ids (parseBjobs excludes exited
// elements), bounding the reserved set over a long-lived manager.
func (s *lsf) pruneReserved(present map[string]bool) {
	s.reservedMu.Lock()
	defer s.reservedMu.Unlock()

	for id := range s.reservedElements {
		if !present[id] {
			delete(s.reservedElements, id)
		}
	}
}

// killElements asks LSF to kill the given excess LSF element ids. The ids are
// handed to bkill in batches of at most maxBkillBatchSize, each exec bounded by
// bkillExecTimeout (DEVELOPERS.md rule 7: cap what you hand external tools, and
// time-bound it), skipping elements wr has already asked LSF to kill recently
// (see dueForKill and nextKillInterval; rule 8), and the whole cycle produces ONE
// bounded summary log line rather than the entire id list (which cost ~75KB/min
// of manager log at prod scale).
//
// WHICH elements get killed is deliberately unchanged: the collector's decisions
// - including never handing bkill an element wr has given a job reservation to,
// and killExcessCmds' documented race trade-off of letting some cmds start and
// then killing them - are untouched, and the only elements left for a later cycle
// are ones wr has ALREADY asked LSF to kill.
func (s *lsf) killElements(ctx context.Context, ids []string) {
	due, sum := s.dueForKill(ids)
	if len(due) == 0 {
		clog.Debug(ctx, "checkCmd bkill of excess runners deferred", "deferred", sum.deferred)

		return
	}

	// the deferral deadline is calculated once per cycle, so it costs one backoff
	// step and one log line however many elements are being killed.
	deadline := time.Now().Add(s.nextKillInterval(ctx, sum.retried > 0))

	//nolint:contextcheck // bkillBatch deliberately takes no ctx: its exec must not be cancellable by the scheduling ctx
	for batch := range slices.Chunk(due, maxBkillBatchSize) {
		s.markKillsIssued(batch, deadline)

		sum.batches++

		if !s.bkillBatch(batch, &sum) {
			// this bkill never completed, so don't pile more onto a wedged LSF;
			// the batches not asked about are left un-deferred, so the next cycle
			// picks them up immediately.
			break
		}
	}

	sum.abandoned = len(due) - sum.requested

	sum.log(ctx, s.bkillExe, due)
}

// dueForKill splits the given excess element ids into those wr may bkill now
// (returned) and those deferred because wr has already asked LSF to kill them and
// the back-off interval has not passed yet (counted in the returned summary,
// along with how many of the due ids are repeat attempts). An element wr has
// never asked about is always due immediately, so newly over-provisioned runners
// are never left waiting.
//
// It also drops long-expired deferrals, bounding the map over a long-lived
// manager.
func (s *lsf) dueForKill(ids []string) ([]string, killSummary) {
	now := time.Now()

	s.killMu.Lock()
	defer s.killMu.Unlock()

	s.sweepKillDeferrals(now)

	var (
		due []string
		sum killSummary
	)

	for _, id := range ids {
		until, known := s.killDeferred[id]

		if known && now.Before(until) {
			sum.deferred++

			continue
		}

		if known {
			sum.retried++
		}

		due = append(due, id)
	}

	return due, sum
}

// sweepKillDeferrals drops deferrals whose next-attempt deadline passed more than
// a couple of back-off ceilings ago: LSF has not reported those elements as
// excess since, so they are gone and their state can go too. A deferral that is
// merely due is kept, so an element that keeps needing killing keeps escalating
// rather than starting again at killBackoffMin.
//
// It walks the whole map, so it is rate-limited to once per killBackoffMin: every
// scheduler group with excess elements calls the kill path every scheduling cycle,
// and this bounds memory, it does not need to be prompt. killMu must be held.
func (s *lsf) sweepKillDeferrals(now time.Time) {
	if now.Sub(s.killSwept) < killBackoffMin {
		return
	}

	s.killSwept = now
	retention := killDeferralRetentionCeilings * killBackoffMax

	for id, until := range s.killDeferred {
		if now.Sub(until) > retention {
			delete(s.killDeferred, id)
		}
	}
}

// markKillsIssued records that wr has just asked LSF to kill the given elements,
// so they will not be asked about again before the given deadline.
func (s *lsf) markKillsIssued(ids []string, deadline time.Time) {
	s.killMu.Lock()
	defer s.killMu.Unlock()

	if s.killDeferred == nil {
		s.killDeferred = make(map[string]time.Time, len(ids))
	}

	for _, id := range ids {
		s.killDeferred[id] = deadline
	}
}

// nextKillInterval returns how long wr will leave the elements it is about to
// bkill alone before asking LSF about them again. It is the house
// backoff.Backoff's jittered exponential (DEVELOPERS.md rule 8): it escalates
// from killBackoffMin towards killBackoffMax while kills keep having to be
// repeated, and resets to killBackoffMin as soon as a cycle needs no repeat. The
// ceiling means wr stops asking every cycle, but it never stops asking, so an
// over-provisioned runner cannot be stranded un-reclaimed.
//
// The wait is expressed as a DEADLINE rather than by actually sleeping, because
// this path is polled once per scheduling pass: a deadline needs no goroutine
// (parking one per element - thousands at prod scale - is the goroutine-storm
// anti-pattern reliable4 removed elsewhere) and cannot block the pass.
// backoff.Sleeper is the seam the package provides for deciding how the sleeping
// happens, and deferredSleeper simply records the duration.
func (s *lsf) nextKillInterval(ctx context.Context, repeating bool) time.Duration {
	s.killMu.Lock()

	if s.killBackoff == nil {
		s.killSleeper = &deferredSleeper{}
		s.killBackoff = &backoff.Backoff{
			Min:     killBackoffMin,
			Max:     killBackoffMax,
			Factor:  killBackoffFactor,
			Sleeper: s.killSleeper,
		}
	}

	b, sleeper := s.killBackoff, s.killSleeper

	s.killMu.Unlock()

	if !repeating {
		b.Reset()
	}

	// this does not sleep; it makes the Backoff work out the next interval and
	// hand it to our Sleeper. The duration is read back atomically because two
	// scheduler groups can be killing at once (either group's interval is a valid
	// one to use).
	b.Sleep(ctx)

	return sleeper.recorded()
}

// bkillBatch runs one bkill for the given element ids, recording in sum what it
// achieved. It returns false if that bkill could not be run to completion (it hit
// bkillExecTimeout, or could not be executed at all), in which case the caller
// must not issue further batches this cycle.
func (s *lsf) bkillBatch(ids []string, sum *killSummary) bool {
	// as in submitToQueue, the exec context is derived from context.Background()
	// rather than the scheduling ctx (which is why this takes no ctx), so
	// scheduling-context cancellation cannot abort a kill LSF has already been
	// asked for; bkillExecTimeout then bounds it so a hung bkill cannot block
	// excess-runner reclamation indefinitely.
	execCtx, cancel := context.WithTimeout(context.Background(), bkillExecTimeout)
	defer cancel()

	//nolint:gosec // LSF management command; execCtx is deliberately not the scheduling ctx (see above)
	killcmd := exec.CommandContext(execCtx, s.bkillExe, bkillArgs(ids)...)

	// WaitDelay bounds how long CombinedOutput() waits for bkill's pipes to close,
	// so a child still holding them open cannot stop the exec returning - whether
	// bkill was killed by the timeout above or exited on its own (see
	// bkillPipeCloseGrace).
	killcmd.WaitDelay = bkillPipeCloseGrace

	out, err := killcmd.CombinedOutput()

	sum.requested += len(ids)
	sum.account(ids, string(out), err)

	return sum.ranToCompletion(execCtx.Err(), err, string(out))
}

// bkillArgs returns the argv for a bkill of the given element ids. The -b flag is
// the one wr has always used, so LSF deals with a bulk kill request as fast as it
// can.
func bkillArgs(ids []string) []string {
	args := make([]string, 0, len(ids)+1)
	args = append(args, "-b")

	return append(args, ids...)
}

// runBsub runs bsub with the given args, returning its stdout and the error from
// running it.
//
// The exec context is derived from context.Background() rather than the scheduling
// ctx (which is why this takes no ctx), so scheduling-context cancellation cannot
// abort an in-flight submission, but it is bounded by bsubExecTimeout so a
// hung/oversized bsub becomes a retryable error instead of blocking this group's
// scheduling forever, and by bsubPipeCloseGrace so a child still holding the
// output pipe open cannot stop the exec returning either.
func (s *lsf) runBsub(bsubArgs []string) ([]byte, error) {
	execCtx, cancel := context.WithTimeout(context.Background(), bsubExecTimeout)
	defer cancel()

	//nolint:gosec // LSF job submission; execCtx is deliberately detached from the scheduling ctx (see above)
	bsubcmd := exec.CommandContext(execCtx, s.bsubExe, bsubArgs...)
	bsubcmd.WaitDelay = bsubPipeCloseGrace

	return bsubcmd.Output()
}

// bsubFailure returns the message describing a bsub that failed. bsub reports the
// real reason an LSF submission was rejected (e.g. a pending-job threshold or a
// restricted queue) on its stderr, while err itself carries only the bare exit
// status (typically "exit status 255"), so bsubStderr recovers that stderr and it
// is surfaced alongside, making the rejection diagnosable rather than opaque. When
// there is no stderr to recover the suffix is omitted rather than appending a
// misleading empty (bsub stderr: "") that would hide the real failure mode.
func (s *lsf) bsubFailure(bsubArgs []string, err error) string {
	msg := fmt.Sprintf("failed to run %s %s: %s", s.bsubExe, bsubArgs, err)

	if stderr := bsubStderr(err); stderr != "" {
		msg += fmt.Sprintf(" (bsub stderr: %q)", stderr)
	}

	return msg
}

// bsubStderr returns bsub's stderr, extracted from an error returned by
// exec.Cmd.Output(). When bsub starts but exits non-zero, Output() returns an
// *exec.ExitError whose Stderr field holds the captured stderr (captured because
// runBsub leaves Cmd.Stderr nil); that stderr carries the real LSF rejection
// reason. Any other error yields an empty string: bsub could not be executed at
// all, or it exited successfully and only its pipes went wrong (an
// exec.ErrWaitDelay carries no stderr, which is why the production log of
// .docs/bugfixes/260824-1.md showed none).
func bsubStderr(err error) string {
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return strings.TrimSpace(string(exitErr.Stderr))
	}

	return ""
}

// pollForBjob polls bjobs for the given job id until it appears, returning true,
// or the given window passes without it appearing, returning false. Each poll is
// itself bounded (see bjobAppeared), so an unanswered one cannot leave this
// running indefinitely after waitForBjob has given up on it.
func (s *lsf) pollForBjob(jobID string, window, execTimeout, pipeGrace time.Duration) bool {
	limit := time.After(window)

	ticker := time.NewTicker(bjobsAppearPollFreq)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			if s.bjobAppeared(jobID, execTimeout, pipeGrace) {
				return true
			}
		case <-limit:
			return false
		}
	}
}

// bqueuesParser holds the mutable state used while parsing the output of
// `bqueues -l` line by line.
type bqueuesParser struct {
	s                 *lsf
	highest           map[string]int
	bmgroups          map[string]map[string]bool
	queue             string
	nextIsMemlimit    int
	parsedBmgroups    bool
	nextIsPrio        bool
	lookingAtDefaults bool
	nextIsRunlimit    bool

	reQueue            *regexp.Regexp
	rePrio             *regexp.Regexp
	reDefaultLimits    *regexp.Regexp
	reDefaultsFinished *regexp.Regexp
	reMemlimit         *regexp.Regexp
	reNumUnit          *regexp.Regexp
	reRunLimit         *regexp.Regexp
	reParseRunlimit    *regexp.Regexp
	reUserHosts        *regexp.Regexp
	reChunkJobSize     *regexp.Regexp
}

// updateHighest records val as the highest value seen so far for htype.
func (p *bqueuesParser) updateHighest(htype string, val int) {
	if val > p.highest[htype] {
		p.highest[htype] = val
	}
}

// newBqueuesParser creates a bqueuesParser with its regexes compiled and its
// state ready to parse `bqueues -l` output for the given lsf scheduler.
func newBqueuesParser(s *lsf) *bqueuesParser {
	return &bqueuesParser{
		s: s,
		highest: map[string]int{
			criterionRunlimit: 0, criterionMemlimit: 0, criterionMax: 0,
			criterionMaxUser: 0, criterionUsers: 0, criterionHosts: 0,
		},
		bmgroups: make(map[string]map[string]bool),

		reQueue:            regexp.MustCompile(`^QUEUE: (\S+)`),
		rePrio:             regexp.MustCompile(`^PRIO\s+NICE\s+STATUS\s+MAX\s+JL\/U`),
		reDefaultLimits:    regexp.MustCompile(`^DEFAULT LIMITS:`),
		reDefaultsFinished: regexp.MustCompile(`^MAXIMUM LIMITS:|^SCHEDULING PARAMETERS`),
		reMemlimit:         regexp.MustCompile(`MEMLIMIT`),
		reNumUnit:          regexp.MustCompile(`(\d+(?:\.\d+)?) (\w)`),
		reRunLimit:         regexp.MustCompile(`RUNLIMIT`),
		reParseRunlimit:    regexp.MustCompile(`^\s*(\d+)(?:\.\d+)? min`),
		reUserHosts:        regexp.MustCompile(`^(USERS|HOSTS):\s+(.+?)\s*$`),
		reChunkJobSize:     regexp.MustCompile(`^CHUNK_JOB_SIZE:\s+(\d+)`),
	}
}

// parseBqueues parses bqueues -l to figure out what usable queues we have,
// populating s.queues and returning the highest value seen per criterion.
func (s *lsf) parseBqueues() (map[string]int, error) {
	// parse bqueues -l to figure out what usable queues we have
	//nolint:gosec,noctx // LSF management command; must complete regardless of scheduling ctx cancellation
	bqcmd := exec.Command(s.config.Shell, "-c", "bqueues -l")

	bqout, err := bqcmd.StdoutPipe()
	if err != nil {
		return nil, Error{lsfScheduler, opInitialize, fmt.Sprintf("failed to create pipe for [bqueues -l]: %s", err)}
	}

	if err = bqcmd.Start(); err != nil {
		return nil, Error{lsfScheduler, opInitialize, fmt.Sprintf("failed to start [bqueues -l]: %s", err)}
	}

	s.queues = make(map[string]map[string]int)
	p := newBqueuesParser(s)

	bqScanner := bufio.NewScanner(bqout)
	for bqScanner.Scan() {
		if err = p.parseLine(bqScanner.Text()); err != nil {
			return nil, err
		}
	}

	if serr := bqScanner.Err(); serr != nil {
		return nil, Error{lsfScheduler, opInitialize, fmt.Sprintf("failed to read everything from [bqueues -l]: %s", serr)}
	}

	if err = bqcmd.Wait(); err != nil {
		return nil, Error{lsfScheduler, opInitialize, fmt.Sprintf("failed to finish running [bqueues -l]: %s", err)}
	}

	return p.highest, nil
}

// parseLine processes a single line of `bqueues -l` output.
func (p *bqueuesParser) parseLine(line string) error {
	if matches := p.reQueue.FindStringSubmatch(line); len(matches) == reMatchOneGroup {
		p.queue = matches[1]
		p.s.queues[p.queue] = make(map[string]int)

		return nil
	}

	skip, err := p.parseQueueDetail(line)
	if err != nil || skip {
		return err
	}

	if err := p.parseUserHosts(line); err != nil {
		return err
	}

	return p.parseChunkSize(line)
}

// parseQueueDetail handles the prio/limits section of a queue's bqueues -l
// output. It returns skip=true when the rest of the line should not be examined
// (matching the original loop's `continue` behaviour).
func (p *bqueuesParser) parseQueueDetail(line string) (skip bool, err error) {
	switch {
	case p.queue == "":
		return true, nil
	case p.rePrio.MatchString(line):
		p.nextIsPrio = true

		return true, nil
	case p.nextIsPrio:
		return false, p.parsePrio(line)
	case p.reDefaultLimits.MatchString(line):
		p.lookingAtDefaults = true

		return true, nil
	case p.reDefaultsFinished.MatchString(line) || !p.lookingAtDefaults:
		p.lookingAtDefaults = false

		return p.parseLimits(line)
	}

	return false, nil
}

// parsePrio parses the PRIO/MAX/JL/U line for the current queue.
func (p *bqueuesParser) parsePrio(line string) error {
	fields := strings.Fields(line)

	var err error

	p.s.queues[p.queue]["prio"], err = strconv.Atoi(fields[0])
	if err != nil {
		return Error{lsfScheduler, opInitialize, fmt.Sprintf("failed to parse [bqueues -l]: %s", err)}
	}

	if err := p.parseOptionalLimit(fields[3], criterionMax); err != nil {
		return err
	}

	if err := p.parseOptionalLimit(fields[4], criterionMaxUser); err != nil {
		return err
	}

	p.nextIsPrio = false

	return nil
}

// parseOptionalLimit records field as the value of criterion for the current
// queue, unless field is "-" (meaning unset). field must be an integer.
func (p *bqueuesParser) parseOptionalLimit(field, criterion string) error {
	if field == "-" {
		return nil
	}

	i, err := strconv.Atoi(field)
	if err != nil {
		return Error{lsfScheduler, opInitialize, fmt.Sprintf("failed to parse [bqueues -l]: %s", err)}
	}

	p.s.queues[p.queue][criterion] = i
	p.updateHighest(criterion, i)

	return nil
}

// parseLimits handles the MEMLIMIT/RUNLIMIT section of a queue's output. It
// returns skip=true when the rest of the line should not be examined.
func (p *bqueuesParser) parseLimits(line string) (skip bool, err error) {
	switch {
	case p.reMemlimit.MatchString(line):
		p.nextIsMemlimit = 0
		for word := range strings.FieldsSeq(line) {
			p.nextIsMemlimit++

			if word == "MEMLIMIT" {
				break
			}
		}

		return true, nil
	case p.nextIsMemlimit > 0:
		return false, p.parseMemlimit(line)
	case p.reRunLimit.MatchString(line):
		p.nextIsRunlimit = true

		return true, nil
	case p.nextIsRunlimit:
		return false, p.parseRunlimit(line)
	}

	return false, nil
}

// parseMemlimit parses the memory limit value for the current queue.
func (p *bqueuesParser) parseMemlimit(line string) error {
	if matches := p.reNumUnit.FindAllStringSubmatch(line, -1); matches != nil && len(matches) >= p.nextIsMemlimit-1 {
		val, err := strconv.ParseFloat(matches[p.nextIsMemlimit-1][1], 32)
		if err != nil {
			return Error{lsfScheduler, opInitialize, fmt.Sprintf("failed to parse [bqueues -l]: %s", err)}
		}

		unit := matches[p.nextIsMemlimit-1][2]
		switch unit {
		case "T":
			val *= 1000000
		case "G":
			val *= 1000
		case "K":
			val /= 1000
		}

		p.s.queues[p.queue][criterionMemlimit] = int(val)
		p.updateHighest(criterionMemlimit, int(val))
	}

	p.nextIsMemlimit = 0

	return nil
}

// parseRunlimit parses the run limit value for the current queue.
func (p *bqueuesParser) parseRunlimit(line string) error {
	if matches := p.reParseRunlimit.FindStringSubmatch(line); len(matches) == reMatchOneGroup {
		mins, err := strconv.Atoi(matches[1])
		if err != nil {
			return Error{lsfScheduler, opInitialize, fmt.Sprintf("failed to parse [bqueues -l]: %s", err)}
		}

		p.s.queues[p.queue][criterionRunlimit] = mins * secsPerMinute
		// updateHighest(criterionRunlimit, ...) for queues that do not
		// specify a run limit, we won't base the default on the
		// highest value seen on other queues, but on a hard-coded 1
		// year
	}

	p.nextIsRunlimit = false

	return nil
}

// parseUserHosts handles USERS:/HOSTS: lines, recording how many users/hosts a
// queue has, and dropping queues the current user can't submit to.
func (p *bqueuesParser) parseUserHosts(line string) error {
	matches := p.reUserHosts.FindStringSubmatch(line)
	if len(matches) != reMatchTwoGroups {
		return nil
	}

	kind := strings.ToLower(matches[1])
	vals := strings.Fields(matches[2])

	if kind == criterionUsers {
		return p.parseUsers(vals)
	}

	if matches[2] != "all" {
		return p.parseHosts(kind, vals)
	}

	return nil
}

// parseUsers records the number of users that can use the current queue, and
// drops the queue if the current user can't use it.
func (p *bqueuesParser) parseUsers(vals []string) error {
	users := make(map[string]bool)
	for _, val := range vals {
		users[val] = true
	}

	p.s.queues[p.queue][criterionNumUsers] = len(users)
	if users["all"] {
		p.s.queues[p.queue][criterionNumUsers] = manyUsers
	}

	me, err := internal.Username()
	if err != nil {
		return Error{lsfScheduler, opInitialize, fmt.Sprintf("could not get current user: %s", err)}
	}

	if !users["all"] && !users[me] {
		delete(p.s.queues, p.queue)
		p.queue = ""
	}

	return nil
}

// parseHosts records the number of distinct hosts that back the current queue,
// expanding any host group names via bmgroup.
func (p *bqueuesParser) parseHosts(kind string, vals []string) error {
	hosts := make(map[string]bool)

	for _, val := range vals {
		if err := p.addHost(hosts, val); err != nil {
			return err
		}
	}

	p.s.queues[p.queue][kind] = len(hosts)
	p.updateHighest(kind, len(hosts))

	return nil
}

// addHost adds val to hosts. If val is a host group name (ends in "/"), it is
// looked up in bmgroup and expanded to its member hosts.
func (p *bqueuesParser) addHost(hosts map[string]bool, val string) error {
	if !strings.HasSuffix(val, "/") {
		hosts[val] = true

		return nil
	}

	// this is a group name, look it up in bmgroup
	if !p.parsedBmgroups {
		if perr := p.s.parseBmgroups(p.bmgroups); perr != nil {
			return perr
		}

		p.parsedBmgroups = true
	}

	val = strings.TrimSuffix(val, "/")
	if servers, exists := p.bmgroups[val]; exists {
		for server := range servers {
			hosts[server] = true
		}
	} else {
		hosts[val] = true
	}

	return nil
}

// parseChunkSize records the chunk job size of the current queue, if present.
func (p *bqueuesParser) parseChunkSize(line string) error {
	matches := p.reChunkJobSize.FindStringSubmatch(line)
	if len(matches) != reMatchOneGroup {
		return nil
	}

	chunks, err := strconv.Atoi(matches[1])
	if err != nil {
		return Error{lsfScheduler, opInitialize, fmt.Sprintf("failed to parse [bqueues -l]: %s", err)}
	}

	p.s.queues[p.queue][criterionChunkSize] = chunks

	return nil
}

// rankQueues fills in default criteria values for all queues, then sorts them
// so that those most likely to run jobs sooner come first in s.sortedqs.
func (s *lsf) rankQueues(highest map[string]int) {
	// for each criteria we're going to sort the queues on later, hard-code
	// [weight, sort-order]. We want to avoid chunked queues because that means
	// jobs will run sequentially instead of in parallel. For time and memory,
	// prefer the queue that is more limited, since we suppose they might be
	// less busy or will at least become free sooner
	criteriaHandling := map[string][]int{
		criterionHosts:     {18, 1}, // weight, sort order
		criterionMaxUser:   {10, 1},
		criterionMax:       {5, 1},
		"prio":             {1, 0},
		criterionChunkSize: {10000, 0},
		criterionRunlimit:  {1, 1},
		criterionMemlimit:  {1, 0},
		criterionNumUsers:  {15, 0},
	}

	s.applyQueueDefaults(highest)

	// sort the queues, those most likely to run jobs sooner coming first
	ranking := make(map[string]int)

	// instead of range over criteriaHandling, because max_user must come first
	for _, criterion := range []string{
		criterionMaxUser, criterionMax, criterionHosts,
		"prio", criterionChunkSize, criterionNumUsers, criterionRunlimit, criterionMemlimit,
	} {
		s.rankByCriterion(ranking, criterion, criteriaHandling[criterion])
	}

	s.sortedqs = internal.SortMapKeysByIntValue(ranking, false)

	// now s.sortedqs has [0] containing our default preferred order or queues,
	// and other numbers which can be tested against any global maximum number
	// of jobs that we should submit to LSF, and if lower than any of those
	// we prefer the order described there
	// *** we probably don't need this if we won't be having a global max
	// specified by the user
}

// applyQueueDefaults fills in default values for the criteria on all the
// queues, basing defaults on the highest value seen per criterion where one
// was seen.
func (s *lsf) applyQueueDefaults(highest map[string]int) {
	defaults := map[string]int{
		criterionNumUsers:  manyUsers,
		criterionRunlimit:  oneYearInSeconds,
		criterionMemlimit:  hugeQueueLimit,
		criterionMax:       hugeQueueLimit,
		criterionMaxUser:   hugeQueueLimit,
		criterionUsers:     hugeQueueLimit,
		criterionHosts:     hugeQueueLimit,
		criterionChunkSize: 0,
	}

	for criterion, highestVal := range highest {
		if highestVal > 0 {
			defaults[criterion] = highestVal + 1
		}
	}

	for _, qmap := range s.queues {
		for criterion, cdefault := range defaults {
			if _, wasSet := qmap[criterion]; !wasSet {
				qmap[criterion] = cdefault
			}
		}
	}
}

// rankByCriterion adds to each queue's entry in ranking based on its position
// when the queues are sorted by the given criterion. handling is the
// [weight, sort-order] pair for that criterion.
func (s *lsf) rankByCriterion(ranking map[string]int, criterion string, handling []int) {
	// sort queues by this criterion
	sorted := internal.SortMapKeysByMapIntValue(s.queues, criterion, handling[1] == 1)

	weight := handling[0]
	prevVal := -1
	rank := 0

	for _, queue := range sorted {
		val := s.queues[queue][criterion]
		if prevVal != -1 {
			diff := int(math.Abs(float64(val) - float64(prevVal)))
			if diff >= 1 {
				rank++
			}
		}

		ranking[queue] += rank * weight

		prevVal = val
	}
}

// parseBmgroups parses the output of `bmgroup`, storing group name as a key in
// the supplied map, with a map of hosts in that group as the value.
func (s *lsf) parseBmgroups(groups map[string]map[string]bool) error {
	//nolint:gosec,noctx // LSF management command; must complete regardless of scheduling ctx cancellation
	bmgcmd := exec.Command(s.config.Shell, "-c", "bmgroup -w")

	bmgout, err := bmgcmd.StdoutPipe()
	if err != nil {
		return Error{lsfScheduler, opInitialize, fmt.Sprintf("failed to create pipe for [bmgroup]: %s", err)}
	}

	if err = bmgcmd.Start(); err != nil {
		return Error{lsfScheduler, opInitialize, fmt.Sprintf("failed to start [bmgroup]: %s", err)}
	}

	bmgScanner := bufio.NewScanner(bmgout)
	for bmgScanner.Scan() {
		fields := strings.Fields(bmgScanner.Text())
		if len(fields) < bmgroupMinFields {
			continue
		}

		addBmgroup(groups, fields)
	}

	return nil
}

// addBmgroup records the hosts (fields after the first) belonging to the group
// named by fields[0], expanding any nested group references.
func addBmgroup(groups map[string]map[string]bool, fields []string) {
	group := fields[0]

	for _, field := range fields[1:] {
		if groups[group] == nil {
			groups[group] = make(map[string]bool)
		}

		if before, ok := strings.CutSuffix(field, "/"); ok {
			for server := range groups[before] {
				groups[group][server] = true
			}
		} else {
			groups[group][field] = true
		}
	}
}

// reserveTimeout achieves the aims of ReserveTimeout().
func (s *lsf) reserveTimeout(ctx context.Context, req *Requirements) int {
	if val, defined := req.Other["rtimeout"]; defined {
		timeout, err := strconv.Atoi(val)
		if err != nil {
			clog.Error(ctx, fmt.Sprintf("Failed to convert timeout to integer: %s", err))

			return defaultReserveTimeout
		}

		return timeout
	}

	return defaultReserveTimeout
}

// maxQueueTime achieves the aims of MaxQueueTime().
func (s *lsf) maxQueueTime(req *Requirements) time.Duration {
	queue, err := s.determineQueue(req)
	if err == nil {
		return time.Duration(s.queues[queue][criterionRunlimit]) * time.Second
	}

	return infiniteQueueTime
}

// schedule achieves the aims of Schedule(). Note that if rescheduling a cmd
// at a lower count, we cannot guarantee that only that number get run; it may
// end up being a few more.
func (s *lsf) schedule(ctx context.Context, cmd string, req *Requirements, _ uint8, count int) error {
	// use the given queue or find the best queue for these resource
	// requirements
	queue, err := s.determineQueue(req)
	if err != nil {
		return err // impossible to run cmd with these reqs
	}

	// get the details of everything already in the scheduler for this cmd,
	// removing from the queue anything not currently running when we're over
	// the desired count
	scheduledCount, err := s.checkCmd(ctx, cmd, count)
	if err != nil {
		return err
	}

	stillNeeded := count - scheduledCount
	if stillNeeded < 1 {
		return nil
	}

	// split stillNeeded across as many arrays of at most maxBsubArraySize as
	// required, submitting each in this one scheduling pass. An oversized single
	// array is never emitted (some LSF installations accept it but then hang for
	// minutes). Each array gets its own uniquified, cmd-correlated name (via
	// generateBsubName), so checkCmd/killExcessCmds keep working across chunks.
	for remaining := stillNeeded; remaining > 0; remaining -= maxBsubArraySize {
		chunk := remaining
		if chunk > maxBsubArraySize {
			chunk = maxBsubArraySize
		}

		bsubArgs := s.generateBsubArgs(ctx, queue, req, cmd, chunk)

		if err := s.submitToQueue(ctx, bsubArgs); err != nil {
			return err
		}
	}

	return nil
}

// submitToQueue runs bsub with the given args and waits until the submitted job
// appears in bjobs (see waitForBjob for why).
func (s *lsf) submitToQueue(ctx context.Context, bsubArgs []string) error {
	//nolint:contextcheck // runBsub deliberately takes no ctx: its exec must not be cancellable by the scheduling ctx
	bsubout, err := s.runBsub(bsubArgs)

	if err != nil && !lingeredOnPipes(err) {
		return Error{lsfScheduler, opSchedule, s.bsubFailure(bsubArgs, err)}
	}

	if err != nil {
		// bsub exited successfully but left a descendant holding its stdout open
		// past bsubPipeCloseGrace, so os/exec closed the pipe and returned
		// exec.ErrWaitDelay (see there). LSF took the submission all the same, so
		// this is not the failure it used to be reported as - but the output may
		// have been cut short, so it is not silent either.
		clog.Warn(ctx, "bsub exited successfully but left a descendant holding its output pipe open",
			"cmd", s.bsubExe, "grace", bsubPipeCloseGrace, "out", loggableProcessOutput(string(bsubout)))
	}

	matches := s.bsubRegex.FindStringSubmatch(string(bsubout))
	if len(matches) != reMatchOneGroup {
		return Error{lsfScheduler, opSchedule, fmt.Sprintf("bsub %s returned unexpected output: %s", bsubArgs, bsubout)}
	}

	if !s.waitForBjob(ctx, matches[1]) {
		return Error{lsfScheduler, opSchedule, "after running bsub, failed to find the submitted jobs in bjobs"}
	}

	return nil
}

// waitForBjob waits until the submitted job with the given id appears in bjobs
// -w output, returning true once it does, or false if it never appears within
// bjobsAppearTimeout.
//
// unfortunately, a job can be successfully submitted to the queue but not
// immediately appear in bjobs, and if it completes in less than a few seconds,
// it will never appear there (unless you supply bjobs the job id). This means
// that our busy() method, if called immediately after the schedule(), would
// return false, even though the job may actually be running. To solve this
// issue we wait until bjobs -w <jobid> is found and only then return. If a
// subsequent busy() call returns false, that means the job completed and we're
// really not busy.
func (s *lsf) waitForBjob(ctx context.Context, jobID string) bool {
	// the bounds are read here, on this goroutine, and handed to the poller
	// below: that poller can outlive this call (a poll in progress cannot be
	// interrupted) and they are package vars tests lower, so a poller reading
	// them itself would be reading state its caller has already moved on from.
	window, execTimeout, pipeGrace := bjobsAppearTimeout, bjobsExecTimeout, bjobsPipeCloseGrace

	// ready is buffered so that when the deadline below wins, the abandoned
	// poller's send cannot block it from returning (see bjobsAppearTimeout).
	ready := make(chan bool, 1)

	go func() {
		defer internal.LogPanic(ctx, "lsf scheduling", true)

		//nolint:contextcheck // each poll's exec is bounded by its own ctx (see bjobAppeared)
		ready <- s.pollForBjob(jobID, window, execTimeout, pipeGrace)
	}()

	select {
	case appeared := <-ready:
		return appeared
	case <-time.After(window):
		return false
	}
}

// bjobAppeared returns true if bjobs -w now reports the job with the given id.
//
// The exec is bounded by the given execTimeout plus pipeGrace, the package vars
// waitForBjob read for it, exactly as parseBjobs' list query is bounded. It had
// neither until .docs/bugfixes/260827-2.md, and since a poll in progress cannot
// be interrupted, one appearance check LSF never answered hung waitForBjob ->
// submitToQueue -> schedule() for as long as it liked, leaving that scheduler
// group's Scheduler.Schedule limiter held and so the group never scheduled again.
func (s *lsf) bjobAppeared(jobID string, execTimeout, pipeGrace time.Duration) bool {
	// as in parseBjobs, the exec timeout is applied via a context of its own,
	// derived from context.Background(), so cancelling the scheduling ctx cannot
	// abort a query wr has already asked LSF for (see bjobsExecTimeout).
	execCtx, cancel := context.WithTimeout(context.Background(), execTimeout)
	defer cancel()

	//nolint:gosec // LSF management command; execCtx is deliberately not the scheduling ctx (see above)
	bjcmd := exec.CommandContext(execCtx, s.bjobsExe, "-w", jobID)

	// WaitDelay bounds how long CombinedOutput waits for bjobs' output pipes to
	// close once bjobs itself is done, so a descendant that inherited them cannot
	// hold this exec open after the timeout has killed bjobs. It must stay
	// non-zero, and only works because CombinedOutput's pipes are os/exec's own
	// ones, which are the only ones os/exec force-closes (see
	// bjobsPipeCloseGrace).
	bjcmd.WaitDelay = pipeGrace

	// a bjobs that exited cleanly and merely left a descendant on its pipes
	// printed everything it meant to (see lingeredOnPipes), so its answer counts;
	// any other failure is just "not appeared yet", and the next poll asks again.
	bjout, err := bjcmd.CombinedOutput()
	if err != nil && !lingeredOnPipes(err) {
		return false
	}

	return len(bjout) > bjobsMinJobLineLen
}

// scheduled achieves the aims of Scheduled().
func (s *lsf) scheduled(ctx context.Context, cmd string) (int, error) {
	return s.checkCmd(ctx, cmd, -1)
}

// generateBsubArgs generates the appropriate bsub args for the given req and
// cmd and queue.
func (s *lsf) generateBsubArgs(ctx context.Context, queue string, req *Requirements, cmd string, needed int) []string {
	args, err := generateBsubArgs(queue, req, cmd, s.config.Deployment, needed, s.memLimitMultiplier)
	if err != nil {
		clog.Warn(ctx, err.Error())
	}

	return args
}

func generateBsubArgs(queue string, req *Requirements, cmd, deployment string,
	needed int, memLimitMultiplier float32) ([]string, error) {
	var bsubArgs []string

	megabytes := req.RAM
	m := float32(megabytes) * memLimitMultiplier

	bsubArgs = append(bsubArgs, "-q", queue, "-M", fmt.Sprintf("%0.0f", m),
		"-R", fmt.Sprintf("select[mem>%[1]d] rusage[mem=%[1]d] span[hosts=1]", megabytes))

	var err error

	if val, ok := req.Other["scheduler_misc"]; ok {
		var parts []string

		parts, err = parseUserArgs(val, strconv.FormatInt(int64(megabytes), 10))
		bsubArgs = append(bsubArgs, parts...)
	}

	if req.Cores > 1 {
		bsubArgs = append(bsubArgs, "-n", strconv.Itoa(int(math.Ceil(req.Cores))))
	}

	name := generateBsubName(cmd, deployment, needed)
	bsubArgs = append(bsubArgs, "-J", name, "-o", "/dev/null", "-e", "/dev/null", cmd)

	return bsubArgs, err
}

func parseUserArgs(userArgs, megabytes string) ([]string, error) {
	words, err := shellwords.Parse(userArgs)
	if err != nil {
		return nil, fmt.Errorf("scheduler misc option ignored since could not be parsed: %w", err)
	}

	for n := 0; n < len(words); n += 2 {
		if !strings.HasPrefix(words[n], "-") {
			return nil, Error{lsfScheduler, opParseUserArgs, ErrInvalidBsubOpts}
		}

		if words[n] != "-R" {
			continue
		}

		reqs, err := bsubresource.ParseBsubR(words[n+1])
		if err != nil {
			return nil, fmt.Errorf("scheduler misc option ignored since could not be parsed: %w", err)
		}

		reqs.ReplaceMemoryAndHosts(megabytes, "1")

		words[n+1] = reqs.String()
	}

	return words, nil
}

// for checkCmd() to work efficiently we must always set a job name that
// corresponds to the cmd. It must also be unique otherwise LSF would not
// start running jobs with duplicate names until previous ones complete.
func generateBsubName(cmd, deployment string, needed int) string {
	name := jobName(cmd, deployment, true)

	if needed > 1 {
		name += fmt.Sprintf("[1-%d]", needed)
	}

	return name
}

// BsubValidator provides a cacheable bsub argument validator.
type BsubValidator map[string]bool

// Validate takes a string of bsub options and confirms that we can understand
// them and the bsub will accept them.
func (s BsubValidator) Validate(opts, queue string) (valid bool) {
	var ok bool

	if valid, ok = s[opts]; ok {
		return valid
	}

	defer func() {
		s[opts] = valid
	}()

	args, err := generateBsubArgs(queue, &Requirements{
		RAM:   1,
		Other: map[string]string{"scheduler_misc": opts},
	}, "echo", "production", 1, 1)
	if err != nil {
		return false
	}

	//nolint:noctx // LSF validation command; must complete regardless of scheduling ctx cancellation
	cmd := exec.Command("bsub", args...)

	cmd.Env = append(os.Environ(), "BSUB_CHK_RESREQ=1")
	err = cmd.Run()
	valid = err == nil

	return valid
}

// recover achieves the aims of Recover(). We don't have to do anything, since
// when the cmd finishes running, LSF itself will clean up.
func (s *lsf) recover(_ context.Context, _ string, _ *Requirements, _ *RecoveredHostDetails) error {
	return nil
}

// busy returns true if there are any jobs with our jobName() prefix in any
// queue. It also returns true if the most recently submitted job is pending or
// running.
func (s *lsf) busy(ctx context.Context) bool {
	count, err := s.checkCmd(ctx, "", -1)
	if err != nil {
		// busy() doesn't return an error, so just assume we're busy
		return true
	}

	return count > 0
}

// determineQueue picks a queue, preferring ones that are more likely to run our
// job the soonest (amongst those that are capable of running it). If req.Other
// contains a scheduler_queue value, returns that instead.
func (s *lsf) determineQueue(req *Requirements) (string, error) {
	queues := s.sortedqs

	if queue, ok := req.Other["scheduler_queue"]; strings.Contains(queue, ",") {
		queues = strings.Split(queue, ",")
	} else if ok {
		return queue, nil
	}

	seconds := req.Time.Seconds() + minimumQueueTime.Seconds()

	var queuesToAvoid []string
	if req.Other["scheduler_queues_avoid"] != "" {
		queuesToAvoid = strings.Split(req.Other["scheduler_queues_avoid"], ",")
	}

	if queue, ok := s.firstSuitableQueue(queues, queuesToAvoid, req, seconds); ok {
		return queue, nil
	}

	return "", Error{lsfScheduler, opDetermineQueue, ErrImpossible}
}

// firstSuitableQueue returns the first queue (from the given preference-ordered
// list) that isn't to be avoided and has enough memory and time for req.
func (s *lsf) firstSuitableQueue(queues, queuesToAvoid []string, req *Requirements, seconds float64) (string, bool) {
	for _, queue := range queues {
		if queueShouldBeAvoided(queue, queuesToAvoid) {
			continue
		}

		if s.queueHasTooLittleMemory(queue, req) {
			continue
		}

		if s.queueHasTooLittleTime(queue, seconds) {
			continue
		}

		return queue, true
	}

	return "", false
}

func queueShouldBeAvoided(queue string, queuesToAvoid []string) bool {
	for _, queueToAvoid := range queuesToAvoid {
		if strings.Contains(queue, queueToAvoid) {
			return true
		}
	}

	return false
}

func (s *lsf) queueHasTooLittleMemory(queue string, req *Requirements) bool {
	return s.queues[queue][criterionMemlimit] > 0 && s.queues[queue][criterionMemlimit] < req.RAM
}

func (s *lsf) queueHasTooLittleTime(queue string, seconds float64) bool {
	return s.queues[queue][criterionRunlimit] > 0 && float64(s.queues[queue][criterionRunlimit]) < seconds
}

// checkCmd asks LSF how many of the supplied cmd are running, and if
// maxAllowed >= 0 is supplied, kills any extraneous non-running jobs for the
// cmd. If the supplied cmd is the empty string, it will report/act on all cmds
// submitted by schedule() for this deployment.
func (s *lsf) checkCmd(ctx context.Context, cmd string, maxAllowed int) (count int, err error) {
	// bjobs -w does not output a column for both array index and the command.
	// The LSF related modules on CPAN either just parse the command line output
	// or don't work. Ideally we'd use the C-API's lsb_readjobinfo call, but we
	// don't want to be troubled by compilation issues and different versions.
	// We REALLY don't want to manually parse the entire output of bjobs -l for
	// all jobs. Instead when submitting we'll have arranged that JOB_NAME be
	// set to jobName(cmd, ..., true), and in the case of a job array it will
	// have [array_index] appended to it. This lets us use a single bjobs -w
	// call to get all that we need. We can't use -J name to limit which jobs
	// bjobs -w reports on, since we may have submitted the cmd multiple times
	// as multiple different arrays, each with a uniqified job name. It gets
	// uniquified because otherwise none of the jobs in the second array would
	// start until the first array with the same name ended.
	// an empty cmd means we scan all of this deployment's wr jobs, giving a full
	// bjobs snapshot we can use to prune the reserved-element set.
	full := cmd == ""

	var jobPrefix string
	if full {
		// must match jobName's "wr<initial><token>_" layout so an isolated
		// manager's WR_JOBNAME_TOKEN-namespaced jobs are still recognised as ours.
		jobPrefix = jobNamePrefix(s.config.Deployment)
	} else {
		jobPrefix = jobName(cmd, s.config.Deployment, false)
	}

	if maxAllowed < 0 {
		return s.countCmds(ctx, jobPrefix, full)
	}

	return s.killExcessCmds(ctx, jobPrefix, maxAllowed)
}

// countCmds counts how many jobs with the given prefix are known to LSF. When
// full is true the prefix covers all of this deployment's wr jobs, so the
// scanned element ids are used to prune the reserved-element set (bounded
// memory over a long-lived manager).
func (s *lsf) countCmds(ctx context.Context, jobPrefix string, full bool) (count int, err error) {
	var (
		present map[string]bool
		reAid   *regexp.Regexp
	)

	if full {
		present = make(map[string]bool)
		reAid = regexp.MustCompile(`\[(\d+)\]$`)
	}

	cb := func(jobID, _, jobName string) {
		count++

		if full {
			if id := killableID(jobID, jobName, reAid); id != "" {
				present[id] = true
			}
		}
	}
	err = s.parseBjobs(ctx, jobPrefix, cb)

	if full {
		s.pruneReserved(present)
	}

	return count, err
}

// killCollector counts running cmds and collects the ids of non-running cmds
// beyond a maximum, so they can be killed.
type killCollector struct {
	reAid    *regexp.Regexp
	reserved map[string]bool
	// toKill holds just the element ids (killElements batches them into bkill
	// argvs, so no bkill flags belong here).
	toKill     []string
	count      int
	maxAllowed int
}

// consider counts the given job, and if we're now over maxAllowed and the job
// isn't running, records it for killing (and doesn't count it).
func (k *killCollector) consider(jobID, stat, jobName string) {
	k.count++
	if k.count > k.maxAllowed && stat != "RUN" {
		sidaid := killableID(jobID, jobName, k.reAid)

		if sidaid != "" && k.reserved[sidaid] {
			// wr has handed this element a job reservation; never kill it, even
			// though bjobs still reports it as non-RUN. Keep it counted toward
			// maxAllowed since it is effectively active.
			return
		}

		if sidaid != "" {
			k.toKill = append(k.toKill, sidaid)
		}

		k.count--
	}
}

// killExcessCmds counts how many jobs with the given prefix are known to LSF
// and kills any non-running ones beyond maxAllowed, returning the resulting
// count.
func (s *lsf) killExcessCmds(ctx context.Context, jobPrefix string, maxAllowed int) (count int, err error) {
	// to avoid a race condition where we collect id[index]s to kill here,
	// then later kill them all, though some may have started running by
	// then, we used to collect the jod ids now, then later bmod to allow
	// 0 running, then repeat the bjobs to find the ones to kill, kill them,
	// then bmod back to allowing lots to run. However, use of bmod resulted
	// in big rescheduling delays, and overall it seemed better (in terms of
	// getting jobs run quicker) to allow the race condition and allow some
	// cmds to start running and then get killed.
	kc := &killCollector{
		reAid:      regexp.MustCompile(`\[(\d+)\]$`),
		reserved:   s.snapshotReserved(),
		maxAllowed: maxAllowed,
	}

	err = s.parseBjobs(ctx, jobPrefix, kc.consider)

	if len(kc.toKill) > 0 {
		s.killElements(ctx, kc.toKill)
	}

	return kc.count, err
}

// killableID returns the submission-id[array-index] string to pass to bkill for
// the given bjobs job id and job name, or "" if one can't be determined.
func killableID(jobID, jobName string, reAid *regexp.Regexp) string {
	if strings.HasSuffix(jobID, "]") {
		return jobID
	}

	if aidmatch := reAid.FindStringSubmatch(jobName); len(aidmatch) == reMatchOneGroup {
		return jobID + "[" + aidmatch[1] + "]"
	}

	return ""
}

type bjobsCB func(jobID, stat, jobName string)

// parseBjobs runs bjobs, filters on a job name prefix, excludes exited jobs and
// gives columns 1 (JOBID), 3 (STAT) and 7 (JOB_NAME) to your callback for each
// bjobs output line.
//
// The exec is bounded by bjobsExecTimeout plus bjobsPipeCloseGrace, and anything
// that leaves the list incomplete is returned as an error rather than as a
// silently short set of callbacks: the counts those callbacks build (countCmds,
// killExcessCmds) decide whether wr submits more runners or kills some, so a
// short list would have it do the wrong one. schedule() turns the error into a
// retryable scheduling failure under the scheduler group's existing backoff.
func (s *lsf) parseBjobs(ctx context.Context, jobPrefix string, callback bjobsCB) error {
	// the exec timeout is applied via a context of its own, derived from
	// context.Background(), so cancelling the scheduling ctx cannot abort a query
	// wr has already asked LSF for (see bjobsExecTimeout).
	execCtx, cancel := context.WithTimeout(context.Background(), bjobsExecTimeout)
	defer cancel()

	//nolint:gosec,contextcheck // LSF management command; execCtx is deliberately not the scheduling ctx (see above)
	bjcmd := exec.CommandContext(execCtx, s.config.Shell, "-c", s.bjobsExe+" -w")

	// WaitDelay bounds how long Run() waits for bjobs' output pipe to close once
	// bjobs itself is done, so a descendant that inherited the pipe cannot hold
	// this exec open indefinitely (see bjobsPipeCloseGrace).
	bjcmd.WaitDelay = bjobsPipeCloseGrace

	lines := &bjobsLineParser{jobPrefix: jobPrefix, callback: callback}
	bjcmd.Stdout = lines

	err := bjcmd.Run()

	if err != nil && !lingeredOnPipes(err) {
		return Error{lsfScheduler, opParseBjobs, fmt.Sprintf("failed to run [bjobs -w]: %s", err)}
	}

	if err != nil {
		// bjobs exited by itself and then left a descendant holding its stdout
		// open past bjobsPipeCloseGrace, so os/exec closed the pipe and returned
		// exec.ErrWaitDelay (see there). The list is still complete - bjobs could
		// not have exited until everything it wrote had been consumed - so this is
		// not a failure, but an LSF command outliving its own exit is not silent
		// either (mirroring the same decision for bsub in
		// .docs/bugfixes/260824-1.md).
		clog.Warn(ctx, "bjobs exited successfully but left a descendant holding its output pipe open",
			"cmd", s.bjobsExe, "grace", bjobsPipeCloseGrace, "lines", lines.lines)
	}

	if lines.tooLong {
		return Error{lsfScheduler, opParseBjobs, fmt.Sprintf(
			"failed to read everything from [bjobs -w]: a line exceeded %d bytes", scanBufferSize)}
	}

	lines.flush()

	return nil
}

// parseBjobsLine passes the job id, status and name from a single bjobs -w line
// to callback, unless the line is too short, the job has exited, or its name
// doesn't have the given prefix.
func parseBjobsLine(line, jobPrefix string, callback bjobsCB) {
	fields := strings.Fields(line)
	if len(fields) <= bjobsMinFields {
		return
	}

	if fields[2] == "EXIT" || fields[2] == "DONE" || !strings.HasPrefix(fields[6], jobPrefix) {
		return
	}

	callback(fields[0], fields[2], fields[6])
}

// hostToID always returns an empty string, since we're not in the cloud.
func (s *lsf) hostToID(_ string) string {
	return ""
}

// getHost returns a Host backed by a fresh throwaway cloud.Server for the given
// host, whose Close() closes the ssh connection it dials.
func (s *lsf) getHost(host string) (Host, bool) {
	name := "unknown"
	if user, err := user.Current(); err == nil {
		name = user.Username
	}

	server := cloud.NewServer(name, host, s.privateKey)
	if server == nil {
		return nil, false
	}

	return &lsfHost{server: server}, true
}

// setMessageCallBack does nothing at the moment, since we don't generate any
// messages for the user.
func (s *lsf) setMessageCallBack(_ context.Context, _ MessageCallBack) {}

// setBadServerCallBack does nothing, since we're not a cloud-based scheduler.
func (s *lsf) setBadServerCallBack(_ context.Context, _ BadServerCallBack) {}

// cleanup bkills any remaining jobs we created.
func (s *lsf) cleanup(ctx context.Context) {
	toKill := []string{"-b"}
	cb := func(jobID, _, _ string) {
		toKill = append(toKill, jobID)
	}

	err := s.parseBjobs(ctx, jobNamePrefix(s.config.Deployment), cb)
	if err != nil {
		clog.Error(ctx, "cleanup parse bjobs failed", "err", err)
	}

	if len(toKill) > 1 {
		//nolint:gosec,noctx // LSF management command; must complete regardless of scheduling ctx cancellation
		killcmd := exec.Command(s.bkillExe, toKill...)

		err = killcmd.Run()
		if err != nil {
			clog.Warn(ctx, "cleanup bkill failed", "err", err)
		}
	}
}
