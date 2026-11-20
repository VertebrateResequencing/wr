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

// Soak additions: a container actor whose dependents wait on it through
// command dependencies (wr add cmd_deps / --cmd_deps), a dynamic portal mode
// that sizes each burst to keep a target concurrency busy (so a ramp can drive
// the farm load), and spike captures that save profiles while a call or ping
// is still slow.

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/VertebrateResequencing/wr/jobqueue"
)

const (
	defaultCtrEvery   = 30 * time.Minute
	defaultCtrJobs    = 20
	defaultPortalGap  = 5 * time.Minute
	defaultSpikeCall  = 15 * time.Second
	defaultSpikeGap   = 10 * time.Minute
	ctrRunTime        = time.Minute
	ctrDepRunTime     = 30 * time.Second
	ctrMemory         = "200M"
	ctrTime           = "20m"
	pingSpikeFraction = 10
)

const (
	// defaultPortalMaxIncomplete holds the live set near battery10's and
	// production's, about 300,000 jobs, however fast the store adds.
	defaultPortalMaxIncomplete = 300000
	// portalRecheck is the longest a paused dynamic portal waits between
	// counts of the incomplete jobs.
	portalRecheck = 30 * time.Second
)

// spikeExempt says which ops are slow by design, so never trigger a capture.
func spikeExempt(op string) bool {
	return op == "submit_and_wait" || op == "status_portal_summary"
}

// soakConfig holds the soak additions' flags.
type soakConfig struct {
	ctrImage            string
	ctrJobs             int
	ctrEvery            time.Duration // simulated
	portalTargetFile    string
	portalGap           time.Duration // real
	portalMedianMins    float64
	portalMaxIncomplete int
	operatorLimits      bool
	spikeCall           time.Duration
	spikeGap            time.Duration
}

func soakFlags(c *soakConfig) {
	flag.StringVar(&c.ctrImage, "ctr-image", "", "singularity image for container jobs (empty = no container actor jobs)")
	flag.IntVar(&c.ctrJobs, "ctr-jobs", defaultCtrJobs, "container jobs (and as many dependents) per batch")
	flag.DurationVar(&c.ctrEvery, "ctr-every", defaultCtrEvery, "simulated interval between container batches")
	flag.StringVar(&c.portalTargetFile, "portal-target-file", "",
		"dynamic portal mode: every -portal-gap submit a burst sized to keep the concurrency in this file busy")
	flag.DurationVar(&c.portalGap, "portal-gap", defaultPortalGap, "real time between dynamic-mode portal bursts")
	flag.Float64Var(&c.portalMedianMins, "portal-median-mins", portalMedianMins,
		"median portal job run time (sim minutes)")
	flag.IntVar(&c.portalMaxIncomplete, "portal-max-incomplete", defaultPortalMaxIncomplete,
		"dynamic portal mode: skip bursts while the manager has more incomplete jobs than this (0 = no cap)")
	flag.BoolVar(&c.operatorLimits, "operator-limits", true, "let the operator change the portal limit")
	flag.DurationVar(&c.spikeCall, "spike-call", defaultSpikeCall,
		"a call this slow (or a ping a tenth of it) triggers an extra profile capture while it is still slow")
	flag.DurationVar(&c.spikeGap, "spike-gap", defaultSpikeGap, "minimum real time between spike captures")
}

// spiker rate-limits spike captures.
type spiker struct {
	mu   sync.Mutex
	last time.Time
	pp   *pprofClient
}

// incompleteCounter counts the manager's incomplete jobs for the dynamic
// portal over a raw client, connected on first use and again after a failure,
// so it survives the manager's restarts.
type incompleteCounter struct {
	s  *sim
	jq *jobqueue.Client
}

// count returns how many incomplete jobs the manager holds, timed as the
// portal's count_incomplete op. It is the per-state count `wr status -o
// counts` asks for, which walks the queue once without decoding any job.
func (c *incompleteCounter) count(ctx context.Context) (int, error) {
	var total int

	//nolint:contextcheck // a spike capture outlives the call
	err := c.s.timed(actorPortal, "count_incomplete", func() (int, error) {
		if c.jq == nil {
			jq, err := jobqueue.ConnectUsingConfig(ctx, c.s.cfg.deployment, c.s.cfg.clientTimeout)
			if err != nil {
				return 0, err
			}

			c.jq = jq
		}

		//nolint:contextcheck // no Context variant; the client's Timeout bounds it
		summaries, err := c.jq.GetStatusByRepGroupMatch("", jobqueue.RepGroupMatchExact, nil, false, false)
		if err != nil {
			c.close()

			return 0, err
		}

		total = incompleteTotal(summaries)

		return total, nil
	})

	return total, err
}

// incompleteTotal sums the counts of every state but complete.
func incompleteTotal(summaries map[string]*jobqueue.RepGroupStatus) int {
	total := 0

	for _, summary := range summaries {
		for state, n := range summary.Counts {
			if state != jobqueue.JobStateComplete {
				total += n
			}
		}
	}

	return total
}

// close disconnects the client, if connected.
func (c *incompleteCounter) close() {
	if c.jq != nil {
		disconnect(c.jq)
		c.jq = nil
	}
}

func writeJSONLine(b *strings.Builder, v any) {
	j, err := json.Marshal(v)
	if err != nil {
		panic(err) // a struct of strings always marshals
	}

	b.Write(j)
	b.WriteByte('\n')
}

// armSpike returns a stop func; if it is not called within d, a spike capture
// naming what is taken, unless one was taken within -spike-gap.
func (s *sim) armSpike(what string, d time.Duration) func() bool {
	if s.cfg.pprofAddr == "" || d <= 0 {
		return func() bool { return true }
	}

	t := time.AfterFunc(d, func() { s.spike(what, d) })

	return t.Stop
}

func (s *sim) spike(what string, d time.Duration) {
	s.spk.mu.Lock()

	pp := s.spk.pp
	if pp == nil || (!s.spk.last.IsZero() && time.Since(s.spk.last) < s.cfg.soak.spikeGap) {
		s.spk.mu.Unlock()

		return
	}

	s.spk.last = time.Now()
	s.spk.mu.Unlock()

	s.event("sampler", fmt.Sprintf("spike %s still running after %s; capturing profiles", what, d))
	s.captureProfilesTagged(context.Background(), pp, "spike.")
}

// portalTarget reads the target concurrency from the dynamic-mode file, or
// returns def.
func (s *sim) portalTarget(def int) int {
	b, err := os.ReadFile(s.cfg.soak.portalTargetFile)
	if err != nil {
		return def
	}

	v, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil || v <= 0 {
		return def
	}

	return v
}

// portalDynamic submits, every -portal-gap, a two-phase burst big enough to
// keep the target concurrency busy for the gap.
func (s *sim) portalDynamic(ctx context.Context) {
	const actor = actorPortal

	sch := s.newScheduler(ctx, actor)
	if sch == nil {
		return
	}

	defer disconnect(sch)

	sigma2 := portalSigma * portalSigma / 2 //nolint:mnd // lognormal mean factor exp(sigma^2/2)
	meanSecs := s.sim(time.Duration(s.cfg.soak.portalMedianMins*float64(time.Minute))).Seconds() * math.Exp(sigma2)

	counter := &incompleteCounter{s: s}
	defer counter.close()

	for {
		if !s.portalWaitUnderMax(ctx, counter.count) {
			return
		}

		t0 := time.Now()
		target := s.portalTarget(s.cfg.portalLimit)
		n := max(1, int(float64(target)*s.cfg.soak.portalGap.Seconds()/meanSecs))
		ts := t0.Format("20060102T150405")

		for phase, name := range []string{"dedupe", "compress"} {
			if !s.portalPhase(ctx, actor, sch, ts, n, phase, name) {
				return
			}
		}

		s.event(actor, fmt.Sprintf("burst %s jobs=%d x2 cmdKB=%d target=%d", ts, n, s.cfg.portalCmdKB, target))

		if !sleep(ctx, max(time.Second, s.cfg.soak.portalGap-time.Since(t0))) {
			return
		}
	}
}

// portalWaitUnderMax waits until count says the manager has at most
// -portal-max-incomplete incomplete jobs, counting again every -portal-gap or
// portalRecheck, whichever is shorter, but at most once a second. It logs a
// pause event when it first finds too many and a resume event when it stops
// waiting. A count that fails is waited out like too many, since the burst
// could not be added either. It returns false if the run ends first. With no
// cap it returns true at once.
func (s *sim) portalWaitUnderMax(ctx context.Context, count func(context.Context) (int, error)) bool {
	limit := s.cfg.soak.portalMaxIncomplete
	if limit <= 0 {
		return true
	}

	paused := false

	for {
		if n, err := count(ctx); err == nil {
			paused = s.portalCapEvent(paused, n, limit)
			if !paused {
				return true
			}
		}

		if !sleep(ctx, max(time.Second, min(s.cfg.soak.portalGap, portalRecheck))) {
			return false
		}
	}
}

// portalCapEvent logs a pause if a count of n incomplete jobs is over limit and
// the portal was not paused, or a resume if it is not over and the portal was
// paused, and returns whether the portal is now paused.
func (s *sim) portalCapEvent(paused bool, n, limit int) bool {
	over := n > limit
	if over == paused {
		return paused
	}

	verb := "resume"
	if over {
		verb = "pause"
	}

	s.event(actorPortal, fmt.Sprintf("%s incomplete=%d max=%d", verb, n, limit))

	return over
}

// containers adds, every -ctr-every, a batch of singularity jobs that each
// write the time they finished to a file, and as many dependents that wait on
// them by command (cmd_deps) and record, via psimjob.sh's D marker, when their
// dependency finished, so a dependent that started too early can be counted.
// The last dependent of each batch is added with the --cmd_deps flag rather
// than the JSON key.
func (s *sim) containers(ctx context.Context) {
	const actor = "containers"

	if s.cfg.soak.ctrImage == "" {
		return
	}

	dir := filepath.Join(s.cfg.workDir, "ctr")
	if err := os.MkdirAll(dir, dirPerm); err != nil {
		s.event(actor, "could not make "+dir+": "+err.Error())

		return
	}

	for sleep(ctx, s.jitter(s.sim(s.cfg.soak.ctrEvery))) {
		ts := time.Now().Format("20060102T150405")
		n := max(1, s.scaled(s.cfg.soak.ctrJobs))
		ctrs, deps, lastDep := s.ctrBatch(dir, ts, n)

		s.wrCLIIn(ctx, actor, "add_ctr", ctrs, "add", "-f", "-", "--queue", s.cfg.queue)

		if deps != "" {
			s.wrCLIIn(ctx, actor, "add_ctrdep", deps, "add", "-f", "-", "--queue", s.cfg.queue)
		}

		s.wrCLIIn(ctx, actor, "add_ctrdep_flag", lastDep.Cmd+"\n", "add", "-f", "-", "--queue", s.cfg.queue,
			"--cmd_deps", lastDep.CmdDeps[0].Cmd+","+lastDep.CmdDeps[0].Cwd, "-i", lastDep.RepGrp,
			"-c", lastDep.Cwd, "--cwd_matters", "-m", ctrMemory, "-t", ctrTime)
		s.event(actor, fmt.Sprintf("batch %s jobs=%d dependents=%d", ts, n, n))
	}
}

// ctrJSON is one line of `wr add -f` input.
type ctrJSON struct {
	Cmd             string        `json:"cmd"`
	Cwd             string        `json:"cwd"`
	CwdMatters      bool          `json:"cwd_matters"`
	RepGrp          string        `json:"rep_grp"`
	Memory          string        `json:"memory"`
	Time            string        `json:"time"`
	Retries         int           `json:"retries"`
	WithSingularity string        `json:"with_singularity,omitempty"`
	CmdDeps         []ctrJSONDeps `json:"cmd_deps,omitempty"`
}

type ctrJSONDeps struct {
	Cmd string `json:"cmd"`
	Cwd string `json:"cwd"`
}

// ctrBatch returns n container jobs and all but the last of their dependents
// as `wr add -f` JSON lines, and the last dependent, which is added with the
// --cmd_deps flag. n must be at least 1.
func (s *sim) ctrBatch(dir, ts string, n int) (string, string, ctrJSON) {
	const minRunFrac = 0.5 // a container job runs for 0.5-1.5x ctrRunTime

	var ctrs, deps strings.Builder

	var lastDep ctrJSON

	for i := range n {
		done := filepath.Join(dir, fmt.Sprintf("c.%s.%d.done", ts, i))
		ccmd := fmt.Sprintf("sleep %.1f; date +%%s > %s", s.simSecs(ctrRunTime)*(minRunFrac+s.float()), done)
		writeJSONLine(&ctrs, ctrJSON{Cmd: ccmd, Cwd: s.cfg.workDir, CwdMatters: true, RepGrp: "ctr_" + ts,
			Memory: ctrMemory, Time: ctrTime, WithSingularity: s.cfg.soak.ctrImage})

		lastDep = ctrJSON{Cmd: s.jobCmd("ctrdep", fmt.Sprintf("%s.%d", ts, i), s.simSecs(ctrDepRunTime), 0, 0, 0, done),
			Cwd: s.cfg.workDir, CwdMatters: true, RepGrp: "ctrdep_" + ts, Memory: ctrMemory, Time: ctrTime,
			CmdDeps: []ctrJSONDeps{{Cmd: ccmd, Cwd: s.cfg.workDir}}}

		if i < n-1 {
			writeJSONLine(&deps, lastDep)
		}
	}

	return ctrs.String(), deps.String(), lastDep
}

// wrCLIIn is wrCLI with stdin.
func (s *sim) wrCLIIn(ctx context.Context, actor, op, stdin string, args ...string) {
	s.measure(actor, op, func() (int, error) { //nolint:contextcheck // a spike capture outlives the call
		cctx, cancel := context.WithTimeout(ctx, cliTimeout)
		defer cancel()

		args = append(args, "--deployment", s.cfg.deployment)
		cmd := exec.CommandContext(cctx, s.cfg.wrBin, args...) //nolint:gosec // our -wr binary
		cmd.Stdin = strings.NewReader(stdin)

		out, err := cmd.CombinedOutput()
		if err != nil {
			err = fmt.Errorf("%w: %s", err, strings.TrimSpace(string(out)))
		}

		return len(out), err
	})
}
