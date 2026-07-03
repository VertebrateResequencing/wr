/*******************************************************************************
 * Copyright (c) 2017-2021, 2024, 2026 Genome Research Ltd.
 *
 * Author: Sendu Bala <sb10@sanger.ac.uk>
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

package cloud

// This file contains the code for the Server struct.

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/VertebrateResequencing/wr/clog"
	"github.com/VertebrateResequencing/wr/internal"
	mth "github.com/VertebrateResequencing/wr/math"
	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
)

const (
	sharePath       = "/shared" // mount point for the *SharedDisk methods
	sshShortTimeOut = 15 * time.Second
	localhostName   = "localhost"
)

// maxSSHSessions is the maximum number of sessions we will try and multiplex on
// each ssh client we make for a server. It doesn't matter if this is lower than
// the server is configured for in /etc/ssh/sshd_config (MaxSessions); we create
// more clients than stricly needed, but this is harmless? If it's higher than
// the configured MaxSessions, there doesn't seem to be much we can do about it.
// MaxSessions can't be queried ahead of time, and we can't discover the correct
// value because if we go over it, then all sessions fail, not just the ones
// over the max (and all failures appear at the ~same time if sessions were
// made at the ~same time). The best we can do for now is to set this to sshd's
// default MaxSessions, which is 10. *** can we do better?
const maxSSHSessions = 10

var (
	errConnectionCouldNotBeEstablished = errors.New("connection could not be established")
	errConnectionAttemptCancelled      = errors.New("connection attempt cancelled")
	errNeverReady                      = errors.New("cloud server never became ready to use")
	errWaitReadyCancelled              = errors.New("cloud server waiting for ready was cancelled")
	errScriptTimeout                   = errors.New("cloud server script failed to complete within")
	errMissingSSHKey                   = errors.New("missing ssh key")
	errNoRouteToHostNow                = errors.New("ssh used to work, but now there's no route to host")
	errSSHGaveUp                       = errors.New("giving up waiting for ssh to work")
	errSSHCancelled                    = errors.New("cancelled waiting for ssh to work")
	errSessionTimedOut                 = errors.New("cloud SSHSession() timed out")
	errSessionCancelled                = errors.New("cloud SSHSession() cancelled")
	errRunCmdDestroyed                 = errors.New("cloud RunCmd() cancelled due to destruction of server")
	errRunCmdCancelled                 = errors.New("cloud RunCmd() on server")
	errProviderNotSet                  = errors.New("provider not set")
)

// maxDialTicks is the number of one-second dial attempts dialNewSSHClient makes
// (for an already-created server) before giving up on a non-startup error, to
// allow for the vagaries of OS start ups (eg. CentOS brings up sshd and starts
// rejecting connections before the centos user gets added).
const maxDialTicks = 9

// localRemotePathParts is the number of colon-separated parts in a CopyOver
// path that explicitly specifies both a local and a remote path.
const localRemotePathParts = 2

// CreateSharedDisk creates an NFS share at /shared, which must be empty or not
// exist. This does not work for remote Servers, so only call this on the return
// value of LocalhostServer(). Does nothing and returns nil if the share was
// already created. NB: this is currently hard-coded to only work on Ubuntu, and
// the ability to sudo is required! Also assumes you don't have any other shares
// configured, and no other process started the NFS server!
// createSharedDiskTimeout bounds how long the whole CreateSharedDisk sequence
// (apt-get install, etc.) may take.
const createSharedDiskTimeout = 120 * time.Second

// maxCleanShutdownScriptTime and maxCleanShutdownCmdTime are how long the user
// destroy script and the clean shutdown command may take before we log that
// they took a long time.
const (
	maxCleanShutdownScriptTime = 3 * time.Minute
	maxCleanShutdownCmdTime    = 10 * time.Second
)

// Flavor describes a "flavor" of server, which is a certain (virtual) hardware
// configuration.
type Flavor struct {
	ID    string
	Name  string
	Cores int
	RAM   int // MB
	Disk  int // GB
}

// HasSpaceFor takes the cpu, ram and disk requirements of a command and tells
// you how many of those commands could run simultaneously on a server of our
// flavor. Returns 0 if not even 1 command could fit on a server with this
// flavor.
func (f *Flavor) HasSpaceFor(cores float64, ramMB, diskGB int) int {
	if mth.FloatLessThan(float64(f.Cores), cores) || (f.RAM < ramMB) || (f.Disk < diskGB) {
		return 0
	}

	var canDo int
	if cores == 0 {
		// rather than allow an infinite or very large number of cmds to run on
		// a server, because there are still real limits on the number of
		// processes we can run at once before things start falling over, we
		// only allow double the actual core count of zero core things to run
		canDo = f.Cores * internal.ZeroCoreMultiplier
	} else {
		canDo = int(math.Floor(float64(f.Cores) / cores))
	}

	return capByRAMAndDisk(canDo, f.RAM, ramMB, f.Disk, diskGB)
}

// capByRAMAndDisk reduces canDo so that it does not exceed how many times ramMB
// fits in availRAM, nor how many times diskGB fits in availDisk. A ramMB or
// diskGB of 0 (or a canDo of 1 or less) imposes no limit from that resource.
func capByRAMAndDisk(canDo, availRAM, ramMB, availDisk, diskGB int) int {
	if canDo <= 1 {
		return canDo
	}

	if ramMB > 0 {
		if n := availRAM / ramMB; n < canDo {
			canDo = n
		}
	}

	if diskGB > 0 {
		if n := availDisk / diskGB; n < canDo {
			canDo = n
		}
	}

	return canDo
}

// Server provides details of the server that Spawn() created for you, and some
// methods that let you keep track of how you use that server.
type Server struct {
	Script            []byte // the content of a start-up script run on the server
	DestroyScript     []byte // the content of a script to run on the server before it is destoyted
	sshClients        []*ssh.Client
	sshClientSessions []int
	AdminPass         string
	PrivateKey        string // PEM format string of a private key that can be used to SSH to the server
	ID                string
	IP                string // ip address that you could SSH to
	Name              string // ought to correspond to the hostname
	OS                string // the name of the Operating System image
	ConfigFiles       string // files that you will CopyOver() and require to be on this Server, in CopyOver() format
	UserName          string // the username needed to log in to the server
	permanentProblem  string
	homeDir           string
	Flavor            *Flavor
	Disk              int           // GB of available disk space
	TTD               time.Duration // amount of idle time allowed before destruction
	goneBad           time.Time
	cancelDestruction chan bool
	cancelID          int
	cancelRunCmd      map[int]chan bool
	provider          *Provider
	sshClientConfig   *ssh.ClientConfig
	usedCores         float64
	cancels           int
	usedZeroCores     int // we keep track of how many zero core things are allocated
	usedDisk          int
	usedRAM           int
	mutex             sync.RWMutex
	hmutex            sync.Mutex
	csmutex           sync.Mutex
	IsHeadNode        bool
	SharedDisk        bool // the server will mount /shared
	created           bool // to distinguish instances we discovered or spawned
	toBeDestroyed     bool
	destroyed         bool
	onDeathrow        bool
	sshStarted        bool
	createdShare      bool
	used              bool
}

// NewServer returns a Server with the minimal details set needed to SSH to it
// and use the various SSH-requiring methods. You will need to manually set
// other properties for other functionality to work.
func NewServer(username, ip, key string) *Server {
	return &Server{
		UserName:     username,
		IP:           ip,
		PrivateKey:   key,
		cancelRunCmd: make(map[int]chan bool),
	}
}

// WaitUntilReady waits for the server to become fully ready: the boot process
// will have completed and ssh will work. This is not part of provider.Spawn()
// because you may not want or be able to ssh to your server, and so that you
// can Spawn() another server while waiting for this one to become ready. If you
// get an err, you will want to call server.Destroy() as this is not done for
// you.
//
// You supply a context so that you can cancel waiting if you no longer need
// this server. Be sure to Destroy() it after cancelling.
//
// files is a string in the format taken by the CopyOver() method; if supplied
// non-blank it will CopyOver the specified files (after the server is ready,
// before any postCreationScript is run).
//
// postCreationScript is the []byte content of a script that will be run on the
// server (as the user supplied to Spawn()) once it is ready, and it will
// complete before this function returns; empty slice means do nothing.
func (s *Server) WaitUntilReady(ctx context.Context, files string, postCreationScript []byte) error {
	ctx = s.getContextWithServerID(ctx)
	// wait for ssh to come up
	_, _, err := s.SSHClient(ctx)
	if err != nil {
		return err
	}

	// wait for sentinelFilePath to exist, indicating that the server is
	// really ready to use
	if err = s.waitForSentinel(ctx); err != nil {
		return err
	}

	// copy over any desired files
	if files != "" {
		err = s.CopyOver(ctx, files)
		if err != nil {
			return fmt.Errorf("cloud server files failed to upload: %w", err)
		}

		s.ConfigFiles = files
	}

	// run the postCreationScript
	return s.runPostCreationScript(ctx, postCreationScript)
}

// waitForSentinel waits for sentinelFilePath to exist on the server (indicating
// the server is really ready to use), then removes it. Returns an error if the
// server never becomes ready or ctx is cancelled.
func (s *Server) waitForSentinel(ctx context.Context) error {
	limit := time.After(sentinelTimeOut)
	ticker := time.NewTicker(1 * time.Second)

	for {
		select {
		case <-ticker.C:
			if s.sentinelExists(ctx) {
				ticker.Stop()
				s.removeSentinel(ctx)

				return nil
			}
		case <-limit:
			ticker.Stop()

			return errNeverReady
		case <-ctx.Done():
			ticker.Stop()

			return errWaitReadyCancelled
		}
	}
}

// sentinelExists reports whether sentinelFilePath currently exists on the
// server.
func (s *Server) sentinelExists(ctx context.Context) bool {
	o, e, fileErr := s.RunCmd(ctx, "file "+sentinelFilePath, false)

	return fileErr == nil && !strings.Contains(o, "No such file") && !strings.Contains(e, "No such file")
}

// removeSentinel deletes sentinelFilePath from the server, logging (but not
// returning) any failure.
func (s *Server) removeSentinel(ctx context.Context) {
	// *** o contains "empty"; test for that instead? Does file behave the same
	// way on all linux variants?
	_, _, rmErr := s.RunCmd(ctx, "sudo rm "+sentinelFilePath, false)
	if rmErr != nil {
		clog.Warn(ctx, "failed to remove sentinel file", "path", sentinelFilePath, "err", rmErr)
	}
}

// runPostCreationScript runs the user's post-creation script (if any) and, on
// success, clears the cached ssh clients (since the script may have altered
// PATH and other things subsequent RunCmd relies on).
func (s *Server) runPostCreationScript(ctx context.Context, postCreationScript []byte) error {
	if len(postCreationScript) == 0 {
		return nil
	}

	if err := s.runScript(ctx, postCreationScript); err != nil {
		return err
	}

	s.Script = postCreationScript

	for _, client := range s.sshClients {
		if err := client.Close(); err != nil {
			clog.Warn(ctx, "failed to close client ssh connection", "err", err)
		}
	}

	s.sshClients = []*ssh.Client{}
	s.sshClientSessions = []int{}

	return nil
}

// runScript runs the given script (eg. the byte content of a bash script) on
// the server after transferring it to /tmp on the server with the given
// basename.
func (s *Server) runScript(ctx context.Context, script []byte) error {
	if len(script) == 0 {
		return nil
	}

	path := filepath.Join("/tmp", ".server_script")

	err := s.CreateFile(ctx, string(script), path)
	if err != nil {
		return fmt.Errorf("cloud server script failed to upload: %w", err)
	}

	_, _, err = s.RunCmd(ctx, "chmod u+x "+path, false)
	if err != nil {
		return fmt.Errorf("cloud server script could not be made executable: %w", err)
	}

	if err = s.runScriptWithTimeout(ctx, path); err != nil {
		return err
	}

	_, _, rmErr := s.RunCmd(ctx, "rm "+path, false)
	if rmErr != nil {
		clog.Warn(ctx, "failed to remove script", "path", path, "err", rmErr)
	}

	return nil
}

// runScriptWithTimeout runs the already-uploaded script at the given path on
// the server, returning an error if it fails (including its STDERR) or doesn't
// complete within pcsTimeOut.
func (s *Server) runScriptWithTimeout(ctx context.Context, path string) error {
	limit := time.After(pcsTimeOut)
	exiterr := make(chan error, 1)

	var stderr string

	go func() {
		var runerr error

		_, stderr, runerr = s.RunCmd(ctx, path, false)
		exiterr <- runerr
	}()

	select {
	case err := <-exiterr:
		if err == nil {
			return nil
		}

		err = fmt.Errorf("cloud server script failed: %w", err)
		if len(stderr) > 0 {
			err = fmt.Errorf("%w\nSTDERR:\n%s", err, stderr)
		}

		return err
	case <-limit:
		return fmt.Errorf("%w %s", errScriptTimeout, pcsTimeOut)
	}
}

// SetDestroyScript will result in future Destroy() calls first running the
// given script over ssh, if possible.
func (s *Server) SetDestroyScript(preDestroyScript []byte) {
	s.mutex.Lock()
	defer s.mutex.Unlock()

	s.DestroyScript = preDestroyScript
}

// Matches tells you if in principle a Server has the given os, script, config
// files, flavor and has a shared disk mounted. Useful before calling
// HasSpaceFor, since if you don't match these things you can't use the Server
// regardless of how empty it is. configFiles is in the CopyOver() format.
func (s *Server) Matches(os string, script []byte, configFiles string, flavor *Flavor, sharedDisk bool) bool {
	return s.OS == os &&
		bytes.Equal(s.Script, script) &&
		s.ConfigFiles == configFiles &&
		(flavor == nil || flavor.ID == s.Flavor.ID) &&
		s.SharedDisk == sharedDisk
}

// Allocate considers the current usage (according to prior calls)
// and records the given resources have now been used up on this server, if
// there was enough space. Returns true if there was enough space and the
// allocation occurred.
func (s *Server) Allocate(ctx context.Context, cores float64, ramMB, diskGB int) bool {
	ctx = s.getContextWithServerID(ctx)

	s.mutex.Lock()
	defer s.mutex.Unlock()

	if s.checkSpace(cores, ramMB, diskGB) == 0 {
		return false
	}

	s.used = true

	if cores == 0 {
		s.usedZeroCores++
	} else {
		s.usedCores = mth.FloatAdd(s.usedCores, cores)
	}

	s.usedRAM += ramMB
	s.usedDisk += diskGB

	clog.Debug(ctx, "server allocate", "cores", cores, "RAM", ramMB, "disk", diskGB, "usedCores",
		s.usedCores, "usedZeroCores", s.usedZeroCores, "usedRAM", s.usedRAM, "usedDisk", s.usedDisk)

	s.cancelDeathrow()

	return true
}

// cancelDeathrow cancels any in-progress countdown to destruction. You must
// hold the mutex.
func (s *Server) cancelDeathrow() {
	if !s.onDeathrow {
		return
	}

	s.cancels++

	go func() {
		s.cancelDestruction <- true
	}()
}

// getContextWithServerID returns the context with the server id set. For localhost
// it'll set the server id to localhost.
func (s *Server) getContextWithServerID(ctx context.Context) context.Context {
	if s.Name == localhostName {
		return clog.ContextWithServerID(ctx, localhostName)
	}

	return clog.ContextWithServerID(ctx, s.ID)
}

// Used tells you if this server has ever had Allocate() called on it.
func (s *Server) Used() bool {
	s.mutex.RLock()
	defer s.mutex.RUnlock()

	return s.used
}

// Release records that the given resources have now been freed.
func (s *Server) Release(ctx context.Context, cores float64, ramMB, diskGB int) {
	ctx = s.getContextWithServerID(ctx)

	s.mutex.Lock()
	defer s.mutex.Unlock()

	if cores == 0 {
		s.usedZeroCores--
	} else {
		s.usedCores = mth.FloatSubtract(s.usedCores, cores)
	}

	s.usedRAM -= ramMB
	s.usedDisk -= diskGB
	clog.Debug(ctx, "server release", "cores", cores, "RAM", ramMB, "disk", diskGB, "usedCores", s.usedCores,
		"usedZeroCores", s.usedZeroCores, "usedRAM", s.usedRAM, "usedDisk", s.usedDisk)

	// if the server is now doing nothing, we'll initiate a countdown to
	// destroying the host
	if s.usedCores <= 0 && s.usedZeroCores <= 0 && s.usedRAM <= 0 && s.TTD.Seconds() > 0 {
		clog.Debug(ctx, "server idle")
		go s.startDeathrowCountdown(ctx)
	}
}

// startDeathrowCountdown puts an idle server on "deathrow": after s.TTD with no
// new allocations it will be destroyed. A subsequent Allocate() (which sends on
// s.cancelDestruction) cancels the countdown. Intended to be run in a goroutine.
func (s *Server) startDeathrowCountdown(ctx context.Context) {
	defer internal.LogPanic(ctx, "server release", false)

	if !s.enterDeathrow(ctx) {
		return
	}

	timeToDie := time.After(s.TTD)
	clog.Debug(ctx, "server entering deathrow", "death", time.Now().Add(s.TTD))

	select {
	case <-s.cancelDestruction:
		// *** this block needed to fail the "Run lots of jobs on a
		// deathrow server" scheduler test prior to fix, but we have
		// no reasonable way for a scheduler test to turn this on...
		// s.mutex.RLock()
		// if s.cancels <= 5 {
		// 	s.mutex.RUnlock()
		// 	<-time.After(2 * time.Second)
		// } else {
		// 	s.mutex.RUnlock()
		// }
		s.mutex.Lock()
		for i := 1; i < s.cancels; i++ {
			<-s.cancelDestruction
		}

		s.cancels = 0
		s.onDeathrow = false

		s.mutex.Unlock()
		clog.Debug(ctx, "server cancelled deathrow")
	case <-timeToDie:
		// destroy the server
		s.mutex.Lock()
		s.onDeathrow = false
		s.toBeDestroyed = true
		s.mutex.Unlock()
		err := s.Destroy(ctx)
		clog.Debug(ctx, "server died on deathrow", "err", err)
	}
}

// enterDeathrow marks the server as being on deathrow, returning false (without
// doing so) if it is already on deathrow or has been allocated to again.
func (s *Server) enterDeathrow(ctx context.Context) bool {
	s.mutex.Lock()
	defer s.mutex.Unlock()

	if s.onDeathrow {
		clog.Debug(ctx, "server already on deathrow")

		return false
	}

	if s.usedCores > 0 || s.usedRAM > 0 {
		clog.Debug(ctx, "allocated before entering deathrow")

		return false
	}

	s.cancelDestruction = make(chan bool)
	s.onDeathrow = true

	return true
}

// HasSpaceFor considers the current usage (according to prior Allocation calls)
// and tells you how many of a cmd needing the given resources can run on this
// server.
func (s *Server) HasSpaceFor(cores float64, ramMB, diskGB int) int {
	s.mutex.RLock()
	defer s.mutex.RUnlock()

	return s.checkSpace(cores, ramMB, diskGB)
}

// checkSpace does the work of HasSpaceFor. You must hold a read lock on mutex!
func (s *Server) checkSpace(cores float64, ramMB, diskGB int) int {
	if s.destroyed {
		return 0
	}

	notEnoughCores := mth.FloatLessThan(float64(s.Flavor.Cores)-s.usedCores, cores)
	if notEnoughCores || (s.Flavor.RAM-s.usedRAM < ramMB) || (s.Disk-s.usedDisk < diskGB) {
		return 0
	}

	canDo := s.coresCanDo(cores)

	return capByRAMAndDisk(canDo, s.Flavor.RAM-s.usedRAM, ramMB, s.Disk-s.usedDisk, diskGB)
}

// coresCanDo works out how many commands needing the given number of cores can
// still fit on this server's free cores. You must hold a read lock on mutex.
func (s *Server) coresCanDo(cores float64) int {
	if cores != 0 {
		return int(math.Floor(mth.FloatSubtract(float64(s.Flavor.Cores), s.usedCores) / cores))
	}

	// rather than allow an infinite or very large number of cmds to run on
	// this server, because there are still real limits on the number of
	// processes we can run at once before things start falling over, we
	// only allow double the actual core count of zero core things to run
	// (on top of up to actual core count of non-zero core things).
	// On a server with "zero" cores, we also allow a reasonable number of
	// zero core jobs to run
	if s.Flavor.Cores == 0 {
		return internal.ZeroCoreMultiplier*internal.ZeroCoreMultiplier - s.usedZeroCores
	}

	return s.Flavor.Cores*internal.ZeroCoreMultiplier - s.usedZeroCores
}

// createSSHClientConfig creates an ssh client config and stores it on self.
func (s *Server) createSSHClientConfig(ctx context.Context) error {
	if s.PrivateKey == "" {
		if s.provider != nil && s.provider.PrivateKey() == "" {
			clog.Error(ctx, "resource file did not contain the ssh key", "path", s.provider.savePath)
		}

		return errMissingSSHKey
	}

	// parse private key and make config
	signer, err := ssh.ParsePrivateKey([]byte(s.PrivateKey))
	if err != nil {
		path := "unknown"
		if s.provider != nil {
			path = s.provider.savePath
		}

		clog.Error(ctx, "failed to parse private key", "path", path, "err", err)

		return err
	}

	s.sshClientConfig = &ssh.ClientConfig{
		User: s.UserName,
		Auth: []ssh.AuthMethod{
			ssh.PublicKeys(signer),
		},
		// *** we don't currently know a freshly spawned server's host key, so we
		// can't yet use ssh.FixedHostKey(publicKey) instead.
		HostKeyCallback: ssh.InsecureIgnoreHostKey(), //nolint:gosec // host key not known for fresh cloud servers
		Timeout:         sshShortTimeOut,
	}

	return nil
}

// SSHClient returns an ssh.Client object that could be used to ssh to the
// server. Requires that port 22 is accessible for SSH. The client returned will
// be one that hasn't failed to create a session yet; a new client will be
// created if necessary. You get back the client's index, so that if this client
// fails to create a session you can mark this client as bad.
func (s *Server) SSHClient(ctx context.Context) (*ssh.Client, int, error) {
	ctx = s.getContextWithServerID(ctx)
	s.mutex.Lock()
	defer s.mutex.Unlock()

	// return a client that is still good (most likely to be a more recent
	// client)
	if client, index, ok := s.reuseGoodSSHClient(); ok {
		return client, index, nil
	}

	// create a new client, add it to the pool
	if s.sshClientConfig == nil {
		err := s.createSSHClientConfig(ctx)
		if err != nil {
			return nil, 0, err
		}
	}

	client, err := s.dialNewSSHClient(ctx)
	if err != nil {
		return nil, 0, err
	}

	s.sshClients = append(s.sshClients, client)
	s.sshClientSessions = append(s.sshClientSessions, 1)
	s.sshStarted = true

	return client, len(s.sshClients) - 1, nil
}

// reuseGoodSSHClient returns an existing ssh client that still has session
// capacity (incrementing its session count), preferring more recent clients.
// The bool is false if no such client exists. You must hold the mutex.
func (s *Server) reuseGoodSSHClient() (*ssh.Client, int, bool) {
	for i := len(s.sshClients) - 1; i >= 0; i-- {
		if s.sshClientSessions[i] < maxSSHSessions {
			s.sshClientSessions[i]++

			return s.sshClients[i], i, true
		}
	}

	return nil, 0, false
}

// dialNewSSHClient dials a new ssh connection to the server, retrying on errors
// that indicate the network or server isn't ready for ssh yet (waiting up to
// sshTimeOut if we only just created this server). You must hold the mutex.
func (s *Server) dialNewSSHClient(ctx context.Context) (*ssh.Client, error) {
	hostAndPort := s.IP + ":22"

	client, err := sshDial(ctx, hostAndPort, s.sshClientConfig)
	if err == nil {
		return client, nil
	}

	// if we're trying to destroy this server, just give up straight away
	if s.destroyed {
		return nil, err
	}

	// otherwise, keep trying
	return s.retryDialSSHClient(ctx, hostAndPort)
}

// retryDialSSHClient repeatedly re-dials the server (once a second) until a
// connection succeeds, a fatal error occurs, sshTimeOut elapses, or ctx is
// cancelled. You must hold the mutex.
func (s *Server) retryDialSSHClient(ctx context.Context, hostAndPort string) (*ssh.Client, error) {
	limit := time.After(sshTimeOut)
	ticker := time.NewTicker(1 * time.Second)
	ticks := 0

	for {
		select {
		case <-ticker.C:
			client, err := sshDial(ctx, hostAndPort, s.sshClientConfig)

			if done, derr := s.assessDialAttempt(err, &ticks); done {
				ticker.Stop()

				return client, derr
			}
		case <-limit:
			ticker.Stop()

			return nil, errSSHGaveUp
		case <-ctx.Done():
			ticker.Stop()

			return nil, errSSHCancelled
		}
	}
}

// assessDialAttempt decides, given the latest dial error, whether
// dialNewSSHClient should stop. When done is true, the returned error is the
// result to return (nil on success). ticks counts only non-startup attempts and
// is incremented here when appropriate, matching the original loop. You must
// hold the mutex.
func (s *Server) assessDialAttempt(err error, ticks *int) (done bool, result error) {
	if handled, sdone, sresult := s.handleStartupDialError(err); handled {
		return sdone, sresult
	}

	*ticks++

	if errors.Is(err, errConnectionAttemptCancelled) {
		return true, err
	}

	// if it worked, we stop trying; if it failed again with a different error,
	// we keep trying for at least maxDialTicks seconds
	if err == nil || *ticks == maxDialTicks || !s.created {
		return true, err
	}

	return false, nil
}

// handleStartupDialError handles the "ssh still starting up" class of dial
// errors: if not such an error, handled is false. Otherwise we keep waiting
// (done false), unless ssh had previously worked and there's now no route to
// host, in which case we give up.
func (s *Server) handleStartupDialError(err error) (handled, done bool, result error) {
	if !sshMayStillBeStarting(err, s.created) {
		return false, false, nil
	}

	if s.sshStarted && strings.HasSuffix(err.Error(), "no route to host") {
		return true, true, errNoRouteToHostNow
	}

	return true, false, nil
}

// sshDial calls ssh.Dial() and enforces the config's timeout, which ssh.Dial()
// doesn't always seem to obey.
func sshDial(ctx context.Context, addr string, sshConfig *ssh.ClientConfig) (*ssh.Client, error) {
	clientCh := make(chan *ssh.Client, 1)
	errCh := make(chan error, 1)

	go func() {
		defer internal.LogPanic(ctx, "sshDial", false)

		sshClient, err := ssh.Dial("tcp", addr, sshConfig)
		clientCh <- sshClient

		errCh <- err
	}()

	deadline := time.After(sshConfig.Timeout + 1*time.Second)
	select {
	case err := <-errCh:
		return <-clientCh, err
	case <-deadline:
		return nil, errConnectionCouldNotBeEstablished
	case <-ctx.Done():
		return nil, errConnectionAttemptCancelled
	}
}

func sshMayStillBeStarting(err error, created bool) bool {
	if err == nil {
		return false
	}

	if sshHasStartupSuffix(err.Error()) {
		return true
	}

	if !created {
		return false
	}

	return errors.Is(err, errConnectionCouldNotBeEstablished) ||
		strings.HasSuffix(err.Error(), errConnectionCouldNotBeEstablished.Error())
}

// SSHSession returns an ssh.Session object that could be used to do things via
// ssh on the server. Will time out and return an error if the session can't be
// created within 5s. Also returns the index of the client this session came
// from, so that when you can call CloseSSHSession() when you're done with the
// returned session.
func (s *Server) SSHSession(ctx context.Context) (*ssh.Session, int, error) {
	ctx = s.getContextWithServerID(ctx)

	sshClient, clientIndex, err := s.SSHClient(ctx)
	if err != nil {
		clog.Debug(ctx, "server ssh could not be established", "err", err)

		return nil, clientIndex, fmt.Errorf("cloud SSHSession() failed to get a client: %w", err)
	}

	session, err := newSSHSessionWithTimeout(ctx, sshClient, clientIndex)
	if err != nil {
		s.mutex.Lock()
		// pretend we're now at max sessions, so this client won't be used again
		// in the future, at least until sessions get closed, when it might
		// start working again
		s.sshClientSessions[clientIndex] = maxSSHSessions
		s.mutex.Unlock()

		return nil, clientIndex, err
	}

	return session, clientIndex, nil
}

// newSSHSessionWithTimeout creates a new session on the given ssh client,
// enforcing our own sshShortTimeOut (and honouring ctx cancellation) because
// sshClient.NewSession() can otherwise hang forever against a dead server.
func newSSHSessionWithTimeout(ctx context.Context, sshClient *ssh.Client, clientIndex int) (*ssh.Session, error) {
	done := make(chan error, 1)
	worked := make(chan bool, 1)
	sessionCh := make(chan *ssh.Session, 1)

	go watchSSHSessionTimeout(ctx, clientIndex, worked, done)
	go openSSHSession(ctx, sshClient, clientIndex, worked, sessionCh, done)

	if err := <-done; err != nil {
		return nil, err
	}

	return <-sessionCh, nil
}

// providerDestroyContext detaches caller cancellation from provider deletion
// while preserving values and any deadline on the original context.
func providerDestroyContext(ctx context.Context) (context.Context, context.CancelFunc) {
	detachedCtx := context.WithoutCancel(ctx)

	deadline, ok := ctx.Deadline()
	if !ok {
		return detachedCtx, func() {}
	}

	return context.WithDeadline(detachedCtx, deadline)
}

// watchSSHSessionTimeout sends a timeout or cancellation error on done if the
// session isn't established (signalled on worked) within sshShortTimeOut or
// before ctx is cancelled. Intended to be run in a goroutine.
func watchSSHSessionTimeout(ctx context.Context, clientIndex int, worked chan bool, done chan error) {
	select {
	case <-time.After(sshShortTimeOut):
		clog.Debug(ctx, "server ssh timed out", "clientindex", clientIndex)

		done <- errSessionTimedOut
	case <-ctx.Done():
		clog.Debug(ctx, "server ssh cancelled", "clientindex", clientIndex)

		done <- errSessionCancelled
	case <-worked:
		return
	}
}

// openSSHSession opens a new session on sshClient, signalling success on worked
// and delivering the session on sessionCh, or an error on done. Intended to be
// run in a goroutine.
func openSSHSession(ctx context.Context, sshClient *ssh.Client, clientIndex int, worked chan bool,
	sessionCh chan *ssh.Session, done chan error,
) {
	defer internal.LogPanic(ctx, "server sshsession", false)

	session, errf := sshClient.NewSession()
	if errf != nil {
		clog.Debug(ctx, "server ssh failed", "err", errf, "clientindex", clientIndex)

		done <- fmt.Errorf("cloud SSHSession() failed to establish a session: %w", errf)

		return
	}

	worked <- true

	done <- nil

	sessionCh <- session
}

// CloseSSHSession is used to close a session opened with SSHSession(). If the
// client used to create the session (as indicated by the supplied index, also
// retrieved from SSHSession()) was marked as bad, it will now be marked as
// good, on the assumption there is now "space" for a new session.
func (s *Server) CloseSSHSession(ctx context.Context, session *ssh.Session, clientIndex int) {
	ctx = s.getContextWithServerID(ctx)
	err := session.Close()
	s.closeWarning(ctx, err)

	s.mutex.Lock()
	defer s.mutex.Unlock()

	s.sshClientSessions[clientIndex]--
}

// closeWarning warns about the given error if not nil, unless it is expected
// in a close situation.
func (s *Server) closeWarning(ctx context.Context, err error) {
	if err != nil && !isExpectedCloseError(err) {
		clog.Warn(ctx, "failed to close ssh session", "err", err)
	}
}

func isExpectedCloseError(err error) bool {
	return errors.Is(err, io.EOF) ||
		errors.Is(err, net.ErrClosed) ||
		strings.Contains(err.Error(), "use of closed network connection")
}

// SFTPClient is like sftp.NewClient(), but the underlying
// clientConn.conn.WriteCloser is mutex protected to avoid data races between
// closes due to errors and direct Close() calls on the *sftp.Client.
func SFTPClient(conn *ssh.Client) (*sftp.Client, error) {
	s, err := conn.NewSession()
	if err != nil {
		return nil, err
	}

	if err = s.RequestSubsystem("sftp"); err != nil {
		return nil, err
	}

	pw, err := s.StdinPipe()
	if err != nil {
		return nil, err
	}

	pw = &threadSafeWriteCloser{WriteCloser: pw}

	pr, err := s.StdoutPipe()
	if err != nil {
		return nil, err
	}

	return sftp.NewClientPipe(pr, pw)
}

type threadSafeWriteCloser struct {
	io.WriteCloser
	sync.Mutex
}

func (c *threadSafeWriteCloser) Close() error {
	c.Lock()
	defer c.Unlock()

	return c.WriteCloser.Close()
}

// RunCmd runs the given command on the server, optionally in the background.
// You get the command's STDOUT and STDERR as strings.
func (s *Server) RunCmd(ctx context.Context, cmd string, background bool) (stdout, stderr string, err error) {
	ctx = s.getContextWithServerID(ctx)
	// create a session
	session, clientIndex, err := s.SSHSession(ctx)
	if err != nil {
		return stdout, stderr, err
	}

	defer s.CloseSSHSession(ctx, session, clientIndex)

	// if the sever is destroyed while running, arrange to immediately return an
	// error
	s.mutex.Lock()
	cancelID, cancelCh := s.registerCancelChannel()
	done := make(chan error, 1)
	outCh := make(chan string, 1)
	errCh := make(chan string, 1)
	finished := make(chan bool, 1)

	go s.watchRunCmdCancellation(ctx, cancelID, cancelCh, finished, outCh, errCh, done)
	go s.runCmdOnSession(ctx, session, cmd, background, finished, outCh, errCh, done)

	s.mutex.Unlock()

	err = <-done
	stdout = <-outCh
	stderr = <-errCh

	return stdout, stderr, err
}

// registerCancelChannel allocates a unique cancellation id and channel for an
// in-flight RunCmd and records it so Destroy() can cancel the command. You must
// hold the mutex.
func (s *Server) registerCancelChannel() (int, chan bool) {
	cancelID := s.cancelID
	s.cancelID = cancelID + 1
	cancelCh := make(chan bool, 1)
	s.cancelRunCmd[cancelID] = cancelCh

	return cancelID, cancelCh
}

// watchRunCmdCancellation waits for the command to finish, the server to be
// destroyed, or ctx to be cancelled; in the latter two cases it sends empty
// output and a cancellation error on the channels. It always deregisters the
// command's cancel channel before returning. Intended to be run in a goroutine.
func (s *Server) watchRunCmdCancellation(ctx context.Context, cancelID int, cancelCh, finished chan bool,
	outCh, errCh chan string, done chan error,
) {
	defer internal.LogPanic(ctx, "server runcmd cancellation", false)

	select {
	case <-cancelCh:
		outCh <- ""

		errCh <- ""

		done <- fmt.Errorf("%w %s", errRunCmdDestroyed, s.ID)
	case <-ctx.Done():
		outCh <- ""

		errCh <- ""

		done <- fmt.Errorf("%w %s cancelled on request", errRunCmdCancelled, s.ID)
	case <-finished:
		// end select
	}

	s.mutex.Lock()
	close(cancelCh)
	delete(s.cancelRunCmd, cancelID)
	s.mutex.Unlock()
}

// runCmdOnSession runs cmd on the given session (optionally backgrounded),
// reporting its stdout, stderr and result on the channels. Intended to be run
// in a goroutine.
func (s *Server) runCmdOnSession(ctx context.Context, session *ssh.Session, cmd string, background bool,
	finished chan bool, outCh, errCh chan string, done chan error,
) {
	defer internal.LogPanic(ctx, "server runcmd", false)

	// run the command, returning stdout
	if background {
		cmd = "sh -c 'nohup " + cmd + " > /dev/null 2>&1 &'"
	}

	var (
		o bytes.Buffer
		e bytes.Buffer
	)

	session.Stdout = &o
	session.Stderr = &e
	errf := session.Run(cmd)

	finished <- true

	// Buffer.String() returns "" for an empty buffer, which is what we want
	outCh <- o.String()

	errCh <- e.String()

	if errf != nil {
		done <- fmt.Errorf("cloud RunCmd(%s) failed: %w", cmd, errf)
	} else {
		done <- nil
	}
}

// sftpClient establishes an ssh client to the server and returns an sftp client
// on top of it. The caller is responsible for closing the returned client.
func (s *Server) sftpClient(ctx context.Context) (*sftp.Client, error) {
	sshClient, _, err := s.SSHClient(ctx)
	if err != nil {
		return nil, err
	}

	return SFTPClient(sshClient)
}

// UploadFile uploads a local file to the given location on the server.
func (s *Server) UploadFile(ctx context.Context, source string, dest string) error {
	ctx = s.getContextWithServerID(ctx)

	client, err := s.sftpClient(ctx)
	if err != nil {
		return err
	}

	defer internal.LogClose(ctx, client, "upload file client session", "source", source, "dest", dest)

	// create all parent dirs of dest
	err = s.MkDir(ctx, filepath.Dir(dest))
	if err != nil {
		return err
	}

	// open source, create dest
	sourceFile, err := os.Open(source)
	if err != nil {
		return err
	}

	defer internal.LogClose(ctx, sourceFile, "upload file source", "source", source, "dest", dest)

	destFile, err := client.Create(dest)
	if err != nil {
		return err
	}

	// copy the file content over
	_, err = io.Copy(destFile, sourceFile)

	return err
}

// CopyOver uploads the given local files to the corresponding locations on the
// server. files argument is a comma separated list of local file paths.
// Absolute paths are uploaded to the same absolute path on the server. Paths
// beginning with ~/ are uploaded from the local home directory to the server's
// home directory.
//
// If local path and desired remote path are unrelated, the paths can be
// separated with a colon.
//
// If a specified local path does not exist, it is silently ignored, allowing
// the specification of multiple possible config files when you might only have
// one. The mtimes of the files are retained.
//
// NB: currently only works if the server supports the command 'pwd'.
func (s *Server) CopyOver(ctx context.Context, files string) error {
	ctx = s.getContextWithServerID(ctx)

	for path := range strings.SplitSeq(files, ",") {
		if err := s.copyOverPath(ctx, path); err != nil {
			return err
		}
	}

	return nil
}

// copyOverPath handles a single comma-separated entry of CopyOver's files
// argument. A local path that doesn't exist is silently skipped.
func (s *Server) copyOverPath(ctx context.Context, path string) error {
	localPath, remotePath := splitCopyOverPath(path)

	// ignore if it doesn't exist locally
	localPath = internal.TildaToHome(localPath)

	info, exists := localFileInfo(localPath)
	if !exists {
		return nil
	}

	if strings.HasPrefix(remotePath, "~/") {
		homeDir, errh := s.HomeDir(ctx)
		if errh != nil {
			return errh
		}

		remotePath = strings.TrimLeft(remotePath, "~/")
		remotePath = filepath.Join(homeDir, remotePath)
	}

	if err := s.UploadFile(ctx, localPath, remotePath); err != nil {
		return err
	}

	// if these are config files we likely need to make them user-only read,
	// and if they're not, I can't see how it matters if group/all can't
	// read? This is a single user server and I'm the only one using it...
	if _, _, err := s.RunCmd(ctx, "chmod 600 "+remotePath, false); err != nil {
		return err
	}

	// sometimes the mtime of the file matters, so we try and set that on
	// the remote copy
	_, _, err := s.RunCmd(ctx, fmt.Sprintf("touch -d %s %s", info.ModTime().Format(touchStampFormat), remotePath), false)

	return err
}

// localFileInfo returns os.Stat info for the given local path, and whether it
// could be stat'd at all (a path we can't stat is treated as not present, and
// silently skipped by CopyOver).
func localFileInfo(localPath string) (os.FileInfo, bool) {
	info, err := os.Stat(localPath)

	return info, err == nil
}

// splitCopyOverPath splits a CopyOver path into its local and remote parts. A
// "local:remote" form specifies them separately; otherwise both are the path.
func splitCopyOverPath(path string) (localPath, remotePath string) {
	split := strings.Split(path, ":")
	if len(split) == localRemotePathParts {
		return split[0], split[1]
	}

	return path, path
}

// HomeDir gets the absolute path to the server's home directory. Depends on
// 'pwd' command existing on the server.
func (s *Server) HomeDir(ctx context.Context) (string, error) {
	ctx = s.getContextWithServerID(ctx)
	s.hmutex.Lock()
	defer s.hmutex.Unlock()

	if s.homeDir != "" {
		return s.homeDir, nil
	}

	stdout, _, err := s.RunCmd(ctx, "pwd", false)
	if err != nil {
		return "", err
	}

	s.homeDir = strings.TrimSuffix(stdout, "\n")

	return s.homeDir, nil
}

// CreateFile creates a new file with the given content on the server.
func (s *Server) CreateFile(ctx context.Context, content string, dest string) error {
	ctx = s.getContextWithServerID(ctx)

	client, err := s.sftpClient(ctx)
	if err != nil {
		return err
	}

	defer internal.LogClose(ctx, client, "create file client session")

	// create all parent dirs of dest
	err = s.MkDir(ctx, filepath.Dir(dest))
	if err != nil {
		return err
	}

	// create dest
	destFile, err := client.OpenFile(dest, os.O_WRONLY|os.O_CREATE|os.O_TRUNC)
	if err != nil {
		return err
	}

	// write the content
	_, err = io.WriteString(destFile, content)

	return err
}

// DownloadFile downloads a file from the server and stores it locally. The
// directory for your local file must already exist.
func (s *Server) DownloadFile(ctx context.Context, source string, dest string) error {
	ctx = s.getContextWithServerID(ctx)

	client, err := s.sftpClient(ctx)
	if err != nil {
		return err
	}

	defer internal.LogClose(ctx, client, "download file client session", "source", source, "dest", dest)

	// open source, create dest
	sourceFile, err := client.Open(source)
	if err != nil {
		return err
	}

	defer internal.LogClose(ctx, sourceFile, "download file source", "source", source, "dest", dest)

	destFile, err := os.Create(dest)
	if err != nil {
		return err
	}

	// copy the file content over
	_, err = io.Copy(destFile, sourceFile)
	if err != nil {
		return err
	}

	return os.Chmod(dest, ownerReadWrite)
}

// MkDir creates a directory (and it's parents as necessary) on the server.
// Requires sudo.
func (s *Server) MkDir(ctx context.Context, dir string) error {
	ctx = s.getContextWithServerID(ctx)

	if dir == "." {
		return nil
	}

	// *** it would be nice to do this with client.Mkdir, but that doesn't do
	// the equivalent of mkdir -p, and errors out if dirs already exist... for
	// now it's easier to just call mkdir
	_, _, err := s.RunCmd(ctx, fmt.Sprintf("[ -d %s ]", dir), false)
	if err == nil {
		// dir already exists
		return nil
	}

	// try without sudo, so that if we create multiple dirs, they all have the
	// correct permissions
	_, _, err = s.RunCmd(ctx, "mkdir -p "+dir, false)
	if err == nil {
		return nil
	}

	// try again with sudo
	_, e, err := s.RunCmd(ctx, "sudo mkdir -p "+dir, false)
	if err != nil {
		return fmt.Errorf("%s; %w", e, err)
	}

	// correct permission on leaf dir *** not currently correcting permission on
	// any parent dirs we might have just made
	_, e, err = s.RunCmd(ctx, fmt.Sprintf("sudo chown %s:%s %s", s.UserName, s.UserName, dir), false)
	if err != nil {
		return fmt.Errorf("%s; %w", e, err)
	}

	return nil
}

func (s *Server) CreateSharedDisk() error {
	s.csmutex.Lock()
	defer s.csmutex.Unlock()

	if s.createdShare {
		return nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), createSharedDiskTimeout)
	defer cancel()

	if err := runBashCommand(ctx, "sudo apt-get update && sudo apt-get install nfs-kernel-server -y"); err != nil {
		return err
	}

	if err := s.ensureExportsEntry(ctx); err != nil {
		return err
	}

	if err := s.ensureSharePathDir(ctx); err != nil {
		return err
	}

	// the split of "export"+"fs" is to avoid a false-positive spelling mistake
	if err := runBashCommand(ctx, "sudo systemctl start nfs-kernel-server.service && sudo export"+"fs -a"); err != nil {
		return err
	}

	s.createdShare = true
	s.SharedDisk = true

	return nil
}

// runBashCommand runs the given command line via "bash -c" under ctx.
func runBashCommand(ctx context.Context, command string) error {
	cmd := exec.CommandContext(ctx, "bash", "-c", command)

	return cmd.Run()
}

// ensureExportsEntry adds an NFS export entry for sharePath to /etc/exports if
// one is not already present.
func (s *Server) ensureExportsEntry(ctx context.Context) error {
	f, err := os.Open("/etc/exports")
	if err != nil {
		return err
	}

	defer internal.LogClose(ctx, f, "/etc/exports")

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		if strings.HasPrefix(scanner.Text(), sharePath) {
			return nil
		}
	}

	return runBashCommand(ctx, fmt.Sprintf(
		"echo '%s *(rw,sync,no_root_squash)' | sudo tee --append /etc/exports > /dev/null", sharePath))
}

// ensureSharePathDir creates sharePath (owned by the server's user) if it does
// not already exist.
func (s *Server) ensureSharePathDir(ctx context.Context) error {
	if _, errs := os.Stat(sharePath); errs == nil || !os.IsNotExist(errs) {
		return nil
	}

	if err := runBashCommand(ctx, "sudo mkdir "+sharePath); err != nil {
		return err
	}

	return runBashCommand(ctx, fmt.Sprintf("sudo chown %s:%s %s", s.UserName, s.UserName, sharePath))
}

// MountSharedDisk can be used to mount a share from another Server (identified
// by its IP address) that you called CreateSharedDisk() on. The shared disk
// will be accessible at /shared. Does nothing and returns nil if the share was
// already mounted (or created on this Server). NB: currently hard-coded to use
// apt-get to install nfs-common on the server first, so probably only
// compatible with Ubuntu. Requires sudo.
func (s *Server) MountSharedDisk(ctx context.Context, nfsServerIP string) error {
	ctx = s.getContextWithServerID(ctx)
	s.csmutex.Lock()
	defer s.csmutex.Unlock()

	if s.createdShare {
		return nil
	}

	_, _, err := s.RunCmd(ctx, "sudo apt-get update && sudo apt-get install nfs-common -y", false)
	if err != nil {
		return err
	}

	err = s.MkDir(ctx, sharePath)
	if err != nil {
		return err
	}

	clog.Debug(ctx, "ran MkDir")

	if err = s.mountNFS(ctx, nfsServerIP); err != nil {
		return err
	}

	s.createdShare = true
	s.SharedDisk = true

	clog.Debug(ctx, "mounted shared disk")

	return nil
}

// mountNFS mounts the NFS share exported by nfsServerIP at sharePath, logging
// the command output on failure.
func (s *Server) mountNFS(ctx context.Context, nfsServerIP string) error {
	stdo, stde, err := s.RunCmd(ctx, fmt.Sprintf("sudo mount %s:%s %s", nfsServerIP, sharePath, sharePath), false)
	if err != nil {
		clog.Error(ctx, "mount attempt failed", "stdout", stdo, "stderr", stde)

		return err
	}

	return nil
}

// GoneBad lets you mark a server as having something wrong with it, so you can
// avoid using it in the future, until the problems are confirmed. (At that
// point you'd either Destroy() it, or if this was a false alarm, call
// NotBad()).
//
// The optional permanentProblem arg (some explanatory error message) makes it
// such that NotBad() will have no effect. For use when the server is Alive()
// but you just never want to re-use this server. The only reason you don't just
// Destroy() it is that you want to allow an end user to investigate the server
// manually.
func (s *Server) GoneBad(permanentProblem ...string) {
	s.mutex.Lock()
	defer s.mutex.Unlock()

	s.goneBad = time.Now()

	if len(permanentProblem) == 1 {
		s.permanentProblem = permanentProblem[0]
	}
}

// NotBad lets you change your mind about a server you called GoneBad() on.
// (Unless GoneBad() was called with a permanentProblem, or the server has been
// destroyed).
func (s *Server) NotBad() bool {
	s.mutex.Lock()
	defer s.mutex.Unlock()

	if !s.destroyed && !s.toBeDestroyed && s.permanentProblem == "" {
		s.goneBad = time.Time{}

		return true
	}

	return false
}

// IsBad tells you if GoneBad() has been called (more recently than NotBad()).
func (s *Server) IsBad() bool {
	s.mutex.RLock()
	defer s.mutex.RUnlock()

	return !s.goneBad.IsZero()
}

// BadDuration tells you how long it has been since the last GoneBad() call
// (when there hasn't been a NotBad() call since). Returns 0 seconds if not
// actually bad right now.
func (s *Server) BadDuration() time.Duration {
	s.mutex.RLock()
	defer s.mutex.RUnlock()

	if s.goneBad.IsZero() {
		return 0 * time.Second
	}

	return time.Since(s.goneBad)
}

// PermanentProblem tells you if GoneBad("problem message") has been called,
// returning that reason the server is not usable.
func (s *Server) PermanentProblem() string {
	s.mutex.RLock()
	defer s.mutex.RUnlock()

	return s.permanentProblem
}

// Destroy destroys the server, first trying to run any script that was set with
// SetDestroyScript().
func (s *Server) Destroy(ctx context.Context) error {
	ctx = s.getContextWithServerID(ctx)
	s.mutex.Lock()
	defer s.mutex.Unlock()

	if s.destroyed {
		return nil
	}

	s.cancelInFlightWork()

	s.toBeDestroyed = false
	s.destroyed = true

	s.attemptCleanShutdown(ctx)
	s.closeSSHClients(ctx)

	if s.goneBad.IsZero() {
		s.goneBad = time.Now()
	}

	// for testing purposes, we anticipate that provider isn't set
	if s.provider == nil {
		return errProviderNotSet
	}

	// Provider deletion should survive caller cancellation while retaining ctx
	// values and any deadline.
	providerCtx, cancelProviderCtx := providerDestroyContext(ctx)
	defer cancelProviderCtx()

	return s.destroyViaProvider(providerCtx)
}

// attemptCleanShutdown, if ssh has ever worked for this server, ssh's in to run
// any destroy script and cleanly shut down. It briefly releases the mutex while
// doing so (which the caller must hold), reacquiring it before returning.
func (s *Server) attemptCleanShutdown(ctx context.Context) {
	if !s.sshStarted {
		return
	}

	destroyScript := s.DestroyScript
	s.mutex.Unlock()
	s.cleanShutdownOverSSH(ctx, destroyScript)
	s.mutex.Lock()
}

// cancelInFlightWork cancels any deathrow countdown and signals any in-progress
// RunCmd calls to return an error. You must hold the mutex.
func (s *Server) cancelInFlightWork() {
	// if the server has initiated its countdown to destruction, cancel that
	if s.onDeathrow {
		s.cancelDestruction <- true
	}

	// if the user is in the middle of RunCmd(), have those return an error now
	for _, ch := range s.cancelRunCmd {
		ch <- true
	}
}

// closeSSHClients explicitly closes any open ssh client connections, warning
// about unexpected close errors. You must hold the mutex.
func (s *Server) closeSSHClients(ctx context.Context) {
	for _, client := range s.sshClients {
		err := client.Close()
		s.closeWarning(ctx, err)
	}
}

// cleanShutdownOverSSH ssh's to the server to run any user destroy script and
// then the clean shutdown command. Failures are logged, not returned, since we
// are destroying the server regardless. Must be called without holding the
// mutex.
//
// We deliberately ssh using context.Background() rather than the caller's ctx:
// Destroy() is frequently called precisely because the caller's ctx was
// cancelled, but we still want to attempt a clean shutdown.
func (s *Server) cleanShutdownOverSSH(ctx context.Context, destroyScript []byte) {
	t := time.Now()

	// detached ctx (see doc comment)
	session, clientIndex, err := s.SSHSession(context.Background()) //nolint:contextcheck // detached, see doc comment
	if err != nil {
		clog.Warn(ctx, "failed to ssh to cleanly shutdown", "took", time.Since(t), "err", err)

		return
	}

	if len(destroyScript) > 0 {
		s.runDestroyScript(ctx, destroyScript)
	}

	t = time.Now()
	// detached ctx (see doc comment)
	//nolint:contextcheck // detached, see doc comment
	stdo, stde, err := s.RunCmd(context.Background(), cleanShutDownCmd, false)

	rt := time.Since(t)
	if err != nil {
		clog.Warn(ctx, "clean shutdown failed", "took", rt, "err", err, "stdout", stdo, "stderr", stde)
	} else if rt > maxCleanShutdownCmdTime {
		clog.Warn(ctx, "clean shutdown took a long time", "took", rt, "stdout", stdo)
	}

	s.CloseSSHSession(ctx, session, clientIndex)
}

// runDestroyScript runs the user's pre-destroy script over an existing ssh
// connection, logging (but not returning) any failure or slowness.
func (s *Server) runDestroyScript(ctx context.Context, destroyScript []byte) {
	t := time.Now()
	err := s.runScript(ctx, destroyScript)

	rt := time.Since(t)
	if err != nil {
		clog.Warn(ctx, "user destroy script failed", "took", rt, "err", err)
	} else if rt > maxCleanShutdownScriptTime {
		clog.Warn(ctx, "user destroy script took a long time", "took", rt)
	}
}

// destroyViaProvider asks the provider to destroy the server, treating a
// "server no longer exists" situation as success.
func (s *Server) destroyViaProvider(ctx context.Context) error {
	err := s.provider.DestroyServer(ctx, s.ID)
	clog.Debug(ctx, "server destroyed", "err", err)

	if err == nil {
		return nil
	}

	// check if the server exists
	ok, errc := s.provider.CheckServer(ctx, s.ID)
	if ok && errc == nil {
		return err
	}

	// if not, assume there's no Server and ignore this error (which may
	// just be along the lines of "the server doesn't exist")
	return nil
}

// Destroyed tells you if a server was destroyed using Destroy() or the
// automatic destruction due to being idle. It is NOT the opposite of Alive(),
// since it does not check if the server is still usable.
func (s *Server) Destroyed() bool {
	s.mutex.Lock()
	defer s.mutex.Unlock()

	return s.destroyed || s.toBeDestroyed
}

// Alive tells you if a server is usable. It first does the same check as
// Destroyed() before calling out to the provider. Supplying an optional boolean
// will double check the server to make sure it can be ssh'd to. If the server
// doesn't exist, it will be removed from the provider's resources file.
func (s *Server) Alive(ctx context.Context, checkSSH ...bool) bool {
	ctx = s.getContextWithServerID(ctx)
	s.mutex.Lock()
	if s.destroyed || s.toBeDestroyed {
		s.mutex.Unlock()

		return false
	}

	ok, errc := s.provider.CheckServer(ctx, s.ID)
	s.mutex.Unlock()

	if !ok || errc != nil {
		return false
	}

	if len(checkSSH) == 1 && checkSSH[0] {
		return s.aliveOverSSH(ctx)
	}

	return true
}

// aliveOverSSH double-checks a server the provider claims is fine really is
// usable, by confirming we can still ssh to it.
//
// We deliberately ssh using context.Background() rather than the caller's ctx:
// the session has its own timeout, and callers expect this check to work even
// when their ctx has been cancelled.
func (s *Server) aliveOverSSH(ctx context.Context) bool {
	// detached ctx (see doc comment)
	session, clientIndex, err := s.SSHSession(context.Background()) //nolint:contextcheck // detached, see doc comment
	if err != nil {
		return false
	}

	s.CloseSSHSession(ctx, session, clientIndex)

	return true
}

// Known tells you if a server exists according to the provider. This can
// return false even if the server exists, because the credentials you used for
// the provider are different to the ones used to create this server. If the
// server isn't known about, the provider's resource file is NOT updated,
// because this indicates you're using the wrong resource file for these
// credentials.
func (s *Server) Known(ctx context.Context) bool {
	ctx = s.getContextWithServerID(ctx)

	known, err := s.provider.ServerIsKnown(s.ID)
	if err != nil {
		clog.Warn(ctx, "could not check if the server is known about", "err", err)
	}

	return known
}

func sshHasStartupSuffix(errStr string) bool {
	startupSuffixes := [...]string{
		"connection timed out",
		"no route to host",
		"connection refused",
	}

	for _, suffix := range startupSuffixes {
		if strings.HasSuffix(errStr, suffix) {
			return true
		}
	}

	return false
}
