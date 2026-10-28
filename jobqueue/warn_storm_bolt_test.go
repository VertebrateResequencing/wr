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
	"fmt"
	"os"
	"regexp"
	"testing"
	"time"

	"github.com/VertebrateResequencing/wr/clog"
	. "github.com/smartystreets/goconvey/convey"
)

// This file holds the reservation half of the warning storm test, which stalls
// reservation writes by holding bbolt's writer, so is deleted with bbolt (spec
// F5). The slow-request half needs no bbolt and lives in warn_storm_test.go.

func TestWarningStormIsAggregated(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given a manager whose reservation writes are stalled", t, func() {
		restoreInterval := warnAggregateInterval
		warnAggregateInterval = time.Hour

		defer func() { warnAggregateInterval = restoreInterval }()

		_, serverConfig, addr, standardReqs, clientConnectTime := startDurabilityConfig(t)
		serverConfig.Timings.ReserveWriteWait = 50 * time.Millisecond

		server, _, token, err := serve(ctx, serverConfig)
		So(err, ShouldBeNil)

		stopped := false

		defer func() {
			if !stopped {
				server.Stop(ctx, true)
			}
		}()

		jq, err := Connect(addr, serverConfig.CAFile, serverConfig.CertDomain, token, clientConnectTime)
		So(err, ShouldBeNil)

		defer disconnect(jq)

		jobs := make([]*Job, warnStormReserves)
		for i := range jobs {
			jobs[i] = &Job{
				Cmd: fmt.Sprintf("echo %d", i), Cwd: testCwd, RepGroup: warnStormRepGroup,
				ReqGroup: warnStormRepGroup, Requirements: standardReqs,
			}
		}

		inserts, _, err := jq.Add(jobs, os.Environ(), true)
		So(err, ShouldBeNil)
		So(inserts, ShouldEqual, warnStormReserves)

		logs := clog.ToBufferAtLevel("warn")

		defer clog.ToDefault()

		holdTx, err := server.boltDB().bolt.Begin(true)
		So(err, ShouldBeNil)

		reserved := 0

		for range warnStormReserves {
			if job, errr := jq.Reserve(time.Second); errr == nil && job != nil {
				reserved++
			}
		}

		So(holdTx.Rollback(), ShouldBeNil)
		So(reserved, ShouldEqual, warnStormReserves)

		disconnect(jq)
		server.Stop(ctx, true)

		stopped = true
		out := logs.String()

		Convey("the reservation warning is logged once, with the rest counted and a sample job key", func() {
			So(countLines(out, reserveNotRecordedMsg, "repeated"), ShouldEqual, 1)

			summary := findLine(out, reserveNotRecordedMsg, "(repeated)")
			So(summary, ShouldContainSubstring, fmt.Sprintf("repeats=%d", warnStormReserves-1))
			So(regexp.MustCompile(`sample_key=[0-9a-f]{32}`).MatchString(summary), ShouldBeTrue)
		})
	})
}
