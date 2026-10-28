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
	"os"
	"testing"

	. "github.com/smartystreets/goconvey/convey"
)

// TestReaddQueuedWritesNothing proves that re-adding a ready job under its own
// RepGroup, which TestReaddQueuedKeepsRecord shows leaves its record alone, does
// not write to the bbolt database at all.
func TestReaddQueuedWritesNothing(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given a manager with a ready job in the queue", t, func() {
		_, serverConfig, addr, standardReqs, clientConnectTime := startDurabilityConfig(t)

		server, _, token, err := serve(ctx, serverConfig)
		So(err, ShouldBeNil)

		defer func() { server.Stop(ctx, true) }()

		jq, err := Connect(addr, serverConfig.CAFile, serverConfig.CertDomain, token, clientConnectTime)
		So(err, ShouldBeNil)

		defer disconnect(jq)

		newJob := func() *Job {
			return &Job{
				Cmd: "echo readd_queued", Cwd: testCwd, RepGroup: readdQueuedRepGroup,
				ReqGroup: readdQueuedRepGroup, Requirements: standardReqs, Retries: 3,
			}
		}

		inserts, _, err := jq.Add([]*Job{newJob()}, os.Environ(), true)
		So(err, ShouldBeNil)
		So(inserts, ShouldEqual, 1)

		Convey("re-adding it under its own RepGroup writes nothing to the database", func() {
			server.boltDB().wg.Wait(readdQueuedSettle)
			writesBefore := boltWrites(server.boltDB())

			readded, existed, erra := jq.Add([]*Job{newJob()}, os.Environ(), false)
			So(erra, ShouldBeNil)
			So(readded, ShouldEqual, 0)
			So(existed, ShouldEqual, 1)

			server.boltDB().wg.Wait(readdQueuedSettle)
			So(boltWrites(server.boltDB()), ShouldEqual, writesBefore)
		})
	})
}
