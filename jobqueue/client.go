// Copyright © 2016-2017 Genome Research Limited
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

// This file contains the functions needed to implement a jobqueue client.

import (
	"bytes"
	"fmt"
	"github.com/VertebrateResequencing/wr/internal"
	"github.com/go-mangos/mangos"
	"github.com/go-mangos/mangos/protocol/req"
	"github.com/go-mangos/mangos/transport/tcp"
	"github.com/satori/go.uuid"
	"github.com/ugorji/go/codec"
	"io/ioutil"
	"os"
	"os/exec"
	"os/signal"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"
)

// FailReason* are the reasons for cmd line failure stored on Jobs
const (
	FailReasonEnv      = "failed to get environment variables"
	FailReasonCwd      = "working directory does not exist"
	FailReasonStart    = "command failed to start"
	FailReasonCPerm    = "command permission problem"
	FailReasonCFound   = "command not found"
	FailReasonCExit    = "command invalid exit code"
	FailReasonExit     = "command exited non-zero"
	FailReasonRAM      = "command used too much RAM"
	FailReasonTime     = "command used too much time"
	FailReasonAbnormal = "command failed to complete normally"
	FailReasonRelease  = "lost contact with runner"
	FailReasonSignal   = "runner received a signal to stop"
	FailReasonResource = "resource requirements cannot be met"
	FailReasonMount    = "mounting of remote file system(s) failed"
	FailReasonUpload   = "failed to upload files to remote file system"
)

// these global variables are primarily exported for testing purposes; you
// probably shouldn't change them (*** and they should probably be re-factored
// as fields of a config struct...)
var (
	ClientTouchInterval               = 15 * time.Second
	ClientReleaseDelay                = 30 * time.Second
	RAMIncreaseMin            float64 = 1000
	RAMIncreaseMultLow                = 2.0
	RAMIncreaseMultHigh               = 1.3
	RAMIncreaseMultBreakpoint float64 = 8192
)

// clientRequest is the struct that clients send to the server over the network
// to request it do something. (The properties are only exported so the
// encoder doesn't ignore them.)
type clientRequest struct {
	User           string
	ClientID       uuid.UUID
	Method         string
	Queue          string
	Jobs           []*Job
	Job            *Job
	IgnoreComplete bool
	Keys           []string
	Timeout        time.Duration
	SchedulerGroup string
	Env            []byte // compressed binc encoding of []string
	GetStd         bool
	GetEnv         bool
	Limit          int
	State          JobState
	FirstReserve   bool
}

// Client represents the client side of the socket that the jobqueue server is
// Serve()ing, specific to a particular queue.
type Client struct {
	sock        mangos.Socket
	queue       string
	ch          codec.Handle
	clientid    uuid.UUID
	hostID      string
	gotHostID   bool
	user        string
	hasReserved bool
	teMutex     sync.Mutex // to protect Touch() from other methods during Execute()
	sync.Mutex
}

// envStr holds the []string from os.Environ(), for codec compatibility.
type envStr struct {
	Environ []string
}

// Connect creates a connection to the jobqueue server, specific to a single
// queue. Timeout determines how long to wait for a response from the server,
// not only while connecting, but for all subsequent interactions with it using
// the returned Client.
func Connect(addr string, queue string, timeout time.Duration) (c *Client, err error) {
	// a server is only allowed to be accessed by a particular user, so we get
	// our username here. NB: *** this is not real security, since someone could
	// just recompile with the following line altered to a hardcoded username
	// value; it is only intended to prevent accidental use of someone else's
	// server
	user, err := internal.Username()
	if err != nil {
		return
	}

	sock, err := req.NewSocket()
	if err != nil {
		return
	}

	if err = sock.SetOption(mangos.OptionMaxRecvSize, 0); err != nil {
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
	c = &Client{sock: sock, queue: queue, ch: new(codec.BincHandle), user: user, clientid: uuid.NewV4()}

	// Dial succeeds even when there's no server up, so we test the connection
	// works with a Ping()
	err = c.Ping(timeout)
	if err != nil {
		sock.Close()
		c = nil
		msg := ErrNoServer
		if jqerr, ok := err.(Error); ok && jqerr.Err == ErrWrongUser {
			msg = ErrWrongUser
		}
		err = Error{queue, "Connect", "", msg}
	}

	return
}

// Disconnect closes the connection to the jobqueue server. It is CRITICAL that
// you call Disconnect() before calling Connect() again in the same process.
func (c *Client) Disconnect() {
	c.sock.Close()
}

// Ping tells you if your connection to the server is working. If err is nil,
// it works.
func (c *Client) Ping(timeout time.Duration) (err error) {
	_, err = c.request(&clientRequest{Method: "ping", Timeout: timeout})
	return
}

// DrainServer tells the server to stop spawning new runners, stop letting
// existing runners reserve new jobs, and exit once existing runners stop
// running. You get back a count of existing runners and and an estimated time
// until completion for the last of those runners.
func (c *Client) DrainServer() (running int, etc time.Duration, err error) {
	resp, err := c.request(&clientRequest{Method: "drain"})
	if err != nil {
		return
	}
	s := resp.SStats
	running = s.Running
	etc = s.ETC
	return
}

// ShutdownServer tells the server to immediately cease all operations. Its last
// act will be to backup its internal database. Any existing runners will fail.
// Because the server gets shut down it can't respond with success/failure, so
// we indirectly report if the server was shut down successfully.
func (c *Client) ShutdownServer() bool {
	_, err := c.request(&clientRequest{Method: "shutdown"})
	if err == nil || (err != nil && err.Error() == "receive time out") {
		return true
	}
	return false
}

// ServerStats returns stats of the jobqueue server itself.
func (c *Client) ServerStats() (s *ServerStats, err error) {
	resp, err := c.request(&clientRequest{Method: "sstats"})
	if err != nil {
		return
	}
	s = resp.SStats
	return
}

// BackupDB backs up the server's database to the given path. Note that
// automatic backups occur to the configured location without calling this.
func (c *Client) BackupDB(path string) (err error) {
	resp, err := c.request(&clientRequest{Method: "backup"})
	if err != nil {
		return
	}
	tmpPath := path + ".tmp"
	err = ioutil.WriteFile(tmpPath, resp.DB, dbFilePermission)
	if err == nil {
		err = os.Rename(tmpPath, path)
	} else {
		os.Remove(tmpPath)
	}
	return
}

// Add adds new jobs to the job queue, but only if those jobs aren't already in
// there.
//
// If any were already there, you will not get an error, but the returned
// 'existed' count will be > 0. Note that no cross-queue checking is done, so
// you need to be careful not to add the same job to different queues.
//
// Note that if you add jobs to the queue that were previously added, Execute()d
// and were successfully Archive()d, the existed count will be 0 and the jobs
// will be treated like new ones, though when Archive()d again, the new Job will
// replace the old one in the database. To have such jobs skipped as "existed"
// instead, supply ignoreComplete as true.
//
// The envVars argument is a slice of ("key=value") strings with the environment
// variables you want to be set when the job's Cmd actually runs. Typically you
// would pass in os.Environ().
func (c *Client) Add(jobs []*Job, envVars []string, ignoreComplete bool) (added int, existed int, err error) {
	resp, err := c.request(&clientRequest{Method: "add", Jobs: jobs, Env: c.CompressEnv(envVars), IgnoreComplete: ignoreComplete})
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
// some time.
//
// If no job was available in the queue for as long as the timeout argument, nil
// is returned for both job and error. If your timeout is 0, you will wait
// indefinitely for a job.
//
// NB: if your jobs have schedulerGroups (and they will if you added them to a
// server configured with a RunnerCmd), this will most likely not return any
// jobs; use ReserveScheduled() instead.
func (c *Client) Reserve(timeout time.Duration) (j *Job, err error) {
	fr := false
	if !c.hasReserved {
		fr = true
		c.hasReserved = true
	}
	resp, err := c.request(&clientRequest{Method: "reserve", Timeout: timeout, FirstReserve: fr})
	if err != nil {
		return
	}
	j = resp.Job
	return
}

// ReserveScheduled is like Reserve(), except that it will only return jobs from
// the specified schedulerGroup.
//
// Based on the scheduler the server was configured with, it will group jobs
// based on their resource requirements and then submit runners to handle them
// to your system's job scheduler (such as LSF), possibly in different scheduler
// queues. These runners are told the group they are a part of, and that same
// group name is applied internally to the Jobs as the "schedulerGroup", so that
// the runners can reserve only Jobs that they're supposed to. Therefore, it
// does not make sense for you to call this yourself; it is only for use by
// runners spawned by the server.
func (c *Client) ReserveScheduled(timeout time.Duration, schedulerGroup string) (j *Job, err error) {
	fr := false
	if !c.hasReserved {
		fr = true
		c.hasReserved = true
	}
	resp, err := c.request(&clientRequest{Method: "reserve", Timeout: timeout, SchedulerGroup: schedulerGroup, FirstReserve: fr})
	if err != nil {
		return
	}
	j = resp.Job
	return
}

// Execute runs the given Job's Cmd and blocks until it exits. Then any Job
// Behaviours get triggered as appropriate for the exit status.
//
// The Cmd is run using the environment variables set when the Job was Add()ed,
// or the current environment is used if none were set.
//
// The Cmd is also run within the Job's Cwd. If CwdMatters is false, a unique
// subdirectory is created within Cwd, and that is used as the actual working
// directory. When creating these unique subdirectories, directory hashing is
// used to allow the safe running of 100s of thousands of Jobs all using the
// same Cwd (that is, we will not break the directory listing of Cwd).
// Furthermore, a sister folder will be created in the unique location for this
// Job, the path to which will become the value of the TMPDIR environment
// variable. Once the Cmd exits, this temp directory will be deleted and the
// path to the actual working directory created will be in the Job's ActualCwd
// property. The unique folder structure itself can be wholly deleted through
// the Job behaviour "cleanup".
//
// If any remote file system mounts have been configured for the Job, these are
// mounted prior to running the Cmd, and unmounted afterwards.
//
// Internally, Execute() calls Mount(), Started() and Ended() and keeps track of
// peak RAM used. It regularly calls Touch() on the Job so that the server knows
// we are still alive and handling the Job successfully. It also intercepts
// SIGTERM, SIGINT, SIGQUIT, SIGUSR1 and SIGUSR2, sending SIGKILL to the running
// Cmd and returning Error.Err(FailReasonSignal); you should check for this and
// exit your process. Finally it calls Unmount() and TriggerBehaviours().
//
// If no error is returned, the Cmd will have run OK, exited with status 0, and
// been Archive()d from the queue while being placed in the permanent store.
// Otherwise, it will have been Release()d or Bury()ied as appropriate.
//
// The supplied shell is the shell to execute the Cmd under, ideally bash
// (something that understand the command "set -o pipefail").
//
// You have to have been the one to Reserve() the supplied Job, or this will
// immediately return an error. NB: the peak RAM tracking assumes we are running
// on a modern linux system with /proc/*/smaps.
func (c *Client) Execute(job *Job, shell string) error {
	// quickly check upfront that we Reserve()d the job; this isn't required
	// for other methods since the server does this check and returns an error,
	// but in this case we want to avoid starting to execute the command before
	// finding out about this problem
	if !uuid.Equal(c.clientid, job.ReservedBy) {
		return Error{c.queue, "Execute", job.key(), ErrMustReserve}
	}

	// we support arbitrary shell commands that may include semi-colons,
	// quoted stuff and pipes, so it's best if we just pass it to bash
	jc := job.Cmd
	if strings.Contains(jc, " | ") {
		jc = "set -o pipefail; " + jc
	}
	cmd := exec.Command(shell, "-c", jc)

	// we'll filter STDERR/OUT of the cmd to keep only the first and last line
	// of any contiguous block of \r terminated lines (to mostly eliminate
	// progress bars), and  we'll store only up to 4kb of their head and tail
	errReader, err := cmd.StderrPipe()
	if err != nil {
		return fmt.Errorf("failed to create a pipe for STDERR from cmd [%s]: %s", jc, err)
	}
	stderr := &prefixSuffixSaver{N: 4096}
	stderrWait := stdFilter(errReader, stderr)
	outReader, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("failed to create a pipe for STDOUT from cmd [%s]: %s", jc, err)
	}
	stdout := &prefixSuffixSaver{N: 4096}
	stdoutWait := stdFilter(outReader, stdout)

	// we'll run the command from the desired directory, which must exist or
	// it will fail
	if fi, err := os.Stat(job.Cwd); err != nil || !fi.Mode().IsDir() {
		c.Bury(job, FailReasonCwd)
		return fmt.Errorf("working directory [%s] does not exist", job.Cwd)
	}
	var actualCwd, tmpDir string
	if job.CwdMatters {
		cmd.Dir = job.Cwd
	} else {
		// we'll create a unique location to work in
		actualCwd, tmpDir, err = mkHashedDir(job.Cwd, job.key())
		if err != nil {
			buryErr := fmt.Errorf("could not create working directory: %s", err)
			c.Bury(job, FailReasonCwd, buryErr)
			return buryErr
		}
		cmd.Dir = actualCwd
		job.ActualCwd = actualCwd
	}

	// we'll mount any configured remote file systems
	err = job.Mount()
	if err != nil {
		if strings.Contains(err.Error(), "fusermount exited with code 256") {
			// *** not sure what causes this, but perhaps trying again after a
			// few seconds will help?
			<-time.After(5 * time.Second)
			err = job.Mount()
		}
		if err != nil {
			buryErr := fmt.Errorf("failed to mount remote file system(s): %s", err)
			c.Bury(job, FailReasonMount, buryErr)
			return buryErr
		}
	}

	// and we'll run it with the environment variables that were present when
	// the command was first added to the queue (or if none, current env vars,
	// and in either case, including any overrides) *** we need a way for users
	// to update a job with new env vars
	env, err := job.Env()
	if err != nil {
		c.Bury(job, FailReasonEnv)
		job.Unmount(true)
		return fmt.Errorf("failed to extract environment variables for job [%s]: %s", job.key(), err)
	}
	if tmpDir != "" {
		// (this works fine even if tmpDir has a space in one of the dir names)
		env = envOverride(env, []string{"TMPDIR=" + tmpDir})
		defer os.RemoveAll(tmpDir)

		if job.ChangeHome {
			env = envOverride(env, []string{"HOME=" + actualCwd})
		}
	}
	cmd.Env = env

	// intercept certain signals (under LSF and SGE, SIGUSR2 may mean out-of-
	// time, but there's no reliable way of knowing out-of-memory, so we will
	// just treat them all the same)
	sigs := make(chan os.Signal, 5)
	signal.Notify(sigs, os.Interrupt, syscall.SIGTERM, syscall.SIGQUIT, syscall.SIGUSR1, syscall.SIGUSR2)
	defer signal.Stop(sigs)

	// start running the command
	endT := time.Now().Add(job.Requirements.Time)
	err = cmd.Start()
	if err != nil {
		// some obscure internal error about setting things up
		c.Release(job, FailReasonStart)
		job.Unmount(true)
		return fmt.Errorf("could not start command [%s]: %s", jc, err)
	}

	// update the server that we've started the job
	err = c.Started(job, cmd.Process.Pid)
	if err != nil {
		// if we can't access the server, may as well bail out now - kill the
		// command (and don't bother trying to Release(); it will auto-Release)
		cmd.Process.Kill()
		job.TriggerBehaviours(false)
		job.Unmount(true)
		return fmt.Errorf("command [%s] started running, but I killed it due to a jobqueue server error: %s", job.Cmd, err)
	}

	// update peak mem used by command, touch job and check if we use too much
	// resources, every 15s. Also check for signals
	peakmem := 0
	ticker := time.NewTicker(ClientTouchInterval) //*** this should be less than the ServerItemTTR set when the server started, not a fixed value
	memTicker := time.NewTicker(1 * time.Second)  // we need to check on memory usage frequently
	bailed := false
	ranoutMem := false
	ranoutTime := false
	signalled := false
	var stateMutex sync.Mutex
	stopChecking := make(chan bool, 1)
	go func() {
		for {
			select {
			case <-sigs:
				cmd.Process.Kill()
				stateMutex.Lock()
				signalled = true
				stateMutex.Unlock()
				return
			case <-ticker.C:
				stateMutex.Lock()
				if !ranoutTime && time.Now().After(endT) {
					ranoutTime = true
					// we allow things to go over time, but then if we end up
					// getting signalled later, we now know it may be because we
					// used too much time
				}
				stateMutex.Unlock()

				err := c.Touch(job)
				if err != nil {
					// this could fail for a number of reasons and it's important
					// we bail out on failure to Touch()
					cmd.Process.Kill()
					stateMutex.Lock()
					bailed = true
					stateMutex.Unlock()
					return
				}
			case <-memTicker.C:
				mem, err := currentMemory(job.Pid)
				stateMutex.Lock()
				if err == nil && mem > peakmem {
					peakmem = mem

					if peakmem > job.Requirements.RAM {
						// we don't allow things to use too much memory, or we
						// could screw up the machine we're running on
						cmd.Process.Kill()
						ranoutMem = true
						stateMutex.Unlock()
						return
					}
				}
				stateMutex.Unlock()
			case <-stopChecking:
				return
			}
		}
	}()

	// wait for the command to exit
	<-stderrWait
	<-stdoutWait
	err = cmd.Wait()
	ticker.Stop()
	memTicker.Stop()
	stopChecking <- true
	stateMutex.Lock()
	defer stateMutex.Unlock()

	// we could get the max rss from ProcessState.SysUsage, but we'll stick with
	// our better (?) pss-based Peakmem, unless the command exited so quickly
	// we never ticked and calculated it
	if peakmem == 0 {
		ru := cmd.ProcessState.SysUsage().(*syscall.Rusage)
		if runtime.GOOS == "darwin" {
			// Maxrss values are bytes
			peakmem = int((ru.Maxrss / 1024) / 1024)
		} else {
			// Maxrss values are kb
			peakmem = int(ru.Maxrss / 1024)
		}
	}

	// include our own memory usage in the peakmem of the command, since the
	// peak memory is used to schedule us in the job scheduler, which may
	// kill us for using more memory than expected: we need to allow for our
	// own memory usage
	ourmem, cmerr := currentMemory(os.Getpid())
	if cmerr != nil {
		ourmem = 10
	}
	peakmem += ourmem

	// get the exit code and figure out what to do with the Job
	exitcode := 0
	var myerr error
	dobury := false
	dorelease := false
	doarchive := false
	failreason := ""
	var mayBeTemp string
	if job.UntilBuried > 1 {
		mayBeTemp = ", which may be a temporary issue, so it will be tried again"
	}
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
				if ranoutMem {
					failreason = FailReasonRAM
					myerr = Error{c.queue, "Execute", job.key(), FailReasonRAM}
				} else if signalled {
					if ranoutTime {
						failreason = FailReasonTime
						myerr = Error{c.queue, "Execute", job.key(), FailReasonTime}
					} else {
						failreason = FailReasonSignal
						myerr = Error{c.queue, "Execute", job.key(), FailReasonSignal}
					}
				} else {
					failreason = FailReasonExit
					myerr = fmt.Errorf("command [%s] exited with code %d%s", job.Cmd, exitcode, mayBeTemp)
				}
			}
		} else {
			// some obscure internal error unrelated to the exit code
			exitcode = 255
			dorelease = true
			failreason = FailReasonAbnormal
			myerr = fmt.Errorf("command [%s] failed to complete normally (%v)%s", job.Cmd, err, mayBeTemp)
		}
	} else {
		// the command worked fine
		exitcode = cmd.ProcessState.Sys().(syscall.WaitStatus).ExitStatus()
		doarchive = true
		myerr = nil
	}

	finalStdErr := bytes.TrimSpace(stderr.Bytes())

	// behaviours/ unmounting may take some time we need to make sure to keep
	// touching
	ticker2 := time.NewTicker(ClientTouchInterval)
	stopChecking2 := make(chan bool, 1)
	go func() {
		for {
			select {
			case <-sigs:
				return
			case <-ticker2.C:
				err := c.Touch(job)
				if err != nil {
					return
				}
			case <-stopChecking2:
				return
			}
		}
	}()

	// run behaviours
	berr := job.TriggerBehaviours(myerr == nil)
	if berr != nil {
		if myerr != nil {
			myerr = fmt.Errorf("%s; behaviour(s) also had problem(s): %s", myerr.Error(), berr.Error())
		} else {
			myerr = berr
		}
	}

	// try and unmount now, because if we fail to upload files, we'll have to
	// start over
	addMountLogs := dobury || dorelease
	logs, unmountErr := job.Unmount()
	if unmountErr != nil {
		if strings.Contains(unmountErr.Error(), "failed to upload") {
			if !dobury {
				dorelease = true
			}
			if failreason == "" {
				failreason = FailReasonUpload
			}
			if exitcode == 0 {
				exitcode = -2
			}
		}

		if myerr != nil {
			myerr = fmt.Errorf("%s; unmounting also caused problem(s): %s", myerr.Error(), unmountErr.Error())
		} else {
			myerr = unmountErr
		}
	}
	ticker2.Stop()
	stopChecking2 <- true

	if addMountLogs && logs != "" {
		finalStdErr = append(finalStdErr, "\n\nMount logs:\n"...)
		finalStdErr = append(finalStdErr, logs...)
	}

	if (dobury || dorelease) && berr != nil {
		finalStdErr = append(finalStdErr, "\n\nBehaviour problems:\n"...)
		finalStdErr = append(finalStdErr, berr.Error()...)
	}

	// though we may have bailed or had some other problem, we always try and
	// update our job end state
	err = c.Ended(job, actualCwd, exitcode, peakmem, cmd.ProcessState.SystemTime(), bytes.TrimSpace(stdout.Bytes()), finalStdErr)

	if err != nil {
		// if we can't access the server, we'll have to treat this as failed
		// and let it auto-Release
		if bailed {
			return fmt.Errorf("command [%s] was running fine, but will need to be rerun due to a jobqueue server error", job.Cmd)
		}
		return fmt.Errorf("command [%s] ended, but will need to be rerun due to a jobqueue server error: %s", job.Cmd, err)
	}

	// update the database with our final state
	if dobury {
		err = c.Bury(job, failreason)
	} else if dorelease {
		err = c.Release(job, failreason) // which buries after job.Retries fails in a row
	} else if doarchive {
		err = c.Archive(job)
	}
	if err != nil {
		job.TriggerBehaviours(false)
		return fmt.Errorf("command [%s] finished running, but will need to be rerun due to a jobqueue server error: %s", job.Cmd, err)
	}

	return myerr
}

// Started updates a Job on the server with information that you've started
// running the Job's Cmd. Started also figures out some host name, ip and
// possibly id (in cloud situations) to associate with the job, so that if
// something goes wrong the user can go to the host and investigate. Note that
// HostID will not be set on job after this call; only the server will know
// about it (use one of the Get methods afterwards to get a new object with the
// HostID set if necessary).
func (c *Client) Started(job *Job, pid int) (err error) {
	// host details
	host, err := os.Hostname()
	if err != nil {
		host = "localhost"
	}
	job.Host = host
	job.HostIP = CurrentIP("")
	job.Pid = pid
	job.Attempts++             // not considered by server, which does this itself - just for benefit of this process
	job.StartTime = time.Now() // ditto
	_, err = c.request(&clientRequest{Method: "jstart", Job: job})
	return
}

// Touch adds to a job's ttr, allowing you more time to work on it. Note that
// you must have reserved the job before you can touch it.
func (c *Client) Touch(job *Job) (err error) {
	c.teMutex.Lock()
	defer c.teMutex.Unlock()
	_, err = c.request(&clientRequest{Method: "jtouch", Job: job})
	return
}

// Ended updates a Job on the server with information that you've finished
// running the Job's Cmd. Peakram should be in MB. The cwd you supply should be
// the actual working directory used, which may be different to the Job's Cwd
// property; if not, supply empty string.
func (c *Client) Ended(job *Job, cwd string, exitcode int, peakram int, cputime time.Duration, stdout []byte, stderr []byte) (err error) {
	c.teMutex.Lock()
	defer c.teMutex.Unlock()
	job.Exited = true
	job.Exitcode = exitcode
	job.PeakRAM = peakram
	job.CPUtime = cputime
	if cwd != "" {
		job.ActualCwd = cwd
	}
	if len(stdout) > 0 {
		job.StdOutC = compress(stdout)
	}
	if len(stderr) > 0 {
		job.StdErrC = compress(stderr)
	}
	_, err = c.request(&clientRequest{Method: "jend", Job: job})
	return
}

// Archive removes a job from the jobqueue and adds it to the database of
// complete jobs, for use after you have run the job successfully. You have to
// have been the one to Reserve() the supplied Job, and the Job must be marked
// as having successfully run, or you will get an error.
func (c *Client) Archive(job *Job) (err error) {
	c.teMutex.Lock()
	defer c.teMutex.Unlock()
	_, err = c.request(&clientRequest{Method: "jarchive", Job: job})
	if err == nil {
		job.State = JobStateComplete
	}
	return
}

// Release places a job back on the jobqueue, for use when you can't handle the
// job right now (eg. there was a suspected transient error) but maybe someone
// else can later. Note that you must reserve a job before you can release it.
// You can only Release() the same job as many times as its Retries value if it
// has been run and failed; a subsequent call to Release() will instead result
// in a Bury(). (If the job's Cmd was not run, you can Release() an unlimited
// number of times.)
func (c *Client) Release(job *Job, failreason string) (err error) {
	c.teMutex.Lock()
	defer c.teMutex.Unlock()
	job.FailReason = failreason
	_, err = c.request(&clientRequest{Method: "jrelease", Job: job})
	if err == nil {
		// update our process with what the server would have done
		if job.Exited && job.Exitcode != 0 {
			job.UntilBuried--
			job.updateRecsAfterFailure()
		}
		if job.UntilBuried <= 0 {
			job.State = JobStateBuried
		} else {
			job.State = JobStateDelayed
		}
	}
	return
}

// Bury marks a job as unrunnable, so it will be ignored (until the user does
// something to perhaps make it runnable and kicks the job). Note that you must
// reserve a job before you can bury it. Optionally supply an error that will
// be be displayed as the Job's stderr.
func (c *Client) Bury(job *Job, failreason string, stderr ...error) (err error) {
	c.teMutex.Lock()
	defer c.teMutex.Unlock()
	job.FailReason = failreason
	if len(stderr) == 1 && stderr[0] != nil {
		job.StdErrC = compress([]byte(stderr[0].Error()))
	}
	_, err = c.request(&clientRequest{Method: "jbury", Job: job})
	if err == nil {
		job.State = JobStateBuried
	}
	return
}

// Kick makes previously Bury()'d jobs runnable again (it can be Reserve()d in
// the future). It returns a count of jobs that it actually kicked. Errors will
// only be related to not being able to contact the server.
func (c *Client) Kick(jes []*JobEssence) (kicked int, err error) {
	keys := c.jesToKeys(jes)
	resp, err := c.request(&clientRequest{Method: "jkick", Keys: keys})
	if err != nil {
		return
	}
	kicked = resp.Existed
	return
}

// Delete removes previously Bury()'d jobs from the queue completely. For use
// when jobs were created incorrectly/ by accident, or they can never be fixed.
// It returns a count of jobs that it actually removed. Errors will only be
// related to not being able to contact the server.
func (c *Client) Delete(jes []*JobEssence) (deleted int, err error) {
	keys := c.jesToKeys(jes)
	resp, err := c.request(&clientRequest{Method: "jdel", Keys: keys})
	if err != nil {
		return
	}
	deleted = resp.Existed
	return
}

// GetByEssence gets a Job given a JobEssence to describe it. With the boolean
// args set to true, this is the only way to get a Job that StdOut() and
// StdErr() will work on, and one of 2 ways that Env() will work (the other
// being Reserve()).
func (c *Client) GetByEssence(je *JobEssence, getstd bool, getenv bool) (j *Job, err error) {
	resp, err := c.request(&clientRequest{Method: "getbc", Keys: []string{je.Key()}, GetStd: getstd, GetEnv: getenv})
	if err != nil {
		return
	}
	jobs := resp.Jobs
	if len(jobs) > 0 {
		j = jobs[0]
	}
	return
}

// GetByEssences gets multiple Jobs at once given JobEssences that describe
// them.
func (c *Client) GetByEssences(jes []*JobEssence) (out []*Job, err error) {
	keys := c.jesToKeys(jes)
	resp, err := c.request(&clientRequest{Method: "getbc", Keys: keys})
	if err != nil {
		return
	}
	out = resp.Jobs
	return
}

// jesToKeys deals with the jes arg that GetByEccences(), Kick() and Delete()
// take.
func (c *Client) jesToKeys(jes []*JobEssence) (keys []string) {
	for _, je := range jes {
		keys = append(keys, je.Key())
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
// stdout, stderr and environement variables for the Jobs.
func (c *Client) GetByRepGroup(repgroup string, limit int, state JobState, getStd bool, getEnv bool) (jobs []*Job, err error) {
	resp, err := c.request(&clientRequest{Method: "getbr", Job: &Job{RepGroup: repgroup}, Limit: limit, State: state, GetStd: getStd, GetEnv: getEnv})
	if err != nil {
		return
	}
	jobs = resp.Jobs
	return
}

// GetIncomplete gets all Jobs that are currently in the jobqueue, ie. excluding
// those that are complete and have been Archive()d. The args are as in
// GetByRepGroup().
func (c *Client) GetIncomplete(limit int, state JobState, getStd bool, getEnv bool) (jobs []*Job, err error) {
	resp, err := c.request(&clientRequest{Method: "getin", Limit: limit, State: state, GetStd: getStd, GetEnv: getEnv})
	if err != nil {
		return
	}
	jobs = resp.Jobs
	return
}

// request the server do something and get back its response. We can only cope
// with one request at a time per client, or we'll get replies back in the
// wrong order, hence we lock.
func (c *Client) request(cr *clientRequest) (sr *serverResponse, err error) {
	c.Lock()
	defer c.Unlock()

	// encode and send the request
	var encoded []byte
	enc := codec.NewEncoderBytes(&encoded, c.ch)
	cr.Queue = c.queue
	cr.User = c.user
	cr.ClientID = c.clientid
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
			key = cr.Job.key()
		}
		err = Error{cr.Queue, cr.Method, key, sr.Err}
	}
	return
}

// CompressEnv encodes the given environment variables (slice of "key=value"
// strings) and then compresses that, so that for Add() the server can store it
// on disc without holding it in memory, and pass the compressed bytes back to
// us when we need to know the Env (during Execute()).
func (c *Client) CompressEnv(envars []string) []byte {
	var encoded []byte
	enc := codec.NewEncoderBytes(&encoded, c.ch)
	enc.Encode(&envStr{envars})
	return compress(encoded)
}
