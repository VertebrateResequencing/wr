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

// Command prodsim drives an ISOLATED wr manager with a production-shaped
// workload made only of harmless commands, and samples the manager's health
// while it does. It is developer tooling for developers/wrdev.sh prodsim; it
// is not part of the shipped binary. See developers/README.md.
//
// The workload is modelled on the clients the real production manager serves:
//
//   - ibackup server: every simulated minute re-submits the same N "put"
//     jobs (mostly duplicates), limit group irods, OnFailure Remove.
//   - ibackup fofn watcher: polls incomplete jobs and last completion times
//     by rep group PREFIX, removes buried jobs, and now and then submits a
//     new fofn's batch of chunk jobs in a new rep group.
//   - wrstat multi: walk jobs that themselves `wr add` stat jobs into the
//     walk's dep group, a combine job depending on it and a tidy job
//     depending on the combine, all behind a datetime< limit group.
//   - wrstat-ui watch: hourly submits nothing (production's hourly
//     "bad request" line) and sometimes a build/publish pair.
//   - portal: occasional huge two-phase bursts with ~KB-long commands behind
//     one results_portal limit group.
//   - status page users: websocket clients that connect, take the seed,
//     refresh, click a rep group, and occasionally search.
//   - wr status pollers, a Go client waiting through a subscription, and an
//     operator changing limits, retrying and killing.
//
// Every client call is timed and appended to calls.tsv; the sampler appends
// manager RSS/goroutines/heap/DB size to samples.tsv and saves pprof profiles.
// prodsim -report <dir> summarises a run.
//
// prodsim refuses to start unless WR_CONFIG_DIR is set and wr's config for the
// deployment names the manager whose runtime directory is -rundir, so it cannot
// drive a real deployment.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/VertebrateResequencing/wr/internal"
)

// Defaults of the flags that shape the run.
const (
	defaultDuration     = 2 * time.Hour
	defaultSimMinute    = 6 * time.Second
	defaultPortalJobs   = 20000
	defaultPortalLimit  = 150
	defaultPortalCmdKB  = 8
	defaultPortalEvery  = 12 * time.Hour
	defaultIrodsLimit   = 40
	defaultStatLimit    = 60
	defaultWebClients   = 3
	defaultSampleEvery  = 30 * time.Second
	defaultProfileEvery = 15 * time.Minute
)

const (
	exitFailure = 1
	exitUsage   = 2
	dirPerm     = 0o750
)

var (
	errNoConfigDir  = errors.New("WR_CONFIG_DIR must name the isolated manager's config dir")
	errNotIsolated  = errors.New("refusing to drive a manager other than the isolated one")
	errMissingFlag  = errors.New("missing required flag")
	errUnknownActor = errors.New("unknown actor")
)

type config struct {
	deployment   string
	wrBin        string
	jobScript    string
	workDir      string
	outDir       string
	queue        string
	runDir       string // the manager's runtime dir (pid, token, db, log)
	webAddr      string
	pprofAddr    string
	duration     time.Duration
	simMinute    time.Duration // real time per simulated minute
	scale        float64       // multiplier on job counts
	portalJobs   int
	portalLimit  int
	portalCmdKB  int
	portalEvery  time.Duration // simulated
	irodsLimit   int
	statLimit    int
	webClients   int
	sampleEvery  time.Duration
	profileEvery time.Duration
	seed         uint64
	actors       string
}

func main() {
	os.Exit(run())
}

// run is main, returning the exit code, so that deferred calls run first.
func run() int {
	cfg, reportDir := parseFlags()

	if reportDir != "" {
		return exitCode(report(os.Stdout, reportDir), exitFailure)
	}

	if err := cfg.validate(); err != nil {
		return exitCode(err, exitUsage)
	}

	ctx, cancel := context.WithTimeout(context.Background(), cfg.duration)
	defer cancel()

	ctx, stop := signal.NotifyContext(ctx, syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	if err := checkIsolated(ctx, cfg); err != nil {
		return exitCode(err, exitUsage)
	}

	s, err := newSim(cfg)
	if err != nil {
		return exitCode(err, exitFailure)
	}

	s.run(ctx)

	return 0
}

// exitCode prints err, if any, and returns code for it or 0 without one.
func exitCode(err error, code int) int {
	if err == nil {
		return 0
	}

	fmt.Fprintln(os.Stderr, "prodsim:", err)

	return code
}

func parseFlags() (config, string) {
	var cfg config

	flag.StringVar(&cfg.deployment, "deployment", internal.Production, "wr deployment of the isolated manager")
	flag.StringVar(&cfg.wrBin, "wr", "", "isolated wr binary (used by nested adds and CLI pollers)")
	flag.StringVar(&cfg.jobScript, "job", "", "path to psimjob.sh, the harmless job command")
	flag.StringVar(&cfg.workDir, "workdir", "", "directory jobs run in (cwd)")
	flag.StringVar(&cfg.outDir, "out", "", "directory for calls.tsv, samples.tsv, events.tsv and profiles")
	flag.StringVar(&cfg.queue, "queue", "normal", "scheduler queue")
	flag.StringVar(&cfg.runDir, "rundir", "", "the manager's runtime dir (holds pid, client.token, db, log)")
	flag.StringVar(&cfg.webAddr, "web", "", "manager web host:port")
	flag.StringVar(&cfg.pprofAddr, "pprof", "", "manager WR_PPROF_ADDR host:port (empty = no profiles)")
	flag.StringVar(&cfg.actors, "actors", strings.Join(actorNames(), ","), "comma-separated actors to run")
	workloadFlags(&cfg)

	reportDir := flag.String("report", "", "summarise an existing run directory and exit")

	flag.Parse()

	return cfg, *reportDir
}

// workloadFlags defines the flags that shape the run.
func workloadFlags(cfg *config) {
	flag.DurationVar(&cfg.duration, "duration", defaultDuration, "how long to run")
	flag.DurationVar(&cfg.simMinute, "simminute", defaultSimMinute, "real time per simulated minute")
	flag.Float64Var(&cfg.scale, "scale", 1, "multiplier on job counts")
	flag.IntVar(&cfg.portalJobs, "portal-jobs", defaultPortalJobs, "jobs per portal phase per burst (x2 phases)")
	flag.IntVar(&cfg.portalLimit, "portal-limit", defaultPortalLimit, "results_portal limit")
	flag.IntVar(&cfg.portalCmdKB, "portal-cmd-kb", defaultPortalCmdKB, "approximate portal command length in KB")
	flag.DurationVar(&cfg.portalEvery, "portal-every", defaultPortalEvery,
		"simulated interval between portal bursts (first at start)")
	flag.IntVar(&cfg.irodsLimit, "irods-limit", defaultIrodsLimit, "irods limit group limit")
	flag.IntVar(&cfg.statLimit, "stat-limit", defaultStatLimit, "wrstat-stat limit group limit")
	flag.IntVar(&cfg.webClients, "web-clients", defaultWebClients, "status page users")
	flag.DurationVar(&cfg.sampleEvery, "sample-every", defaultSampleEvery, "real interval between health samples")
	flag.DurationVar(&cfg.profileEvery, "profile-every", defaultProfileEvery, "real interval between pprof captures")
	flag.Uint64Var(&cfg.seed, "seed", 1, "random seed")
}

// validate checks the required flags are set and every actor is known.
func (c config) validate() error {
	for name, v := range map[string]string{"wr": c.wrBin, "job": c.jobScript, "workdir": c.workDir,
		"out": c.outDir, "rundir": c.runDir} {
		if v == "" {
			return fmt.Errorf("%w: -%s", errMissingFlag, name)
		}
	}

	for _, name := range c.actorList() {
		if _, ok := findActor(name); !ok {
			return fmt.Errorf("%w: %q", errUnknownActor, name)
		}
	}

	return nil
}

func (c config) actorList() []string {
	names := strings.Split(c.actors, ",")
	for i, name := range names {
		names[i] = strings.TrimSpace(name)
	}

	return names
}

// checkIsolated returns an error unless WR_CONFIG_DIR is set and wr's config
// for the deployment, as every client prodsim makes will load it, names the
// manager whose runtime dir is cfg.runDir and, if -web is given, that
// manager's web port.
func checkIsolated(ctx context.Context, cfg config) error {
	if os.Getenv("WR_CONFIG_DIR") == "" {
		return errNoConfigDir
	}

	wrCfg := internal.ConfigLoadFromCurrentDir(ctx, cfg.deployment)

	if filepath.Clean(wrCfg.ManagerDir) != filepath.Clean(cfg.runDir) {
		return fmt.Errorf("%w: %s config names managerdir %s, not -rundir %s", errNotIsolated,
			cfg.deployment, wrCfg.ManagerDir, cfg.runDir)
	}

	if cfg.webAddr == "" {
		return nil
	}

	if _, port, err := net.SplitHostPort(cfg.webAddr); err != nil || port != wrCfg.ManagerWeb {
		return fmt.Errorf("%w: -web %s is not the %s config's web port %s", errNotIsolated, cfg.webAddr,
			cfg.deployment, wrCfg.ManagerWeb)
	}

	return nil
}

// run starts every configured actor and waits for them all to finish, which
// they do when ctx ends.
func (s *sim) run(ctx context.Context) {
	s.event("main", fmt.Sprintf("start duration=%s simminute=%s scale=%.2f actors=%s", s.cfg.duration,
		s.cfg.simMinute, s.cfg.scale, s.cfg.actors))

	s.setLimits(ctx)

	var wg sync.WaitGroup

	for _, name := range s.cfg.actorList() {
		fn, _ := findActor(name)

		wg.Go(func() { fn(s, ctx) })
	}

	wg.Wait()
	s.event("main", fmt.Sprintf("end submitted=%d", s.submitted.Load()))
	s.close()
}

// newSim makes the output directory and files for a run of cfg.
func newSim(cfg config) (*sim, error) {
	if err := os.MkdirAll(filepath.Join(cfg.outDir, "profiles"), dirPerm); err != nil {
		return nil, err
	}

	s := &sim{cfg: cfg, rng: newRand(cfg.seed), start: time.Now()}

	for _, t := range []struct {
		w      **tsvWriter
		name   string
		header string
	}{
		{&s.calls, "calls.tsv", "t_s\tactor\top\tms\tn\terr"},
		{&s.events, "events.tsv", "t_s\tactor\tevent"},
		{&s.samples, "samples.tsv", sampleHeader},
	} {
		w, err := newTSV(filepath.Join(cfg.outDir, t.name), t.header)
		if err != nil {
			return nil, err
		}

		*t.w = w
	}

	return s, nil
}
