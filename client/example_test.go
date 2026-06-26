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

package client_test

import (
	"context"
	"time"

	"github.com/VertebrateResequencing/wr/client"
	"github.com/VertebrateResequencing/wr/jobqueue"
	"github.com/inconshreveable/log15/v3"
)

const (
	exampleSchedulerTimeout = time.Minute
	exampleDeployment       = "production"
	exampleJSONRetries      = 2
	exampleJSONOverride     = 1
)

func ExampleScheduler_SubmitJobsAndReturnIDs() {
	s, err := client.New(client.SchedulerSettings{
		Deployment: exampleDeployment,
		Timeout:    exampleSchedulerTimeout,
		Logger:     log15.New(),
	})

	checkExampleErr(err)
	defer func() {
		checkExampleErr(s.Disconnect())
	}()

	job := s.NewJob("samtools index sample.bam", "sample-index", "sample-index", "", "", nil)

	keys, err := s.SubmitJobsAndReturnIDs([]*jobqueue.Job{job}, client.SubmitJobsOptions{})
	checkExampleErr(err)

	submittedKey := keys[0]
	_ = submittedKey
}

func ExampleScheduler_SubmitJobsAndWait() {
	ctx, cancel := context.WithTimeout(context.Background(), time.Hour)
	defer cancel()

	s, err := client.New(client.SchedulerSettings{
		Deployment: exampleDeployment,
		Timeout:    exampleSchedulerTimeout,
		Logger:     log15.New(),
	})

	checkExampleErr(err)
	defer func() {
		checkExampleErr(s.Disconnect())
	}()

	job := s.NewJob("qc sample.bam", "sample-qc", "sample-qc", "", "", nil)

	terminalJobs, err := s.SubmitJobsAndWait(ctx, []*jobqueue.Job{job}, client.SubmitJobsOptions{})
	checkExampleErr(err)

	terminalJob := terminalJobs[0]
	stdout, err := terminalJob.StdOut()
	checkExampleErr(err)

	stderr, err := terminalJob.StdErr()
	checkExampleErr(err)

	jobResult := struct {
		State      jobqueue.JobState
		Exitcode   int
		FailReason string
		StdOut     string
		StdErr     string
	}{
		State:      terminalJob.State,
		Exitcode:   terminalJob.Exitcode,
		FailReason: terminalJob.FailReason,
		StdOut:     stdout,
		StdErr:     stderr,
	}
	_ = jobResult
}

func ExampleScheduler_SubmitJobsAndReturnIDs_rerunCompleted() {
	s, err := client.New(client.SchedulerSettings{
		Deployment: exampleDeployment,
		Timeout:    exampleSchedulerTimeout,
		Logger:     log15.New(),
	})

	checkExampleErr(err)
	defer func() {
		checkExampleErr(s.Disconnect())
	}()

	job := s.NewJob("samtools sort sample.bam", "sample-sort", "sample-sort", "", "", nil)

	keys, err := s.SubmitJobsAndReturnIDs([]*jobqueue.Job{job},
		client.SubmitJobsOptions{RerunCompleted: true})
	checkExampleErr(err)

	submittedKey := keys[0]
	_ = submittedKey
}

func ExampleScheduler_NewJobFromJSON() {
	s, err := client.New(client.SchedulerSettings{
		Deployment: exampleDeployment,
		Timeout:    exampleSchedulerTimeout,
		Logger:     log15.New(),
	})

	checkExampleErr(err)
	defer func() {
		checkExampleErr(s.Disconnect())
	}()

	spec := &jobqueue.JobViaJSON{
		Cmd:       "cram_to_bam input.cram output.bam",
		RepGrp:    "sample-convert",
		Retries:   new(exampleJSONRetries),
		LimitGrps: []string{"s3:1"},
		Memory:    "8G",
		Time:      "8h",
		Override:  new(exampleJSONOverride),
		MountConfigs: jobqueue.MountConfigs{
			{
				Mount: "/mnt/sample-data",
				Targets: []jobqueue.MountTarget{
					{
						Path:     "analysis-bucket/sample-data",
						Cache:    true,
						CacheDir: "/scratch/wr-cache/sample-data",
						Write:    true,
					},
				},
			},
		},
	}

	job, err := s.NewJobFromJSON(spec)
	checkExampleErr(err)

	keys, err := s.SubmitJobsAndReturnIDs([]*jobqueue.Job{job},
		client.SubmitJobsOptions{RerunCompleted: true})
	checkExampleErr(err)

	submittedKey := keys[0]
	_ = submittedKey
}

func checkExampleErr(err error) {
	if err != nil {
		panic(err)
	}
}

//go:fix inline
func ptr[T any](value T) *T {
	return new(value)
}
