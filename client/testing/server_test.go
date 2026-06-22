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

	. "github.com/smartystreets/goconvey/convey"
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

			conn, err := net.Dial("tcp", s.ServerInfo.Addr)
			So(err, ShouldBeNil)
			So(conn, ShouldNotBeNil)

			err = conn.Close()
			So(err, ShouldBeNil)

			s.Stop(context.Background(), false)
		})
	})
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
}

func reserveFirstLanePort(t *testing.T) (int, int, net.Listener) {
	t.Helper()

	const firstCandidateOffset = 1

	for laneOffset := range 20 {
		lane := 30 + laneOffset
		port := laneBasePort + lane*laneSpan + firstCandidateOffset

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
