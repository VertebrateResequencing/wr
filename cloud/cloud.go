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
must add a case for it to New() and rebuild.
*/
package cloud

import (
	"encoding/gob"
	"github.com/satori/go.uuid"
	"os"
	"strings"
)

// Err* constants are found in the returned Errors under err.Err, so you can
// cast and check if it's a certain type of error. ErrMissingEnv gets appended
// to with missing environment variable names, so check based on prefix.
var (
	ErrBadProvider     = "unknown provider name"
	ErrMissingEnv      = "missing environment variables: "
	ErrBadResourceName = "your resource name prefix contains disallowed characters"
	ErrNoFlavor        = "no server flavor can meet your resource requirements"
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
	ResourceName string            // the resource name prefix that resources were created with
	Details      map[string]string // whatever key values the provider needs to describe what it created
	PrivateKey   string            // PEM format string of the key user would need to ssh in to any created servers
	Servers      map[string]string // the serverID => ip mapping of any servers Spawn()ed with an external ip
}

// Quota struct describes the limit on what resources you are allowed to use (0
// values mean that resource is unlimited), and how much you have already used.
type Quota struct {
	MaxRam        int // total MBs allowed
	MaxCores      int // total CPU cores allowed
	MaxInstances  int // max number of instances allowed
	UsedRam       int
	UsedCores     int
	UsedInstances int
}

// this interface must be satisfied to add support for a particular cloud
// provider.
type provideri interface {
	requiredEnv() []string                                                                                                                 // return the environment variables required to function
	initialize() error                                                                                                                     // do any initial config set up such as authentication
	deploy(resources *Resources, requiredPorts []int) error                                                                                // achieve the aims of Deploy(), recording what you create in resources.Details and resources.PrivateKey
	getQuota() (*Quota, error)                                                                                                             // achieve the aims of GetQuota()
	cheapestServerFlavor(minRAM int, minDisk int, minCPUs int) (flavorID string, ramMB int, diskGB int, CPUs int, err error)               // achieve the aims of CheapestServerFlavor()
	spawn(resources *Resources, os string, flavor string, externalIP bool) (serverID string, serverIP string, adminPass string, err error) // achieve the aims of Spawn()
	checkServer(serverID string) (working bool, err error)                                                                                 // achieve the aims of CheckServer()
	destroyServer(serverID string) error                                                                                                   // achieve the aims of DestroyServer()
	tearDown(resources *Resources) error                                                                                                   // achieve the aims of TearDown()
}

// Provider gives you access to all of the methods you'll need to interact with
// a cloud provider.
type Provider struct {
	impl      provideri
	Name      string
	savePath  string
	resources *Resources
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
// (core count) requirements. Use the first return value for passing to Spawn().
// If no flavor meets your requirements you will get an error matching
// ErrNoFlavor.
func (p *Provider) CheapestServerFlavor(minRAM int, minDisk int, minCPUs int) (flavorID string, ramMB int, diskGB int, CPUs int, err error) {
	return p.impl.cheapestServerFlavor(minRAM, minDisk, minCPUs)
}

// Spawn creates a new server using an OS image with a name prefixed with the
// given os name, with the given flavor ID (that you could get from
// CheapestServerFlavor()). If you need an external IP so that you can ssh to
// the server externally, supply true as the last argument. Returns the serverID
// so that you can later call DestroyServer(). Returns the ip so you can ssh to
// it, and the admin password in case you need to sudo on the server. You will
// need to know the username that you can log in with on your chosen OS image.
func (p *Provider) Spawn(os string, flavorID string, externalIP bool) (serverID string, serverIP string, adminPass string, err error) {
	serverID, serverIP, adminPass, err = p.impl.spawn(p.resources, os, flavorID, externalIP)

	if err == nil && externalIP {
		// update resources and save to disk
		p.resources.Servers[serverID] = serverIP
		err = p.saveResources()
	}

	return
}

// CheckServer asks the provider if the status of the given server (id retrieved
// from Spawn() or Servers()) indicates it is working fine. (If it's not and
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
// Spawn().
func (p *Provider) DestroyServer(serverID string) (err error) {
	err = p.impl.destroyServer(serverID)
	if err == nil {
		// update resources and save to disk
		delete(p.resources.Servers, serverID)
		err = p.saveResources()
	}
	return
}

// Servers returns a mapping of serverID => serverIP for all servers that were
// Spawn()ed with an externalIP (including those spawned in past sessions where
// the same arguments to New() were used). You should use CheckServer() before
// trying to use one of these servers. Do not alter the return value!
func (p *Provider) Servers() map[string]string {
	return p.resources.Servers
}

// PrivateKey returns a PEM format string of the private key that was created
// by Deploy() (on its first invocation with the same arguments to New()).
func (p *Provider) PrivateKey() string {
	return p.resources.PrivateKey
}

// TearDown deletes all resources recorded during Deploy() or loaded from a
// previous session during New(). It also deletes any servers with names
// prefixed with the resourceName given to the initial New() call.
func (p *Provider) TearDown() (err error) {
	err = p.impl.tearDown(p.resources)
	if err != nil {
		return
	}

	// delete our savePath
	err = p.deleteResourceFile()
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
	resources = &Resources{ResourceName: resourceName, Details: make(map[string]string), Servers: make(map[string]string)}
	if _, serr := os.Stat(p.savePath); os.IsNotExist(serr) {
		return
	}

	file, err := os.Open(p.savePath)
	if err == nil {
		defer file.Close()
		decoder := gob.NewDecoder(file)
		err = decoder.Decode(resources)
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
