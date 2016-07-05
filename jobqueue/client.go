// Copyright © 2016 Genome Research Limited
// Author: Sendu Bala <sb10@sanger.ac.uk>.
//
//  This file is part of VRPipe.
//
//  VRPipe is free software: you can redistribute it and/or modify
//  it under the terms of the GNU Lesser General Public License as published by
//  the Free Software Foundation, either version 3 of the License, or
//  (at your option) any later version.
//
//  VRPipe is distributed in the hope that it will be useful,
//  but WITHOUT ANY WARRANTY; without even the implied warranty of
//  MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
//  GNU Lesser General Public License for more details.
//
//  You should have received a copy of the GNU Lesser General Public License
//  along with VRPipe. If not, see <http://www.gnu.org/licenses/>.

/*
Package jobqueue provides server/client functions to interact with the queue
structure provided by the queue package over a network.

It provides a job queue and running system which guarantees:
# Created jobs are never lost accidentally.
# The same job will not run more than once simultaneously:
  - Duplicate jobs are not created
  - Each job is handled by only a single client
# Jobs are handled in the desired order (user priority and fifo).
# Jobs still get run despite crashing clients.
# Completed jobs are kept forever for historical purposes.

This file contains all the functions for clients to interact with the server.
See server.go for the functions needed to implement a server executable.
*/
package jobqueue

import (
	"bytes"
	"fmt"
	"github.com/go-mangos/mangos"
	"github.com/go-mangos/mangos/protocol/req"
	"github.com/go-mangos/mangos/transport/tcp"
	"github.com/satori/go.uuid"
	"github.com/ugorji/go/codec"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"
	"time"
)

const (
	FailReasonEnv      = "failed to get environment variables"
	FailReasonStart    = "command failed to start"
	FailReasonCPerm    = "command permission problem"
	FailReasonCFound   = "command not found"
	FailReasonCExit    = "command invalid exit code"
	FailReasonExit     = "command exited non-zero"
	FailReasonMem      = "command used too much memory"
	FailReasonTime     = "command used too much time"
	FailReasonAbnormal = "command failed to complete normally"
	FailReasonSignal   = "runner received a signal to stop"
	FailReasonResource = "resource requirements cannot be met"
)

var (
	ClientTouchInterval = 15 * time.Second
	ClientReleaseDelay  = 30 * time.Second
)

// clientRequest is the struct that clients send to the server over the network
// to request it do something. (The properties are only exported so the
// encoder doesn't ignore them.)
type clientRequest struct {
	ClientID       uuid.UUID
	Method         string
	Queue          string
	Jobs           []*Job
	Job            *Job
	Keys           []string
	Timeout        time.Duration
	SchedulerGroup string
	Env            []byte // compressed binc encoding of []string
	GetStd         bool
	GetEnv         bool
	Limit          int
	State          string
}

// Job is a struct that represents a command that needs to be run and some
// associated metadata. ReqGroup is a string that you supply to group together
// all commands that you expect to have similar memory and time requirements.
// Memory and Time are added by the system based on past experience of running
// jobs with the same ReqGroup. If you supply these yourself, your memory and
// time will be used if there is insufficient past experience, or if you also
// supply Override, which can be 0 to not override, 1 to override past
// experience if your supplied values are higher, or 2 to always override.
// Priority is a number between 0 and 255 inclusive - higher numbered jobs will
// run before lower numbered ones (the default is 0). If you get a Job back
// from the server (via Reserve() or Get*()), you should treat the properties as
// read-only: changing them will have no effect.
type Job struct {
	RepGroup       string // a name associated with related Jobs to help group them together when reporting on their status etc.
	ReqGroup       string
	Cmd            string
	Cwd            string        // the working directory to cd to before running Cmd
	Memory         int           // the expected peak memory in MB Cmd will use while running
	Time           time.Duration // the expected time Cmd will take to run
	CPUs           int           // how many processor cores the Cmd will use
	Override       uint8
	Priority       uint8
	Peakmem        int           // the actual peak memory is recorded here (MB)
	Exited         bool          // true if the Cmd was run and exited
	Exitcode       int           // if the job ran and exited, its exit code is recorded here, but check Exited because when this is not set it could like like exit code 0
	FailReason     string        // if the job failed to complete successfully, this will hold one of the FailReason* strings
	Pid            int           // the pid of the running or ran process is recorded here
	Host           string        // the host the process is running or did run on is recorded here
	Walltime       time.Duration // if the job ran or is running right now, the walltime for the run is recorded here
	CPUtime        time.Duration // if the job ran, the CPU time is recorded here
	StdErrC        []byte        // to read, call job.StdErr() instead; if the job ran, its (truncated) STDERR will be here
	StdOutC        []byte        // to read, call job.StdOut() instead; if the job ran, its (truncated) STDOUT will be here
	EnvC           []byte        // to read, call job.Env() instead, to get the environment variables as a []string, where each string is like "key=value"
	State          string        // the job's state in the queue: 'delayed', 'ready', 'reserved', 'running', 'buried' or 'complete'
	Attempts       uint32        // the number of times the job had ever entered 'running' state
	UntilBuried    uint8         // the remaining number of Release()s allowed before being buried instead
	starttime      time.Time     // the time the cmd starts running is recorded here
	endtime        time.Time     // the time the cmd stops running is recorded here
	schedulerGroup string        // we add this internally to match up runners we spawn via the scheduler to the Jobs they're allowed to ReserveFiltered()
	ReservedBy     uuid.UUID     // we note which client reserved this job, for validating if that client has permission to do other stuff to this Job; the server only ever sets this on Reserve(), so clients can't cheat by changing this on their end
	EnvKey         string        // on the server we don't store EnvC with the job, but look it up in db via this key
	Similar        int           // when retrieving jobs with a limit, this tells you how many jobs were excluded
}

// NewJob makes it a little easier to make a new Job, for use with Add()
func NewJob(cmd string, cwd string, group string, memory int, time time.Duration, cpus int, override uint8, priority uint8, repgroup string) *Job {
	return &Job{
		RepGroup: repgroup,
		ReqGroup: group,
		Cmd:      cmd,
		Cwd:      cwd,
		Memory:   memory,
		Time:     time,
		CPUs:     cpus,
		Override: override,
		Priority: priority,
	}
}

// Client represents the client side of the socket that the jobqueue server is
// Serve()ing, specific to a particular queue
type Client struct {
	sock     mangos.Socket
	queue    string
	ch       codec.Handle
	clientid uuid.UUID
}

// envStr holds the []string from os.Environ(), for codec compatibility
type envStr struct {
	Environ []string
}

// Connect creates a connection to the jobqueue server, specific to a single
// queue. Timeout determines how long to wait for a response from the server,
// not only while connecting, but for all subsequent interactions with it using
// the returned Client.
func Connect(addr string, queue string, timeout time.Duration) (c *Client, err error) {
	sock, err := req.NewSocket()
	if err != nil {
		return
	}

	err = sock.SetOption(mangos.OptionRecvDeadline, timeout)
	if err != nil {
		return
	}

	sock.AddTransport(tcp.NewTransport())

	err = sock.Dial("tcp://" + addr)
	if err != nil {
		return
	}

	// clients identify themselves (only for the purpose of calling methods that
	// require the client has previously used Require()) with a UUID; v4 is used
	// since speed doesn't matter: a typical client executable will only
	// Connect() once; on the other hand, we avoid any possible problem with
	// running on machines with low time resolution
	c = &Client{sock: sock, queue: queue, ch: new(codec.BincHandle), clientid: uuid.NewV4()}

	// Dial succeeds even when there's no server up, so we test the connection
	// works with a Ping()
	ok := c.Ping(timeout)
	if !ok {
		sock.Close()
		c = nil
		err = Error{queue, "Connect", "", ErrNoServer}
	}

	return
}

// Disconnect closes the connection to the jobqueue server
func (c *Client) Disconnect() {
	c.sock.Close()
}

// Ping tells you if your connection to the server is working
func (c *Client) Ping(timeout time.Duration) bool {
	_, err := c.request(&clientRequest{Method: "ping", Queue: c.queue, Timeout: timeout})
	if err != nil {
		return false
	}
	return true
}

// Stats returns stats of the jobqueue server queue you connected to.
// func (c *Conn) Stats() (s TubeStats, err error) {
// 	data, err := c.beanstalk.StatsTube(c.tube)
// 	if err != nil {
// 		err = fmt.Errorf("Failed to get stats for beanstalk tube %s: %s\n", c.tube, err.Error())
// 		return
// 	}
// 	s = TubeStats{}
// 	err = yaml.Unmarshal(data, &s)
// 	if err != nil {
// 		err = fmt.Errorf("Failed to parse yaml for beanstalk tube %s stats: %s", c.tube, err.Error())
// 	}
// 	return
// }

// ServerStats returns stats of the jobqueue server itself.
func (c *Client) ServerStats() (s *ServerStats, err error) {
	resp, err := c.request(&clientRequest{Method: "sstats", Queue: c.queue})
	if err != nil {
		return
	}
	s = resp.SStats
	return
}

// Add adds new jobs to the job queue, but only if those jobs aren't already in
// there. If any were already there, you will not get an error, but the
// returned 'existed' count will be > 0. Note that no cross-queue checking is
// done, so you need to be careful not to add the same job to different queues.
func (c *Client) Add(jobs []*Job) (added int, existed int, err error) {
	resp, err := c.request(&clientRequest{Method: "add", Queue: c.queue, Jobs: jobs, Env: c.compressEnv()})
	if err != nil {
		return
	}
	added = resp.Added
	existed = resp.Existed
	return
}

// Reserve takes a job off the jobqueue. If you process the job successfully you
// should Archive() it. If you can't deal with it right now you should Release()
// it. If you think it can never be dealt with you should Bury() it. If you die
// unexpectedly, the job will automatically be released back to the queue after
// some time. If no job was available in the queue for as long as the timeout
// argument, nil is returned for both job and error. If your timeout is 0, you
// will wait indefinitely for a job.
func (c *Client) Reserve(timeout time.Duration) (j *Job, err error) {
	resp, err := c.request(&clientRequest{Method: "reserve", Queue: c.queue, Timeout: timeout, ClientID: c.clientid})
	if err != nil {
		return
	}
	j = resp.Job
	return
}

// ReserveScheduled is like Reserve(), except that it will only return jobs from
// the specified schedulerGroup. Based on the scheduler the server was
// configured with, it will group jobs based on their resource requirements and
// then submit runners to handle them to your system's job scheduler (such as
// LSF), possibly in different scheduler queues. These runners are told the
// group they are a part of, and that same group name is applied internally to
// the Jobs as the "schedulerGroup", so that the runners can reserve only Jobs
// that they're supposed to. Therefore, it does not make sense for you to call
// this yourself; it is only for use by runners spawned by the server.
func (c *Client) ReserveScheduled(timeout time.Duration, schedulerGroup string) (j *Job, err error) {
	resp, err := c.request(&clientRequest{Method: "reserve", Queue: c.queue, Timeout: timeout, ClientID: c.clientid, SchedulerGroup: schedulerGroup})
	if err != nil {
		return
	}
	j = resp.Job
	return
}

// Execute runs the given Job's Cmd and blocks until it exits. Internally it
// calls Started() and Ended() and keeps track of peak memory used. It regularly
// calls Touch() on the Job so that the server knows we are still alive and
// handling the Job successfully. It also intercepts SIGTERM, SIGINT, SIGQUIT,
// SIGUSR1 and SIGUSR2, sending SIGKILL to the running Cmd and returning
// Error.Err(FailReasonSignal); you should check for this and exit your
// process). If no error is returned, the Cmd will have run OK, exited with
// status 0, and been Archive()d from the queue while being placed in the
// permanent store. Otherwise, it will have been Release()d or Bury()ied as
// appropriate. The supplied shell is the shell to execute the Cmd under,
// ideally bash (something that understand the command "set -o pipefail"). You
// have to have been the one to Reserve() the supplied Job, or this will
// immediately return an error. NB: the peak memory tracking assumes we are
// running on a modern linux system with /proc/*/smaps.
func (c *Client) Execute(job *Job, shell string) error {
	// quickly check upfront that we Reserve()d the job; this isn't required
	// for other methods since the server does this check and returns an error,
	// but in this case we want to avoid starting to execute the command before
	// finding out about this problem
	if !uuid.Equal(c.clientid, job.ReservedBy) {
		return Error{c.queue, "Execute", jobKey(job), ErrMustReserve}
	}

	// we support arbitrary shell commands that may include semi-colons,
	// quoted stuff and pipes, so it's best if we just pass it to bash
	jc := job.Cmd
	if strings.Contains(jc, " | ") {
		jc = "set -o pipefail; " + jc
	}
	cmd := exec.Command(shell, "-c", jc)

	// we'll store up to 4kb of the head and tail of command's STDERR and STDOUT
	cmd.Stderr = &prefixSuffixSaver{N: 4096}
	cmd.Stdout = &prefixSuffixSaver{N: 4096}

	// we'll run the command from the desired directory
	cmd.Dir = job.Cwd

	// and we'll run it with the environment variables that were present when
	// the command was first added to the queue *** we need a way for users to update a job with new env vars
	env, err := job.Env()
	if err != nil {
		c.Bury(job, FailReasonEnv)
		return fmt.Errorf("failed to extract environment variables for job [%s]: %s", jobKey(job), err)
	}
	cmd.Env = env

	// intercept certain signals (under LSF and SGE, SIGUSR2 may mean out-of-
	// time, but there's no reliable way of knowing out-of-memory, so we will
	// just treat them all the same)
	sigs := make(chan os.Signal, 5)
	signal.Notify(sigs, os.Interrupt, syscall.SIGTERM, syscall.SIGQUIT, syscall.SIGUSR1, syscall.SIGUSR2)
	defer signal.Stop(sigs)

	// start running the command
	err = cmd.Start()
	if err != nil {
		// some obscure internal error about setting things up
		c.Release(job, FailReasonStart, ClientReleaseDelay)
		return fmt.Errorf("could not start command [%s]: %s", jc, err)
	}

	// update the server that we've started the job
	host, err := os.Hostname()
	if err != nil {
		host = "localhost"
	}
	err = c.Started(job, cmd.Process.Pid, host)
	if err != nil {
		// if we can't access the server, may as well bail out now - kill the
		// command (and don't bother trying to Release(); it will auto-Release)
		cmd.Process.Kill()
		return fmt.Errorf("command [%s] started running, but I killed it due to a jobqueue server error: %s", job.Cmd, err)
	}

	// update peak mem used by command, and touch job every 15s
	peakmem := 0
	ticker := time.NewTicker(ClientTouchInterval) //*** this should be less than the ServerItemTTR set when the server started, not a fixed value
	timedOut := false
	signalled := false
	go func() {
		for {
			select {
			case <-sigs:
				cmd.Process.Kill()
				signalled = true
				break
			case <-ticker.C:
				err := c.Touch(job)
				if err != nil {
					// this could fail for a number of reasons and it's important
					// we bail out on failure to Touch()
					cmd.Process.Kill()
					timedOut = true
					break
				}

				mem, err := currentMemory(job.Pid)
				if err == nil && mem > peakmem {
					peakmem = mem
				}
			}
		}
	}()

	// wait for the command to exit
	err = cmd.Wait()
	ticker.Stop()

	// we could get the max rss from ProcessState.SysUsage, but we'll stick with
	// our better (?) pss-based Peakmem, unless the command exited so quickly
	// we never ticked and calculated it
	if peakmem == 0 {
		ru := cmd.ProcessState.SysUsage().(*syscall.Rusage)
		peakmem = int(ru.Maxrss / 1024)
	}

	// include our own memory usage in the peakmem of the command, since the
	// peak memory is used to schedule us in the job scheduler, which may
	// kill us for using more memory than expected: we need to allow for our
	// own memory usage
	ourmem, cmerr := currentMemory(os.Getpid())
	if cmerr != nil {
		ourmem = 10
	}
	peakmem += ourmem + 40 // +40 for a little leeway for memory usage vagaries

	// get the exit code and figure out what to do with the Job
	exitcode := 0
	var myerr error
	dobury := false
	dorelease := false
	doarchive := false
	failreason := ""
	if err != nil {
		// there was a problem running the command
		if exitError, ok := err.(*exec.ExitError); ok {
			exitcode = exitError.Sys().(syscall.WaitStatus).ExitStatus()
			switch exitcode {
			case 126:
				dobury = true
				failreason = FailReasonCPerm
				myerr = fmt.Errorf("command [%s] exited with code %d (permission problem, or command is not executable), which seems permanent, so it has been buried", job.Cmd, exitcode)
			case 127:
				dobury = true
				failreason = FailReasonCFound
				myerr = fmt.Errorf("command [%s] exited with code %d (command not found), which seems permanent, so it has been buried", job.Cmd, exitcode)
			case 128:
				dobury = true
				failreason = FailReasonCExit
				myerr = fmt.Errorf("command [%s] exited with code %d (invalid exit code), which seems permanent, so it has been buried", job.Cmd, exitcode)
			default:
				dorelease = true
				if signalled {
					failreason = FailReasonSignal
					myerr = Error{c.queue, "Execute", jobKey(job), FailReasonSignal}
				} else {
					failreason = FailReasonExit
					myerr = fmt.Errorf("command [%s] exited with code %d, which may be a temporary issue, so it will be tried again", job.Cmd, exitcode)
				}
			}
		} else {
			// some obscure internal error unrelated to the exit code
			exitcode = 255
			dorelease = true
			failreason = FailReasonAbnormal
			myerr = fmt.Errorf("command [%s] failed to complete normally (%v), which may be a temporary issue, so it will be tried again", job.Cmd, err)
		}
	} else {
		// the command worked fine
		exitcode = cmd.ProcessState.Sys().(syscall.WaitStatus).ExitStatus()
		doarchive = true
		myerr = nil
	}

	err = c.Ended(job, exitcode, peakmem, cmd.ProcessState.SystemTime(), bytes.TrimSpace(cmd.Stdout.(*prefixSuffixSaver).Bytes()), bytes.TrimSpace(cmd.Stderr.(*prefixSuffixSaver).Bytes()))
	if err != nil {
		// if we can't access the server, we'll have to treat this as failed
		// and let it auto-Release
		if timedOut {
			return fmt.Errorf("command [%s] was running fine, but will need to be rerun due to a jobqueue server error", job.Cmd)
		}
		return fmt.Errorf("command [%s] finished running, but will need to be rerun due to a jobqueue server error: %s", job.Cmd, err)
	}

	if dobury {
		err = c.Bury(job, failreason)
	} else if dorelease {
		err = c.Release(job, failreason, ClientReleaseDelay) // which buries after 3 fails in a row
	} else if doarchive {
		err = c.Archive(job)
	}
	if err != nil {
		return fmt.Errorf("command [%s] finished running, but will need to be rerun due to a jobqueue server error: %s", job.Cmd, err)
	}

	return myerr
}

// Started updates a Job on the server with information that you've started
// running the Job's Cmd. (The Job's Walltime is handled by the server
// internally, based on you calling this.)
func (c *Client) Started(job *Job, pid int, host string) (err error) {
	job.Pid = pid
	job.Host = host
	job.Attempts++             // not considered by server, which does this itself - just for benefit of this process
	job.starttime = time.Now() // ditto
	_, err = c.request(&clientRequest{Method: "jstart", Queue: c.queue, Job: job, ClientID: c.clientid})
	return
}

// Touch adds to a job's ttr, allowing you more time to work on it. Note that
// you must have reserved the job before you can touch it.
func (c *Client) Touch(job *Job) (err error) {
	_, err = c.request(&clientRequest{Method: "jtouch", Queue: c.queue, Job: job, ClientID: c.clientid})
	return
}

// Ended updates a Job on the server with information that you've finished
// running the Job's Cmd. (The Job's Walltime is handled by the server
// internally, based on you calling this.) Peakmem should be in MB.
func (c *Client) Ended(job *Job, exitcode int, peakmem int, cputime time.Duration, stdout []byte, stderr []byte) (err error) {
	job.Exited = true
	job.Exitcode = exitcode
	job.Peakmem = peakmem
	job.CPUtime = cputime
	if len(stdout) > 0 {
		job.StdOutC = compress(stdout)
	}
	if len(stderr) > 0 {
		job.StdErrC = compress(stderr)
	}
	_, err = c.request(&clientRequest{Method: "jend", Queue: c.queue, Job: job, ClientID: c.clientid})
	job.Walltime = time.Since(job.starttime)
	return
}

// Archive removes a job from the jobqueue and adds it to the database of
// complete jobs, for use after you have run the job successfully. You have to
// have been the one to Reserve() the supplied Job, and the Job must be marked
// as having successfully run, or you will get an error.
func (c *Client) Archive(job *Job) (err error) {
	_, err = c.request(&clientRequest{Method: "jarchive", Queue: c.queue, Job: job, ClientID: c.clientid})
	if err == nil {
		job.State = "complete"
	}
	return
}

// Release places a job back on the jobqueue, for use when you can't handle the
// job right now (eg. there was a suspected transient error) but maybe someone
// else can later. Note that you must reserve a job before you can release it.
// The delay arg is the duration to wait after your call to Release() before
// anyone else can Reserve() this job again - could help you stop immediately
// Reserve()ing the job again yourself. You can only Release() the same job 3
// times if it has been run and failed; a subsequent call to Release() will
// instead result in a Bury(). (If the job's Cmd was not run, you can Release()
// an unlimited number of times.)
func (c *Client) Release(job *Job, failreason string, delay time.Duration) (err error) {
	job.FailReason = failreason
	_, err = c.request(&clientRequest{Method: "jrelease", Queue: c.queue, Job: job, Timeout: delay, ClientID: c.clientid})
	if err == nil {
		// update our process with what the server would have done
		if job.Exited && job.Exitcode != 0 {
			job.UntilBuried--
		}
		if job.UntilBuried <= 0 {
			job.State = "buried"
		} else {
			job.State = "delayed"
		}
	}
	return
}

// Bury marks a job as unrunnable, so it will be ignored (until the user does
// something to perhaps make it runnable and kicks the job). Note that you must
// reserve a job before you can bury it.
func (c *Client) Bury(job *Job, failreason string) (err error) {
	job.FailReason = failreason
	_, err = c.request(&clientRequest{Method: "jbury", Queue: c.queue, Job: job, ClientID: c.clientid})
	if err == nil {
		job.State = "buried"
	}
	return
}

// Kick makes previously Bury()'d jobs runnable again (it can be Reserve()d in
// the future). It returns a count of jobs that it actually kicked. Errors will
// only be related to not being able to contact the server. The ccs argument is
// the same as for GetByCmds()
func (c *Client) Kick(ccs [][2]string) (kicked int, err error) {
	keys := c.ccsToKeys(ccs)
	resp, err := c.request(&clientRequest{Method: "jkick", Queue: c.queue, Keys: keys})
	if err != nil {
		return
	}
	kicked = resp.Existed
	return
}

// Delete removes previously Bury()'d jobs from the queue completely. For use
// when jobs were created incorrectly/ by accident, or they can never be fixed.
// It returns a count of jobs that it actually removed. Errors will only be
// related to not being able to contact the server. The ccs argument is the same
// as for GetByCmds().
func (c *Client) Delete(ccs [][2]string) (deleted int, err error) {
	keys := c.ccsToKeys(ccs)
	resp, err := c.request(&clientRequest{Method: "jdel", Queue: c.queue, Keys: keys})
	if err != nil {
		return
	}
	deleted = resp.Existed
	return
}

// GetByCmd gets a Job given its Cmd and Cwd. With the boolean args set to true,
// this is the only way to get a Job that StdOut() and StdErr() will work on,
// and one of 2 ways that Env() will work (the other being Reserve()).
func (c *Client) GetByCmd(cmd string, cwd string, getstd bool, getenv bool) (j *Job, err error) {
	resp, err := c.request(&clientRequest{Method: "getbc", Queue: c.queue, Keys: []string{byteKey([]byte(fmt.Sprintf("%s.%s", cwd, cmd)))}, GetStd: getstd, GetEnv: getenv})
	if err != nil {
		return
	}
	jobs := resp.Jobs
	if len(jobs) > 0 {
		j = jobs[0]
	}
	return
}

// GetByCmds gets multiple Jobs at once given their Cmds and Cwds. You supply a
// slice of cmd/cwd string tuples like: [][2]string{[2]string{cmd1, cwd1},
// [2]string{cmd2, cwd2}, ...}. It is also possible to supply
// "",key if you know the "key" of the desired job; you can get these keys when
// you use GetByRepGroup() or GetIncomplete() with a limit.
func (c *Client) GetByCmds(ccs [][2]string) (out []*Job, err error) {
	keys := c.ccsToKeys(ccs)
	resp, err := c.request(&clientRequest{Method: "getbc", Queue: c.queue, Keys: keys})
	if err != nil {
		return
	}
	out = resp.Jobs
	return
}

// ccsToKeys deals with the ccs arg that GetByCmds(), Kick() and Delete() take.
func (c *Client) ccsToKeys(ccs [][2]string) (keys []string) {
	for _, cc := range ccs {
		if cc[0] == "" {
			keys = append(keys, cc[1])
		} else {
			keys = append(keys, byteKey([]byte(fmt.Sprintf("%s.%s", cc[1], cc[0]))))
		}
	}
	return
}

// GetByRepGroup gets multiple Jobs at once given their RepGroup (an arbitrary
// user-supplied identifier for the purpose of grouping related jobs together
// for reporting purposes). 'limit', if greater than 0, limits the number of
// jobs returned that have the same State, FailReason and Exitcode, and on the
// the last job of each State+FailReason group it populates 'Similar' with the
// number of other excluded jobs there were in that group. Providing 'state'
// only returns jobs in that State. 'getStd' and 'getEnv', if true, retrieve the
// stdout, stderr and environement variables for the Jobs, but only if 'limit'
// is <= 5.
func (c *Client) GetByRepGroup(repgroup string, limit int, state string, getStd bool, getEnv bool) (jobs []*Job, err error) {
	resp, err := c.request(&clientRequest{Method: "getbr", Queue: c.queue, Job: &Job{RepGroup: repgroup}, Limit: limit, State: state, GetStd: getStd, GetEnv: getEnv})
	if err != nil {
		return
	}
	jobs = resp.Jobs
	return
}

// GetIncomplete gets all Jobs that are currently in the jobqueue, ie. excluding
// those that are complete and have been Archive()d. The args are as in
// GetByRepGroup().
func (c *Client) GetIncomplete(limit int, state string, getStd bool, getEnv bool) (jobs []*Job, err error) {
	resp, err := c.request(&clientRequest{Method: "getin", Queue: c.queue, Limit: limit, State: state, GetStd: getStd, GetEnv: getEnv})
	if err != nil {
		return
	}
	jobs = resp.Jobs
	return
}

// Env decompresses and decodes job.EnvC (the output of compressEnv(), which are
// the environment variables the Job's Cmd should run/ran under). Note that EnvC
// is only populated if you got the Job from GetByCmd(_, _, true) or Reserve().
func (j *Job) Env() (env []string, err error) {
	decompressed, err := decompress(j.EnvC)
	if err != nil {
		return
	}
	ch := new(codec.BincHandle)
	dec := codec.NewDecoderBytes([]byte(decompressed), ch)
	es := &envStr{}
	err = dec.Decode(es)
	if err != nil {
		return
	}
	env = es.Environ
	return
}

// StdOut returns the decompressed job.StdOutC, which is the head and tail of
// job.Cmd's STDOUT when it ran. If the Cmd hasn't run yet, or if it output
// nothing to STDOUT, you will get an empty string. Note that StdOutC is only
// populated if you got the Job from GetByCmd(_, true), and if the Job's Cmd ran
// but failed.
func (j *Job) StdOut() (stdout string, err error) {
	if len(j.StdOutC) == 0 {
		return
	}
	decomp, err := decompress(j.StdOutC)
	if err != nil {
		return
	}
	stdout = string(decomp)
	return
}

// StdErr returns the decompressed job.StdErrC, which is the head and tail of
// job.Cmd's STDERR when it ran. If the Cmd hasn't run yet, or if it output
// nothing to STDERR, you will get an empty string. Note that StdErrC is only
// populated if you got the Job from GetByCmd(_, true), and if the Job's Cmd ran
// but failed.
func (j *Job) StdErr() (stderr string, err error) {
	if len(j.StdErrC) == 0 {
		return
	}
	decomp, err := decompress(j.StdErrC)
	if err != nil {
		return
	}
	stderr = string(decomp)
	return
}

// request the server do something and get back its response
func (c *Client) request(cr *clientRequest) (sr *serverResponse, err error) {
	// encode and send the request
	var encoded []byte
	enc := codec.NewEncoderBytes(&encoded, c.ch)
	err = enc.Encode(cr)
	if err != nil {
		return
	}
	err = c.sock.Send(encoded)
	if err != nil {
		return
	}

	// get the response and decode it
	resp, err := c.sock.Recv()
	if err != nil {
		return
	}
	sr = &serverResponse{}
	dec := codec.NewDecoderBytes(resp, c.ch)
	err = dec.Decode(sr)
	if err != nil {
		return
	}

	// pull the error out of sr
	if sr.Err != "" {
		key := ""
		if cr.Job != nil {
			key = jobKey(cr.Job)
		}
		err = Error{cr.Queue, cr.Method, key, sr.Err}
	}
	return
}

// compressEnv encodes the current user environment variables and then
// compresses that, so that for Add() the server can store it on disc without
// holding it in memory, and pass the compressed bytes back to us when we need
// to know the Env (during Execute()).
func (c *Client) compressEnv() []byte {
	var encoded []byte
	enc := codec.NewEncoderBytes(&encoded, c.ch)
	enc.Encode(&envStr{os.Environ()})
	return compress(encoded)
}
