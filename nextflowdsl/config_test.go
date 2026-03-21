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

package nextflowdsl

import (
	"strings"
	"testing"

	. "github.com/smartystreets/goconvey/convey"
)

func TestParseConfig(t *testing.T) {
	Convey("ParseConfig handles B1 config files", t, func() {
		Convey("params blocks populate Config.Params", func() {
			cfg, err := ParseConfig(strings.NewReader("params { input = '/data' ; output = '/out' }"))

			So(err, ShouldBeNil)
			So(cfg, ShouldNotBeNil)
			So(cfg.Params["input"], ShouldEqual, "/data")
			So(cfg.Params["output"], ShouldEqual, "/out")
		})

		Convey("process blocks populate default resource directives", func() {
			cfg, err := ParseConfig(strings.NewReader("process { cpus = 2 ; memory = '4 GB' }"))

			So(err, ShouldBeNil)
			So(cfg.Process, ShouldNotBeNil)
			So(cfg.Process.Cpus, ShouldEqual, 2)
			So(cfg.Process.Memory, ShouldEqual, 4096)
		})

		Convey("profiles blocks retain profile-scoped params", func() {
			cfg, err := ParseConfig(strings.NewReader("profiles { test { params { input = '/test' } } }"))

			So(err, ShouldBeNil)
			So(cfg.Profiles, ShouldContainKey, "test")
			So(cfg.Profiles["test"].Params["input"], ShouldEqual, "/test")
		})

		Convey("process container defaults are captured", func() {
			cfg, err := ParseConfig(strings.NewReader("process { container = 'ubuntu:22.04' }"))

			So(err, ShouldBeNil)
			So(cfg.Process, ShouldNotBeNil)
			So(cfg.Process.Container, ShouldEqual, "ubuntu:22.04")
		})

		Convey("empty config returns zero-value fields without error", func() {
			cfg, err := ParseConfig(strings.NewReader("\n // nothing here\n"))

			So(err, ShouldBeNil)
			So(cfg, ShouldNotBeNil)
			So(cfg.Params, ShouldBeNil)
			So(cfg.Process, ShouldBeNil)
			So(cfg.Profiles, ShouldBeNil)
		})

		Convey("syntax errors include the line number", func() {
			_, err := ParseConfig(strings.NewReader("profiles {\n  test {\n    params { input = '/test' }\n"))

			So(err, ShouldNotBeNil)
			So(err.Error(), ShouldContainSubstring, "line 3")
		})

		Convey("simple arithmetic expressions in config values are evaluated", func() {
			cfg, err := ParseConfig(strings.NewReader("process { cpus = 2 + 3 }"))

			So(err, ShouldBeNil)
			So(cfg.Process, ShouldNotBeNil)
			So(cfg.Process.Cpus, ShouldEqual, 5)
		})

		Convey("process settings can use params-backed expressions from config input", func() {
			cfg, err := ParseConfig(strings.NewReader("params { cpus = 4 }\nprocess { cpus = params.cpus * 2 }"))

			So(err, ShouldBeNil)
			So(cfg.Process, ShouldNotBeNil)
			So(cfg.Process.Cpus, ShouldEqual, 8)
		})

		Convey("process settings can use params-backed expressions defined later in the config", func() {
			cfg, err := ParseConfig(strings.NewReader("process { cpus = params.cpus * 2 }\nparams { cpus = 4 }"))

			So(err, ShouldBeNil)
			So(cfg.Process, ShouldNotBeNil)
			So(cfg.Process.Cpus, ShouldEqual, 8)
		})

		Convey("profile process settings can use params-backed expressions defined later in the profile", func() {
			cfg, err := ParseConfig(strings.NewReader("profiles { test { process { cpus = params.cpus * 2 } params { cpus = 4 } } }"))

			So(err, ShouldBeNil)
			So(cfg.Profiles, ShouldContainKey, "test")
			So(cfg.Profiles["test"].Process, ShouldNotBeNil)
			So(cfg.Profiles["test"].Process.Cpus, ShouldEqual, 8)
		})
	})
}
