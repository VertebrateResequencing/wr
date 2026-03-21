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
	"os"
	"path/filepath"
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

		Convey("unknown top-level docker scopes are skipped with a warning", func() {
			var (
				cfg    *Config
				err    error
				stderr string
			)

			stderr = captureParseStderr(func() {
				cfg, err = ParseConfig(strings.NewReader("docker { enabled = true }\nparams { x = 1 }"))
			})

			So(err, ShouldBeNil)
			So(cfg.Params["x"], ShouldEqual, 1)
			So(stderr, ShouldContainSubstring, "skipping unsupported top-level config scope \"docker\"")
		})

		Convey("unknown top-level manifest scopes are skipped with a warning", func() {
			var (
				cfg    *Config
				err    error
				stderr string
			)

			stderr = captureParseStderr(func() {
				cfg, err = ParseConfig(strings.NewReader("manifest { name = 'test' }"))
			})

			So(err, ShouldBeNil)
			So(cfg, ShouldNotBeNil)
			So(stderr, ShouldContainSubstring, "skipping unsupported top-level config scope \"manifest\"")
		})

		Convey("unknown top-level env scopes are skipped with a warning", func() {
			var (
				cfg    *Config
				err    error
				stderr string
			)

			stderr = captureParseStderr(func() {
				cfg, err = ParseConfig(strings.NewReader("env { FOO = 'bar' }"))
			})

			So(err, ShouldBeNil)
			So(cfg, ShouldNotBeNil)
			So(stderr, ShouldContainSubstring, "skipping unsupported top-level config scope \"env\"")
		})

		Convey("unknown top-level singularity scopes are skipped with a warning", func() {
			var (
				cfg    *Config
				err    error
				stderr string
			)

			stderr = captureParseStderr(func() {
				cfg, err = ParseConfig(strings.NewReader("singularity { enabled = true }"))
			})

			So(err, ShouldBeNil)
			So(cfg, ShouldNotBeNil)
			So(stderr, ShouldContainSubstring, "skipping unsupported top-level config scope \"singularity\"")
		})

		Convey("unknown top-level timeline and report scopes are skipped with warnings", func() {
			var (
				cfg    *Config
				err    error
				stderr string
			)

			stderr = captureParseStderr(func() {
				cfg, err = ParseConfig(strings.NewReader("timeline { enabled = true }\nreport { enabled = true }"))
			})

			So(err, ShouldBeNil)
			So(cfg, ShouldNotBeNil)
			So(stderr, ShouldContainSubstring, "skipping unsupported top-level config scope \"timeline\"")
			So(stderr, ShouldContainSubstring, "skipping unsupported top-level config scope \"report\"")
		})

		Convey("ParseConfigFromPath loads params from an included config", func() {
			dir := t.TempDir()
			mainPath := filepath.Join(dir, "nextflow.config")
			includePath := filepath.Join(dir, "base.config")

			err := os.WriteFile(includePath, []byte("params { input = '/data' }\n"), 0o644)
			So(err, ShouldBeNil)

			err = os.WriteFile(mainPath, []byte("includeConfig 'base.config'\n"), 0o644)
			So(err, ShouldBeNil)

			cfg, parseErr := ParseConfigFromPath(mainPath)

			So(parseErr, ShouldBeNil)
			So(cfg.Params["input"], ShouldEqual, "/data")
		})

		Convey("included config params override earlier parent params and merge new keys", func() {
			dir := t.TempDir()
			mainPath := filepath.Join(dir, "nextflow.config")
			includePath := filepath.Join(dir, "base.config")

			err := os.WriteFile(includePath, []byte("params { x = 2 ; y = 3 }\n"), 0o644)
			So(err, ShouldBeNil)

			err = os.WriteFile(mainPath, []byte("params { x = 1 }\nincludeConfig 'base.config'\n"), 0o644)
			So(err, ShouldBeNil)

			cfg, parseErr := ParseConfigFromPath(mainPath)

			So(parseErr, ShouldBeNil)
			So(cfg.Params["x"], ShouldEqual, 2)
			So(cfg.Params["y"], ShouldEqual, 3)
		})

		Convey("missing included config returns an error mentioning the missing path", func() {
			dir := t.TempDir()
			mainPath := filepath.Join(dir, "nextflow.config")

			err := os.WriteFile(mainPath, []byte("includeConfig 'missing.config'\n"), 0o644)
			So(err, ShouldBeNil)

			_, parseErr := ParseConfigFromPath(mainPath)

			So(parseErr, ShouldNotBeNil)
			So(parseErr.Error(), ShouldContainSubstring, "missing.config")
		})

		Convey("circular includeConfig directives return an error", func() {
			dir := t.TempDir()
			mainPath := filepath.Join(dir, "a.config")
			includePath := filepath.Join(dir, "b.config")

			err := os.WriteFile(mainPath, []byte("includeConfig 'b.config'\n"), 0o644)
			So(err, ShouldBeNil)

			err = os.WriteFile(includePath, []byte("includeConfig 'a.config'\n"), 0o644)
			So(err, ShouldBeNil)

			_, parseErr := ParseConfigFromPath(mainPath)

			So(parseErr, ShouldNotBeNil)
			So(parseErr.Error(), ShouldContainSubstring, "circular includeConfig")
		})

		Convey("nested includeConfig paths resolve relative to the including file", func() {
			dir := t.TempDir()
			mainPath := filepath.Join(dir, "nextflow.config")
			nestedDir := filepath.Join(dir, "sub")
			nestedPath := filepath.Join(nestedDir, "nested.config")
			deepPath := filepath.Join(nestedDir, "deep.config")

			err := os.MkdirAll(nestedDir, 0o755)
			So(err, ShouldBeNil)

			err = os.WriteFile(deepPath, []byte("params { answer = 42 }\n"), 0o644)
			So(err, ShouldBeNil)

			err = os.WriteFile(nestedPath, []byte("includeConfig 'deep.config'\n"), 0o644)
			So(err, ShouldBeNil)

			err = os.WriteFile(mainPath, []byte("includeConfig 'sub/nested.config'\n"), 0o644)
			So(err, ShouldBeNil)

			cfg, parseErr := ParseConfigFromPath(mainPath)

			So(parseErr, ShouldBeNil)
			So(cfg.Params["answer"], ShouldEqual, 42)
		})

		Convey("ParseConfigFromPath preserves included params while skipping unknown top-level scopes", func() {
			dir := t.TempDir()
			mainPath := filepath.Join(dir, "nextflow.config")
			includePath := filepath.Join(dir, "x.config")

			err := os.WriteFile(includePath, []byte("params { a = 1 }\n"), 0o644)
			So(err, ShouldBeNil)

			err = os.WriteFile(mainPath, []byte("includeConfig 'x.config'\ndocker { enabled = true }\n"), 0o644)
			So(err, ShouldBeNil)

			var (
				cfg    *Config
				parseErr error
				stderr string
			)

			stderr = captureParseStderr(func() {
				cfg, parseErr = ParseConfigFromPath(mainPath)
			})

			So(parseErr, ShouldBeNil)
			So(cfg.Params["a"], ShouldEqual, 1)
			So(stderr, ShouldContainSubstring, "skipping unsupported top-level config scope \"docker\"")
		})
	})
}
