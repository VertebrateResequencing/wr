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
remove those resources when you're done.

It's a pseudo plug-in system in that it is designed so that you can easily add a
go file that implements the methods of the provideri interface, to support a
new cloud provider. On the other hand, there is no dynamic loading of these go
files; they are all imported (they all belong to the cloud package), and the
correct one used at run time. To "register" a new provideri implementation you
must add a case for it to New() and rebuild.
*/
package cloud

import (
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
)

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
	NamePrefix string            // the resource name prefix that resources were created with
	Details    map[string]string // whatever key values the provider needs to describe what it created
	PrivateKey string            // PEM format string of the key user would need to ssh in to any created servers
}

// this interface must be satisfied to add support for a particular cloud
// provider.
type provideri interface {
	requiredEnv() []string                                                                                                                                        // return the environment variables required to function
	initialize() error                                                                                                                                            // do any initial config set up such as authentication
	deploy(resources *Resources, requiredPorts []int) error                                                                                                       // achieve the aims of Deploy(), recording what you create in resources.Details and resources.PrivateKey
	spawn(resources *Resources, os string, minRAM int, minDisk int, minCPUs int, externalIP bool) (serverID string, serverIP string, adminPass string, err error) // achieve the aims of Spawn()
	destroyServer(serverID string) error                                                                                                                          // achieve the aims of DestroyServer()
	tearDown(resources *Resources) error                                                                                                                          // achieve the aims of TearDown()
}

// Provider gives you access to all of the methods you'll need to interact with
// a cloud provider.
type Provider struct {
	impl      provideri
	Name      string
	resources *Resources
}

// New creates a new Provider to interact with the given cloud provider.
// Possible names so far are "openstack" ("aws" is planned). You must provide a
// resource name prefix that will be used to name any created cloud resources.
func New(name string, resourceNamePrefix string) (p *Provider, err error) {
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
		p.resources = &Resources{NamePrefix: resourceNamePrefix, Details: make(map[string]string)}

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
// exists with the resourceNamePrefix you supplied to New(), we assume it
// belongs to us and we don't create another (but report it as created in the
// return value). You must provide a slice of port numbers that your application
// needs to be able to communicate to any servers you spawn (eg. [22] for ssh)
// through. Returns Resources that record what was created, that you should
// store for later supplying to TearDown(). NB: Every time you run this with the
// same resourceNamePrefix until you run TearDown(), the returned resources will
// have the same content (assuming you didn't delete resources manually), except
// that resources.PrivateKey will only be set the first time you deploy - be
// sure to save that separately!
func (p *Provider) Deploy(requiredPorts []int) (resources *Resources, err error) {
	resources = p.resources
	err = p.impl.deploy(resources, requiredPorts)
	// (we return resources even though it's an attribute of p so that user can
	// serialise it to disk for later input to TearDown)
	return
}

// Spawn creates a new server using an OS image with a name prefixed with the
// given os name, and the smallest (cheapest) server "flavour" available that
// satisfies your minimum ram (MB), disk (GB) and CPU requirements. If you need
// an external IP so that you can ssh to the server externally, supply true as
// the last argument. Returns the serverID so that you can later call
// DestroyServer(). Returns the ip so you can ssh to it, and the admin password
// in case you need to sudo on the server. You will need to know the username
// that you can log in with on your chosen OS image.
func (p *Provider) Spawn(os string, minRAM int, minDisk int, minCPUs int, externalIP bool) (serverID string, serverIP string, adminPass string, err error) {
	return p.impl.spawn(p.resources, os, minRAM, minDisk, minCPUs, externalIP)
}

// DestroyServer destroys a server given its id, that you would have gotten from
// Spawn().
func (p *Provider) DestroyServer(serverID string) error {
	return p.impl.destroyServer(serverID)
}

// TearDown deletes all resources recorded in the supplied Resources variable
// that you stored after calling Deploy(). It also deletes any servers with
// names that have the resourceNamePrefix given to the initial New() call.
func (p *Provider) TearDown(resources *Resources) error {
	// (we don't use p.resources to allow for using this in a different session
	// to when Deploy() was called)
	return p.impl.tearDown(resources)
}

// uniqueResourceName takes the given prefix and appends a unique string to it
// (a uuid).
func uniqueResourceName(prefix string) (unique string) {
	return prefix + "-" + uuid.NewV4().String()
}
