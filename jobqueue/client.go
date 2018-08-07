// Copyright © 2016-2018 Genome Research Limited
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
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"io/ioutil"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/VertebrateResequencing/wr/internal"
	"github.com/docker/docker/api/types"
	docker "github.com/docker/docker/client"
	"github.com/gofrs/uuid"
	"github.com/ugorji/go/codec"
	"nanomsg.org/go-mangos"
	"nanomsg.org/go-mangos/protocol/req"
	"nanomsg.org/go-mangos/transport/tlstcp"
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
	FailReasonDisk     = "ran out of disk space"
	FailReasonTime     = "command used too much time"
	FailReasonDocker   = "could not interact with docker"
	FailReasonAbnormal = "command failed to complete normally"
	FailReasonLost     = "lost contact with runner"
	FailReasonSignal   = "runner received a signal to stop"
	FailReasonResource = "resource requirements cannot be met"
	FailReasonMount    = "mounting of remote file system(s) failed"
	FailReasonUpload   = "failed to upload files to remote file system"
	FailReasonKilled   = "killed by user request"
)

// lsfEmulationDir is the name of the directory we store our LSF emulation
// symlinks in
const lsfEmulationDir = ".wr_lsf_emulation"

// localhost is the name of host we're running on
const localhost = "localhost"

// these global variables are primarily exported for testing purposes; you
// probably shouldn't change them (*** and they should probably be re-factored
// as fields of a config struct...)
var (
	ClientTouchInterval               = 15 * time.Second
	ClientReleaseDelay                = 30 * time.Second
	ClientPercentMemoryKill           = 90
	RAMIncreaseMin            float64 = 1000
	RAMIncreaseMultLow                = 2.0
	RAMIncreaseMultHigh               = 1.3
	RAMIncreaseMultBreakpoint float64 = 8192
)

// clientRequest is the struct that clients send to the server over the network
// to request it do something. (The properties are only exported so the
// encoder doesn't ignore them.)
type clientRequest struct {
	ClientID       uuid.UUID
	Env            []byte // compressed binc encoding of []string
	FirstReserve   bool
	GetEnv         bool
	GetStd         bool
	IgnoreComplete bool
	Job            *Job
	JobEndState    *JobEndState
	Jobs           []*Job
	Keys           []string
	Search         bool
	Limit          int
	Method         string
	SchedulerGroup string
	State          JobState
	File           []byte // compressed bytes of file content
	Path           string // desired path File should be stored at, can be blank
	Timeout        time.Duration
	Token          []byte
}

// Client represents the client side of the socket that the jobqueue server is
// Serve()ing, specific to a particular queue.
type Client struct {
	ch          codec.Handle
	clientid    uuid.UUID
	hasReserved bool
	sock        mangos.Socket
	sync.Mutex
	teMutex    sync.Mutex // to protect Touch() from other methods during Execute()
	token      []byte
	ServerInfo *ServerInfo
	host       string
	port       string
}

// envStr holds the []string from os.Environ(), for codec compatibility.
type envStr struct {
	Environ []string
}

// Connect creates a connection to the jobqueue server.
//
// addr is the host or IP of the machine running the server, suffixed with a
// colon and the port it is listening on, eg localhost:1234
//
// caFile is a path to the PEM encoded CA certificate that was used to sign the
// server's certificate. If set as a blank string, or if the file doesn't exist,
// the server's certificate will be trusted based on the CAs installed in the
// normal location on the system.
//
// certDomain is a domain that the server's certificate is supposed to be valid
// for.
//
// token is the authentication token that Serve() returned when the server was
// started.
//
// Timeout determines how long to wait for a response from the server, not only
// while connecting, but for all subsequent interactions with it using the
// returned Client.
func Connect(addr, caFile, certDomain string, token []byte, timeout time.Duration) (*Client, error) {
	sock, err := req.NewSocket()
	if err != nil {
		return nil, err
	}

	if err = sock.SetOption(mangos.OptionMaxRecvSize, 0); err != nil {
		return nil, err
	}

	err = sock.SetOption(mangos.OptionRecvDeadline, timeout)
	if err != nil {
		return nil, err
	}

	sock.AddTransport(tlstcp.NewTransport())
	tlsConfig := &tls.Config{ServerName: certDomain}
	caCert, err := ioutil.ReadFile(caFile)
	if err == nil {
		certPool := x509.NewCertPool()
		certPool.AppendCertsFromPEM(caCert)
		tlsConfig.RootCAs = certPool
	}

	dialOpts := make(map[string]interface{})
	dialOpts[mangos.OptionTLSConfig] = tlsConfig
	if err = sock.DialOptions("tls+tcp://"+addr, dialOpts); err != nil {
		return nil, err
	}

	// clients identify themselves (only for the purpose of calling methods that
	// require the client has previously used Reserve()) with a UUID; v4 is used
	// since speed doesn't matter: a typical client executable will only
	// Connect() once; on the other hand, we avoid any possible problem with
	// running on machines with low time resolution
	u, err := uuid.NewV4()
	if err != nil {
		return nil, err
	}
	addrParts := strings.Split(addr, ":")
	c := &Client{
		sock:     sock,
		ch:       new(codec.BincHandle),
		token:    token,
		clientid: u,
		host:     addrParts[0],
		port:     addrParts[1],
	}

	// Dial succeeds even when there's no server up, so we test the connection
	// works with a Ping()
	si, err := c.Ping(timeout)
	if err != nil {
		errc := sock.Close()
		if errc != nil {
			return c, errc
		}
		msg := ErrNoServer
		if jqerr, ok := err.(Error); ok && jqerr.Err == ErrPermissionDenied {
			msg = ErrPermissionDenied
		}
		return nil, Error{"Connect", "", msg}
	}
	c.ServerInfo = si

	return c, err
}

// Disconnect closes the connection to the jobqueue server. It is CRITICAL that
// you call Disconnect() before calling Connect() again in the same process.
func (c *Client) Disconnect() error {
	return c.sock.Close()
}

// Ping tells you if your connection to the server is working, returning static
// information about the server. If err is nil, it works. This is the only
// command that interacts with the server that works if a blank or invalid
// token had been supplied to Connect().
func (c *Client) Ping(timeout time.Duration) (*ServerInfo, error) {
	resp, err := c.request(&clientRequest{Method: "ping", Timeout: timeout})
	if err != nil {
		return nil, err
	}
	return resp.SInfo, err
}

// DrainServer tells the server to stop spawning new runners, stop letting
// existing runners reserve new jobs, and exit once existing runners stop
// running. You get back a count of existing runners and and an estimated time
// until completion for the last of those runners.
func (c *Client) DrainServer() (running int, etc time.Duration, err error) {
	resp, err := c.request(&clientRequest{Method: "drain"})
	if err != nil {
		return running, etc, err
	}
	s := resp.SStats
	running = s.Running
	etc = s.ETC
	return running, etc, err
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

// BackupDB backs up the server's database to the given path. Note that
// automatic backups occur to the configured location without calling this.
func (c *Client) BackupDB(path string) error {
	resp, err := c.request(&clientRequest{Method: "backup"})
	if err != nil {
		return err
	}
	tmpPath := path + ".tmp"
	err = ioutil.WriteFile(tmpPath, resp.DB, dbFilePermission)
	if err != nil {
		rerr := os.Remove(tmpPath)
		if rerr != nil {
			err = fmt.Errorf("%s\n%s", err.Error(), rerr.Error())
		}
		return err
	}

	return os.Rename(tmpPath, path)
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
func (c *Client) Add(jobs []*Job, envVars []string, ignoreComplete bool) (added, existed int, err error) {
	compressed, err := c.CompressEnv(envVars)
	if err != nil {
		return 0, 0, err
	}
	resp, err := c.request(&clientRequest{Method: "add", Jobs: jobs, Env: compressed, IgnoreComplete: ignoreComplete})
	if err != nil {
		return 0, 0, err
	}
	return resp.Added, resp.Existed, err
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
func (c *Client) Reserve(timeout time.Duration) (*Job, error) {
	fr := false
	if !c.hasReserved {
		fr = true
		c.hasReserved = true
	}
	resp, err := c.request(&clientRequest{Method: "reserve", Timeout: timeout, FirstReserve: fr})
	if err != nil {
		return nil, err
	}
	return resp.Job, err
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
func (c *Client) ReserveScheduled(timeout time.Duration, schedulerGroup string) (*Job, error) {
	fr := false
	if !c.hasReserved {
		fr = true
		c.hasReserved = true
	}
	resp, err := c.request(&clientRequest{Method: "reserve", Timeout: timeout, SchedulerGroup: schedulerGroup, FirstReserve: fr})
	if err != nil {
		return nil, err
	}
	return resp.Job, err
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
// Internally, Execute() calls Mount() and Started() and keeps track of peak RAM
// and disk used. It regularly calls Touch() on the Job so that the server knows
// we are still alive and handling the Job successfully. It also intercepts
// SIGTERM, SIGINT, SIGQUIT, SIGUSR1 and SIGUSR2, sending SIGKILL to the running
// Cmd and returning Error.Err(FailReasonSignal); you should check for this and
// exit your process. Finally it calls Unmount() and TriggerBehaviours().
//
// If Kill() is called while executing the Cmd, the next internal Touch() call
// will result in the Cmd being killed and the job being Bury()ied.
//
// If no error is returned, the Cmd will have run OK, exited with status 0, and
// been Archive()d from the queue while being placed in the permanent store.
// Otherwise, it will have been Release()d or Bury()ied as appropriate.
//
// The supplied shell is the shell to execute the Cmd under, ideally bash
// (something that understands the command "set -o pipefail").
//
// You have to have been the one to Reserve() the supplied Job, or this will
// immediately return an error. NB: the peak RAM tracking assumes we are running
// on a modern linux system with /proc/*/smaps.
func (c *Client) Execute(job *Job, shell string) error {
	// quickly check upfront that we Reserve()d the job; this isn't required
	// for other methods since the server does this check and returns an error,
	// but in this case we want to avoid starting to execute the command before
	// finding out about this problem
	if c.clientid != job.ReservedBy {
		return Error{"Execute", job.Key(), ErrMustReserve}
	}

	// we support arbitrary shell commands that may include semi-colons,
	// quoted stuff and pipes, so it's best if we just pass it to bash
	jc := job.Cmd
	if strings.Contains(jc, " | ") {
		jc = "set -o pipefail; " + jc
	}
	cmd := exec.Command(shell, "-c", jc) // #nosec Our whole purpose is to allow users to run arbitrary commands via us...

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
	if fi, errf := os.Stat(job.Cwd); errf != nil || !fi.Mode().IsDir() {
		errm := os.MkdirAll(job.Cwd, os.ModePerm)
		if _, errs := os.Stat(job.Cwd); errs != nil {
			errb := c.Bury(job, nil, FailReasonCwd)
			extra := ""
			if errb != nil {
				extra = fmt.Sprintf(" (and burying the job failed: %s)", errb)
			}
			return fmt.Errorf("working directory [%s] does not exist%s: %s", job.Cwd, extra, errm)
		}
	}
	var actualCwd, tmpDir string
	var dirsToCheckDiskSpace []string
	if job.CwdMatters {
		cmd.Dir = job.Cwd
	} else {
		// we'll create a unique location to work in
		actualCwd, tmpDir, err = mkHashedDir(job.Cwd, job.Key())
		if err != nil {
			buryErr := fmt.Errorf("could not create working directory: %s", err)
			errb := c.Bury(job, nil, FailReasonCwd, buryErr)
			if errb != nil {
				buryErr = fmt.Errorf("%s (and burying the job failed: %s)", buryErr.Error(), errb)
			}
			return buryErr
		}
		cmd.Dir = actualCwd
		job.ActualCwd = actualCwd
		dirsToCheckDiskSpace = append(dirsToCheckDiskSpace, tmpDir)
	}

	var myerr error

	var onCwd bool
	var prependPath string
	if job.BsubMode != "" {
		// create our bsub symlinks in a tmp dir
		prependPath, err = ioutil.TempDir("", lsfEmulationDir)
		if err != nil {
			buryErr := fmt.Errorf("could not create lsf emulation directory: %s", err)
			errb := c.Bury(job, nil, FailReasonCwd, buryErr)
			if errb != nil {
				buryErr = fmt.Errorf("%s (and burying the job failed: %s)", buryErr.Error(), errb)
			}
			return buryErr
		}
		defer func() {
			errr := os.RemoveAll(prependPath)
			if errr != nil {
				if myerr == nil {
					myerr = errr
				} else {
					myerr = fmt.Errorf("%s (and removing the lsf emulation dir failed: %s)", myerr.Error(), errr)
				}
			}
		}()

		err = c.createLSFSymlinks(prependPath, job)
		if err != nil {
			return err
		}

		onCwd = job.CwdMatters
	}

	// if we are a child job of another running on the same host, we expect
	// mounting to fail since we're running in the same directory as our
	// parent
	var mountCouldFail bool
	host, err := os.Hostname()
	if err != nil {
		host = localhost
	}
	if jsonStr := job.Getenv("WR_BSUB_CONFIG"); jsonStr != "" {
		configJob := &Job{}
		if erru := json.Unmarshal([]byte(jsonStr), configJob); erru == nil && configJob.Host == host {
			mountCouldFail = true
			// *** but the problem with this is, the parent job could finish
			// while we're still running, and unmount!...
		}
	}

	// we'll mount any configured remote file systems
	uniqueCacheDirs, uniqueMountedDirs, err := job.Mount(onCwd)
	if err != nil && !mountCouldFail {
		if strings.Contains(err.Error(), "fusermount exited with code 256") {
			// *** not sure what causes this, but perhaps trying again after a
			// few seconds will help?
			<-time.After(5 * time.Second)
			uniqueCacheDirs, uniqueMountedDirs, err = job.Mount()
		}
		if err != nil {
			buryErr := fmt.Errorf("failed to mount remote file system(s): %s (%s)", err, os.Environ())
			errb := c.Bury(job, nil, FailReasonMount, buryErr)
			if errb != nil {
				buryErr = fmt.Errorf("%s (and burying the job failed: %s)", buryErr.Error(), errb)
			}
			return buryErr
		}
	}

	// later, check mount cache dirs for disk usage
	if len(uniqueCacheDirs) > 0 {
		dirsToCheckDiskSpace = append(dirsToCheckDiskSpace, uniqueCacheDirs...)
	}

	// later, check unmounted parts of unique cwd for disk usage, or mounted
	// parts that start off empty
	dontCheckDirs := make(map[string]bool)
	if actualCwd != "" {
		if len(uniqueMountedDirs) > 0 {
			noCheck := false
			for _, dir := range uniqueMountedDirs {
				d, erro := os.Open(dir)
				if erro == nil {
					files, errr := d.Readdir(1)
					if errc := d.Close(); errc != nil {
						if myerr == nil {
							myerr = errc
						} else {
							myerr = fmt.Errorf("%s (and closing dir failed: %s)", myerr.Error(), errc)
						}
					}
					if (errr == nil || errr == io.EOF) && len(files) == 0 {
						continue
					}
				}

				if dir == cmd.Dir {
					noCheck = true
					break
				}
				if strings.HasPrefix(dir, actualCwd) {
					dontCheckDirs[dir] = true
				}
			}

			if !noCheck {
				dirsToCheckDiskSpace = append(dirsToCheckDiskSpace, actualCwd)
			}
		} else {
			dirsToCheckDiskSpace = append(dirsToCheckDiskSpace, actualCwd)
		}
	}

	// and we'll run it with the environment variables that were present when
	// the command was first added to the queue (or if none, current env vars,
	// and in either case, including any overrides) *** we need a way for users
	// to update a job with new env vars
	env, err := job.Env()
	if err != nil {
		errb := c.Bury(job, nil, FailReasonEnv)
		extra := ""
		if errb != nil {
			extra = fmt.Sprintf(" (and burying the job failed: %s)", errb)
		}
		_, erru := job.Unmount(true)
		if erru != nil {
			extra += fmt.Sprintf(" (and unmounting the job failed: %s)", erru)
		}
		return fmt.Errorf("failed to extract environment variables for job [%s]: %s%s", job.Key(), err, extra)
	}
	if tmpDir != "" {
		// (this works fine even if tmpDir has a space in one of the dir names)
		env = envOverride(env, []string{"TMPDIR=" + tmpDir})
		defer func() {
			errr := os.RemoveAll(tmpDir)
			if errr != nil {
				if myerr == nil {
					myerr = errr
				} else {
					myerr = fmt.Errorf("%s (and removing the tmpdir failed: %s)", myerr.Error(), errr)
				}
			}
		}()

		if job.ChangeHome {
			env = envOverride(env, []string{"HOME=" + actualCwd})
		}
	}
	if prependPath != "" {
		// alter env PATH to have prependPath come first
		override := []string{"PATH=" + prependPath}
		for _, envvar := range env {
			pair := strings.Split(envvar, "=")
			if pair[0] == "PATH" {
				override[0] += ":" + pair[1]
				break
			}
		}
		env = envOverride(env, override)

		// add an environment variable of this job as JSON, so that any cloud_*
		// or mount options can be copied to child jobs created via our bsub
		// symlink. (It will also need to know our deployment, stored in
		// BsubMode, and to know the host we're running on in case our children
		// run on the same host as us and therefore any mounts are expected to
		// fail)
		simplified := &Job{
			Requirements: job.Requirements,
			BsubMode:     job.BsubMode,
			Host:         host,
		}
		if _, exists := job.Requirements.Other["cloud_shared"]; !exists {
			simplified.MountConfigs = job.MountConfigs
		}
		jobJSON, errm := json.Marshal(simplified)
		if errm != nil {
			errb := c.Bury(job, nil, fmt.Sprintf("could not convert job to JSON: %s", errm))
			extra := ""
			if errb != nil {
				extra = fmt.Sprintf(" (and burying the job failed: %s)", errb)
			}
			return fmt.Errorf("could not convert job to JSON: %s%s", errm, extra)
		}
		env = envOverride(env, []string{
			"WR_BSUB_CONFIG=" + string(jobJSON),
			"WR_MANAGER_HOST=" + c.host,
			"WR_MANAGER_PORT=" + c.port,
			"LSF_SERVERDIR=/dev/null",
			"LSF_LIBDIR=/dev/null",
			"LSF_ENVDIR=/dev/null",
			"LSF_BINDIR=" + prependPath,
		})
	}
	cmd.Env = env

	// if docker monitoring has been requested, try and get the docker client
	// now and fail early if we can't
	var dockerClient *docker.Client
	existingDockerContainers := make(map[string]bool)
	var monitorDocker, getFirstDockerContainer bool
	if job.MonitorDocker != "" {
		monitorDocker = true
		dockerClient, err = docker.NewClientWithOpts(docker.FromEnv)
		if err != nil {
			buryErr := fmt.Errorf("failed to create docker client: %s", err)
			errb := c.Bury(job, nil, FailReasonDocker, buryErr)
			if errb != nil {
				buryErr = fmt.Errorf("%s (and burying the job failed: %s)", buryErr.Error(), errb)
			}
			return buryErr
		}

		// if we've been asked to monitor the first container that appears, note
		// existing containers
		if job.MonitorDocker == "?" {
			getFirstDockerContainer = true
			containers, errc := dockerClient.ContainerList(context.Background(), types.ContainerListOptions{})
			if errc != nil {
				buryErr := fmt.Errorf("failed to get docker containers: %s", errc)
				errb := c.Bury(job, nil, FailReasonDocker, buryErr)
				if errb != nil {
					buryErr = fmt.Errorf("%s (and burying the job failed: %s)", buryErr.Error(), errb)
				}
				return buryErr
			}
			for _, container := range containers {
				existingDockerContainers[container.ID] = true
			}
		}
	}

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
		errr := c.Release(job, nil, FailReasonStart)
		extra := ""
		if errr != nil {
			extra = fmt.Sprintf(" (and releasing the job failed: %s)", errr)
		}
		_, erru := job.Unmount(true)
		if erru != nil {
			extra += fmt.Sprintf(" (and unmounting the job failed: %s)", erru)
		}
		return fmt.Errorf("could not start command [%s]: %s%s", jc, err, extra)
	}

	// update the server that we've started the job
	err = c.Started(job, cmd.Process.Pid)
	if err != nil {
		// if we can't access the server, may as well bail out now - kill the
		// command (and don't bother trying to Release(); it will auto-Release)
		errk := cmd.Process.Kill()
		extra := ""
		if errk != nil {
			extra = fmt.Sprintf(" (and killing the cmd failed: %s)", errk)
		}
		errt := job.TriggerBehaviours(false)
		if errt != nil {
			extra += fmt.Sprintf(" (and triggering behaviours failed: %s)", errt)
		}
		_, erru := job.Unmount(true)
		if erru != nil {
			extra += fmt.Sprintf(" (and unmounting the job failed: %s)", erru)
		}
		return fmt.Errorf("command [%s] started running, but I killed it due to a jobqueue server error: %s%s", job.Cmd, err, extra)
	}

	// update peak mem and disk used by command, touch job and check if we use
	// too much resources, every 15s. Also check for signals
	peakmem := 0
	var peakdisk int64
	dockerCPU := 0
	ticker := time.NewTicker(ClientTouchInterval) //*** this should be less than the ServerItemTTR set when the server started, not a fixed value
	memTicker := time.NewTicker(1 * time.Second)  // we need to check on memory usage frequently
	ranoutMem := false
	ranoutTime := false
	ranoutDisk := false
	signalled := false
	killCalled := false
	var killErr error
	var closeErr error
	var stateMutex sync.Mutex
	stopChecking := make(chan bool, 1)
	diskUsageCheck := func() (int64, error) {
		var used int64
		for _, dir := range dirsToCheckDiskSpace {
			var thisUsed int64
			var thisErr error
			if dir == actualCwd {
				thisUsed, thisErr = currentDisk(dir, dontCheckDirs)
			} else {
				thisUsed, thisErr = currentDisk(dir)
			}
			if thisErr != nil {
				return 0, thisErr
			}
			used += thisUsed
		}
		return used, nil
	}
	go func() {
		var dockerContainerID string

		killCmd := func() error {
			// get children first
			children, errc := getChildProcesses(int32(cmd.Process.Pid))

			// then kill *** race condition if cmd spawns more children...
			errk := cmd.Process.Kill()

			if errc != nil {
				if errk == nil {
					errk = errc
				} else {
					errk = fmt.Errorf("%s, and getting child processes failed: %s", errk.Error(), errc.Error())
				}
			}

			if dockerContainerID != "" {
				// kill the docker container as well
				errd := dockerClient.ContainerKill(context.Background(), dockerContainerID, "SIGKILL")
				if errk == nil {
					errk = errd
				} else {
					errk = fmt.Errorf("%s, and killing the docker container failed: %s", errk.Error(), errd.Error())
				}
			}

			for _, child := range children {
				// try and kill any children in case the above didn't already
				// result in their death
				errc = child.Kill()
				if errk == nil {
					errk = errc
				} else {
					errk = fmt.Errorf("%s, and killing its child process failed: %s", errk.Error(), errc.Error())
				}
			}

			return errk
		}

	CHECKING:
		for {
			select {
			case <-sigs:
				killErr = killCmd()
				stateMutex.Lock()
				signalled = true
				stateMutex.Unlock()
				errc := errReader.Close()
				if errc != nil {
					closeErr = errc
				}
				errc = outReader.Close()
				if errc != nil {
					closeErr = errc
				}
				break CHECKING
			case <-ticker.C:
				stateMutex.Lock()
				if !ranoutTime && time.Now().After(endT) {
					ranoutTime = true
					// we allow things to go over time, but then if we end up
					// getting signalled later, we now know it may be because we
					// used too much time
				}
				stateMutex.Unlock()

				kc, errf := c.Touch(job)
				if kc {
					killErr = killCmd()
					stateMutex.Lock()
					killCalled = true
					stateMutex.Unlock()
					errc := errReader.Close()
					if errc != nil {
						closeErr = errc
					}
					errc = outReader.Close()
					if errc != nil {
						closeErr = errc
					}
					break CHECKING
				}
				if errf != nil {
					// we may have lost contact with the manager; this is OK. We
					// will keep trying to touch until it works
					continue
				}
			case <-memTicker.C:
				// always see if we've run out of disk space on the machine, in
				// which case abort
				if internal.NoDiskSpaceLeft(filepath.Dir(job.Cwd)) {
					killErr = killCmd()
					stateMutex.Lock()
					ranoutDisk = true
					stateMutex.Unlock()
					break CHECKING
				}

				// get current memory usage
				mem, errf := currentMemory(job.Pid)

				// deal with docker monitoring
				var cpuS int
				if monitorDocker {
					if dockerContainerID == "" {
						if getFirstDockerContainer {
							// look for a new container
							containers, errc := dockerClient.ContainerList(context.Background(), types.ContainerListOptions{})
							if errc == nil {
								for _, container := range containers {
									if _, exists := existingDockerContainers[container.ID]; !exists {
										dockerContainerID = container.ID
										break
									}
								}
							}
						} else {
							// job.MonitorDocker might be a file path
							cidPath := job.MonitorDocker
							if !strings.HasPrefix(cidPath, "/") {
								cidPath = filepath.Join(cmd.Dir, cidPath)
							}
							_, errs := os.Stat(cidPath)
							if errs == nil {
								b, errr := ioutil.ReadFile(cidPath)
								if errr == nil {
									dockerContainerID = strings.TrimSuffix(string(b), "\n")
								}
							}

							// or might be a name; check names of all new
							// containers
							if dockerContainerID == "" {
								containers, errc := dockerClient.ContainerList(context.Background(), types.ContainerListOptions{})
								if errc == nil {
								CONTAINERS:
									for _, container := range containers {
										if _, exists := existingDockerContainers[container.ID]; !exists {
											for _, name := range container.Names {
												name = strings.TrimPrefix(name, "/")
												if name == job.MonitorDocker {
													dockerContainerID = container.ID
													break CONTAINERS
												}
											}
										}
									}
								}
							}
						}
					}

					if dockerContainerID != "" {
						dockerMem, thisDockerCPU, errs := internal.DockerStats(dockerClient, dockerContainerID)
						if errs == nil {
							if dockerMem > mem {
								mem = dockerMem
							}
							cpuS = thisDockerCPU
						}
					}
				}

				// get current disk usage
				disk, errd := diskUsageCheck()

				// now update peaks
				stateMutex.Lock()
				if errf == nil && mem > peakmem {
					peakmem = mem

					if peakmem > job.Requirements.RAM {
						maxRAM, errp := internal.ProcMeminfoMBs()

						// we don't allow cmds to use both more than exepected
						// and more than 90% of physical memory, or we could
						// screw up the machine we're running on
						if errp == nil && peakmem >= ((maxRAM/100)*ClientPercentMemoryKill) {
							killErr = killCmd()
							ranoutMem = true
							stateMutex.Unlock()
							break CHECKING
						}
					}
				}
				if cpuS > dockerCPU {
					dockerCPU = cpuS
				}
				if errd == nil && disk > peakdisk {
					peakdisk = disk
				}
				stateMutex.Unlock()
			case <-stopChecking:
				break CHECKING
			}
		}
	}()

	// wait for the command to exit
	errsew := <-stderrWait
	errsow := <-stdoutWait
	err = cmd.Wait()
	ticker.Stop()
	memTicker.Stop()
	stopChecking <- true
	stateMutex.Lock()
	defer stateMutex.Unlock()

	// though we have tried to track peak memory while the cmd ran (mainly to
	// know if we use too much memory and kill during a run), our method might
	// miss a peak that cmd.ProcessState can tell us about, so use that if
	// higher
	peakRSS := cmd.ProcessState.SysUsage().(*syscall.Rusage).Maxrss
	var peakRSSMB int
	if runtime.GOOS == "darwin" {
		// Maxrss values are bytes
		peakRSSMB = int((peakRSS / 1024) / 1024)
	} else {
		// Maxrss values are kb
		peakRSSMB = int(peakRSS / 1024)
	}
	if peakRSSMB > peakmem {
		peakmem = peakRSSMB
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

	// get a final read on disk usage, for jobs that produce output after the
	// last ticker fired
	finalDisk, errd := diskUsageCheck()
	if errd == nil && finalDisk > peakdisk {
		peakdisk = finalDisk
	}

	// get the exit code and figure out what to do with the Job
	var exitcode int
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
					myerr = Error{"Execute", job.Key(), FailReasonRAM}
				} else if ranoutDisk {
					failreason = FailReasonDisk
					myerr = Error{"Execute", job.Key(), FailReasonDisk}
				} else if signalled {
					if ranoutTime {
						failreason = FailReasonTime
						myerr = Error{"Execute", job.Key(), FailReasonTime}
					} else {
						failreason = FailReasonSignal
						myerr = Error{"Execute", job.Key(), FailReasonSignal}
					}
				} else if killCalled {
					dobury = true
					failreason = FailReasonKilled
					myerr = Error{"Execute", job.Key(), FailReasonKilled}
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
				if !killCalled && !ranoutMem && !ranoutDisk && !signalled {
					_, errf := c.Touch(job)
					if errf != nil {
						return
					}
				}
			case <-stopChecking2:
				return
			}
		}
	}()

	if killErr != nil {
		if myerr != nil {
			myerr = fmt.Errorf("%s; killing the cmd also failed: %s", myerr.Error(), killErr.Error())
		} else {
			myerr = killErr
		}
	}

	if closeErr != nil {
		if myerr != nil {
			myerr = fmt.Errorf("%s; closing stderr/out of the cmd also failed: %s", myerr.Error(), closeErr.Error())
		} else {
			myerr = closeErr
		}
	}

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

	if errsew != nil {
		finalStdErr = append(finalStdErr, "\n\nSTDERR handling problems:\n"...)
		finalStdErr = append(finalStdErr, errsew.Error()...)
	}

	// *** following is useful when debugging; need a better way to see these
	// errors from runner clients...
	// if myerr != nil {
	// 	finalStdErr = append(finalStdErr, "\n\nExecution errors:\n"...)
	// 	finalStdErr = append(finalStdErr, myerr.Error()...)
	// }

	finalStdOut := bytes.TrimSpace(stdout.Bytes())
	if errsow != nil {
		finalStdOut = append(finalStdOut, "\n\nSTDOUT handling problems:\n"...)
		finalStdOut = append(finalStdOut, errsow.Error()...)
	}

	// though we may have had some problem, we always try and update our job end
	// state, and we try many times to avoid having to repeat jobs unnecessarily
	// (we keep retying for ~12+ hrs, giving plenty of time for issues to be
	// fixed and potentially a new manager to be brought online for us to
	// connect to and succeed)
	maxRetries := 300
	worked := false
	jes := &JobEndState{
		Cwd:      actualCwd,
		Exitcode: exitcode,
		PeakRAM:  peakmem,
		PeakDisk: peakdisk,
		CPUtime:  cmd.ProcessState.SystemTime() + cmd.ProcessState.UserTime() + time.Duration(dockerCPU)*time.Second,
		Stdout:   finalStdOut,
		Stderr:   finalStdErr,
		Exited:   true,
	}
	for retryNum := 0; retryNum < maxRetries; retryNum++ {
		// update the database with our final state
		if dobury {
			err = c.Bury(job, jes, failreason)
		} else if dorelease {
			err = c.Release(job, jes, failreason) // which buries after job.Retries fails in a row
		} else if doarchive {
			err = c.Archive(job, jes)
		}
		if err != nil {
			<-time.After(time.Duration(retryNum*100) * time.Millisecond)
			continue
		}
		worked = true
		break
	}

	if !worked {
		errt := job.TriggerBehaviours(false)
		extra := ""
		if errt != nil {
			extra = fmt.Sprintf(" (and triggering behaviours failed: %s)", errt)
		}
		return fmt.Errorf("command [%s] finished running, but will need to be rerun due to a jobqueue server error: %s%s", job.Cmd, err, extra)
	}

	return myerr
}

// createLSFSymlinks creates symlinks of bsub, bjobs and bkill to own exe,
// inside the given dir.
func (c *Client) createLSFSymlinks(prependPath string, job *Job) error {
	wr, erre := os.Executable()
	if erre != nil {
		errb := c.Bury(job, nil, FailReasonCwd)
		extra := ""
		if errb != nil {
			extra = fmt.Sprintf(" (and burying the job failed: %s)", errb)
		}
		return fmt.Errorf("could not get path to wr: %s%s", erre, extra)
	}

	bsub := filepath.Join(prependPath, "bsub")
	bjobs := filepath.Join(prependPath, "bjobs")
	bkill := filepath.Join(prependPath, "bkill")
	err := os.Symlink(wr, bsub)
	if err != nil {
		errb := c.Bury(job, nil, FailReasonCwd)
		extra := ""
		if errb != nil {
			extra = fmt.Sprintf(" (and burying the job failed: %s)", errb)
		}
		return fmt.Errorf("could not create bsub symlink: %s%s", err, extra)
	}
	err = os.Symlink(wr, bjobs)
	if err != nil {
		errb := c.Bury(job, nil, FailReasonCwd)
		extra := ""
		if errb != nil {
			extra = fmt.Sprintf(" (and burying the job failed: %s)", errb)
		}
		return fmt.Errorf("could not create bjobs symlink: %s%s", err, extra)
	}
	err = os.Symlink(wr, bkill)
	if err != nil {
		errb := c.Bury(job, nil, FailReasonCwd)
		extra := ""
		if errb != nil {
			extra = fmt.Sprintf(" (and burying the job failed: %s)", errb)
		}
		return fmt.Errorf("could not create bkill symlink: %s%s", err, extra)
	}

	return nil
}

// Started updates a Job on the server with information that you've started
// running the Job's Cmd. Started also figures out some host name, ip and
// possibly id (in cloud situations) to associate with the job, so that if
// something goes wrong the user can go to the host and investigate. Note that
// HostID will not be set on job after this call; only the server will know
// about it (use one of the Get methods afterwards to get a new object with the
// HostID set if necessary).
func (c *Client) Started(job *Job, pid int) error {
	// host details
	host, err := os.Hostname()
	if err != nil {
		host = localhost
	}
	job.Host = host
	job.HostIP, err = internal.CurrentIP("")
	if err != nil {
		return err
	}
	job.Pid = pid
	job.Attempts++             // not considered by server, which does this itself - just for benefit of this process
	job.StartTime = time.Now() // ditto
	_, err = c.request(&clientRequest{Method: "jstart", Job: job})
	return err
}

// Touch adds to a job's ttr, allowing you more time to work on it. Note that
// you must have reserved the job before you can touch it. If the returned bool
// is true, you stop doing what you're doing and bury the job, since this means
// that Kill() has been called for this job.
func (c *Client) Touch(job *Job) (bool, error) {
	c.teMutex.Lock()
	defer c.teMutex.Unlock()
	resp, err := c.request(&clientRequest{Method: "jtouch", Job: job})
	if err != nil {
		return false, err
	}
	return resp.KillCalled, err
}

// JobEndState is used to describe the state of a job after it has (tried to)
// execute it's Cmd. You supply these to Client.Bury(), Release() and Archive().
// The cwd you supply should be the actual working directory used, which may be
// different to the Job's Cwd property; if not, supply empty string. Always set
// exited to true, and populate all other fields, unless you never actually
// tried to execute the Cmd, in which case you would just provide a nil
// JobEndState to the methods that need one.
type JobEndState struct {
	Cwd      string
	Exitcode int
	PeakRAM  int
	PeakDisk int64
	CPUtime  time.Duration
	Stdout   []byte
	Stderr   []byte
	Exited   bool
}

// ended updates a Job for the benefit of the client only; this has no effect on
// the server's knowledge of the Job, but does alter the Job so that it's
// StdOutC and StdErrC are populated correctly for passing to the server).
func (c *Client) ended(job *Job, jes *JobEndState) error {
	if jes == nil || !jes.Exited {
		return nil
	}
	c.teMutex.Lock()
	defer c.teMutex.Unlock()
	job.Exited = true
	job.Exitcode = jes.Exitcode
	job.PeakRAM = jes.PeakRAM
	job.PeakDisk = jes.PeakDisk
	job.CPUtime = jes.CPUtime
	if jes.Cwd != "" {
		job.ActualCwd = jes.Cwd
	}
	var err error
	if len(jes.Stdout) > 0 {
		job.StdOutC, err = compress(jes.Stdout)
		if err != nil {
			return err
		}
	}
	if len(jes.Stderr) > 0 {
		job.StdErrC, err = compress(jes.Stderr)
		if err != nil {
			return err
		}
	}
	return err
}

// Archive removes a job from the jobqueue and adds it to the database of
// complete jobs, for use after you have run the job successfully. You have to
// have been the one to Reserve() the supplied Job, and the Job must be marked
// as having successfully run, or you will get an error.
func (c *Client) Archive(job *Job, jes *JobEndState) error {
	err := c.ended(job, jes)
	if err != nil {
		return err
	}
	c.teMutex.Lock()
	defer c.teMutex.Unlock()
	_, err = c.request(&clientRequest{Method: "jarchive", Job: job, JobEndState: jes})
	if err != nil {
		return err
	}
	job.State = JobStateComplete
	return err
}

// Release places a job back on the jobqueue, for use when you can't handle the
// job right now (eg. there was a suspected transient error) but maybe someone
// else can later. Note that you must reserve a job before you can release it.
// You can only Release() the same job as many times as its Retries value if it
// has been run and failed; a subsequent call to Release() will instead result
// in a Bury(). (If the job's Cmd was not run, you can Release() an unlimited
// number of times.)
func (c *Client) Release(job *Job, jes *JobEndState, failreason string) error {
	err := c.ended(job, jes)
	if err != nil {
		return err
	}
	c.teMutex.Lock()
	defer c.teMutex.Unlock()
	job.FailReason = failreason
	_, err = c.request(&clientRequest{Method: "jrelease", Job: job, JobEndState: jes})
	if err != nil {
		return err
	}

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
	return err
}

// Bury marks a job as unrunnable, so it will be ignored (until the user does
// something to perhaps make it runnable and kicks the job). Note that you must
// reserve a job before you can bury it. Optionally supply an error that will
// be be displayed as the Job's stderr.
func (c *Client) Bury(job *Job, jes *JobEndState, failreason string, stderr ...error) error {
	err := c.ended(job, jes)
	if err != nil {
		return err
	}
	c.teMutex.Lock()
	defer c.teMutex.Unlock()
	job.FailReason = failreason
	if len(stderr) == 1 && stderr[0] != nil {
		job.StdErrC, err = compress([]byte(stderr[0].Error()))
		if err != nil {
			return err
		}
	}
	_, err = c.request(&clientRequest{Method: "jbury", Job: job, JobEndState: jes})
	if err != nil {
		return err
	}
	job.State = JobStateBuried
	return err
}

// Kick makes previously Bury()'d jobs runnable again (it can be Reserve()d in
// the future). It returns a count of jobs that it actually kicked. Errors will
// only be related to not being able to contact the server.
func (c *Client) Kick(jes []*JobEssence) (int, error) {
	keys := c.jesToKeys(jes)
	resp, err := c.request(&clientRequest{Method: "jkick", Keys: keys})
	if err != nil {
		return 0, err
	}
	return resp.Existed, err
}

// Delete removes incomplete, not currently running jobs from the queue
// completely. For use when jobs were created incorrectly/ by accident, or they
// can never be fixed. It returns a count of jobs that it actually removed.
// Errors will only be related to not being able to contact the server.
func (c *Client) Delete(jes []*JobEssence) (int, error) {
	keys := c.jesToKeys(jes)
	resp, err := c.request(&clientRequest{Method: "jdel", Keys: keys})
	if err != nil {
		return 0, err
	}
	return resp.Existed, err
}

// Kill will cause the next Touch() call for the job(s) described by the input
// to return a kill signal. Touches happening as part of an Execute() will
// respond to this signal by terminating their execution and burying the job. As
// such you should note that there could be a delay between calling Kill() and
// execution ceasing; wait until the jobs actually get buried before retrying
// the jobs if desired.
//
// Kill returns a count of jobs that were eligible to be killed (those still in
// running state). Errors will only be related to not being able to contact the
// server.
func (c *Client) Kill(jes []*JobEssence) (int, error) {
	keys := c.jesToKeys(jes)
	resp, err := c.request(&clientRequest{Method: "jkill", Keys: keys})
	if err != nil {
		return 0, err
	}
	return resp.Existed, err
}

// GetByEssence gets a Job given a JobEssence to describe it. With the boolean
// args set to true, this is the only way to get a Job that StdOut() and
// StdErr() will work on, and one of 2 ways that Env() will work (the other
// being Reserve()).
func (c *Client) GetByEssence(je *JobEssence, getstd bool, getenv bool) (*Job, error) {
	resp, err := c.request(&clientRequest{Method: "getbc", Keys: []string{je.Key()}, GetStd: getstd, GetEnv: getenv})
	if err != nil {
		return nil, err
	}
	jobs := resp.Jobs
	if len(jobs) == 0 {
		return nil, err
	}
	return jobs[0], err
}

// GetByEssences gets multiple Jobs at once given JobEssences that describe
// them.
func (c *Client) GetByEssences(jes []*JobEssence) ([]*Job, error) {
	keys := c.jesToKeys(jes)
	resp, err := c.request(&clientRequest{Method: "getbc", Keys: keys})
	if err != nil {
		return nil, err
	}
	return resp.Jobs, err
}

// jesToKeys deals with the jes arg that GetByEccences(), Kick() and Delete()
// take.
func (c *Client) jesToKeys(jes []*JobEssence) []string {
	var keys []string
	for _, je := range jes {
		keys = append(keys, je.Key())
	}
	return keys
}

// GetByRepGroup gets multiple Jobs at once given their RepGroup (an arbitrary
// user-supplied identifier for the purpose of grouping related jobs together
// for reporting purposes).
//
// If 'subStr' is true, gets Jobs in all RepGroups that the supplied repgroup is
// a substring of.
//
// 'limit', if greater than 0, limits the number of jobs returned that have the
// same State, FailReason and Exitcode, and on the the last job of each
// State+FailReason group it populates 'Similar' with the number of other
// excluded jobs there were in that group.
//
// Providing 'state' only returns jobs in that State. 'getStd' and 'getEnv', if
// true, retrieve the stdout, stderr and environement variables for the Jobs.
func (c *Client) GetByRepGroup(repgroup string, subStr bool, limit int, state JobState, getStd bool, getEnv bool) ([]*Job, error) {
	resp, err := c.request(&clientRequest{Method: "getbr", Job: &Job{RepGroup: repgroup}, Search: subStr, Limit: limit, State: state, GetStd: getStd, GetEnv: getEnv})
	if err != nil {
		return nil, err
	}
	return resp.Jobs, err
}

// GetIncomplete gets all Jobs that are currently in the jobqueue, ie. excluding
// those that are complete and have been Archive()d. The args are as in
// GetByRepGroup().
func (c *Client) GetIncomplete(limit int, state JobState, getStd bool, getEnv bool) ([]*Job, error) {
	resp, err := c.request(&clientRequest{Method: "getin", Limit: limit, State: state, GetStd: getStd, GetEnv: getEnv})
	if err != nil {
		return nil, err
	}
	return resp.Jobs, err
}

// UploadFile uploads a local file to the machine where the server is running,
// so you can add cloud jobs that need a script or config file on your local
// machine to be copied over to created cloud instances.
//
// If the remote path is supplied as a blank string, the remote path will be
// chosen for you based on the MD5 checksum of your file data, rooted in the
// server's configured UploadDir.
//
// The remote path can be supplied prefixed with ~/ to upload relative to the
// remote's home directory. Otherwise it should be an absolute path.
//
// Returns the absolute path of the uploaded file on the server's machine.
//
// NB: This is only suitable for transferring small files!
func (c *Client) UploadFile(local, remote string) (string, error) {
	compressed, err := compressFile(local)
	if err != nil {
		return "", err
	}
	resp, err := c.request(&clientRequest{Method: "upload", File: compressed, Path: remote})
	if err != nil {
		return "", err
	}
	return resp.Path, err
}

// request the server do something and get back its response. We can only cope
// with one request at a time per client, or we'll get replies back in the
// wrong order, hence we lock.
func (c *Client) request(cr *clientRequest) (*serverResponse, error) {
	c.Lock()
	defer c.Unlock()

	// encode and send the request
	var encoded []byte
	enc := codec.NewEncoderBytes(&encoded, c.ch)
	cr.Token = c.token
	cr.ClientID = c.clientid
	err := enc.Encode(cr)
	if err != nil {
		return nil, err
	}
	err = c.sock.Send(encoded)
	if err != nil {
		return nil, err
	}

	// get the response and decode it
	resp, err := c.sock.Recv()
	if err != nil {
		return nil, err
	}
	sr := &serverResponse{}
	dec := codec.NewDecoderBytes(resp, c.ch)
	err = dec.Decode(sr)
	if err != nil {
		return nil, err
	}

	// pull the error out of sr
	if sr.Err != "" {
		key := ""
		if cr.Job != nil {
			key = cr.Job.Key()
		}
		return sr, Error{cr.Method, key, sr.Err}
	}
	return sr, err
}

// CompressEnv encodes the given environment variables (slice of "key=value"
// strings) and then compresses that, so that for Add() the server can store it
// on disc without holding it in memory, and pass the compressed bytes back to
// us when we need to know the Env (during Execute()).
func (c *Client) CompressEnv(envars []string) ([]byte, error) {
	var encoded []byte
	enc := codec.NewEncoderBytes(&encoded, c.ch)
	err := enc.Encode(&envStr{envars})
	if err != nil {
		return nil, err
	}
	return compress(encoded)
}
