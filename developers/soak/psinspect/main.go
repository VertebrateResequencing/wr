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

// Command psinspect is soak tooling, not part of wr: it prints, for every job
// in the given rep groups of the isolated manager that WR_CONFIG_DIR names,
// the psimjob.sh kind and id from its command with its state, exit code,
// attempts and fail reason, so run markers can be joined to the manager's
// view of each job.
//
// usage: WR_CONFIG_DIR=<dir> psinspect <repgroup>|inc:<repgroup>|key:<jobkey>...
//
// inc: fetches only a group's incomplete jobs, for groups with too much
// history to fetch whole; key: fetches one job by the key the manager logs,
// and also prints its stdout, stderr and times.
package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/VertebrateResequencing/wr/jobqueue"
)

const (
	exitUsage      = 2
	connectTimeout = 2 * time.Minute
	minFields      = 3
)

func main() {
	if os.Getenv("WR_CONFIG_DIR") == "" {
		fmt.Fprintln(os.Stderr, "psinspect: WR_CONFIG_DIR must name the isolated manager's config dir")
		os.Exit(exitUsage)
	}

	jq, err := jobqueue.ConnectUsingConfig(context.Background(), "production", connectTimeout)
	if err != nil {
		fmt.Fprintln(os.Stderr, "psinspect:", err)
		os.Exit(1)
	}

	for _, arg := range os.Args[1:] {
		rg, jobs, err := fetch(jq, arg)
		if err != nil {
			fmt.Fprintln(os.Stderr, "psinspect:", rg, err)

			continue
		}

		for _, j := range jobs {
			printJob(rg, j)
		}
	}

	jq.Disconnect() //nolint:errcheck // exiting anyway
}

// fetch returns the rep group (or key) arg names and its jobs.
func fetch(jq *jobqueue.Client, arg string) (string, []*jobqueue.Job, error) {
	if key, ok := strings.CutPrefix(arg, "key:"); ok {
		jobs, err := fetchKey(jq, key)

		return arg, jobs, err
	}

	if rg, ok := strings.CutPrefix(arg, "inc:"); ok {
		jobs, err := jq.GetByRepGroup(rg, false, 0, jobqueue.JobStateIncomplete, false, false)

		return rg, jobs, err
	}

	jobs, err := jq.GetByRepGroup(arg, false, 0, "", false, false)

	return arg, jobs, err
}

// fetchKey returns the job with the given key, after printing its stdout,
// stderr and times as # comment lines.
func fetchKey(jq *jobqueue.Client, key string) ([]*jobqueue.Job, error) {
	j, err := jq.GetByEssence(&jobqueue.JobEssence{JobKey: key}, true, false)
	if err != nil || j == nil {
		return nil, err
	}

	so, _ := j.StdOut() //nolint:errcheck // shown only as a comment
	se, _ := j.StdErr() //nolint:errcheck // shown only as a comment

	fmt.Fprintf(os.Stdout, "# %s rg=%s host=%s pid=%d start=%s end=%s attempts=%d\n# stdout: %.200s\n# stderr: %.300s\n",
		key, j.RepGroup, j.Host, j.Pid, j.StartTime, j.EndTime, j.Attempts, so, se)

	return []*jobqueue.Job{j}, nil
}

func printJob(rg string, j *jobqueue.Job) {
	kind, id := "", ""
	if f := strings.Fields(j.Cmd); len(f) >= minFields && strings.HasSuffix(f[0], "psimjob.sh") {
		kind, id = f[1], f[2]
	}

	fmt.Fprintf(os.Stdout, "%s\t%s\t%s\t%s\t%d\t%d\t%d\t%s\t%s\t%s\t%d\t%d\n", rg, kind, id, j.State, j.Exitcode,
		j.Attempts, j.UntilBuried, strings.ReplaceAll(j.FailReason, "\t", " "), j.Key(), j.Host,
		j.StartTime.Unix(), j.EndTime.Unix())
}
