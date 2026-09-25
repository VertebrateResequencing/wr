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
	"errors"
	"fmt"
	"os"
	"os/exec"
	"testing"
	"time"

	jqs "github.com/VertebrateResequencing/wr/jobqueue/scheduler"
	. "github.com/smartystreets/goconvey/convey"
)

const (
	slowHostTTR          = 300 * time.Millisecond
	slowHostCheckTimeout = 1 * time.Second
	slowHostRetryTime    = time.Hour // never reached: only the first round counts
	slowHostReclaimWait  = 5 * time.Second
	slowHostRunners      = 10
)

// psOnlyForcedCommand is the ps-only forced command cmd/conf.go documents for a
// restricted ssh key: it runs `ps -o stat=` on the first "-p <pid>" it finds in
// whatever command wr sends, and ignores the rest.
const psOnlyForcedCommand = `p=$(echo "$SSH_ORIGINAL_COMMAND" | grep -oE '[-]p [0-9]+' | ` +
	`grep -oE '[0-9]+' | head -1); ps -o stat= -p "${p:-0}" 2>/dev/null || test $? -eq 1`

// errSlowHostCancelled is what the real ssh RunCmd returns when its context ends
// first.
var errSlowHostCancelled = errors.New("cloud RunCmd() on server cancelled on request")

// TestConfirmDeadSlowHost is the regression test for lost jobs sitting out the
// lost-job retry time when several runners died on one slow host: the check
// timeout once bounded the whole host batch rather than each remote command, so
// only the first few pids were ever checked in a round.
func TestConfirmDeadSlowHost(t *testing.T) {
	if runnermode || servermode {
		return
	}

	Convey("Lost jobs whose runners died on one slow host are all reclaimed in the first round", t, func() {
		Convey("when the host runs the command sent, however many pids it must check", func() {
			// a per-pid check of all 10 would take 8.8s, well past the reclaim wait
			got := reclaimDeadRunnersOnSlowHost(t, "slowhost_shell", slowHost(800*time.Millisecond, false))
			So(got, ShouldEqual, slowHostRunners)
		})

		Convey("when a ps-only forced command answers one pid per command", func() {
			// 11 commands of 0.2s: more than the check timeout, but each within it
			got := reclaimDeadRunnersOnSlowHost(t, "slowhost_forced", slowHost(200*time.Millisecond, true))
			So(got, ShouldEqual, slowHostRunners)
		})
	})
}

// reclaimDeadRunnersOnSlowHost reserves slowHostRunners first-attempt jobs as
// that many runners on one host, gives each a pid that is not running here, never
// Starts or touches them, waits for all to go lost, then returns how many a fresh
// runner can reserve within slowHostReclaimWait.
func reclaimDeadRunnersOnSlowHost(t *testing.T, rg string,
	runCmd func(context.Context, string, bool) (string, string, error),
) int {
	t.Helper()

	ctx := context.Background()
	config, serverConfig, addr, standardReqs, clientConnectTime := jobqueueTestInit(true)
	serverConfig.Timings.ItemTTR = slowHostTTR
	serverConfig.SchedulerName = schedulerNameMock
	serverConfig.SchedulerConfig = &jqs.ConfigMock{
		RunnerFunc: func(context.Context, string) {},
		RunCmdFunc: runCmd,
	}

	server, _, token, err := serve(ctx, serverConfig)
	So(err, ShouldBeNil)

	defer server.Stop(ctx, true)

	server.SetLostJobCheckTimeout(slowHostCheckTimeout)
	server.SetLostJobCheckRetryTime(slowHostRetryTime)

	jq, err := Connect(addr, config.ManagerCAFile, config.ManagerCertDomain, token, clientConnectTime)
	So(err, ShouldBeNil)

	defer disconnect(jq)

	jobs := make([]*Job, slowHostRunners)
	for i := range slowHostRunners {
		jobs[i] = &Job{
			Cmd: fmt.Sprintf("%s %s %d", restFormTrue, rg, i), Cwd: testCwdPath, RepGroup: rg,
			ReqGroup: rg, Requirements: standardReqs, Retries: 3,
		}
	}

	_, _, err = jq.Add(jobs, os.Environ(), true)
	So(err, ShouldBeNil)

	keys := make(map[string]bool, slowHostRunners)

	for range slowHostRunners {
		reserved, errr := jq.Reserve(2 * time.Second)
		So(errr, ShouldBeNil)
		So(reserved, ShouldNotBeNil)

		keys[reserved.Key()] = true
		So(setServerJobPid(server, reserved.Key(), definitelyDeadPid(t)), ShouldBeTrue)
	}

	lost := 0

	for key := range keys {
		if waitForJobLost(server, key, 20*slowHostTTR) {
			lost++
		}
	}

	So(lost, ShouldEqual, slowHostRunners)

	jq2, err := Connect(addr, config.ManagerCAFile, config.ManagerCertDomain, token, clientConnectTime)
	So(err, ShouldBeNil)

	defer disconnect(jq2)

	reclaimed := 0

	for deadline := time.Now().Add(slowHostReclaimWait); time.Now().Before(deadline) && reclaimed < slowHostRunners; {
		j, errr := jq2.Reserve(100 * time.Millisecond)
		So(errr, ShouldBeNil)

		// counted once: a reclaimed job this loop then holds goes lost again
		if j != nil && keys[j.Key()] {
			delete(keys, j.Key())

			reclaimed++
		}
	}

	return reclaimed
}

// slowHost returns a RunCmdFunc for a host where every remote command takes
// perCall (as ssh to a farm node whose login shell loads modules does) before
// running on this machine. With forced false it runs the command as sent, as an
// unrestricted key does; with forced true it runs psOnlyForcedCommand instead.
func slowHost(perCall time.Duration, forced bool) func(context.Context, string, bool) (string, string, error) {
	return func(ctx context.Context, cmd string, _ bool) (string, string, error) {
		select {
		case <-time.After(perCall):
		case <-ctx.Done():
			return "", "", errSlowHostCancelled
		}

		sh := exec.CommandContext(ctx, "sh", "-c", cmd) // #nosec G204 -- the command wr sends
		if forced {
			sh = exec.CommandContext(ctx, "sh", "-c", psOnlyForcedCommand) // #nosec G204 -- fixed

			sh.Env = append(os.Environ(), "SSH_ORIGINAL_COMMAND="+cmd)
		}

		out, err := sh.Output()

		return string(out), "", err
	}
}
