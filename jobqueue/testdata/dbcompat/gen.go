//go:build ignore

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

// gen.go produces the committed DB-compatibility golden fixture
// (jobqueue/testdata/dbcompat/db.golden) used by
// jobqueue/reliable2_dbcompat_test.go (spec.md section F1).
//
// It MUST be run from a checkout of a PRE-REMOVAL reliable2 commit that still
// maintains the per-RepGroup complete-counter machinery (buckets
// repGroupCompleteCount / repGroupCompleteBackfilled, adjustRepGroupComplete and
// the backfill sentinel). Only such code writes the now-dead buckets that the
// fixture must contain to prove the reworked build opens a current-code-upgraded
// DB without error or data loss. See jobqueue/testdata/README.md for the exact
// regeneration procedure.
//
// The program drives the real jobqueue DB open path: it starts an in-process
// Serve on a fresh (development) BoltDB, connects a client, adds 4 jobs across
// two rep groups (two of them carrying non-empty WaitingForDepGroups and
// LimitGroups/LimitGroupsForDisplay), reserves+starts+archives two of them (so
// jobscomplete, endTimeToKey, repgroupEndTime and repGroupCompleteCount populate
// and the backfill sentinel is written), leaves the other two incomplete in
// jobslive, then shuts down cleanly and copies the resulting bolt file to the
// path given as the sole command-line argument.
//
// Usage:
//
//	go run gen.go /abs/path/to/db.golden
package main

import (
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"time"

	"github.com/VertebrateResequencing/wr/jobqueue"
	"github.com/VertebrateResequencing/wr/jobqueue/scheduler"
	"github.com/phayes/freeport"
)

const (
	completeRepGroup   = "reliable2-dbcompat-complete"
	incompleteRepGroup = "reliable2-dbcompat-incomplete"
	reqGroup           = "reliable2-dbcompat"
	missingDepGroup    = "reliable2-dbcompat-missing-depgroup"
	clientTimeout      = 15 * time.Second
)

func main() {
	if len(os.Args) != 2 {
		log.Fatalf("usage: go run gen.go /abs/path/to/db.golden")
	}

	out := os.Args[1]

	ctx := context.Background()

	dir, err := os.MkdirTemp("", "wr-dbcompat-gen-")
	if err != nil {
		log.Fatalf("failed to make temp dir: %s", err)
	}
	defer os.RemoveAll(dir)

	dbFile := filepath.Join(dir, "db")

	config := serverConfig(dir, dbFile)

	server, _, token, err := jobqueue.Serve(ctx, config)
	if err != nil {
		log.Fatalf("Serve failed: %s", err)
	}

	// Serve returns while prior-state recovery is still running, so the manager
	// port is not yet bound and populate's client would get ErrNoServer. This
	// file is //go:build ignore and run by hand, so no test run would catch it.
	<-server.Serving()

	populate(ctx, config, token)

	// give the one-time startup backfill goroutine time to write the
	// fully-backfilled sentinel to repGroupCompleteBackfilled before we close.
	time.Sleep(2 * time.Second)

	server.Stop(ctx, true)

	if err := copyFile(dbFile, out); err != nil {
		log.Fatalf("failed to copy fixture to %s: %s", out, err)
	}

	fmt.Printf("wrote DB-compat fixture to %s\n", out)
}

// serverConfig builds a minimal local-scheduler development ServerConfig whose
// token, TLS certs and BoltDB all live under dir. Serve generates the token and
// certs itself when the files do not yet exist.
func serverConfig(dir, dbFile string) jobqueue.ServerConfig {
	port, err := freeport.GetFreePort()
	if err != nil {
		log.Fatalf("failed to get free port: %s", err)
	}

	webPort, err := freeport.GetFreePort()
	if err != nil {
		log.Fatalf("failed to get free web port: %s", err)
	}

	config := jobqueue.ServerConfig{
		Port:            fmt.Sprintf("%d", port),
		WebPort:         fmt.Sprintf("%d", webPort),
		SchedulerName:   "local",
		SchedulerConfig: &scheduler.ConfigLocal{Shell: "bash"},
		DBFile:          dbFile,
		DBFileBackup:    dbFile + "_bk",
		TokenFile:       filepath.Join(dir, "token"),
		CAFile:          filepath.Join(dir, "ca.pem"),
		CertFile:        filepath.Join(dir, "cert.pem"),
		KeyFile:         filepath.Join(dir, "key.pem"),
		CertDomain:      "localhost",
		Deployment:      "development",
	}

	// a long TTR so the two jobs we reserve are not marked lost before we
	// archive them; no runners are spawned (RunnerCmd is empty) so nothing else
	// touches the queue.
	config.Timings.ItemTTR = 60 * time.Second

	return config
}

// populate connects a client and creates the fixture's jobs: two complete jobs
// (reserved+started+archived) in completeRepGroup, and two incomplete jobs left
// in jobslive in incompleteRepGroup, the latter carrying non-empty
// WaitingForDepGroups and LimitGroups (so LimitGroupsForDisplay is set too).
func populate(ctx context.Context, config jobqueue.ServerConfig, token []byte) {
	jq, err := jobqueue.Connect("localhost:"+config.Port, config.CAFile, config.CertDomain, token, clientTimeout)
	if err != nil {
		log.Fatalf("Connect failed: %s", err)
	}
	defer func() {
		if errd := jq.Disconnect(); errd != nil {
			log.Printf("client Disconnect failed: %s", errd)
		}
	}()

	env := os.Environ()

	addComplete(jq, env)
	archiveTwo(jq)
	addIncomplete(jq, env)
}

// addComplete adds the two jobs destined to be archived complete.
func addComplete(jq *jobqueue.Client, env []string) {
	jobs := []*jobqueue.Job{
		newJob("true 1", completeRepGroup, nil, nil),
		newJob("true 2", completeRepGroup, nil, nil),
	}

	inserted, _, err := jq.Add(jobs, env, true)
	if err != nil {
		log.Fatalf("Add(complete) failed: %s", err)
	}

	if inserted != len(jobs) {
		log.Fatalf("Add(complete) inserted %d, want %d", inserted, len(jobs))
	}
}

// archiveTwo reserves, starts and archives (successfully) two jobs.
func archiveTwo(jq *jobqueue.Client) {
	for i := range 2 {
		job, err := jq.Reserve(5 * time.Second)
		if err != nil {
			log.Fatalf("Reserve %d failed: %s", i, err)
		}

		if job == nil {
			log.Fatalf("Reserve %d returned no job", i)
		}

		if err := jq.Started(job, os.Getpid()); err != nil {
			log.Fatalf("Started %d failed: %s", i, err)
		}

		end := &jobqueue.JobEndState{Exited: true, Exitcode: 0, EndTime: time.Now()}
		if err := jq.Archive(job, end); err != nil {
			log.Fatalf("Archive %d failed: %s", i, err)
		}
	}
}

// addIncomplete adds the two jobs left incomplete in jobslive, carrying
// non-empty WaitingForDepGroups and LimitGroups (they have no real dependency,
// so they remain reservable/runnable after recovery, as F1 acceptance #3
// requires).
func addIncomplete(jq *jobqueue.Client, env []string) {
	jobs := []*jobqueue.Job{
		newJob("true 3", incompleteRepGroup, []string{"dbcompat-incomplete-limit"}, []string{missingDepGroup}),
		newJob("true 4", incompleteRepGroup, []string{"dbcompat-incomplete-limit"}, []string{missingDepGroup}),
	}

	inserted, _, err := jq.Add(jobs, env, true)
	if err != nil {
		log.Fatalf("Add(incomplete) failed: %s", err)
	}

	if inserted != len(jobs) {
		log.Fatalf("Add(incomplete) inserted %d, want %d", inserted, len(jobs))
	}
}

// newJob builds a Job with the standard fixture requirements. limitGroups and
// waitingForDepGroups may be nil.
func newJob(cmd, repGroup string, limitGroups, waitingForDepGroups []string) *jobqueue.Job {
	return &jobqueue.Job{
		Cmd:                 cmd,
		Cwd:                 "/tmp",
		RepGroup:            repGroup,
		ReqGroup:            reqGroup,
		Requirements:        &scheduler.Requirements{RAM: 10, Time: 10 * time.Second, Cores: 1, Other: map[string]string{}},
		LimitGroups:         limitGroups,
		WaitingForDepGroups: waitingForDepGroups,
	}
}

// copyFile copies src to dst.
func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.Create(dst)
	if err != nil {
		return err
	}

	if _, err := io.Copy(out, in); err != nil {
		out.Close()

		return err
	}

	return out.Close()
}
