/*******************************************************************************
 * Copyright (c) 2017-2021, 2024-2026 Genome Research Ltd.
 *
 * Author: Sendu Bala <sb10@sanger.ac.uk>
 * Author: Michael Woolnough <mw31@sanger.ac.uk>
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

package jobqueue

// This file contains the job related code.

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"os/signal"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/VertebrateResequencing/muxfys/v5"
	"github.com/VertebrateResequencing/wr/container"
	"github.com/VertebrateResequencing/wr/jobqueue/scheduler"
	"github.com/VertebrateResequencing/wr/limiter"
	"github.com/VertebrateResequencing/wr/queue"
	"github.com/gofrs/uuid/v5"
	multierror "github.com/hashicorp/go-multierror"
	"github.com/kballard/go-shellquote"
	"github.com/ugorji/go/codec"
)

// jobSchedLimitGroupSeparator is the separator between requirements and limit
// groups in scheduler group names.
const jobSchedLimitGroupSeparator = "~"

// jobLimitGroupSeparator is the separator between limit groups in scheduler
// group names.
const jobLimitGroupSeparator = ","

// defaultMountRetries is the number of times muxfys retries a mount when the
// MountConfig doesn't specify its own Retries.
const defaultMountRetries = 10

// errNoTargets is returned by Mount when a MountConfig has no usable Targets.
var errNoTargets = errors.New("no Targets specified")

// JobState is how we describe the possible job states.
type JobState string

// JobState* constants represent all the possible job states. The fake "new" and
// "deleted" states are for the benefit of the web interface (the jstateCount
// status-bar delta feed). "lost" is also a "fake" state indicating the job was
// running and we lost contact with it; it may be dead. "unknown" is an error
// case that shouldn't happen. "deletable" is a meta state that can be used
// when filtering jobs to mean !(reserved|running|complete), and "incomplete" is
// a meta state meaning !complete, ie. every job still live in the queue: it lets
// a command that can only ever act on live jobs (wr suspend, wr resume) say so,
// which is what keeps the manager from scanning and decoding the whole archived
// history of every matching RepGroup for them (reliable4 FINDING 1). Neither
// meta state is ever the state OF a job, so they are only meaningful as filters.
const (
	JobStateNew        JobState = "new"
	JobStateDelayed    JobState = "delayed"
	JobStateReady      JobState = "ready"
	JobStateReserved   JobState = "reserved"
	JobStateRunning    JobState = "running"
	JobStateLost       JobState = "lost"
	JobStateBuried     JobState = "buried"
	JobStateDependent  JobState = "dependent"
	JobStateSuspended  JobState = "suspended"
	JobStateComplete   JobState = "complete"
	JobStateDeleted    JobState = "deleted"
	JobStateDeletable  JobState = "deletable"
	JobStateIncomplete JobState = "incomplete"
	JobStateUnknown    JobState = "unknown"
)

// subqueueToJobState converts queue.SubQueue entries to JobStates.
//
//nolint:gochecknoglobals // immutable lookup table
var subqueueToJobState = map[queue.SubQueue]JobState{
	queue.SubQueueNew:       JobStateNew,
	queue.SubQueueDelay:     JobStateDelayed,
	queue.SubQueueReady:     JobStateReady,
	queue.SubQueueRun:       JobStateRunning,
	queue.SubQueueBury:      JobStateBuried,
	queue.SubQueueDependent: JobStateDependent,
	queue.SubQueueSuspended: JobStateSuspended,
	queue.SubQueueRemoved:   JobStateComplete,
}

// itemsStateToJobState converts queue.ItemState entries to JobStates.
//
//nolint:gochecknoglobals // immutable lookup table
var itemsStateToJobState = map[queue.ItemState]JobState{
	queue.ItemStateDelay:     JobStateDelayed,
	queue.ItemStateReady:     JobStateReady,
	queue.ItemStateRun:       JobStateReserved,
	queue.ItemStateBury:      JobStateBuried,
	queue.ItemStateDependent: JobStateDependent,
	queue.ItemStateSuspended: JobStateSuspended,
	queue.ItemStateRemoved:   JobStateComplete,
}

// resolveMountPoint says where a MountConfig.Mount ends up being mounted: the
// default when it is unspecified, itself when it is absolute, and otherwise
// relative to cwd (so it can climb out of cwd, eg. "../shared" for a mount
// shared between the Jobs of a Cwd).
func resolveMountPoint(mcMount, cwd, defaultMount string) string {
	if mcMount == "" {
		return defaultMount
	}

	if filepath.IsAbs(mcMount) {
		return mcMount
	}

	return filepath.Join(cwd, mcMount)
}

// jobDerived holds the EXPENSIVE derived strings a rac cycle needs for a ready
// job: its Key() (which MD5s Cwd+Cmd+mount+container), the scheduler-adjusted
// Requirements from reqForScheduler, and the scheduler group name those imply
// (which sorts and MD5s Requirements.Other). Computing them costs 2 MD5s, a sort
// and several allocations, and the rac pre-pass needs them for EVERY ready job on
// EVERY cycle, so they are memoised on the Job (reliable4 FINDING 3: an idle
// 61,000-job limit-blocked backlog burnt 0.79 cores recomputing them).
//
// Their inputs (Cmd, Cwd, CwdMatters, MountConfigs, the container fields,
// Requirements and LimitGroups) do not change for the great majority of a job's
// life, but they are not immutable: see Job.invalidateDerivedLocked for the
// complete set of places that change them, each of which invalidates the memo.
// The priority and current scheduler group a snapshot also carries DO change
// often and are cheap, so they are deliberately not memoised and are read live.
//
// The requirements pointer is shared by every snapshot taken from the memo, so it
// must be treated as read-only; the only consumer, ensureGroup, Clone()s it.
//
// Job.Key() is deliberately left uncached in itself, and memoised only here. The
// reason is re-entrancy, not the before/after key comparison modifyJob makes:
// Key() is called with the job's write lock ALREADY held (prepareInputJobs,
// modifyJob before and after applyTo, and derivedLocked below), so a Key() that
// took the lock to read or fill a cache would deadlock, and one that used its own
// atomic instead would need a second invalidation discipline for exactly the same
// set of mutators. Memoising it here gets the whole saving on the only hot path,
// under a lock its callers already hold.
type jobDerived struct {
	key          string
	requirements *scheduler.Requirements
	group        string
}

func (j *Job) decrementLimitGroupsLocked(lim *limiter.Limiter) {
	if len(j.incrementedLimitGroups) > 0 {
		if lim != nil {
			lim.Decrement(j.incrementedLimitGroups)
		}

		j.incrementedLimitGroups = []string{}
	}
}

// dropImpossibleCleanups removes every cleanup Behaviour this Job would carry
// without ever being able to carry it out.
//
// Behaviour.cleanup only ever deletes the working directory wr itself created
// below Cwd, so on a CwdMatters job, which runs in the user's own Cwd, a cleanup
// is a no-op that must not be stored: it would have wr advertise a deletion that
// will never happen.
//
// The server calls this on every Job that enters its store, whether the Job came
// from a client (prepareInputJobs) or from the database (db.decodeJob), so that a
// hand-built Job and a persisted one are both covered.
func (j *Job) dropImpossibleCleanups() {
	if !j.CwdMatters {
		return
	}

	j.Behaviours = j.Behaviours.withoutCleanups()
}

// cwdLeaf returns the part of cwd below cwdBase, prefixed with "/", for display
// alongside cwdBase as a Job's working directory. It is the single projection
// used for both a stored Job's JStatus and a live Job's JobUpdate, so that
// neither can gain or lose a guard the other lacks.
//
// A blank cwd (the Job has no working directory of its own yet) gives "". A cwd
// that is cwdBase gives "/", which is what a Job persisted by wr v0.37.0|1 has.
// A cwd that is not below cwdBase at all - a stale ActualCwd left over from a
// `wr mod --cwd` - is shown in full rather than as a leaf climbing out of cwdBase
// with "..".
func cwdLeaf(cwdBase, cwd string) (string, error) {
	if cwd == "" {
		return "", nil
	}

	if cwdBase == "" {
		return cwd, nil
	}

	rel, err := filepath.Rel(cwdBase, cwd)
	if err != nil {
		return "", err
	}

	if rel == "." {
		return "/", nil
	}

	if !relIsBelow(rel) {
		return cwd, nil
	}

	return "/" + rel, nil
}

// memoisedSchedulerGroupSnapshot returns the snapshot built from the memoised
// derived strings under a read lock, with memoised false if they have not been
// computed yet (or have been invalidated), in which case the caller must go
// through derivedLocked under the write lock.
func (j *Job) memoisedSchedulerGroupSnapshot() (schedulerGroupSnapshot, bool) {
	j.RLock()
	defer j.RUnlock()

	if j.derived == nil {
		return schedulerGroupSnapshot{}, false
	}

	return j.snapshotWithDerived(j.derived), true
}

// snapshotWithDerived combines the given memoised derived strings with this job's
// live priority and current scheduler group. Call with at least the read lock
// held.
func (j *Job) snapshotWithDerived(derived *jobDerived) schedulerGroupSnapshot {
	return schedulerGroupSnapshot{
		key:           derived.key,
		requirements:  derived.requirements,
		previousGroup: j.schedulerGroup,
		group:         derived.group,
		priority:      j.Priority,
	}
}

// derivedLocked returns this job's memoised derived strings, computing them if
// this is the first call since the job was created or last invalidated. The write
// lock must be held, both because it stores the result and so that a mutator that
// invalidates under that same lock can never be overtaken by a computation made
// from the fields as they were before its change.
func (j *Job) derivedLocked() *jobDerived {
	if j.derived != nil {
		return j.derived
	}

	req := reqForScheduler(j.Requirements)

	derived := &jobDerived{
		key:          j.Key(),
		requirements: req,
		group:        schedulerGroupString(req, j.LimitGroups),
	}

	j.derived = derived
	j.derivations++

	return derived
}

// invalidateDerivedLocked drops this job's memoised derived strings, so the next
// schedulerGroupSnapshot recomputes them. It must be called, with the job's write
// lock held, by everything that changes an input of jobDerived. Those are:
//
//   - JobModifier.applyTo (Cmd, Cwd, CwdMatters, MountConfigs, the container
//     fields, LimitGroups and the Requirements fields), the only path that
//     changes a live job's Cmd/Cwd/mounts/container fields at all;
//   - updateJobRequirementsForRetry (Requirements RAM/Disk/Time), the only path
//     that applies learned or post-failure requirements to a live job, via
//     prepareReadyJob;
//   - Server.handleUserSpecifiedJobLimitGroups (LimitGroups normalisation).
//
// That the set is complete is necessary but not sufficient: each of those call
// sites must also invalidate on EVERY path through it that can have mutated an
// input. applyTo and handleUserSpecifiedJobLimitGroups have a single exit each and
// invalidate at it, but updateJobRequirementsForRetry mutates Requirements before
// an early return, so it invalidates in a DEFER; invalidating at its tail instead
// would silently leave a stale memo (and so schedule against stale requirements)
// for every job whose override says to keep its own values but which still learns
// the resources it did not specify.
//
// Everywhere else those fields are only ever set while building a brand new Job
// (at add time, on decoding a database record, or in the field-by-field copies
// made for clients), which starts with no memo, so nothing to invalidate.
func (j *Job) invalidateDerivedLocked() {
	j.derived = nil
}

func sshCommandForRunningJob(state JobState, reqs *scheduler.Requirements, host, hostIP, workingDir string) string {
	if state != JobStateRunning || workingDir == "" {
		return ""
	}

	target := sshTarget(reqs, host, hostIP)
	if target == "" {
		return ""
	}

	remote := "cd " + quoteRemoteCwd(workingDir) + " && exec ${SHELL:-/bin/sh} -l"

	return "ssh -- " + shellquote.Join(target) + " " + singleQuoteShellArg(remote)
}

// mergeBehaviours returns existing with, for each trigger that modifications
// supplies behaviours for, all of existing's behaviours for that trigger
// replaced by the whole supplied set in the supplied order. Triggers that
// modifications does not mention keep their existing behaviours, and a trigger
// that existing does not have yet gets appended.
func mergeBehaviours(existing, modifications Behaviours) Behaviours {
	if len(modifications) == 0 {
		return existing
	}

	replaced := make(map[BehaviourTrigger]bool, len(modifications))
	for _, b := range modifications {
		replaced[b.When] = true
	}

	merged := make(Behaviours, 0, len(existing)+len(modifications))

	for _, b := range existing {
		if !replaced[b.When] {
			merged = append(merged, b)
		}
	}

	return append(merged, modifications...)
}

func sshTarget(reqs *scheduler.Requirements, host, hostIP string) string {
	if hostIP == "" {
		return host
	}

	if reqs != nil && reqs.Other["cloud_user"] != "" && hostIP != "" {
		return reqs.Other["cloud_user"] + "@" + hostIP
	}

	return hostIP
}

// Job is a struct that represents a command that needs to be run and some
// associated metadata. If you get a Job back from the server (via Reserve() or
// Get*()), you should treat the properties as read-only: changing them will
// have no effect.
type Job struct {
	// Cmd is the actual command line that will be run via the shell.
	Cmd string

	// Cwd determines the command working directory, the directory we cd to
	// before running Cmd. When CwdMatters, Cwd is used exactly, otherwise a
	// unique sub-directory of Cwd is used as the command working directory.
	Cwd string

	// CwdMatters should be made true when Cwd contains input files that you
	// will refer to using relative (from Cwd) paths in Cmd, and when other Jobs
	// have identical Cmds because you have many different directories that
	// contain different but identically named input files. Cwd will become part
	// of what makes the Job unique.
	// When CwdMatters is false (default), Cmd gets run in a unique subfolder of
	// Cwd, enabling features like tracking disk space usage and clean up of the
	// working directory by simply deleting the whole thing. The TMPDIR
	// environment variable is also set to a sister folder of the unique
	// subfolder, and this is always cleaned up after the Cmd exits.
	CwdMatters bool

	// ChangeHome sets the $HOME environment variable to the actual working
	// directory before running Cmd, but only when CwdMatters is false.
	ChangeHome bool

	// RepGroup is a name associated with related Jobs to help group them
	// together when reporting on their status etc.
	RepGroup string

	// ReqGroup is a string that you supply to group together all commands that
	// you expect to have similar resource requirements.
	ReqGroup string

	// Group is the group name to run the executable as; a value of empty string
	// will use the default group.
	Group string

	// Requirements describes the resources this Cmd needs to run, such as RAM,
	// Disk and time. These may be determined for you by the system (depending
	// on Override) based on past experience of running jobs with the same
	// ReqGroup.
	Requirements *scheduler.Requirements

	// RequirementsOrig is like Requirements, but only has the original RAM,
	// Disk and time values set by you, if any.
	RequirementsOrig *scheduler.Requirements

	// Override determines if your own supplied Requirements get used, or if the
	// systems' calculated values get used. 0 means prefer the system values. 1
	// means prefer your values if they are higher. 2 means always use your
	// values.
	Override uint8

	// Priority is a number between 0 and 255 inclusive - higher numbered jobs
	// will run before lower numbered ones (the default is 0).
	Priority uint8

	// Retries is the number of times to retry running a Cmd if it fails.
	Retries uint8

	// NoRetriesOverWalltime is the amount of time that a cmd can run for and
	// then fail and still automatically retry. If it runs longer than this
	// duration and fails, it will instead be immediately buried.
	NoRetriesOverWalltime time.Duration

	// LimitGroups are names of limit groups that this job belongs to. If any
	// of these groups are defined (elsewhere) to have a limit, then if as many
	// other jobs as the limit are currently running, this job will not start
	// running. It's a way of not running too many of a type of job at once.
	LimitGroups []string

	// LimitGroupsForDisplay preserves any user-supplied limit suffixes for
	// status output after LimitGroups has been normalised for scheduling.
	LimitGroupsForDisplay []string

	// Modules are the names of environment modules that should be loaded before
	// running Cmd.
	Modules []string

	// DepGroups are the dependency groups this job belongs to that other jobs
	// can refer to in their Dependencies.
	DepGroups []string

	// Dependencies describe the jobs that must be complete before this job
	// starts.
	Dependencies Dependencies

	// WaitingForDepGroups lists dependency groups this job is waiting on that
	// have not yet appeared on any job.
	WaitingForDepGroups []string

	// Behaviours describe what should happen after Cmd is executed, depending
	// on its success.
	Behaviours Behaviours

	// MountConfigs describes remote file systems or object stores that you wish
	// to be fuse mounted prior to running the Cmd. Once Cmd exits, the mounts
	// will be unmounted (with uploads only occurring if it exits with code 0).
	// If you want multiple separate mount points accessed from different local
	// directories, you will supply more than one MountConfig in the slice. If
	// you want multiple remote locations multiplexed and accessible from a
	// single local directory, you will supply a single MountConfig in the
	// slice, configured with multiple MountTargets. Relative paths for your
	// MountConfig.Mount options will be relative to Cwd (or ActualCwd if
	// CwdMatters == false). If a MountConfig.Mount is not specified, it
	// defaults to Cwd/mnt if CwdMatters, otherwise ActualCwd itself will be the
	// mount point. If a MountConfig.CachBase is not specified, it defaults to
	// Cwd if CwdMatters, otherwise it will be a sister directory of
	// ActualCwd.
	MountConfigs MountConfigs

	// BsubMode set to either Production or Development when Add()ing a job will
	// result in the job being assigned a BsubID. Such jobs, when they run, will
	// see bsub, bjobs and bkill as symlinks to wr, thus if they call bsub, they
	// will actually add jobs to the jobqueue etc. Those jobs will pick up the
	// same Requirements.Other as this job, and the same MountConfigs. If
	// Requirements.Other["cloud_shared"] is "true", the MountConfigs are not
	// reused.
	BsubMode string

	// MonitorDocker turns on monitoring of a docker container identified by its
	// --name or path to its --cidfile, adding its peak RAM and CPU usage to the
	// reported RAM and CPU usage of this job.
	//
	// Only a container that appears after the job starts is monitored, however
	// it is identified: a container that is already running under that name (or
	// named in a stale cidfile) belongs to someone else, and monitoring it
	// would make wr kill it when this job is killed or runs out of resources.
	//
	// If the special argument "?" is supplied, monitoring will apply to the
	// first new docker container that appears after the Cmd starts to run.
	// NB: if multiple jobs that run docker containers start running at the same
	// time on the same machine, the reported stats could be wrong for one or
	// more of those jobs.
	//
	// Requires that docker is installed on the machine where the job will run
	// (and that the Cmd uses docker to run a container). NB: does not handle
	// monitoring of multiple docker containers run by a single Cmd.
	MonitorDocker string

	// WithDocker will result in CmdLine() returning a `docker run` cmd that
	// uses the image specified here to run Cmd by piping Cmd into the
	// container's /bin/sh. Cwd will be mounted inside the container and will
	// be the working directory in the container. Anything specified by
	// ContainerMounts will also be mounted in the container. Any EnvOverride
	// environment variable name values will also be set for import in to the
	// container. Setting this sets (and overrides) MonitorDocker to this Job's
	// Key(), which will also be the container's --name.
	WithDocker string

	// WithSingularity will result in CmdLine() returning a `singularity shell`
	// command that uses the image specified here to run Cmd by piping it in to
	// the container. Cwd will be mounted inside the container and will be the
	// working directory in the container. Anything specified by ContainerMounts
	// will also be mounted in the container. All the job's environment
	// variables will be available inside the container.
	WithSingularity string

	// ContainerMounts is a comma separated list of strings each in the format:
	// /outside/container/path[:/inside/container/path] (where inside defaults
	// to outside if not provided)). If WithDocker or WithSingularity is also
	// specfied, the outside paths will be specified as to be bound to the
	// inside paths in the cmd returned by CmdLine().
	ContainerMounts string

	// The remaining properties are used to record information about what
	// happened when Cmd was executed, or otherwise provide its current state.
	// It is meaningless to set these yourself.

	// the actual working directory used, which would have been created with a
	// unique name if CwdMatters = false
	ActualCwd string
	// peak RAM (MB) used.
	PeakRAM int
	// peak disk (MB) used.
	PeakDisk int64
	// true if the Cmd was run and exited.
	Exited bool
	// if the job ran and exited, its exit code is recorded here, but check
	// Exited because when this is not set it could like exit code 0.
	Exitcode int
	// true if the job was running but we've lost contact with it
	Lost bool
	// if the job failed to complete successfully, this will hold one of the
	// FailReason* strings. Also set if Lost == true.
	FailReason string
	// pid of the running or ran process.
	Pid int
	// RunnerPid is the pid of the wr runner process that reserved and is executing
	// this job (its own os.Getpid()), as distinct from Pid (the command's child
	// process). The runner outlives the command and is what sends the archive, so it
	// is the correct liveness signal: a lost job is only truly dead (and safe to
	// re-run) if BOTH the command AND its runner are gone. 0 if not reported (old
	// records / pre-Started).
	RunnerPid int
	// host the process is running or did run on.
	Host string
	// host id the process is running or did run on (cloud specific).
	HostID string
	// host ip the process is running or did run on (cloud specific).
	HostIP string
	// time the cmd started running.
	StartTime time.Time
	// time the cmd stopped running.
	EndTime time.Time
	// CPU time used.
	CPUtime time.Duration
	// to read, call job.StdErr() instead; if the job ran, its (truncated)
	// STDERR will be here.
	StdErrC []byte
	// to read, call job.StdOut() instead; if the job ran, its (truncated)
	// STDOUT will be here.
	StdOutC []byte
	// to read, call job.Env() instead, to get the environment variables as a
	// []string, where each string is like "key=value".
	EnvC []byte
	// Since EnvC isn't always populated on job retrieval, this lets job.Env()
	// distinguish between no EnvC and merely not requested.
	EnvCRetrieved bool
	// if set (using output of CompressEnv()), they will be returned in the
	// results of job.Env().
	EnvOverride []byte
	// job's state in the queue: 'delayed', 'ready', 'reserved', 'running',
	// 'buried', 'complete' or 'dependent'.
	State JobState
	// number of times the job had ever entered 'running' state.
	Attempts uint32
	// remaining number of Release()s allowed before being buried instead.
	UntilBuried uint8
	// we note which client reserved this job, for validating if that client has
	// permission to do other stuff to this Job; the server only ever sets this
	// on Reserve(), so clients can't cheat by changing this on their end.
	ReservedBy uuid.UUID
	// on the server we don't store EnvC with the job, but look it up in db via
	// this key.
	EnvKey string
	// when retrieving jobs with a limit, this tells you how many jobs were
	// excluded.
	Similar int
	// name of the queue the Job was added to.
	Queue string
	// unique (for this manager session) id of the job submission, present if
	// BsubMode was set when the job was added.
	BsubID uint64
	// delay is the duration we would next spend in the delay queue
	DelayTime time.Duration

	// we add this internally to match up runners we spawn via the scheduler to
	// the Jobs they're allowed to ReserveFiltered().
	schedulerGroup string

	// derived memoises this job's expensive derived scheduler-group strings; nil
	// means "not computed yet or invalidated". See jobDerived and
	// schedulerGroupSnapshot. Being unexported it is neither serialised to the
	// database nor sent to clients, and the field-by-field copies made for clients
	// (copyJobForClient) and for key calculations (JobModifier.modifiedKey) start
	// out with it unset, so a memo can never travel to a different Job.
	derived *jobDerived

	// derivations counts how many times derived has actually been computed (ie.
	// how many 2-MD5-plus-sort derivations this job has cost). It is INERT
	// observability in the style of Server.racScanWork: nothing but the reliable4
	// memoisation test reads it, and it affects no behaviour. It lives here, rather
	// than on the Server a Job has no reference to, so that a test can count the
	// derivations of ITS OWN jobs; a process-wide counter would be perturbed by any
	// other live server in the same test binary. It is written under the write lock
	// (in derivedLocked) and so must be read under at least the read lock.
	derivations uint32

	// we store the MuxFys that we mount during Mount() so we can Unmount() them
	// later; this is purely client side.
	mountedFS []*muxfys.MuxFys

	// killCalled is set for running jobs if Kill() is called on them.
	killCalled bool

	// runID identifies the RUN this Job is currently on, and is minted by the
	// manager at every reservation. It is server side only: see runToken.
	runID runToken

	// incrementedLimitGroups notes that we have incremented limit groups for
	// this job, so they should be decremented when the job finishes running.
	incrementedLimitGroups []string

	sync.RWMutex
}

// CmdLine normally returns Cmd and a no-op function. However, if WithDocker or
// WithSingularity has been set, then:
//
// * Cmd is stored in a tmp file.
// * A new `docker run` or `singularity shell` command is returned that:
//   - Pulls the image specified in WithDocker|Singularity if it is missing.
//   - Creates a container (with docker, it's name will be our Key()).
//   - That will mount Cwd inside the container and use it as the workdir.
//   - That will also mount any ContainerMounts.
//   - That for docker will tell it to use our explicit EnvOverrides
//     (singularity will use all env vars).
//   - That will receive a pipe of the Cmd file contents to its shell.
//
// In the case of WithDocker, MonitorDocker will also be set to our Key().
//
// Once you have executed the returned command you should call the returned
// function which will delete the tmp file.
func (j *Job) CmdLine(ctx context.Context) (string, func(), error) {
	noop := func() {}

	if j.WithDocker == "" && j.WithSingularity == "" {
		return j.Cmd, noop, nil
	}

	path, cleanup, err := container.PrepareCmdFile(ctx, j.Cmd)
	if err != nil {
		return j.Cmd, noop, err
	}

	cmd, err := j.containerRunCmd(path)
	if err != nil {
		return j.Cmd, cleanup, err
	}

	return cmd, cleanup, nil
}

// containerRunCmd builds the command line that runs j.Cmd (already written to
// path) inside the configured docker or singularity container.
func (j *Job) containerRunCmd(path string) (string, error) {
	if j.WithDocker == "" {
		return container.SingularityRunCmd(j.WithSingularity, path, j.containerMounts()), nil
	}

	envs, err := j.containerEnv()
	if err != nil {
		return "", err
	}

	cmd := container.DockerRunCmd(j.WithDocker, path, j.Key(), j.containerMounts(), envs)
	j.MonitorDocker = j.Key()

	return cmd, nil
}

// containerMounts converts ContainerMounts to a slice of the mount values.
func (j *Job) containerMounts() []string {
	if j.ContainerMounts == "" {
		return nil
	}

	return strings.Split(j.ContainerMounts, ",")
}

// containerEnv converts EnvOverride to a slice of the envionrment variable
// names that were set.
func (j *Job) containerEnv() ([]string, error) {
	overrideEs, err := j.envCurrentOverrides()
	if err != nil {
		return nil, err
	}

	names := make([]string, len(overrideEs))

	for i, envvar := range overrideEs {
		parts := strings.Split(envvar, ":")
		names[i] = parts[0]
	}

	return names, nil
}

// WallTime returns the time the job took to run if it ran to completion, or the
// time taken so far if it is currently running.
func (j *Job) WallTime() time.Duration {
	if j.StartTime.IsZero() {
		return 0
	}

	if j.EndTime.IsZero() || j.State == JobStateReserved {
		return time.Since(j.StartTime)
	}

	return j.EndTime.Sub(j.StartTime)
}

// Env decompresses and decodes job.EnvC (the output of CompressEnv(), which are
// the environment variables the Job's Cmd should run/ran under). Note that EnvC
// is only populated if you got the Job from GetByCmd(_, _, true),
// GetByEssence(_, _, true), or Reserve().
// If no environment was stored for the job, returns current environment
// variables instead. A stored non-nil empty environment returns an empty slice.
// In both cases, alters the return value to apply any overrides stored in
// job.EnvOverride.
func (j *Job) Env() ([]string, error) {
	return jobEnv{envC: j.EnvC, overrides: j.envOverrideSnapshot(), retrieved: j.EnvCRetrieved}.decode()
}

// envDecodeHook, when set, is called every time a Job's stored environment is
// decoded, so that a test can count the decodes a code path makes. Decoding is a
// decompress plus a decode of every variable the Job was added with, on the path
// of every Job that exits, and a path that does none of it cannot show that in
// its result, only in the work it did. It is nil in production.
var envDecodeHook func() //nolint:gochecknoglobals // the only seam onto work that has no result

// jobEnv is a Job's environment in the compressed form the Job keeps it in:
// EnvC, EnvOverride, and whether EnvC was asked for (see Env for what each case
// means).
//
// Copying it costs three fields, while decoding it costs a decompress and a
// decode of every variable, so a consumer that may not need the environment can
// carry this and decode only if it turns out to need it; see jobRunEnv.
type jobEnv struct {
	envC      []byte
	overrides []byte
	retrieved bool
}

// storedEnvLocked copies the Job's stored environment. The caller must hold at
// least the Job's read lock.
//
// Copying the slice headers is enough, since both are only ever replaced
// wholesale - EnvC by the retrieval that fills it, EnvOverride by
// EnvAddOverride.
func (j *Job) storedEnvLocked() jobEnv {
	return jobEnv{envC: j.EnvC, overrides: j.EnvOverride, retrieved: j.EnvCRetrieved}
}

// decode is Env for an environment already copied out of a Job.
func (e jobEnv) decode() ([]string, error) {
	if envDecodeHook != nil {
		envDecodeHook()
	}

	overrideEs, err := decodeCompressedEnv(e.overrides)
	if err != nil {
		return nil, err
	}

	if len(e.envC) == 0 {
		if !e.retrieved {
			return nil, nil
		}

		return applyEnvOverrides(os.Environ(), overrideEs), nil
	}

	env, err := e.decodeStored()
	if err != nil {
		return nil, err
	}

	return applyEnvOverrides(env, overrideEs), nil
}

// decodeStored decodes e.envC, falling back to the current environment if a nil
// environment was stored.
func (e jobEnv) decodeStored() ([]string, error) {
	env, err := decodeCompressedEnv(e.envC)
	if err != nil {
		return nil, err
	}

	if env == nil {
		return os.Environ(), nil
	}

	return env, nil
}

// decodeCompressedEnv decompresses and decodes an environment stored by
// compressEnv, answering nil for a blank one.
func decodeCompressedEnv(compressed []byte) ([]string, error) {
	if len(compressed) == 0 {
		return nil, nil
	}

	decompressed, err := decompress(compressed)
	if err != nil {
		return nil, err
	}

	ch := new(codec.BincHandle)
	dec := codec.NewDecoderBytes(decompressed, ch)
	es := &envStr{}

	if err = dec.Decode(es); err != nil {
		return nil, err
	}

	return es.Environ, nil
}

// applyEnvOverrides applies overrideEs to env if there are any, returning the
// result.
func applyEnvOverrides(env, overrideEs []string) []string {
	if len(overrideEs) > 0 {
		return envOverride(env, overrideEs)
	}

	return env
}

// envCurrentOverrides decompresses and decodes any existing EnvOverride.
func (j *Job) envCurrentOverrides() ([]string, error) {
	return decodeCompressedEnv(j.envOverrideSnapshot())
}

func (j *Job) envOverrideSnapshot() []byte {
	j.RLock()
	defer j.RUnlock()

	return slices.Clone(j.EnvOverride)
}

// EnvAddOverride adds additional overrides to the jobs existing overrides (if
// any). These will then get used to determine the final value of Env(). NB:
// This does not do any updates to a job on the server if called from a client,
// but is suitable for altering a job's environment prior to calling
// Client.Execute().
func (j *Job) EnvAddOverride(env []string) error {
	current, err := j.envCurrentOverrides()
	if err != nil {
		return err
	}

	compressed, err := compressEnv(envOverride(current, env))
	if err != nil {
		return err
	}

	j.Lock()
	j.EnvOverride = compressed
	j.Unlock()

	return nil
}

// Getenv is like os.Getenv(), but for the environment variables stored in the
// job, including any overrides. Returns blank if Env() would have returned
// an error.
func (j *Job) Getenv(key string) string {
	env, err := j.Env()
	if err != nil {
		return ""
	}

	for _, envvar := range env {
		pair := strings.Split(envvar, "=")
		if pair[0] == key {
			return pair[1]
		}
	}

	return ""
}

// StdOut returns the decompressed job.StdOutC, which is the head and tail of
// job.Cmd's STDOUT when it ran. If the Cmd hasn't run yet, or if it output
// nothing to STDOUT, you will get an empty string. Note that StdOutC is only
// populated if you got the Job from GetByCmd(_, true), and if the Job's Cmd ran
// but failed.
func (j *Job) StdOut() (string, error) {
	if len(j.StdOutC) == 0 {
		return "", nil
	}

	decomp, err := decompress(j.StdOutC)
	if err != nil {
		return "", err
	}

	return string(decomp), err
}

// StdErr returns the decompressed job.StdErrC, which is the head and tail of
// job.Cmd's STDERR when it ran. If the Cmd hasn't run yet, or if it output
// nothing to STDERR, you will get an empty string. Note that StdErrC is only
// populated if you got the Job from GetByCmd(_, true), and if the Job's Cmd ran
// but failed.
func (j *Job) StdErr() (string, error) {
	if len(j.StdErrC) == 0 {
		return "", nil
	}

	decomp, err := decompress(j.StdErrC)
	if err != nil {
		return "", err
	}

	return string(decomp), err
}

// TriggerBehaviours triggers this Job's Behaviours based on if its Cmd got
// executed successfully or not. Should only be called as part of or after
// Execute().
func (j *Job) TriggerBehaviours(success bool) error {
	return j.Behaviours.Trigger(success, j)
}

// RemovalRequested tells you if this Job's Behaviours include the 'Remove' one.
func (j *Job) RemovalRequested() bool {
	return j.Behaviours.RemovalRequested()
}

// unmountOnError unmounts this Job's filesystems (discarding logs) following an
// error, folding any unmount failure into the given error.
func (j *Job) unmountOnError(err error) error {
	_, erru := j.Unmount()
	if erru != nil {
		return fmt.Errorf("%w (and the unmount failed: %w)", err, erru)
	}

	return err
}

// Mount uses the Job's MountConfigs to mount the remote file systems at the
// desired mount points. If a mount point is unspecified, mounts in the sub
// folder Cwd/mnt if CwdMatters (and unspecified CacheBase becomes Cwd),
// otherwise the actual working directory is used as the mount point (and the
// parent of that used for unspecified CacheBase). Relative CacheDir options
// are treated relative to the CacheBase.
//
// If the optional onCwd argument is supplied true, and ActualCwd is not
// defined, then instead of mounting at j.Cwd/mnt, it tries to mount at j.Cwd
// itself. (This will fail if j.Cwd is not empty or already mounted by another
// process.)
//
// Returns any non-shared cache directories, and any directories in (or at) the
// job's actual cwd if anything was mounted there, for the purpose of knowing
// what directories to check and not check for disk usage.
func (j *Job) Mount(onCwd ...bool) ([]string, []string, error) {
	ms := &mountState{job: j}
	cwd, defaultMount, defaultCacheBase := j.mountBaseDirs(onCwd)

	for _, mc := range j.MountConfigs {
		if err := ms.mountConfig(mc, cwd, defaultMount, defaultCacheBase); err != nil {
			return ms.cacheDirs, ms.mountedDirs, err
		}
	}

	// unmount all on death without trying to upload
	if len(j.mountedFS) > 0 {
		j.unmountOnDeath()
	}

	return ms.cacheDirs, ms.mountedDirs, nil
}

// mountBaseDirs determines the cwd, default mount point and default cache base
// to use for Mount, based on the Job's Cwd/ActualCwd and the onCwd argument.
func (j *Job) mountBaseDirs(onCwd []bool) (cwd, defaultMount, defaultCacheBase string) {
	cwd = j.Cwd
	defaultMount = filepath.Join(j.Cwd, "mnt")
	defaultCacheBase = cwd

	// a CwdMatters Job runs in the user's own Cwd, so it has no wr-created
	// working directory to mount in or cache beside, whatever ActualCwd says: a
	// Job persisted by wr v0.37.0|1 can have it set to Cwd, and then the cache
	// base would become the parent of the user's own Cwd.
	if created := j.createdCwd(); created != "" {
		cwd = created
		defaultMount = cwd
		defaultCacheBase = filepath.Dir(cwd)
	} else if len(onCwd) == 1 && onCwd[0] {
		defaultMount = j.Cwd
		defaultCacheBase = filepath.Dir(j.Cwd)
	}

	return cwd, defaultMount, defaultCacheBase
}

// unmountOnDeath arranges for all of the Job's mounted filesystems to be
// unmounted (without uploading) if the process is interrupted or terminated.
func (j *Job) unmountOnDeath() {
	const deathSignalBuffer = 2 // we listen for os.Interrupt and syscall.SIGTERM

	deathSignals := make(chan os.Signal, deathSignalBuffer)

	signal.Notify(deathSignals, os.Interrupt, syscall.SIGTERM)
	// (we can't use each fs.UnmountOnDeath() function because that tries to
	// upload, but if we get killed we don't want that)
	go func() {
		<-deathSignals

		var merr *multierror.Error

		for _, fs := range j.mountedFS {
			erru := fs.Unmount(true)
			if erru != nil {
				merr = multierror.Append(merr, erru)
			}
		}

		if len(merr.Errors) > 0 {
			panic(merr)
		}
	}()
}

// mountState accumulates the cache and mount directories created while a Job
// mounts its MountConfigs.
type mountState struct {
	job         *Job
	cacheDirs   []string
	mountedDirs []string
}

// mountConfig mounts a single MountConfig, appending any unique cache/mount
// dirs it creates to the mountState. On error it unmounts everything mounted so
// far (folding any unmount failure into the returned error).
func (ms *mountState) mountConfig(mc MountConfig, cwd, defaultMount, defaultCacheBase string) error {
	rcs, err := ms.buildRemoteConfigs(mc, defaultCacheBase)
	if err != nil {
		return ms.job.unmountOnError(err)
	}

	if len(rcs) == 0 {
		return ms.job.unmountOnError(errNoTargets)
	}

	mount := ms.resolveMount(mc.Mount, cwd, defaultMount)

	cfg := &muxfys.Config{
		Mount:     mount,
		CacheBase: resolveCacheBase(mc.CacheBase, cwd, defaultCacheBase),
		Retries:   mountRetries(mc),
		Verbose:   mc.Verbose,
	}

	fs, err := muxfys.New(cfg)
	if err != nil {
		return ms.job.unmountOnError(err)
	}

	if err = fs.Mount(rcs...); err != nil {
		return ms.job.unmountOnError(err)
	}

	ms.job.mountedFS = append(ms.job.mountedFS, fs)

	return nil
}

// buildRemoteConfigs builds the muxfys RemoteConfigs for a MountConfig's
// Targets, appending any unique cache dirs to the mountState.
func (ms *mountState) buildRemoteConfigs(mc MountConfig, defaultCacheBase string) ([]*muxfys.RemoteConfig, error) {
	var rcs []*muxfys.RemoteConfig

	for _, mt := range mc.Targets {
		accessorConfig, err := muxfys.S3ConfigFromEnvironment(mt.Profile, mt.Path)
		if err != nil {
			return nil, err
		}

		accessor, err := muxfys.NewS3Accessor(accessorConfig)
		if err != nil {
			return nil, err
		}

		rcs = append(rcs, &muxfys.RemoteConfig{
			Accessor:  accessor,
			CacheData: mt.Cache,
			CacheDir:  ms.resolveCacheDir(mt.CacheDir, defaultCacheBase),
			Write:     mt.Write,
		})
	}

	return rcs, nil
}

// resolveCacheDir resolves a target's CacheDir relative to defaultCacheBase. An
// unset CacheDir (muxfys then chooses its own dir inside the CacheBase) or an
// absolute one is returned as given.
//
// It is a function as well as a mountState method because cleanup must work out
// the same locations to know what it must not delete, and a second derivation of
// where a cache lands is a second chance to get it wrong.
func resolveCacheDir(cacheDir, defaultCacheBase string) string {
	if cacheDir == "" || filepath.IsAbs(cacheDir) {
		// *** else, the cache is in a unique dir that I don't know about?
		return cacheDir
	}

	return filepath.Join(defaultCacheBase, cacheDir)
}

// resolveCacheDir resolves a target's CacheDir, recording it as a unique cache
// dir when it is relative - which is exactly when resolving it changed it.
func (ms *mountState) resolveCacheDir(cacheDir, defaultCacheBase string) string {
	resolved := resolveCacheDir(cacheDir, defaultCacheBase)
	if resolved != cacheDir {
		// *** we should only set this if not writing, or if writing to a
		// non-empty dir, which we don't know about at this point...
		ms.cacheDirs = append(ms.cacheDirs, resolved)
	}

	return resolved
}

// resolveMount resolves a MountConfig's mount point relative to cwd (or the
// default), recording it as a unique mounted dir when appropriate.
func (ms *mountState) resolveMount(mcMount, cwd, defaultMount string) string {
	mount := resolveMountPoint(mcMount, cwd, defaultMount)

	if !filepath.IsAbs(mcMount) {
		ms.mountedDirs = append(ms.mountedDirs, mount)
	}

	return mount
}

// resolveCacheBase resolves a MountConfig's CacheBase relative to cwd, or
// returns the default.
func resolveCacheBase(mcCacheBase, cwd, defaultCacheBase string) string {
	if mcCacheBase == "" {
		return defaultCacheBase
	}

	if filepath.IsAbs(mcCacheBase) {
		return mcCacheBase
	}

	return filepath.Join(cwd, mcCacheBase)
}

// mountRetries returns the number of mount retries to use for a MountConfig.
func mountRetries(mc MountConfig) int {
	if mc.Retries > 0 {
		return mc.Retries
	}

	return defaultMountRetries
}

// Unmount unmounts any remote filesystems that were previously mounted with
// Mount(), returning a string of any log messages generated during the mount.
// Returns nil error if Mount() had not been called or there were no
// MountConfigs.
//
// Note that for cached writable mounts, created files will only begin to upload
// once Unmount() is called, so this may take some time to return. Supply true
// to disable uploading of files (eg. if you're unmounting following an error).
// If uploading, error could contain the string "failed to upload", which you
// may want to check for. On success, triggers the deletion of any empty
// directories between the mount point(s) and Cwd if not CwdMatters and the
// mount point was (within) ActualCwd.
func (j *Job) Unmount(stopUploads ...bool) (logs string, err error) {
	// j.Lock()
	// defer j.Unlock()
	doNotUpload := len(stopUploads) == 1 && stopUploads[0]

	logs, merr := j.unmountAll(doNotUpload)

	if err = merr.ErrorOrNil(); err != nil {
		return logs, fmt.Errorf("Unmount failure(s): %w", err)
	}

	// delete any empty dirs; which of them are ours is left entirely to the
	// workspace resolution, which excludes a CwdMatters Job even if it has an
	// ActualCwd, because wr created no directory for one.
	err = j.rmEmptyMountDirs()

	return logs, err
}

// unmountAll unmounts all of the Job's mounted filesystems, returning their
// joined logs and any unmount errors.
func (j *Job) unmountAll(doNotUpload bool) (string, *multierror.Error) {
	var (
		merr    *multierror.Error
		allLogs []string
	)

	for _, fs := range j.mountedFS {
		if uerr := fs.Unmount(doNotUpload); uerr != nil {
			merr = multierror.Append(merr, uerr)
		}

		if theseLogs := fs.Logs(); len(theseLogs) > 0 {
			allLogs = append(allLogs, theseLogs...)
		}
	}

	j.mountedFS = nil

	var logs string
	if len(allLogs) > 0 {
		logs = strings.TrimSpace(strings.Join(allLogs, ""))
	}

	return logs, merr
}

// rmEmptyMountDirs deletes any empty directories between the Job's mount
// point(s) and its Cwd. It returns the error from the last cleanup attempted
// (matching the original Unmount behaviour).
//
// Which dirs it may walk is decided by the same resolution Behaviour.cleanup
// uses, so the two cannot disagree about what wr created.
//
// A refusal from that resolution is swallowed rather than reported, because an
// error returned here fails the job itself, and a tidy-up that found nothing of
// ours to tidy is not a failed unmount.
func (j *Job) rmEmptyMountDirs() error {
	// answered before the resolution, not after, because Unmount is on the exit
	// path of every Job that runs and most have no mounts at all: the resolution
	// costs the hash Key() makes of the Job and an lstat per component of the
	// path below Cwd, only to find no mount point to walk up from.
	if !j.hasMounts() {
		return nil
	}

	ws, err := j.workSpaceSnapshot().resolveWorkSpace()
	if err != nil || ws == nil {
		return nil
	}
	defer ws.Close()

	return ws.rmEmptyMountDirs()
}

// hasMounts reports whether the Job has any MountConfigs, read under its read
// lock as workSpaceSnapshot reads them: the field is only ever replaced
// wholesale, so the slice header is all this has to see, and the lock is
// released before anything walks the filesystem.
//
// A Job with none has no mount points either, whatever its directories are:
// keptDirs.mountPoints is filled from workSpacePaths.mountPoints, which resolves
// exactly one point per MountConfig.
func (j *Job) hasMounts() bool {
	j.RLock()
	defer j.RUnlock()

	return len(j.MountConfigs) > 0
}

// ToEssense converts a Job to its matching JobEssense, taking less space and
// being required as input for certain methods.
func (j *Job) ToEssense() *JobEssence {
	return &JobEssence{JobKey: j.Key()}
}

// noteIncrementedLimitGroups should be used after incrementing limit groups for
// this job. It takes the groups you actually just incremented (as opposed to
// the Job's current LimitGroups), and stores them for decrementing during
// updateAfterExit(). This avoids any issues with the Job's LimitGroups being
// changed between these 2 calls (or between you incrementing and reserving the
// job). The twinned noteIncrementedLimitGroups() and decrementLimitGroups()
// calls ensure we don't decrement groups more times than we incremented them.
func (j *Job) noteIncrementedLimitGroups(groups []string) {
	j.Lock()
	defer j.Unlock()

	j.incrementedLimitGroups = groups
}

// updateAfterExit sets some properties on the job, only if the supplied
// JobEndState indicates the job exited, and if the job wasn't already exited.
// It also calls decrementLimitGroups().
func (j *Job) updateAfterExit(jes *JobEndState, lim *limiter.Limiter) {
	j.RLock()

	if j.Exited {
		j.RUnlock()

		return
	}

	j.RUnlock()
	j.decrementLimitGroups(lim)

	if jes == nil || !jes.Exited {
		return
	}

	j.Lock()
	j.Exited = true
	j.Exitcode = jes.Exitcode
	j.PeakRAM = jes.PeakRAM
	j.PeakDisk = jes.PeakDisk
	j.CPUtime = jes.CPUtime

	j.EndTime = jes.EndTime
	j.setActualCwd(jes.Cwd)
	j.Unlock()
}

// decrementLimitGroups decrements any limit groups of this job that had been
// passed to noteIncrementedLimitGroups(), and then empties that note to make
// multiple calls to this method safe in terms of decrementing.
func (j *Job) decrementLimitGroups(lim *limiter.Limiter) {
	j.Lock()
	defer j.Unlock()

	j.decrementLimitGroupsLocked(lim)
}

// Key calculates a unique key to describe the job.
func (j *Job) Key() string {
	mountKey := j.MountConfigs.Key()

	var image string

	if j.WithDocker != "" {
		image = "docker:" + j.WithDocker
	} else if j.WithSingularity != "" {
		image = "singularity:" + j.WithSingularity
	}

	// Build the same concatenation that the previous fmt.Sprintf calls
	// produced, byte-for-byte, but with a single preallocated buffer to avoid
	// the per-component reflection-based formatting and intermediate strings on
	// this hot path. The layout is, in order:
	//   [Cwd "."]  Cmd "." mountKey  ["." image "." ContainerMounts]
	// where the Cwd prefix is only present when CwdMatters and the image suffix
	// only when a container image is in use.
	size := len(j.Cmd) + 1 + len(mountKey)
	if j.CwdMatters {
		size += len(j.Cwd) + 1
	}

	if image != "" {
		size += 1 + len(image) + 1 + len(j.ContainerMounts)
	}

	var concat strings.Builder

	concat.Grow(size)

	if j.CwdMatters {
		concat.WriteString(j.Cwd)
		concat.WriteByte('.')
	}

	concat.WriteString(j.Cmd)
	concat.WriteByte('.')
	concat.WriteString(mountKey)

	if image != "" {
		concat.WriteByte('.')
		concat.WriteString(image)
		concat.WriteByte('.')
		concat.WriteString(j.ContainerMounts)
	}

	return byteKey([]byte(concat.String()))
}

// generateSchedulerGroup returns a stringified form of the given requirements,
// appended with a standard form of the current limit groups of this job. We
// assume that LimitGroups was sorted and deduplicated when it was set on the
// job (this happens in server.createJobs()).
func (j *Job) generateSchedulerGroup(req *scheduler.Requirements) string {
	return schedulerGroupString(req, j.LimitGroups)
}

type schedulerGroupSnapshot struct {
	key           string
	requirements  *scheduler.Requirements
	previousGroup string
	group         string
	priority      uint8
}

// schedulerGroupSnapshot returns the cheap per-rac-cycle view of this job: its
// memoised derived strings (see jobDerived), plus its live priority and current
// scheduler group. The memo is computed on first use and after any invalidation,
// under this job's own write lock; every later call takes only its read lock, so
// there is no server-wide lock on this path.
func (j *Job) schedulerGroupSnapshot() schedulerGroupSnapshot {
	if snapshot, memoised := j.memoisedSchedulerGroupSnapshot(); memoised {
		return snapshot
	}

	j.Lock()
	defer j.Unlock()

	return j.snapshotWithDerived(j.derivedLocked())
}

func schedulerGroupString(req *scheduler.Requirements, limitGroups []string) string {
	var lgs string
	if len(limitGroups) > 0 {
		lgs = jobSchedLimitGroupSeparator + strings.Join(limitGroups, jobLimitGroupSeparator)
	}

	return req.Stringify() + lgs
}

// getSchedulerGroup provides a thread-safe way of getting the schedulerGroup
// property of a Job.
func (j *Job) getSchedulerGroup() string {
	j.RLock()
	defer j.RUnlock()

	return j.schedulerGroup
}

// setSchedulerGroup provides a thread-safe way of setting the schedulerGroup
// property of a Job.
func (j *Job) setSchedulerGroup(newval string) {
	j.Lock()
	defer j.Unlock()

	j.schedulerGroup = newval
}

func (j *Job) setWaitingForDepGroups(depGroups []string) {
	j.Lock()
	defer j.Unlock()

	if len(depGroups) == 0 {
		j.WaitingForDepGroups = nil

		return
	}

	j.WaitingForDepGroups = append([]string(nil), depGroups...)
}

// jobStatusStreams holds the stderr, stdout, environment and environment
// overrides gathered for a JStatus.
type jobStatusStreams struct {
	stderr       string
	stdout       string
	env          []string
	envOverrides []string
}

// ToStatus converts a job to a simplified JStatus, useful for output as JSON.
func (j *Job) ToStatus() (JStatus, error) {
	streams, err := j.statusStreams()
	if err != nil {
		return JStatus{}, err
	}

	j.RLock()
	defer j.RUnlock()

	leaf, err := cwdLeaf(j.Cwd, j.createdCwd())
	if err != nil {
		return JStatus{}, err
	}

	return j.buildJStatus(streams, leaf), nil
}

// buildJStatus assembles a JStatus from the job and the already-gathered
// streams and cwd leaf. Must be called with at least an RLock held.
//
//nolint:funlen // a flat field-by-field mapping of the many-fielded Job struct
func (j *Job) buildJStatus(streams jobStatusStreams, leaf string) JStatus {
	state := j.State
	if state == JobStateRunning && j.Lost {
		state = JobStateLost
	}

	js := JStatus{
		Key:                 j.Key(),
		RepGroup:            j.RepGroup,
		ReqGroup:            j.ReqGroup,
		LimitGroups:         j.limitGroupsForStatus(),
		DepGroups:           j.DepGroups,
		Dependencies:        j.Dependencies.Stringify(),
		WaitingForDepGroups: j.WaitingForDepGroups,
		Modules:             j.Modules,
		Cmd:                 j.Cmd,
		State:               state,
		CwdBase:             j.Cwd,
		Cwd:                 leaf,
		HomeChanged:         j.ChangeHome,
		Behaviours:          j.Behaviours.String(),
		Mounts:              j.MountConfigs.String(),
		MonitorDocker:       j.MonitorDocker,
		WithDocker:          j.WithDocker,
		WithSingularity:     j.WithSingularity,
		ContainerMounts:     j.ContainerMounts,
		ExpectedRAM:         j.Requirements.RAM,
		ExpectedTime:        j.Requirements.Time.Seconds(),
		RequestedDisk:       j.Requirements.Disk,
		EnvOverrides:        streams.envOverrides,
		OtherRequests:       j.otherRequests(),
		Cores:               j.Requirements.Cores,
		NoRetryOverWalltime: j.NoRetriesOverWalltime.Seconds(),
		PeakRAM:             j.PeakRAM,
		PeakDisk:            j.PeakDisk,
		Exited:              j.Exited,
		Exitcode:            j.Exitcode,
		FailReason:          j.FailReason,
		Pid:                 j.Pid,
		Host:                j.Host,
		HostID:              j.HostID,
		HostIP:              j.HostIP,
		SSHCommand:          sshCommandForRunningJob(state, j.Requirements, j.Host, j.HostIP, j.workingDir()),
		Walltime:            j.WallTime().Seconds(),
		CPUtime:             j.CPUtime.Seconds(),
		Attempts:            j.Attempts,
		Similar:             j.Similar,
		Override:            j.Override,
		Priority:            j.Priority,
		Retries:             j.Retries,
		CwdMatters:          j.CwdMatters,
		StdErr:              streams.stderr,
		StdOut:              streams.stdout,
		Env:                 streams.env,
		Started:             unixNanoPtr(j.StartTime),
		Ended:               unixNanoPtr(j.EndTime),
	}

	return js
}

// limitGroupsForStatus returns the limit groups to show in a JStatus,
// preferring the display variant if set.
func (j *Job) limitGroupsForStatus() []string {
	if len(j.LimitGroupsForDisplay) > 0 {
		return j.LimitGroupsForDisplay
	}

	return j.LimitGroups
}

// otherRequests returns the Requirements.Other map as a slice of "key:val"
// strings.
func (j *Job) otherRequests() []string {
	ot := make([]string, 0, len(j.Requirements.Other))
	for key, val := range j.Requirements.Other {
		ot = append(ot, key+":"+val)
	}

	return ot
}

// statusStreams gathers the stderr, stdout, environment and environment
// overrides needed to build a JStatus.
func (j *Job) statusStreams() (jobStatusStreams, error) {
	var (
		streams jobStatusStreams
		err     error
	)

	if streams.stderr, err = j.StdErr(); err != nil {
		return streams, err
	}

	if streams.stdout, err = j.StdOut(); err != nil {
		return streams, err
	}

	if streams.env, err = j.Env(); err != nil {
		return streams, err
	}

	streams.envOverrides, err = j.envCurrentOverrides()

	return streams, err
}

// runToken identifies ONE run of a Job. The manager mints it at every
// reservation (resetJobForReservation) and never lets it out: it is only ever
// compared for equality with a token this manager minted. Counting from 1 keeps
// the zero token for a run recovered from the database, which it never reserved.
//
// The manager mints it rather than reading a field the runner reports, because
// nothing reported tells two runs of a job apart: Key() and every path below Cwd
// are the same for every run by construction, ActualCwd is blank until a run's
// first Touch (and always, for a cwd_matters run or a manager with no web port),
// and Attempts is a count that resetJobStatusFields puts back to 0.
//
// A run BEGINS AT RESERVE, so that is where the token is minted: making the
// working directory, mounting remote filesystems and starting the Cmd all happen
// after the reservation and before the runner's Started reaches the manager, so
// minting at Started left that window carrying the previous run's token, and a
// confirmation of an earlier loss killed a Cmd that was already executing.
type runToken uint64

// isLostRunLocked says whether this Job is still lost, and still the RUN the
// given token was minted for. The caller must hold at least the Job's read lock.
//
// Both halves are needed: a job that recovered on a touch is not lost, and a job
// that was released and reserved again is neither lost nor that run, so a decision
// pinned to the earlier run is refused from the moment the retry exists.
func (j *Job) isLostRunLocked(run runToken) bool {
	return j.Lost && j.runID == run
}

// setActualCwd records cwd as the unique working directory that wr created below
// Cwd for this Job's Cmd. A blank cwd is ignored, and so is any cwd for a
// CwdMatters Job: that Cmd runs in the user's own Cwd, and a blank ActualCwd is
// how the rest of wr knows there is no wr-created directory to delete. Must be
// called with the Job locked.
func (j *Job) setActualCwd(cwd string) {
	if cwd == "" || j.CwdMatters {
		return
	}

	j.ActualCwd = cwd
}

// createdCwd returns the unique working directory wr created for this Job below
// Cwd, or "" if wr created none.
//
// It is "" for a CwdMatters Job whatever ActualCwd says, because wr creates no
// directory for one. setActualCwd refuses to write ActualCwd on such a Job, but
// one persisted by wr v0.37.0|1 can still be read back carrying Cwd there, and
// every caller of this is deciding something about a directory wr owns. Must be
// called with at least an RLock held.
func (j *Job) createdCwd() string {
	if j.CwdMatters {
		return ""
	}

	return j.ActualCwd
}

// workingDir returns the directory this Job's Cmd runs in, or "" if that isn't
// known yet: Cwd when CwdMatters, since then the Cmd runs in Cwd itself, and
// otherwise ActualCwd, the unique working directory wr created below Cwd. A
// non-CwdMatters Job that has yet to report an ActualCwd gets "" rather than
// Cwd, because its Cmd runs in a unique directory below Cwd, not in Cwd.
//
// CwdMatters is checked FIRST so that ActualCwd is ignored entirely on such a
// Job rather than only when it is empty, since a Job persisted by v0.37.x can be
// read back carrying Cwd there and this is what wr displays and offers to ssh
// to. Must be called with at least an RLock held.
func (j *Job) workingDir() string {
	if j.CwdMatters {
		return j.Cwd
	}

	return j.createdCwd()
}

// unixNanoPtr returns a pointer to t's UnixNano value, or nil if t is the zero
// time.
func unixNanoPtr(t time.Time) *int64 {
	if t.IsZero() {
		return nil
	}

	i := t.UnixNano()

	return &i
}

// JobEssence struct describes the essential aspects of a Job that make it
// unique, used to describe a Job when eg. you want to search for one.
type JobEssence struct {
	// JobKey can be set by itself if you already know the "key" of the desired
	// job; you can get these keys when you use GetByRepGroup() or
	// GetIncomplete() with a limit. When this is set, other properties are
	// ignored.
	JobKey string

	// Cmd always forms an essential part of a Job.
	Cmd string

	// Cwd should only be set if the Job was created with CwdMatters = true.
	Cwd string

	// Mounts should only be set if the Job was created with Mounts
	MountConfigs MountConfigs
}

// Key returns the same value that key() on the matching Job would give you.
func (j *JobEssence) Key() string {
	if j.JobKey != "" {
		return j.JobKey
	}

	mountKey := j.MountConfigs.Key()

	// Build the same byte slice the previous fmt.Appendf calls produced,
	// byte-for-byte, but via a single preallocated buffer rather than
	// reflection-based formatting: "Cmd.mountKey", optionally prefixed with
	// "Cwd." when a Cwd is set.
	size := len(j.Cmd) + 1 + len(mountKey)
	if j.Cwd != "" {
		size += len(j.Cwd) + 1
	}

	concat := make([]byte, 0, size)

	if j.Cwd != "" {
		concat = append(concat, j.Cwd...)
		concat = append(concat, '.')
	}

	concat = append(concat, j.Cmd...)
	concat = append(concat, '.')
	concat = append(concat, mountKey...)

	return byteKey(concat)
}

// Stringify returns a nice printable form of a JobEssence.
func (j *JobEssence) Stringify() string {
	if j.JobKey != "" {
		return j.JobKey
	}

	out := j.Cmd
	if j.Cwd != "" {
		out += " [" + j.Cwd + "]"
	}

	return out
}

// JobModifier has the same settable properties as Job, but also has Set*()
// methods that record which properties you have explicitly set, allowing its
// Modify() method to know what you wanted to change, including changing to
// default, without changing to default for properties you wanted to leave
// alone. The only thing you can't set is RepGroup. The methods on this struct
// are not thread safe. Do not set any of the properties directly yourself.
type JobModifier struct {
	EnvOverride              []byte
	LimitGroups              []string
	Modules                  []string
	DepGroups                []string
	Dependencies             Dependencies
	Behaviours               Behaviours
	MountConfigs             MountConfigs
	Cmd                      string
	Cwd                      string
	ReqGroup                 string
	Group                    string
	BsubMode                 string
	MonitorDocker            string
	WithDocker               string
	WithSingularity          string
	ContainerMounts          string
	Requirements             *scheduler.Requirements
	CwdMatters               bool
	CwdMattersSet            bool
	ChangeHome               bool
	ChangeHomeSet            bool
	ReqGroupSet              bool
	GroupSet                 bool
	Override                 uint8
	OverrideSet              bool
	Priority                 uint8
	PrioritySet              bool
	Retries                  uint8
	RetriesSet               bool
	NoRetriesOverWalltime    time.Duration
	NoRetriesOverWalltimeSet bool
	EnvOverrideSet           bool
	LimitGroupsSet           bool
	ModulesSet               bool
	DepGroupsSet             bool
	DependenciesSet          bool
	BehavioursSet            bool
	MountConfigsSet          bool
	BsubModeSet              bool
	MonitorDockerSet         bool
	WithDockerSet            bool
	WithSingularitySet       bool
	ContainerMountsSet       bool
}

// NewJobModifer is a convenience for making a new JobModifer, that you can call
// various Set*() methods on before using Modify() to modify a Job.
func NewJobModifer() *JobModifier {
	return &JobModifier{}
}

// SetCmd notes that you want to modify the command line of Jobs to the given
// cmd. You can't modify to an empty command, so if cmd is blank, no set is
// done.
func (j *JobModifier) SetCmd(cmd string) {
	j.Cmd = cmd
}

// SetCwd notes that you want to modify the cwd of Jobs to the given cwd. You
// can't modify to an empty cwd, so if cwd is blank, no set is done.
func (j *JobModifier) SetCwd(cwd string) {
	j.Cwd = cwd
}

// SetCwdMatters notes that you want to modify the CwdMatters of Jobs.
func (j *JobModifier) SetCwdMatters(newVal bool) {
	j.CwdMatters = newVal
	j.CwdMattersSet = true
}

// SetChangeHome notes that you want to modify the ChangeHome of Jobs.
func (j *JobModifier) SetChangeHome(newVal bool) {
	j.ChangeHome = newVal
	j.ChangeHomeSet = true
}

// SetReqGroup notes that you want to modify the ReqGroup of Jobs.
func (j *JobModifier) SetReqGroup(newVal string) {
	j.ReqGroup = newVal
	j.ReqGroupSet = true
}

func (j *JobModifier) SetUnixGroup(group string) {
	j.Group = group
	j.GroupSet = true
}

// SetRequirements notes that you want to modify the Requirements of Jobs. You
// can't modify to a nil Requirements, so if req is nil, no set is done.
//
// NB: If you want to change Cores, Disk or Other, you must set CoresSet,
// DiskSet and OtherSet booleans to true, respectively.
func (j *JobModifier) SetRequirements(req *scheduler.Requirements) {
	j.Requirements = req
}

// SetOverride notes that you want to modify the Override of Jobs.
func (j *JobModifier) SetOverride(newVal uint8) {
	j.Override = newVal
	j.OverrideSet = true
}

// SetPriority notes that you want to modify the Priority of Jobs.
func (j *JobModifier) SetPriority(newVal uint8) {
	j.Priority = newVal
	j.PrioritySet = true
}

// SetRetries notes that you want to modify the Retries of Jobs.
func (j *JobModifier) SetRetries(newVal uint8) {
	j.Retries = newVal
	j.RetriesSet = true
}

// SetNoRetriesOverWalltime notes that you want to modify the
// NoRetriesOverWalltime of Jobs.
func (j *JobModifier) SetNoRetriesOverWalltime(newVal time.Duration) {
	j.NoRetriesOverWalltime = newVal
	j.NoRetriesOverWalltimeSet = true
}

// SetEnvOverride notes that you want to modify the EnvOverride of Jobs. The
// supplied string should be a comma separated list of key=value pairs. This can
// generate an error if compression of the data fails.
func (j *JobModifier) SetEnvOverride(newVal string) error {
	var compressedEnv []byte

	if newVal != "" {
		var err error

		compressedEnv, err = compressEnv(strings.Split(newVal, ","))
		if err != nil {
			return err
		}
	}

	j.EnvOverride = compressedEnv
	j.EnvOverrideSet = true

	return nil
}

func (j *JobModifier) setEnvOverrideValues(newVal []string) error {
	var compressedEnv []byte

	if len(newVal) > 0 {
		var err error

		compressedEnv, err = compressEnv(newVal)
		if err != nil {
			return err
		}
	}

	j.EnvOverride = compressedEnv
	j.EnvOverrideSet = true

	return nil
}

// SetLimitGroups notes that you want to modify the LimitGroups of Jobs.
func (j *JobModifier) SetLimitGroups(newVal []string) {
	j.LimitGroups = newVal
	j.LimitGroupsSet = true
}

// SetModules notes that you want to modify the Modules of Jobs.
func (j *JobModifier) SetModules(newVal []string) {
	j.Modules = newVal
	j.ModulesSet = true
}

// SetDepGroups notes that you want to modify the DepGroups of Jobs.
func (j *JobModifier) SetDepGroups(newVal []string) {
	j.DepGroups = newVal
	j.DepGroupsSet = true
}

// SetDependencies notes that you want to modify the Dependencies of Jobs.
func (j *JobModifier) SetDependencies(newVal Dependencies) {
	j.Dependencies = newVal
	j.DependenciesSet = true
}

// SetBehaviours notes that you want to modify the Behaviours of Jobs.
func (j *JobModifier) SetBehaviours(newVal Behaviours) {
	j.Behaviours = newVal
	j.BehavioursSet = true
}

// SetMountConfigs notes that you want to modify the MountConfigs of Jobs.
func (j *JobModifier) SetMountConfigs(newVal MountConfigs) {
	j.MountConfigs = newVal
	j.MountConfigsSet = true
}

// SetBsubMode notes that you want to modify the BsubMode of Jobs.
func (j *JobModifier) SetBsubMode(newVal string) {
	j.BsubMode = newVal
	j.BsubModeSet = true
}

// SetMonitorDocker notes that you want to modify the MonitorDocker of Jobs.
func (j *JobModifier) SetMonitorDocker(newVal string) {
	j.MonitorDocker = newVal
	j.MonitorDockerSet = true
}

// SetWithDocker notes that you want to modify the WithDocker of Jobs.
func (j *JobModifier) SetWithDocker(newVal string) {
	j.WithDocker = newVal
	j.WithDockerSet = true
}

// SetWithSingularity notes that you want to modify the WithSingularity of Jobs.
func (j *JobModifier) SetWithSingularity(newVal string) {
	j.WithSingularity = newVal
	j.WithSingularitySet = true
}

// SetContainerMounts notes that you want to modify the ContainerMounts of Jobs.
func (j *JobModifier) SetContainerMounts(newVal string) {
	j.ContainerMounts = newVal
	j.ContainerMountsSet = true
}

// validationError rejects nil entries in explicitly set pointer collections.
func (j *JobModifier) validationError() (Error, bool) {
	message := j.validationMessage()

	if message == "" {
		return Error{}, false
	}

	return Error{Op: requestMethodModify, Item: message, Err: ErrBadRequest}, true
}

func (j *JobModifier) validationMessage() string {
	if j == nil {
		return "modifier is nil"
	}

	if j.DependenciesSet {
		if index := slices.Index(j.Dependencies, nil); index >= 0 {
			return fmt.Sprintf("modifier.Dependencies[%d] is nil", index)
		}
	}

	if j.BehavioursSet {
		if index := slices.Index(j.Behaviours, nil); index >= 0 {
			return fmt.Sprintf("modifier.Behaviours[%d] is nil", index)
		}
	}

	return ""
}

// Modify takes existing jobs and modifies them all by setting the new values
// that you have previously set using the Set*() methods. Other values are left
// alone. Note that this could result in a Job's Key() changing.
//
// server is supplied to ensure we don't modify to the same key as another job.
//
// NB: this is only an in-memory change to the Jobs, so it is only meaningful
// for the Server to call this and then store changes in the database. You will
// also need to handle dependencies of a job changing.
//
// Returns a REVERSE mapping of new to old Job keys.
func (j *JobModifier) Modify(jobs []*Job, server *Server) (map[string]string, error) {
	if validationErr, invalid := j.validationError(); invalid {
		return nil, validationErr
	}

	keys := make(map[string]string)

	for _, job := range jobs {
		if err := j.modifyJob(job, server, keys); err != nil {
			return keys, err
		}
	}

	return keys, nil
}

// modifyJob applies the modifications to a single job (locking it for the
// duration), recording the new->old key mapping in keys. Jobs whose modified
// key would duplicate another job are left unchanged and not recorded.
func (j *JobModifier) modifyJob(job *Job, server *Server, keys map[string]string) error {
	job.Lock()
	defer job.Unlock()

	before := job.Key()

	skip, err := j.skipForDuplicateKey(job, server, keys, before)
	if err != nil || skip {
		return err
	}

	j.applyTo(job)

	keys[job.Key()] = before

	return nil
}

// skipForDuplicateKey works out whether modifying job would change its key into
// one already used by another job in this batch or in the queue/db, in which
// case the job should be skipped (skip=true) and left unchanged.
func (j *JobModifier) skipForDuplicateKey(job *Job, server *Server, keys map[string]string,
	before string) (skip bool, err error) {
	newKey := j.modifiedKey(job)
	if _, done := keys[newKey]; done {
		// duplicate of prior job in this loop, ignore
		return true, nil
	}

	if newKey == before {
		return false, nil
	}

	// check queue and db
	exists, err := server.checkJobByKey(newKey)
	if err != nil {
		return false, err
	}

	// duplicate of queued or complete job, ignore
	return exists, nil
}

// modifiedKey works out what job's Key() would become after modification,
// without actually modifying it.
func (j *JobModifier) modifiedKey(job *Job) string {
	newJob := &Job{
		Cmd:             job.Cmd,
		Cwd:             job.Cwd,
		CwdMatters:      job.CwdMatters,
		MountConfigs:    job.MountConfigs,
		WithDocker:      job.WithDocker,
		WithSingularity: job.WithSingularity,
		ContainerMounts: job.ContainerMounts,
	}

	j.overrideKeyCwd(newJob)
	j.overrideKeyContainer(newJob)

	return newJob.Key()
}

// overrideKeyCwd applies any set Cmd/Cwd/CwdMatters/MountConfigs modifications
// to newJob (the key-relevant cwd fields).
func (j *JobModifier) overrideKeyCwd(newJob *Job) {
	if j.Cmd != "" {
		newJob.Cmd = j.Cmd
	}

	if j.Cwd != "" {
		newJob.Cwd = j.Cwd
	}

	if j.CwdMattersSet {
		newJob.CwdMatters = j.CwdMatters
	}

	if j.MountConfigsSet {
		newJob.MountConfigs = j.MountConfigs
	}
}

// overrideKeyContainer applies any set container modifications to newJob (the
// key-relevant container fields).
func (j *JobModifier) overrideKeyContainer(newJob *Job) {
	if j.WithDockerSet {
		newJob.WithDocker = j.WithDocker
	}

	if j.WithSingularitySet {
		newJob.WithSingularity = j.WithSingularity
	}

	if j.ContainerMountsSet {
		newJob.ContainerMounts = j.ContainerMounts
	}
}

// applyTo applies all the set modifications to job in place, and then discards
// any ActualCwd the modifications have invalidated.
//
// ActualCwd is the working directory mkHashedDir built below Cwd from the job's
// Key(), and rebuilding that path from the key is exactly what licenses cleanup
// to sweep its parent and a `run` behaviour to execute in it. So the pair has to
// stay consistent, and Key() covers the Cmd, the MountConfigs and the container
// image, every one of which is modifiable here: a modification that changes the
// key leaves the stored path describing a job definition that no longer exists,
// and a blank ActualCwd - which cleanup treats as nothing to do - is then the
// true account of it.
//
// The test is the key itself rather than a clear in each Set* method, because the
// question is not which fields were touched but whether what was stored is still
// the path this job's definition builds. Clearing too eagerly leaks a workspace
// rather than pointing a deletion at something else. applyCmdCwd clears it for a
// Cwd change separately, since moving Cwd invalidates the path without changing
// the key.
//
// The caller must hold job's write lock (modifyJob does).
func (j *JobModifier) applyTo(job *Job) {
	keyBefore := job.Key()

	j.applyCmdCwd(job)
	j.applyGrouping(job)
	j.applyRequirements(job)
	j.applyScheduling(job)
	j.applyBehaviours(job)
	j.applyContainer(job)

	if job.Key() != keyBefore {
		job.ActualCwd = ""
	}

	// this is the one path that changes a live job's Key() and scheduler group
	// inputs, so the memoised derived strings must be recomputed.
	job.invalidateDerivedLocked()
}

// applyCmdCwd applies the Cmd/Cwd/CwdMatters/ChangeHome modifications to job.
func (j *JobModifier) applyCmdCwd(job *Job) {
	if j.Cmd != "" {
		job.Cmd = j.Cmd
	}

	// ActualCwd names a wr-created working directory below the Cwd the job last
	// ran in, and is what lets the cleanup behaviours treat its parent as a
	// disposable workspace. Setting a Cwd, or making the job run in Cwd itself,
	// means it has no such directory any more. (The clear can't be skipped when
	// the supplied Cwd equals the job's: cmd/mod.go passes the flag through
	// unnormalised, so they could name the same dir without matching.)
	//
	// Neither clear is covered by applyTo's key check: a Cwd change on a job
	// where Cwd is not part of the key changes no key, and nor does a
	// --cwd_matters on a job that already had it set - which is the job wr
	// v0.37.0|1 persisted with ActualCwd poisoned to Cwd, and this is where a
	// user clears it.
	if j.Cwd != "" {
		job.Cwd = j.Cwd
		job.ActualCwd = ""
	}

	if j.CwdMattersSet {
		job.CwdMatters = j.CwdMatters

		if j.CwdMatters {
			job.ActualCwd = ""
		}
	}

	if j.ChangeHomeSet {
		job.ChangeHome = j.ChangeHome
	}
}

// applyGrouping applies the group/module/dependency modifications to job.
func (j *JobModifier) applyGrouping(job *Job) {
	if j.ReqGroupSet {
		job.ReqGroup = j.ReqGroup
	}

	if j.GroupSet {
		job.Group = j.Group
	}

	if j.ModulesSet {
		job.Modules = j.Modules
	}

	if j.DepGroupsSet {
		job.DepGroups = j.DepGroups
	}

	if j.DependenciesSet {
		job.Dependencies = j.Dependencies
	}
}

// applyRequirements applies any set scheduler Requirements modifications to job.
func (j *JobModifier) applyRequirements(job *Job) {
	if j.Requirements == nil {
		return
	}

	if j.Requirements.RAM != 0 {
		job.Requirements.RAM = j.Requirements.RAM
	}

	if j.Requirements.Time != 0 {
		job.Requirements.Time = j.Requirements.Time
	}

	if j.Requirements.CoresSet {
		job.Requirements.Cores = j.Requirements.Cores
	}

	if j.Requirements.DiskSet {
		job.Requirements.Disk = j.Requirements.Disk
	}

	if j.Requirements.OtherSet {
		job.Requirements.Other = j.Requirements.Other
	}
}

// applyScheduling applies the override/priority/retry/limit modifications to
// job.
func (j *JobModifier) applyScheduling(job *Job) {
	if j.OverrideSet {
		job.Override = j.Override
	}

	if j.PrioritySet {
		job.Priority = j.Priority
	}

	if j.RetriesSet {
		job.Retries = j.Retries
	}

	if j.NoRetriesOverWalltimeSet {
		job.NoRetriesOverWalltime = j.NoRetriesOverWalltime
	}

	if j.EnvOverrideSet {
		job.EnvOverride = j.EnvOverride
	}

	if j.LimitGroupsSet {
		job.LimitGroups = slices.Clone(j.LimitGroups)
		job.LimitGroupsForDisplay = nil
	}
}

// applyBehaviours merges any set Behaviours into job, then drops every cleanup
// behaviour the job would be left holding if it now runs in the user's own Cwd.
//
// The cleanup filter deliberately runs over the merged result rather than over
// j.Behaviours: mod's semantics are "replace what this trigger does", so
// filtering a cleanup out of j.Behaviours would leave the trigger unmentioned and
// mergeBehaviours would keep the job's old behaviour for it. It also runs when no
// behaviours were modified at all, so that `wr mod --cwd_matters` drops a cleanup
// the job already stored. applyTo applies the Cwd modifications first, so
// job.CwdMatters is by now the value this modification leaves the job with.
func (j *JobModifier) applyBehaviours(job *Job) {
	if j.BehavioursSet {
		job.Behaviours = mergeBehaviours(job.Behaviours, j.Behaviours)
	}

	job.dropImpossibleCleanups()
}

// applyContainer applies the mount, bsub and container modifications to job.
func (j *JobModifier) applyContainer(job *Job) {
	if j.MountConfigsSet {
		// a COPY per job, because one JobModifier is applied to every job of a
		// `wr mod --mounts` batch: assigning the modifier's own slice would give
		// all of them one backing array, guarded by as many different mutexes as
		// there were jobs. The clone has to be DEEP for that to hold, since a
		// per-config copy alone still shares every Targets backing array.
		job.MountConfigs = cloneMountConfigs(j.MountConfigs)
	}

	if j.BsubModeSet {
		job.BsubMode = j.BsubMode

		atomic.AddUint64(&BsubID, 1)
		job.BsubID = atomic.LoadUint64(&BsubID)
	}

	if j.MonitorDockerSet {
		job.MonitorDocker = j.MonitorDocker
	}

	if j.WithDockerSet {
		job.WithDocker = j.WithDocker
	}

	if j.WithSingularitySet {
		job.WithSingularity = j.WithSingularity
	}

	if j.ContainerMountsSet {
		job.ContainerMounts = j.ContainerMounts
	}
}

// initialUntilBuried returns the retry budget a job starts (or restarts) with,
// given its Retries: the first attempt, plus one per retry.
//
// It saturates instead of wrapping. UntilBuried is a uint8 and Retries is a
// documented, accepted 0-255 (cmd/add.go's "--retries [0-255]", wr mod -r, and
// the REST API's restModifyUint8Max), so a plain Retries+1 overflows to 0 at
// the maximum - seeding a LIVE job with an already-exhausted budget. That is
// exactly the state finalizeReleasedJob's clamp defends against: the release
// path would not bury the item (there is no budget left to spend down to the
// bury threshold) while finalizeReleasedJob still called the job buried, so
// wr status would report it buried for ever while it kept being reserved, run
// and released, burning a runner slot each time.
//
// Saturating costs the --retries 255 case exactly one attempt (255 attempts
// rather than a nonsensical 256) and leaves every other value untouched.
func initialUntilBuried(retries uint8) uint8 {
	if retries == math.MaxUint8 {
		return math.MaxUint8
	}

	return retries + 1
}

func quoteRemoteCwd(cwd string) string {
	if isShellSafeUnquoted(cwd) {
		return cwd
	}

	return singleQuoteShellArg(cwd)
}

func isShellSafeUnquoted(arg string) bool {
	if arg == "" {
		return false
	}

	const safe = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789_+-./:=@%"
	for _, char := range arg {
		if !strings.ContainsRune(safe, char) {
			return false
		}
	}

	return true
}

func singleQuoteShellArg(arg string) string {
	return "'" + strings.ReplaceAll(arg, "'", `'"'"'`) + "'"
}
