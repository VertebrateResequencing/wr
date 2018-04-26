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

package cloud

// This file contains the code for the Server struct.

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/VertebrateResequencing/wr/internal"
	"github.com/inconshreveable/log15"
	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
)

// Flavor describes a "flavor" of server, which is a certain (virtual) hardware
// configuration
type Flavor struct {
	ID    string
	Name  string
	Cores int
	RAM   int // MB
	Disk  int // GB
}

// Server provides details of the server that Spawn() created for you, and some
// methods that let you keep track of how you use that server.
type Server struct {
	AdminPass         string
	Disk              int // GB of available disk space
	Flavor            *Flavor
	ID                string
	IP                string // ip address that you could SSH to
	IsHeadNode        bool
	Name              string        // ought to correspond to the hostname
	OS                string        // the name of the Operating System image
	Script            []byte        // the content of a start-up script run on the server
	ConfigFiles       string        // files that you will CopyOver() and require to be on this Server, in CopyOver() format
	TTD               time.Duration // amount of idle time allowed before destruction
	UserName          string        // the username needed to log in to the server
	cancelDestruction chan bool
	cancelID          int
	cancelRunCmd      map[int]chan bool
	created           bool // to distinguish instances we discovered or spawned
	destroyed         bool
	goneBad           bool
	location          *time.Location
	mutex             sync.RWMutex
	onDeathrow        bool
	permanentProblem  string
	provider          *Provider
	sshclient         *ssh.Client
	usedCores         int
	usedDisk          int
	usedRAM           int
	homeDir           string
	hmutex            sync.Mutex
	logger            log15.Logger // (not embedded to make gob happy)
}

// Matches tells you if in principle a Server has the given os, script, config
// files and flavor. Useful before calling HasSpaceFor, since if you don't match
// these things you can't use the Server regardless of how empty it is.
// configFiles is in the CopyOver() format.
func (s *Server) Matches(os string, script []byte, configFiles string, flavor *Flavor) bool {
	return s.OS == os && bytes.Equal(s.Script, script) && s.ConfigFiles == configFiles && (flavor == nil || flavor.ID == s.Flavor.ID)
}

// Allocate records that the given resources have now been used up on this
// server.
func (s *Server) Allocate(cores, ramMB, diskGB int) {
	s.mutex.Lock()
	defer s.mutex.Unlock()
	s.usedCores += cores
	s.usedRAM += ramMB
	s.usedDisk += diskGB

	s.logger.Debug("server allocate", "cores", cores, "RAM", ramMB, "disk", diskGB, "usedCores", s.usedCores, "usedRAM", s.usedRAM, "usedDisk", s.usedDisk)

	// if the host has initiated its countdown to destruction, cancel that
	if s.onDeathrow {
		s.cancelDestruction <- true
	}
}

// Release records that the given resources have now been freed.
func (s *Server) Release(cores, ramMB, diskGB int) {
	s.mutex.Lock()
	defer s.mutex.Unlock()
	s.usedCores -= cores
	s.usedRAM -= ramMB
	s.usedDisk -= diskGB
	s.logger.Debug("server release", "cores", cores, "RAM", ramMB, "disk", diskGB, "usedCores", s.usedCores, "usedRAM", s.usedRAM, "usedDisk", s.usedDisk)

	// if the server is now doing nothing, we'll initiate a countdown to
	// destroying the host
	if s.usedCores <= 0 && s.TTD.Seconds() > 0 {
		s.logger.Debug("server idle")
		go func() {
			defer internal.LogPanic(s.logger, "server release", false)

			s.mutex.Lock()
			if s.onDeathrow {
				s.mutex.Unlock()
				s.logger.Debug("server already on deathrow")
				return
			}
			s.cancelDestruction = make(chan bool, 4) // *** the 4 is a hack to prevent deadlock, should find proper fix...
			s.onDeathrow = true
			s.mutex.Unlock()

			timeToDie := time.After(s.TTD)
			s.logger.Debug("server entering deathrow", "death", time.Now().Add(s.TTD))
			for {
				select {
				case <-s.cancelDestruction:
					s.mutex.Lock()
					s.onDeathrow = false
					s.mutex.Unlock()
					s.logger.Debug("server cancelled deathrow")
					return
				case <-timeToDie:
					// destroy the server
					s.mutex.Lock()
					s.onDeathrow = false
					s.mutex.Unlock()
					err := s.Destroy()
					s.logger.Debug("server died on deathrow", "err", err)
					return
				}
			}
		}()
	}
}

// HasSpaceFor considers the current usage (according to prior Allocation calls)
// and tells you how many of a cmd needing the given resources can run on this
// server.
func (s *Server) HasSpaceFor(cores, ramMB, diskGB int) int {
	s.mutex.RLock()
	defer s.mutex.RUnlock()
	if (s.Flavor.Cores-s.usedCores < cores) || (s.Flavor.RAM-s.usedRAM < ramMB) || (s.Disk-s.usedDisk < diskGB) {
		return 0
	}
	canDo := (s.Flavor.Cores - s.usedCores) / cores
	if canDo > 1 {
		var n int
		if ramMB > 0 {
			n = (s.Flavor.RAM - s.usedRAM) / ramMB
			if n < canDo {
				canDo = n
			}
		}
		if diskGB > 0 {
			n = (s.Disk - s.usedDisk) / diskGB
			if n < canDo {
				canDo = n
			}
		}
	}
	return canDo
}

// SSHClient returns an ssh.Client object that could be used to ssh to the
// server. Requires that port 22 is accessible for SSH.
func (s *Server) SSHClient() (*ssh.Client, error) {
	s.mutex.Lock()
	defer s.mutex.Unlock()
	if s.sshclient == nil {
		if s.provider.PrivateKey() == "" {
			s.logger.Error("resource file did not contain the ssh key", "path", s.provider.savePath)
			return nil, errors.New("missing ssh key")
		}

		// parse private key and make config
		signer, err := ssh.ParsePrivateKey([]byte(s.provider.PrivateKey()))
		if err != nil {
			s.logger.Error("failed to parse private key", "path", s.provider.savePath, "err", err)
			return nil, err
		}
		sshConfig := &ssh.ClientConfig{
			User: s.UserName,
			Auth: []ssh.AuthMethod{
				ssh.PublicKeys(signer),
			},
			HostKeyCallback: ssh.InsecureIgnoreHostKey(), // *** don't currently know the server's host key, want to use ssh.FixedHostKey(publicKey) instead...
			Timeout:         5 * time.Second,
		}

		// dial in to the server, allowing certain errors that indicate that the
		// network or server isn't really ready for ssh yet; wait for up to
		// 5mins for success, if we had only just created this server
		hostAndPort := s.IP + ":22"
		s.sshclient, err = sshDial(hostAndPort, sshConfig)
		if err != nil {
			limit := time.After(sshTimeOut)
			ticker := time.NewTicker(1 * time.Second)
			ticks := 0
		DIAL:
			for {
				select {
				case <-ticker.C:
					s.sshclient, err = sshDial(hostAndPort, sshConfig)
					if err != nil && (strings.HasSuffix(err.Error(), "connection timed out") || strings.HasSuffix(err.Error(), "no route to host") || strings.HasSuffix(err.Error(), "connection refused") || (s.created && strings.HasSuffix(err.Error(), "connection could not be established"))) {
						continue DIAL
					}

					// if it worked, we stop trying; if it failed again with a
					// different error, we keep trying for at least 45 seconds
					// to allow for the vagueries of OS start ups (eg. CentOS
					// brings up sshd and starts rejecting connections before
					// the centos user gets added)
					ticks++
					if err == nil || ticks == 9 || !s.created {
						ticker.Stop()
						break DIAL
					} else {
						continue DIAL
					}
				case <-limit:
					ticker.Stop()
					err = errors.New("giving up waiting for ssh to work")
					break DIAL
				}
			}
			if err != nil {
				return nil, err
			}
		}
	}
	return s.sshclient, nil
}

// sshDial calls ssh.Dial() and enforces the config's timeout, which ssh.Dial()
// doesn't always seem to obey.
func sshDial(addr string, sshConfig *ssh.ClientConfig) (*ssh.Client, error) {
	clientCh := make(chan *ssh.Client, 1)
	errCh := make(chan error, 1)
	go func() {
		sshClient, err := ssh.Dial("tcp", addr, sshConfig)
		clientCh <- sshClient
		errCh <- err
	}()
	deadline := time.After(sshConfig.Timeout + 1*time.Second)
	select {
	case err := <-errCh:
		return <-clientCh, err
	case <-deadline:
		return nil, fmt.Errorf("connection could not be established")
	}
}

// SSHSession returns an ssh.Session object that could be used to do things via
// ssh on the server. Will time out and return an error if the session can't be
// created within 5s.
func (s *Server) SSHSession() (*ssh.Session, error) {
	sshClient, err := s.SSHClient()
	if err != nil {
		s.logger.Debug("server ssh could not be established", "err", err)
		return nil, fmt.Errorf("cloud SSHSession() failed: %s", err.Error())
	}

	// *** even though sshclient has a timeout, it still hangs forever if we
	// try to get a NewSession to a dead server, so we implement our own 5s
	// timeout here

	done := make(chan error, 1)
	worked := make(chan bool, 1)
	sessionCh := make(chan *ssh.Session)
	go func() {
		select {
		case <-time.After(5 * time.Second):
			s.logger.Debug("server ssh timed out")
			done <- fmt.Errorf("cloud SSHSession() timed out")
		case <-worked:
			return
		}
	}()
	go func() {
		defer internal.LogPanic(s.logger, "server sshsession", false)
		session, errf := sshClient.NewSession()
		if errf != nil {
			s.logger.Debug("server ssh failed", "err", errf)
			done <- fmt.Errorf("cloud SSHSession() failed: %s", errf.Error())
			return
		}
		worked <- true
		done <- nil
		sessionCh <- session
	}()

	err = <-done
	if err != nil {
		return nil, err
	}
	return <-sessionCh, nil
}

// RunCmd runs the given command on the server, optionally in the background.
// You get the command's STDOUT and STDERR as strings.
func (s *Server) RunCmd(cmd string, background bool) (stdout, stderr string, err error) {
	// create a session
	session, err := s.SSHSession()
	if err != nil {
		return stdout, stderr, err
	}
	defer internal.LogClose(s.logger, session, "runcmd ssh session", "cmd", cmd)

	// if the sever is destroyed while running, arrange to immediately return an
	// error
	s.mutex.Lock()
	cancelID := s.cancelID
	s.cancelID = cancelID + 1
	cancelCh := make(chan bool, 1)
	s.cancelRunCmd[cancelID] = cancelCh
	done := make(chan error, 1)
	outCh := make(chan string, 1)
	errCh := make(chan string, 1)
	finished := make(chan bool, 1)
	go func() {
		select {
		case <-cancelCh:
			outCh <- ""
			errCh <- ""
			done <- fmt.Errorf("cloud RunCmd() cancelled due to destruction of server %s", s.ID)
		case <-finished:
			// end select
		}
		s.mutex.Lock()
		close(cancelCh)
		delete(s.cancelRunCmd, cancelID)
		s.mutex.Unlock()
	}()
	go func() {
		defer internal.LogPanic(s.logger, "server runcmd", false)

		// run the command, returning stdout
		if background {
			cmd = "sh -c 'nohup " + cmd + " > /dev/null 2>&1 &'"
		}
		var o bytes.Buffer
		var e bytes.Buffer
		session.Stdout = &o
		session.Stderr = &e
		errf := session.Run(cmd)
		finished <- true
		if o.Len() > 0 {
			outCh <- o.String()
		} else {
			outCh <- ""
		}
		if e.Len() > 0 {
			errCh <- e.String()
		} else {
			errCh <- ""
		}
		if errf != nil {
			done <- fmt.Errorf("cloud RunCmd(%s) failed: %s", cmd, errf.Error())
		} else {
			done <- nil
		}
	}()
	s.mutex.Unlock()

	err = <-done
	stdout = <-outCh
	stderr = <-errCh
	return stdout, stderr, err
}

// UploadFile uploads a local file to the given location on the server.
func (s *Server) UploadFile(source string, dest string) error {
	sshClient, err := s.SSHClient()
	if err != nil {
		return err
	}

	client, err := sftp.NewClient(sshClient)
	if err != nil {
		return err
	}
	defer internal.LogClose(s.logger, client, "upload file client session", "source", source, "dest", dest)

	// create all parent dirs of dest
	err = s.MkDir(dest)
	if err != nil {
		return err
	}

	// open source, create dest
	sourceFile, err := os.Open(source)
	if err != nil {
		return err
	}
	defer internal.LogClose(s.logger, sourceFile, "upload file source", "source", source, "dest", dest)

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
func (s *Server) CopyOver(files string) error {
	timezone, err := s.GetTimeZone()
	if err != nil {
		return err
	}

	for _, path := range strings.Split(files, ",") {
		split := strings.Split(path, ":")
		var localPath, remotePath string
		if len(split) == 2 {
			localPath = split[0]
			remotePath = split[1]
		} else {
			localPath = path
			remotePath = path
		}

		// ignore if it doesn't exist locally
		localPath = internal.TildaToHome(localPath)
		var info os.FileInfo
		info, err = os.Stat(localPath)
		if err != nil {
			err = nil
			continue
		}

		if strings.HasPrefix(remotePath, "~/") {
			homeDir, errh := s.HomeDir()
			if errh != nil {
				return errh
			}
			remotePath = strings.TrimLeft(remotePath, "~/")
			remotePath = filepath.Join(homeDir, remotePath)
		}

		err = s.UploadFile(localPath, remotePath)
		if err != nil {
			return err
		}

		// if these are config files we likely need to make them user-only read,
		// and if they're not, I can't see how it matters if group/all can't
		// read? This is a single user server and I'm the only one using it...
		_, _, err = s.RunCmd("chmod 600 "+remotePath, false)
		if err != nil {
			return err
		}

		// sometimes the mtime of the file matters, so we try and set that on
		// the remote copy
		timestamp := info.ModTime().UTC().In(timezone).Format(touchStampFormat)
		_, _, err = s.RunCmd(fmt.Sprintf("touch -t %s %s", timestamp, remotePath), false)
		if err != nil {
			return err
		}
	}
	return err
}

// HomeDir gets the absolute path to the server's home directory. Depends on
// 'pwd' command existing on the server.
func (s *Server) HomeDir() (string, error) {
	s.hmutex.Lock()
	defer s.hmutex.Unlock()
	if s.homeDir != "" {
		return s.homeDir, nil
	}

	stdout, _, err := s.RunCmd("pwd", false)
	if err != nil {
		return "", err
	}
	s.homeDir = strings.TrimSuffix(stdout, "\n")
	return s.homeDir, nil
}

// GetTimeZone gets the server's time zone as a fixed time.Location in the fake
// timezone 'SER'; you should only rely on the offset to convert times.
func (s *Server) GetTimeZone() (*time.Location, error) {
	if s.location != nil {
		return s.location, nil
	}

	serverDate, _, err := s.RunCmd(`date +%z`, false)
	if err != nil {
		return nil, err
	}
	serverDate = strings.TrimSpace(serverDate)

	t, err := time.Parse("-0700", serverDate)
	if err != nil {
		return nil, err
	}
	_, offset := t.Zone()

	location := time.FixedZone("SER", offset)
	s.location = location
	return location, err
}

// CreateFile creates a new file with the given content on the server.
func (s *Server) CreateFile(content string, dest string) error {
	sshClient, err := s.SSHClient()
	if err != nil {
		return err
	}

	client, err := sftp.NewClient(sshClient)
	if err != nil {
		return err
	}
	defer internal.LogClose(s.logger, client, "create file client session")

	// create all parent dirs of dest
	err = s.MkDir(dest)
	if err != nil {
		return err
	}

	// create dest
	destFile, err := client.Create(dest)
	if err != nil {
		return err
	}

	// write the content
	_, err = io.WriteString(destFile, content)
	return err
}

// DownloadFile downloads a file from the server and stores it locally. The
// directory for your local file must already exist.
func (s *Server) DownloadFile(source string, dest string) error {
	sshClient, err := s.SSHClient()
	if err != nil {
		return err
	}

	client, err := sftp.NewClient(sshClient)
	if err != nil {
		return err
	}
	defer internal.LogClose(s.logger, client, "download file client session", "source", source, "dest", dest)

	// open source, create dest
	sourceFile, err := client.Open(source)
	if err != nil {
		return err
	}
	defer internal.LogClose(s.logger, sourceFile, "download file source", "source", source, "dest", dest)

	destFile, err := os.Create(dest)
	if err != nil {
		return err
	}

	// copy the file content over
	_, err = io.Copy(destFile, sourceFile)
	if err != nil {
		return err
	}

	return os.Chmod(dest, 0600)
}

// MkDir creates a directory (and it's parents as necessary) on the server.
func (s *Server) MkDir(dest string) error {
	//*** it would be nice to do this with client.Mkdir, but that doesn't do
	// the equivalent of mkdir -p, and errors out if dirs already exist... for
	// now it's easier to just call mkdir
	dir := filepath.Dir(dest)
	if dir != "." {
		_, _, err := s.RunCmd("mkdir -p "+dir, false)
		if err != nil {
			return err
		}
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
	s.goneBad = true

	if len(permanentProblem) == 1 {
		s.permanentProblem = permanentProblem[0]
	}
}

// NotBad lets you change your mind about a server you called GoneBad() on.
// (Unless GoneBad() was called with a permanentProblem.)
func (s *Server) NotBad() {
	s.mutex.Lock()
	defer s.mutex.Unlock()
	if s.permanentProblem == "" {
		s.goneBad = false
	}
}

// IsBad tells you if GoneBad() has been called (more recently than NotBad()).
func (s *Server) IsBad() bool {
	s.mutex.RLock()
	defer s.mutex.RUnlock()
	return s.goneBad
}

// PermanentProblem tells you if GoneBad("problem message") has been called,
// returning that reason the server is not usable.
func (s *Server) PermanentProblem() string {
	s.mutex.RLock()
	defer s.mutex.RUnlock()
	return s.permanentProblem
}

// Destroy immediately destroys the server.
func (s *Server) Destroy() error {
	s.mutex.Lock()
	defer s.mutex.Unlock()

	if s.destroyed {
		return nil
	}

	// if the server has initiated its countdown to destruction, cancel that
	if s.onDeathrow {
		s.cancelDestruction <- true
	}

	// if the user is in the middle of RunCmd(), have those return an error now
	for _, ch := range s.cancelRunCmd {
		ch <- true
	}

	s.destroyed = true
	s.goneBad = true

	// for testing purposes, we anticipate that provider isn't set
	if s.provider == nil {
		return fmt.Errorf("provider not set")
	}

	err := s.provider.DestroyServer(s.ID)
	s.logger.Debug("server destroyed", "err", err)
	if err != nil {
		// check if the server exists
		ok, _ := s.provider.CheckServer(s.ID)
		if ok {
			return err
		}
		// if not, assume there's no Server and ignore this error (which may
		// just be along the lines of "the server doesn't exist")
		return nil
	}

	return err
}

// Destroyed tells you if a server was destroyed using Destroy() or the
// automatic destruction due to being idle. It is NOT the opposite of Alive(),
// since it does not check if the server is still usable.
func (s *Server) Destroyed() bool {
	s.mutex.Lock()
	defer s.mutex.Unlock()
	return s.destroyed
}

// Alive tells you if a server is usable. It first does the same check as
// Destroyed() before calling out to the provider. Supplying an optional boolean
// will double check the server to make sure it can be ssh'd to.
func (s *Server) Alive(checkSSH ...bool) bool {
	s.mutex.Lock()
	if s.destroyed {
		s.mutex.Unlock()
		return false
	}
	ok, _ := s.provider.CheckServer(s.ID)
	if !ok {
		s.mutex.Unlock()
		return false
	}
	s.mutex.Unlock()

	if len(checkSSH) == 1 && checkSSH[0] {
		// provider may claim the server is fine, but it might not really be
		// usable; confirm we can still ssh to it
		session, err := s.SSHSession()
		if err != nil {
			return false
		}
		errc := session.Close()
		if errc != nil {
			s.logger.Warn("alive check ssh session did not close", "err", errc)
		}
	}

	return true
}
