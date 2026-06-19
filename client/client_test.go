/*******************************************************************************
 * Copyright (c) 2021,2025 Genome Research Ltd.
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

package client

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"slices"
	"strconv"
	"testing"
	"time"

	clienttesting "github.com/VertebrateResequencing/wr/client/testing"
	"github.com/VertebrateResequencing/wr/jobqueue"
	jqs "github.com/VertebrateResequencing/wr/jobqueue/scheduler"
	"github.com/inconshreveable/log15/v3"
	. "github.com/smartystreets/goconvey/convey"
)

const testDeployment = "development"

func TestSchedulerSubmitJobsAndReturnIDs(t *testing.T) {
	Convey("Given a running test manager and one scheduler job", t, func() {
		ctx := context.Background()

		config, d := clienttesting.PrepareWrConfig(t)
		defer d()

		server := clienttesting.Serve(t, config)
		defer server.Stop(ctx, true)

		s, err := New(SchedulerSettings{
			Deployment: testDeployment,
			Timeout:    10 * time.Second,
			Logger:     log15.New(),
		})
		So(err, ShouldBeNil)

		So(s, ShouldNotBeNil)
		defer func() {
			So(s.Disconnect(), ShouldBeNil)
		}()

		job := s.NewJob("echo ok", "rg-a1", "req-a1", "", "", nil)
		_ = &ErrDuplicateJobs

		So(ErrDuplicateJobs.Error(), ShouldEqual, "some of the added jobs were duplicates")

		Convey("SubmitJobsAndReturnIDs returns the submitted job key", func() {
			keys, err := s.SubmitJobsAndReturnIDs([]*jobqueue.Job{job}, SubmitJobsOptions{})
			So(err, ShouldBeNil)
			So(keys, ShouldResemble, []string{job.Key()})

			info := server.GetServerStats()
			So(info.Ready, ShouldEqual, 1)

			Convey("submitting the same queued job again returns its key without adding another ready job", func() {
				keys, err = s.SubmitJobsAndReturnIDs([]*jobqueue.Job{job}, SubmitJobsOptions{})
				So(err, ShouldBeNil)
				So(keys, ShouldResemble, []string{job.Key()})

				info = server.GetServerStats()
				So(info.Ready, ShouldEqual, 1)

				Convey("SubmitJobs exposes duplicate failures through ErrDuplicateJobs", func() {
					err = s.SubmitJobs([]*jobqueue.Job{job})
					So(err, ShouldNotBeNil)
					So(errors.Is(err, ErrDuplicateJobs), ShouldBeTrue)
					So(err.Error(), ShouldEqual, "some of the added jobs were duplicates")
				})
			})
		})
	})
}

func TestSchedulerSubmitJobsOptions(t *testing.T) {
	Convey("Given a running test manager and scheduler", t, func() {
		ctx := context.Background()

		config, d := clienttesting.PrepareWrConfig(t)
		defer d()

		server := clienttesting.Serve(t, config)
		defer server.Stop(ctx, true)

		s, err := New(SchedulerSettings{
			Deployment: testDeployment,
			Timeout:    10 * time.Second,
			Logger:     log15.New(),
		})
		So(err, ShouldBeNil)
		So(s, ShouldNotBeNil)

		defer func() {
			So(s.Disconnect(), ShouldBeNil)
		}()

		jq, ok := s.jq.(*jobqueue.Client)
		So(ok, ShouldBeTrue)

		Convey("completed jobs are skipped by default and rerun when requested", func() {
			job := s.NewJob("echo a2 complete", "rg-a2-complete", "req-a2", "", "", nil)

			keys, err := s.SubmitJobsAndReturnIDs([]*jobqueue.Job{job}, SubmitJobsOptions{})
			So(err, ShouldBeNil)
			So(keys, ShouldResemble, []string{job.Key()})

			reserved, err := jq.Reserve(50 * time.Millisecond)
			So(err, ShouldBeNil)
			So(reserved, ShouldNotBeNil)
			So(reserved.Key(), ShouldEqual, job.Key())
			So(jq.Started(reserved, os.Getpid()), ShouldBeNil)

			err = jq.Archive(reserved, &jobqueue.JobEndState{
				Exited:   true,
				Exitcode: 0,
				EndTime:  time.Now(),
			})
			So(err, ShouldBeNil)

			keys, err = s.SubmitJobsAndReturnIDs([]*jobqueue.Job{job}, SubmitJobsOptions{})
			So(err, ShouldBeNil)
			So(keys, ShouldBeEmpty)

			info := server.GetServerStats()
			So(info.Ready, ShouldEqual, 0)

			keys, err = s.SubmitJobsAndReturnIDs([]*jobqueue.Job{job},
				SubmitJobsOptions{RerunCompleted: true})
			So(err, ShouldBeNil)
			So(keys, ShouldResemble, []string{job.Key()})

			info = server.GetServerStats()
			So(info.Ready, ShouldEqual, 1)
		})

		Convey("explicit environment variables are persisted", func() {
			job := s.NewJob("echo a2 env", "rg-a2-env", "req-a2", "", "", nil)

			keys, err := s.SubmitJobsAndReturnIDs([]*jobqueue.Job{job},
				SubmitJobsOptions{EnvVars: []string{"A=B"}})
			So(err, ShouldBeNil)
			So(keys, ShouldResemble, []string{job.Key()})

			stored, err := jq.GetByEssence(&jobqueue.JobEssence{JobKey: keys[0]}, false, true)
			So(err, ShouldBeNil)

			env, err := stored.Env()
			So(err, ShouldBeNil)
			So(slices.Contains(env, "A=B"), ShouldBeTrue)
		})

		Convey("an explicit empty environment is persisted as empty", func() {
			t.Setenv("WR_A2_EMPTY_ENV_SHOULD_NOT_APPEAR", "present")

			job := s.NewJob("echo a2 empty env", "rg-a2-empty-env", "req-a2", "", "", nil)

			keys, err := s.SubmitJobsAndReturnIDs([]*jobqueue.Job{job},
				SubmitJobsOptions{EnvVars: []string{}})
			So(err, ShouldBeNil)
			So(keys, ShouldResemble, []string{job.Key()})

			stored, err := jq.GetByEssence(&jobqueue.JobEssence{JobKey: keys[0]}, false, true)
			So(err, ShouldBeNil)

			env, err := stored.Env()
			So(err, ShouldBeNil)
			So(env, ShouldResemble, []string{})
		})
	})
}

func TestScheduler(t *testing.T) {
	Convey("Given some scheduler settings", t, func() {
		deployment := testDeployment
		timeout := 10 * time.Second
		logger := log15.New()
		ctx := context.Background()

		settings := SchedulerSettings{
			Deployment: deployment,
			Timeout:    timeout,
			Logger:     logger,
		}

		Convey("You can get unique strings", func() {
			str := UniqueString()
			So(len(str), ShouldEqual, 20)

			str2 := UniqueString()
			So(len(str2), ShouldEqual, 20)
			So(str2, ShouldNotEqual, str)
		})

		Convey("When the jobqueue server is up", func() {
			config, d := clienttesting.PrepareWrConfig(t)
			defer d()

			server := clienttesting.Serve(t, config)
			defer server.Stop(ctx, true)

			Convey("You can make a Scheduler", func() {
				s, err := New(settings)
				So(err, ShouldBeNil)
				So(s, ShouldNotBeNil)

				wd, err := os.Getwd()
				So(err, ShouldBeNil)
				So(s.cwd, ShouldEqual, wd)

				exe, err := os.Executable()
				So(err, ShouldBeNil)
				So(s.Executable(), ShouldEqual, exe)

				So(s.jq, ShouldNotBeNil)

				Convey("which lets you create jobs", func() {
					job := s.NewJob("cmd", "rep", "req", "", "", nil)
					So(job.Cmd, ShouldEqual, "cmd")
					So(job.RepGroup, ShouldEqual, "rep")
					So(job.ReqGroup, ShouldEqual, "req")
					So(job.Cwd, ShouldEqual, wd)
					So(job.CwdMatters, ShouldBeTrue)
					So(job.Requirements, ShouldResemble, &jqs.Requirements{RAM: 100, Time: 10 * time.Second, Cores: 1, Disk: 1})
					So(job.Retries, ShouldEqual, 30)
					So(job.DepGroups, ShouldBeNil)
					So(job.Dependencies, ShouldBeNil)
					So(job.Override, ShouldEqual, 0)

					job2 := s.NewJob("cmd2", "rep", "req", "a", "b", nil)
					So(job2.Cmd, ShouldEqual, "cmd2")
					So(job2.DepGroups, ShouldResemble, []string{"a"})
					So(job2.Dependencies, ShouldResemble,
						jobqueue.Dependencies{&jobqueue.Dependency{DepGroup: "b"}})

					Convey("which you can add to the queue", func() {
						err = s.SubmitJobs([]*jobqueue.Job{job, job2})
						So(err, ShouldBeNil)

						info := server.GetServerStats()
						So(info.Ready, ShouldEqual, 2)

						Convey("but you get an error if there are duplicates", func() {
							err = s.SubmitJobs([]*jobqueue.Job{job, job2})
							So(err, ShouldNotBeNil)
							So(errors.Is(err, ErrDuplicateJobs), ShouldBeTrue)
							So(err.Error(), ShouldEqual, "some of the added jobs were duplicates")

							info := server.GetServerStats()
							So(info.Ready, ShouldEqual, 2)
						})
					})

					Convey("which you can't add to the queue if the server is down", func() {
						server.Stop(ctx, true)

						err = s.SubmitJobs([]*jobqueue.Job{job, job2})
						So(err, ShouldNotBeNil)
					})

					Convey("which you can't add to the queue if you disconnected", func() {
						err = s.Disconnect()
						So(err, ShouldBeNil)
						err = s.SubmitJobs([]*jobqueue.Job{job, job2})
						So(err, ShouldNotBeNil)
					})
				})
			})

			Convey("You can make a Scheduler with a specified cwd and it creates jobs in there", func() {
				cwd := t.TempDir()
				settings.Cwd = cwd

				s, err := New(settings)
				So(err, ShouldBeNil)
				So(s, ShouldNotBeNil)

				job := s.NewJob("cmd", "rep", "req", "", "", nil)
				So(job.Cwd, ShouldEqual, cwd)
				So(job.CwdMatters, ShouldBeTrue)
			})

			Convey("You can't create a Scheduler in an invalid dir", func() {
				d := cdNonExistantDir(t)
				defer d()

				s, err := New(settings)
				So(err, ShouldNotBeNil)
				So(s, ShouldBeNil)
			})

			Convey("You can't create a Scheduler if you pass an invalid dir", func() {
				settings.Cwd = "/non_existent"
				s, err := New(settings)
				So(err, ShouldNotBeNil)
				So(s, ShouldBeNil)
			})

			Convey("You can make a Scheduler that creates sudo jobs", func() {
				s, err := New(settings)
				So(err, ShouldBeNil)
				So(s, ShouldNotBeNil)
				s.EnableSudo()

				job := s.NewJob("cmd", "rep", "req", "", "", nil)
				So(job.Cmd, ShouldEqual, "sudo cmd")
			})

			Convey("You can make a Scheduler with a Req override", func() {
				s, err := New(settings)
				So(err, ShouldBeNil)
				So(s, ShouldNotBeNil)

				req := DefaultRequirements()
				req.RAM = 16000

				job := s.NewJob("cmd", "rep", "req", "", "", req)
				So(job.Requirements.RAM, ShouldEqual, 16000)
				So(job.Override, ShouldEqual, 1)
			})

			Convey("You can make a Scheduler with a queue override", func() {
				settings.Queue = "foo"
				s, err := New(settings)
				So(err, ShouldBeNil)
				So(s, ShouldNotBeNil)

				dreq := DefaultRequirements()

				job := s.NewJob("cmd", "rep", "req", "", "", nil)
				So(job.Requirements.RAM, ShouldEqual, dreq.RAM)
				So(job.Override, ShouldEqual, 0)
				So(job.Requirements.Other, ShouldResemble, map[string]string{"scheduler_queue": "foo"})
			})

			Convey("You can make a Scheduler with queues to avoid", func() {
				settings.QueuesAvoid = "avoid,queue"
				s, err := New(settings)
				So(err, ShouldBeNil)
				So(s, ShouldNotBeNil)

				dreq := DefaultRequirements()
				job := s.NewJob("cmd", "rep", "req", "", "", nil)
				So(job.Requirements.RAM, ShouldEqual, dreq.RAM)
				So(job.Override, ShouldEqual, 0)
				So(job.Requirements.Other, ShouldResemble, map[string]string{"scheduler_queues_avoid": "avoid,queue"})
			})
		})

		Convey("When the jobqueue server is not up, you can't make a Scheduler", func() {
			_, d := clienttesting.PrepareWrConfig(t)
			defer d()

			s, err := New(settings)
			So(err, ShouldNotBeNil)
			So(s, ShouldBeNil)
		})
	})
}

// cdNonExistantDir changes directory to a temp directory, then deletes that
// directory. It returns a function you should defer to change back to your
// original directory.
func cdNonExistantDir(t *testing.T) func() {
	t.Helper()

	tmpDir, d := clienttesting.CDTmpDir(t)

	os.RemoveAll(tmpDir)

	return d
}

func TestFakeScheduler(t *testing.T) {
	Convey("Given scheduler settings configured to store add commands", t, func() {
		PretendSubmissions = " "

		settings := SchedulerSettings{
			Deployment: testDeployment,
			Timeout:    10 * time.Second,
			Logger:     log15.New(),
		}

		Convey("You can make a Scheduler that records submitted jobs without a real server", func() {
			s, err := New(settings)
			So(err, ShouldBeNil)
			So(s, ShouldNotBeNil)

			job1 := s.NewJob("cmd1", "rep1suffix", "req1", "depg1", "dep1", nil)
			job2 := s.NewJob("cmd2", "rep2suffix", "req2", "depg2", "dep2", nil)

			err = s.SubmitJobs([]*jobqueue.Job{job1, job2})
			So(err, ShouldBeNil)

			submittedJobs := s.SubmittedJobs()
			So(submittedJobs, ShouldResemble, []*jobqueue.Job{job1, job2})

			Convey("You can FindJobsByRepGroupSuffix", func() {
				jobs, err := s.FindJobsByRepGroupSuffix("none")
				So(err, ShouldBeNil)
				So(jobs, ShouldBeNil)

				jobs, err = s.FindJobsByRepGroupSuffix("p1suffix")
				So(err, ShouldBeNil)
				So(jobs, ShouldResemble, []*jobqueue.Job{job1})

				jobs, err = s.FindJobsByRepGroupSuffix("suffix")
				So(err, ShouldBeNil)
				So(jobs, ShouldResemble, []*jobqueue.Job{job1, job2})
			})

			Convey("You can FindJobsByRepGroupPrefixAndState", func() {
				jobs, err := s.FindJobsByRepGroupPrefixAndState("none", "")
				So(err, ShouldBeNil)
				So(jobs, ShouldBeNil)

				jobs, err = s.FindJobsByRepGroupPrefixAndState("rep1", jobqueue.JobStateDelayed)
				So(err, ShouldBeNil)
				So(jobs, ShouldResemble, []*jobqueue.Job{job1})

				jobs, err = s.FindJobsByRepGroupPrefixAndState("ep1", jobqueue.JobStateDelayed)
				So(err, ShouldBeNil)
				So(jobs, ShouldBeNil)

				jobs, err = s.FindJobsByRepGroupPrefixAndState("rep1", jobqueue.JobStateRunning)
				So(err, ShouldBeNil)
				So(jobs, ShouldBeNil)

				jobs, err = s.FindJobsByRepGroupPrefixAndState("rep", "")
				So(err, ShouldBeNil)
				So(jobs, ShouldResemble, []*jobqueue.Job{job1, job2})
			})

			Convey("You can find only incomplete jobs by repgroup match", func() {
				job3 := s.NewJob("cmd3", "rep1complete", "req3", "", "", nil)
				err = s.SubmitJobs([]*jobqueue.Job{job3})
				So(err, ShouldBeNil)

				job2.State = jobqueue.JobStateReady
				job3.State = jobqueue.JobStateComplete

				jobs, errf := s.FindIncompleteJobsByRepGroup("rep1", jobqueue.RepGroupMatchPrefix)
				So(errf, ShouldBeNil)
				So(jobs, ShouldResemble, []*jobqueue.Job{job1})
			})

			Convey("You can find only incomplete jobs by repgroup match and state", func() {
				job3 := s.NewJob("cmd3", "rep1running", "req3", "", "", nil)
				err = s.SubmitJobs([]*jobqueue.Job{job3})
				So(err, ShouldBeNil)

				job1.State = jobqueue.JobStateDelayed
				job2.State = jobqueue.JobStateReady
				job3.State = jobqueue.JobStateRunning

				jobs, errf := s.FindIncompleteJobsByRepGroupAndState("rep1",
					jobqueue.RepGroupMatchPrefix, jobqueue.JobStateRunning)
				So(errf, ShouldBeNil)
				So(jobs, ShouldResemble, []*jobqueue.Job{job3})
			})

			Convey("You can get the latest completion time by repgroup", func() {
				now := time.Now().Truncate(time.Second)

				job1.State = jobqueue.JobStateComplete
				job1.EndTime = now.Add(1 * time.Second)

				job2.State = jobqueue.JobStateComplete
				job2.EndTime = now.Add(2 * time.Second)

				job3 := s.NewJob("cmd3", "rep2suffix", "req3", "", "", nil)
				err = s.SubmitJobs([]*jobqueue.Job{job3})
				So(err, ShouldBeNil)

				job3.State = jobqueue.JobStateComplete
				job3.EndTime = now.Add(3 * time.Second)

				lct, errf := s.GetLastCompletionTimeByRepGroup("rep1suffix",
					jobqueue.RepGroupMatchExact)
				So(errf, ShouldBeNil)
				So(lct, ShouldResemble,
					map[string]time.Time{"rep1suffix": job1.EndTime})

				lct, errf = s.GetLastCompletionTimeByRepGroup("rep",
					jobqueue.RepGroupMatchPrefix)
				So(errf, ShouldBeNil)
				So(lct, ShouldResemble, map[string]time.Time{
					"rep1suffix": job1.EndTime,
					"rep2suffix": job3.EndTime,
				})

				lct, errf = s.GetLastCompletionTimeByRepGroup("missing",
					jobqueue.RepGroupMatchExact)
				So(errf, ShouldBeNil)
				So(lct, ShouldResemble, map[string]time.Time{})
			})

			Convey("You can remove jobs", func() {
				err := s.RemoveJobs(job1)
				So(err, ShouldBeNil)

				jobs, err := s.FindJobsByRepGroupSuffix("suffix")
				So(err, ShouldBeNil)
				So(jobs, ShouldResemble, []*jobqueue.Job{job2})
			})
		})

		Convey("Setting pretendSubmissions to a file description writes new jobs to it", func() {
			pr, pw, err := os.Pipe()
			So(err, ShouldBeNil)

			PretendSubmissions = strconv.FormatUint(uint64(pw.Fd()), 10)

			var (
				jobs []*jobqueue.Job
				jch  = make(chan error)
			)

			go func() {
				jch <- json.NewDecoder(pr).Decode(&jobs)
			}()

			s, err := New(settings)
			So(err, ShouldBeNil)

			job1 := s.NewJob("cmd1", "rep1suffix", "req1", "depg1", "dep1", nil)
			job2 := s.NewJob("cmd2", "rep2suffix", "req2", "depg2", "dep2", nil)

			err = s.SubmitJobs([]*jobqueue.Job{job1, job2})
			So(err, ShouldBeNil)

			pw.Close()

			So(<-jch, ShouldBeNil)
			So(jobs, ShouldResemble, []*jobqueue.Job{job1, job2})
		})
	})
}

func TestPretendGetIncompleteByRepGroupEmptyRepGroup(t *testing.T) {
	Convey("Given a pretend jobqueue with mixed complete and incomplete jobs", t, func() {
		p := newPretendJobqueue()
		p.jobBuffer = []*jobqueue.Job{
			{RepGroup: "rg1", State: jobqueue.JobStateReady},
			{RepGroup: "rg2", State: jobqueue.JobStateRunning},
			{RepGroup: "rg3", State: jobqueue.JobStateComplete},
		}

		Convey("GetIncompleteByRepGroupMatch with empty repgroup returns all incomplete jobs", func() {
			jobs, err := p.GetIncompleteByRepGroupMatch("", jobqueue.RepGroupMatchExact,
				0, "", false, false)
			So(err, ShouldBeNil)
			So(jobs, ShouldResemble, []*jobqueue.Job{p.jobBuffer[0], p.jobBuffer[1]})
		})
	})
}

func TestPretendGetByRepGroupEmptyRepGroup(t *testing.T) {
	Convey("Given a pretend jobqueue", t, func() {
		p := newPretendJobqueue()

		Convey("GetByRepGroupMatch with empty repgroup returns ErrBadRequest", func() {
			jobs, err := p.GetByRepGroupMatch("", jobqueue.RepGroupMatchExact,
				0, "", false, false)
			So(jobs, ShouldBeNil)
			So(err, ShouldResemble, jobqueue.Error{Op: "GetByRepGroupMatch", Err: jobqueue.ErrBadRequest})
		})

		Convey("GetByRepGroup with empty repgroup returns ErrBadRequest", func() {
			jobs, err := p.GetByRepGroup("", false, 0, "", false, false)
			So(jobs, ShouldBeNil)
			So(err, ShouldResemble, jobqueue.Error{Op: "GetByRepGroupMatch", Err: jobqueue.ErrBadRequest})
		})
	})
}
