/*******************************************************************************
 * Copyright (c) 2025 Genome Research Ltd.
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

package testing

import (
	"context"
	"net"
	"os"
	"path/filepath"
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
