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
			So(cfg.Env, ShouldBeNil)
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

		Convey("profile process blocks retain selector overrides", func() {
			cfg, err := ParseConfig(strings.NewReader("profiles { test { process { withLabel: 'big' { cpus = 8 } } } }"))

			So(err, ShouldBeNil)
			So(cfg.Profiles, ShouldContainKey, "test")
			So(cfg.Profiles["test"].Selectors, ShouldHaveLength, 1)
			So(cfg.Profiles["test"].Selectors[0].Kind, ShouldEqual, "withLabel")
			So(cfg.Profiles["test"].Selectors[0].Pattern, ShouldEqual, "big")
			So(cfg.Profiles["test"].Selectors[0].Settings.Cpus, ShouldEqual, 8)
		})

		Convey("unknown top-level docker scopes are skipped with a warning", func() {
			cfg, err := ParseConfig(strings.NewReader("docker { enabled = true }\nparams { x = 1 }"))

			So(err, ShouldBeNil)
			So(cfg.Params["x"], ShouldEqual, 1)
			So(cfg.ContainerEngine, ShouldEqual, "docker")
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

		Convey("top-level env scopes populate Config.Env", func() {
			cfg, err := ParseConfig(strings.NewReader("env { FOO = 'bar' ; BAZ = 'qux' }"))

			So(err, ShouldBeNil)
			So(cfg, ShouldNotBeNil)
			So(cfg.Env, ShouldResemble, map[string]string{"FOO": "bar", "BAZ": "qux"})
		})

		Convey("top-level env scopes merge with other supported sections", func() {
			cfg, err := ParseConfig(strings.NewReader("env { FOO = 'bar' }\nparams { x = 1 }"))

			So(err, ShouldBeNil)
			So(cfg, ShouldNotBeNil)
			So(cfg.Env, ShouldResemble, map[string]string{"FOO": "bar"})
			So(cfg.Params["x"], ShouldEqual, 1)
		})

		Convey("unknown top-level singularity scopes are skipped with a warning", func() {
			cfg, err := ParseConfig(strings.NewReader("singularity { enabled = true }"))

			So(err, ShouldBeNil)
			So(cfg, ShouldNotBeNil)
			So(cfg.ContainerEngine, ShouldEqual, "singularity")
		})

		Convey("top-level apptainer scopes populate the container engine", func() {
			cfg, err := ParseConfig(strings.NewReader("apptainer { enabled = true }"))

			So(err, ShouldBeNil)
			So(cfg, ShouldNotBeNil)
			So(cfg.ContainerEngine, ShouldEqual, "apptainer")
		})

		Convey("container engine remains empty when container scopes are absent or disabled", func() {
			cfg, err := ParseConfig(strings.NewReader("docker { enabled = false }\nsingularity { enabled = false }\nparams { x = 1 }"))

			So(err, ShouldBeNil)
			So(cfg, ShouldNotBeNil)
			So(cfg.Params["x"], ShouldEqual, 1)
			So(cfg.ContainerEngine, ShouldEqual, "")
		})

		Convey("last enabled container scope wins when multiple scopes are defined", func() {
			cfg, err := ParseConfig(strings.NewReader("docker { enabled = true }\nsingularity { enabled = true }"))

			So(err, ShouldBeNil)
			So(cfg, ShouldNotBeNil)
			So(cfg.ContainerEngine, ShouldEqual, "singularity")
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

		Convey("M2 remaining standard Nextflow config scopes parse without error", func() {
			testCases := []struct {
				name          string
				input         string
				expectedParam string
				expectedValue any
			}{
				{
					name:          "wave scopes do not block later params parsing",
					input:         "wave { enabled = true }\nparams { x = 1 }",
					expectedParam: "x",
					expectedValue: 1,
				},
				{name: "tower scopes parse without error", input: "tower { enabled = true }"},
				{name: "conda scopes parse without error", input: "conda { enabled = true }"},
				{name: "dag scopes parse without error", input: "dag { overwrite = true }"},
				{name: "manifest scopes parse without error", input: "manifest { name = 'my-pipeline' }"},
				{name: "notification scopes parse without error", input: "notification { enabled = true }"},
				{name: "report scopes parse without error", input: "report { enabled = true }"},
				{name: "timeline scopes parse without error", input: "timeline { enabled = true }"},
				{name: "trace scopes parse without error", input: "trace { enabled = true }"},
				{name: "weblog scopes parse without error", input: "weblog { url = 'http://example.com' }"},
				{
					name:  "all standard remaining scopes parse together without error",
					input: "conda {}\ndag {}\nmanifest {}\nnotification {}\nreport {}\ntimeline {}\ntower {}\ntrace {}\nwave {}\nweblog {}",
				},
			}

			for _, testCase := range testCases {
				testCase := testCase
				Convey(testCase.name, func() {
					cfg, err := ParseConfig(strings.NewReader(testCase.input))

					So(err, ShouldBeNil)
					So(cfg, ShouldNotBeNil)
					if testCase.expectedParam != "" {
						So(cfg.Params, ShouldContainKey, testCase.expectedParam)
						So(cfg.Params[testCase.expectedParam], ShouldEqual, testCase.expectedValue)
					}
				})
			}
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

			cfg, parseErr := ParseConfigFromPath(mainPath)

			So(parseErr, ShouldBeNil)
			So(cfg.Params["a"], ShouldEqual, 1)
			So(cfg.ContainerEngine, ShouldEqual, "docker")
		})
	})
}

func TestParseConfigEnvScope(t *testing.T) {
	Convey("ParseConfig handles G1 env scopes", t, func() {
		Convey("env blocks populate Config.Env", func() {
			cfg, err := ParseConfig(strings.NewReader("env { FOO = 'bar' ; BAZ = 'qux' }"))

			So(err, ShouldBeNil)
			So(cfg, ShouldNotBeNil)
			So(cfg.Env, ShouldResemble, map[string]string{"FOO": "bar", "BAZ": "qux"})
		})

		Convey("missing env blocks leave Config.Env empty", func() {
			cfg, err := ParseConfig(strings.NewReader("params { x = 1 }"))

			So(err, ShouldBeNil)
			So(cfg, ShouldNotBeNil)
			So(cfg.Env, ShouldBeNil)
		})

		Convey("env blocks merge with other supported sections", func() {
			cfg, err := ParseConfig(strings.NewReader("env { FOO = 'bar' }\nparams { x = 1 }"))

			So(err, ShouldBeNil)
			So(cfg, ShouldNotBeNil)
			So(cfg.Env, ShouldResemble, map[string]string{"FOO": "bar"})
			So(cfg.Params["x"], ShouldEqual, 1)
		})
	})
}

func TestParseConfigContainerScopes(t *testing.T) {
	Convey("ParseConfig handles G3 container scopes", t, func() {
		Convey("docker enabled scopes set the container engine", func() {
			cfg, err := ParseConfig(strings.NewReader("docker { enabled = true }"))

			So(err, ShouldBeNil)
			So(cfg, ShouldNotBeNil)
			So(cfg.ContainerEngine, ShouldEqual, "docker")
		})

		Convey("singularity enabled scopes set the container engine", func() {
			cfg, err := ParseConfig(strings.NewReader("singularity { enabled = true }"))

			So(err, ShouldBeNil)
			So(cfg, ShouldNotBeNil)
			So(cfg.ContainerEngine, ShouldEqual, "singularity")
		})

		Convey("apptainer enabled scopes set the container engine", func() {
			cfg, err := ParseConfig(strings.NewReader("apptainer { enabled = true }"))

			So(err, ShouldBeNil)
			So(cfg, ShouldNotBeNil)
			So(cfg.ContainerEngine, ShouldEqual, "apptainer")
		})

		Convey("disabled or absent container scopes leave the engine empty", func() {
			cfg, err := ParseConfig(strings.NewReader("docker { enabled = false }\nsingularity { enabled = false }\nparams { x = 1 }"))

			So(err, ShouldBeNil)
			So(cfg, ShouldNotBeNil)
			So(cfg.Params["x"], ShouldEqual, 1)
			So(cfg.ContainerEngine, ShouldEqual, "")
		})

		Convey("the last enabled container scope wins", func() {
			cfg, err := ParseConfig(strings.NewReader("docker { enabled = true }\nsingularity { enabled = true }"))

			So(err, ShouldBeNil)
			So(cfg, ShouldNotBeNil)
			So(cfg.ContainerEngine, ShouldEqual, "singularity")
		})

		Convey("ParseConfigFromPath preserves included params while capturing container scopes", func() {
			dir := t.TempDir()
			mainPath := filepath.Join(dir, "nextflow.config")
			includePath := filepath.Join(dir, "x.config")

			err := os.WriteFile(includePath, []byte("params { a = 1 }\n"), 0o644)
			So(err, ShouldBeNil)

			err = os.WriteFile(mainPath, []byte("includeConfig 'x.config'\ndocker { enabled = true }\n"), 0o644)
			So(err, ShouldBeNil)

			cfg, parseErr := ParseConfigFromPath(mainPath)

			So(parseErr, ShouldBeNil)
			So(cfg.Params["a"], ShouldEqual, 1)
			So(cfg.ContainerEngine, ShouldEqual, "docker")
		})
	})
}

func TestParseConfigSelectors(t *testing.T) {
	Convey("ParseConfig handles A2 process selectors", t, func() {
		Convey("process withLabel selectors are parsed into Config.Selectors", func() {
			cfg, err := ParseConfig(strings.NewReader("process { withLabel: 'big_mem' { cpus = 8 } }"))

			So(err, ShouldBeNil)
			So(cfg.Selectors, ShouldHaveLength, 1)
			So(cfg.Selectors[0].Kind, ShouldEqual, "withLabel")
			So(cfg.Selectors[0].Pattern, ShouldEqual, "big_mem")
			So(cfg.Selectors[0].Settings, ShouldNotBeNil)
			So(cfg.Selectors[0].Settings.Cpus, ShouldEqual, 8)
		})

		Convey("process withName selectors parse resource settings", func() {
			cfg, err := ParseConfig(strings.NewReader("process { withName: 'ALIGN' { memory = '32 GB' } }"))

			So(err, ShouldBeNil)
			So(cfg.Selectors, ShouldHaveLength, 1)
			So(cfg.Selectors[0].Kind, ShouldEqual, "withName")
			So(cfg.Selectors[0].Pattern, ShouldEqual, "ALIGN")
			So(cfg.Selectors[0].Settings, ShouldNotBeNil)
			So(cfg.Selectors[0].Settings.Memory, ShouldEqual, 32768)
		})

		Convey("process selectors preserve declaration order", func() {
			cfg, err := ParseConfig(strings.NewReader("process { withLabel: 'small' { cpus = 1 } ; withLabel: 'big' { cpus = 16 } }"))

			So(err, ShouldBeNil)
			So(cfg.Selectors, ShouldHaveLength, 2)
			So(cfg.Selectors[0].Pattern, ShouldEqual, "small")
			So(cfg.Selectors[0].Settings.Cpus, ShouldEqual, 1)
			So(cfg.Selectors[1].Pattern, ShouldEqual, "big")
			So(cfg.Selectors[1].Settings.Cpus, ShouldEqual, 16)
		})

		Convey("process selectors preserve regex-prefixed patterns as text", func() {
			cfg, err := ParseConfig(strings.NewReader("process { withName: '~ALIGN.*' { cpus = 4 } }"))

			So(err, ShouldBeNil)
			So(cfg.Selectors, ShouldHaveLength, 1)
			So(cfg.Selectors[0].Pattern, ShouldEqual, "~ALIGN.*")
			So(cfg.Selectors[0].Settings.Cpus, ShouldEqual, 4)
		})

		Convey("generic process defaults and selectors coexist", func() {
			cfg, err := ParseConfig(strings.NewReader("process { cpus = 2 ; withLabel: 'big' { cpus = 16 } }"))

			So(err, ShouldBeNil)
			So(cfg.Process, ShouldNotBeNil)
			So(cfg.Process.Cpus, ShouldEqual, 2)
			So(cfg.Selectors, ShouldHaveLength, 1)
			So(cfg.Selectors[0].Settings.Cpus, ShouldEqual, 16)
		})

		Convey("selector settings populate all supported process defaults", func() {
			cfg, err := ParseConfig(strings.NewReader("process { withLabel: 'big' { memory = '64 GB' ; time = '2h' ; container = 'ubuntu:22.04' ; env { FOO = 'bar' } } }"))

			So(err, ShouldBeNil)
			So(cfg.Selectors, ShouldHaveLength, 1)
			So(cfg.Selectors[0].Settings, ShouldNotBeNil)
			So(cfg.Selectors[0].Settings.Memory, ShouldEqual, 65536)
			So(cfg.Selectors[0].Settings.Time, ShouldEqual, 120)
			So(cfg.Selectors[0].Settings.Container, ShouldEqual, "ubuntu:22.04")
			So(cfg.Selectors[0].Settings.Env, ShouldResemble, map[string]string{"FOO": "bar"})
		})
	})
}

func TestParseConfigNestedSelectors(t *testing.T) {
	Convey("ParseConfig handles L1 nested process selectors", t, func() {
		Convey("nested withLabel and withName selectors are stored as an outer selector with an inner selector", func() {
			cfg, err := ParseConfig(strings.NewReader("process { withLabel: 'big' { withName: 'ALIGN' { cpus = 32 } } }"))

			So(err, ShouldBeNil)
			So(cfg.Selectors, ShouldHaveLength, 1)
			So(cfg.Selectors[0].Kind, ShouldEqual, "withLabel")
			So(cfg.Selectors[0].Pattern, ShouldEqual, "big")
			So(cfg.Selectors[0].Inner, ShouldNotBeNil)
			So(cfg.Selectors[0].Inner.Kind, ShouldEqual, "withName")
			So(cfg.Selectors[0].Inner.Pattern, ShouldEqual, "ALIGN")
			So(cfg.Selectors[0].Inner.Settings, ShouldNotBeNil)
			So(cfg.Selectors[0].Inner.Settings.Cpus, ShouldEqual, 32)
		})
	})
}

func TestParseConfigExecutorScope(t *testing.T) {
	Convey("ParseConfig handles M1 executor scopes", t, func() {
		Convey("top-level executor blocks capture name, queueSize, and clusterOptions", func() {
			cfg, err := ParseConfig(strings.NewReader("executor { name = 'slurm'; queueSize = 100; clusterOptions = '--account=mylab' }"))

			So(err, ShouldBeNil)
			So(cfg.Executor, ShouldResemble, map[string]any{"name": "slurm", "queueSize": 100, "clusterOptions": "--account=mylab"})
		})

		Convey("top-level executor blocks capture queue settings", func() {
			cfg, err := ParseConfig(strings.NewReader("executor { queue = 'long' }"))

			So(err, ShouldBeNil)
			So(cfg.Executor, ShouldResemble, map[string]any{"queue": "long"})
		})

		Convey("missing executor blocks leave Config.Executor empty", func() {
			cfg, err := ParseConfig(strings.NewReader("params { x = 1 }"))

			So(err, ShouldBeNil)
			So(cfg.Executor, ShouldBeNil)
		})

		Convey("profile-scoped executor blocks are retained alongside top-level executor config", func() {
			cfg, err := ParseConfig(strings.NewReader("executor { name = 'local' }\nprofiles { hpc { executor { name = 'slurm' } } }"))

			So(err, ShouldBeNil)
			So(cfg.Executor, ShouldResemble, map[string]any{"name": "local"})
			So(cfg.Profiles, ShouldContainKey, "hpc")
			So(cfg.Profiles["hpc"].Executor, ShouldResemble, map[string]any{"name": "slurm"})
		})

		Convey("executor blocks preserve additional supported Nextflow settings", func() {
			cfg, err := ParseConfig(strings.NewReader("executor { name = 'local'; submitRateLimit = '1 sec' }\nprofiles { hpc { executor { name = 'slurm'; pollInterval = '30 sec'; queueSize = 16 } } }"))

			So(err, ShouldBeNil)
			So(cfg.Executor, ShouldResemble, map[string]any{"name": "local", "submitRateLimit": "1 sec"})
			So(cfg.Profiles, ShouldContainKey, "hpc")
			So(cfg.Profiles["hpc"].Executor, ShouldResemble, map[string]any{"name": "slurm", "pollInterval": "30 sec", "queueSize": 16})
		})
	})
}
