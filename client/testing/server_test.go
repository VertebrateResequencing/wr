/*******************************************************************************
 * Copyright (c) 2025-2026 Genome Research Ltd.
 *
 * Author: Michael Woolnough <mw31@sanger.ac.uk>
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

package testing

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/VertebrateResequencing/wr/jobqueue"
	jqs "github.com/VertebrateResequencing/wr/jobqueue/scheduler"
	. "github.com/smartystreets/goconvey/convey"
)

const (
	// seedJobCount is how many prior incomplete jobs TestServeWaitsForPublication
	// leaves in the database, so the server it then starts has a real recovery to
	// run before it can publish itself.
	seedJobCount = 3

	// serveTestCertDomain is the domain jobqueue.Serve generates its certificate
	// for when the config names none, and so the name a client must verify
	// against.
	serveTestCertDomain = "localhost"
)

func TestTestingServer(t *testing.T) {
	Convey("You can generate a server config", t, func() {
		cwd, err := os.Getwd()
		So(err, ShouldBeNil)

		config, f := PrepareWrConfig(t)

		newCwd, err := os.Getwd()
		So(err, ShouldBeNil)
		So(newCwd, ShouldNotEqual, cwd)

		f()

		newCwd, err = os.Getwd()
		So(err, ShouldBeNil)
		So(newCwd, ShouldEqual, cwd)

		So(config.Port, ShouldNotEqual, 0)
		So(config.WebPort, ShouldNotEqual, 0)
		So(config.SchedulerName, ShouldEqual, "local")

		dir := filepath.Dir(config.DBFile)
		fi, err := os.Stat(dir)
		So(err, ShouldBeNil)
		So(fi.IsDir(), ShouldBeTrue)

		Convey("Which can be used to start a server", func() {
			s := Serve(t, config)
			So(s.ServerInfo.Port, ShouldEqual, config.Port)

			var dialer net.Dialer

			conn, err := dialer.DialContext(context.Background(), "tcp", s.ServerInfo.Addr)
			So(err, ShouldBeNil)
			So(conn, ShouldNotBeNil)

			err = conn.Close()
			So(err, ShouldBeNil)

			s.Stop(context.Background(), false)
		})
	})
}

// TestServeWaitsForPublication covers E2 acceptance test 5. Serve is exported,
// so "the returned server is reachable" is the only post-condition an
// out-of-repo caller has to rely on, and jobqueue.Serve no longer provides it:
// it returns while prior-state recovery is still running, with the manager port
// closed. A DB holding prior incomplete jobs is what gives that recovery
// something to do, so a helper that did not wait would fail here on every run.
func TestServeWaitsForPublication(t *testing.T) {
	Convey("Serve returns a server a single Connect can reach", t, func() {
		ctx := context.Background()

		config, d := PrepareWrConfig(t)
		defer d()

		// production, because a development database is wiped when it is opened
		// and the flag that suppresses that is not part of the exported config.
		config.Deployment = "production"

		// the token is captured from the seeding server, which persists it: Serve
		// reuses an existing token file, so the same token authenticates against
		// the restarted server. Reading it here rather than after the restart is
		// what makes the Connect below the assertion that discriminates - the
		// token file is published at the same moment as the listener.
		token := seedIncompleteJobs(t, config)

		server := Serve(t, config)
		defer server.Stop(ctx, false)

		jq, err := jobqueue.Connect(managerAddr(config), config.CAFile,
			serveTestCertDomain, token, serverTimeout)
		So(err, ShouldBeNil)
		So(jq, ShouldNotBeNil)
		So(jq.Disconnect(), ShouldBeNil)
	})
}

// seedIncompleteJobs starts a server, adds seedJobCount jobs that nothing will
// run (the test config configures no runner command), and stops it, leaving
// config's database holding that many prior incomplete jobs. It returns the auth
// token, which the next server reuses.
func seedIncompleteJobs(t *testing.T, config jobqueue.ServerConfig) []byte {
	t.Helper()

	ctx := context.Background()
	server := Serve(t, config)

	token := readServerToken(t, config)

	jq, err := jobqueue.Connect(managerAddr(config), config.CAFile,
		serveTestCertDomain, token, serverTimeout)
	So(err, ShouldBeNil)

	jobs := make([]*jobqueue.Job, seedJobCount)
	for i := range jobs {
		jobs[i] = &jobqueue.Job{
			Cmd:          "echo seed " + strconv.Itoa(i),
			Cwd:          os.TempDir(),
			ReqGroup:     "clienttesting-seed",
			RepGroup:     "clienttesting-seed",
			Requirements: &jqs.Requirements{RAM: 10, Time: time.Second, Cores: 1},
		}
	}

	added, existed, err := jq.Add(jobs, os.Environ(), false)
	So(err, ShouldBeNil)
	So(added, ShouldEqual, seedJobCount)
	So(existed, ShouldEqual, 0)
	So(jq.Disconnect(), ShouldBeNil)

	server.Stop(ctx, true)

	return token
}

// readServerToken reads the auth token the server wrote when it published
// itself.
func readServerToken(t *testing.T, config jobqueue.ServerConfig) []byte {
	t.Helper()

	token, err := os.ReadFile(config.TokenFile)
	So(err, ShouldBeNil)

	return token
}

func managerAddr(config jobqueue.ServerConfig) string {
	return net.JoinHostPort(serveTestCertDomain, config.Port)
}

func TestLaneFreePort(t *testing.T) {
	Convey("Lane free port skips a lane port that is already in use", t, func() {
		lane, occupiedPort, listener := reserveFirstLanePort(t)
		defer func() {
			So(listener.Close(), ShouldBeNil)
		}()

		setLaneForTest(t, strconv.Itoa(lane), 0)

		port, err := laneFreePort()
		So(err, ShouldBeNil)
		So(port, ShouldNotEqual, occupiedPort)

		probe := listenOnPort(t, port)
		defer func() {
			So(probe.Close(), ShouldBeNil)
		}()
	})

	Convey("Lane free port uses a suite-provided port base", t, func() {
		const (
			base = 20000
			lane = 1
		)

		setLaneForTest(t, strconv.Itoa(lane), 0)
		setPortBaseForTest(t, strconv.Itoa(base))

		port, err := laneFreePort()
		So(err, ShouldBeNil)
		So(port, ShouldBeBetweenOrEqual, base+lane*laneSpan, base+(lane+1)*laneSpan-1)

		probe := listenOnPort(t, port)
		defer func() {
			So(probe.Close(), ShouldBeNil)
		}()
	})
}

func reserveFirstLanePort(t *testing.T) (int, int, net.Listener) {
	t.Helper()

	const firstCandidateOffset = 1

	for laneOffset := range 20 {
		lane := 30 + laneOffset
		port := defaultLaneBasePort + lane*laneSpan + firstCandidateOffset

		listener, err := tryListenOnPort(port)
		if err == nil {
			return lane, port, listener
		}
	}

	t.Fatal("could not reserve a lane port for testing")

	return 0, 0, nil
}

func setLaneForTest(t *testing.T, lane string, next int) {
	t.Helper()

	priorLane, hadPriorLane := os.LookupEnv("WR_TEST_LANE")
	priorNext := laneTestPortNext

	t.Cleanup(func() {
		laneTestPortNext = priorNext

		if hadPriorLane {
			_ = os.Setenv("WR_TEST_LANE", priorLane)

			return
		}

		_ = os.Unsetenv("WR_TEST_LANE")
	})

	So(os.Setenv("WR_TEST_LANE", lane), ShouldBeNil)

	laneTestPortNext = next
}

func setPortBaseForTest(t *testing.T, base string) {
	t.Helper()

	priorBase, hadPriorBase := os.LookupEnv(testPortBaseEnv)

	t.Cleanup(func() {
		if hadPriorBase {
			_ = os.Setenv(testPortBaseEnv, priorBase)

			return
		}

		_ = os.Unsetenv(testPortBaseEnv)
	})

	So(os.Setenv(testPortBaseEnv, base), ShouldBeNil)
}

func listenOnPort(t *testing.T, port int) net.Listener {
	t.Helper()

	listener, err := tryListenOnPort(port)
	So(err, ShouldBeNil)

	return listener
}

func tryListenOnPort(port int) (net.Listener, error) {
	var listenConfig net.ListenConfig

	return listenConfig.Listen(context.Background(), "tcp", portAddr(port))
}

func portAddr(port int) string {
	return net.JoinHostPort("0.0.0.0", strconv.Itoa(port))
}
