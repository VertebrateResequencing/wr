// Copyright © 2016-2019 Genome Research Limited
// Author: Sendu Bala <sb10@sanger.ac.uk>.
//
//  This file is part of wr.
//
//  wr is free software: you can redistribute it and/or modify
//  it under the terms of the GNU Lesser General Public License as published by
//  the Free Software Foundation, either version 3 of the License, or
//  (at your option) any later version.
//
//  wr is distributed in the hope that it will be useful,
//  but WITHOUT ANY WARRANTY; without even the implied warranty of
//  MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
//  GNU Lesser General Public License for more details.
//
//  You should have received a copy of the GNU Lesser General Public License
//  along with wr. If not, see <http://www.gnu.org/licenses/>.

package jobqueue

// This file contains the job related code.

import (
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	sync "github.com/sasha-s/go-deadlock"

	"github.com/VertebrateResequencing/muxfys/v4"
	"github.com/VertebrateResequencing/wr/jobqueue/scheduler"
	"github.com/VertebrateResequencing/wr/limiter"
	"github.com/VertebrateResequencing/wr/queue"
	"github.com/gofrs/uuid"
	multierror "github.com/hashicorp/go-multierror"
	"github.com/ugorji/go/codec"
)

// JobState is how we describe the possible job states.
type JobState string

// JobState* constants represent all the possible job states. The fake "new" and
// "deleted" states are for the benefit of the web interface (jstateCount).
// "lost" is also a "fake" state indicating the job was running and we lost
// contact with it; it may be dead. "unknown" is an error case that shouldn't
// happen. "deletable" is a meta state that can be used when filtering jobs to
// mean !(running|complete).
const (
	JobStateNew       JobState = "new"
	JobStateDelayed   JobState = "delayed"
	JobStateReady     JobState = "ready"
	JobStateReserved  JobState = "reserved"
	JobStateRunning   JobState = "running"
	JobStateLost      JobState = "lost"
	JobStateBuried    JobState = "buried"
	JobStateDependent JobState = "dependent"
	JobStateComplete  JobState = "complete"
	JobStateDeleted   JobState = "deleted"
	JobStateDeletable JobState = "deletable"
	JobStateUnknown   JobState = "unknown"
)

// jobSchedLimitGroupSeparator is the separator between requirements and limit
// groups in schedular group names.
const jobSchedLimitGroupSeparator = "~"

// jobLimitGroupSeparator is the separator between limit groups in schedular
// group names.
const jobLimitGroupSeparator = ","

// subqueueToJobState converts queue.SubQueue entries to JobStates.
var subqueueToJobState = map[queue.SubQueue]JobState{
	queue.SubQueueNew:       JobStateNew,
	queue.SubQueueDelay:     JobStateDelayed,
	queue.SubQueueReady:     JobStateReady,
	queue.SubQueueRun:       JobStateRunning,
	queue.SubQueueBury:      JobStateBuried,
	queue.SubQueueDependent: JobStateDependent,
	queue.SubQueueRemoved:   JobStateComplete,
}

// itemsStateToJobState converts queue.ItemState entries to JobStates.
var itemsStateToJobState = map[queue.ItemState]JobState{
	queue.ItemStateDelay:     JobStateDelayed,
	queue.ItemStateReady:     JobStateReady,
	queue.ItemStateRun:       JobStateReserved,
	queue.ItemStateBury:      JobStateBuried,
	queue.ItemStateDependent: JobStateDependent,
	queue.ItemStateRemoved:   JobStateComplete,
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

	// LimitGroups are names of limit groups that this job belongs to. If any
	// of these groups are defined (elsewhere) to have a limit, then if as many
	// other jobs as the limit are currently running, this job will not start
	// running. It's a way of not running too many of a type of job at once.
	LimitGroups []string

	// DepGroups are the dependency groups this job belongs to that other jobs
	// can refer to in their Dependencies.
	DepGroups []string

	// Dependencies describe the jobs that must be complete before this job
	// starts.
	Dependencies Dependencies

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
	// to Cwd if CwdMatters, otherwise it will be a sister directory of
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
	// Exited because when this is not set it could like like exit code 0.
	Exitcode int
	// true if the job was running but we've lost contact with it
	Lost bool
	// if the job failed to complete successfully, this will hold one of the
	// FailReason* strings. Also set if Lost == true.
	FailReason string
	// pid of the running or ran process.
	Pid int
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

	// we add this internally to match up runners we spawn via the scheduler to
	// the Jobs they're allowed to ReserveFiltered().
	schedulerGroup string

	// we store the MuxFys that we mount during Mount() so we can Unmount() them
	// later; this is purely client side.
	mountedFS []*muxfys.MuxFys

	// killCalled is set for running jobs if Kill() is called on them.
	killCalled bool

	// incrementedLimitGroups notes that we have incremented limit groups for
	// this job, so they should be decremented when the job finishes running.
	incrementedLimitGroups []string

	sync.RWMutex
}

// WallTime returns the time the job took to run if it ran to completion, or the
// time taken so far if it is currently running.
func (j *Job) WallTime() time.Duration {
	var d time.Duration
	if !j.StartTime.IsZero() {
		if j.EndTime.IsZero() || j.State == JobStateReserved {
			d = time.Since(j.StartTime)
		} else {
			d = j.EndTime.Sub(j.StartTime)
		}
	}
	return d
}

// Env decompresses and decodes job.EnvC (the output of CompressEnv(), which are
// the environment variables the Job's Cmd should run/ran under). Note that EnvC
// is only populated if you got the Job from GetByCmd(_, _, true) or Reserve().
// If no environment variables were passed in when the job was Add()ed to the
// queue, returns current environment variables instead. In both cases, alters
// the return value to apply any overrides stored in job.EnvOverride.
func (j *Job) Env() ([]string, error) {
	overrideEs, err := j.envCurrentOverrides()
	if err != nil {
		return nil, err
	}

	if len(j.EnvC) == 0 {
		if j.EnvCRetrieved {
			env := os.Environ()
			if len(overrideEs) > 0 {
				env = envOverride(env, overrideEs)
			}
			return env, err
		}
		return nil, nil
	}

	decompressed, err := decompress(j.EnvC)
	if err != nil {
		return nil, err
	}
	ch := new(codec.BincHandle)
	dec := codec.NewDecoderBytes(decompressed, ch)
	es := &envStr{}
	err = dec.Decode(es)
	if err != nil {
		return nil, err
	}
	env := es.Environ

	if len(env) == 0 {
		env = os.Environ()
	}

	if len(overrideEs) > 0 {
		env = envOverride(env, overrideEs)
	}

	return env, err
}

// envCurrentOverrides decompresses and decodes any existing EnvOverride.
func (j *Job) envCurrentOverrides() ([]string, error) {
	if len(j.EnvOverride) > 0 {
		decompressed, err := decompress(j.EnvOverride)
		if err != nil {
			return nil, err
		}
		ch := new(codec.BincHandle)
		dec := codec.NewDecoderBytes(decompressed, ch)
		overrideEs := &envStr{}
		err = dec.Decode(overrideEs)
		if err != nil {
			return nil, err
		}
		return overrideEs.Environ, err
	}
	return nil, nil
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

	j.EnvOverride, err = compressEnv(envOverride(current, env))

	return err
}

// Getenv is like os.Getenv(), but for the environment variables stored in the
// the job, including any overrides. Returns blank if Env() would have returned
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
	cwd := j.Cwd
	defaultMount := filepath.Join(j.Cwd, "mnt")
	defaultCacheBase := cwd
	if j.ActualCwd != "" {
		cwd = j.ActualCwd
		defaultMount = cwd
		defaultCacheBase = filepath.Dir(cwd)
	} else if len(onCwd) == 1 && onCwd[0] {
		defaultMount = j.Cwd
		defaultCacheBase = filepath.Dir(j.Cwd)
	}

	var uniqueCacheDirs []string
	var uniqueMountedDirs []string
	for _, mc := range j.MountConfigs {
		var rcs []*muxfys.RemoteConfig
		for _, mt := range mc.Targets {
			accessorConfig, err := muxfys.S3ConfigFromEnvironment(mt.Profile, mt.Path)
			if err != nil {
				_, erru := j.Unmount()
				if erru != nil {
					err = fmt.Errorf("%s (and the unmount failed: %s)", err.Error(), erru)
				}
				return uniqueCacheDirs, uniqueMountedDirs, err
			}
			accessor, err := muxfys.NewS3Accessor(accessorConfig)
			if err != nil {
				_, erru := j.Unmount()
				if erru != nil {
					err = fmt.Errorf("%s (and the unmount failed: %s)", err.Error(), erru)
				}
				return uniqueCacheDirs, uniqueMountedDirs, err
			}

			cacheDir := mt.CacheDir
			if cacheDir != "" && !filepath.IsAbs(cacheDir) {
				cacheDir = filepath.Join(defaultCacheBase, cacheDir)

				// *** we should only set this if not writing, or if writing to
				// a non-empty dir, which we don't know about at this point...
				uniqueCacheDirs = append(uniqueCacheDirs, cacheDir)
			} // *** else, the cache is in a unique dir that I don't know about?
			rc := &muxfys.RemoteConfig{
				Accessor:  accessor,
				CacheData: mt.Cache,
				CacheDir:  cacheDir,
				Write:     mt.Write,
			}

			rcs = append(rcs, rc)
		}

		if len(rcs) == 0 {
			err := fmt.Errorf("no Targets specified")
			_, erru := j.Unmount()
			if erru != nil {
				err = fmt.Errorf("%s (and the unmount failed: %s)", err.Error(), erru)
			}
			return uniqueCacheDirs, uniqueMountedDirs, err
		}

		retries := 10
		if mc.Retries > 0 {
			retries = mc.Retries
		}

		mount := mc.Mount
		if mount != "" {
			if !filepath.IsAbs(mount) {
				mount = filepath.Join(cwd, mount)
				uniqueMountedDirs = append(uniqueMountedDirs, mount)
			}
		} else {
			mount = defaultMount
			uniqueMountedDirs = append(uniqueMountedDirs, mount)
		}
		cacheBase := mc.CacheBase
		if cacheBase != "" {
			if !filepath.IsAbs(cacheBase) {
				cacheBase = filepath.Join(cwd, cacheBase)
			}
		} else {
			cacheBase = defaultCacheBase
		}
		cfg := &muxfys.Config{
			Mount:     mount,
			CacheBase: cacheBase,
			Retries:   retries,
			Verbose:   mc.Verbose,
		}

		fs, err := muxfys.New(cfg)
		if err != nil {
			_, erru := j.Unmount()
			if erru != nil {
				err = fmt.Errorf("%s (and the unmount failed: %s)", err.Error(), erru)
			}
			return uniqueCacheDirs, uniqueMountedDirs, err
		}

		err = fs.Mount(rcs...)
		if err != nil {
			_, erru := j.Unmount()
			if erru != nil {
				err = fmt.Errorf("%s (and the unmount failed: %s)", err.Error(), erru)
			}
			return uniqueCacheDirs, uniqueMountedDirs, err
		}

		// (we can't use each fs.UnmountOnDeath() function because that tries
		// to upload, but if we get killed we don't want that)

		j.mountedFS = append(j.mountedFS, fs)
	}

	// unmount all on death without trying to upload
	if len(j.mountedFS) > 0 {
		deathSignals := make(chan os.Signal, 2)
		signal.Notify(deathSignals, os.Interrupt, syscall.SIGTERM)
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

	return uniqueCacheDirs, uniqueMountedDirs, nil
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

	var doNotUpload bool
	if len(stopUploads) == 1 {
		doNotUpload = stopUploads[0]
	}
	var merr *multierror.Error
	var allLogs []string
	for _, fs := range j.mountedFS {
		uerr := fs.Unmount(doNotUpload)
		if uerr != nil {
			merr = multierror.Append(merr, uerr)
		}
		theseLogs := fs.Logs()
		if len(theseLogs) > 0 {
			allLogs = append(allLogs, theseLogs...)
		}
	}
	j.mountedFS = nil
	if len(allLogs) > 0 {
		logs = strings.TrimSpace(strings.Join(allLogs, ""))
	}

	err = merr.ErrorOrNil()
	if err != nil {
		return logs, fmt.Errorf("Unmount failure(s): %s", err.Error())
	}

	// delete any empty dirs
	if j.ActualCwd != "" {
		for _, mc := range j.MountConfigs {
			if mc.Mount == "" {
				err = rmEmptyDirs(j.ActualCwd, j.Cwd)
			} else if !filepath.IsAbs(mc.Mount) {
				err = rmEmptyDirs(filepath.Join(j.ActualCwd, mc.Mount), j.Cwd)
			}
		}
	}

	return logs, err
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
	if jes.Cwd != "" {
		j.ActualCwd = jes.Cwd
	}
	j.Unlock()
}

// decrementLimitGroups decrements any limit groups of this job that had been
// passed to noteIncrementedLimitGroups(), and then empties that note to make
// multiple calls to this method safe in terms of decrementing.
func (j *Job) decrementLimitGroups(lim *limiter.Limiter) {
	j.Lock()
	defer j.Unlock()
	if len(j.incrementedLimitGroups) > 0 {
		lim.Decrement(j.incrementedLimitGroups)
		j.incrementedLimitGroups = []string{}
	}
}

// Key calculates a unique key to describe the job.
func (j *Job) Key() string {
	if j.CwdMatters {
		return byteKey([]byte(fmt.Sprintf("%s.%s.%s", j.Cwd, j.Cmd, j.MountConfigs.Key())))
	}
	return byteKey([]byte(fmt.Sprintf("%s.%s", j.Cmd, j.MountConfigs.Key())))
}

// generateSchedulerGroup returns a stringified form of the given requirements,
// appended with a standard form of the current limit groups of this job. We
// assume that LimitGroups was sorted and deduplicated when it was set on the
// job (this happens in server.createJobs()).
func (j *Job) generateSchedulerGroup(req *scheduler.Requirements) string {
	var lgs string
	if len(j.LimitGroups) > 0 {
		lgs = jobSchedLimitGroupSeparator + strings.Join(j.LimitGroups, jobLimitGroupSeparator)
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

// ToStatus converts a job to a simplified JStatus, useful for output as JSON.
func (j *Job) ToStatus() (JStatus, error) {
	stderr, err := j.StdErr()
	if err != nil {
		return JStatus{}, err
	}
	stdout, err := j.StdOut()
	if err != nil {
		return JStatus{}, err
	}
	env, err := j.Env()
	if err != nil {
		return JStatus{}, err
	}
	var cwdLeaf string
	j.RLock()
	defer j.RUnlock()
	if j.ActualCwd != "" {
		cwdLeaf, err = filepath.Rel(j.Cwd, j.ActualCwd)
		if err != nil {
			return JStatus{}, err
		}
		cwdLeaf = "/" + cwdLeaf
	}
	state := j.State
	if state == JobStateRunning && j.Lost {
		state = JobStateLost
	}
	ot := make([]string, 0, len(j.Requirements.Other))
	for key, val := range j.Requirements.Other {
		ot = append(ot, key+":"+val)
	}
	return JStatus{
		Key:           j.Key(),
		RepGroup:      j.RepGroup,
		LimitGroups:   j.LimitGroups,
		DepGroups:     j.DepGroups,
		Dependencies:  j.Dependencies.Stringify(),
		Cmd:           j.Cmd,
		State:         state,
		CwdBase:       j.Cwd,
		Cwd:           cwdLeaf,
		HomeChanged:   j.ChangeHome,
		Behaviours:    j.Behaviours.String(),
		Mounts:        j.MountConfigs.String(),
		MonitorDocker: j.MonitorDocker,
		ExpectedRAM:   j.Requirements.RAM,
		ExpectedTime:  j.Requirements.Time.Seconds(),
		RequestedDisk: j.Requirements.Disk,
		OtherRequests: ot,
		Cores:         j.Requirements.Cores,
		PeakRAM:       j.PeakRAM,
		PeakDisk:      j.PeakDisk,
		Exited:        j.Exited,
		Exitcode:      j.Exitcode,
		FailReason:    j.FailReason,
		Pid:           j.Pid,
		Host:          j.Host,
		HostID:        j.HostID,
		HostIP:        j.HostIP,
		Walltime:      j.WallTime().Seconds(),
		CPUtime:       j.CPUtime.Seconds(),
		Started:       j.StartTime.Unix(),
		Ended:         j.EndTime.Unix(),
		Attempts:      j.Attempts,
		Similar:       j.Similar,
		StdErr:        stderr,
		StdOut:        stdout,
		Env:           env,
	}, nil
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

	if j.Cwd != "" {
		return byteKey([]byte(fmt.Sprintf("%s.%s.%s", j.Cwd, j.Cmd, j.MountConfigs.Key())))
	}
	return byteKey([]byte(fmt.Sprintf("%s.%s", j.Cmd, j.MountConfigs.Key())))
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
	EnvOverride      []byte
	LimitGroups      []string
	DepGroups        []string
	Dependencies     Dependencies
	Behaviours       Behaviours
	MountConfigs     MountConfigs
	Cmd              string
	Cwd              string
	ReqGroup         string
	BsubMode         string
	MonitorDocker    string
	Requirements     *scheduler.Requirements
	CwdMatters       bool
	CwdMattersSet    bool
	ChangeHome       bool
	ChangeHomeSet    bool
	ReqGroupSet      bool
	Override         uint8
	OverrideSet      bool
	Priority         uint8
	PrioritySet      bool
	Retries          uint8
	RetriesSet       bool
	EnvOverrideSet   bool
	LimitGroupsSet   bool
	DepGroupsSet     bool
	DependenciesSet  bool
	BehavioursSet    bool
	MountConfigsSet  bool
	BsubModeSet      bool
	MonitorDockerSet bool
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
func (j *JobModifier) SetCwdMatters(new bool) {
	j.CwdMatters = new
	j.CwdMattersSet = true
}

// SetChangeHome notes that you want to modify the ChangeHome of Jobs.
func (j *JobModifier) SetChangeHome(new bool) {
	j.ChangeHome = new
	j.ChangeHomeSet = true
}

// SetReqGroup notes that you want to modify the ReqGroup of Jobs.
func (j *JobModifier) SetReqGroup(new string) {
	j.ReqGroup = new
	j.ReqGroupSet = true
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
func (j *JobModifier) SetOverride(new uint8) {
	j.Override = new
	j.OverrideSet = true
}

// SetPriority notes that you want to modify the Priority of Jobs.
func (j *JobModifier) SetPriority(new uint8) {
	j.Priority = new
	j.PrioritySet = true
}

// SetRetries notes that you want to modify the Retries of Jobs.
func (j *JobModifier) SetRetries(new uint8) {
	j.Retries = new
	j.RetriesSet = true
}

// SetEnvOverride notes that you want to modify the EnvOverride of Jobs. The
// supplied string should be a comma separated list of key=value pairs. This can
// generate an error if compression of the data fails.
func (j *JobModifier) SetEnvOverride(new string) error {
	var compressedEnv []byte
	if new != "" {
		var err error
		compressedEnv, err = compressEnv(strings.Split(new, ","))
		if err != nil {
			return err
		}
	}
	j.EnvOverride = compressedEnv
	j.EnvOverrideSet = true
	return nil
}

// SetLimitGroups notes that you want to modify the LimitGroups of Jobs.
func (j *JobModifier) SetLimitGroups(new []string) {
	j.LimitGroups = new
	j.LimitGroupsSet = true
}

// SetDepGroups notes that you want to modify the DepGroups of Jobs.
func (j *JobModifier) SetDepGroups(new []string) {
	j.DepGroups = new
	j.DepGroupsSet = true
}

// SetDependencies notes that you want to modify the Dependencies of Jobs.
func (j *JobModifier) SetDependencies(new Dependencies) {
	j.Dependencies = new
	j.DependenciesSet = true
}

// SetBehaviours notes that you want to modify the Behaviours of Jobs.
func (j *JobModifier) SetBehaviours(new Behaviours) {
	j.Behaviours = new
	j.BehavioursSet = true
}

// SetMountConfigs notes that you want to modify the MountConfigs of Jobs.
func (j *JobModifier) SetMountConfigs(new MountConfigs) {
	j.MountConfigs = new
	j.MountConfigsSet = true
}

// SetBsubMode notes that you want to modify the BsubMode of Jobs.
func (j *JobModifier) SetBsubMode(new string) {
	j.BsubMode = new
	j.BsubModeSet = true
}

// SetMonitorDocker notes that you want to modify the MonitorDocker of Jobs.
func (j *JobModifier) SetMonitorDocker(new string) {
	j.MonitorDocker = new
	j.MonitorDockerSet = true
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
	keys := make(map[string]string)
	for _, job := range jobs {
		job.Lock()
		before := job.Key()

		// first work out if the key would change and make sure it doesn't
		// change in to an existing key
		new := &Job{Cmd: job.Cmd, Cwd: job.Cwd, CwdMatters: job.CwdMatters, MountConfigs: job.MountConfigs}
		if j.Cmd != "" {
			new.Cmd = j.Cmd
		}
		if j.Cwd != "" {
			new.Cwd = j.Cwd
		}
		if j.CwdMattersSet {
			new.CwdMatters = j.CwdMatters
		}
		if j.MountConfigsSet {
			new.MountConfigs = j.MountConfigs
		}
		newKey := new.Key()
		if _, done := keys[newKey]; done {
			// duplicate of prior job in this loop, ignore
			job.Unlock()
			continue
		}
		if newKey != before {
			// check queue and db
			exists, err := server.checkJobByKey(newKey)
			if err != nil {
				job.Unlock()
				return keys, err
			}
			if exists {
				// duplicate of queued or complete job, ignore
				job.Unlock()
				continue
			}
		}

		if j.Cmd != "" {
			job.Cmd = j.Cmd
		}
		if j.Cwd != "" {
			job.Cwd = j.Cwd
		}
		if j.CwdMattersSet {
			job.CwdMatters = j.CwdMatters
			if j.CwdMatters {
				job.ActualCwd = job.Cwd
			}
		}
		if j.ChangeHomeSet {
			job.ChangeHome = j.ChangeHome
		}
		if j.ReqGroupSet {
			job.ReqGroup = j.ReqGroup
		}
		if j.Requirements != nil {
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
		if j.OverrideSet {
			job.Override = j.Override
		}
		if j.PrioritySet {
			job.Priority = j.Priority
		}
		if j.RetriesSet {
			job.Retries = j.Retries
		}
		if j.EnvOverrideSet {
			job.EnvOverride = j.EnvOverride
		}
		if j.LimitGroupsSet {
			job.LimitGroups = j.LimitGroups
		}
		if j.DepGroupsSet {
			job.DepGroups = j.DepGroups
		}
		if j.DependenciesSet {
			job.Dependencies = j.Dependencies
		}
		if j.BehavioursSet {
			for _, new := range j.Behaviours {
				var found bool
				for i, old := range job.Behaviours {
					if old.When == new.When {
						job.Behaviours[i] = new
						found = true
						break
					}
				}
				if !found {
					job.Behaviours = append(job.Behaviours, new)
				}
			}
		}
		if j.MountConfigsSet {
			job.MountConfigs = j.MountConfigs
		}
		if j.BsubModeSet {
			job.BsubMode = j.BsubMode
			atomic.AddUint64(&BsubID, 1)
			job.BsubID = atomic.LoadUint64(&BsubID)
		}
		if j.MonitorDockerSet {
			job.MonitorDocker = j.MonitorDocker
		}
		keys[job.Key()] = before
		job.Unlock()
	}
	return keys, nil
}
