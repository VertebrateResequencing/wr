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

package scheduler

import (
	"context"
	"sync"
	"time"
)

const (
	mockSchedulerName   = "mock"
	mockInitializeOp    = "initialize"
	errMockConfig       = "SchedulerConfig must be ConfigMock or *ConfigMock with RunnerFunc"
	errMockNoRunnerFunc = "SchedulerConfig must include RunnerFunc"
)

// ConfigMock is the config option you supply to New() when using the "mock"
// scheduler. The mock scheduler does not spawn any subprocesses; instead, for
// every runner it is asked to run, it calls RunnerFunc in its own goroutine.
//
// This lets a caller (in practice, a test) supply an in-process function that
// behaves like a `wr runner` for a scheduled command - connecting to the
// server and driving jobs through their lifecycle - without the cost of
// forking a real runner subprocess or executing a real job command. It is the
// scheduler equivalent of a test double: it exercises all of the server-side
// scheduling and job-state behaviour, while leaving the actual job execution to
// the supplied function.
type ConfigMock struct {
	// RunnerFunc is called, in its own goroutine, once for each runner the
	// scheduler decides to "run" for a scheduled command. It is passed the same
	// command string the real schedulers would have executed (so it can parse
	// out the scheduler group, server address etc. that the server templated
	// into ServerConfig.RunnerCmd). It should return when there is no more work
	// for that command, at which point the scheduler considers that runner
	// finished.
	RunnerFunc func(ctx context.Context, cmd string)

	// ReserveTimeoutSeconds is what reserveTimeout() returns (how long a runner
	// should wait for a job). Defaults to 1 if <= 0.
	ReserveTimeoutSeconds int

	// ScheduleBlock, if non-nil, is received from at the start of every
	// schedule() call (before any other work or lock is taken), letting a test
	// hold Schedule() calls open to simulate a slow external scheduler command
	// (eg. bsub). Close the channel to release all blocked (and allow future)
	// schedule() calls. Leave nil for normal non-blocking behaviour.
	ScheduleBlock <-chan struct{}

	// ScheduleError, if non-nil, is called near the start of every schedule()
	// call (after ScheduleBlock); if it returns a non-nil error, schedule()
	// returns that error immediately without running any runners. This lets a
	// test drive the server's scheduling-failure and retry paths (eg. by
	// returning an error for the first N calls, then nil). Leave nil for normal
	// behaviour.
	ScheduleError func() error

	// RunCmdFunc, if non-nil, makes getHost return a stub Host (for any host
	// name) whose RunCmd calls this. This lets a test observe and drive the
	// ProcessNotRunningOnHost / KillProcessOnHost "ssh" path (which the real
	// schedulers run against a getHost'd Host) without any real hosts. Leave nil
	// for the default behaviour, where getHost reports no host exists.
	RunCmdFunc func(ctx context.Context, cmd string, background bool) (stdout, stderr string, err error)
}

// mockHost is a stub Host whose RunCmd delegates to a supplied function, letting
// a test stand in for the remote command execution ProcessNotRunningOnHost and
// KillProcessOnHost perform.
type mockHost struct {
	runCmd func(ctx context.Context, cmd string, background bool) (stdout, stderr string, err error)
}

// RunCmd satisfies the Host interface by delegating to the supplied function.
func (h *mockHost) RunCmd(ctx context.Context, cmd string, background bool) (string, string, error) {
	return h.runCmd(ctx, cmd, background)
}

// Close satisfies the Host interface. It is a no-op: the mock host opens no ssh
// connection or other resource that needs releasing.
func (h *mockHost) Close(_ context.Context) {}

// mock is a scheduleri implementation that runs RunnerFunc goroutines instead
// of spawning runner subprocesses.
type mock struct {
	config    ConfigMock
	mutex     sync.Mutex
	running   map[string]int // scheduled cmd -> number of RunnerFunc goroutines currently running
	cleanedUp bool
}

// sets up the mock scheduler before use (method named to satisfy scheduleri).
func (s *mock) initialize(_ context.Context, config any) error {
	switch conf := config.(type) {
	case *ConfigMock:
		if conf == nil {
			return Error{mockSchedulerName, mockInitializeOp, errMockConfig}
		}

		s.config = *conf
	case ConfigMock:
		s.config = conf
	default:
		return Error{mockSchedulerName, mockInitializeOp, errMockConfig}
	}

	if s.config.RunnerFunc == nil {
		return Error{mockSchedulerName, mockInitializeOp, errMockNoRunnerFunc}
	}

	s.running = make(map[string]int)

	return nil
}

// schedule achieves the aims of Schedule(): it ensures that `count` RunnerFunc
// goroutines are running for the given cmd. If more than `count` are already
// running, the excess are left to finish on their own (a runner finishes when
// RunnerFunc returns, i.e. when there is no more work).
func (s *mock) schedule(ctx context.Context, cmd string, _ *Requirements, _ uint8, count int) error {
	if s.config.ScheduleBlock != nil {
		<-s.config.ScheduleBlock
	}

	if s.config.ScheduleError != nil {
		if err := s.config.ScheduleError(); err != nil {
			return err
		}
	}

	s.mutex.Lock()
	defer s.mutex.Unlock()

	if s.cleanedUp {
		return nil
	}

	for s.running[cmd] < count {
		s.running[cmd]++

		go func() {
			defer s.runnerFinished(cmd)

			if s.config.RunnerFunc != nil {
				s.config.RunnerFunc(ctx, cmd)
			}
		}()
	}

	return nil
}

// runnerFinished records that a RunnerFunc goroutine for the cmd has returned.
func (s *mock) runnerFinished(cmd string) {
	s.mutex.Lock()
	defer s.mutex.Unlock()

	s.running[cmd]--
	if s.running[cmd] <= 0 {
		delete(s.running, cmd)
	}
}

// scheduled achieves the aims of Scheduled(): how many runners are running for
// the cmd.
func (s *mock) scheduled(_ context.Context, cmd string) (int, error) {
	s.mutex.Lock()
	defer s.mutex.Unlock()

	return s.running[cmd], nil
}

// busy achieves the aims of Busy(): true if any runners are running.
func (s *mock) busy(_ context.Context) bool {
	s.mutex.Lock()
	defer s.mutex.Unlock()

	for _, n := range s.running {
		if n > 0 {
			return true
		}
	}

	return false
}

// reserveTimeout achieves the aims of ReserveTimeout().
func (s *mock) reserveTimeout(_ context.Context, _ *Requirements) int {
	if s.config.ReserveTimeoutSeconds > 0 {
		return s.config.ReserveTimeoutSeconds
	}

	return 1
}

// maxQueueTime achieves the aims of MaxQueueTime(): infinite for the mock.
func (s *mock) maxQueueTime(_ *Requirements) time.Duration {
	return 0
}

// recover achieves the aims of Recover(): a no-op for the mock.
func (s *mock) recover(_ context.Context, _ string, _ *Requirements, _ *RecoveredHostDetails) error {
	return nil
}

func (s *mock) hostToID(_ string) string {
	return ""
}

func (s *mock) getHost(_ string) (Host, bool) {
	if s.config.RunCmdFunc != nil {
		return &mockHost{runCmd: s.config.RunCmdFunc}, true
	}

	return nil, false
}

func (s *mock) setMessageCallBack(_ context.Context, _ MessageCallBack) {}

func (s *mock) setBadServerCallBack(_ context.Context, _ BadServerCallBack) {}

// reserved is a no-op for the mock scheduler.
func (s *mock) reserved(_ string) {}

// cleanup achieves the aims of Cleanup().
func (s *mock) cleanup(_ context.Context) {
	s.mutex.Lock()
	s.cleanedUp = true
	s.mutex.Unlock()
}
