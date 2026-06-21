/*******************************************************************************
 * Copyright (c) 2026 Genome Research Ltd.
 *
 * Author: Sendu Bala <sb10@sanger.ac.uk>
 *
 * This file is part of wr.
 *
 * wr is free software: you can redistribute it and/or modify
 * it under the terms of the GNU Lesser General Public License as published by
 * the Free Software Foundation, either version 3 of the License, or
 * (at your option) any later version.
 *
 * wr is distributed in the hope that it will be useful,
 * but WITHOUT ANY WARRANTY; without even the implied warranty of
 * MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
 * GNU Lesser General Public License for more details.
 *
 * You should have received a copy of the GNU Lesser General Public License
 * along with wr. If not, see <http://www.gnu.org/licenses/>.
 ******************************************************************************/

package scheduler

import (
	"context"
	"sync"
	"time"
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
}

// mock is a scheduleri implementation that runs RunnerFunc goroutines instead
// of spawning runner subprocesses.
type mock struct {
	config    ConfigMock
	mutex     sync.Mutex
	running   map[string]int // scheduled cmd -> number of RunnerFunc goroutines currently running
	cleanedUp bool
}

// sets up the mock scheduler before use (method named to satisfy scheduleri).
func (s *mock) initialize(_ context.Context, config any) error { //nolint:misspell
	switch conf := config.(type) {
	case *ConfigMock:
		s.config = *conf
	case ConfigMock:
		s.config = conf
	}

	s.running = make(map[string]int)

	return nil
}

// schedule achieves the aims of Schedule(): it ensures that `count` RunnerFunc
// goroutines are running for the given cmd. If more than `count` are already
// running, the excess are left to finish on their own (a runner finishes when
// RunnerFunc returns, i.e. when there is no more work).
func (s *mock) schedule(ctx context.Context, cmd string, _ *Requirements, _ uint8, count int) error {
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
	return nil, false
}

func (s *mock) setMessageCallBack(_ context.Context, _ MessageCallBack) {}

func (s *mock) setBadServerCallBack(_ context.Context, _ BadServerCallBack) {}

// cleanup achieves the aims of Cleanup().
func (s *mock) cleanup(_ context.Context) {
	s.mutex.Lock()
	s.cleanedUp = true
	s.mutex.Unlock()
}
