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

package main

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/VertebrateResequencing/wr/client"
	"github.com/VertebrateResequencing/wr/jobqueue"
	jqs "github.com/VertebrateResequencing/wr/jobqueue/scheduler"
)

// The workload's shape. Counts are multiplied by -scale; times are simulated
// and compressed by -simminute.
const (
	// The ibackup server.
	ibServerClients = 10
	ibPutRAM        = 1024
	ibPutMemMB      = 200
	ibPutRunTime    = 20 * time.Minute
	ibPutRetries    = 3
	ibPutLimit      = 8 * time.Hour
	ibFailPct       = 1

	// The ibackup fofn watcher.
	fofnEvery         = 5 * time.Minute
	fofnNewChance     = 0.35
	fofnMedianJobs    = 150
	fofnMinJobs       = 5
	fofnMaxJobs       = 3000
	fofnDirs          = 40
	fofnMedianMinutes = 6
	fofnSigma         = 0.8
	fofnMinMemMB      = 300
	fofnMemSpreadMB   = 700
	fofnFailPct       = 2

	// wrstat multi.
	wrstatPaths          = 12
	wrstatEvery          = 8 * time.Hour
	wrstatLimitFor       = 20 * time.Hour
	wrstatScratchBase    = 120
	wrstatScratchVolumes = 6
	wrstatMedianStats    = 60
	wrstatMinStats       = 3
	wrstatStatSigma      = 0.7
	wrstatStatMedianMins = 15
	wrstatStatRunSigma   = 0.8
	wrstatWalkTime       = 8 * time.Minute
	wrstatCombineTime    = 10 * time.Minute
	wrstatWalkRAM        = 1000
	wrstatWalkMemMB      = 800
	wrstatCombineRAM     = 2000
	wrstatCombineMemMB   = 1500
	wrstatWalkLimit      = 2 * time.Hour
	wrstatCombineLimit   = 4 * time.Hour
	uniqueSuffixes       = 10000

	// wrstat-ui watch.
	wrstatUIEvery      = time.Hour
	wrstatUIBuildEvery = 6
	wrstatUIBuildTime  = 30 * time.Minute
	wrstatUIBuildRAM   = 1500
	wrstatUIBuildLimit = 3 * time.Hour

	// The portal, the Go-client waiter, the operator and the status pollers.
	portalBatch        = 1000
	portalChunk        = 100
	portalMedianMins   = 0.4
	portalSigma        = 0.5
	portalDedupeRAM    = 300
	portalCompressRAM  = 1024
	portalMemFraction  = 3
	portalRetries      = 2
	portalLimitTime    = 30 * time.Minute
	portalLowLimit     = 20
	kb                 = 1024
	pipelineJobs       = 5
	pipelineEvery      = 20 * time.Minute
	pipelineRunTime    = 3 * time.Minute
	pipelineMemMB      = 100
	pipelineRAM        = 200
	pipelineWaitFor    = 4 * time.Hour
	operatorEvery      = 90 * time.Minute
	statusCountsEvery  = 2 * time.Minute
	statusFofnEvery    = 10 * time.Minute
	statusPortalEvery  = 15 * time.Minute
	statusBuriedEvery  = 20 * time.Minute
	managerStatusEvery = time.Minute

	actorPortal = "portal"
	statusCmd   = "status"
	limitFlag   = "--limit"
)

// actor is one kind of client; it runs until ctx ends.
type actor struct {
	name string
	run  func(*sim, context.Context)
}

// actors returns every actor, in the order -actors defaults to.
func actors() []actor {
	return []actor{
		{"ibserver", (*sim).ibackupServer},
		{"fofn", (*sim).fofnWatcher},
		{"wrstat", (*sim).wrstatMulti},
		{"wrstatui", (*sim).wrstatUI},
		{actorPortal, (*sim).portal},
		{"web", (*sim).webUsers},
		{statusCmd, (*sim).statusPollers},
		{"operator", (*sim).operator},
		{"sampler", (*sim).sampler},
	}
}

func actorNames() []string {
	all := actors()
	names := make([]string, len(all))

	for i, a := range all {
		names[i] = a.name
	}

	return names
}

func findActor(name string) (func(*sim, context.Context), bool) {
	for _, a := range actors() {
		if a.name == name {
			return a.run, true
		}
	}

	return nil, false
}

func (s *sim) setLimits(ctx context.Context) {
	jq := s.newJQ(ctx, "main")
	if jq == nil {
		return
	}

	defer disconnect(jq)

	for _, lg := range []string{
		fmt.Sprintf("irods:%d", s.cfg.irodsLimit),
		fmt.Sprintf("results_portal:%d", s.cfg.portalLimit),
		fmt.Sprintf("wrstat-stat:%d", s.cfg.statLimit),
	} {
		s.measure("main", "setlimit", func() (int, error) { return jq.GetOrSetLimitGroup(lg) })
	}
}

// everySim runs fn with a connected scheduler, then again every simulated
// interval, until the run ends.
func (s *sim) everySim(ctx context.Context, actor string, interval time.Duration, fn func(*client.Scheduler)) {
	sch := s.newScheduler(ctx, actor)
	if sch == nil {
		return
	}

	defer disconnect(sch)

	for {
		fn(sch)

		if !sleep(ctx, s.sim(interval)) {
			return
		}
	}
}

// ibackupServer mimics ibackup's server: it re-submits the same N put jobs
// every simulated minute; while they are live that is all duplicates.
func (s *sim) ibackupServer(ctx context.Context) {
	const actor = "ibserver"

	req := &jqs.Requirements{RAM: ibPutRAM, Cores: 1, Time: ibPutLimit}

	s.everySim(ctx, actor, time.Minute, func(sch *client.Scheduler) {
		jobs := make([]*jobqueue.Job, s.scaled(ibServerClients))

		for i := range jobs {
			// the command must be stable to be a duplicate, as ibackup's is;
			// psimjob.sh varies the run length around the mean.
			job := sch.NewJob(s.jobCmd("put", strconv.Itoa(i), s.simSecs(ibPutRunTime), ibPutMemMB, ibFailPct, 0),
				"ibackup_server_put", "ibackup_server", "", "", req)
			job.Retries = ibPutRetries
			job.LimitGroups = []string{"irods"}
			job.Behaviours = []*jobqueue.Behaviour{{When: jobqueue.OnFailure, Do: jobqueue.Remove}}
			jobs[i] = job
		}

		s.submit(actor, "add_put", sch, jobs) //nolint:errcheck // logged by submit
	})
}

// fofnWatcher mimics ibackup's fofn watcher poll: classify by rep group
// prefix, remove buried jobs, and sometimes submit a new fofn.
func (s *sim) fofnWatcher(ctx context.Context) {
	const actor = "fofn"

	fofnN := 0

	s.everySim(ctx, actor, fofnEvery, func(sch *client.Scheduler) {
		s.fofnPoll(actor, sch)

		if s.float() < fofnNewChance {
			fofnN++
			s.fofnSubmit(actor, sch, fofnN)
		}
	})
}

// fofnPoll makes the watcher's two prefix queries and removes buried jobs.
func (s *sim) fofnPoll(actor string, sch *client.Scheduler) {
	const prefix = "ibackup_fofn_"

	var incomplete []*jobqueue.Job

	s.measure(actor, "find_incomplete_prefix", func() (int, error) {
		var err error

		incomplete, err = sch.FindIncompleteJobsByRepGroup(prefix, jobqueue.RepGroupMatchPrefix)

		return len(incomplete), err
	})

	s.measure(actor, "getlct_prefix", func() (int, error) {
		m, err := sch.GetLastCompletionTimeByRepGroup(prefix, jobqueue.RepGroupMatchPrefix)

		return len(m), err
	})

	var buried []*jobqueue.Job

	for _, j := range incomplete {
		if j.State == jobqueue.JobStateBuried {
			buried = append(buried, j)
		}
	}

	if len(buried) > 0 {
		s.measure(actor, "remove_buried", func() (int, error) { return len(buried), sch.RemoveJobs(buried...) })
	}
}

// fofnSubmit submits the chunk jobs of the fofnN-th new fofn.
func (s *sim) fofnSubmit(actor string, sch *client.Scheduler, fofnN int) {
	n := int(s.lognormal(float64(s.scaled(fofnMedianJobs)), 1.0))
	n = max(fofnMinJobs, min(n, s.scaled(fofnMaxJobs)))
	rg := fmt.Sprintf("ibackup_fofn_dir%03d_%d", fofnN%fofnDirs, time.Now().Unix())
	req := &jqs.Requirements{RAM: ibPutRAM, Cores: 1, Time: ibPutLimit}
	jobs := make([]*jobqueue.Job, n)

	for i := range jobs {
		chunk := fmt.Sprintf("/lustre/scratch/ibackup/%s/chunk.%06d", rg, i)
		cmd := s.jobCmd("fofnput", rg+"."+strconv.Itoa(i), s.simMinutes(fofnMedianMinutes, fofnSigma),
			fofnMinMemMB+s.intn(fofnMemSpreadMB), fofnFailPct, 0, "-f", chunk)
		job := sch.NewJob(cmd, rg, "ibackup", "", "", req)
		job.Retries = 0
		job.LimitGroups = []string{"irods"}
		jobs[i] = job
	}

	if err := s.submit(actor, "add_fofn", sch, jobs); err == nil {
		s.event(actor, fmt.Sprintf("fofn %s jobs=%d", rg, n))
	}
}

// wrstatRun is one `wrstat multi` invocation's names and limit group.
type wrstatRun struct {
	now, unique, limit string
}

// wrstatMulti mimics `wrstat multi`: per path a walk (which adds its own stat
// jobs into its dep group), a combine that depends on that group and a tidy
// that depends on the combine, all under a fresh datetime< limit group.
func (s *sim) wrstatMulti(ctx context.Context) {
	const actor = "wrstat"

	sch := s.newScheduler(ctx, actor)
	if sch == nil {
		return
	}

	defer disconnect(sch)

	for run := 1; ; run++ {
		r := wrstatRun{
			now:    time.Now().Format("20060102-150405"),
			unique: fmt.Sprintf("u%d%04d", time.Now().Unix(), s.intn(uniqueSuffixes)),
			// wrstat's timeout is hours; the date limit is then in the (real) future.
			limit: "datetime<" + time.Now().Add(s.sim(wrstatLimitFor)).Format(time.DateTime),
		}

		s.wrstatSubmit(actor, sch, r)
		s.event(actor, fmt.Sprintf("run %d unique=%s paths=%d", run, r.unique, s.scaled(wrstatPaths)))

		if !sleep(ctx, s.jitter(s.sim(wrstatEvery))) {
			return
		}
	}
}

// wrstatSubmit submits run r's walk, combine and tidy jobs for every path.
func (s *sim) wrstatSubmit(actor string, sch *client.Scheduler, r wrstatRun) {
	paths := s.scaled(wrstatPaths)
	walks := make([]*jobqueue.Job, 0, paths)
	combines := make([]*jobqueue.Job, 0, paths)
	tidies := make([]*jobqueue.Job, 0, paths)

	for p := range paths {
		walk, comb, tidy := s.wrstatPathJobs(actor, sch, r, p)
		walks = append(walks, walk)
		combines = append(combines, comb)
		tidies = append(tidies, tidy)
	}

	s.submit(actor, "add_walk", sch, walks)       //nolint:errcheck // logged by submit
	s.submit(actor, "add_combine", sch, combines) //nolint:errcheck // logged by submit
	s.submit(actor, "add_tidy", sch, tidies)      //nolint:errcheck // logged by submit
}

// wrstatPathJobs returns run r's walk, combine and tidy jobs for path p, after
// looking for an earlier run's tidy as wrstat does.
func (s *sim) wrstatPathJobs(actor string, sch *client.Scheduler, r wrstatRun,
	p int,
) (walk, comb, tidy *jobqueue.Job) {
	path := fmt.Sprintf("/lustre/scratch%d/team%03d", wrstatScratchBase+p%wrstatScratchVolumes, p)
	name := func(kind string) string {
		return fmt.Sprintf("wrstat-%s-%s-%s-%s", kind, strings.ReplaceAll(path, "/", "_"), r.now, r.unique)
	}

	s.measure(actor, "find_dependent_prefix", func() (int, error) {
		jobs, err := sch.FindJobsByRepGroupPrefixAndState("wrstat-tidy-"+strings.ReplaceAll(path, "/", "_"),
			jobqueue.JobStateDependent)

		return len(jobs), err
	})

	walkU := fmt.Sprintf("walk%s%02d", r.unique, p)
	combU := fmt.Sprintf("comb%s%02d", r.unique, p)
	walk = s.wrstatWalkJob(sch, r, path, walkU, name("walk"), name("stat"))

	comb = sch.NewJob(s.jobCmd("combine", combU, s.simSecs(wrstatCombineTime), wrstatCombineMemMB, 0, 0),
		name("combine"), "wrstat-combine", combU, walkU,
		&jqs.Requirements{RAM: wrstatCombineRAM, Cores: 1, Time: wrstatCombineLimit})
	comb.LimitGroups = []string{r.limit}

	tidy = sch.NewJob(s.jobCmd("tidy", combU, s.simSecs(time.Minute), 0, 0, 0), name("tidy"), "wrstat-tidy", "",
		combU, nil)

	return walk, comb, tidy
}

// wrstatWalkJob returns a walk job, which psimjob.sh makes add its own stat
// jobs, in rep group statRG, into the walk's dep group walkU.
func (s *sim) wrstatWalkJob(sch *client.Scheduler, r wrstatRun, path, walkU, walkRG,
	statRG string,
) *jobqueue.Job {
	nStat := max(wrstatMinStats, int(s.lognormal(float64(s.scaled(wrstatMedianStats)), wrstatStatSigma)))
	statSecs := s.simMinutes(wrstatStatMedianMins, wrstatStatRunSigma)

	walk := sch.NewJob(s.jobCmd("walk", walkU, s.simSecs(wrstatWalkTime), wrstatWalkMemMB, 0, 0,
		s.cfg.wrBin, s.cfg.deployment, strconv.Itoa(nStat), statRG, walkU, shellQuote("wrstat-stat,"+r.limit),
		fmt.Sprintf("%.1f", statSecs), s.cfg.workDir, s.cfg.queue, path), walkRG, "wrstat-walk", walkU, "",
		&jqs.Requirements{RAM: wrstatWalkRAM, Cores: 1, Time: wrstatWalkLimit})
	walk.LimitGroups = []string{r.limit}

	return walk
}

func shellQuote(v string) string {
	return "'" + strings.ReplaceAll(v, "'", `'\''`) + "'"
}

// wrstatUI mimics wrstat-ui watch: hourly, usually nothing to submit (and it
// submits the empty list anyway), sometimes a build and a publish that
// depends on it by essence.
func (s *sim) wrstatUI(ctx context.Context) {
	const actor = "wrstatui"

	n := 0

	s.everySim(ctx, actor, wrstatUIEvery, func(sch *client.Scheduler) {
		n++
		if n == 1 {
			return // the first submission is an hour in
		}

		var jobs []*jobqueue.Job

		if n%wrstatUIBuildEvery == 1 {
			rg := fmt.Sprintf("wrstat-ui-summarise-%d", time.Now().Unix())
			build := sch.NewJob(s.jobCmd("build", rg, s.simSecs(wrstatUIBuildTime), wrstatUIBuildRAM, 0, 0), rg,
				"wrstat-ui-summarise", "", "", &jqs.Requirements{RAM: wrstatUIBuildRAM, Cores: 1,
					Time: wrstatUIBuildLimit})
			publish := sch.NewJob(s.jobCmd("publish", rg, s.simSecs(time.Minute), 0, 0, 0), rg,
				"wrstat-ui-publish", "", "", nil)
			publish.Dependencies = jobqueue.Dependencies{jobqueue.NewEssenceDependency(build.Cmd, build.Cwd)}
			jobs = []*jobqueue.Job{build, publish}
		}

		s.submit(actor, fmt.Sprintf("add_%d", len(jobs)), sch, jobs) //nolint:errcheck // logged by submit
	})
}

// portal mimics the results_portal workload: a huge dedupe phase and a
// compress phase that depends on it chunk by chunk, with long commands.
func (s *sim) portal(ctx context.Context) {
	const actor = actorPortal

	sch := s.newScheduler(ctx, actor)
	if sch == nil {
		return
	}

	defer disconnect(sch)

	for {
		ts := time.Now().Format("20060102T150405")
		n := s.scaled(s.cfg.portalJobs)

		for phase, name := range []string{"dedupe", "compress"} {
			if !s.portalPhase(ctx, actor, sch, ts, n, phase, name) {
				return
			}
		}

		s.event(actor, fmt.Sprintf("burst %s jobs=%d x2 cmdKB=%d", ts, n, s.cfg.portalCmdKB))

		if !sleep(ctx, s.jitter(s.sim(s.cfg.portalEvery))) {
			return
		}
	}
}

// portalPhase submits one phase of a burst in batches, as the real portal
// client does, reporting false if the run ended part-way.
func (s *sim) portalPhase(ctx context.Context, actor string, sch *client.Scheduler, ts string, n, phase int,
	name string,
) bool {
	rg := fmt.Sprintf("portal_%s_%s", ts, name)
	ram := []int{portalDedupeRAM, portalCompressRAM}[phase]
	req := &jqs.Requirements{RAM: ram, Cores: 1, Time: portalLimitTime}
	jobs := make([]*jobqueue.Job, 0, portalBatch)

	for i := range n {
		// the dedupe phase makes each chunk a dep group; the compress phase
		// depends on it.
		depGroups := [2]string{fmt.Sprintf("pchunk_%s_%d", ts, i/portalChunk), ""}
		if phase == 1 {
			depGroups[0], depGroups[1] = depGroups[1], depGroups[0]
		}

		cmd := s.jobCmd("portal_"+name, fmt.Sprintf("%s.%d", ts, i), s.simMinutes(portalMedianMins, portalSigma),
			ram/portalMemFraction, 1, s.cfg.portalCmdKB*kb)
		jobs = append(jobs, portalJob(sch.NewJob(cmd, rg, "portal_builder_"+name, depGroups[0], depGroups[1], req)))

		if len(jobs) < portalBatch && i < n-1 {
			continue
		}

		s.submit(actor, "add_portal_"+name, sch, jobs) //nolint:errcheck // logged by submit
		jobs = make([]*jobqueue.Job, 0, portalBatch)

		if ctx.Err() != nil {
			return false
		}
	}

	return true
}

// portalJob sets a portal job's limit group and retries.
func portalJob(job *jobqueue.Job) *jobqueue.Job {
	job.LimitGroups = []string{"results_portal"}
	job.Retries = portalRetries

	return job
}

// statusPollers run the CLI the way people and cron jobs do, plus a Go-client
// user that waits on its jobs through a subscription.
func (s *sim) statusPollers(ctx context.Context) {
	polls := []struct {
		op    string
		every time.Duration
		args  []string
	}{
		{"status_counts", statusCountsEvery, []string{statusCmd, "-o", "counts"}},
		{"status_fofn_z", statusFofnEvery, []string{statusCmd, "-i", "ibackup_fofn_", "-z", limitFlag, "1"}},
		{"status_portal_summary", statusPortalEvery, []string{statusCmd, "-i", actorPortal, "-z", "-o", "summary"}},
		{"status_buried", statusBuriedEvery, []string{statusCmd, "-b", limitFlag, "1"}},
		{"manager_status", managerStatusEvery, []string{"manager", statusCmd}},
	}

	done := make(chan struct{})

	for _, p := range polls {
		go func() {
			defer func() { done <- struct{}{} }()

			for sleep(ctx, s.jitter(s.sim(p.every))) {
				s.wrCLI(ctx, statusCmd, p.op, p.args...)
			}
		}()
	}

	s.waiter(ctx)

	for range polls {
		<-done
	}
}

// waiter submits small batches and waits for them via SubmitJobsAndWait,
// which subscribes to their updates, like pipeline tools using wr's client.
func (s *sim) waiter(ctx context.Context) {
	const actor = "waiter"

	sch := s.newScheduler(ctx, actor)
	if sch == nil {
		return
	}

	defer disconnect(sch)

	req := &jqs.Requirements{RAM: pipelineRAM, Cores: 1, Time: time.Hour}

	for sleep(ctx, s.jitter(s.sim(pipelineEvery))) {
		rg := fmt.Sprintf("pipeline_%d", time.Now().UnixNano())
		jobs := make([]*jobqueue.Job, pipelineJobs)

		for i := range jobs {
			jobs[i] = sch.NewJob(s.jobCmd("pipeline", fmt.Sprintf("%s.%d", rg, i), s.simSecs(pipelineRunTime),
				pipelineMemMB, 0, 0), rg, "pipeline", "", "", req)
		}

		s.measure(actor, "submit_and_wait", func() (int, error) {
			wctx, cancel := context.WithTimeout(ctx, s.sim(pipelineWaitFor))
			defer cancel()

			res, err := sch.SubmitJobsAndWait(wctx, jobs, client.SubmitJobsOptions{})

			return len(res), err
		})
	}
}

// operator does what the owner does by hand: raise and lower the portal
// limit, retry buried jobs, kill a few running ones, look at a group.
func (s *sim) operator(ctx context.Context) {
	const actor = "operator"

	for sleep(ctx, s.jitter(s.sim(operatorEvery))) {
		limits := []int{portalLowLimit, s.cfg.portalLimit, s.cfg.portalLimit * 2}
		l := limits[s.intn(len(limits))]
		chores := []struct {
			op   string
			args []string
		}{
			{"limit_portal", []string{"limit", "-g", fmt.Sprintf("results_portal:%d", l)}},
			{"retry_portal", []string{"retry", "-i", actorPortal, "-z"}},
			{"kill_fofn", []string{"kill", "-i", "ibackup_fofn_dir001", "-z"}},
			{"status_plain", []string{statusCmd, "-i", "wrstat", "-z", limitFlag, "5", "-o", "plain"}},
			{"limit_list", []string{"limit"}},
		}

		c := chores[s.intn(len(chores))]
		s.wrCLI(ctx, actor, c.op, c.args...)

		if c.op == "limit_portal" {
			s.event(actor, fmt.Sprintf("results_portal limit -> %d", l))
		}
	}
}
