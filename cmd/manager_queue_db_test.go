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
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/VertebrateResequencing/wr/internal"
	"github.com/VertebrateResequencing/wr/jobqueue"
	. "github.com/smartystreets/goconvey/convey"
	"github.com/ugorji/go/codec"
	bolt "go.etcd.io/bbolt"
)

const (
	mqdbDefaultQueue   = "wrdefq"
	mqdbAvoidedQueue   = "wrdefavoidq"
	mqdbDefaultAvoid   = "wrdefavoid"
	mqdbQueueMarker    = "wrdef"
	mqdbKeyQueue       = "scheduler_queue"
	mqdbKeyAvoid       = "scheduler_queues_avoid"
	mqdbBsubCallSuffix = ".call"
	mqdbWaitTimeout    = 20 * time.Second
	mqdbFakeExeMode    = 0o700
	mqdbShell          = "bash"
)

// TestManagerQueueDefaultsAreNotStored checks that the manager's --queue and
// --queues_avoid defaults are only applied when the lsf scheduler submits, and
// are never stored in a job: jobs added via the wr add parsing path, the Go
// client and the REST API have no scheduler_queue or scheduler_queues_avoid in
// their stored Requirements.Other, live or archived, yet bsub is given the
// default queue.
func TestManagerQueueDefaultsAreNotStored(t *testing.T) {
	ctx := context.Background()

	Convey("Given a live lsf-scheduler manager with default queue settings and fake LSF exes", t, func() {
		bsubCallsDir := installFakeLSF(t)

		oldScheduler, oldQueue, oldAvoid := scheduler, managerQueue, managerQueuesAvoid

		defer func() {
			scheduler, managerQueue, managerQueuesAvoid = oldScheduler, oldQueue, oldAvoid
		}()

		scheduler = schedulerLSF
		managerQueue = mqdbAvoidedQueue + "," + mqdbDefaultQueue
		managerQueuesAvoid = mqdbDefaultAvoid

		oldConfig := config

		defer func() { config = oldConfig }()

		// the scheduler config comes from the real manager start mapping
		config = &internal.Config{Deployment: internal.Development, RunnerExecShell: mqdbShell}
		schedulerConfig, _ := buildSchedulerConfig("wr", nil, nil)

		testConfig, serverConfig, addr, _, server, token := startTestServer(ctx, t, func(sc *jobqueue.ServerConfig) {
			sc.SchedulerName = schedulerLSF
			sc.SchedulerConfig = schedulerConfig
			sc.RunnerCmd = "true --group '%s' --deployment %s --server '%s' --domain %s " +
				"--reserve_timeout %d --max_mins %d"
		})

		stopped := false

		defer func() {
			if !stopped {
				server.Stop(ctx, true)
			}
		}()

		jq, err := jobqueue.Connect(addr, serverConfig.CAFile, serverConfig.CertDomain, token, testConnectTimeout)
		So(err, ShouldBeNil)

		defer func() { _ = jq.Disconnect() }() //nolint:errcheck

		addBatch := func(label string) []string {
			cmdPath := filepath.Join(t.TempDir(), "cmds.txt")
			So(os.WriteFile(cmdPath, []byte("echo cli "+label+"\n"), 0o600), ShouldBeNil)

			configureAddParserTest(t, cmdPath)

			config = testConfig
			cmdRepGroup = "mqdb-cli-" + label

			cliJobs, _, _ := parseCmdFile(jq, false, false)
			So(cliJobs, ShouldHaveLength, 1)

			goJob := &jobqueue.Job{
				Cmd:          "echo go " + label,
				Cwd:          t.TempDir(),
				ReqGroup:     "mqdb",
				RepGroup:     "mqdb-go-" + label,
				Requirements: cliJobs[0].Requirements.Clone(),
			}
			goJob.Requirements.Other = nil

			added, _, errA := jq.Add([]*jobqueue.Job{cliJobs[0], goJob}, os.Environ(), true)
			So(errA, ShouldBeNil)
			So(added, ShouldEqual, 2)

			restCmd := "echo rest " + label
			postRESTJob(t, testConfig, token, restCmd, "mqdb-rest-"+label)

			return []string{cliJobs[0].Cmd, goJob.Cmd, restCmd}
		}

		archivedCmds := addBatch("archived")

		// act as the runners bsub was asked to start, archiving each job
		So(archiveViaRunnerGroups(jq, bsubCallsDir, len(archivedCmds)), ShouldEqual, len(archivedCmds))

		liveCmds := addBatch("live")

		bsubCalls := waitForBsubCalls(bsubCallsDir)

		So(jq.Disconnect(), ShouldBeNil)

		server.Stop(ctx, true)

		stopped = true

		Convey("bsub was given the default queue, and the defaults appear nowhere else in its args", func() {
			So(bsubCalls, ShouldNotBeEmpty)

			for _, call := range bsubCalls {
				queues, others := splitBsubQueueArgs(call)
				So(queues, ShouldResemble, []string{mqdbDefaultQueue})

				for _, arg := range others {
					So(arg, ShouldNotContainSubstring, mqdbQueueMarker)
				}
			}
		})

		Convey("no stored job, live or archived, has a scheduler queue setting", func() {
			live := readStoredJobs(serverConfig.DBFile, "jobslive")
			complete := readStoredJobs(serverConfig.DBFile, "jobscomplete")

			So(storedCmds(live), ShouldResemble, sortedCopy(liveCmds))
			So(storedCmds(complete), ShouldResemble, sortedCopy(archivedCmds))

			for _, stored := range append(live, complete...) {
				So(strings.Contains(stored.raw, mqdbKeyQueue), ShouldBeFalse)
				So(strings.Contains(stored.raw, mqdbQueueMarker), ShouldBeFalse)

				if stored.job.Requirements != nil {
					_, hasQueue := stored.job.Requirements.Other[mqdbKeyQueue]
					_, hasAvoid := stored.job.Requirements.Other[mqdbKeyAvoid]

					So(hasQueue, ShouldBeFalse)
					So(hasAvoid, ShouldBeFalse)
				}
			}
		})
	})
}

// installFakeLSF puts fake lsadmin, bqueues, bsub, bjobs, bkill and bmgroup
// exes first in PATH, and returns the directory in which bsub records the args
// of each call, one per line, in its own file ending mqdbBsubCallSuffix.
func installFakeLSF(t *testing.T) string {
	t.Helper()

	dir := t.TempDir()
	callsDir := filepath.Join(dir, "bsub-calls")

	if err := os.Mkdir(callsDir, mqdbFakeExeMode); err != nil {
		t.Fatal(err)
	}

	queue := func(name string) string {
		return "QUEUE: " + name + "\n" +
			"PRIO NICE STATUS          MAX JL/U JL/P JL/H NJOBS  PEND   RUN SSUSP USUSP  RSV\n" +
			" 30   20  Open:Active       -    -    -    -     0     0     0     0     0    0\n" +
			"USERS: all\nHOSTS:  all\n\n"
	}

	exes := map[string]string{
		"lsadmin": "echo 'LSF_UNIT_FOR_LIMITS = MB'\n",
		"bqueues": "cat <<'EOF'\n" + queue(mqdbAvoidedQueue) + queue(mqdbDefaultQueue) + "EOF\n",
		"bsub": "f=$(mktemp -p " + callsDir + ")\nprintf '%s\\n' \"$@\" > \"$f\"\n" +
			"mv \"$f\" \"$f" + mqdbBsubCallSuffix + "\"\necho 'Job <321>'\n",
		"bjobs": "if [ -n \"$2\" ]; then\n" +
			"  echo \"$2 user RUN " + mqdbDefaultQueue + " host1 host2 fakejobname000000000000000 Jul 22 12:00\"\nfi\n",
		"bkill":   "",
		"bmgroup": "",
	}

	for name, body := range exes {
		err := os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/bash\n"+body+"exit 0\n"), mqdbFakeExeMode)
		if err != nil {
			t.Fatal(err)
		}
	}

	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	return callsDir
}

// postRESTJob adds a job with the given cmd and rep group via the REST API.
func postRESTJob(t *testing.T, testConfig *internal.Config, token []byte, cmd, repGroup string) {
	t.Helper()

	caCert, err := os.ReadFile(testConfig.ManagerCAFile)
	So(err, ShouldBeNil)

	pool := x509.NewCertPool()
	So(pool.AppendCertsFromPEM(caCert), ShouldBeTrue)

	client := &http.Client{Transport: &http.Transport{
		Proxy:           nil,
		TLSClientConfig: &tls.Config{ServerName: testConfig.ManagerCertDomain, RootCAs: pool, MinVersion: tls.VersionTLS12},
	}}

	body, err := json.Marshal([]map[string]string{{"cmd": cmd, "rep_grp": repGroup, "cwd": t.TempDir()}})
	So(err, ShouldBeNil)

	url := "https://" + testConfig.ManagerCertDomain + ":" + testConfig.ManagerWeb + "/rest/v1/jobs/"

	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, url, bytes.NewReader(body))
	So(err, ShouldBeNil)
	req.Header.Add("Authorization", "Bearer "+string(token))
	req.Header.Add("Content-Type", "application/json")

	resp, err := client.Do(req)
	So(err, ShouldBeNil)

	defer resp.Body.Close()

	So(resp.StatusCode, ShouldEqual, http.StatusCreated)
}

// waitForBsubCalls waits until fake bsub has been called at least once, and
// returns the args of each completed call.
func waitForBsubCalls(callsDir string) [][]string {
	deadline := time.Now().Add(mqdbWaitTimeout)

	for {
		paths, err := filepath.Glob(filepath.Join(callsDir, "*"+mqdbBsubCallSuffix))
		if err == nil && len(paths) > 0 {
			calls := make([][]string, 0, len(paths))

			for _, path := range paths {
				content, errr := os.ReadFile(path)
				if errr != nil {
					continue
				}

				calls = append(calls, strings.Split(strings.TrimSuffix(string(content), "\n"), "\n"))
			}

			return calls
		}

		if time.Now().After(deadline) {
			return nil
		}

		time.Sleep(100 * time.Millisecond)
	}
}

// archiveViaRunnerGroups reserves, starts and archives up to n jobs, using the
// scheduler groups of the runners fake bsub was asked to submit, and returns
// how many it archived.
func archiveViaRunnerGroups(jq *jobqueue.Client, callsDir string, n int) int {
	groupRe := regexp.MustCompile(`--group '([^']+)'`)
	deadline := time.Now().Add(mqdbWaitTimeout)
	archived := 0

	for archived < n && time.Now().Before(deadline) {
		groups := make(map[string]bool)

		for _, call := range waitForBsubCalls(callsDir) {
			for _, arg := range call {
				if m := groupRe.FindStringSubmatch(arg); m != nil {
					groups[m[1]] = true
				}
			}
		}

		for group := range groups {
			job, err := jq.ReserveScheduled(100*time.Millisecond, group)
			if err != nil || job == nil {
				continue
			}

			So(jq.Started(job, os.Getpid()), ShouldBeNil)
			So(jq.Archive(job, &jobqueue.JobEndState{Exited: true, Exitcode: 0, EndTime: time.Now()}), ShouldBeNil)

			archived++
		}
	}

	return archived
}

// splitBsubQueueArgs returns the values given to -q in args, and all the other
// args.
func splitBsubQueueArgs(args []string) (queues, others []string) {
	for i := 0; i < len(args); i++ {
		if args[i] == "-q" && i+1 < len(args) {
			queues = append(queues, args[i+1])
			i++

			continue
		}

		others = append(others, args[i])
	}

	return queues, others
}

type mqdbStoredJob struct {
	raw string
	job *jobqueue.Job
}

// readStoredJobs decodes every job in the given bucket of the (closed) manager
// database.
func readStoredJobs(dbFile, bucket string) []mqdbStoredJob {
	db, err := bolt.Open(dbFile, 0o600, &bolt.Options{ReadOnly: true, Timeout: 5 * time.Second})
	So(err, ShouldBeNil)

	defer db.Close()

	var stored []mqdbStoredJob

	err = db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte(bucket))
		So(b, ShouldNotBeNil)

		return b.ForEach(func(_, v []byte) error {
			job := &jobqueue.Job{}
			if errd := codec.NewDecoderBytes(v, new(codec.BincHandle)).Decode(job); errd != nil {
				return errd
			}

			stored = append(stored, mqdbStoredJob{raw: string(v), job: job})

			return nil
		})
	})
	So(err, ShouldBeNil)

	return stored
}

func storedCmds(stored []mqdbStoredJob) []string {
	cmds := make([]string, 0, len(stored))
	for _, s := range stored {
		cmds = append(cmds, s.job.Cmd)
	}

	return sortedCopy(cmds)
}

func sortedCopy(in []string) []string {
	out := slices.Clone(in)
	slices.Sort(out)

	return out
}
