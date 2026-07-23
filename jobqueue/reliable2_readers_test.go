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

// This file covers spec.md section B1 (Phase 2, Item 2.1): N concurrent
// RecvMsg readers on the existing command socket. Two acceptance tests:
//
//   B1.1 (safety, -race): with numRPCReaders > 1 and many concurrent clients
//        each issuing a distinct request-reply round-trip, every client
//        receives exactly its own correct reply (no misrouted/dropped/
//        duplicated reply) and the -race detector reports no data race. This is
//        the N4 HARD REQUIREMENT proof.
//
//   B1.2 (admission fairness, supporting): with the server saturated by many
//        concurrent goroutines issuing reserve/touch RPCs in a tight loop, a
//        control RPC (GetStatusByRepGroupMatch) returns within a bounded time
//        (well under the 60s client timeout) when numRPCReaders > 1. This
//        in-process test SUPPORTS but is not the sole evidence; the headline
//        responsiveness claim is Tier B (real LSF at scale). As documented in
//        the B1.2 Convey block, the spec's single-reader fail-before is not
//        reproducible in-process (admission is decoupled from handling by
//        per-request goroutine dispatch), so this test asserts only the
//        reproducible concurrent-reader pass-after and records the single-reader
//        latency as an unasserted diagnostic.

import (
	"context"
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	. "github.com/smartystreets/goconvey/convey"
)

// TestReliable2Readers covers the B1 acceptance tests for concurrent RPC
// readers on the single command socket.
func TestReliable2Readers(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("B1.1: with concurrent readers, M clients each get exactly their own reply", t, func() {
		// N4 HARD REQUIREMENT: run with more than one reader and prove no reply
		// is misrouted, dropped or duplicated. Each client queries a distinct
		// rep group whose ready-job count is unique to it, so a misrouted reply
		// (one client receiving another's status summary) is detectable.
		restore := setNumRPCReaders(6)
		defer restore()

		config, serverConfig, addr, standardReqs, clientConnectTime := jobqueueTestInit(false)

		server, _, token, err := serve(ctx, serverConfig)
		So(err, ShouldBeNil)

		defer server.Stop(ctx, true)

		rawConnect := func() (*Client, error) {
			return Connect(addr, config.ManagerCAFile, config.ManagerCertDomain, token, clientConnectTime)
		}

		connect := func() *Client {
			c, errc := rawConnect()
			So(errc, ShouldBeNil)

			return c
		}

		const numClients = 20

		// a setup client adds numClients distinct rep groups, where rg_i holds
		// exactly i+1 ready jobs, so each client's expected answer is unique.
		repGroup := func(i int) string { return fmt.Sprintf("b1_1_rg_%d", i) }

		setup := connect()

		for i := range numClients {
			jobs := make([]*Job, 0, i+1)
			for j := range i + 1 {
				jobs = append(jobs, &Job{
					Cmd: fmt.Sprintf("true b1_1 %d %d", i, j), Cwd: testCwdPath,
					RepGroup: repGroup(i), ReqGroup: "b1_1", Requirements: standardReqs,
				})
			}

			inserts, _, erra := setup.Add(jobs, os.Environ(), true)
			So(erra, ShouldBeNil)
			So(inserts, ShouldEqual, i+1)
		}

		disconnect(setup)

		// each client concurrently issues many status round-trips for its own
		// rep group; every reply must report exactly that client's job count.
		const roundsPerClient = 30

		var (
			mismatches atomic.Int64
			successes  atomic.Int64
			wg         sync.WaitGroup
		)

		wg.Add(numClients)

		for i := range numClients {
			go func(i int) {
				defer wg.Done()

				jq, errc := rawConnect()
				if errc != nil {
					mismatches.Add(int64(roundsPerClient))

					return
				}
				defer disconnect(jq)

				rg := repGroup(i)
				want := i + 1

				for range roundsPerClient {
					summaries, errg := jq.GetStatusByRepGroupMatch(rg, RepGroupMatchExact,
						nil, false, false)
					if errg != nil {
						mismatches.Add(1)

						continue
					}

					status, ok := summaries[rg]
					if !ok || len(summaries) != 1 {
						mismatches.Add(1)

						continue
					}

					total := 0
					for _, c := range status.Counts {
						total += c
					}

					if total != want {
						mismatches.Add(1)

						continue
					}

					successes.Add(1)
				}
			}(i)
		}

		wg.Wait()

		So(mismatches.Load(), ShouldEqual, 0)
		So(successes.Load(), ShouldEqual, int64(numClients*roundsPerClient))
	})

	Convey("B1.2: a control RPC stays responsive under a reserve/touch flood with concurrent readers", t, func() {
		// SUPPORTING test (spec B1 acceptance test 2). It asserts the
		// reproducible pass-after property: with concurrent readers, a control
		// RPC issued while the server is saturated by a reserve/touch flood
		// returns well within the 60s client timeout.
		//
		// The spec's fail-before ("with a single reader the control RPC is
		// starved") is NOT reproducible in-process, and this test does not
		// assert it: wr's reader dispatches every admitted request to its own
		// goroutine (serveClients -> `go dispatchClientRequest`), so a reader's
		// per-message cost is only a fast recvQ channel pop plus a goroutine
		// spawn -- cheaper than handling a request on a multi-core box. A single
		// reader therefore keeps up with admission and the control RPC is
		// admitted just as promptly as with many readers; the single-reader
		// latency below is printed purely as a diagnostic and is deliberately
		// not asserted on, because it is dominated by noise and does not
		// consistently exceed the multi-reader latency. The starvation this
		// feature removes manifests at Tier-B LSF scale (thousands of real
		// remote runners, per-message TLS/network admission cost, and the reader
		// goroutine competing with thousands of pipe receivers for scheduling),
		// which the spec designates as the headline responsiveness evidence.
		multiLatency := measureControlLatencyUnderFlood(ctx, 6)
		singleLatency := measureControlLatencyUnderFlood(ctx, 1)

		t.Logf("B1.2 control-RPC latency under flood: readers=6 -> %s; readers=1 -> %s "+
			"(single-reader value is an unasserted diagnostic; see comment)", multiLatency, singleLatency)

		// pass-after: with concurrent readers the control RPC is admitted and
		// answered promptly, far below the 60s ClientMinRequestTimeout.
		So(multiLatency, ShouldBeLessThan, 3*time.Second)
	})
}

// measureControlLatencyUnderFlood starts a server with the given number of RPC
// readers, saturates it with a reserve/touch flood, then returns how long a
// single GetStatusByRepGroupMatch control RPC takes while the flood runs.
func measureControlLatencyUnderFlood(ctx context.Context, readers int) time.Duration {
	restore := setNumRPCReaders(readers)
	defer restore()

	config, serverConfig, addr, standardReqs, clientConnectTime := jobqueueTestInit(false)

	server, _, token, err := serve(ctx, serverConfig)
	So(err, ShouldBeNil)

	defer server.Stop(ctx, true)

	rawConnect := func() (*Client, error) {
		return Connect(addr, config.ManagerCAFile, config.ManagerCertDomain, token, clientConnectTime)
	}

	connect := func() *Client {
		c, errc := rawConnect()
		So(errc, ShouldBeNil)

		return c
	}

	const (
		floodClients = 120
		controlRG    = "b1_2_control"
	)

	// add plenty of ready jobs for the flood clients to reserve, plus a small
	// distinct control rep group for the latency probe to summarise.
	setup := connect()

	floodJobs := make([]*Job, 0, floodClients*2)
	for j := range floodClients * 2 {
		floodJobs = append(floodJobs, &Job{
			Cmd: fmt.Sprintf("true b1_2 flood %d", j), Cwd: testCwdPath,
			RepGroup: "b1_2_flood", ReqGroup: "b1_2", Requirements: standardReqs,
		})
	}

	controlJobs := make([]*Job, 0, 3)
	for j := range 3 {
		controlJobs = append(controlJobs, &Job{
			Cmd: fmt.Sprintf("true b1_2 control %d", j), Cwd: testCwdPath,
			RepGroup: controlRG, ReqGroup: "b1_2", Requirements: standardReqs,
		})
	}

	_, _, err = setup.Add(append(floodJobs, controlJobs...), os.Environ(), true)
	So(err, ShouldBeNil)
	disconnect(setup)

	stop := make(chan struct{})

	var flooding sync.WaitGroup

	startFloodClients(rawConnect, floodClients, stop, &flooding)

	// let the flood ramp up and saturate before probing.
	time.Sleep(500 * time.Millisecond)

	probe := connect()
	defer disconnect(probe)

	start := time.Now()
	_, errg := probe.GetStatusByRepGroupMatch(controlRG, RepGroupMatchExact, nil, false, false)
	latency := time.Since(start)

	So(errg, ShouldBeNil)

	close(stop)
	flooding.Wait()

	return latency
}

// setNumRPCReaders sets the numRPCReaders package var to n and returns a
// function that restores the previous value, so a test can flip between a
// single reader and many without leaking the change to other tests.
func setNumRPCReaders(n int) func() {
	prev := numRPCReaders
	numRPCReaders = n

	return func() { numRPCReaders = prev }
}

// startFloodClients launches n goroutines that each reserve a job and then hold
// the server busy with a tight touch loop until stop is closed. It takes a raw
// connect that returns an error (rather than asserting) because it must not call
// GoConvey So() off the test goroutine's stack.
func startFloodClients(connect func() (*Client, error), n int,
	stop <-chan struct{}, flooding *sync.WaitGroup) {
	flooding.Add(n)

	for range n {
		go func() {
			defer flooding.Done()

			jq, errc := connect()
			if errc != nil {
				return
			}
			defer disconnect(jq)

			job, errr := jq.Reserve(2 * time.Second)
			if errr != nil || job == nil {
				// still contribute load via status calls if no job was free.
				floodWithStatus(jq, stop)

				return
			}

			if errs := jq.Started(job, os.Getpid()); errs != nil {
				return
			}

			for {
				select {
				case <-stop:
					return
				default:
					_, _ = jq.Touch(job) //nolint:errcheck // load generator; touch outcome is irrelevant here
				}
			}
		}()
	}
}

// floodWithStatus keeps issuing status RPCs until stop is closed, used by a
// flood client that failed to reserve a job so it still contributes load.
func floodWithStatus(jq *Client, stop <-chan struct{}) {
	for {
		select {
		case <-stop:
			return
		default:
			//nolint:errcheck // load generator; status outcome is irrelevant here
			_, _ = jq.GetStatusByRepGroupMatch("b1_2_flood", RepGroupMatchExact, nil, false, false)
		}
	}
}
