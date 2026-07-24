/*******************************************************************************
 * Copyright (c) 2016-2022, 2024, 2026 Genome Research Ltd.
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

/*
Package scheduler lets the jobqueue server interact with the configured job
scheduler (if any) to submit jobqueue runner clients and have them run on a
compute cluster (or local machine).

Currently implemented schedulers are local, LSF, and OpenStack. The
implementation of each supported scheduler type is in its own .go file.

It's a pseudo plug-in system in that it is designed so that you can easily add a
go file that implements the methods of the scheduleri interface, to support a
new job scheduler. On the other hand, there is no dynamic loading of these go
files; they are all imported (they all belong to the scheduler package), and the
correct one used at run time. To "register" a new scheduleri implementation you
must add a case for it to New() and rebuild.

	import "github.com/VertebrateResequencing/wr/jobqueue/scheduler"
	s, err := scheduler.New("local", &scheduler.ConfigLocal{"bash"})
	req := &scheduler.Requirements{RAM: 300, Time: 2 * time.Hour, Cores: 1}
	err = s.Schedule("myWRRunnerClient -args", req, 24)
	// wait, and when s.Busy() returns false, your command has been run 24 times
*/
package scheduler

import (
	"context"
	"crypto/md5" // #nosec - not used for cryptographic purposes here
	"fmt"
	"maps"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/VertebrateResequencing/wr/clog"
	"github.com/VertebrateResequencing/wr/cloud"
	"github.com/VertebrateResequencing/wr/internal"
	"github.com/dgryski/go-farm"
)

const (
	defaultReserveTimeout               = 1 // implementers of reserveTimeout can just return this
	infiniteQueueTime     time.Duration = 0
	minimumQueueTime      time.Duration = 1 * time.Minute
)

// Scheduler name constants, used both as the name passed to New() and as the
// Scheduler in returned Errors.
const (
	localScheduler     = "local"
	lsfScheduler       = "lsf"
	openstackScheduler = "openstack"
)

// Err* constants are found in the returned Errors under err.Err, so you can
// cast and check if it's a certain type of error.
const (
	ErrBadScheduler = "unknown scheduler name"
	ErrImpossible   = "scheduler cannot accept the job, since its resource requirements are too high"
	ErrBadFlavor    = "unknown server flavor"
)

// cannotConfirmWarnInterval rate-limits the warnCannotConfirm log so a persistent
// misconfiguration cannot flood the manager log while still remaining visible.
const cannotConfirmWarnInterval = time.Minute

// loggableProcessOutputMax bounds how many characters of unexpected remote ps
// output warnCannotConfirm will log. It keeps a misbehaving or verbose forced
// command (see ProcessNotRunningOnHost's CONTRACT WARNING) that emits many lines or
// a large banner from blowing up the manager log.
const loggableProcessOutputMax = 120

// processLiveness is the outcome of interpreting a host's `ps` output for a pid.
type processLiveness int

const (
	processDead    processLiveness = iota // no such process, or a zombie
	processAlive                          // a recognised, live process state
	processUnknown                        // output we cannot interpret
)

// Reserved records that a scheduler element (opaque, scheduler-specific id,
// e.g. LSF "jobid[index]") has been handed a wr job reservation, so it must not
// be killed as excess. Non-LSF schedulers ignore it.
func (s *Scheduler) Reserved(schedulerID string) {
	s.impl.reserved(schedulerID)
}

// interpretProcessState maps the trimmed stdout of the remote
// `ps -o stat= -p <pid>` command (see ProcessNotRunningOnHost's CONTRACT WARNING)
// to a liveness outcome: empty output (no such process) or a zombie ("Z...") is
// dead; any other recognised process-state code is alive; anything else is
// unknown (eg. a misconfigured forced command emitting a line count rather than a
// bare stat), which the caller must NOT treat as a confident alive/dead answer.
func interpretProcessState(state string) processLiveness {
	switch {
	case state == "" || strings.HasPrefix(state, "Z"):
		return processDead
	case isProcessState(state):
		return processAlive
	default:
		return processUnknown
	}
}

// warnCannotConfirm logs, at most once per cannotConfirmWarnInterval, that
// ProcessNotRunningOnHost could not determine whether pid on hostName is alive or
// dead, so a lost job's death cannot be confirmed (and its limit-group slot cannot
// be reclaimed). This makes a broken confirmation path - a bad ssh key, an
// unreachable host, or a forced command whose output no longer matches the parse
// contract - diagnosable instead of silently masquerading as a healthy manager.
func (s *Scheduler) warnCannotConfirm(ctx context.Context, hostName string, pid int, reason string) {
	now := time.Now().UnixNano()

	last := s.lastCannotConfirm.Load()
	if last != 0 && now-last < int64(cannotConfirmWarnInterval) {
		return
	}

	if !s.lastCannotConfirm.CompareAndSwap(last, now) {
		return
	}

	clog.Warn(ctx, "could not confirm whether a lost job's process is still running on its host",
		"host", hostName, "pid", pid, "reason", reason)
}

// loggableProcessOutput returns a short, single-line, length-capped excerpt of a
// remote command's output that is safe to put in a log field: only its first line,
// truncated to loggableProcessOutputMax characters, with a trailing "..." marker
// whenever anything (a longer first line, or any further lines) was dropped. The
// length cap is applied on a rune boundary so a multi-byte rune is never split.
func loggableProcessOutput(output string) string {
	excerpt := output
	truncated := false

	if i := strings.IndexByte(excerpt, '\n'); i >= 0 {
		excerpt = excerpt[:i]
		truncated = true
	}

	count := 0
	for pos := range excerpt {
		if count == loggableProcessOutputMax {
			excerpt = excerpt[:pos]
			truncated = true

			break
		}

		count++
	}

	if truncated {
		excerpt += "..."
	}

	return excerpt
}

// Error records an error and the operation and scheduler that caused it.
type Error struct {
	Scheduler string // the scheduler's Name
	Op        string // name of the method
	Err       string // one of our Err* vars
}

func (e Error) Error() string {
	return "scheduler(" + e.Scheduler + ") " + e.Op + "(): " + e.Err
}

// Requirements describes the resource requirements of the commands you want to
// run, so that when provided to a scheduler it will be able to schedule things
// appropriately.
type Requirements struct {
	RAM   int           // the expected peak RAM in MB Cmd will use while running
	Time  time.Duration // the expected time Cmd will take to run
	Cores float64       // how many processor cores the Cmd will use
	Disk  int           // the required local disk space in GB the Cmd needs to run
	// Other is a map that will be passed through to the job scheduler, defining
	// further arbitrary resource requirements.
	Other map[string]string
	// CoresSet distinguishes between you specifying 0 Cores and not specifying
	// Cores at all.
	CoresSet bool
	// DiskSet distinguishes between you specifying 0 Disk and not specifying
	// Disk at all.
	DiskSet  bool
	OtherSet bool
}

// Stringify represents the contents of the Requirements as a string, sorting
// the keys of Other to ensure the same result is returned for the same content
// every time. Note that the data in Other undergoes a 1-way transformation,
// so you cannot recreate the Requirements from the output of this method.
func (req *Requirements) Stringify() string {
	var other string

	if len(req.Other) > 0 {
		otherKeys := make([]string, 0, len(req.Other))
		for key := range req.Other {
			otherKeys = append(otherKeys, key)
		}

		sort.Strings(otherKeys)

		var otherSb strings.Builder

		for _, key := range otherKeys {
			otherSb.WriteString(":" + key + "=" + req.Other[key])
		}

		other += otherSb.String()

		// now convert it all in to an md5sum, to avoid any problems with some
		// key values having line returns etc. *** we might like to use
		// byteKey() from jobqueue package instead, but that isn't exported...
		other = fmt.Sprintf(":%x", md5.Sum([]byte(other))) // #nosec
	}

	return fmt.Sprintf("%d:%.0f:%s:%d%s",
		req.RAM, req.Time.Minutes(),
		strconv.FormatFloat(req.Cores, 'f', -1, 64),
		req.Disk, other)
}

// Clone creates a copy of the Requirements.
func (req *Requirements) Clone() *Requirements {
	clone := &Requirements{
		RAM:      req.RAM,
		Time:     req.Time,
		Cores:    req.Cores,
		CoresSet: req.CoresSet,
		Disk:     req.Disk,
		DiskSet:  req.DiskSet,
		OtherSet: req.OtherSet,
	}
	if req.OtherSet || len(req.Other) > 0 {
		newOther := make(map[string]string, len(req.Other))
		maps.Copy(newOther, req.Other)
		clone.Other = newOther
	}

	return clone
}

// CmdStatus lets you describe how many of a given cmd are already in the job
// scheduler, and gives the details of those jobs.
type CmdStatus struct {
	Count   int
	Running [][2]int // a slice of [id, index] tuples
	Pending [][2]int // ditto
	Other   [][2]int // ditto, for jobs in some strange state
}

// MessageCallBack functions receive a message that would be good to display to
// end users, so they understand current error conditions related to the
// scheduler.
type MessageCallBack func(msg string)

// BadServerCallBack functions receive a server when a cloud scheduler discovers
// that a server it spawned no longer seems functional. It's possible that this
// was due to a temporary networking issue, in which case the callback will be
// called again with the same server when it is working fine again: check
// server.IsBad(). If it's bad, you'd probably call server.Destroy() after
// confirming the server is definitely unusable (eg. ask the end user to
// manually check).
type BadServerCallBack func(server *cloud.Server)

// RecoveredHostDetails lets you describe a host for supplying to Recover(). Not
// all fields are relevant for all schedulers. Some might use none, so a nil
// RecoveredHostDetails might be valid. Cloud schedulers need all fields
// specified.
type RecoveredHostDetails struct {
	Host     string        // host's hostname
	UserName string        // username needed to ssh log in to host
	TTD      time.Duration // frequency to check if the host is idle, and if so destroy it
}

// Host interface let's us run a command on a local or remote host.
type Host interface {
	// RunCmd runs the given cmd on the host, optionally in the background,
	// cancellable with the context, returning stdout, stderr from the command,
	// or an error if running the command wasn't possible.
	RunCmd(ctx context.Context, cmd string, background bool) (stdout, stderr string, err error)
}

// scheduleri interface must be satisfied to add support for a particular job
// scheduler. It is intentionally broad: each method maps to a corresponding
// public Scheduler method that the whole package is built around.
//
//nolint:interfacebloat // each method backs a distinct public Scheduler method
type scheduleri interface {
	// initialize does any initial set up to be able to use the job scheduler.
	initialize(ctx context.Context, config any) error

	// schedule achieves the aims of Schedule().
	schedule(ctx context.Context, cmd string, req *Requirements, priority uint8, count int) error

	// scheduled achieves the aims of Scheduled().
	scheduled(ctx context.Context, cmd string) (int, error)

	// recover achieves the aims of Recover().
	recover(ctx context.Context, cmd string, req *Requirements, host *RecoveredHostDetails) error

	// busy achieves the aims of Busy().
	busy(ctx context.Context) bool

	// reserveTimeout achieves the aims of ReserveTimeout().
	reserveTimeout(ctx context.Context, req *Requirements) int

	// maxQueueTime achieves the aims of MaxQueueTime(), returning 0 for infinite
	// queue time.
	maxQueueTime(req *Requirements) time.Duration

	// hostToID achieves the aims of HostToID().
	hostToID(host string) string

	// getHost gets a Host that can be used to run commands over ssh on the given
	// host, returning a false boolean if no such host exists.
	getHost(host string) (Host, bool)

	// setMessageCallBack achieves the aims of SetMessageCallBack().
	setMessageCallBack(ctx context.Context, cb MessageCallBack)

	// setBadServerCallBack achieves the aims of SetBadServerCallBack().
	setBadServerCallBack(ctx context.Context, cb BadServerCallBack)

	// reserved achieves the aims of Reserved().
	reserved(schedulerID string)

	// cleanup does any clean up once you've finished using the job scheduler.
	cleanup(ctx context.Context)
}

// CloudConfig interface could be satisfied by the config option taken by cloud
// schedulers which have a ConfigFiles property, a property for configuring a
// default ssh login username, and a property for determining how long to keep
// idle servers.
type CloudConfig interface {
	// AddConfigFile takes a value like that of the ConfigFiles property of the
	// struct implementing this interface, and appends this value to what is
	// in ConfigFiles, or sets it if unset.
	AddConfigFile(spec string)

	// GetOSUser returns the default ssh login username for servers.
	GetOSUser() string

	// GetServerKeepTime returns the time to keep idle servers alive for.
	GetServerKeepTime() time.Duration
}

// Scheduler gives you access to all of the methods you'll need to interact with
// a job scheduler.
type Scheduler struct {
	impl    scheduleri
	Name    string
	limiter map[string]int
	// lastCannotConfirm is the UnixNano time ProcessNotRunningOnHost last logged a
	// could-not-determine warning. It rate-limits that warning (accessed
	// atomically) so a persistent misconfiguration cannot flood the log.
	lastCannotConfirm atomic.Int64
	sync.Mutex
}

// New creates a new Scheduler to interact with the given job scheduler.
// Possible names so far are "lsf", "local", and "openstack". You must also
// provide a config struct appropriate for your chosen scheduler, eg.
// for the local scheduler you will provide a ConfigLocal.
//
// Providing a logger allows for debug messages to be logged somewhere, along
// with any "harmless" or unreturnable errors. If not supplied, we use a default
// logger that discards all log messages.
func New(ctx context.Context, name string, config any) (*Scheduler, error) {
	var s *Scheduler

	switch name {
	case lsfScheduler:
		s = &Scheduler{impl: new(lsf)}
	case localScheduler:
		s = &Scheduler{impl: new(local)}
	case openstackScheduler:
		s = &Scheduler{impl: new(opst)}
	case mockSchedulerName:
		// a test double that runs an in-process function instead of spawning
		// runner subprocesses; see ConfigMock.
		s = &Scheduler{impl: new(mock)}
	default:
		return nil, Error{name, "New", ErrBadScheduler}
	}

	s.Name = name
	s.limiter = make(map[string]int)
	err := s.impl.initialize(s.typeContext(ctx), config)

	return s, err
}

// typeContext returns a context based on the scheduler type.
func (s *Scheduler) typeContext(ctx context.Context) context.Context {
	return clog.ContextWithSchedulerType(ctx, s.Name)
}

// SetMessageCallBack sets the function that will be called when a scheduler has
// some message that could be informative to end users wondering why something
// is not getting scheduled. The message typically describes an error condition.
func (s *Scheduler) SetMessageCallBack(ctx context.Context, cb MessageCallBack) {
	s.impl.setMessageCallBack(s.typeContext(ctx), cb)
}

// SetBadServerCallBack sets the function that will be called when a cloud
// scheduler discovers that one of the servers it spawned seems to no longer be
// functional or reachable. Only relevant for cloud schedulers.
func (s *Scheduler) SetBadServerCallBack(ctx context.Context, cb BadServerCallBack) {
	s.impl.setBadServerCallBack(s.typeContext(ctx), cb)
}

// Schedule gets your cmd scheduled in the job scheduler. You give it a command
// that you would like `count` identical instances of running via your job
// scheduler. If you already had `count` many scheduled, it will do nothing. If
// you had less than `count`, it will schedule more to run. If you have more
// than `count`, it will remove the appropriate number of scheduled (but not yet
// running) jobs that were previously scheduled for this same cmd (counts of 0
// are legitimate - it will get rid of all non-running jobs for the cmd).
//
// Typically schedulers will end up running cmds according to their "size" (cpu
// and memory needed as per the req), with larger cmds running first due to bin
// packing. Some schedulers will take the given priority in to account and try
// to run cmds with higher priorities before those with lower ones. Equal
// priority jobs will use the normal approach.
//
// If no error is returned, you know all `count` of your jobs are now scheduled
// and will eventually run unless you call Schedule() again with the same
// command and a lower count. NB: there is no guarantee that the jobs run
// successfully, and no feedback on their success or failure is given.
func (s *Scheduler) Schedule(ctx context.Context, cmd string, req *Requirements, priority uint8, count int) error {
	// Schedule may get called many times in different go routines, eg. a
	// succession of calls with the same cmd and req but decrementing count.
	// Here we arrange that impl.schedule is only called once at a time per
	// cmd: if not already running we call as normal; if running we don't run
	// it but return immediately while storing the more recent desired count;
	// when it finishes running, we re-run with the most recent count, if any
	s.Lock()
	if _, limited := s.limiter[cmd]; limited {
		s.limiter[cmd] = count
		s.Unlock()

		return nil
	}

	s.limiter[cmd] = count
	s.Unlock()

	err := s.impl.schedule(s.typeContext(ctx), cmd, req.Clone(), priority, count)

	s.rescheduleIfCountChanged(ctx, cmd, req, priority, count)

	return err
}

// rescheduleIfCountChanged clears the limiter entry for cmd that Schedule() set,
// and if the most recently desired count differs from the one we just scheduled
// for, kicks off another Schedule() in the background with that newer count.
func (s *Scheduler) rescheduleIfCountChanged(
	ctx context.Context, cmd string, req *Requirements, priority uint8, count int,
) {
	s.Lock()
	defer s.Unlock()

	newcount, limited := s.limiter[cmd]
	if !limited {
		return
	}

	delete(s.limiter, cmd)

	if newcount == count {
		return
	}

	go func() {
		defer internal.LogPanic(ctx, "schedule recall", true)

		errf := s.Schedule(ctx, cmd, req, priority, newcount)
		if errf != nil {
			clog.Error(s.typeContext(ctx), "schedule recall", "err", errf)
		}
	}()
}

// Scheduled tells you how many of the given cmd are currently scheduled in the
// scheduler.
func (s *Scheduler) Scheduled(ctx context.Context, cmd string) (int, error) {
	return s.impl.scheduled(s.typeContext(ctx), cmd)
}

// Recover is used if you had Scheduled some cmds, then you crashed, and now
// you're starting up again and want the scheduler to take in to account the
// fact that you still have some commands running on certain hosts. Doing this
// may allow us to avoid overcommitting resources or terminate unneeded hosts,
// if relevant for the scheduler in question. (For some schedulers, this does
// nothing.)
//
// The cmd and req ought to exactly match those previously supplied to
// Schedule() before your crash.
func (s *Scheduler) Recover(ctx context.Context, cmd string, req *Requirements, host *RecoveredHostDetails) error {
	return s.impl.recover(s.typeContext(ctx), cmd, req, host)
}

// Busy reports true if there are any Schedule()d cmds still in the job
// scheduler's system. This is useful when testing and other situations where
// you want to avoid shutting down the server while there are still clients
// running/ about to run.
func (s *Scheduler) Busy(ctx context.Context) bool {
	return s.impl.busy(s.typeContext(ctx))
}

// ReserveTimeout returns the number of seconds that runners spawned in this
// scheduler should wait for new jobs to appear in the manager's queue.
func (s *Scheduler) ReserveTimeout(ctx context.Context, req *Requirements) int {
	return s.impl.reserveTimeout(s.typeContext(ctx), req)
}

// MaxQueueTime returns the maximum amount of time that jobs with the given
// resource requirements are allowed to run for in the job scheduler's queue. If
// the job scheduler doesn't have a queue system, or if the queue allows jobs to
// run forever, then this returns req.Time + 15 mins.
func (s *Scheduler) MaxQueueTime(req *Requirements) time.Duration {
	d := s.impl.maxQueueTime(req)
	if d == 0 {
		// jobqueue Server uses this to pass a time limit to the client process
		// being scheduled, which we want to exit soon after it has done a
		// minimal amount of work, but not earlier than 1min to aid efficiency
		return req.Time + minimumQueueTime
	}

	return d
}

// HostToID will return the server id of the server with the given host name, if
// the scheduler is cloud based. Otherwise this just returns an empty string.
func (s *Scheduler) HostToID(host string) string {
	return s.impl.hostToID(host)
}

// GetHost will return a Host with the given host name. For cloud-based
// schedulers, you can cast the return value as a *cloud.Server. Returns nil on
// error (if a host with the given name doesn't exist).
func (s *Scheduler) GetHost(hostName string) Host {
	host, worked := s.impl.getHost(hostName)
	if !worked {
		return nil
	}

	return host
}

// ProcessNotRunningOnHost will ssh to the given host and check if the given
// process id is still running. Returns true if it isn't. Returns false if it is
// running, or if the ssh wasn't possible. This is to find out if a process is
// really dead, or if there might just be a temporary networking problem where
// ssh might fail. The ssh attempt can be cancelled using the supplied context.
//
// CONTRACT WARNING: the remote command below is a compatibility contract with
// any forced command a user has configured for this key in their farm nodes'
// authorized_keys (see the privatekeypath docs in cmd/conf.go). It runs
// `ps -o stat= -p <pid>` and treats EMPTY output as "dead". Do NOT change the
// command or the way its output is interpreted without a migration plan: a
// user's forced command that reproduces the old output (e.g. an older wr sent
// `ps -p <pid> | wc -l` and users wrapped keys to emit that count) will then
// silently mis-report every process as still running, so lost jobs are never
// reclaimed and limit-group scheduling stalls. Prefer to fail loudly (log) on
// output that is neither empty nor a plausible process state, rather than
// treating an unexpected value as "still running".
func (s *Scheduler) ProcessNotRunningOnHost(ctx context.Context, pid int, hostName string) bool {
	host, ok := s.impl.getHost(hostName)
	if !ok {
		s.warnCannotConfirm(ctx, hostName, pid, "could not get the host to ssh to")

		return false
	}

	stdo, _, err := host.RunCmd(ctx, fmt.Sprintf("ps -o stat= -p %d 2>/dev/null || test $? -eq 1", pid), false)
	if err != nil {
		s.warnCannotConfirm(ctx, hostName, pid, "the remote ps command failed: "+err.Error())

		return false
	}

	state := strings.TrimSpace(stdo)

	switch interpretProcessState(state) {
	case processDead:
		return true
	case processAlive:
		return false
	case processUnknown:
		s.warnCannotConfirm(ctx, hostName, pid, "unexpected ps output: "+loggableProcessOutput(state))
	}

	return false
}

// Cleanup means you've finished using a scheduler and it can delete any
// remaining jobs in its system and clean up any other used resources.
func (s *Scheduler) Cleanup(ctx context.Context) {
	s.impl.cleanup(s.typeContext(ctx))
}

// isProcessState reports whether state begins with a recognised ps process-state
// code (see ps(1) STATE): the leading character is the primary state, optionally
// followed by modifier characters we ignore here.
func isProcessState(state string) bool {
	const processStateCodes = "DIRSTtWXZ"

	if state == "" {
		return false
	}

	return strings.IndexByte(processStateCodes, state[0]) >= 0
}

// jobName could be useful to a scheduleri implementer if it needs a constant-
// width (length 36) string unique to the cmd and deployment, and optionally
// suffixed with a random string (length 9, total length 45).
func jobName(cmd string, deployment string, unique bool) string {
	l, h := farm.Hash128([]byte(cmd))
	name := fmt.Sprintf("wr%s_%016x%016x", deployment[0:1], l, h)

	if unique {
		name += "_" + internal.RandomString()
	}

	return name
}
