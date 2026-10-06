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

// This file holds benchmarks for kicking buried jobs and resuming suspended
// ones while runners reserve from the same queue. A kick or resume updates each
// job and queues its write, which encodes the whole job, so a job with a large
// command costs real time per item; where that work happens relative to the
// queue's lock decides how long a concurrent reserve can be held up. Like the
// benchmarks in db_bench_test.go they are plain testing.B benchmarks, so only
// `go test -bench` runs them, never `make test`.

import (
	"context"
	"math/rand/v2"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	jqs "github.com/VertebrateResequencing/wr/jobqueue/scheduler"
)

const (
	// kickResumeBenchTargets is how many jobs each kick or resume handles.
	kickResumeBenchTargets = 1000

	// kickResumeBenchCmdBytes is the size of each target job's command, as large
	// as real portal commands can be.
	kickResumeBenchCmdBytes = 130 * 1024

	// kickResumeBenchFillers is how many small ready jobs the reservers have to
	// reserve, more than they can take while a kick or resume runs.
	kickResumeBenchFillers = 20000

	// kickResumeBenchReservers is how many goroutines reserve concurrently.
	kickResumeBenchReservers = 8

	// kickResumeBenchReservePause is how long each reserver waits between
	// reserves, standing in for the arrival rate of runners' requests.
	kickResumeBenchReservePause = 50 * time.Microsecond

	kickResumeBenchAddBatch = 50
	kickResumeBenchLetters  = "abcdefghijklmnopqrstuvwxyz"
	kickResumeBenchRepGroup = "kick_resume_bench"
	percentile50            = 50
	percentile99            = 99
	percentileMax           = 100
)

// kickResumeBenchBury reserves and buries the n ready targets on s.
func kickResumeBenchBury(ctx context.Context, b *testing.B, s *Server, n int) ([]*Job, []string) {
	b.Helper()

	jobs := make([]*Job, 0, n)
	keys := make([]string, 0, n)

	for range n {
		item, srerr := s.reserveItem(ctx, &clientRequest{})
		if item == nil {
			b.Fatalf("could not reserve a target: %q", srerr)
		}

		job, ok := item.Data().(*Job)
		if !ok {
			b.Fatal("queue item is not a *Job")
		}

		if err := s.q.Bury(item.Key); err != nil {
			b.Fatal(err)
		}

		job.Lock()
		job.State = JobStateBuried
		job.Unlock()

		jobs = append(jobs, job)
		keys = append(keys, item.Key)
	}

	return jobs, keys
}

// kickResumeBenchSuspend suspends the n ready targets on s.
func kickResumeBenchSuspend(ctx context.Context, b *testing.B, s *Server, n int) ([]*Job, []string) {
	b.Helper()

	keys := make([]string, 0, n)

	for _, item := range s.q.AllItems() {
		keys = append(keys, item.Key)
	}

	if len(keys) != n {
		b.Fatalf("%d items, not %d targets", len(keys), n)
	}

	if suspended := s.suspendJobs(ctx, keys); suspended != n {
		b.Fatalf("suspended %d of %d targets", suspended, n)
	}

	return nil, keys
}

// kickResumeBenchPrepare parks the n ready target jobs on s, the only jobs it
// holds, returning their server-side jobs and keys.
type kickResumeBenchPrepare func(ctx context.Context, b *testing.B, s *Server, n int) ([]*Job, []string)

// benchKickResumeUnderReserveLoad runs b.N rounds, each on a fresh server: it
// adds the target jobs, has prepare park them (bury or suspend), adds the
// fillers, starts the reservers, then times op over every target.
func benchKickResumeUnderReserveLoad(b *testing.B, prepare kickResumeBenchPrepare, op kickResumeBenchOp) {
	b.Helper()

	ctx := context.Background()

	var opTotal time.Duration

	latencies := make([]time.Duration, 0, b.N*kickResumeBenchFillers)

	b.StopTimer()

	for range b.N {
		s, jobs, keys, stop := kickResumeBenchServer(ctx, b, prepare)

		got, elapsed := kickResumeBenchRound(ctx, b, s, jobs, keys, op)
		latencies = append(latencies, got...)
		opTotal += elapsed

		stop()
	}

	b.ReportMetric(float64(opTotal.Nanoseconds())/float64(b.N), "ns/op")
	b.ReportMetric(float64(len(latencies))/float64(b.N), "reserves/op")
	b.ReportMetric(durationPercentileMicros(latencies, percentile50), "reserve-p50-us")
	b.ReportMetric(durationPercentileMicros(latencies, percentile99), "reserve-p99-us")
	b.ReportMetric(durationPercentileMicros(latencies, percentileMax), "reserve-max-us")
}

// kickResumeBenchRound starts the reservers, times op over every target, stops
// the reservers, and returns the latencies of the reserves that began while op
// ran and got a job, and how long op took.
func kickResumeBenchRound(ctx context.Context, b *testing.B, s *Server, jobs []*Job,
	keys []string, op kickResumeBenchOp,
) ([]time.Duration, time.Duration) {
	b.Helper()

	var (
		wg      sync.WaitGroup
		mu      sync.Mutex
		window  = make(chan struct{})
		done    = make(chan struct{})
		samples []time.Duration
	)

	for range kickResumeBenchReservers {
		wg.Go(func() {
			got := kickResumeBenchReserve(ctx, s, window, done)

			mu.Lock()

			samples = append(samples, got...)
			mu.Unlock()
		})
	}

	time.Sleep(10 * time.Millisecond)

	b.StartTimer()
	close(window)

	start := time.Now()
	n := op(ctx, s, jobs, keys)
	elapsed := time.Since(start)

	close(done)
	b.StopTimer()

	wg.Wait()

	if n != len(keys) {
		b.Fatalf("op handled %d of %d targets", n, len(keys))
	}

	return samples, elapsed
}

// kickResumeBenchReserve reserves repeatedly until done is closed, returning the
// latency of each reserve that began after window was closed and got a job.
func kickResumeBenchReserve(ctx context.Context, s *Server, window, done <-chan struct{}) []time.Duration {
	var samples []time.Duration

	for {
		select {
		case <-done:
			return samples
		default:
		}

		inWindow := isClosed(window)
		start := time.Now()
		item, _ := s.reserveItem(ctx, &clientRequest{})
		took := time.Since(start)

		if inWindow && item != nil && !isClosed(done) {
			samples = append(samples, took)
		}

		time.Sleep(kickResumeBenchReservePause)
	}
}

func isClosed(ch <-chan struct{}) bool {
	select {
	case <-ch:
		return true
	default:
		return false
	}
}

// durationPercentileMicros returns the pth percentile (nearest rank) of ds in
// microseconds, or 0 if ds is empty.
func durationPercentileMicros(ds []time.Duration, p int) float64 {
	if len(ds) == 0 {
		return 0
	}

	sorted := slices.Clone(ds)
	slices.Sort(sorted)

	idx := (len(sorted)*p+percentileMax-1)/percentileMax - 1
	idx = max(0, min(idx, len(sorted)-1))

	return float64(sorted[idx].Nanoseconds()) / float64(time.Microsecond)
}

// kickResumeBenchServer starts a server holding kickResumeBenchTargets target
// jobs, parked by prepare, and kickResumeBenchFillers ready filler jobs. It
// returns the server, the targets' server-side jobs and keys, and a function
// that stops the server.
func kickResumeBenchServer(ctx context.Context, b *testing.B,
	prepare kickResumeBenchPrepare,
) (*Server, []*Job, []string, func()) {
	b.Helper()

	_, serverConfig, addr, standardReqs, clientConnectTime := jobqueueTestInit(false)

	dir := b.TempDir()
	serverConfig.DBFile = filepath.Join(dir, "db")
	serverConfig.DBFileBackup = filepath.Join(dir, "db.bk")
	serverConfig.TokenFile = filepath.Join(dir, "token")
	serverConfig.Timings.ItemTTR = time.Hour

	s, _, token, err := serve(ctx, serverConfig)
	if err != nil {
		b.Fatal(err)
	}

	jq, err := Connect(addr, serverConfig.CAFile, serverConfig.CertDomain, token, clientConnectTime)
	if err != nil {
		s.Stop(ctx, true)
		b.Fatal(err)
	}

	stop := func() {
		disconnect(jq)
		s.Stop(ctx, true)
	}

	targets := kickResumeBenchTargetJobs(standardReqs)
	kickResumeBenchAdd(b, jq, targets)

	jobs, keys := prepare(ctx, b, s, len(targets))

	fillers := make([]*Job, kickResumeBenchFillers)
	for i := range fillers {
		fillers[i] = &Job{
			Cmd: "true filler " + strconv.Itoa(i), Cwd: testCwd, RepGroup: kickResumeBenchRepGroup,
			ReqGroup: kickResumeBenchRepGroup, Requirements: standardReqs, Retries: 3,
		}
	}

	kickResumeBenchAdd(b, jq, fillers)

	return s, jobs, keys, stop
}

// kickResumeBenchTargetJobs returns the target jobs, each with a distinct
// command of kickResumeBenchCmdBytes made of path-like words.
func kickResumeBenchTargetJobs(reqs *jqs.Requirements) []*Job {
	rng := rand.New(rand.NewPCG(3, 4)) //nolint:gosec
	targets := make([]*Job, kickResumeBenchTargets)

	for i := range targets {
		var cmd strings.Builder

		cmd.WriteString("true target " + strconv.Itoa(i))

		for cmd.Len() < kickResumeBenchCmdBytes {
			cmd.WriteString(" /lustre/scratch/portal/")

			for range 8 + rng.IntN(16) {
				cmd.WriteByte(kickResumeBenchLetters[rng.IntN(len(kickResumeBenchLetters))])
			}

			cmd.WriteString(".cram")
		}

		targets[i] = &Job{
			Cmd: cmd.String(), Cwd: testCwd, RepGroup: kickResumeBenchRepGroup,
			ReqGroup: kickResumeBenchRepGroup, Requirements: reqs, Retries: 3, Priority: 1,
		}
	}

	return targets
}

// kickResumeBenchAdd adds jobs in batches, failing unless all are new.
func kickResumeBenchAdd(b *testing.B, jq *Client, jobs []*Job) {
	b.Helper()

	for batch := range slices.Chunk(jobs, kickResumeBenchAddBatch) {
		inserts, _, err := jq.Add(batch, os.Environ(), true)
		if err != nil {
			b.Fatal(err)
		}

		if inserts != len(batch) {
			b.Fatalf("added %d of %d jobs", inserts, len(batch))
		}
	}
}

// kickResumeBenchOp kicks or resumes the parked targets, returning how many it
// handled.
type kickResumeBenchOp func(ctx context.Context, s *Server, jobs []*Job, keys []string) int

// BenchmarkKickUnderReserveLoad measures kicking buried jobs with large commands
// while reservers reserve from the same queue. ns/op is the time the kick of
// every target takes; reserve-p50-us and reserve-p99-us are the latencies of
// the reserves that began while the kick ran and got a job.
func BenchmarkKickUnderReserveLoad(b *testing.B) {
	benchKickResumeUnderReserveLoad(b, kickResumeBenchBury,
		func(ctx context.Context, s *Server, jobs []*Job, _ []string) int {
			return s.kickJobs(ctx, jobs)
		})
}

// BenchmarkResumeUnderReserveLoad is BenchmarkKickUnderReserveLoad for resuming
// suspended jobs.
func BenchmarkResumeUnderReserveLoad(b *testing.B) {
	benchKickResumeUnderReserveLoad(b, kickResumeBenchSuspend,
		func(ctx context.Context, s *Server, _ []*Job, keys []string) int {
			return s.resumeJobs(ctx, keys)
		})
}
