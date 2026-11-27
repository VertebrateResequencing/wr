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
	"encoding/base64"
	"fmt"
	"math/rand/v2"
	"os"
	"sync"
	"testing"
	"time"

	jqs "github.com/VertebrateResequencing/wr/jobqueue/scheduler"
	. "github.com/smartystreets/goconvey/convey"
)

const (
	// zzReplyJobs jobs of zzReplyCmdBytes poorly compressible bytes each make
	// a GetByRepGroup reply of about 64MB: one the manager accepts for sending
	// in an instant but takes far longer than a stop to write to the client.
	zzReplyJobs     = 64
	zzReplyCmdBytes = 1 << 20

	// zzReplyRequestTimeout is the client's request timeout: the most it waits
	// for any reply before giving up on it.
	zzReplyRequestTimeout = 10 * time.Second
)

type zzReplyResult struct {
	jobs []*Job
	err  error
}

// TestZZReplyAcceptedBeforeStopReachesClient: a reply the manager has accepted
// for sending (Server.reply's SendMsg returned nil) before it stops must reach
// the client, rather than be dropped when the stop closes the command socket.
func TestZZReplyAcceptedBeforeStopReachesClient(t *testing.T) {
	if runnermode || servermode {
		return
	}

	Convey("Given a manager whose status reply for a rep group is large", t, func() {
		ctx := context.Background()
		config, serverConfig, addr, standardReqs, clientConnectTime := jobqueueTestInit(false)

		Reset(setClientMinRequestTimeout(zzReplyRequestTimeout))

		server, _, token, err := serve(ctx, serverConfig)
		So(err, ShouldBeNil)

		defer server.Stop(ctx, true)

		jq, err := Connect(addr, config.ManagerCAFile, config.ManagerCertDomain, token, clientConnectTime)
		So(err, ShouldBeNil)

		defer disconnect(jq)

		zzAddBigJobs(jq, standardReqs)

		Convey("stopping it just after it accepted that reply for sending still delivers the reply", func() {
			var once sync.Once

			queued := make(chan struct{})
			hook := func(err error) {
				if err == nil {
					once.Do(func() { close(queued) })
				}
			}

			Reset(func() { replySentHook.Store(nil) })
			replySentHook.Store(&hook)

			result := make(chan zzReplyResult, 1)

			go func() {
				jobs, errg := jq.GetByRepGroup("zz_reply", false, 0, "", false, false)
				result <- zzReplyResult{jobs: jobs, err: errg}
			}()

			select {
			case <-queued:
			case <-time.After(zzReplyRequestTimeout):
				So("the manager never accepted the reply for sending", ShouldBeEmpty)
			}

			stopStart := time.Now()
			server.Stop(ctx, true)
			t.Logf("manager stopped %s after accepting the reply", time.Since(stopStart))

			select {
			case r := <-result:
				So(r.err, ShouldBeNil)
				So(r.jobs, ShouldHaveLength, zzReplyJobs)
			case <-time.After(zzReplyRequestTimeout / 2): // well inside the request's own timeout
				zzLogLateResult(t, result)
				So("the reply accepted before the stop never reached the client", ShouldBeEmpty)
			}
		})
	})
}

// zzAddBigJobs adds zzReplyJobs jobs in rep group "zz_reply", each with a
// command of zzReplyCmdBytes random, poorly compressible characters.
func zzAddBigJobs(jq *Client, reqs *jqs.Requirements) {
	rng := rand.NewChaCha8([32]byte{2})
	raw := make([]byte, base64.StdEncoding.DecodedLen(zzReplyCmdBytes))

	jobs := make([]*Job, 0, zzReplyJobs)

	for i := range zzReplyJobs {
		_, err := rng.Read(raw)
		So(err, ShouldBeNil)

		jobs = append(jobs, &Job{
			Cmd:          fmt.Sprintf("echo %d %s", i, base64.StdEncoding.EncodeToString(raw)),
			Cwd:          defaultUploadDir,
			ReqGroup:     "zz_reply",
			RepGroup:     "zz_reply",
			Requirements: reqs,
		})
	}

	added, existed, err := jq.Add(jobs, os.Environ(), true)
	So(err, ShouldBeNil)
	So(added, ShouldEqual, zzReplyJobs)
	So(existed, ShouldEqual, 0)
}

// zzLogLateResult logs what the client's request returns once it gives up on
// the reply, waiting at most its request timeout for that.
func zzLogLateResult(t *testing.T, result <-chan zzReplyResult) {
	t.Helper()

	start := time.Now()

	select {
	case r := <-result:
		t.Logf("%s later the client's request returned %d jobs and error: %v", time.Since(start), len(r.jobs), r.err)
	case <-time.After(zzReplyRequestTimeout):
		t.Logf("the client's request had still not returned %s later", time.Since(start))
	}
}
