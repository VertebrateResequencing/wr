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

// Command dbstart is soak tooling, not part of wr: it reads a STOPPED
// manager's store (see jobqueue.ReadStore, which takes its lock) and prints one
// line per psimjob.sh job that is complete, then each that is live (and each
// relbury.sh job, as kind relb): jobscomplete or jobslive, key, kind, id,
// state, exit code, attempts, host, pid, and start and end times in unix ms. A
// live job's line shows its latest durable run state. The columns are those
// dbstart printed when the store was bbolt, named after its buckets.
//
// usage: dbstart <db dir>
package main

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/VertebrateResequencing/wr/jobqueue"
)

const (
	exitUsage = 2
	minFields = 3
)

func main() {
	os.Exit(runArgs(os.Args[1:], os.Stdout, os.Stderr))
}

// runArgs runs dbstart with the command-line arguments args (without the
// program name), writing to out and errOut, and returns the exit code.
func runArgs(args []string, out, errOut io.Writer) int {
	if len(args) != 1 {
		fmt.Fprintln(errOut, "usage: dbstart <db dir>")

		return exitUsage
	}

	if err := run(args[0], out); err != nil {
		fmt.Fprintln(errOut, "dbstart:", err)

		return 1
	}

	return 0
}

// run prints a line to out for each psimjob.sh and relbury.sh job in the store
// at dbDir: the complete ones, then the live ones, as dbstart printed bbolt's
// jobscomplete bucket before its jobslive bucket.
func run(dbDir string, out io.Writer) error {
	var live bytes.Buffer

	err := jobqueue.ReadStore(dbDir, func(isLive bool, job *jobqueue.Job) error {
		if isLive {
			printJob(&live, "jobslive", job)
		} else {
			printJob(out, "jobscomplete", job)
		}

		return nil
	})
	if err != nil {
		return err
	}

	_, err = live.WriteTo(out)

	return err
}

// printJob prints job, if it is a psimjob.sh or relbury.sh job, as being in
// bucket.
func printJob(out io.Writer, bucket string, job *jobqueue.Job) {
	f := strings.Fields(job.Cmd)
	if len(f) < minFields || (!strings.HasSuffix(f[0], "psimjob.sh") && f[0] != ":") {
		return
	}

	// relbury.sh's jobs start ": relb <name>;"
	f[2] = strings.TrimSuffix(f[2], ";")
	fmt.Fprintf(out, "%s\t%s\t%s\t%s\t%s\t%d\t%d\t%s\t%d\t%d\t%d\n", bucket, job.Key(), f[1], f[2], job.State,
		job.Exitcode, job.Attempts, job.Host, job.Pid, ms(job.StartTime), ms(job.EndTime))
}

func ms(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}

	return t.UnixMilli()
}
