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
	"bytes"
	"context"
	"runtime/pprof"
	"strings"
	"sync"
	"testing"
	"time"

	. "github.com/smartystreets/goconvey/convey"
)

// subscriptionSweepIdleTimeout is the SubscriptionIdleTimeout the sweep tests
// give their server, so a sweep runs every few tens of milliseconds.
const subscriptionSweepIdleTimeout = 300 * time.Millisecond

// subscriptionSweepPollTimeout is how long the polling subscription's each
// wait is held, well inside subscriptionSweepIdleTimeout.
const subscriptionSweepPollTimeout = 20 * time.Millisecond

// subscriptionSweepYoungTimeout is the SubscriptionIdleTimeout of the server
// that checks a young unpolled subscription survives several sweeps, and
// subscriptionSweepYoungAge is how long after registering it the check is made:
// three sweeps in, and a whole second before the subscription may go.
const (
	subscriptionSweepYoungTimeout = 2 * time.Second
	subscriptionSweepYoungAge     = time.Second
)

func TestSubscriptionIdleSweepLiveClient(t *testing.T) {
	if runnermode || servermode {
		return
	}

	allowShortSubscriptionIdleTimeouts(t)

	Convey("AddAndWait whose subscription is swept resubscribes and sees a completion from while it had none", t, func() {
		ctx := context.Background()
		serverConfig, addr, standardReqs, clientConnectTime := subscriptionTestConfig(t)
		serverConfig.Timings.SubscriptionIdleTimeout = subscriptionSweepIdleTimeout
		applySubscriptionReconnectTimings(&serverConfig, 50*time.Millisecond, subscriptionRestartRetryTime)
		server, _, token, err := serve(ctx, serverConfig)
		So(err, ShouldBeNil)

		defer server.Stop(ctx, true)

		jq, err := Connect(addr, serverConfig.CAFile, serverConfig.CertDomain, token, clientConnectTime)
		So(err, ShouldBeNil)

		defer disconnect(jq)

		runner, err := Connect(addr, serverConfig.CAFile, serverConfig.CertDomain, token, clientConnectTime)
		So(err, ShouldBeNil)

		defer disconnect(runner)

		// the first reconnect, which only the sweep can cause here, is held
		// before its resubscribe while the job completes, so the completion
		// happens while the client has no subscription at all.
		swept := make(chan struct{})
		release := make(chan struct{})

		var once sync.Once

		reconnectConnectedHook = func() {
			once.Do(func() {
				close(swept)
				<-release
			})
		}

		defer func() {
			reconnectConnectedHook = nil
		}()

		waitCtx, cancel := context.WithTimeout(ctx, subscriptionUpdateWait)
		defer cancel()

		input := subscriptionTestJobs("subscription-sweep-live", standardReqs, 1)
		resultCh := addAndWaitAsync(waitCtx, jq, input)

		job := startNextAddAndWaitJob(runner)

		select {
		case <-swept:
		case <-time.After(subscriptionUpdateWait):
			So("the live subscription was never swept", ShouldBeBlank)
		}

		So(runner.Archive(job, &JobEndState{Exited: true, Exitcode: 0, EndTime: time.Now()}), ShouldBeNil)
		close(release)

		result := receiveAddAndWaitResult(resultCh, subscriptionUpdateWait)
		So(result.err, ShouldBeNil)
		So(result.jobs, ShouldHaveLength, 1)
		So(result.jobs[0].Key(), ShouldEqual, input[0].Key())
		So(result.jobs[0].State, ShouldEqual, JobStateComplete)
	})
}

func TestSubscriptionIdleSweep(t *testing.T) {
	if runnermode || servermode {
		return
	}

	allowShortSubscriptionIdleTimeouts(t)

	Convey("A subscription nobody polls is swept, while polled and status ones are kept", t, func() {
		ctx := context.Background()
		serverConfig, _, _, _ := subscriptionTestConfig(t)
		serverConfig.Timings.SubscriptionIdleTimeout = subscriptionSweepIdleTimeout
		server, _, _, err := serve(ctx, serverConfig)
		So(err, ShouldBeNil)

		defer server.Stop(ctx, true)

		before := subscriptionDeliveryGoroutines()

		idleID, err := server.registerClientSubscription([]string{"subscription-sweep-idle"}, "")
		So(err, ShouldBeNil)

		polledID, err := server.registerClientSubscription([]string{"subscription-sweep-polled"}, "")
		So(err, ShouldBeNil)

		defer server.unregisterClientSubscription(polledID)

		statusID, err := server.registerStatusSubscription()
		So(err, ShouldBeNil)

		defer server.unregisterClientSubscription(statusID)

		So(subscriptionDeliveryGoroutinesBecome(before+3, time.Second), ShouldBeTrue)

		stopPolling := make(chan struct{})
		pollErr := pollServerSubscriptionUntil(server, polledID, stopPolling)

		So(serverSubscriptionKnownBecomes(server, idleID, false, 10*subscriptionSweepIdleTimeout), ShouldBeTrue)
		So(subscriptionDeliveryGoroutinesBecome(before+2, time.Second), ShouldBeTrue)

		time.Sleep(3 * subscriptionSweepIdleTimeout)

		close(stopPolling)
		So(<-pollErr, ShouldBeNil)

		_, polledKnown := server.clientSubscription(polledID)
		So(polledKnown, ShouldBeTrue)

		_, statusKnown := server.clientSubscription(statusID)
		So(statusKnown, ShouldBeTrue)
	})

	Convey("A subscription nobody has polled yet survives sweeps until it has been idle for the timeout", t, func() {
		ctx := context.Background()
		serverConfig, _, _, _ := subscriptionTestConfig(t)
		serverConfig.Timings.SubscriptionIdleTimeout = subscriptionSweepYoungTimeout
		server, _, _, err := serve(ctx, serverConfig)
		So(err, ShouldBeNil)

		defer server.Stop(ctx, true)

		youngID, err := server.registerClientSubscription([]string{"subscription-sweep-young"}, "")
		registered := time.Now()

		So(err, ShouldBeNil)

		defer server.unregisterClientSubscription(youngID)

		time.Sleep(time.Until(registered.Add(subscriptionSweepYoungAge)))

		_, known := server.clientSubscription(youngID)
		So(known, ShouldBeTrue)
	})

	Convey("A configured idle timeout shorter than a long poll is raised so live polls are never swept", t, func() {
		So(subscriptionIdleTimeoutMin, ShouldEqual, time.Millisecond)

		subscriptionIdleTimeoutMin = defaultSubscriptionIdleTimeoutMin

		defer func() {
			subscriptionIdleTimeoutMin = time.Millisecond
		}()

		for _, configured := range []time.Duration{time.Nanosecond, time.Second, serverSubscriptionHoldTime} {
			got := ServerTimings{SubscriptionIdleTimeout: configured}.withDefaults().SubscriptionIdleTimeout
			So(got, ShouldEqual, 2*serverSubscriptionHoldTime)
		}

		So(ServerTimings{}.withDefaults().SubscriptionIdleTimeout, ShouldEqual, serverSubscriptionIdleTimeout)
		So(ServerTimings{SubscriptionIdleTimeout: time.Hour}.withDefaults().SubscriptionIdleTimeout,
			ShouldEqual, time.Hour)
	})
}

// allowShortSubscriptionIdleTimeouts lowers the floor on
// ServerTimings.SubscriptionIdleTimeout for the rest of the test, so its
// servers can sweep in milliseconds.
func allowShortSubscriptionIdleTimeouts(t *testing.T) {
	t.Helper()

	subscriptionIdleTimeoutMin = time.Millisecond

	t.Cleanup(func() {
		subscriptionIdleTimeoutMin = defaultSubscriptionIdleTimeoutMin
	})
}

func subscriptionDeliveryGoroutinesBecome(expected int, timeout time.Duration) bool {
	deadline := time.After(timeout)

	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()

	for {
		if subscriptionDeliveryGoroutines() == expected {
			return true
		}

		select {
		case <-deadline:
			return false
		case <-ticker.C:
		}
	}
}

// subscriptionDeliveryGoroutines counts the goroutines each serverSubscription
// runs until it is closed.
func subscriptionDeliveryGoroutines() int {
	var buf bytes.Buffer
	if err := pprof.Lookup("goroutine").WriteTo(&buf, 2); err != nil {
		return 0
	}

	return strings.Count(buf.String(), "jobqueue.(*serverSubscription).deliverQueuedUpdates(")
}

// pollServerSubscriptionUntil polls subscription id the way a live client does
// until stop is closed, then sends the first error a poll returned, or nil.
func pollServerSubscriptionUntil(server *Server, id string, stop <-chan struct{}) <-chan error {
	errCh := make(chan error, 1)

	go func() {
		for {
			select {
			case <-stop:
				errCh <- nil

				return
			default:
			}

			if _, err := server.waitForSubscriptionUpdates(id, subscriptionSweepPollTimeout); err != nil {
				errCh <- err

				return
			}
		}
	}()

	return errCh
}

func serverSubscriptionKnownBecomes(server *Server, id string, expected bool, timeout time.Duration) bool {
	deadline := time.After(timeout)

	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()

	for {
		if _, known := server.clientSubscription(id); known == expected {
			return true
		}

		select {
		case <-deadline:
			return false
		case <-ticker.C:
		}
	}
}
