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
	"strconv"
	"testing"
)

const (
	// benchDepGroupMembers and benchDepGroupWaiters are the shape of
	// dep-granularity-check's add: a member added to a dep group that many
	// queued jobs wait on, each of which the add reads as a live dependent.
	benchDepGroupMembers = 3000
	benchDepGroupWaiters = 30000

	benchDepGroup = "bench-dep-group"
)

// BenchmarkAddDepGroupMember measures one add of a new member to a dep group
// that benchDepGroupWaiters queued jobs wait on, through a real manager: the
// add reads all of them as live dependents, guards them, writes the member and
// gives them their new dependency (see running_dependent.go). None of them has
// run, so none can have been archived since the add read it.
func BenchmarkAddDepGroupMember(b *testing.B) {
	ctx := context.Background()
	_, serverConfig, addr, reqs, connectTime := jobqueueTestInit(true)

	server, _, token, err := serve(ctx, serverConfig)
	if err != nil {
		b.Fatal(err)
	}

	defer server.Stop(ctx, true)

	jq, err := Connect(addr, serverConfig.CAFile, serverConfig.CertDomain, token, connectTime)
	if err != nil {
		b.Fatal(err)
	}

	defer disconnect(jq)

	job := func(cmd string) *Job {
		return &Job{Cmd: cmd, Cwd: testCwd, ReqGroup: reqGroupFake, Requirements: reqs, RepGroup: "bench"}
	}

	jobs := make([]*Job, 0, benchDepGroupMembers+benchDepGroupWaiters)

	for i := range benchDepGroupMembers {
		member := job("echo bench member " + strconv.Itoa(i))
		member.DepGroups = []string{benchDepGroup}
		jobs = append(jobs, member)
	}

	for i := range benchDepGroupWaiters {
		waiter := job("echo bench waiter " + strconv.Itoa(i))
		waiter.Dependencies = Dependencies{NewDepGroupDependency(benchDepGroup)}
		jobs = append(jobs, waiter)
	}

	if _, _, err = jq.Add(jobs, envVars, true); err != nil {
		b.Fatal(err)
	}

	b.ResetTimer()

	for i := range b.N {
		b.StopTimer()

		member := job("echo bench new member " + strconv.Itoa(i))
		member.DepGroups = []string{benchDepGroup}

		b.StartTimer()

		if _, _, err = jq.Add([]*Job{member}, envVars, true); err != nil {
			b.Fatal(err)
		}
	}
}
