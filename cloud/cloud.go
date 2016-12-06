// Copyright © 2016 Genome Research Limited
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

/*
Package cloud provides functions to interact with cloud providers such as
OpenStack and AWS (not yet implemented).

You use it to create cloud resources so that you can spawn servers, then
delete those resources when you're done.

Please note that the methods in this package are NOT safe to be used by more
than 1 process at a time.

It's a pseudo plug-in system in that it is designed so that you can easily add a
go file that implements the methods of the provideri interface, to support a
new cloud provider. On the other hand, there is no dynamic loading of these go
files; they are all imported (they all belong to the cloud package), and the
correct one used at run time. To "register" a new provideri implementation you
must add a case for it to New() and RequiredEnv() and rebuild.
*/
package cloud

import (
	"bytes"
	"encoding/gob"
	"errors"
	"github.com/pkg/sftp"
	"github.com/satori/go.uuid"
	"golang.org/x/crypto/ssh"
	"io"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

// Err* constants are found in the returned Errors under err.Err, so you can
// cast and check if it's a certain type of error. ErrMissingEnv gets appended
// to with missing environment variable names, so check based on prefix.
var (
	ErrBadProvider     = "unknown provider name"
	ErrMissingEnv      = "missing environment variables: "
	ErrBadResourceName = "your resource name prefix contains disallowed characters"
	ErrNoFlavor        = "no server flavor can meet your resource requirements"
	ErrBadFlavor       = "no server flavor with that id exists"
	ErrBadRegex        = "your flavor regular expression was not valid"
)

// dnsNameServers holds some public (google) dns name server addresses for use
// when creating cloud subnets that need internet access.
var dnsNameServers = [...]string{"8.8.4.4", "8.8.8.8"}

// Error records an error and the operation and provider caused it.
type Error struct {
	Provider string // the provider's Name
	Op       string // name of the method
	Err      string // one of our Err* vars
}

func (e Error) Error() string {
	return "cloud(" + e.Provider + ") " + e.Op + "(): " + e.Err
}

// Resources struct contains provider-specific details of every resource that
// we created, in a format understood by TearDown(), so that we can delete
// those resources when they're no longer needed. There are also fields for
// important things the user needs to know.
type Resources struct {
	ResourceName string             // the resource name prefix that resources were created with
	Details      map[string]string  // whatever key values the provider needs to describe what it created
	PrivateKey   string             // PEM format string of the key user would need to ssh in to any created servers
	Servers      map[string]*Server // the serverID => *Server mapping of any servers Spawn()ed with an external ip
}

// Quota struct describes the limit on what resources you are allowed to use (0
// values mean that resource is unlimited), and how much you have already used.
type Quota struct {
	MaxRAM        int // total MBs allowed
	MaxCores      int // total CPU cores allowed
	MaxInstances  int // max number of instances allowed
	UsedRAM       int
	UsedCores     int
	UsedInstances int
}

// this interface must be satisfied to add support for a particular cloud
// provider.
type provideri interface {
	requiredEnv() []string                                                                                                                                            // return the environment variables required to function
	initialize() error                                                                                                                                                // do any initial config set up such as authentication
	deploy(resources *Resources, requiredPorts []int) error                                                                                                           // achieve the aims of Deploy(), recording what you create in resources.Details and resources.PrivateKey
	getQuota() (*Quota, error)                                                                                                                                        // achieve the aims of GetQuota()
	flavors() map[string]Flavor                                                                                                                                       // return a map of all server flavors, with their flavor ids as keys
	spawn(resources *Resources, os string, flavor string, externalIP bool, postCreationScript []byte) (serverID string, serverIP string, adminPass string, err error) // achieve the aims of Spawn()
	checkServer(serverID string) (working bool, err error)                                                                                                            // achieve the aims of CheckServer()
	destroyServer(serverID string) error                                                                                                                              // achieve the aims of DestroyServer()
	tearDown(resources *Resources) error                                                                                                                              // achieve the aims of TearDown()
}

// Provider gives you access to all of the methods you'll need to interact with
// a cloud provider.
type Provider struct {
	impl      provideri
	Name      string
	savePath  string
	resources *Resources
}

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
	ID                string
	IP                string // ip address that you could SSH to
	UserName          string // the username needed to log in to the server
	AdminPass         string
	Flavor            Flavor
	TTD               time.Duration // amount of idle time allowed before destruction
	usedRAM           int
	usedCores         int
	usedDisk          int
	onDeathrow        bool
	mutex             sync.RWMutex
	cancelDestruction chan bool
	destroyed         bool
	provider          *Provider
	sshclient         *ssh.Client
}

// Allocate records that the given resources have now been used up on this
// server.
func (s *Server) Allocate(cores, ramMB, diskGB int) {
	s.mutex.Lock()
	defer s.mutex.Unlock()
	s.usedCores += cores
	s.usedRAM += ramMB
	s.usedDisk += diskGB

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

	// if the server is now doing nothing, we'll initiate a countdown to
	// destroying the host
	if s.usedCores <= 0 && s.TTD.Seconds() > 0 {
		go func() {
			s.mutex.Lock()
			if s.onDeathrow {
				s.mutex.Unlock()
				return
			}
			s.cancelDestruction = make(chan bool, 4) // *** the 4 is a hack to prevent deadlock, should find proper fix...
			s.onDeathrow = true
			s.mutex.Unlock()

			timeToDie := time.After(s.TTD)
			for {
				select {
				case <-s.cancelDestruction:
					s.onDeathrow = false
					return
				case <-timeToDie:
					// destroy the server
					s.onDeathrow = false
					s.Destroy()
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
	if (s.Flavor.Cores-s.usedCores < cores) || (s.Flavor.RAM-s.usedRAM < ramMB) || (s.Flavor.Disk-s.usedDisk < diskGB) {
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
			n = (s.Flavor.Disk - s.usedDisk) / diskGB
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
			log.Printf("resource file %s did not contain the ssh key\n", s.provider.savePath)
			return nil, errors.New("missing ssh key")
		}

		// parse private key and make config
		key, err := ssh.ParsePrivateKey([]byte(s.provider.PrivateKey()))
		if err != nil {
			log.Printf("failure to parse the private key: %s\n", err)
			return nil, err
		}
		sshConfig := &ssh.ClientConfig{
			User: s.UserName,
			Auth: []ssh.AuthMethod{
				ssh.PublicKeys(key),
			},
		}

		// dial in to the server, allowing certain errors that indicate that the
		// network or server isn't really ready for ssh yet; wait for up to
		// 5mins for success
		hostAndPort := s.IP + ":22"
		s.sshclient, err = ssh.Dial("tcp", hostAndPort, sshConfig)
		if err != nil {
			limit := time.After(5 * time.Minute)
			ticker := time.NewTicker(1 * time.Second)
		DIAL:
			for {
				select {
				case <-ticker.C:
					s.sshclient, err = ssh.Dial("tcp", hostAndPort, sshConfig)
					if err != nil && (strings.HasSuffix(err.Error(), "connection timed out") || strings.HasSuffix(err.Error(), "no route to host") || strings.HasSuffix(err.Error(), "connection refused")) {
						continue DIAL
					}
					// worked, or failed with a different error: stop trying
					ticker.Stop()
					break DIAL
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

// RunCmd runs the given command on the server, optionally in the background.
// You get the command's STDOUT as a string response.
func (s *Server) RunCmd(cmd string, background bool) (response string, err error) {
	sshClient, err := s.SSHClient()
	if err != nil {
		return
	}

	// create a session
	session, err := sshClient.NewSession()
	if err != nil {
		return
	}
	defer session.Close()

	// run the command, returning stdout
	if background {
		cmd = "sh -c 'nohup " + cmd + " > /dev/null 2>&1 &'"
	}
	var b bytes.Buffer
	session.Stdout = &b
	if err = session.Run(cmd); err != nil {
		return
	}
	response = b.String()
	return
}

// UploadFile uploads a local file to the given location on the server.
func (s *Server) UploadFile(source string, dest string) (err error) {
	sshClient, err := s.SSHClient()
	if err != nil {
		return
	}

	client, err := sftp.NewClient(sshClient)
	if err != nil {
		return
	}
	defer client.Close()

	// create all parent dirs of dest
	err = s.MkDir(dest)
	if err != nil {
		return
	}

	// open source, create dest
	sourceFile, err := os.Open(source)
	if err != nil {
		return
	}
	defer sourceFile.Close()

	destFile, err := client.Create(dest)
	if err != nil {
		return
	}

	// copy the file content over
	_, err = io.Copy(destFile, sourceFile)
	return
}

// CreateFile creates a new file with the given content on the server.
func (s *Server) CreateFile(content string, dest string) (err error) {
	sshClient, err := s.SSHClient()
	if err != nil {
		return
	}

	client, err := sftp.NewClient(sshClient)
	if err != nil {
		return
	}
	defer client.Close()

	// create all parent dirs of dest
	err = s.MkDir(dest)
	if err != nil {
		return
	}

	// create dest
	destFile, err := client.Create(dest)
	if err != nil {
		return
	}

	// write the content
	_, err = io.WriteString(destFile, content)
	return
}

// MkDir creates a directory (and it's parents as necessary) on the server.
func (s *Server) MkDir(dest string) (err error) {
	//*** it would be nice to do this with client.Mkdir, but that doesn't do
	// the equivalent of mkdir -p, and errors out if dirs already exist... for
	// now it's easier to just call mkdir
	dir := filepath.Dir(dest)
	if dir != "." {
		_, err = s.RunCmd("mkdir -p "+dir, false)
		if err != nil {
			return
		}
	}
	return
}

// Destroy immediately destroys the server.
func (s *Server) Destroy() error {
	s.mutex.Lock()
	defer s.mutex.Unlock()

	// if the server has initiated its countdown to destruction, cancel that
	if s.onDeathrow {
		s.cancelDestruction <- true
	}

	err := s.provider.DestroyServer(s.ID)
	if err != nil {
		ok, _ := s.provider.CheckServer(s.ID)
		if ok {
			return err
		}
	}

	s.destroyed = true
	return nil
}

// Destroyed tells you if a server was destroyed using Destroy() or the
// automatic destruction due to being idle. It is NOT the opposite of Alive(),
// since it does not check if the server is still usable.
func (s *Server) Destroyed() bool {
	s.mutex.Lock()
	defer s.mutex.Unlock()
	return s.destroyed
}

// Alive tells you if a server is usable.
func (s *Server) Alive() bool {
	s.mutex.Lock()
	defer s.mutex.Unlock()
	if s.destroyed {
		return false
	}
	ok, _ := s.provider.CheckServer(s.ID)
	return ok
}

// RequiredEnv returns the environment variables that are needed by the given
// provider before New() will work for it. See New() for possible providerNames.
func RequiredEnv(providerName string) (vars []string, err error) {
	var p *Provider
	switch providerName {
	case "openstack":
		p = &Provider{impl: new(openstackp)}
	case "aws":
		//p = &Provider{impl: new(aws)}
	}

	if p == nil {
		err = Error{providerName, "RequiredEnv", ErrBadProvider}
	} else {
		vars = p.impl.requiredEnv()
	}
	return
}

// New creates a new Provider to interact with the given cloud provider.
// Possible names so far are "openstack" ("aws" is planned). You must provide a
// resource name that will be used to name any created cloud resources. You must
// also provide a file path prefix to save details of created resources to (the
// actual file created will be suffixed with your resourceName). Note that the
// file could contain created private key details, so should be kept accessible
// only by you.
func New(name string, resourceName string, savePath string) (p *Provider, err error) {
	switch name {
	case "openstack":
		p = &Provider{impl: new(openstackp)}
	case "aws":
		//p = &Provider{impl: new(aws)}
	}

	if p == nil {
		err = Error{name, "New", ErrBadProvider}
	} else {
		p.Name = name
		p.savePath = savePath + "." + resourceName

		// load any resources we previously saved, or get an empty set to work
		// with
		p.resources, err = p.loadResources(resourceName)
		if err != nil {
			return
		}

		var missingEnv []string
		for _, envKey := range p.impl.requiredEnv() {
			if os.Getenv(envKey) == "" {
				missingEnv = append(missingEnv, envKey)
			}
		}
		if len(missingEnv) > 0 {
			err = Error{name, "New", ErrMissingEnv + strings.Join(missingEnv, ", ")}
		} else {
			err = p.impl.initialize()
		}
	}

	return
}

// Deploy triggers the creation of required cloud resources such as networks,
// ssh keys, security profiles and so on, such that you can subsequently Spawn()
// and ssh to your created server successfully.  If a resource we need already
// exists with the resourceName you supplied to New(), we assume it belongs to
// us and we don't create another (so it is safe to call Deploy multiple times
// with the same args to New() and Deploy(): you don't need to check if you have
// already deployed). You must provide a slice of port numbers that your
// application needs to be able to communicate to any servers you spawn (eg.
// [22] for ssh) through. Saves the resources it created to disk, which are what
// TearDown() will delete when you call it. (They are saved to disk so that
// TearDown() can work if you call it in a different session to when you
// Deploy()ed, and so that PrivateKey() can work if you call it in a different
// session to the Deploy() call that actually created the ssh key.)
func (p *Provider) Deploy(requiredPorts []int) (err error) {
	// impl.deploy should overwrite any existing values in p.resources with
	// updated values, but should leave other things - such as an existing
	// PrivateKey when we have not just made a new one - alone
	err = p.impl.deploy(p.resources, requiredPorts)
	if err != nil {
		return
	}

	// save updated resources to disk
	err = p.saveResources()

	return
}

// GetQuota returns details of the maximum resources the user can request, and
// the current resources used.
func (p *Provider) GetQuota() (quota *Quota, err error) {
	return p.impl.getQuota()
}

// CheapestServerFlavor returns details of the smallest (cheapest) server
// "flavor" available that satisfies your minimum ram (MB), disk (GB) and CPU
// (core count) requirements, and that also matches the given regex (empty
// string for the regex means not limited by regex). Use the ID property of the
// return value for passing to Spawn(). If no flavor meets your requirements you
// will get an error matching ErrNoFlavor.
func (p *Provider) CheapestServerFlavor(cores, ramMB, diskGB int, regex string) (fr Flavor, err error) {
	// from all available flavours, pick the one that has the lowest ram, disk
	// and cpus that meet our minimums, and also matches the regex
	var r *regexp.Regexp
	if regex != "" {
		r, err = regexp.Compile(regex)
		if err != nil {
			err = Error{"openstack", "cheapestServerFlavor", ErrBadRegex}
			return
		}
	}

	for _, f := range p.impl.flavors() {
		if regex != "" {
			if !r.MatchString(f.Name) {
				continue
			}
		}

		if f.Cores >= cores && f.RAM >= ramMB && f.Disk >= diskGB {
			if fr.ID == "" {
				fr = f
			} else if f.Cores < fr.Cores {
				fr = f
			} else if f.Cores == fr.Cores {
				if f.RAM < fr.RAM {
					fr = f
				} else if f.RAM == fr.RAM && f.Disk < fr.Disk {
					fr = f
				}
			}
		}
	}

	if err == nil && fr.ID == "" {
		err = Error{"openstack", "cheapestServerFlavor", ErrNoFlavor}
	}

	return
}

// Spawn creates a new server using an OS image with a name prefixed with the
// given os name, with the given flavor ID (that you could get from
// CheapestServerFlavor().ID). If you supply a non-zero value for the ttd
// argument, then this amount of time after the last s.Release() call you make
// (or after the last cmd you started with s.RunCmd exits) that causes the
// server to be considered idle, the server will be destroyed. If you need an
// external IP so that you can ssh to the server externally, supply true as the
// last argument. Returns a *Server so you can s.Destroy it later, find out its
// ip address so you can ssh to it, and get its admin password in case you need
// to sudo on the server. You will need to know the username that you can log in
// with on your chosen OS image. If you call Spawn() while running on a cloud
// server, then the newly spawned server will be in the same network and
// security group as the current server. postCreationScript is the []byte
// contents of a script that will be run on the server after it has been
// created, before it is used for anything else; empty slice means do nothing.
func (p *Provider) Spawn(os string, osUser string, flavorID string, ttd time.Duration, externalIP bool, postCreationScript []byte) (server *Server, err error) {
	f, found := p.impl.flavors()[flavorID]
	if !found {
		err = Error{"openstack", "Spawn", ErrBadFlavor}
		return
	}

	serverID, serverIP, adminPass, err := p.impl.spawn(p.resources, os, flavorID, externalIP, postCreationScript)

	server = &Server{
		ID:        serverID,
		IP:        serverIP,
		AdminPass: adminPass,
		UserName:  osUser,
		Flavor:    f,
		TTD:       ttd,
		provider:  p,
	}

	if err == nil && externalIP {
		// update resources and save to disk
		p.resources.Servers[serverID] = server
		err = p.saveResources()
	}

	return
}

// CheckServer asks the provider if the status of the given server (id retrieved
// via Spawn() or Servers()) indicates it is working fine. (If it's not and
// was previously thought to be a spawned server with an external IP, then it
// will be removed from the results of Servers().)
func (p *Provider) CheckServer(serverID string) (working bool, err error) {
	working, err = p.impl.checkServer(serverID)

	if err == nil && !working {
		// update resources and save to disk
		if _, present := p.resources.Servers[serverID]; present {
			delete(p.resources.Servers, serverID)
			err = p.saveResources()
		}
	}

	return
}

// DestroyServer destroys a server given its id, that you would have gotten from
// the ID property of Spawn()'s return value.
func (p *Provider) DestroyServer(serverID string) (err error) {
	err = p.impl.destroyServer(serverID)
	if err == nil {
		// update resources and save to disk
		delete(p.resources.Servers, serverID)
		err = p.saveResources()
	}
	return
}

// Servers returns a mapping of serverID => *Server for all servers that were
// Spawn()ed with an external IP (including those spawned in past sessions where
// the same arguments to New() were used). You should use s.Alive() before
// trying to use one of these servers. Do not alter the return value!
func (p *Provider) Servers() map[string]*Server {
	return p.resources.Servers
}

// PrivateKey returns a PEM format string of the private key that was created
// by Deploy() (on its first invocation with the same arguments to New()).
func (p *Provider) PrivateKey() string {
	return p.resources.PrivateKey
}

// TearDown deletes all resources recorded during Deploy() or loaded from a
// previous session during New(). It also deletes any servers with names
// prefixed with the resourceName given to the initial New() call. If currently
// running on a cloud server, however, it will not delete anything needed by
// this server, including the resource file that contains the private key.
func (p *Provider) TearDown() (err error) {
	err = p.impl.tearDown(p.resources)
	if err != nil {
		return
	}

	// delete our savePath unless our resources still contains the private key,
	// indicating it is still in the cloud and could be needed in the future
	if p.resources.PrivateKey == "" {
		err = p.deleteResourceFile()
	}
	return
}

// saveResources saves our resources to our savePath, overwriting any existing
// content. This is not thread safe!
func (p *Provider) saveResources() (err error) {
	file, err := os.OpenFile(p.savePath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0600)
	if err == nil {
		defer file.Close()
		encoder := gob.NewEncoder(file)
		encoder.Encode(p.resources)
	}
	return
}

// loadResources loads our resources from our savePath, or returns an empty
// set of resources if savePath doesn't exist.
func (p *Provider) loadResources(resourceName string) (resources *Resources, err error) {
	resources = &Resources{ResourceName: resourceName, Details: make(map[string]string), Servers: make(map[string]*Server)}
	if _, serr := os.Stat(p.savePath); os.IsNotExist(serr) {
		return
	}

	file, err := os.Open(p.savePath)
	if err == nil {
		defer file.Close()
		decoder := gob.NewDecoder(file)
		err = decoder.Decode(resources)

		if err == nil {
			// add in the ref to ourselves to each of our servers
			for _, server := range resources.Servers {
				server.provider = p
			}
		}
	}

	return
}

// deleteResourceFile deletes our savePath.
func (p *Provider) deleteResourceFile() (err error) {
	err = os.Remove(p.savePath)
	return
}

// uniqueResourceName takes the given prefix and appends a unique string to it
// (a uuid).
func uniqueResourceName(prefix string) (unique string) {
	return prefix + "-" + uuid.NewV4().String()
}
