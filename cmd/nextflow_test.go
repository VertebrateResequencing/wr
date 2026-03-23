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

package cmd

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	clienttesting "github.com/VertebrateResequencing/wr/client/testing"
	"github.com/VertebrateResequencing/wr/jobqueue"
	jqs "github.com/VertebrateResequencing/wr/jobqueue/scheduler"
	"github.com/VertebrateResequencing/wr/nextflowdsl"
	. "github.com/smartystreets/goconvey/convey"
)

func TestGenerateNextflowRunID(t *testing.T) {
	Convey("generated run IDs are lowercase hex strings of at least 8 chars", t, func() {
		runID := generateNextflowRunID("/tmp/workflow.nf")
		So(regexp.MustCompile("^[0-9a-f]{8,}$").MatchString(runID), ShouldBeTrue)
	})
}

func TestNextflowRemoteWorkflowName(t *testing.T) {
	Convey("remote workflow names retain owner and repo to avoid collisions", t, func() {
		name, ok := nextflowRemoteWorkflowName("nextflow-io/hello")

		So(ok, ShouldBeTrue)
		So(name, ShouldEqual, "nextflow-io/hello")
	})

	Convey("remote workflow names allow dotted repo names", t, func() {
		name, ok := nextflowRemoteWorkflowName("nextflow-io/hello.world")

		So(ok, ShouldBeTrue)
		So(name, ShouldEqual, "nextflow-io/hello.world")
	})

	Convey("GitHub workflow URLs retain owner and repo to avoid collisions", t, func() {
		name, ok := nextflowRemoteWorkflowName("https://github.com/nextflow-io/hello.git")

		So(ok, ShouldBeTrue)
		So(name, ShouldEqual, "nextflow-io/hello")
	})

	Convey("resolved remote workflow names do not fall back to the cache revision for dotted repos", t, func() {
		name := nextflowResolvedWorkflowName(
			"nextflow-io/hello.world",
			filepath.Join("/tmp", "cache", "nextflow-io", "hello.world", "HEAD", "main.nf"),
			true,
		)

		So(name, ShouldEqual, "nextflow-io/hello.world")
	})

	Convey("local main.nf workflows use the containing directory name", t, func() {
		name := nextflowWorkflowName(filepath.Join("/tmp", "demo-workflow", "main.nf"))

		So(name, ShouldEqual, "demo-workflow")
	})
}

func TestReadCapturedOutput(t *testing.T) {
	Convey("readCapturedOutput covers D1", t, func() {
		tempDir := t.TempDir()

		Convey("missing files return an empty result", func() {
			output, err := readCapturedOutput(filepath.Join(tempDir, "missing"), 1<<20)

			So(err, ShouldBeNil)
			So(output, ShouldEqual, "")
		})

		Convey("non-empty files are returned as-is", func() {
			path := filepath.Join(tempDir, "stdout.txt")
			err := os.WriteFile(path, []byte("hello\n"), 0o644)
			So(err, ShouldBeNil)

			output, readErr := readCapturedOutput(path, 1<<20)

			So(readErr, ShouldBeNil)
			So(output, ShouldEqual, "hello\n")
		})

		Convey("empty files are ignored", func() {
			path := filepath.Join(tempDir, "empty.txt")
			err := os.WriteFile(path, nil, 0o644)
			So(err, ShouldBeNil)

			output, readErr := readCapturedOutput(path, 1<<20)

			So(readErr, ShouldBeNil)
			So(output, ShouldEqual, "")
		})

		Convey("whitespace-only files are ignored", func() {
			path := filepath.Join(tempDir, "whitespace.txt")
			err := os.WriteFile(path, []byte("  \n\t\n"), 0o644)
			So(err, ShouldBeNil)

			output, readErr := readCapturedOutput(path, 1<<20)

			So(readErr, ShouldBeNil)
			So(output, ShouldEqual, "")
		})

		Convey("files larger than maxBytes are truncated with an indicator", func() {
			path := filepath.Join(tempDir, "large.txt")
			content := strings.Repeat("x", 2*(1<<20))
			err := os.WriteFile(path, []byte(content), 0o644)
			So(err, ShouldBeNil)

			output, readErr := readCapturedOutput(path, 1<<20)

			So(readErr, ShouldBeNil)
			So(len(output), ShouldEqual, (1<<20)+len("\n[... output truncated ...]"))
			So(strings.HasSuffix(output, "[... output truncated ...]"), ShouldBeTrue)
		})

		Convey("file read errors are treated as empty output", func() {
			path := filepath.Join(tempDir, "not-a-file")
			err := os.Mkdir(path, 0o755)
			So(err, ShouldBeNil)

			output, readErr := readCapturedOutput(path, 1<<20)

			So(readErr, ShouldBeNil)
			So(output, ShouldEqual, "")
		})
	})
}

func TestNextflowOutputFormattingHelpers(t *testing.T) {
	Convey("D2 output formatting helpers match the spec", t, func() {
		Convey("formatJobOutput renders single-line stdout inline with the label", func() {
			So(formatJobOutput("[P]", "one line\n", ""), ShouldEqual, "[P] one line\n")
		})

		Convey("formatJobOutput renders multi-line stdout under an indented header", func() {
			So(formatJobOutput("[P]", "a\nb\n", ""), ShouldEqual, "[P]\n  a\n  b\n")
		})

		Convey("formatJobOutput prints stdout before stderr for single-line content", func() {
			So(formatJobOutput("[P]", "out\n", "err\n"), ShouldEqual, "[P] out\n[P] (stderr) err\n")
		})

		Convey("formatJobOutput renders stderr-only output with the stderr suffix", func() {
			So(formatJobOutput("[P]", "", "err\n"), ShouldEqual, "[P] (stderr) err\n")
		})

		Convey("formatJobOutput returns an empty string when both streams are empty", func() {
			So(formatJobOutput("[P]", "", ""), ShouldEqual, "")
		})

		Convey("formatJobOutput renders multi-line stderr with its own header", func() {
			So(formatJobOutput("[P]", "a\nb\n", "c\nd\n"), ShouldEqual, "[P]\n  a\n  b\n[P] (stderr)\n  c\n  d\n")
		})

		Convey("jobOutputLabel omits the index for single-instance processes", func() {
			So(jobOutputLabel("sayHello", "/w/nf-work/r1/sayHello", false), ShouldEqual, "[sayHello]")
		})

		Convey("jobOutputLabel includes the cwd-derived index for multi-instance processes", func() {
			So(jobOutputLabel("sayHello", "/w/nf-work/r1/sayHello/2", true), ShouldEqual, "[sayHello (2)]")
		})

		Convey("jobOutputLabel preserves cross-product indexes for each-expanded jobs", func() {
			So(jobOutputLabel("sayHello", "/w/nf-work/r1/sayHello/0_1", true), ShouldEqual, "[sayHello (0_1)]")
		})

		Convey("instanceIndexFromCwd extracts a trailing numeric path segment", func() {
			index, parts, ok := instanceIndexFromCwd("/w/nf-work/r1/proc/3")
			So(ok, ShouldBeTrue)
			So(index, ShouldEqual, "3")
			So(parts, ShouldResemble, []int{3})
		})

		Convey("instanceIndexFromCwd extracts cross-product suffixes", func() {
			index, parts, ok := instanceIndexFromCwd("/w/nf-work/r1/proc/2_10")
			So(ok, ShouldBeTrue)
			So(index, ShouldEqual, "2_10")
			So(parts, ShouldResemble, []int{2, 10})
		})

		Convey("instanceIndexFromCwd reports no index when the cwd has no trailing number", func() {
			index, parts, ok := instanceIndexFromCwd("/w/nf-work/r1/proc")
			So(ok, ShouldBeFalse)
			So(index, ShouldEqual, "")
			So(parts, ShouldBeNil)
		})
	})
}

func TestPrintJobsOutput(t *testing.T) {
	Convey("D3 printJobsOutput matches the spec", t, func() {
		makeJob := func(tempDir, process, cwdSuffix string) *jobqueue.Job {
			cwd := filepath.Join(tempDir, cwdSuffix)
			err := os.MkdirAll(cwd, 0o755)
			So(err, ShouldBeNil)

			return &jobqueue.Job{
				RepGroup: "nf.wf.r1." + process,
				Cwd:      cwd,
			}
		}

		writeOutput := func(job *jobqueue.Job, name, content string) {
			err := os.WriteFile(filepath.Join(job.Cwd, name), []byte(content), 0o644)
			So(err, ShouldBeNil)
		}

		Convey("output is grouped by process name alphabetically", func() {
			tempDir := t.TempDir()
			beta := makeJob(tempDir, "beta", "beta")
			alpha := makeJob(tempDir, "alpha", "alpha")
			writeOutput(beta, ".nf-stdout", "b\n")
			writeOutput(alpha, ".nf-stdout", "a\n")

			var out bytes.Buffer
			err := printJobsOutput(&out, []*jobqueue.Job{beta, alpha}, []*jobqueue.Job{beta, alpha}, 1<<20)

			So(err, ShouldBeNil)
			So(out.String(), ShouldEqual, "[alpha] a\n[beta] b\n")
		})

		Convey("jobs with the same process are ordered by instance index", func() {
			tempDir := t.TempDir()
			align1 := makeJob(tempDir, "align", filepath.Join("align", "1"))
			align0 := makeJob(tempDir, "align", filepath.Join("align", "0"))
			writeOutput(align1, ".nf-stdout", "one\n")
			writeOutput(align0, ".nf-stdout", "zero\n")

			var out bytes.Buffer
			err := printJobsOutput(&out, []*jobqueue.Job{align1, align0}, []*jobqueue.Job{align1, align0}, 1<<20)

			So(err, ShouldBeNil)
			So(out.String(), ShouldEqual, "[align (0)] zero\n[align (1)] one\n")
		})

		Convey("cross-product jobs are ordered numerically by their indexed suffix", func() {
			tempDir := t.TempDir()
			align010 := makeJob(tempDir, "align", filepath.Join("align", "0_10"))
			align02 := makeJob(tempDir, "align", filepath.Join("align", "0_2"))
			align11 := makeJob(tempDir, "align", filepath.Join("align", "1_1"))
			writeOutput(align010, ".nf-stdout", "ten\n")
			writeOutput(align02, ".nf-stdout", "two\n")
			writeOutput(align11, ".nf-stdout", "one-one\n")

			var out bytes.Buffer
			err := printJobsOutput(&out, []*jobqueue.Job{align010, align11, align02}, []*jobqueue.Job{align010, align11, align02}, 1<<20)

			So(err, ShouldBeNil)
			So(out.String(), ShouldEqual, "[align (0_2)] two\n[align (0_10)] ten\n[align (1_1)] one-one\n")
		})

		Convey("jobs with no captured output are skipped", func() {
			tempDir := t.TempDir()
			job := makeJob(tempDir, "align", filepath.Join("align", "0"))

			var out bytes.Buffer
			err := printJobsOutput(&out, []*jobqueue.Job{job}, []*jobqueue.Job{job}, 1<<20)

			So(err, ShouldBeNil)
			So(out.String(), ShouldEqual, "")
		})

		Convey("multi-instance detection uses allJobs rather than displayJobs", func() {
			tempDir := t.TempDir()
			align0 := makeJob(tempDir, "align", filepath.Join("align", "0"))
			align1 := makeJob(tempDir, "align", filepath.Join("align", "1"))
			writeOutput(align0, ".nf-stdout", "zero\n")

			var out bytes.Buffer
			err := printJobsOutput(&out, []*jobqueue.Job{align0}, []*jobqueue.Job{align0, align1}, 1<<20)

			So(err, ShouldBeNil)
			So(out.String(), ShouldEqual, "[align (0)] zero\n")
		})
	})
}

type nextflowCommandTestEnv struct {
	t      *testing.T
	server *jobqueue.Server
	undo   func()
	token  []byte

	managerAddrPath   string
	managerHostPort   string
	managerCAFile     string
	managerCertDomain string
}

func newNextflowCommandTestEnv(t *testing.T) *nextflowCommandTestEnv {
	t.Helper()

	deployment = "development"
	serverConfig, undo := clienttesting.PrepareWrConfig(t)
	server := clienttesting.Serve(t, serverConfig)
	initConfig()
	timeoutint = 2

	token, err := os.ReadFile(config.ManagerTokenFile)
	if err != nil {
		t.Fatalf("read manager token file: %v", err)
	}

	return &nextflowCommandTestEnv{
		t:                 t,
		server:            server,
		undo:              undo,
		token:             token,
		managerAddrPath:   filepath.Join(filepath.Dir(config.ManagerTokenFile), "manager.addr"),
		managerHostPort:   config.ManagerHost + ":" + config.ManagerPort,
		managerCAFile:     caFile,
		managerCertDomain: config.ManagerCertDomain,
	}
}

func (e *nextflowCommandTestEnv) cleanup() {
	e.server.Stop(context.Background(), true)
	e.undo()
}

func (e *nextflowCommandTestEnv) writeWorkflow(name, content string) string {
	e.t.Helper()

	return e.writeText(name, content)
}

func (e *nextflowCommandTestEnv) writeText(name, content string) string {
	e.t.Helper()

	path := filepath.Join(mustGetwd(e.t), name)
	err := os.MkdirAll(filepath.Dir(path), 0o755)
	if err != nil {
		e.t.Fatalf("create parent directory for %s: %v", name, err)
	}

	err = os.WriteFile(path, []byte(content), 0o644)
	if err != nil {
		e.t.Fatalf("write %s: %v", name, err)
	}

	return path
}

func mustGetwd(t *testing.T) string {
	t.Helper()

	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("get working directory: %v", err)
	}

	return wd
}

func (e *nextflowCommandTestEnv) executeRun(args ...string) error {
	e.t.Helper()

	_, err := e.executeRunWithOutput(args...)

	return err
}

func (e *nextflowCommandTestEnv) executeRunWithOutput(args ...string) (string, error) {
	e.t.Helper()

	options := nextflowRunOptions{}
	cmd := newNextflowRunCommand(&options)
	buf := &bytes.Buffer{}
	cmd.SetOut(buf)
	cmd.SetErr(buf)
	cmd.SetArgs(args)

	err := cmd.Execute()

	return buf.String(), err
}

func (e *nextflowCommandTestEnv) executeStatus(args ...string) (string, error) {
	e.t.Helper()

	options := nextflowStatusOptions{}
	cmd := newNextflowStatusCommand(&options)
	buf := &bytes.Buffer{}
	cmd.SetOut(buf)
	cmd.SetErr(buf)
	cmd.SetArgs(args)

	err := cmd.Execute()

	return buf.String(), err
}

func (e *nextflowCommandTestEnv) connectClient() *jobqueue.Client {
	e.t.Helper()

	connectWithAddr := func(serverAddr string) (*jobqueue.Client, error) {
		return jobqueue.Connect(serverAddr, e.managerCAFile, e.managerCertDomain, e.token, 2*time.Second)
	}

	serverAddr := e.managerHostPort
	if addrBytes, err := os.ReadFile(e.managerAddrPath); err == nil {
		serverAddr = string(addrBytes)
	}

	jq, err := connectWithAddr(serverAddr)
	if err == nil {
		return jq
	}
	if serverAddr != e.managerHostPort {
		jq, err = connectWithAddr(e.managerHostPort)
		if err == nil {
			return jq
		}
	}

	e.t.Fatalf("connect jobqueue client: %v", err)

	return nil
}

func (e *nextflowCommandTestEnv) addJob(job *jobqueue.Job) {
	e.t.Helper()

	jq := e.connectClient()
	defer func() {
		if err := jq.Disconnect(); err != nil {
			e.t.Fatalf("disconnect jobqueue client: %v", err)
		}
	}()

	inserted, already, err := jq.Add([]*jobqueue.Job{job}, nil, true)
	if err != nil {
		e.t.Fatalf("add job %s: %v", job.RepGroup, err)
	}
	if inserted != 1 || already != 0 {
		e.t.Fatalf("unexpected add counts for %s: inserted=%d already=%d", job.RepGroup, inserted, already)
	}
}

func (e *nextflowCommandTestEnv) addAndReserveJob(job *jobqueue.Job) (*jobqueue.Client, *jobqueue.Job) {
	e.t.Helper()

	jq := e.connectClient()
	inserted, already, err := jq.Add([]*jobqueue.Job{job}, nil, true)
	if err != nil {
		e.t.Fatalf("add job %s: %v", job.RepGroup, err)
	}
	if inserted != 1 || already != 0 {
		e.t.Fatalf("unexpected add counts for %s: inserted=%d already=%d", job.RepGroup, inserted, already)
	}

	reserved, err := jq.Reserve(2 * time.Second)
	if err != nil {
		_ = jq.Disconnect()
		e.t.Fatalf("reserve job %s: %v", job.RepGroup, err)
	}
	if reserved == nil {
		_ = jq.Disconnect()
		e.t.Fatalf("reserve job %s: got nil job", job.RepGroup)
	}
	if reserved.RepGroup != job.RepGroup {
		_ = jq.Disconnect()
		e.t.Fatalf("reserved unexpected job: got %s want %s", reserved.RepGroup, job.RepGroup)
	}

	return jq, reserved
}

func (e *nextflowCommandTestEnv) waitForRepGroupState(repGroup string, state jobqueue.JobState) *jobqueue.Job {
	e.t.Helper()

	jq := e.connectClient()
	defer func() {
		if err := jq.Disconnect(); err != nil {
			e.t.Fatalf("disconnect jobqueue client: %v", err)
		}
	}()

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		jobs, err := jq.GetByRepGroup(repGroup, false, 0, state, false, false)
		if err == nil && len(jobs) == 1 {
			return jobs[0]
		}

		time.Sleep(25 * time.Millisecond)
	}

	e.t.Fatalf("timed out waiting for %s to reach %s", repGroup, state)

	return nil
}

func (e *nextflowCommandTestEnv) waitForRepGroupStateCount(repGroup string, state jobqueue.JobState, count int) []*jobqueue.Job {
	e.t.Helper()

	jq := e.connectClient()
	defer func() {
		if err := jq.Disconnect(); err != nil {
			e.t.Fatalf("disconnect jobqueue client: %v", err)
		}
	}()

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		jobs, err := jq.GetByRepGroup(repGroup, false, 0, state, false, false)
		if err == nil && len(jobs) == count {
			return jobs
		}

		time.Sleep(25 * time.Millisecond)
	}

	e.t.Fatalf("timed out waiting for %s to reach %s count %d", repGroup, state, count)

	return nil
}

func (e *nextflowCommandTestEnv) jobsByRepGroupSubstring(repGroup string) []*jobqueue.Job {
	e.t.Helper()

	jq := e.connectClient()
	defer func() {
		if err := jq.Disconnect(); err != nil {
			e.t.Fatalf("disconnect jobqueue client: %v", err)
		}
	}()

	jobs, err := jq.GetByRepGroup(repGroup, true, 0, "", false, false)
	if err != nil {
		e.t.Fatalf("get jobs for %s: %v", repGroup, err)
	}

	sort.Slice(jobs, func(i, j int) bool {
		if jobs[i].RepGroup != jobs[j].RepGroup {
			return jobs[i].RepGroup < jobs[j].RepGroup
		}

		return jobs[i].Cmd < jobs[j].Cmd
	})

	return jobs
}

func TestNextflowRunCommand(t *testing.T) {
	Convey("wr nextflow run covers E1", t, func() {
		Convey("valid workflows are translated and submitted with an auto-generated lowercase hex run ID", func() {
			env := newNextflowCommandTestEnv(t)
			defer env.cleanup()

			workflowPath := env.writeWorkflow("workflow.nf", simplePipelineWorkflow("hello", "echo $x"))

			output, err := env.executeRunWithOutput(workflowPath)
			So(err, ShouldBeNil)

			jobs := env.jobsByRepGroupSubstring("nf.workflow.")
			So(jobs, ShouldHaveLength, 2)

			sort.Slice(jobs, func(i, j int) bool {
				return jobs[i].RepGroup < jobs[j].RepGroup
			})

			runID := strings.Split(jobs[0].RepGroup, ".")[2]
			So(regexp.MustCompile("^[0-9a-f]{8,}$").MatchString(runID), ShouldBeTrue)
			So(output, ShouldEqual, "Run ID: "+runID+"\n")
			So(jobs[0].RepGroup, ShouldEqual, "nf.workflow."+runID+".A")
			So(jobs[1].RepGroup, ShouldEqual, "nf.workflow."+runID+".B")
			So(jobs[0].DepGroups, ShouldResemble, []string{"nf." + runID + ".A"})
			So(jobs[1].Dependencies.DepGroups(), ShouldResemble, []string{"nf." + runID + ".A"})
		})

		Convey("explicit run IDs are propagated into submitted report groups", func() {
			env := newNextflowCommandTestEnv(t)
			defer env.cleanup()

			workflowPath := env.writeWorkflow("workflow.nf", simplePipelineWorkflow("hello", "echo $x"))

			output, err := env.executeRunWithOutput("--run-id", "myrun", workflowPath)
			So(err, ShouldBeNil)
			So(output, ShouldEqual, "")

			jobs := env.jobsByRepGroupSubstring("nf.workflow.myrun")
			So(jobs, ShouldHaveLength, 2)
			for _, job := range jobs {
				So(job.RepGroup, ShouldContainSubstring, ".myrun.")
			}
		})

		Convey("stub-run submits stub bodies for processes that define them", func() {
			env := newNextflowCommandTestEnv(t)
			defer env.cleanup()

			workflowPath := env.writeWorkflow("stubbed.nf", "process A {\nscript: 'real_cmd'\nstub: 'touch out.txt'\n}\nworkflow { A() }\n")

			err := env.executeRun("--stub-run", workflowPath)
			So(err, ShouldBeNil)

			jobs := env.jobsByRepGroupSubstring("nf.stubbed.")
			So(jobs, ShouldHaveLength, 1)
			So(jobs[0].Cmd, ShouldContainSubstring, "touch out.txt")
			So(jobs[0].Cmd, ShouldNotContainSubstring, "real_cmd")
		})

		Convey("config params are applied to translated job commands", func() {
			env := newNextflowCommandTestEnv(t)
			defer env.cleanup()

			workflowPath := env.writeWorkflow("configurable.nf", singleProcessWorkflow("cat ${params.input}"))
			configPath := env.writeText("nextflow.config", "params { input = '/cfg' }")

			err := env.executeRun("--config", configPath, workflowPath)
			So(err, ShouldBeNil)

			jobs := env.jobsByRepGroupSubstring("nf.configurable.")
			So(jobs, ShouldHaveLength, 1)
			So(jobs[0].Cmd, ShouldContainSubstring, "/cfg")
		})

		Convey("config includeConfig directives are applied when running workflows", func() {
			env := newNextflowCommandTestEnv(t)
			defer env.cleanup()

			workflowPath := env.writeWorkflow("config_include.nf", singleProcessWorkflow("cat ${params.input}"))
			configPath := env.writeText("configs/nextflow.config", "includeConfig 'base.config'\n")
			env.writeText("configs/base.config", "params { input = '/included' }\n")

			err := env.executeRun("--config", configPath, workflowPath)
			So(err, ShouldBeNil)

			jobs := env.jobsByRepGroupSubstring("nf.config_include.")
			So(jobs, ShouldHaveLength, 1)
			So(jobs[0].Cmd, ShouldContainSubstring, "/included")
		})

		Convey("params files override config params", func() {
			env := newNextflowCommandTestEnv(t)
			defer env.cleanup()

			workflowPath := env.writeWorkflow("configurable.nf", singleProcessWorkflow("cat ${params.input}"))
			configPath := env.writeText("nextflow.config", "params { input = '/cfg' }")
			paramsPath := env.writeText("params.json", `{"input":"/file"}`)

			err := env.executeRun("--config", configPath, "--params-file", paramsPath, workflowPath)
			So(err, ShouldBeNil)

			jobs := env.jobsByRepGroupSubstring("nf.configurable.")
			So(jobs, ShouldHaveLength, 1)
			So(jobs[0].Cmd, ShouldContainSubstring, "/file")
		})

		Convey("docker runtime uses WithDocker for containerized jobs", func() {
			env := newNextflowCommandTestEnv(t)
			defer env.cleanup()

			workflowPath := env.writeWorkflow("container.nf", "process A {\ncontainer 'ubuntu:22.04'\nscript: 'echo hello'\n}\nworkflow { A() }\n")

			err := env.executeRun("--container-runtime", "docker", workflowPath)
			So(err, ShouldBeNil)

			jobs := env.jobsByRepGroupSubstring("nf.container.")
			So(jobs, ShouldHaveLength, 1)
			So(jobs[0].WithDocker, ShouldEqual, "ubuntu:22.04")
			So(jobs[0].WithSingularity, ShouldEqual, "")
		})

		Convey("selected profiles contribute their config-scoped params", func() {
			env := newNextflowCommandTestEnv(t)
			defer env.cleanup()
			workflowPath := env.writeWorkflow("profiled.nf", singleProcessWorkflow("cat ${params.input}"))
			configPath := env.writeText("nextflow.config", "profiles { test { params { input = '/profile' } } }")

			err := env.executeRun("--config", configPath, "--profile", "test", workflowPath)
			So(err, ShouldBeNil)

			jobs := env.jobsByRepGroupSubstring("nf.profiled.")
			So(jobs, ShouldHaveLength, 1)
			So(jobs[0].Cmd, ShouldContainSubstring, "/profile")
		})

		Convey("selected profiles contribute process selectors", func() {
			env := newNextflowCommandTestEnv(t)
			defer env.cleanup()

			workflowPath := env.writeWorkflow("profile_selector.nf", "process ALIGN {\nlabel 'big'\nscript: 'echo hi'\n}\nworkflow { ALIGN() }\n")
			configPath := env.writeText("nextflow.config", "profiles { test { process { withLabel: 'big' { cpus = 8 } } } }")

			err := env.executeRun("--config", configPath, "--profile", "test", workflowPath)
			So(err, ShouldBeNil)

			jobs := env.jobsByRepGroupSubstring("nf.profile_selector.")
			So(jobs, ShouldHaveLength, 1)
			So(jobs[0].Requirements.Cores, ShouldEqual, 8)
		})

		Convey("profiles require an explicit config path so selection is not silently ignored", func() {
			env := newNextflowCommandTestEnv(t)
			defer env.cleanup()

			workflowPath := env.writeWorkflow("profile_requires_config.nf", singleProcessWorkflow("echo hello"))

			err := env.executeRun("--profile", "test", workflowPath)
			So(err, ShouldNotBeNil)
			So(err.Error(), ShouldContainSubstring, "--profile requires --config")
		})

		Convey("unknown selected profiles fail fast with the available profile names", func() {
			env := newNextflowCommandTestEnv(t)
			defer env.cleanup()

			workflowPath := env.writeWorkflow("profile_missing.nf", singleProcessWorkflow("echo hello"))
			configPath := env.writeText("nextflow.config", "profiles { test { params { input = '/profile' } } other { params { input = '/other' } } }")

			err := env.executeRun("--config", configPath, "--profile", "typo", workflowPath)
			So(err, ShouldNotBeNil)
			So(err.Error(), ShouldContainSubstring, "unknown config profile \"typo\"")
			So(err.Error(), ShouldContainSubstring, "available profiles: other, test")
		})

		Convey("local imported processes are resolved before translation", func() {
			env := newNextflowCommandTestEnv(t)
			defer env.cleanup()

			workflowPath := env.writeWorkflow("imported_process.nf", "include { helper } from './modules/helper.nf'\nworkflow { helper() }\n")
			env.writeText("modules/helper.nf", "process helper {\nscript: 'echo imported'\n}\n")

			err := env.executeRun(workflowPath)
			So(err, ShouldBeNil)

			jobs := env.jobsByRepGroupSubstring("nf.imported_process.")
			So(jobs, ShouldHaveLength, 1)
			So(jobs[0].RepGroup, ShouldContainSubstring, ".helper")
		})

		Convey("aliased imported processes use the aliased call target", func() {
			env := newNextflowCommandTestEnv(t)
			defer env.cleanup()

			workflowPath := env.writeWorkflow("imported_alias.nf", "include { helper as aliased } from './modules/helper.nf'\nworkflow { aliased() }\n")
			env.writeText("modules/helper.nf", "process helper {\nscript: 'echo imported'\n}\n")

			err := env.executeRun(workflowPath)
			So(err, ShouldBeNil)

			jobs := env.jobsByRepGroupSubstring("nf.imported_alias.")
			So(jobs, ShouldHaveLength, 1)
			So(jobs[0].RepGroup, ShouldContainSubstring, ".aliased")
		})

		Convey("imported subworkflows bring their internal process dependencies", func() {
			env := newNextflowCommandTestEnv(t)
			defer env.cleanup()

			workflowPath := env.writeWorkflow("imported_subworkflow.nf", "include { pack } from './modules/pack.nf'\nworkflow { pack() }\n")
			env.writeText("modules/pack.nf", "process helper {\nscript: 'echo imported'\n}\nworkflow pack {\nhelper()\n}\n")

			err := env.executeRun(workflowPath)
			So(err, ShouldBeNil)

			jobs := env.jobsByRepGroupSubstring("nf.imported_subworkflow.")
			So(jobs, ShouldHaveLength, 1)
			So(jobs[0].RepGroup, ShouldContainSubstring, ".pack.helper")
		})

		Convey("aliased imports rewrite internal channel references inside imported subworkflows", func() {
			env := newNextflowCommandTestEnv(t)
			defer env.cleanup()

			workflowPath := env.writeWorkflow("imported_alias_refs.nf", "include { helper as aliased ; pack } from './modules/pack.nf'\nworkflow { pack() }\n")
			env.writeText("modules/pack.nf", "process helper {\noutput: val 'hello'\nscript: 'echo hello'\n}\nprocess consumer {\ninput: val x\nscript: 'echo $x'\n}\nworkflow pack {\nhelper()\nconsumer(helper.out)\n}\n")

			err := env.executeRun(workflowPath)
			So(err, ShouldBeNil)

			jobs := env.jobsByRepGroupSubstring("nf.imported_alias_refs.")
			So(jobs, ShouldHaveLength, 2)
			So(jobs[0].RepGroup+jobs[1].RepGroup, ShouldContainSubstring, ".aliased")
			So(jobs[0].RepGroup+jobs[1].RepGroup, ShouldContainSubstring, ".pack.consumer")
		})

		Convey("remote owner/repo workflow identifiers resolve through the GitHub download path", func() {
			homeDir := t.TempDir()
			t.Setenv("HOME", homeDir)
			cacheEntry := filepath.Join(homeDir, ".wr", "nextflow_modules", "nextflow-io", "hello", "HEAD", "main.nf")
			_, statErr := os.Stat(cacheEntry)
			So(os.IsNotExist(statErr), ShouldBeTrue)

			cloneCalls, restoreGit := stubGitHubWorkflowDownload(t, homeDir, "nextflow-io", "hello")
			defer restoreGit()

			env := newNextflowCommandTestEnv(t)
			defer env.cleanup()

			err := env.executeRun("nextflow-io/hello")
			So(err, ShouldBeNil)
			So(*cloneCalls, ShouldEqual, 1)
			_, statErr = os.Stat(cacheEntry)
			So(statErr, ShouldBeNil)

			jobs := env.jobsByRepGroupSubstring("nf." + nextflowRepGroupToken("nextflow-io/hello") + ".")
			So(jobs, ShouldHaveLength, 4)
			So(jobs[0].RepGroup, ShouldContainSubstring, ".sayHello")

			commands := []string{jobs[0].Cmd, jobs[1].Cmd, jobs[2].Cmd, jobs[3].Cmd}
			So(strings.Join(commands, "\n"), ShouldContainSubstring, "Bonjour")
			So(strings.Join(commands, "\n"), ShouldContainSubstring, "Ciao")
			So(strings.Join(commands, "\n"), ShouldContainSubstring, "Hello")
			So(strings.Join(commands, "\n"), ShouldContainSubstring, "Hola")
		})

		Convey("remote GitHub workflow URLs resolve through the GitHub download path", func() {
			homeDir := t.TempDir()
			t.Setenv("HOME", homeDir)
			cacheEntry := filepath.Join(homeDir, ".wr", "nextflow_modules", "nextflow-io", "hello", "HEAD", "main.nf")
			cloneCalls, restoreGit := stubGitHubWorkflowDownload(t, homeDir, "nextflow-io", "hello")
			defer restoreGit()

			env := newNextflowCommandTestEnv(t)
			defer env.cleanup()

			err := env.executeRun("https://github.com/nextflow-io/hello")
			So(err, ShouldBeNil)
			So(*cloneCalls, ShouldEqual, 1)
			_, statErr := os.Stat(cacheEntry)
			So(statErr, ShouldBeNil)

			jobs := env.jobsByRepGroupSubstring("nf." + nextflowRepGroupToken("nextflow-io/hello") + ".")
			So(jobs, ShouldHaveLength, 4)
			So(jobs[0].RepGroup, ShouldContainSubstring, ".sayHello")
		})

		Convey("missing workflow files return an error naming the missing path", func() {
			env := newNextflowCommandTestEnv(t)
			defer env.cleanup()

			err := env.executeRun("missing.nf")
			So(err, ShouldNotBeNil)
			So(err.Error(), ShouldContainSubstring, "missing.nf")
		})

		Convey("workflow syntax errors surface parse line numbers", func() {
			env := newNextflowCommandTestEnv(t)
			defer env.cleanup()

			workflowPath := env.writeWorkflow("broken.nf", "process A {\nscript: 'echo hello'\n")

			err := env.executeRun(workflowPath)
			So(err, ShouldNotBeNil)
			So(err.Error(), ShouldContainSubstring, "line 2")
		})

		Convey("CLI params win over config and params-file values", func() {
			env := newNextflowCommandTestEnv(t)
			defer env.cleanup()

			workflowPath := env.writeWorkflow("configurable.nf", singleProcessWorkflow("cat ${params.input}"))
			configPath := env.writeText("nextflow.config", "params { input = '/cfg' }")
			paramsPath := env.writeText("params.yaml", "input: /file\n")

			err := env.executeRun("--config", configPath, "--params-file", paramsPath, "--param", "input=/override", workflowPath)
			So(err, ShouldBeNil)

			jobs := env.jobsByRepGroupSubstring("nf.configurable.")
			So(jobs, ShouldHaveLength, 1)
			So(jobs[0].Cmd, ShouldContainSubstring, "/override")
		})

		Convey("nested CLI param overrides preserve sibling nested params", func() {
			env := newNextflowCommandTestEnv(t)
			defer env.cleanup()

			workflowPath := env.writeWorkflow("nested.nf", singleProcessWorkflow("cat ${params.input.dir}/${params.input.file}"))
			configPath := env.writeText("nextflow.config", "params { input = 'unused' }")
			paramsPath := env.writeText("params.json", `{"input":{"dir":"/data","file":"a.fq"}}`)

			err := env.executeRun("--config", configPath, "--params-file", paramsPath, "--param", "input.file=b.fq", workflowPath)
			So(err, ShouldBeNil)

			jobs := env.jobsByRepGroupSubstring("nf.nested.")
			So(jobs, ShouldHaveLength, 1)
			So(jobs[0].Cmd, ShouldContainSubstring, "/data/b.fq")
		})

		Convey("numeric CLI params keep numeric semantics for directive arithmetic", func() {
			env := newNextflowCommandTestEnv(t)
			defer env.cleanup()

			workflowPath := env.writeWorkflow("numeric_cli.nf", singleProcessWorkflow("echo hello"))
			configPath := env.writeText("nextflow.config", "params { cpus = 1 }\nprocess { cpus = params.cpus * 2 }")

			err := env.executeRun("--config", configPath, "--param", "cpus=4", workflowPath)
			So(err, ShouldBeNil)

			jobs := env.jobsByRepGroupSubstring("nf.numeric_cli.")
			So(jobs, ShouldHaveLength, 1)
			So(jobs[0].Requirements.Cores, ShouldEqual, 8)
		})
	})
}

func simplePipelineWorkflow(outputValue, consumerScript string) string {
	return "process A {\noutput: val '" + outputValue + "'\nscript: 'echo hello'\n}\n" +
		"process B {\ninput: val x\nscript: '" + consumerScript + "'\n}\n" +
		"workflow { A(); B(A.out) }\n"
}

func singleProcessWorkflow(script string) string {
	return "process A {\nscript: '" + script + "'\n}\nworkflow { A() }\n"
}

func stubGitHubWorkflowDownload(t *testing.T, homeDir, owner, repo string) (*int, func()) {
	t.Helper()

	cloneCalls := 0
	cacheDir := filepath.Join(homeDir, ".wr", "nextflow_modules")
	restore := nextflowdsl.SetGitHubResolverRunGitForTesting(func(dir string, args ...string) error {
		t.Helper()

		cloneCalls++
		cachePath := filepath.Join(cacheDir, owner, repo, "HEAD")
		expectedArgs := []string{"clone", "--depth", "1", "https://github.com/" + owner + "/" + repo + ".git", cachePath}
		if dir != cacheDir {
			t.Fatalf("clone dir = %q, want %q", dir, cacheDir)
		}
		if !reflect.DeepEqual(args, expectedArgs) {
			t.Fatalf("clone args = %v, want %v", args, expectedArgs)
		}

		writeDownloadedGitHubWorkflow(t, cachePath)

		return nil
	})

	return &cloneCalls, restore
}

func writeDownloadedGitHubWorkflow(t *testing.T, repoPath string) {
	t.Helper()

	files := map[string]string{
		"README.md":       "# nextflow-io/hello\n\nA minimal greeting workflow used by the command integration tests.\n",
		"nextflow.config": "manifest { name = 'nextflow-io/hello' }\n",
		"main.nf":         remoteHelloWorkflow(),
	}

	for name, content := range files {
		path := filepath.Join(repoPath, name)
		err := os.MkdirAll(filepath.Dir(path), 0o755)
		if err != nil {
			t.Fatalf("create downloaded workflow parent for %s: %v", name, err)
		}

		err = os.WriteFile(path, []byte(content), 0o644)
		if err != nil {
			t.Fatalf("write downloaded workflow file %s: %v", name, err)
		}
	}
}

func remoteHelloWorkflow() string {
	return "#!/usr/bin/env nextflow\n\n" +
		"// downloaded from github.com/nextflow-io/hello\n\n" +
		"process sayHello {\n" +
		"    input:\n" +
		"    val x\n\n" +
		"    output:\n" +
		"  \n" +
		" stdout\n\n" +
		"    script:\n" +
		"    \"\"\"\n" +
		"    echo '${x} world!'\n" +
		"    \"\"\"\n" +
		"}\n\n" +
		"workflow {\n" +
		"   \n" +
		"Channel.of('Bonjour', 'Ciao', 'Hello', 'Hola') | sayHello | view\n" +
		"}\n"
}

func TestNextflowRunCommandContainerRuntimeDefaults(t *testing.T) {
	Convey("K1 container runtime selection uses config defaults only when the CLI flag is unset", t, func() {
		Convey("config container engine is used when no container-runtime flag is provided", func() {
			env := newNextflowCommandTestEnv(t)
			defer env.cleanup()

			workflowPath := env.writeWorkflow("config_docker_runtime.nf", "process A {\ncontainer 'ubuntu:22.04'\nscript: 'echo hello'\n}\nworkflow { A() }\n")
			configPath := env.writeText("nextflow.config", "docker { enabled = true }\n")

			err := env.executeRun("--config", configPath, workflowPath)
			So(err, ShouldBeNil)

			jobs := env.jobsByRepGroupSubstring("nf.config_docker_runtime.")
			So(jobs, ShouldHaveLength, 1)
			So(jobs[0].WithDocker, ShouldEqual, "ubuntu:22.04")
			So(jobs[0].WithSingularity, ShouldEqual, "")
		})

		Convey("container-runtime flag overrides the config container engine", func() {
			env := newNextflowCommandTestEnv(t)
			defer env.cleanup()

			workflowPath := env.writeWorkflow("config_cli_runtime.nf", "process A {\ncontainer 'ubuntu:22.04'\nscript: 'echo hello'\n}\nworkflow { A() }\n")
			configPath := env.writeText("nextflow.config", "singularity { enabled = true }\n")

			err := env.executeRun("--config", configPath, "--container-runtime", "docker", workflowPath)
			So(err, ShouldBeNil)

			jobs := env.jobsByRepGroupSubstring("nf.config_cli_runtime.")
			So(jobs, ShouldHaveLength, 1)
			So(jobs[0].WithDocker, ShouldEqual, "ubuntu:22.04")
			So(jobs[0].WithSingularity, ShouldEqual, "")
		})

		Convey("containerized jobs default to singularity when neither config nor CLI sets a runtime", func() {
			env := newNextflowCommandTestEnv(t)
			defer env.cleanup()

			workflowPath := env.writeWorkflow("default_runtime.nf", "process A {\ncontainer 'ubuntu:22.04'\nscript: 'echo hello'\n}\nworkflow { A() }\n")

			err := env.executeRun(workflowPath)
			So(err, ShouldBeNil)

			jobs := env.jobsByRepGroupSubstring("nf.default_runtime.")
			So(jobs, ShouldHaveLength, 1)
			So(jobs[0].WithDocker, ShouldEqual, "")
			So(jobs[0].WithSingularity, ShouldEqual, "ubuntu:22.04")
		})

		Convey("apptainer config defaults reuse singularity-backed job execution", func() {
			env := newNextflowCommandTestEnv(t)
			defer env.cleanup()

			workflowPath := env.writeWorkflow("config_apptainer_runtime.nf", "process A {\ncontainer 'ubuntu:22.04'\nscript: 'echo hello'\n}\nworkflow { A() }\n")
			configPath := env.writeText("nextflow.config", "apptainer { enabled = true }\n")

			err := env.executeRun("--config", configPath, workflowPath)
			So(err, ShouldBeNil)

			jobs := env.jobsByRepGroupSubstring("nf.config_apptainer_runtime.")
			So(jobs, ShouldHaveLength, 1)
			So(jobs[0].WithDocker, ShouldEqual, "")
			So(jobs[0].WithSingularity, ShouldEqual, "ubuntu:22.04")
		})
	})
}

func TestNextflowStatusCommand(t *testing.T) {
	Convey("wr nextflow status covers E4", t, func() {
		Convey("it does not hide ordinary jobs just because their scoped process name matches a helper suffix", func() {
			testCases := []string{
				"nf.mywf.r1.onError",
				"nf.mywf.r1.onComplete",
				"nf.mywf.r1.SUBWF.onError",
				"nf.mywf.r1.SUBWF.onComplete",
				"nf.mywf.r1.SUBWF.maxErrors",
			}

			for _, repGroup := range testCases {
				parsed, ok := parseNextflowRepGroup(repGroup)
				So(ok, ShouldBeTrue)

				hidden := nextflowStatusJobHidden(&jobqueue.Job{RepGroup: repGroup, ReqGroup: "nf.user.process"}, parsed)

				So(hidden, ShouldBeFalse)
			}
		})

		Convey("it aggregates pending, running, complete, and buried counts per process for a run id", func() {
			env := newNextflowCommandTestEnv(t)
			defer env.cleanup()

			completeClient, completeJob := env.addAndReserveJob(newStatusTestJob("nf.mywf.r1.prepare", "true"))
			So(completeClient.Execute(context.Background(), completeJob, "bash"), ShouldBeNil)
			So(completeClient.Disconnect(), ShouldBeNil)
			env.waitForRepGroupState("nf.mywf.r1.prepare", jobqueue.JobStateComplete)

			buriedClient, buriedJob := env.addAndReserveJob(newStatusTestJob("nf.mywf.r1.publish", "false"))
			So(buriedClient.Execute(context.Background(), buriedJob, "bash"), ShouldNotBeNil)
			So(buriedClient.Disconnect(), ShouldBeNil)
			env.waitForRepGroupState("nf.mywf.r1.publish", jobqueue.JobStateBuried)

			runningJobDef := newStatusTestJob("nf.mywf.r1.call", "sleep 2")
			runningJobDef.DepGroups = []string{"nf.mywf.r1.call.dep"}
			runningClient, runningJob := env.addAndReserveJob(runningJobDef)
			executeErr := make(chan error, 1)
			go func() {
				executeErr <- runningClient.Execute(context.Background(), runningJob, "bash")
				_ = runningClient.Disconnect()
			}()

			env.waitForRepGroupState("nf.mywf.r1.call", jobqueue.JobStateRunning)
			env.addJob(&jobqueue.Job{
				Cmd:          "echo waiting",
				Cwd:          os.TempDir(),
				ReqGroup:     "nextflow-status",
				Requirements: &jqs.Requirements{RAM: 1, Time: time.Minute, Cores: 1, Other: make(map[string]string)},
				Retries:      uint8(0),
				Override:     uint8(2),
				RepGroup:     "nf.mywf.r1.align",
				Dependencies: jobqueue.Dependencies{jobqueue.NewDepGroupDependency("nf.mywf.r1.call.dep")},
			})
			env.waitForRepGroupState("nf.mywf.r1.align", jobqueue.JobStateDependent)

			output, err := env.executeStatus("--run-id", "r1")
			So(err, ShouldBeNil)

			counts := statusCounts(output)
			So(counts, ShouldContainKey, "align")
			So(counts["align"], ShouldResemble, []string{"1", "0", "0", "0", "1"})
			So(counts, ShouldContainKey, "call")
			So(counts["call"], ShouldResemble, []string{"0", "1", "0", "0", "1"})
			So(counts, ShouldContainKey, "prepare")
			So(counts["prepare"], ShouldResemble, []string{"0", "0", "1", "0", "1"})
			So(counts, ShouldContainKey, "publish")
			So(counts["publish"], ShouldResemble, []string{"0", "0", "0", "1", "1"})

			So(<-executeErr, ShouldBeNil)
		})

		Convey("it prints no jobs found and succeeds when the run id has no matches", func() {
			env := newNextflowCommandTestEnv(t)
			defer env.cleanup()

			output, err := env.executeStatus("--run-id", "missing")
			So(err, ShouldBeNil)
			So(strings.TrimSpace(output), ShouldEqual, "no jobs found")
		})

		Convey("it filters results to the requested workflow", func() {
			env := newNextflowCommandTestEnv(t)
			defer env.cleanup()

			env.addJob(newStatusTestJob("nf.wf1.r1.alpha", "echo wf1"))
			env.addJob(newStatusTestJob("nf.wf2.r2.beta", "echo wf2"))

			output, err := env.executeStatus("--workflow", "wf1")
			So(err, ShouldBeNil)

			counts := statusCounts(output)
			So(counts, ShouldContainKey, "alpha")
			So(counts, ShouldNotContainKey, "beta")
		})

		Convey("it handles dotted workflow names and run ids", func() {
			env := newNextflowCommandTestEnv(t)
			defer env.cleanup()

			env.addJob(newStatusTestJob("nf."+nextflowRepGroupToken("rna.seq")+"."+nextflowRepGroupToken("2026.03")+".align", "echo dotted"))

			output, err := env.executeStatus("--workflow", "rna.seq", "--run-id", "2026.03")
			So(err, ShouldBeNil)

			counts := statusCounts(output)
			So(counts, ShouldContainKey, "align")
			So(counts["align"], ShouldResemble, []string{"1", "0", "0", "0", "1"})
		})

		Convey("it rejects --output without --run-id", func() {
			options := nextflowStatusOptions{}
			cmd := newNextflowStatusCommand(&options)
			cmd.SetArgs([]string{"--output"})

			err := cmd.Execute()

			So(err, ShouldNotBeNil)
			So(err.Error(), ShouldContainSubstring, "--output requires --run-id")
		})

		Convey("it prints captured stdout for completed jobs after the count table", func() {
			env := newNextflowCommandTestEnv(t)
			defer env.cleanup()

			completeClient, completeJob := env.addAndReserveJob(newStatusTestJobWithCwd(t, "nf.mywf.r1.prepare", "true"))
			So(completeClient.Execute(context.Background(), completeJob, "bash"), ShouldBeNil)
			So(completeClient.Disconnect(), ShouldBeNil)
			env.waitForRepGroupState("nf.mywf.r1.prepare", jobqueue.JobStateComplete)
			So(os.WriteFile(filepath.Join(completeJob.Cwd, nfStdoutFile), []byte("done\n"), 0o644), ShouldBeNil)

			output, err := env.executeStatus("--output", "--run-id", "r1")

			So(err, ShouldBeNil)
			So(output, ShouldContainSubstring, "Process")
			So(output, ShouldContainSubstring, "[prepare] done\n")
		})

		Convey("it prints captured stderr for buried jobs", func() {
			env := newNextflowCommandTestEnv(t)
			defer env.cleanup()

			buriedClient, buriedJob := env.addAndReserveJob(newStatusTestJobWithCwd(t, "nf.mywf.r1.prepare", "false"))
			So(buriedClient.Execute(context.Background(), buriedJob, "bash"), ShouldNotBeNil)
			So(buriedClient.Disconnect(), ShouldBeNil)
			env.waitForRepGroupState("nf.mywf.r1.prepare", jobqueue.JobStateBuried)
			So(os.WriteFile(filepath.Join(buriedJob.Cwd, nfStderrFile), []byte("failed\n"), 0o644), ShouldBeNil)

			output, err := env.executeStatus("--output", "--run-id", "r1")

			So(err, ShouldBeNil)
			So(output, ShouldContainSubstring, "[prepare] (stderr) failed\n")
		})

		Convey("it omits output for running jobs", func() {
			env := newNextflowCommandTestEnv(t)
			defer env.cleanup()

			runningClient, runningJob := env.addAndReserveJob(newStatusTestJobWithCwd(t, "nf.mywf.r1.prepare", "sleep 2"))
			errCh := make(chan error, 1)
			go func() {
				errCh <- runningClient.Execute(context.Background(), runningJob, "bash")
				_ = runningClient.Disconnect()
			}()

			path := filepath.Join(runningJob.Cwd, nfStdoutFile)
			writeErr := os.WriteFile(path, []byte("still running\n"), 0o644)
			So(writeErr, ShouldBeNil)
			env.waitForRepGroupState("nf.mywf.r1.prepare", jobqueue.JobStateRunning)

			output, err := env.executeStatus("--output", "--run-id", "r1")

			So(err, ShouldBeNil)
			So(output, ShouldNotContainSubstring, "still running")
			So(<-errCh, ShouldBeNil)
		})

		Convey("it prints multi-instance completed jobs with indexed labels in process order", func() {
			env := newNextflowCommandTestEnv(t)
			defer env.cleanup()

			job1Client, job1 := env.addAndReserveJob(newStatusTestJobWithCwd(t, "nf.mywf.r1.align", "echo one >/dev/null", "align", "1"))
			So(job1Client.Execute(context.Background(), job1, "bash"), ShouldBeNil)
			So(job1Client.Disconnect(), ShouldBeNil)
			job0Client, job0 := env.addAndReserveJob(newStatusTestJobWithCwd(t, "nf.mywf.r1.align", "echo zero >/dev/null", "align", "0"))
			So(job0Client.Execute(context.Background(), job0, "bash"), ShouldBeNil)
			So(job0Client.Disconnect(), ShouldBeNil)
			env.waitForRepGroupStateCount("nf.mywf.r1.align", jobqueue.JobStateComplete, 2)
			So(os.WriteFile(filepath.Join(job1.Cwd, nfStdoutFile), []byte("one\n"), 0o644), ShouldBeNil)
			So(os.WriteFile(filepath.Join(job0.Cwd, nfStdoutFile), []byte("zero\n"), 0o644), ShouldBeNil)

			output, err := env.executeStatus("--output", "--run-id", "r1")

			So(err, ShouldBeNil)
			So(strings.Index(output, "[align (0)] zero\n"), ShouldBeLessThan, strings.Index(output, "[align (1)] one\n"))
		})

		Convey("it prints only the count table when no terminal jobs have captured output", func() {
			env := newNextflowCommandTestEnv(t)
			defer env.cleanup()

			completeClient, completeJob := env.addAndReserveJob(newStatusTestJobWithCwd(t, "nf.mywf.r1.prepare", "true"))
			So(completeClient.Execute(context.Background(), completeJob, "bash"), ShouldBeNil)
			So(completeClient.Disconnect(), ShouldBeNil)
			env.waitForRepGroupState("nf.mywf.r1.prepare", jobqueue.JobStateComplete)

			output, err := env.executeStatus("--output", "--run-id", "r1")

			So(err, ShouldBeNil)
			So(strings.TrimSpace(output), ShouldEqual, "Process  Pending  Running  Complete  Buried  Total\nprepare  0        0        1         0       1")
		})

		Convey("it truncates large captured stdout output", func() {
			env := newNextflowCommandTestEnv(t)
			defer env.cleanup()

			payload := strings.Repeat("x", int(nfOutputMaxBytes)+128)
			completeClient, completeJob := env.addAndReserveJob(newStatusTestJobWithCwd(t, "nf.mywf.r1.prepare", "true"))
			So(completeClient.Execute(context.Background(), completeJob, "bash"), ShouldBeNil)
			So(completeClient.Disconnect(), ShouldBeNil)
			env.waitForRepGroupState("nf.mywf.r1.prepare", jobqueue.JobStateComplete)
			writeErr := os.WriteFile(filepath.Join(completeJob.Cwd, nfStdoutFile), []byte(payload), 0o644)
			So(writeErr, ShouldBeNil)

			output, err := env.executeStatus("--output", "--run-id", "r1")

			So(err, ShouldBeNil)
			So(output, ShouldContainSubstring, "[... output truncated ...]")
		})

		Convey("it hides internal lifecycle and maxErrors helper jobs from counts and output", func() {
			env := newNextflowCommandTestEnv(t)
			defer env.cleanup()

			completeClient, completeJob := env.addAndReserveJob(newStatusTestJobWithCwd(t, "nf.mywf.r1.align", "true", "align", "0"))
			So(completeClient.Execute(context.Background(), completeJob, "bash"), ShouldBeNil)
			So(completeClient.Disconnect(), ShouldBeNil)
			env.waitForRepGroupState("nf.mywf.r1.align", jobqueue.JobStateComplete)
			So(os.WriteFile(filepath.Join(completeJob.Cwd, nfStdoutFile), []byte("visible\n"), 0o644), ShouldBeNil)

			onComplete := newStatusTestJobWithCwd(t, "nf.mywf.r1.onComplete", "echo hidden-complete >/dev/null", "onComplete")
			onComplete.ReqGroup = "nf.workflow.onComplete"
			onCompleteClient, onCompleteJob := env.addAndReserveJob(onComplete)
			So(onCompleteClient.Execute(context.Background(), onCompleteJob, "bash"), ShouldBeNil)
			So(onCompleteClient.Disconnect(), ShouldBeNil)
			env.waitForRepGroupState("nf.mywf.r1.onComplete", jobqueue.JobStateComplete)
			So(os.WriteFile(filepath.Join(onCompleteJob.Cwd, nfStdoutFile), []byte("hidden-complete\n"), 0o644), ShouldBeNil)

			onError := newStatusTestJobWithCwd(t, "nf.mywf.r1.onError", "echo hidden-error >/dev/null", "onError")
			onError.ReqGroup = "nf.workflow.onError"
			onErrorClient, onErrorJob := env.addAndReserveJob(onError)
			So(onErrorClient.Execute(context.Background(), onErrorJob, "bash"), ShouldBeNil)
			So(onErrorClient.Disconnect(), ShouldBeNil)
			env.waitForRepGroupState("nf.mywf.r1.onError", jobqueue.JobStateComplete)
			So(os.WriteFile(filepath.Join(onErrorJob.Cwd, nfStdoutFile), []byte("hidden-error\n"), 0o644), ShouldBeNil)

			maxErrors := newStatusTestJobWithCwd(t, "nf.mywf.r1.align.maxErrors", "echo hidden-max-errors >/dev/null", "align", "maxErrors")
			maxErrors.ReqGroup = "nf.align.maxErrors"
			maxErrorsClient, maxErrorsJob := env.addAndReserveJob(maxErrors)
			So(maxErrorsClient.Execute(context.Background(), maxErrorsJob, "bash"), ShouldBeNil)
			So(maxErrorsClient.Disconnect(), ShouldBeNil)
			env.waitForRepGroupState("nf.mywf.r1.align.maxErrors", jobqueue.JobStateComplete)
			So(os.WriteFile(filepath.Join(maxErrorsJob.Cwd, nfStdoutFile), []byte("hidden-max-errors\n"), 0o644), ShouldBeNil)

			output, err := env.executeStatus("--output", "--run-id", "r1")

			So(err, ShouldBeNil)
			counts := statusCounts(output)
			So(counts, ShouldContainKey, "align")
			So(counts["align"], ShouldResemble, []string{"0", "0", "1", "0", "1"})
			So(counts, ShouldNotContainKey, "onComplete")
			So(counts, ShouldNotContainKey, "onError")
			So(counts, ShouldNotContainKey, "align.maxErrors")
			So(output, ShouldContainSubstring, "[align] visible\n")
			So(output, ShouldNotContainSubstring, "hidden-complete")
			So(output, ShouldNotContainSubstring, "hidden-error")
			So(output, ShouldNotContainSubstring, "hidden-max-errors")
		})
	})
}

func newStatusTestJob(repGroup, cmd string) *jobqueue.Job {
	return &jobqueue.Job{
		Cmd:          cmd,
		Cwd:          os.TempDir(),
		ReqGroup:     "nextflow-status",
		Requirements: &jqs.Requirements{RAM: 1, Time: time.Minute, Cores: 1, Other: make(map[string]string)},
		Retries:      uint8(0),
		Override:     uint8(2),
		RepGroup:     repGroup,
	}
}

func statusCounts(output string) map[string][]string {
	rows := make(map[string][]string)
	for _, line := range strings.Split(strings.TrimSpace(output), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 6 || fields[0] == "Process" {
			continue
		}

		rows[fields[0]] = fields[1:]
	}

	return rows
}

func newStatusTestJobWithCwd(t *testing.T, repGroup, cmd string, cwdParts ...string) *jobqueue.Job {
	t.Helper()

	cwd := t.TempDir()
	if len(cwdParts) > 0 {
		cwd = filepath.Join(append([]string{cwd}, cwdParts...)...)
		err := os.MkdirAll(cwd, 0o755)
		if err != nil {
			t.Fatalf("create cwd for %s: %v", repGroup, err)
		}
	}

	job := newStatusTestJob(repGroup, cmd)
	job.Cwd = cwd

	return job
}

func TestNextflowRunCommandPendingHelper(t *testing.T) {
	Convey("wr nextflow run schedules background pending progression when --follow is false", t, func() {
		env := newNextflowCommandTestEnv(t)
		defer env.cleanup()

		workflowPath := env.writeWorkflow("dynamic_pending_helper.nf", dynamicWorkflow("cat $reads > consumed.txt"))

		err := env.executeRun("--run-id", "r1", workflowPath)
		So(err, ShouldBeNil)

		workflowJobs := env.jobsByRepGroupSubstring("nf.dynamic_pending_helper.r1")
		So(workflowJobs, ShouldHaveLength, 1)
		So(workflowJobs[0].RepGroup, ShouldEqual, "nf.dynamic_pending_helper.r1.A")

		helperJobs := env.jobsByRepGroupSubstring(nextflowPendingHelperRepGroupPrefix("dynamic_pending_helper", "r1"))
		So(helperJobs, ShouldHaveLength, 1)
		So(helperJobs[0].RepGroup, ShouldEqual, nextflowPendingHelperRepGroup("dynamic_pending_helper", "r1"))
		So(helperJobs[0].ReqGroup, ShouldEqual, "nextflow-follow-helper")
		So(helperJobs[0].CwdMatters, ShouldBeTrue)
		So(helperJobs[0].Cwd, ShouldEqual, mustGetwd(t))
		So(helperJobs[0].Cmd, ShouldContainSubstring, "'wr' 'nextflow' 'run' '--follow'")
		So(helperJobs[0].Cmd, ShouldContainSubstring, "'--run-id' 'r1'")
		So(helperJobs[0].Cmd, ShouldContainSubstring, "'--poll-interval' '5s'")
		So(helperJobs[0].Cmd, ShouldContainSubstring, nextflowShellQuote(workflowPath))
	})

	Convey("pending helper preserves the stub-run flag when re-invoking wr nextflow run", t, func() {
		env := newNextflowCommandTestEnv(t)
		defer env.cleanup()

		workflowPath := env.writeWorkflow("dynamic_stub_helper.nf", dynamicWorkflow("cat $reads > consumed.txt"))

		err := env.executeRun("--run-id", "r1", "--stub-run", workflowPath)
		So(err, ShouldBeNil)

		helperJobs := env.jobsByRepGroupSubstring(nextflowPendingHelperRepGroupPrefix("dynamic_stub_helper", "r1"))
		So(helperJobs, ShouldHaveLength, 1)
		So(helperJobs[0].Cmd, ShouldContainSubstring, "'--stub-run'")
	})
}

type fakeNextflowSnapshot struct {
	complete   []*jobqueue.Job
	buried     []*jobqueue.Job
	incomplete []*jobqueue.Job
}

func TestNextflowRunCommandFollow(t *testing.T) {
	Convey("wr nextflow run --follow covers E2", t, func() {
		Convey("remote hello workflows with indented triple-quoted scripts complete instead of burying jobs", func() {
			homeDir := t.TempDir()
			t.Setenv("HOME", homeDir)
			cloneCalls, restoreGit := stubGitHubWorkflowDownload(t, homeDir, "nextflow-io", "hello")
			defer restoreGit()

			env := newNextflowCommandTestEnv(t)
			defer env.cleanup()

			workerDone := runReservedJobsInBackground(env, 4)
			err := env.executeRun("--run-id", "followrun", "--follow", "--poll-interval", "20ms", "nextflow-io/hello")
			So(err, ShouldBeNil)
			So(<-workerDone, ShouldBeNil)
			So(*cloneCalls, ShouldEqual, 1)

			repGroupPrefix := nextflowRepGroupPrefix("nextflow-io/hello", "followrun")
			jobs := env.jobsByRepGroupSubstring(repGroupPrefix)
			So(jobs, ShouldHaveLength, 4)
			outputs := make([]string, 0, len(jobs))
			for _, job := range jobs {
				So(job.State, ShouldEqual, jobqueue.JobStateComplete)

				stdout, readErr := os.ReadFile(filepath.Join(job.Cwd, nfStdoutFile))
				So(readErr, ShouldBeNil)
				outputs = append(outputs, string(stdout))
			}
			sort.Strings(outputs)
			So(outputs, ShouldResemble, []string{
				"Bonjour world!\n",
				"Ciao world!\n",
				"Hello world!\n",
				"Hola world!\n",
			})

			jq := env.connectClient()
			defer func() {
				So(jq.Disconnect(), ShouldBeNil)
			}()

			buriedJobs, buriedErr := jq.GetByRepGroup(repGroupPrefix, true, 0, jobqueue.JobStateBuried, false, false)
			So(buriedErr, ShouldBeNil)
			So(buriedJobs, ShouldBeEmpty)
		})

		Convey("the command path follows dynamic workflows using the requested poll interval", func() {
			env := newNextflowCommandTestEnv(t)
			defer env.cleanup()

			workflowPath := env.writeWorkflow("dynamic.nf", "process A {\noutput: path 'produced.txt'\nscript: 'echo hello > produced.txt'\n}\nprocess B {\ninput: path reads\nscript: 'cat $reads > consumed.txt'\n}\nworkflow { A(); B(A.out) }\n")

			workerDone := make(chan error, 1)
			go func() {
				jq := env.connectClient()
				defer func() {
					_ = jq.Disconnect()
				}()

				for range 2 {
					job, err := jq.Reserve(5 * time.Second)
					if err != nil {
						workerDone <- err
						return
					}
					if job == nil {
						workerDone <- os.ErrDeadlineExceeded
						return
					}

					if err := jq.Execute(context.Background(), job, "bash"); err != nil {
						workerDone <- err
						return
					}
				}

				workerDone <- nil
			}()

			sleeps := []time.Duration{}
			previousSleep := nextflowSleep
			nextflowSleep = func(delay time.Duration) {
				sleeps = append(sleeps, delay)
				time.Sleep(10 * time.Millisecond)
			}
			defer func() {
				nextflowSleep = previousSleep
			}()

			err := env.executeRun("--run-id", "followrun", "--follow", "--poll-interval", "20ms", workflowPath)
			So(err, ShouldBeNil)
			So(<-workerDone, ShouldBeNil)

			env.waitForRepGroupState("nf.dynamic.followrun.A", jobqueue.JobStateComplete)
			env.waitForRepGroupState("nf.dynamic.followrun.B", jobqueue.JobStateComplete)
			So(sleeps, ShouldNotBeEmpty)
			for _, sleep := range sleeps {
				So(sleep, ShouldEqual, 20*time.Millisecond)
			}
		})

		Convey("completed upstream jobs are translated into concrete downstream jobs and the workflow exits 0 once all jobs finish", func() {
			queue, pending, tc, producer := newNextflowFollowScenario(t, "dynamic", "cat $reads > consumed.txt")
			queue.completeAfterAdd = true
			queue.snapshots = []fakeNextflowSnapshot{
				{incomplete: []*jobqueue.Job{producer}},
				{complete: []*jobqueue.Job{producer}},
				{complete: []*jobqueue.Job{producer}},
			}

			sleeps, restoreSleep := withFakeNextflowSleep(queue)
			defer restoreSleep()
			err := followNextflowWorkflow(queue, nextflowRepGroupPrefix(tc.WorkflowName, tc.RunID), pending, tc, 100*time.Millisecond, io.Discard)

			So(err, ShouldBeNil)
			So(queue.added, ShouldHaveLength, 1)
			So(queue.added[0], ShouldHaveLength, 1)
			So(queue.added[0][0].Cmd, ShouldContainSubstring, filepath.Join(producer.Cwd, "produced.txt"))
			So(*sleeps, ShouldResemble, []time.Duration{100 * time.Millisecond, 100 * time.Millisecond, 100 * time.Millisecond})
		})

		Convey("buried downstream jobs make follow exit non-zero after the workflow reaches terminal state", func() {
			queue, pending, tc, producer := newNextflowFollowScenario(t, "dynamic_fail", "cat $reads && exit 1")
			queue.buryAfterAdd = true
			queue.snapshots = []fakeNextflowSnapshot{
				{incomplete: []*jobqueue.Job{producer}},
				{complete: []*jobqueue.Job{producer}},
				{complete: []*jobqueue.Job{producer}},
			}

			_, restoreSleep := withFakeNextflowSleep(queue)
			defer restoreSleep()
			err := followNextflowWorkflow(queue, nextflowRepGroupPrefix(tc.WorkflowName, tc.RunID), pending, tc, 100*time.Millisecond, io.Discard)

			So(err, ShouldNotBeNil)
			So(err.Error(), ShouldContainSubstring, "buried")
			So(queue.added, ShouldHaveLength, 1)
		})

		Convey("custom poll intervals are used instead of the 5s default", func() {
			queue, pending, tc, producer := newNextflowFollowScenario(t, "polled", "cat $reads > consumed.txt")
			queue.completeAfterAdd = true
			queue.snapshots = []fakeNextflowSnapshot{
				{incomplete: []*jobqueue.Job{producer}},
				{complete: []*jobqueue.Job{producer}},
				{complete: []*jobqueue.Job{producer}},
			}

			sleeps, restoreSleep := withFakeNextflowSleep(queue)
			defer restoreSleep()
			err := followNextflowWorkflow(queue, nextflowRepGroupPrefix(tc.WorkflowName, tc.RunID), pending, tc, time.Second, io.Discard)

			So(err, ShouldBeNil)
			So(*sleeps, ShouldResemble, []time.Duration{time.Second, time.Second, time.Second})
		})

		Convey("a transient empty snapshot is retried before unresolved pending stages are reported", func() {
			queue, pending, tc, producer := newNextflowFollowScenario(t, "transient_empty", "cat $reads > consumed.txt")
			queue.completeAfterAdd = true
			queue.snapshots = []fakeNextflowSnapshot{
				{incomplete: []*jobqueue.Job{producer}},
				{},
				{complete: []*jobqueue.Job{producer}},
				{complete: []*jobqueue.Job{producer}},
			}

			sleeps, restoreSleep := withFakeNextflowSleep(queue)
			defer restoreSleep()
			err := followNextflowWorkflow(queue, nextflowRepGroupPrefix(tc.WorkflowName, tc.RunID), pending, tc, 75*time.Millisecond, io.Discard)

			So(err, ShouldBeNil)
			So(queue.added, ShouldHaveLength, 1)
			So(*sleeps, ShouldResemble, []time.Duration{75 * time.Millisecond, 75 * time.Millisecond, 75 * time.Millisecond, 75 * time.Millisecond})
		})

		Convey("buried finish-strategy jobs zero their limit group", func() {
			finishJob := &jobqueue.Job{
				RepGroup:    "nf.wf.r1.A",
				LimitGroups: []string{"nf-finish-A-r1"},
				State:       jobqueue.JobStateBuried,
			}
			queue := &fakeNextflowQueue{snapshots: []fakeNextflowSnapshot{{buried: []*jobqueue.Job{finishJob}}}}

			_, restoreSleep := withFakeNextflowSleep(queue)
			defer restoreSleep()

			err := followNextflowWorkflow(queue, "nf.wf.r1.", nil, nextflowdsl.TranslateConfig{RunID: "r1", WorkflowName: "wf"}, 25*time.Millisecond, io.Discard)

			So(err, ShouldNotBeNil)
			So(err.Error(), ShouldContainSubstring, "buried")
			So(queue.limitGroupCalls, ShouldResemble, []string{"nf-finish-A-r1:0"})
		})

		Convey("buried finish-strategy jobs do not stop other processes from continuing", func() {
			finishJob := &jobqueue.Job{
				RepGroup:    "nf.wf.r1.A",
				LimitGroups: []string{"nf-finish-A-r1"},
				State:       jobqueue.JobStateBuried,
			}
			otherRunning := &jobqueue.Job{RepGroup: "nf.wf.r1.B", State: jobqueue.JobStateRunning}
			otherComplete := &jobqueue.Job{RepGroup: "nf.wf.r1.B", State: jobqueue.JobStateComplete}
			queue := &fakeNextflowQueue{snapshots: []fakeNextflowSnapshot{
				{buried: []*jobqueue.Job{finishJob}, incomplete: []*jobqueue.Job{otherRunning}},
				{buried: []*jobqueue.Job{finishJob}, complete: []*jobqueue.Job{otherComplete}},
			}}

			sleeps, restoreSleep := withFakeNextflowSleep(queue)
			defer restoreSleep()

			err := followNextflowWorkflow(queue, "nf.wf.r1.", nil, nextflowdsl.TranslateConfig{RunID: "r1", WorkflowName: "wf"}, 25*time.Millisecond, io.Discard)

			So(err, ShouldNotBeNil)
			So(err.Error(), ShouldContainSubstring, "buried")
			So(queue.limitGroupCalls, ShouldResemble, []string{"nf-finish-A-r1:0"})
			So(*sleeps, ShouldResemble, []time.Duration{25 * time.Millisecond})
		})
	})
}

func TestNextflowRunCommandResumeMissing(t *testing.T) {
	Convey("resume skips completed upstream work and adds missing downstream jobs", t, func() {
		env := newNextflowCommandTestEnv(t)
		defer env.cleanup()

		workflowPath := env.writeWorkflow("resume_missing.nf", dynamicWorkflow("cat $reads > consumed.txt"))

		So(env.executeRun("--run-id", "r1", workflowPath), ShouldBeNil)
		So(executeReservedJobs(env, 1), ShouldBeNil)
		env.waitForRepGroupState("nf.resume_missing.r1.A", jobqueue.JobStateComplete)

		workerDone := runReservedJobsInBackground(env, 1)
		So(env.executeRun("--run-id", "r1", "--follow", "--poll-interval", "20ms", workflowPath), ShouldBeNil)
		So(<-workerDone, ShouldBeNil)

		jobs := env.jobsByRepGroupSubstring("nf.resume_missing.r1")
		So(jobs, ShouldHaveLength, 2)
		env.waitForRepGroupState("nf.resume_missing.r1.B", jobqueue.JobStateComplete)
	})
}

func TestNextflowRunCommandResumeComplete(t *testing.T) {
	Convey("resume exits successfully without re-adding jobs when the whole workflow is already complete", t, func() {
		env := newNextflowCommandTestEnv(t)
		defer env.cleanup()

		workflowPath := env.writeWorkflow("resume_complete.nf", dynamicWorkflow("cat $reads > consumed.txt"))

		workerDone := runReservedJobsInBackground(env, 2)
		So(env.executeRun("--run-id", "r1", "--follow", "--poll-interval", "20ms", workflowPath), ShouldBeNil)
		So(<-workerDone, ShouldBeNil)
		env.waitForRepGroupState("nf.resume_complete.r1.B", jobqueue.JobStateComplete)

		jobsBefore := env.jobsByRepGroupSubstring("nf.resume_complete.r1")
		So(jobsBefore, ShouldHaveLength, 2)

		So(env.executeRun("--run-id", "r1", "--follow", "--poll-interval", "20ms", workflowPath), ShouldBeNil)

		jobsAfter := env.jobsByRepGroupSubstring("nf.resume_complete.r1")
		So(jobsAfter, ShouldHaveLength, 2)
	})
}

func TestNextflowRunCommandResumeBuried(t *testing.T) {
	Convey("resume deletes buried jobs and re-adds them", t, func() {
		env := newNextflowCommandTestEnv(t)
		defer env.cleanup()

		workflowPath := env.writeWorkflow("resume_buried.nf", dynamicWorkflow("cat $reads && false"))

		So(env.executeRun("--run-id", "r1", workflowPath), ShouldBeNil)
		So(executeReservedJobs(env, 1), ShouldBeNil)
		env.waitForRepGroupState("nf.resume_buried.r1.A", jobqueue.JobStateComplete)

		buriedWorker := runReservedJobsInBackground(env, 1)
		err := env.executeRun("--run-id", "r1", "--follow", "--poll-interval", "20ms", workflowPath)
		So(err, ShouldNotBeNil)
		So(<-buriedWorker, ShouldNotBeNil)
		env.waitForRepGroupState("nf.resume_buried.r1.B", jobqueue.JobStateBuried)

		workflowPath = env.writeWorkflow("resume_buried.nf", dynamicWorkflow("cat $reads > consumed.txt"))
		resumeWorker := runReservedJobsInBackground(env, 1)
		So(env.executeRun("--run-id", "r1", "--follow", "--poll-interval", "20ms", workflowPath), ShouldBeNil)
		So(<-resumeWorker, ShouldBeNil)

		env.waitForRepGroupState("nf.resume_buried.r1.B", jobqueue.JobStateComplete)
		jq := env.connectClient()
		buriedJobs, err := jq.GetByRepGroup("nf.resume_buried.r1.B", false, 0, jobqueue.JobStateBuried, false, false)
		So(err, ShouldBeNil)
		So(jq.Disconnect(), ShouldBeNil)
		So(buriedJobs, ShouldBeEmpty)
	})
}

func TestNextflowRunCommandResumeDeleted(t *testing.T) {
	Convey("resume re-adds deleted downstream jobs while skipping completed upstream work", t, func() {
		env := newNextflowCommandTestEnv(t)
		defer env.cleanup()

		workflowPath := env.writeWorkflow("resume_deleted.nf", dynamicWorkflow("cat $reads > consumed.txt"))

		So(env.executeRun("--run-id", "r1", workflowPath), ShouldBeNil)
		So(executeReservedJobs(env, 1), ShouldBeNil)
		producer := env.waitForRepGroupState("nf.resume_deleted.r1.A", jobqueue.JobStateComplete)

		pendingJobs, err := translatedPendingJobs(t, workflowPath, "r1", producer)
		So(err, ShouldBeNil)
		So(pendingJobs, ShouldHaveLength, 1)
		env.addJob(pendingJobs[0])

		jq := env.connectClient()
		deleted, err := jq.Delete([]*jobqueue.JobEssence{{JobKey: pendingJobs[0].Key()}})
		So(err, ShouldBeNil)
		So(deleted, ShouldEqual, 1)
		So(jq.Disconnect(), ShouldBeNil)

		workerDone := runReservedJobsInBackground(env, 1)
		So(env.executeRun("--run-id", "r1", "--follow", "--poll-interval", "20ms", workflowPath), ShouldBeNil)
		So(<-workerDone, ShouldBeNil)

		jobs := env.jobsByRepGroupSubstring("nf.resume_deleted.r1")
		So(jobs, ShouldHaveLength, 2)
		env.waitForRepGroupState("nf.resume_deleted.r1.B", jobqueue.JobStateComplete)
	})
}

func TestFollowNextflowWorkflowOutput(t *testing.T) {
	Convey("followNextflowWorkflow covers B1", t, func() {
		const repGroupPrefix = "nf.wf.r1."

		newJob := func(tempDir, process, cwdSuffix string, state jobqueue.JobState) *jobqueue.Job {
			cwd := filepath.Join(tempDir, cwdSuffix)
			err := os.MkdirAll(cwd, 0o755)
			So(err, ShouldBeNil)

			return &jobqueue.Job{
				Cmd:      "echo " + filepath.ToSlash(cwdSuffix),
				RepGroup: repGroupPrefix + process,
				Cwd:      cwd,
				State:    state,
			}
		}

		writeOutput := func(job *jobqueue.Job, name, content string) {
			err := os.WriteFile(filepath.Join(job.Cwd, name), []byte(content), 0o644)
			So(err, ShouldBeNil)
		}

		runFollow := func(queue *fakeNextflowQueue, writer io.Writer) error {
			tc := nextflowdsl.TranslateConfig{RunID: "r1", WorkflowName: "wf"}
			sleeps, restoreSleep := withFakeNextflowSleep(queue)
			defer restoreSleep()

			err := followNextflowWorkflow(queue, repGroupPrefix, nil, tc, 25*time.Millisecond, writer)
			So(*sleeps, ShouldNotBeNil)

			return err
		}

		Convey("single-instance completed jobs print captured stdout", func() {
			tempDir := t.TempDir()
			job := newJob(tempDir, "sayHello", "sayHello", jobqueue.JobStateComplete)
			writeOutput(job, nfStdoutFile, "Hello world!\n")

			queue := &fakeNextflowQueue{snapshots: []fakeNextflowSnapshot{
				{incomplete: []*jobqueue.Job{cloneJobs([]*jobqueue.Job{job})[0]}},
				{complete: []*jobqueue.Job{job}},
				{complete: []*jobqueue.Job{job}},
			}}

			var out bytes.Buffer
			err := runFollow(queue, &out)

			So(err, ShouldBeNil)
			So(out.String(), ShouldEqual, "[sayHello] Hello world!\n")
		})

		Convey("multi-line stdout is indented under its own header", func() {
			tempDir := t.TempDir()
			job := newJob(tempDir, "sayHello", "sayHello", jobqueue.JobStateComplete)
			writeOutput(job, nfStdoutFile, "line1\nline2\n")

			queue := &fakeNextflowQueue{snapshots: []fakeNextflowSnapshot{
				{incomplete: []*jobqueue.Job{cloneJobs([]*jobqueue.Job{job})[0]}},
				{complete: []*jobqueue.Job{job}},
				{complete: []*jobqueue.Job{job}},
			}}

			var out bytes.Buffer
			err := runFollow(queue, &out)

			So(err, ShouldBeNil)
			So(out.String(), ShouldEqual, "[sayHello]\n  line1\n  line2\n")
		})

		Convey("jobs with no captured files print nothing", func() {
			tempDir := t.TempDir()
			job := newJob(tempDir, "sayHello", "sayHello", jobqueue.JobStateComplete)

			queue := &fakeNextflowQueue{snapshots: []fakeNextflowSnapshot{
				{incomplete: []*jobqueue.Job{cloneJobs([]*jobqueue.Job{job})[0]}},
				{complete: []*jobqueue.Job{job}},
				{complete: []*jobqueue.Job{job}},
			}}

			var out bytes.Buffer
			err := runFollow(queue, &out)

			So(err, ShouldBeNil)
			So(out.String(), ShouldEqual, "")
		})

		Convey("whitespace-only stdout is ignored", func() {
			tempDir := t.TempDir()
			job := newJob(tempDir, "sayHello", "sayHello", jobqueue.JobStateComplete)
			writeOutput(job, nfStdoutFile, "   \n")

			queue := &fakeNextflowQueue{snapshots: []fakeNextflowSnapshot{
				{incomplete: []*jobqueue.Job{cloneJobs([]*jobqueue.Job{job})[0]}},
				{complete: []*jobqueue.Job{job}},
				{complete: []*jobqueue.Job{job}},
			}}

			var out bytes.Buffer
			err := runFollow(queue, &out)

			So(err, ShouldBeNil)
			So(out.String(), ShouldEqual, "")
		})

		Convey("buried jobs still display captured output before follow fails", func() {
			tempDir := t.TempDir()
			job := newJob(tempDir, "sayHello", "sayHello", jobqueue.JobStateBuried)
			writeOutput(job, nfStdoutFile, "buried output\n")

			queue := &fakeNextflowQueue{snapshots: []fakeNextflowSnapshot{
				{incomplete: []*jobqueue.Job{cloneJobs([]*jobqueue.Job{job})[0]}},
				{buried: []*jobqueue.Job{job}},
			}}

			var out bytes.Buffer
			err := runFollow(queue, &out)

			So(err, ShouldNotBeNil)
			So(err.Error(), ShouldContainSubstring, "buried")
			So(out.String(), ShouldEqual, "[sayHello] buried output\n")
		})

		Convey("multi-instance labels use all known jobs across poll iterations", func() {
			tempDir := t.TempDir()
			job0 := newJob(tempDir, "sayHello", filepath.Join("sayHello", "0"), jobqueue.JobStateComplete)
			job1 := newJob(tempDir, "sayHello", filepath.Join("sayHello", "1"), jobqueue.JobStateComplete)
			writeOutput(job0, nfStdoutFile, "zero\n")
			writeOutput(job1, nfStdoutFile, "one\n")

			queue := &fakeNextflowQueue{snapshots: []fakeNextflowSnapshot{
				{incomplete: []*jobqueue.Job{cloneJobs([]*jobqueue.Job{job0, job1})[0], cloneJobs([]*jobqueue.Job{job0, job1})[1]}},
				{complete: []*jobqueue.Job{job0}, incomplete: []*jobqueue.Job{cloneJobs([]*jobqueue.Job{job1})[0]}},
				{complete: []*jobqueue.Job{job0, job1}},
				{complete: []*jobqueue.Job{job0, job1}},
			}}

			var out bytes.Buffer
			err := runFollow(queue, &out)

			So(err, ShouldBeNil)
			So(out.String(), ShouldEqual, "[sayHello (0)] zero\n[sayHello (1)] one\n")
		})

		Convey("stderr is printed after stdout with a stderr-specific label", func() {
			tempDir := t.TempDir()
			job := newJob(tempDir, "processName", "processName", jobqueue.JobStateComplete)
			writeOutput(job, nfStdoutFile, "out\n")
			writeOutput(job, nfStderrFile, "error msg\n")

			queue := &fakeNextflowQueue{snapshots: []fakeNextflowSnapshot{
				{incomplete: []*jobqueue.Job{cloneJobs([]*jobqueue.Job{job})[0]}},
				{complete: []*jobqueue.Job{job}},
				{complete: []*jobqueue.Job{job}},
			}}

			var out bytes.Buffer
			err := runFollow(queue, &out)

			So(err, ShouldBeNil)
			So(out.String(), ShouldEqual, "[processName] out\n[processName] (stderr) error msg\n")
		})

		Convey("stdout larger than 1 MB is truncated with the marker", func() {
			tempDir := t.TempDir()
			job := newJob(tempDir, "sayHello", "sayHello", jobqueue.JobStateComplete)
			writeOutput(job, nfStdoutFile, strings.Repeat("x", int(nfOutputMaxBytes)+128))

			queue := &fakeNextflowQueue{snapshots: []fakeNextflowSnapshot{
				{incomplete: []*jobqueue.Job{cloneJobs([]*jobqueue.Job{job})[0]}},
				{complete: []*jobqueue.Job{job}},
				{complete: []*jobqueue.Job{job}},
			}}

			var out bytes.Buffer
			err := runFollow(queue, &out)

			So(err, ShouldBeNil)
			So(out.String(), ShouldContainSubstring, "[... output truncated ...]")
		})
	})
}

func TestNextflowResumeJobs(t *testing.T) {
	Convey("resume reconciliation handles buried, deleted, and already-existing jobs", t, func() {
		job := &jobqueue.Job{Cmd: "echo hello", RepGroup: "nf.resume.r1.A"}

		Convey("complete jobs are skipped", func() {
			queue := &fakeNextflowQueue{snapshots: []fakeNextflowSnapshot{{complete: []*jobqueue.Job{{Cmd: "echo old", RepGroup: job.RepGroup, State: jobqueue.JobStateComplete}}}}}

			jobsToAdd, err := nextflowResumeJobs(queue, "nf.resume.r1.", []*jobqueue.Job{job})
			So(err, ShouldBeNil)
			So(jobsToAdd, ShouldBeEmpty)
		})

		Convey("buried jobs are deleted and returned for re-add", func() {
			buriedJob := &jobqueue.Job{Cmd: "echo old", RepGroup: job.RepGroup, State: jobqueue.JobStateBuried}
			queue := &fakeNextflowQueue{snapshots: []fakeNextflowSnapshot{{buried: []*jobqueue.Job{buriedJob}}}}

			jobsToAdd, err := nextflowResumeJobs(queue, "nf.resume.r1.", []*jobqueue.Job{job})
			So(err, ShouldBeNil)
			So(jobsToAdd, ShouldResemble, []*jobqueue.Job{job})
			So(queue.deletedKeys, ShouldContain, buriedJob.Key())
		})

		Convey("missing jobs are re-added, which also covers deleted jobs absent from queue queries", func() {
			queue := &fakeNextflowQueue{snapshots: []fakeNextflowSnapshot{{}}}

			jobsToAdd, err := nextflowResumeJobs(queue, "nf.resume.r1.", []*jobqueue.Job{job})
			So(err, ShouldBeNil)
			So(jobsToAdd, ShouldResemble, []*jobqueue.Job{job})
		})

		Convey("running jobs are skipped", func() {
			queue := &fakeNextflowQueue{snapshots: []fakeNextflowSnapshot{{incomplete: []*jobqueue.Job{{Cmd: "echo old", RepGroup: job.RepGroup, State: jobqueue.JobStateRunning}}}}}

			jobsToAdd, err := nextflowResumeJobs(queue, "nf.resume.r1.", []*jobqueue.Job{job})
			So(err, ShouldBeNil)
			So(jobsToAdd, ShouldBeEmpty)
		})

		Convey("dependent jobs are skipped", func() {
			queue := &fakeNextflowQueue{snapshots: []fakeNextflowSnapshot{{incomplete: []*jobqueue.Job{{Cmd: "echo old", RepGroup: job.RepGroup, State: jobqueue.JobStateDependent}}}}}

			jobsToAdd, err := nextflowResumeJobs(queue, "nf.resume.r1.", []*jobqueue.Job{job})
			So(err, ShouldBeNil)
			So(jobsToAdd, ShouldBeEmpty)
		})

		Convey("lost jobs are re-added", func() {
			queue := &fakeNextflowQueue{snapshots: []fakeNextflowSnapshot{{incomplete: []*jobqueue.Job{{Cmd: "echo old", RepGroup: job.RepGroup, State: jobqueue.JobStateLost}}}}}

			jobsToAdd, err := nextflowResumeJobs(queue, "nf.resume.r1.", []*jobqueue.Job{job})
			So(err, ShouldBeNil)
			So(jobsToAdd, ShouldResemble, []*jobqueue.Job{job})
		})

		Convey("jobs with the same rep group but a different key are treated as missing", func() {
			planned := &jobqueue.Job{
				Cmd:        "echo hello",
				RepGroup:   job.RepGroup,
				Cwd:        "/tmp/nf-work/r1/A/2",
				CwdMatters: true,
			}
			queue := &fakeNextflowQueue{snapshots: []fakeNextflowSnapshot{{complete: []*jobqueue.Job{
				{Cmd: "echo hello", RepGroup: job.RepGroup, State: jobqueue.JobStateComplete, Cwd: "/tmp/nf-work/r1/A/0", CwdMatters: true},
				{Cmd: "echo hello", RepGroup: job.RepGroup, State: jobqueue.JobStateComplete, Cwd: "/tmp/nf-work/r1/A/1", CwdMatters: true},
			}}}}

			jobsToAdd, err := nextflowResumeJobs(queue, "nf.resume.r1.", []*jobqueue.Job{planned})
			So(err, ShouldBeNil)
			So(jobsToAdd, ShouldResemble, []*jobqueue.Job{planned})
		})

		Convey("a single indexed job with a different key is treated as missing", func() {
			planned := &jobqueue.Job{
				Cmd:        "echo hello",
				RepGroup:   job.RepGroup,
				Cwd:        "/tmp/nf-work/r1/A/1",
				CwdMatters: true,
			}
			queue := &fakeNextflowQueue{snapshots: []fakeNextflowSnapshot{{complete: []*jobqueue.Job{{
				Cmd:        "echo hello",
				RepGroup:   job.RepGroup,
				State:      jobqueue.JobStateComplete,
				Cwd:        "/tmp/nf-work/r1/A/0",
				CwdMatters: true,
			}}}}}

			jobsToAdd, err := nextflowResumeJobs(queue, "nf.resume.r1.", []*jobqueue.Job{planned})
			So(err, ShouldBeNil)
			So(jobsToAdd, ShouldResemble, []*jobqueue.Job{planned})
		})
	})
}

func dynamicWorkflow(consumerScript string) string {
	return "process A {\noutput: path 'produced.txt'\nscript: 'echo hello > produced.txt'\n}\n" +
		"process B {\ninput: path reads\nscript: '" + consumerScript + "'\n}\n" +
		"workflow { A(); B(A.out) }\n"
}

func executeReservedJobs(env *nextflowCommandTestEnv, count int) error {
	jq := env.connectClient()
	defer func() {
		_ = jq.Disconnect()
	}()

	for range count {
		job, err := jq.Reserve(5 * time.Second)
		if err != nil {
			return err
		}
		if job == nil {
			return os.ErrDeadlineExceeded
		}

		if err = jq.Execute(context.Background(), job, "bash"); err != nil {
			return err
		}
	}

	return nil
}

func runReservedJobsInBackground(env *nextflowCommandTestEnv, count int) <-chan error {
	result := make(chan error, 1)
	go func() {
		result <- executeReservedJobs(env, count)
	}()

	return result
}

func translatedPendingJobs(t *testing.T, workflowPath, runID string, completed *jobqueue.Job) ([]*jobqueue.Job, error) {
	t.Helper()

	workflowFile, err := os.Open(workflowPath)
	if err != nil {
		return nil, err
	}
	defer func() {
		_ = workflowFile.Close()
	}()

	wf, err := nextflowdsl.Parse(workflowFile)
	if err != nil {
		return nil, err
	}

	tc := nextflowdsl.TranslateConfig{
		RunID:        runID,
		WorkflowName: nextflowWorkflowName(workflowPath),
		WorkflowPath: workflowPath,
		Cwd:          mustGetwd(t),
	}

	result, err := nextflowdsl.Translate(wf, nil, tc)
	if err != nil {
		return nil, err
	}
	if len(result.Pending) != 1 {
		return nil, os.ErrInvalid
	}

	completedJobs, ready, err := nextflowdsl.CompletedJobsForPending(result.Pending[0], []*jobqueue.Job{completed}, nil)
	if err != nil {
		return nil, err
	}
	if !ready {
		return nil, os.ErrNotExist
	}

	return nextflowdsl.TranslatePending(result.Pending[0], completedJobs, tc)
}

func newNextflowFollowScenario(t *testing.T, workflowName, consumerScript string) (*fakeNextflowQueue, []*nextflowdsl.PendingStage, nextflowdsl.TranslateConfig, *jobqueue.Job) {
	t.Helper()

	baseDir := t.TempDir()
	wf := &nextflowdsl.Workflow{
		Processes: []*nextflowdsl.Process{
			{
				Name:       "A",
				Directives: map[string]any{},
				Script:     "echo hello > produced.txt",
				Output: []*nextflowdsl.Declaration{{
					Kind: "path",
					Expr: nextflowdsl.StringExpr{Value: "produced.txt"},
				}},
				Env:        map[string]string{},
				PublishDir: []*nextflowdsl.PublishDir{},
			},
			{
				Name:       "B",
				Directives: map[string]any{},
				Input:      []*nextflowdsl.Declaration{{Kind: "path", Name: "reads"}},
				Script:     consumerScript,
				Env:        map[string]string{},
				PublishDir: []*nextflowdsl.PublishDir{},
			},
		},
		EntryWF: &nextflowdsl.WorkflowBlock{Calls: []*nextflowdsl.Call{{Target: "A"}, {Target: "B", Args: []nextflowdsl.ChanExpr{nextflowdsl.ChanRef{Name: "A.out"}}}}},
	}

	tc := nextflowdsl.TranslateConfig{RunID: "r1", WorkflowName: workflowName, Cwd: baseDir}
	result, err := nextflowdsl.Translate(wf, nil, tc)
	if err != nil {
		t.Fatalf("translate follow scenario: %v", err)
	}
	if len(result.Jobs) != 1 || len(result.Pending) != 1 {
		t.Fatalf("unexpected follow scenario shape: jobs=%d pending=%d", len(result.Jobs), len(result.Pending))
	}

	producer := cloneJobs(result.Jobs)[0]
	producer.State = jobqueue.JobStateComplete
	if err = os.MkdirAll(producer.Cwd, 0o755); err != nil {
		t.Fatalf("create producer cwd: %v", err)
	}
	if err = os.WriteFile(filepath.Join(producer.Cwd, "produced.txt"), []byte("hello"), 0o644); err != nil {
		t.Fatalf("write producer output: %v", err)
	}

	queue := &fakeNextflowQueue{}

	return queue, result.Pending, tc, producer
}

func cloneJobs(jobs []*jobqueue.Job) []*jobqueue.Job {
	cloned := make([]*jobqueue.Job, 0, len(jobs))
	for _, job := range jobs {
		if job == nil {
			continue
		}
		cloned = append(cloned, &jobqueue.Job{
			Cmd:             job.Cmd,
			Cwd:             job.Cwd,
			CwdMatters:      job.CwdMatters,
			LimitGroups:     append([]string{}, job.LimitGroups...),
			RepGroup:        job.RepGroup,
			ReqGroup:        job.ReqGroup,
			Requirements:    job.Requirements,
			Override:        job.Override,
			Retries:         job.Retries,
			DepGroups:       append([]string{}, job.DepGroups...),
			Dependencies:    job.Dependencies,
			WithDocker:      job.WithDocker,
			WithSingularity: job.WithSingularity,
			State:           job.State,
			Exitcode:        job.Exitcode,
		})
	}

	return cloned
}

func withFakeNextflowSleep(queue *fakeNextflowQueue) (*[]time.Duration, func()) {
	sleeps := []time.Duration{}
	previousSleep := nextflowSleep
	nextflowSleep = func(delay time.Duration) {
		sleeps = append(sleeps, delay)
		queue.advance()
	}

	return &sleeps, func() {
		nextflowSleep = previousSleep
	}
}

type fakeNextflowQueue struct {
	snapshots         []fakeNextflowSnapshot
	index             int
	added             [][]*jobqueue.Job
	addedCompleteJobs []*jobqueue.Job
	addedBuriedJobs   []*jobqueue.Job
	completeAfterAdd  bool
	buryAfterAdd      bool
	deletedKeys       []string
	limitGroupCalls   []string
	limitGroups       map[string]int
}

func (f *fakeNextflowQueue) Add(jobs []*jobqueue.Job, _ []string, _ bool) (int, int, error) {
	clonedJobs := cloneJobs(jobs)
	f.added = append(f.added, clonedJobs)
	f.addedCompleteJobs = cloneJobs(clonedJobs)
	f.addedBuriedJobs = cloneJobs(clonedJobs)
	for _, job := range f.addedCompleteJobs {
		job.State = jobqueue.JobStateComplete
	}
	for _, job := range f.addedBuriedJobs {
		job.State = jobqueue.JobStateBuried
	}
	if len(f.snapshots) > 0 {
		f.snapshots[f.index].incomplete = append(cloneJobs(f.snapshots[f.index].incomplete), clonedJobs...)
	}
	if f.index+1 < len(f.snapshots) {
		if f.completeAfterAdd {
			f.snapshots[f.index+1].complete = append(cloneJobs(f.snapshots[f.index+1].complete), cloneJobs(f.addedCompleteJobs)...)
		}
		if f.buryAfterAdd {
			f.snapshots[f.index+1].buried = append(cloneJobs(f.snapshots[f.index+1].buried), cloneJobs(f.addedBuriedJobs)...)
		}
	}

	return len(jobs), 0, nil
}

func (f *fakeNextflowQueue) Delete(jes []*jobqueue.JobEssence) (int, error) {
	keys := make(map[string]struct{}, len(jes))
	for _, je := range jes {
		key := je.Key()
		keys[key] = struct{}{}
		f.deletedKeys = append(f.deletedKeys, key)
	}

	deleted := 0
	for index := range f.snapshots {
		complete, removedComplete := removeJobsByKey(f.snapshots[index].complete, keys)
		buried, removedBuried := removeJobsByKey(f.snapshots[index].buried, keys)
		incomplete, removedIncomplete := removeJobsByKey(f.snapshots[index].incomplete, keys)
		f.snapshots[index].complete = complete
		f.snapshots[index].buried = buried
		f.snapshots[index].incomplete = incomplete
		deleted += removedComplete + removedBuried + removedIncomplete
	}

	return deleted, nil
}

func (f *fakeNextflowQueue) GetOrSetLimitGroup(group string) (int, error) {
	if f.limitGroups == nil {
		f.limitGroups = make(map[string]int)
	}
	f.limitGroupCalls = append(f.limitGroupCalls, group)
	parts := strings.SplitN(group, ":", 2)
	if len(parts) == 2 {
		limit, err := strconv.Atoi(parts[1])
		if err != nil {
			return -1, err
		}
		f.limitGroups[parts[0]] = limit

		return limit, nil
	}
	if limit, ok := f.limitGroups[group]; ok {
		return limit, nil
	}

	return -1, nil
}

func (f *fakeNextflowQueue) GetByRepGroupMatch(_ string, _ jobqueue.RepGroupMatch, _ int, state jobqueue.JobState, _ bool, _ bool) ([]*jobqueue.Job, error) {
	snapshot := f.current()
	if state == "" {
		jobs := cloneJobs(snapshot.complete)
		jobs = append(jobs, cloneJobs(snapshot.buried)...)
		jobs = append(jobs, cloneJobs(snapshot.incomplete)...)

		return jobs, nil
	}
	if state == jobqueue.JobStateComplete {
		return cloneJobs(snapshot.complete), nil
	}
	if state == jobqueue.JobStateBuried {
		return cloneJobs(snapshot.buried), nil
	}

	return nil, nil
}

func (f *fakeNextflowQueue) GetIncompleteByRepGroupMatch(_ string, _ jobqueue.RepGroupMatch, _ int, _ jobqueue.JobState, _ bool, _ bool) ([]*jobqueue.Job, error) {
	return cloneJobs(f.current().incomplete), nil
}

func (f *fakeNextflowQueue) current() fakeNextflowSnapshot {
	if len(f.snapshots) == 0 {
		return fakeNextflowSnapshot{}
	}
	if f.index >= len(f.snapshots) {
		return f.snapshots[len(f.snapshots)-1]
	}

	return f.snapshots[f.index]
}

func (f *fakeNextflowQueue) advance() {
	if f.index < len(f.snapshots)-1 {
		f.index++
	}
}

func removeJobsByKey(jobs []*jobqueue.Job, keys map[string]struct{}) ([]*jobqueue.Job, int) {
	filtered := make([]*jobqueue.Job, 0, len(jobs))
	removed := 0
	for _, job := range jobs {
		if _, ok := keys[job.Key()]; ok {
			removed++
			continue
		}

		filtered = append(filtered, job)
	}

	return filtered, removed
}

func remoteWorkflowEntrypoint(script string) string {
	return "#!/usr/bin/env nextflow\n" +
		"// downloaded GitHub workflow entrypoint\n" +
		singleProcessWorkflow(script)
}
