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
	"bufio"
	"context"
	"errors"
	"fmt"
	"math"
	"math/rand/v2"
	"os"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/VertebrateResequencing/wr/client"
	"github.com/VertebrateResequencing/wr/jobqueue"
	"github.com/inconshreveable/log15/v3"
)

const (
	filePerm         = 0o640
	maxLoggedErrLen  = 200
	clientTimeout    = 2 * time.Minute
	reconnectBackoff = 10 * time.Second
	minJobSecs       = 1.0
	cliTimeout       = 3 * time.Minute
	pcgStreamSalt    = 0x9e3779b97f4a7c15
)

// sim holds the shared state of one run.
type sim struct {
	cfg       config
	rng       *rand.Rand
	rngMu     sync.Mutex
	calls     *tsvWriter
	events    *tsvWriter
	samples   *tsvWriter
	start     time.Time
	submitted atomic.Int64
}

// newRand returns the run's deterministic, non-cryptographic random source.
func newRand(seed uint64) *rand.Rand {
	return rand.New(rand.NewPCG(seed, seed^pcgStreamSalt)) //nolint:gosec // workload shape, not security
}

func (s *sim) close() {
	for _, t := range []*tsvWriter{s.calls, s.events, s.samples} {
		if err := t.close(); err != nil {
			fmt.Fprintln(os.Stderr, "prodsim:", err)
		}
	}
}

// tsvWriter appends lines to a file, flushing each so a run that is killed
// still leaves complete lines behind.
type tsvWriter struct {
	mu sync.Mutex
	f  *os.File
	w  *bufio.Writer
}

// newTSV opens path for appending, writing header first if path is new.
func newTSV(path, header string) (*tsvWriter, error) {
	_, statErr := os.Stat(path)

	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, filePerm)
	if err != nil {
		return nil, err
	}

	t := &tsvWriter{f: f, w: bufio.NewWriter(f)}
	if statErr != nil {
		t.line(header)
	}

	return t, nil
}

// line appends s and a newline. A failed write only loses that measurement,
// so it is reported rather than stopping the run.
func (t *tsvWriter) line(s string) {
	t.mu.Lock()
	defer t.mu.Unlock()

	_, err := t.w.WriteString(s + "\n")
	if err == nil {
		err = t.w.Flush()
	}

	if err != nil {
		fmt.Fprintln(os.Stderr, "prodsim:", err)
	}
}

func (t *tsvWriter) close() error {
	t.mu.Lock()
	defer t.mu.Unlock()

	return errors.Join(t.w.Flush(), t.f.Close())
}

func (s *sim) since() float64 { return time.Since(s.start).Seconds() }

func (s *sim) event(actor, ev string) {
	s.events.line(fmt.Sprintf("%.1f\t%s\t%s", s.since(), actor, ev))
}

// callLine appends one row to calls.tsv.
func (s *sim) callLine(actor, op string, ms int64, n int, errStr string) {
	s.calls.line(fmt.Sprintf("%.1f\t%s\t%s\t%d\t%d\t%s", s.since(), actor, op, ms, n, errStr))
}

// measure runs fn, logging to calls.tsv how long it took, n (whatever count fn
// reports) and its error.
func (s *sim) measure(actor, op string, fn func() (int, error)) {
	t0 := time.Now()
	n, err := fn()

	s.callLine(actor, op, time.Since(t0).Milliseconds(), n, loggedErr(err))
}

// timed is measure, also returning fn's error.
func (s *sim) timed(actor, op string, fn func() (int, error)) error {
	var err error

	s.measure(actor, op, func() (int, error) {
		var n int

		n, err = fn()

		return n, err
	})

	return err
}

// loggedErr returns err's message fit for one TSV field, or "" for nil.
func loggedErr(err error) string {
	if err == nil {
		return ""
	}

	es := strings.NewReplacer("\t", " ", "\n", " ").Replace(err.Error())

	return es[:min(len(es), maxLoggedErrLen)]
}

func (s *sim) intn(n int) int {
	if n <= 0 {
		return 0
	}

	s.rngMu.Lock()
	defer s.rngMu.Unlock()

	return s.rng.IntN(n)
}

func (s *sim) float() float64 {
	s.rngMu.Lock()
	defer s.rngMu.Unlock()

	return s.rng.Float64()
}

// lognormal returns a value with the given median whose spread is sigma.
func (s *sim) lognormal(median, sigma float64) float64 {
	s.rngMu.Lock()
	defer s.rngMu.Unlock()

	return median * math.Exp(sigma*s.rng.NormFloat64())
}

// scaled returns n multiplied by the run's scale, at least 1.
func (s *sim) scaled(n int) int {
	return max(1, int(math.Round(float64(n)*s.cfg.scale)))
}

// sim converts a simulated duration to real time.
func (s *sim) sim(d time.Duration) time.Duration {
	return time.Duration(float64(d) / float64(time.Minute) * float64(s.cfg.simMinute))
}

// simSecs converts a simulated duration to the real seconds a job should run
// for, never under 1s, to one decimal place.
func (s *sim) simSecs(d time.Duration) float64 {
	const tenths = 10

	return math.Round(max(minJobSecs, s.sim(d).Seconds())*tenths) / tenths
}

// simMinutes converts a lognormal draw in simulated minutes to the real
// seconds a job should run for.
func (s *sim) simMinutes(median, sigma float64) float64 {
	return s.simSecs(time.Duration(s.lognormal(median, sigma) * float64(time.Minute)))
}

// sleep waits for real duration d or the end of the run, reporting false at
// the end of the run.
func sleep(ctx context.Context, d time.Duration) bool {
	select {
	case <-ctx.Done():
		return false
	case <-time.After(d):
		return true
	}
}

// jitter returns d scaled by a uniform factor in [0.5, 1.5).
func (s *sim) jitter(d time.Duration) time.Duration {
	const low = 0.5

	return time.Duration(float64(d) * (low + s.float()))
}

// retryConnect calls connect, timed as actor's op, until it succeeds or the run
// ends, when it returns the zero T.
func retryConnect[T any](ctx context.Context, s *sim, actor, op string, connect func() (T, error)) T {
	for {
		var conn T

		err := s.timed(actor, op, func() (int, error) {
			var err error

			conn, err = connect()

			return 0, err
		})
		if err == nil {
			return conn
		}

		if !sleep(ctx, reconnectBackoff) {
			var zero T

			return zero
		}
	}
}

// newScheduler connects a wr client the way ibackup and wrstat do, retrying
// until it works or the run ends (nil).
func (s *sim) newScheduler(ctx context.Context, actor string) *client.Scheduler {
	//nolint:contextcheck // client.New takes no context
	return retryConnect(ctx, s, actor, "connect", func() (*client.Scheduler, error) {
		return client.New(client.SchedulerSettings{
			Deployment: s.cfg.deployment,
			Cwd:        s.cfg.workDir,
			Queue:      s.cfg.queue,
			Timeout:    clientTimeout,
			Logger:     log15.New(),
		})
	})
}

// newJQ connects a raw jobqueue client, retrying until it works or the run
// ends (nil).
func (s *sim) newJQ(ctx context.Context, actor string) *jobqueue.Client {
	return retryConnect(ctx, s, actor, "connect", func() (*jobqueue.Client, error) {
		return jobqueue.ConnectUsingConfig(ctx, s.cfg.deployment, clientTimeout)
	})
}

// disconnect disconnects a client at the end of an actor, reporting failure.
func disconnect(d interface{ Disconnect() error }) {
	if err := d.Disconnect(); err != nil {
		fmt.Fprintln(os.Stderr, "prodsim: disconnect:", err)
	}
}

// jobCmd builds a harmless command: psimjob.sh <kind> <id> <secs> <memMB>
// <failPct> [extra...]. padBytes > 0 appends a shell comment of that length,
// the way production's portal commands carry ~25KB of escaped JSON args.
func (s *sim) jobCmd(kind, id string, secs float64, memMB, failPct, padBytes int, extra ...string) string {
	var b strings.Builder

	fmt.Fprintf(&b, "%s %s %s %.1f %d %d", s.cfg.jobScript, kind, id, secs, memMB, failPct)

	for _, e := range extra {
		b.WriteString(" " + e)
	}

	if padBytes > 0 {
		b.WriteString(" # ")

		pad := fmt.Sprintf(`{"file":"/lustre/scratch/projects/%s/%s/sample_%%06d.cram","md5":"0123456789abcdef"},`,
			kind, id)

		for i := 0; b.Len() < padBytes; i++ {
			fmt.Fprintf(&b, pad, i)
		}
	}

	return b.String()
}

// submit adds jobs, timed as actor's op; duplicates are not an error, since
// re-adding live jobs is what several production clients do.
func (s *sim) submit(actor, op string, sch *client.Scheduler, jobs []*jobqueue.Job) error {
	err := s.timed(actor, op, func() (int, error) {
		err := sch.SubmitJobs(jobs)
		if errors.Is(err, client.ErrDuplicateJobs) {
			err = nil
		}

		return len(jobs), err
	})
	if err == nil {
		s.submitted.Add(int64(len(jobs)))
	}

	return err
}

// wrCLI runs the isolated wr binary with args against our deployment, timed
// as actor's op, the way people and cron jobs do.
func (s *sim) wrCLI(ctx context.Context, actor, op string, args ...string) {
	s.measure(actor, op, func() (int, error) {
		cctx, cancel := context.WithTimeout(ctx, cliTimeout)
		defer cancel()

		args = append(args, "--deployment", s.cfg.deployment)

		out, err := exec.CommandContext(cctx, s.cfg.wrBin, args...).CombinedOutput() //nolint:gosec // our -wr binary
		if cctx.Err() != nil && err != nil {
			err = fmt.Errorf("timed out after %s: %w", cliTimeout, err)
		}

		return len(out), err
	})
}
