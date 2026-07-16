/*******************************************************************************
 * Copyright (c) 2016-2021, 2024, 2026 Genome Research Ltd.
 *
 * Author: Sendu Bala <sb10@sanger.ac.uk>
 * Author: [Theo Barber-Bany] <theobarberbany@gmail.com>
 * Author: Ashwini Chhipa <ac55@sanger.ac.uk>
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

package scheduler

// This file contains a scheduleri implementation for 'local': running jobs
// on the local machine directly. It has a very simple strictly fifo queue, so
// may not be very efficient with the machine's resources.

import (
	"context"
	"errors"
	"io"
	"math"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/VertebrateResequencing/wr/clog"
	"github.com/VertebrateResequencing/wr/internal"
	mth "github.com/VertebrateResequencing/wr/math"
	"github.com/VertebrateResequencing/wr/queue"
	logext "github.com/inconshreveable/log15/v3/ext"
	"github.com/shirou/gopsutil/v4/process"
)

const (
	localPlace          = "localhost"
	localReserveTimeout = 1
	priorityScaler      = float64(maxPriority) / float64(percentMultiplier)
	reserveChTimeout    = 30 * time.Second

	// opSchedule is the Op name used in scheduler Errors raised by schedule(),
	// and the reason given to processQueue() for a scheduling-triggered run.
	opSchedule = "schedule"

	// opInitialize is the Op name used in scheduler Errors raised by
	// initialize().
	opInitialize = "initialize"

	// errBadLocalConfig is the Error message used when initialize() is not given
	// a *ConfigLocal.
	errBadLocalConfig = "SchedulerConfig must be *ConfigLocal"

	// maxPriority is the highest queue size/priority value (a cmd needing 100%
	// of a resource), and percentMultiplier turns a 0-1 fraction into a
	// percentage.
	maxPriority       = 255
	percentMultiplier = 100

	// queueItemTTR is the time-to-release for queue items; it only has to be
	// long enough for processQueue() to process a job, not to run the cmds.
	queueItemTTR = 30 * time.Second

	// callIDLength is the length of the random call id tying together a
	// canCounter call and the resulting cmdRunner invocations.
	callIDLength = 8

	// pidCheckInterval is how often recover() polls a recovered pid to see if it
	// has exited.
	pidCheckInterval = 1 * time.Second

	// recoverProcessCacheTTL is the freshness window during which recover()
	// reuses a single process enumeration instead of enumerating again. Recovery
	// calls recover() once per running job in a tight startup loop (a single
	// "recovery pass"), so caching the enumeration for this window means N
	// running jobs cause 1 enumeration instead of N. It is kept short so that a
	// genuinely later, separate recovery re-enumerates; any value comfortably
	// longer than one pass's loop suffices, since a pass only happens once at
	// manager startup and per-pid liveness is tracked independently by
	// monitorRecoveredPid.
	recoverProcessCacheTTL = 5 * time.Second

	// reserveWaitTimeout bounds how long processQueue() waits for spawned
	// runners to reserve their resources before giving up.
	reserveWaitTimeout = 1 * time.Minute
)

// cmdProcessSanitiser is used to make cmds look like their process
// representation.
//
//nolint:gochecknoglobals // a shared, immutable lookup table
var cmdProcessSanitiser = strings.NewReplacer("'", "")

// reqCheckers are functions used by schedule() to see if it is at all possible
// to ever run a job with the given resource requirements. (We make use of this
// in the local struct so that other implementers of scheduleri can embed local,
// use local's schedule(), but have their own reqChecker implementation.)
type reqChecker func(ctx context.Context, req *Requirements) error

// maxResourceGetter are functions used by schedule() to see what the maximum of
// a resource like memory or time is. (We make use of this in the local
// struct so that other implementers of scheduleri can embed local, use local's
// schedule(), but have their own maxResourceGetter implementation.)
type maxResourceGetter func() int

// canCounters are functions used by processQueue() to see how many of a job
// can be run. (We make use of this in the local struct so that other
// implementers of scheduleri can embed local, use local's processQueue(), but
// have their own canCounter implementation.) The call argument will be a random
// string. That same string will also be supplied to the cmdRunner function, so
// you can tie together cmdRunner invocations that are all a result of a
// particular canCounter call.
type canCounter func(ctx context.Context, cmd string, req *Requirements, call string) (canCount int)

// cantHandlers are functions used during processQueue() that are called when
// the canCounter function returns less than the desired number of jobs. They
// represent an opportunity to try and increase available resources (eg. by
// creating new servers).
type cantHandler func(ctx context.Context, desired int, cmd string, req *Requirements, call string)

// stateUpdaters are functions used by processQueue() to update any global state
// that might have become invalid due to changes external to our own actions.
// (We make use of this in the local struct so that other implementers of
// scheduleri can embed local, use local's processQueue(), but have their own
// stateUpdater implementation.)
type stateUpdater func(ctx context.Context)

// cmdRunners are functions used by processQueue() to actually run cmds.
// (Their reason for being is the same as for canCounters.) The reservedCh
// should be sent true as soon as resources have been reserved to run the cmd,
// or sent false if something went wrong before that.
type cmdRunner func(ctx context.Context, cmd string, req *Requirements, reservedCh chan bool) error

// postProcessors are functions used by processQueue() to do something after
// a postProcess() call does work.
type postProcessor func(ctx context.Context)

// unneededCmdHandler are functions called when scheduling a cmd or completing
// the execution of a command, and we no longer need to run more of the cmd.
type unneededCmdHandler func(cmd string)

// local is our implementer of scheduleri.
type local struct {
	config            *ConfigLocal
	maxRAM            int
	maxCores          int
	ram               int
	zeroCores         int
	cores             float64
	rcount            int
	queue             *queue.Queue
	running           map[string]int
	reqCheckFunc      reqChecker
	maxMemFunc        maxResourceGetter
	maxCPUFunc        maxResourceGetter
	canCountFunc      canCounter
	cantFunc          cantHandler
	cmdNotNeededFunc  unneededCmdHandler
	postProcessFunc   postProcessor
	stateUpdateFunc   stateUpdater
	stateUpdateFreq   time.Duration
	runCmdFunc        cmdRunner
	stopAuto          chan bool
	recoveredPids     map[int]bool
	stopPidMonitoring chan struct{}
	processLister     func() ([]*process.Process, error)
	procCache         []*process.Process
	procCacheTime     time.Time
	cleanMutex        sync.RWMutex
	rcMutex           sync.RWMutex
	resourceMutex     sync.RWMutex
	runMutex          sync.RWMutex
	mutex             sync.Mutex
	rpMutex           sync.Mutex
	apMutex           sync.Mutex
	procCacheMutex    sync.Mutex
	cleaned           bool
	autoProcessing    bool
	processing        bool
	recall            bool
}

// ConfigLocal represents the configuration options required by the local
// scheduler. All are required with no usable defaults.
type ConfigLocal struct {
	// Shell is the shell to use to run your commands with; 'bash' is
	// recommended.
	Shell string

	// StateUpdateFrequency is the frequency at which to re-check the queue to
	// see if anything can now run. 0 (default) is treated as 1 minute.
	StateUpdateFrequency time.Duration

	// MaxCores is the maximum number of CPU cores on the machine to use for
	// running jobs. Specifying more cores than the machine has results in using
	// as many cores as the machine has, which is also the default. Values
	// below 1 are treated as default.
	MaxCores int

	// MaxRAM is the maximum amount of machine memory to use for running jobs.
	// The unit is in MB, and defaults to all available memory. Specifying more
	// than this uses the default amount. Values below 1 are treated as default.
	MaxRAM int
}

// jobs are what we store in our queue.
type job struct {
	cmd                string
	req                *Requirements
	priority           uint8
	count              int
	scheduleDecrements int
	sync.RWMutex
}

// itemJob returns the *job stored as the data of the given queue item. Our
// queue only ever holds *job, so a failed type assertion (yielding nil) cannot
// happen in practice.
func itemJob(item *queue.Item) *job {
	//nolint:errcheck // our queue only ever holds *job, so this cannot fail
	j, _ := item.Data().(*job)

	return j
}

// queueErrIs reports whether err is a queue.Error whose underlying Err matches
// target. It returns false for a nil err.
func queueErrIs(err, target error) bool {
	var qerr queue.Error
	if !errors.As(err, &qerr) {
		return false
	}

	return errors.Is(qerr.Err, target)
}

// initialize finds out about the local machine. Compatible with amd64 archs
// only!
func (s *local) initialize(ctx context.Context, config any) error {
	conf, ok := config.(*ConfigLocal)
	if !ok {
		return Error{localScheduler, opInitialize, errBadLocalConfig}
	}

	s.config = conf

	if err := s.detectResources(); err != nil {
		return err
	}

	// make our queue
	s.queue = queue.New(ctx, localPlace)
	s.running = make(map[string]int)

	s.setSchedulerFuncs()

	s.stateUpdateFreq = s.config.StateUpdateFrequency
	if s.stateUpdateFreq == 0 {
		s.stateUpdateFreq = 1 * time.Minute
	}

	s.recoveredPids = make(map[int]bool)
	s.stopPidMonitoring = make(chan struct{})
	s.processLister = process.Processes

	// stopAuto is created here and not in startAutoProcessing() to avoid data
	// races with concurrent stop and start invocations
	s.stopAuto = make(chan bool)

	return nil
}

// detectResources sets maxCores and maxRAM from the local machine, capped by
// any limits configured in s.config.
func (s *local) detectResources() error {
	s.maxCores = runtime.NumCPU()
	if s.config.MaxCores > 0 && s.config.MaxCores < s.maxCores {
		s.maxCores = max(s.config.MaxCores, 1)
	}

	maxRAM, err := internal.ProcMeminfoMBs()
	if err != nil {
		return err
	}

	s.maxRAM = maxRAM
	if s.config.MaxRAM > 0 && s.config.MaxRAM < s.maxRAM {
		s.maxRAM = max(s.config.MaxRAM, 1)
	}

	return nil
}

// setSchedulerFuncs wires up the pluggable functions used by schedule() and
// processQueue() to local's own implementations.
func (s *local) setSchedulerFuncs() {
	s.reqCheckFunc = s.reqCheck
	s.maxMemFunc = s.maxMem
	s.maxCPUFunc = s.maxCPU
	s.canCountFunc = s.canCount
	s.cantFunc = s.cant
	s.runCmdFunc = s.runCmd
	s.stateUpdateFunc = s.stateUpdate
	s.postProcessFunc = s.postProcess
	s.cmdNotNeededFunc = s.cmdNotNeeded
}

// reserveTimeout achieves the aims of ReserveTimeout().
func (s *local) reserveTimeout(ctx context.Context, req *Requirements) int {
	if val, defined := req.Other["rtimeout"]; defined {
		timeout, err := strconv.Atoi(val)
		if err != nil {
			clog.Error(ctx, "Failed to convert rtimeout to integer", "error", err)

			return localReserveTimeout
		}

		return timeout
	}

	return localReserveTimeout
}

// maxQueueTime achieves the aims of MaxQueueTime().
func (s *local) maxQueueTime(_ *Requirements) time.Duration {
	return infiniteQueueTime
}

// schedule achieves the aims of Schedule().
func (s *local) schedule(ctx context.Context, cmd string, req *Requirements, priority uint8, count int) error {
	if s.cleanedUp() {
		return nil
	}

	// first find out if its at all possible to ever run this cmd
	if count != 0 {
		err := s.reqCheckFunc(ctx, req)
		if err != nil {
			return err
		}
	} // else, just in case a job with these reqs somehow got through in the
	// past, allow it to be cancelled

	proceed, err := s.enqueue(ctx, cmd, req, priority, count)
	if err != nil || !proceed {
		return err
	}

	if count > 0 {
		s.startAutoProcessing(ctx)
	}

	// try and run the jobs in the queue
	return s.processQueue(ctx, opSchedule)
}

// enqueue adds (or updates) cmd in the queue and returns whether schedule()
// should go on to process the queue. It holds s.mutex for the add/update.
func (s *local) enqueue(
	ctx context.Context, cmd string, req *Requirements, priority uint8, count int,
) (bool, error) {
	size := s.cmdSize(req)

	key := jobName(cmd, "n/a", false)
	data := &job{
		cmd:      cmd,
		req:      req,
		priority: priority,
		count:    count,
	}

	s.mutex.Lock()
	if s.cleanedUp() {
		return false, nil
	}

	// the ttr just has to be long enough for processQueue() to process a job,
	// not actually run the cmds
	item, err := s.queue.AddWithSize(ctx, key, "", data, priority, size, 0*time.Second, queueItemTTR, "")

	proceed, err := s.applyScheduleResult(ctx, cmd, key, priority, count, size, item, err)
	s.mutex.Unlock()

	return proceed, err
}

// cmdSize calculates the queue "size" (which doubles as a tie-breaking priority
// for equal user priorities) for a cmd with the given requirements. The size is
// based on the max of the percentage of available memory it needs and the
// percentage of cpus it needs: a cmd that needs 100% of memory or cpu is our
// highest priority command (size 255), while one that needs 0% of resources is
// size 0.
func (s *local) cmdSize(req *Requirements) uint8 {
	maxMem := s.maxMemFunc()
	maxCPU := s.maxCPUFunc()
	percentMemNeeded := (float64(req.RAM) / float64(maxMem)) * float64(percentMultiplier)
	percentCPUNeeded := (req.Cores / float64(maxCPU)) * float64(percentMultiplier)

	percentMachineNeeded := percentMemNeeded
	if percentCPUNeeded > percentMachineNeeded {
		percentMachineNeeded = percentCPUNeeded
	}

	return uint8(math.Round(priorityScaler * percentMachineNeeded))
}

// applyScheduleResult handles the outcome of the schedule() AddWithSize call. It
// must be called while holding s.mutex. If the cmd was newly added it just logs;
// if it already existed it updates the existing job's count and priority. It
// returns whether schedule() should proceed to start processing the queue.
func (s *local) applyScheduleResult(
	ctx context.Context, cmd, key string, priority uint8, count int, size uint8,
	item *queue.Item, addErr error,
) (bool, error) {
	if addErr == nil {
		clog.Debug(ctx, "schedule added new cmd", "cmd", cmd, "needs", count, "size", size, "priority", priority)

		return true, nil
	}

	if !queueErrIs(addErr, queue.ErrAlreadyExists) {
		return false, addErr
	}

	return s.updateExistingJob(ctx, cmd, key, priority, count, item), nil
}

// updateExistingJob updates the count and priority of a job that was already in
// the queue, and tidies up if it's no longer needed. It must be called while
// holding s.mutex, and returns whether schedule() should proceed to process the
// queue.
func (s *local) updateExistingJob(
	ctx context.Context, cmd, key string, priority uint8, count int, item *queue.Item,
) bool {
	j := itemJob(item)
	before, running := s.updateJobCountAndPriority(ctx, cmd, key, priority, count, j)

	if count != before {
		clog.Debug(ctx, "schedule changed number needed", "cmd", cmd, "before", before, "needs", count)
	}

	if count == 0 {
		s.removeKey(ctx, key)
		clog.Debug(ctx, "schedule removed cmd", "cmd", cmd)
	}

	// if we don't need to run any more, bypass a pointless processQueue call
	return s.checkNeeded(ctx, cmd, key, count, running)
}

// updateJobCountAndPriority sets j's count (and scheduleDecrements relative to
// how many are running) and updates its queue priority, all while holding j's
// lock. It returns j's previous count and the number currently running.
func (s *local) updateJobCountAndPriority(
	ctx context.Context, cmd, key string, priority uint8, count int, j *job,
) (before, running int) {
	j.Lock()
	defer j.Unlock()

	s.runMutex.RLock()
	running = s.running[key]
	s.runMutex.RUnlock()

	before = j.count

	j.count = count
	if count < running {
		j.scheduleDecrements = running - count
	} else {
		j.scheduleDecrements = 0
	}

	s.updateJobPriority(ctx, cmd, key, priority, j)

	return before, running
}

// updateJobPriority updates the queue item priority for j if it has changed. It
// must be called while holding j's lock.
func (s *local) updateJobPriority(ctx context.Context, cmd, key string, priority uint8, j *job) {
	if j.priority == priority {
		return
	}

	err := s.queue.Update(ctx, key, "", j, priority, 0*time.Second, queueItemTTR)
	if err != nil {
		clog.Error(ctx, "failed to update priority", "cmd", cmd, "err", err)

		return
	}

	clog.Debug(ctx, "schedule changed priority", "cmd", cmd, "before", j.priority, "now", priority)
	j.priority = priority
}

// scheduled achieves the aims of Scheduled().
func (s *local) scheduled(_ context.Context, cmd string) (int, error) {
	if s.cleanedUp() {
		return 0, nil
	}

	s.rcMutex.RLock()
	defer s.rcMutex.RUnlock()

	if s.queue.Stats().Items == 0 && s.rcount <= 0 {
		return 0, nil
	}

	return s.jobCount(jobName(cmd, "n/a", false))
}

// jobCount returns the count stored on the job with the given key. A missing
// key (or nil item) yields a count of 0 and no error; only an unexpected queue
// error is returned.
func (s *local) jobCount(key string) (int, error) {
	item, err := s.queue.Get(key)
	if err != nil {
		if !queueErrIs(err, queue.ErrNotFound) {
			return 0, err
		}

		return 0, nil
	}

	if item == nil {
		return 0, nil
	}

	j := itemJob(item)
	j.RLock()
	count := j.count
	j.RUnlock()

	return count, nil
}

// checkNeeded takes a cmd, item key, current item.Count and number of cmd
// currently running. If we do not need to run any more of this cmd, calls
// cmdNotNeededFunc(cmd).
func (s *local) checkNeeded(ctx context.Context, cmd, key string, needed, running int) bool {
	if needed <= running {
		clog.Debug(ctx, "checkNeeded not needed", "cmd", cmd, "key", key, "needed", needed, "running", running)
		s.cmdNotNeededFunc(cmd)

		return false
	}

	return true
}

// cmdCountRemaining tells you the count of cmd still needed based on what was
// supplied to schedule(), and how many we've already finished running or are
// currently running. Returns 0 if the cmd isn't known about.
func (s *local) cmdCountRemaining(cmd string) int {
	key := jobName(cmd, "n/a", false)

	item, err := s.queue.Get(key)
	if err != nil || item == nil {
		return 0
	}

	j := itemJob(item)
	j.RLock()
	count := j.count
	j.RUnlock()

	s.runMutex.RLock()
	running := s.running[key]
	s.runMutex.RUnlock()

	return count - running
}

// recover achieves the aims of Recover(). Here we find an untracked pid
// corresponding to the given cmd, note that the resources are in use, and
// start tracking the pid to know when it exits to release those resources.
func (s *local) recover(ctx context.Context, cmd string, req *Requirements, _ *RecoveredHostDetails) error {
	processes, err := s.enumerateProcesses()
	if err != nil {
		return err
	}

	cmd = cmdProcessSanitiser.Replace(cmd)

	s.rpMutex.Lock()
	defer s.rpMutex.Unlock()

	for _, p := range processes {
		thisCmd, err := p.Cmdline()
		if err != nil {
			// likely the process stopped existing between the call to
			// Processes() and now, just ignore this one
			continue
		}

		if cmd != thisCmd {
			continue
		}

		if s.recoverPid(ctx, int(p.Pid), req) {
			break
		}
	}

	return nil
}

// enumerateProcesses returns the current list of processes, reusing a cached
// enumeration if it is younger than recoverProcessCacheTTL. Because recover()
// is called once per running job during a single recovery pass, this ensures N
// running jobs cause 1 process enumeration rather than N. The cache is guarded
// by procCacheMutex since recover() may be called concurrently. On enumeration
// error the cache is left untouched so the next call retries.
func (s *local) enumerateProcesses() ([]*process.Process, error) {
	s.procCacheMutex.Lock()
	defer s.procCacheMutex.Unlock()

	if s.procCache != nil && time.Since(s.procCacheTime) < recoverProcessCacheTTL {
		return s.procCache, nil
	}

	processes, err := s.processLister()
	if err != nil {
		return nil, err
	}

	s.procCache = processes
	s.procCacheTime = time.Now()

	return processes, nil
}

// recoverPid notes that the given pid is using the given req's resources, and
// starts a goroutine that releases them when the pid exits. It must be called
// while holding rpMutex. It returns false if the pid was already being tracked
// (so the caller should keep looking for another process), or true once it has
// started tracking the pid.
func (s *local) recoverPid(ctx context.Context, pid int, req *Requirements) bool {
	if s.recoveredPids[pid] {
		return false
	}

	s.addResources(req)

	go s.monitorRecoveredPid(ctx, pid, req)

	s.recoveredPids[pid] = true

	return true
}

// monitorRecoveredPid periodically checks on a recovered pid; once it has
// exited (or pid monitoring is stopped), it releases the pid's resources and
// re-runs the queue.
func (s *local) monitorRecoveredPid(ctx context.Context, pid int, req *Requirements) {
	defer internal.LogPanic(ctx, "recover", true)

	ticker := time.NewTicker(pidCheckInterval)

	for {
		select {
		case <-ticker.C:
			if pidAlive(pid) {
				continue
			}

			ticker.Stop()
			s.releaseResources(req)

			errp := s.processQueue(ctx, "recover")
			if errp != nil {
				clog.Error(ctx, "processQueue call after recovery failed", "err", errp)
			}

			return
		case <-s.stopPidMonitoring:
			ticker.Stop()

			return
		}
	}
}

// pidAlive reports whether the process with the given pid still exists.
func pidAlive(pid int) bool {
	process, err := os.FindProcess(pid)
	if err != nil {
		return false
	}

	return process.Signal(syscall.Signal(0)) == nil
}

// reqCheck gives an ErrImpossible if the given Requirements can not be met.
func (s *local) reqCheck(_ context.Context, req *Requirements) error {
	if req.RAM > s.maxRAM || int(math.Ceil(req.Cores)) > s.maxCores {
		return Error{localScheduler, opSchedule, ErrImpossible}
	}

	return nil
}

// maxMem returns the maximum memory available on the machine in MB.
func (s *local) maxMem() int {
	return s.maxRAM
}

// maxCPU returns the total number of CPU cores available on the machine.
func (s *local) maxCPU() int {
	return s.maxCores
}

// addResources records that a cmd with the given requirements is now using
// resources, locking resourceMutex while it does so.
func (s *local) addResources(req *Requirements) {
	s.resourceMutex.Lock()
	defer s.resourceMutex.Unlock()

	s.addResourcesLocked(req)
}

// addResourcesLocked is like addResources, but the caller must already hold
// resourceMutex.
func (s *local) addResourcesLocked(req *Requirements) {
	s.ram += req.RAM
	if req.Cores == 0 {
		s.zeroCores++
	} else {
		s.cores += req.Cores
	}
}

// releaseResources records that a cmd with the given requirements has stopped
// using resources, locking resourceMutex while it does so.
func (s *local) releaseResources(req *Requirements) {
	s.resourceMutex.Lock()
	defer s.resourceMutex.Unlock()

	s.ram -= req.RAM
	if req.Cores == 0 {
		s.zeroCores--
	} else {
		s.cores -= req.Cores
	}
}

// removeKey removes a key from the queue, for when there are no more jobs for
// that key. If this results in an empty queue, stops autoProcessing.
func (s *local) removeKey(ctx context.Context, key string) {
	err := s.queue.Remove(ctx, key)
	if queueErrIs(err, queue.ErrQueueClosed) {
		return
	}

	// warn unless we've already removed this key
	if err != nil && !queueErrIs(err, queue.ErrNotFound) {
		clog.Warn(ctx, "processQueue item removal failed", "err", err)
	}

	if s.queue.Stats().Items == 0 {
		s.stopAutoProcessing()
	}
}

// processQueue goes through the jobs in the queue by size, sees if it's
// possible to run any, does so if it is, otherwise returns the jobs to the
// queue.
func (s *local) processQueue(ctx context.Context, reason string) error {
	if !s.startProcessing(ctx, reason) {
		return nil
	}

	// now perform any global state update needed by the scheduler
	s.stateUpdateFunc(ctx)

	stats := s.queue.Stats()

	toRelease := make([]string, 0, stats.Items)
	defer func() {
		s.releaseQueueItems(ctx, toRelease)
		s.postProcessFunc(ctx)
		s.finishProcessing(ctx)
		clog.Debug(ctx, "processQueue ending")
	}()

	// go through the jobs largest to smallest (standard bin packing approach)
	for {
		item, err := s.queue.Reserve("", 0)
		if err != nil {
			if queueErrIs(err, queue.ErrNothingReady) || queueErrIs(err, queue.ErrQueueClosed) {
				return nil
			}

			return err
		}

		s.processQueueItem(ctx, item, &toRelease)
	}
}

// startProcessing implements the "only process the queue once at a time" guard
// at the top of processQueue(). Other calls return immediately but cause us to
// recall ourselves once the in-progress run completes. It returns true if the
// caller should go on to process the queue.
func (s *local) startProcessing(ctx context.Context, reason string) bool {
	s.mutex.Lock()
	defer s.mutex.Unlock()

	if s.cleanedUp() {
		return false
	}

	if s.processing {
		s.recall = true

		return false
	}

	s.processing = true

	clog.Debug(ctx, "processQueue starting", "reason", reason)

	return true
}

// releaseQueueItems releases the queue items reserved during processQueue(),
// ignoring not-found and queue-closed errors.
func (s *local) releaseQueueItems(ctx context.Context, toRelease []string) {
	for _, key := range toRelease {
		errr := s.queue.Release(ctx, key)
		if errr != nil && !queueErrIs(errr, queue.ErrNotFound) && !queueErrIs(errr, queue.ErrQueueClosed) {
			clog.Warn(ctx, "processQueue item release failed", "err", errr)
		}
	}
}

// finishProcessing marks processing as complete and, if a processQueue() call
// came in while we were busy, kicks off a recall in the background.
func (s *local) finishProcessing(ctx context.Context) {
	s.mutex.Lock()
	s.processing = false
	recall := s.recall
	s.recall = false
	s.mutex.Unlock()

	if !recall {
		return
	}

	go func() {
		defer internal.LogPanic(ctx, "processQueue recall", true)

		errp := s.processQueue(ctx, "recall")
		if errp != nil {
			clog.Warn(ctx, "processQueue recall failed", "err", errp)
		}
	}()
}

// processQueueItem tries to run as many of a single reserved queue item's cmd as
// there is capacity for, appending the item's key to toRelease when it should be
// returned to the queue afterwards.
func (s *local) processQueueItem(ctx context.Context, item *queue.Item, toRelease *[]string) {
	reserved, canCount := s.binPackItem(ctx, item, toRelease)
	if reserved == nil {
		return
	}

	clog.Debug(ctx, "processQueue runCmdFunc loop complete")
	s.waitForReservations(ctx, reserved, canCount)
}

// binPackItem makes the bin-packing decision for a single reserved queue item
// while holding runMutex and the job's read lock, launching runners for as much
// of the item's cmd as there is capacity for. It returns the channel those
// runners signal and how many were launched, or a nil channel if nothing was
// started. It appends the item's key to toRelease when the item should be
// returned to the queue afterwards.
func (s *local) binPackItem(ctx context.Context, item *queue.Item, toRelease *[]string) (chan bool, int) {
	key := item.Key
	j := itemJob(item)

	j.RLock()
	defer j.RUnlock()

	s.runMutex.Lock()
	defer s.runMutex.Unlock()

	cmd := j.cmd
	req := j.req
	count := j.count
	running := s.running[key]
	clog.Debug(ctx, "processQueue binpacking", "needs", count, "current", running, "cmd", cmd)

	if count == 0 && running == 0 {
		// a cancellation has come in, and somehow we didn't remove this from the
		// queue; do so now
		clog.Debug(ctx, "processQueue cancelling", "cmd", cmd)
		s.removeKey(ctx, key)

		return nil, 0
	}

	*toRelease = append(*toRelease, key)

	return s.launchAvailable(ctx, j, key, cmd, req, count, running)
}

// launchAvailable launches runners for as many of cmd as there is both demand
// (count beyond what is running) and capacity for, returning the channel they
// signal and how many were launched, or a nil channel if none were. It must be
// called while holding runMutex and j's read lock.
func (s *local) launchAvailable(
	ctx context.Context, j *job, key, cmd string, req *Requirements, count, running int,
) (chan bool, int) {
	shouldCount := count - running
	if shouldCount <= 0 {
		// we're already running everything for this job, try the next largest
		// cmd
		return nil, 0
	}

	// now see if there's remaining capacity to run the job
	call := logext.RandId(callIDLength)
	ctx = clog.ContextWithCallValue(ctx, call)

	canCount := s.capacityFor(ctx, cmd, req, call, shouldCount)
	if canCount <= 0 {
		// try and fill any "gaps" (spare memory/ cpu) by seeing if a cmd with
		// lesser resource requirements can be run
		return nil, 0
	}

	// start running what we can
	clog.Debug(ctx, "processQueue runCmdFunc", "count", canCount)

	return s.launchRunners(ctx, j, key, cmd, req, count, call, canCount), canCount
}

// capacityFor determines how many of cmd can be started right now, capped at
// shouldCount. If fewer than shouldCount can run, it gives cantFunc a chance to
// free up more resources. It must be called while holding runMutex.
func (s *local) capacityFor(ctx context.Context, cmd string, req *Requirements, call string, shouldCount int) int {
	canCount := s.canCountFunc(ctx, cmd, req, call)
	clog.Debug(ctx, "processQueue canCount", "can", canCount, "should", shouldCount)

	if canCount > shouldCount {
		canCount = shouldCount
	}

	if canCount < shouldCount {
		s.cantFunc(ctx, shouldCount-canCount, cmd, req, call)
	}

	return canCount
}

// launchRunners starts canCount goroutines that each run cmd, returning the
// channel they signal once their resources are reserved. It must be called
// while holding runMutex and j's read lock.
func (s *local) launchRunners(
	ctx context.Context, j *job, key, cmd string, req *Requirements, count int, call string, canCount int,
) chan bool {
	reserved := make(chan bool, canCount)

	for range canCount {
		s.running[key]++
		s.checkNeeded(ctx, cmd, key, count, s.running[key])

		go func() {
			defer internal.LogPanic(ctx, "processQueue runCmd loop", true)

			s.runScheduledCmd(ctx, j, key, cmd, req, call, reserved)
		}()
	}

	return reserved
}

// runScheduledCmd runs a single instance of cmd, then updates the running and
// remaining counts, removing the queue key if nothing more is needed, and
// finally recalls processQueue().
func (s *local) runScheduledCmd(
	ctx context.Context, j *job, key, cmd string, req *Requirements, call string, reserved chan bool,
) {
	clog.Debug(ctx, "will run cmd", "cmd", cmd, "call", call)
	err := s.runCmdFunc(ctx, cmd, req, reserved)
	clog.Debug(ctx, "ran cmd", "cmd", cmd, "call", call)

	s.recordRunFinished(ctx, j, key, err == nil)

	if err != nil {
		// users are notified of relevant errors during runCmd; here we just
		// debug log everything
		clog.Debug(ctx, "runCmd error", "err", err)
	}

	err = s.processQueue(ctx, "after runCmd")
	if err != nil {
		clog.Error(ctx, "processQueue recall failed", "err", err)
	}
}

// recordRunFinished updates the running count for key now that one of its
// runners has finished, and (if the run succeeded) decrements the remaining
// count for j, removing the queue key once nothing more is needed. The success
// argument is whether the cmd ran without a start error.
func (s *local) recordRunFinished(ctx context.Context, j *job, key string, success bool) {
	j.Lock()
	defer j.Unlock()

	s.runMutex.Lock()
	defer s.runMutex.Unlock()

	s.running[key]--
	if s.running[key] <= 0 {
		delete(s.running, key)
	}

	if !success {
		return
	}

	// decrement j.count here if we didn't already decrement it during a
	// schedule() call
	if j.scheduleDecrements > 0 {
		j.scheduleDecrements--
	} else {
		j.count--
	}

	if j.count <= 0 {
		s.removeKey(ctx, key)
	}
}

// waitForReservations waits for all canCount runners launched for an item to
// signal that they have reserved their resources, so subsequent canCountFunc
// calls are accurate. It bounds the wait so a failed send can't get us stuck.
func (s *local) waitForReservations(ctx context.Context, reserved chan bool, canCount int) {
	ch := make(chan bool, 1)
	done := make(chan bool, 1)

	go func() {
		for range canCount {
			<-reserved
		}

		done <- true

		ch <- true
	}()
	go func() {
		select {
		case <-time.After(reserveWaitTimeout):
			ch <- false
		case <-done:
			return
		}
	}()

	sentAll := <-ch
	if !sentAll {
		clog.Warn(ctx, "processQueue failed to reserve all resources")
	}
}

// canCount tells you how many jobs with the given RAM and core requirements it
// is possible to run, given remaining resources.
func (s *local) canCount(ctx context.Context, _ string, req *Requirements, _ string) int {
	s.resourceMutex.RLock()
	defer s.resourceMutex.RUnlock()

	// we don't do any actual checking of current resources on the machine, but
	// instead rely on our simple tracking based on how many cores and RAM prior
	// cmds were /supposed/ to use. This could be bad for misbehaving cmds that
	// use too much RAM, but we will end up killing cmds that do this, so it
	// shouldn't be too much of an issue.
	canCount := int(math.Floor(float64(s.maxRAM-s.ram) / float64(req.RAM)))
	if canCount < 0 {
		clog.Warn(ctx, "negative canCount", "can", canCount, "maxRam", s.maxRAM, "ram", s.ram, "reqRam", req.RAM)
		canCount = 0
	}

	if canCount < 1 {
		return canCount
	}

	canCount2 := s.canCountByCores(req)
	if canCount2 >= canCount {
		return canCount
	}

	if canCount2 < 0 {
		clog.Warn(ctx, "negative canCount", "can", canCount2, "maxCores", s.maxCores, "cores",
			s.cores, "zeroCores", s.zeroCores, "reqCores", req.Cores)

		return 0
	}

	return canCount2
}

// canCountByCores returns how many cmds with the given core requirement can run
// based on remaining cpu capacity. It must be called while holding
// resourceMutex.
func (s *local) canCountByCores(req *Requirements) int {
	if req.Cores == 0 {
		// rather than allow an infinite or very large number of cmds to run on
		// this machine, because there are still real limits on the number of
		// processes we can run at once before things start falling over, we only
		// allow double the actual core count of zero core things to run (on top
		// of up to actual core count of non-zero core things)
		return s.maxCores*internal.ZeroCoreMultiplier - s.zeroCores
	}

	return int(math.Floor(mth.FloatSubtract(float64(s.maxCores), s.cores) / req.Cores))
}

// cant is our cantFunc, which in the local case does nothing, since we can't
// increase available resources.
func (s *local) cant(_ context.Context, _ int, _ string, _ *Requirements, _ string) {}

// runCmd runs the command, kills it if it goes much over RAM or time limits.
// NB: we only return an error if we can't start the cmd, not if the command
// fails (schedule() only guarantees that the cmds are run count times, not that
// they run /successful/ that many times).
func (s *local) runCmd(ctx context.Context, cmd string, req *Requirements, reservedCh chan bool) error {
	// we deliberately do not use exec.CommandContext here: a running job must
	// not be killed just because the scheduling context that triggered it is
	// cancelled; jobs are meant to run to completion.
	//
	//nolint:gosec,noctx // arbitrary scheduled cmd, intentionally detached from the scheduling ctx
	ec := exec.Command(s.config.Shell, "-c", cmd)
	ec.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	err := ec.Start()
	if err != nil {
		clog.Error(ctx, "runCmd start", "cmd", cmd, "err", err)
		s.sendReserved(ctx, reservedCh, false)

		return err
	}

	s.incRunCount()

	s.resourceMutex.Lock()
	s.addResourcesLocked(req)
	s.sendReserved(ctx, reservedCh, true)
	s.resourceMutex.Unlock()

	// *** set up monitoring of RAM and time usage and kill if >> than
	// req.RAM or req.Time

	err = ec.Wait()
	if err != nil {
		clog.Error(ctx, "runCmd wait", "cmd", cmd, "err", err)
	}

	s.decRunCount()
	s.releaseResources(req)

	return nil // do not return error running the command
}

// sendReserved sends v on reservedCh, which tells processQueue() that runCmd has
// reserved (or failed to reserve) its resources. reservedCh is buffered and
// sending on it should never block, but as we have somehow gotten stuck here
// before, we bound the send so we cannot get stuck.
func (s *local) sendReserved(ctx context.Context, reservedCh chan bool, v bool) {
	ch := make(chan bool, 1)
	done := make(chan bool, 1)

	go func() {
		reservedCh <- v

		done <- true

		ch <- true
	}()
	go func() {
		select {
		case <-time.After(reserveChTimeout):
			ch <- false
		case <-done:
			return
		}
	}()

	sentReserved := <-ch
	if !sentReserved {
		clog.Warn(ctx, "failed to send on reservedCh")
	}
}

// incRunCount increments the count of currently running cmds.
func (s *local) incRunCount() {
	s.rcMutex.Lock()
	defer s.rcMutex.Unlock()

	s.rcount++
}

// decRunCount decrements the count of currently running cmds, flooring it at 0.
func (s *local) decRunCount() {
	s.rcMutex.Lock()
	defer s.rcMutex.Unlock()

	s.rcount--
	if s.rcount < 0 {
		s.rcount = 0
	}
}

// stateUpdate in the local scheduler is a no-op, since there currently isn't
// any state out of our control we worry about.
func (s *local) stateUpdate(_ context.Context) {}

// postProcess in the local scheduler is a no-op, since there currently isn't
// anything that needs to be done after a postProcess() call.
func (s *local) postProcess(_ context.Context) {}

// cmdNotNeeded in the local scheduler is a no-op, since there currently isn't
// anything that needs to be done when a cmd is no longer needed.
func (s *local) cmdNotNeeded(_ string) {}

// startAutoProcessing begins periodic running of processQueue(). Normally
// processQueue is only called when cmds are added or complete. Calling it
// periodically as well means we are responsive to external events freeing up
// resources.
func (s *local) startAutoProcessing(ctx context.Context) {
	s.mutex.Lock()
	defer s.mutex.Unlock()

	if s.cleanedUp() {
		return
	}

	s.apMutex.Lock()
	defer s.apMutex.Unlock()

	if s.autoProcessing {
		return
	}

	go s.autoProcessLoop(ctx)

	s.autoProcessing = true
}

// autoProcessLoop periodically calls processQueue() until stopAutoProcessing()
// signals it to stop.
func (s *local) autoProcessLoop(ctx context.Context) {
	defer internal.LogPanic(ctx, "auto processQueue", false)

	ticker := time.NewTicker(s.stateUpdateFreq)

	for {
		select {
		case <-ticker.C:
			// processQueue can end up calling stopAutoProcessing which will wait
			// on the read of stopAuto below, but we won't read it until this case
			// completes, so call processQueue in a go routine to complete the
			// case ~instantly
			go func() {
				err := s.processQueue(ctx, "auto")
				if err != nil {
					clog.Error(ctx, "Automated processQueue call failed", "err", err)
				}
			}()

			continue
		case <-s.stopAuto:
			ticker.Stop()

			return
		}
	}
}

// stopAutoProcessing turns off the periodic processQueue() calls initiated by
// startAutoProcessing().
func (s *local) stopAutoProcessing() {
	s.apMutex.Lock()
	defer s.apMutex.Unlock()

	if !s.autoProcessing {
		return
	}

	s.stopAuto <- true

	s.autoProcessing = false
}

// busy returns true if there's anything in our queue or we are still running
// any cmd.
func (s *local) busy(_ context.Context) bool {
	if s.cleanedUp() {
		return false
	}

	s.rcMutex.RLock()
	defer s.rcMutex.RUnlock()

	if s.queue.Stats().Items == 0 && s.rcount <= 0 {
		return false
	}

	return true
}

// hostToID always returns an empty string, since we're not in the cloud.
func (s *local) hostToID(_ string) string {
	return ""
}

// localHost implements the Host interface.
type localHost struct {
	shell string
}

// RunCmd runs the given command on localhost, optionally in the background.
// You get the command's STDOUT and STDERR as strings.
func (l *localHost) RunCmd(ctx context.Context, cmd string, background bool) (stdout, stderr string, err error) {
	done := make(chan error, 1)
	outCh := make(chan string, 1)
	errCh := make(chan string, 1)

	go func() {
		defer internal.LogPanic(ctx, "localHost RunCmd", false)

		so, se, errc := l.execCmd(ctx, cmd, background)
		if errc != nil {
			done <- errc

			return
		}

		outCh <- so

		errCh <- se

		done <- nil
	}()

	err = <-done
	if err == nil {
		stdout = <-outCh
		stderr = <-errCh
	}

	return stdout, stderr, err
}

// execCmd synchronously runs cmd via the host's shell (wrapping it to detach if
// background is true) and returns its stdout and stderr as strings.
func (l *localHost) execCmd(ctx context.Context, cmd string, background bool) (stdout, stderr string, err error) {
	if background {
		cmd = "sh -c 'nohup " + cmd + " > /dev/null 2>&1 &'"
	}

	ec := exec.CommandContext(ctx, l.shell, "-c", cmd) // #nosec

	stdoutp, err := ec.StdoutPipe()
	if err != nil {
		return "", "", err
	}

	stderrp, err := ec.StderrPipe()
	if err != nil {
		return "", "", err
	}

	if err = ec.Start(); err != nil {
		return "", "", err
	}

	outBytes, erro := io.ReadAll(stdoutp)
	errBytes, erre := io.ReadAll(stderrp)

	if err = ec.Wait(); err != nil {
		return "", "", err
	}

	return pipeString(outBytes, erro), pipeString(errBytes, erre), nil
}

// pipeString returns the bytes read from a command's output pipe as a string,
// or an empty string if reading errored or there was no output.
func pipeString(data []byte, readErr error) string {
	if readErr != nil || len(data) == 0 {
		return ""
	}

	return string(data)
}

// getHost returns an implementation of the Host interface that can be used
// to run commands on localhost.
func (s *local) getHost(_ string) (Host, bool) {
	return &localHost{shell: s.config.Shell}, true
}

// setMessageCallBack does nothing at the moment, since we don't generate any
// messages for the user.
func (s *local) setMessageCallBack(_ context.Context, _ MessageCallBack) {}

// setBadServerCallBack does nothing, since we're not a cloud-based scheduler.
func (s *local) setBadServerCallBack(_ context.Context, _ BadServerCallBack) {}

// cleanup destroys our internal queue.
func (s *local) cleanup(ctx context.Context) {
	s.mutex.Lock()
	defer s.mutex.Unlock()

	s.runMutex.Lock()
	defer s.runMutex.Unlock()

	s.stopAutoProcessing()
	s.cleanMutex.Lock()
	defer s.cleanMutex.Unlock()

	close(s.stopPidMonitoring)
	s.cleaned = true

	err := s.queue.Destroy()
	if err != nil {
		clog.Warn(ctx, "local scheduler cleanup failed", "err", err)
	}
}

// cleanedUp returns true if cleanup() has been called.
func (s *local) cleanedUp() bool {
	s.cleanMutex.RLock()
	defer s.cleanMutex.RUnlock()

	return s.cleaned
}
