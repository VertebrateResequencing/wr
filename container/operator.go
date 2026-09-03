/*******************************************************************************
 * Copyright (c) 2026 Genome Research Ltd.
 *
 * Author: Sendu Bala <sb10@sanger.ac.uk>
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

package container

import (
	"context"
	"os"
	"path/filepath"

	"github.com/VertebrateResequencing/wr/fs/file"
	fp "github.com/VertebrateResequencing/wr/fs/filepath"
)

// OperationErr is supplied to OperatorErr to define the reasons for the failed
// operations.
type OperationErr string

// ErrContainer* are the reasons for the failed operations.
const (
	ErrContainerList  OperationErr = "Could not list the containers"
	ErrContainerStats OperationErr = "Could not get the container stats"
	ErrContainerKill  OperationErr = "Container could not be killed"
)

// OperatorError records an error and a reason that caused it.
type OperatorError struct {
	Type OperationErr // one of our OperationErr constants
	Err  error
}

// Error returns an error with the reason for a failed operation.
func (et *OperatorError) Error() string {
	msg := string(et.Type)
	if et.Err != nil {
		msg += " [" + et.Err.Error() + "]"
	}

	return msg
}

// Unwrap method returns contained error of OperatorErr.
func (et *OperatorError) Unwrap() error {
	return et.Err
}

// Interactor defines some methods to query containers.
type Interactor interface {
	ContainerList(ctx context.Context) ([]*Container, error)
	ContainerStats(ctx context.Context, containerID string) (*Stats, error)
	ContainerKill(ctx context.Context, containerID string) error
}

// Operator offers some methods for working with containers. You must use the
// NewOperator() method to make one, or the methods won't work.
type Operator struct {
	client             Interactor
	existingContainers map[string]bool
}

// NewOperator creates a new Operator, working on containers using the supplied
// Interactor. eg. to create an operator that can work with Docker containers:
/*
	import (
		"github.com/moby/moby/client"

		"github.com/VertebrateResequencing/wr/container/docker"
	)
	 ....
	 {
		 cli, err := client.NewClientWithOpts(client.FromEnv)
		 dockerInterator := docker.NewInteractor(cli)
		 dockerOperator := NewOperator(dockerInterator)
		 cntrList, err := dockerOperator.GetCurrentContainers(ctx)
	 }
	 ...
*/
func NewOperator(cntrInteractor Interactor) *Operator {
	return &Operator{
		client:             cntrInteractor,
		existingContainers: make(map[string]bool),
	}
}

// GetCurrentContainers returns current containers.
func (o *Operator) GetCurrentContainers(ctx context.Context) ([]*Container, error) {
	contnrList, err := o.client.ContainerList(ctx)
	if err != nil {
		return nil, &OperatorError{Type: ErrContainerList, Err: err}
	}

	o.addClientToContainers(contnrList)

	return contnrList, nil
}

// addClientToContainers adds clients to the container structs for Stats() to
// work on containers.
func (o *Operator) addClientToContainers(cntrList []*Container) {
	for _, cntr := range cntrList {
		cntr.client = o.client
	}
}

// RememberCurrentContainers calls GetCurrentContainers() and stores the
// results, for the benefit of a future GetNewContainers() call.
func (o *Operator) RememberCurrentContainers(ctx context.Context) error {
	curContainers, err := o.GetCurrentContainers(ctx)
	if err != nil {
		return err
	}

	for _, container := range curContainers {
		o.existingContainers[container.ID] = true
	}

	return nil
}

// GetNewContainers returns the list of all the new containers that now exists
// and weren't previously remembered by a RememberCurrentContainers() call.
func (o *Operator) GetNewContainers(ctx context.Context) ([]*Container, error) {
	curContainers, err := o.GetCurrentContainers(ctx)
	if err != nil {
		return nil, err
	}

	var newContainers []*Container

	for _, container := range curContainers {
		if !o.existingContainers[container.ID] {
			newContainers = append(newContainers, container)
		}
	}

	return newContainers, nil
}

// GetNewContainerByName returns a container with given name. Only new
// containers (those not remembered by the last RememberCurrentContainers()
// call) are considered when searching for a container with that name.
func (o *Operator) GetNewContainerByName(ctx context.Context, name string) (*Container, error) {
	newContainers, err := o.GetNewContainers(ctx)
	if err != nil {
		return nil, err
	}

	for _, cntr := range newContainers {
		if cntr.HasName(name) {
			return cntr, nil
		}
	}

	return nil, nil //nolint:nilnil
}

// GetNewContainerByPath is like GetContainerByPath, but only new containers
// (those not remembered by the last RememberCurrentContainers() call) are
// considered, so that a stale or shared cidfile can't identify a container that
// the caller did not create.
func (o *Operator) GetNewContainerByPath(ctx context.Context, path string, dir string) (*Container, error) {
	cntr, err := o.GetContainerByPath(ctx, path, dir)
	if err != nil || cntr == nil {
		return nil, err
	}

	if o.existingContainers[cntr.ID] {
		return nil, nil //nolint:nilnil
	}

	return cntr, nil
}

// GetContainerByPath checks the file at the given path, and if the first line
// contains the ID of a container, that container is returned.
//
// The path can contain globs: `?` to match any non-separator character and `*`
// to match any number of them. The first file to match the glob and also
// contain a valid id is the one used.
//
// In case the name is a relative file path, the second argument is the absolute
// path to the working directory where your container was created.
//
// This method is useful if container IDs are written to file, as they are eg.
// in the case of docker when using the --cidfile argument.
func (o *Operator) GetContainerByPath(ctx context.Context, path string, dir string) (*Container, error) {
	// if name is a relative file path, then create the absolute path with dir
	cidPath := fp.RelToAbsPath(path, dir)

	if _, err := os.Stat(cidPath); err == nil {
		return o.cidPathToContainer(ctx, cidPath)
	}

	// or it might be a file path with globs
	return o.cidPathGlobToContainer(ctx, cidPath)
}

// cidPathToContainer takes the absolute path to a file that exists, reads the
// first line, and checks that it is the ID of a current container. If so,
// returns that container.
func (o *Operator) cidPathToContainer(ctx context.Context, cidPath string) (*Container, error) {
	id, err := file.GetFirstLine(cidPath)
	if err != nil {
		return nil, err
	}

	return o.GetContainerByID(ctx, id)
}

// GetContainerByID checks if the given id is the ID of a current container. If
// so, returns that container.
func (o *Operator) GetContainerByID(ctx context.Context, id string) (*Container, error) {
	curContainers, err := o.GetCurrentContainers(ctx)
	if err != nil {
		return nil, err
	}

	for _, container := range curContainers {
		if container.ID == id {
			return container, nil
		}
	}

	return nil, nil //nolint:nilnil
}

// cidPathGlobToContainer is like cidPathToContainer, but cidPath (which should
// not be relative) can contain standard glob characters such as ? and * and
// matching files will be checked until 1 contains a valid id, of which the
// container gets returned.
func (o *Operator) cidPathGlobToContainer(ctx context.Context, cidGlobPath string) (*Container, error) {
	paths, err := filepath.Glob(cidGlobPath)
	if err != nil {
		return nil, err
	}

	for _, path := range paths {
		cntr, err := o.cidPathToContainer(ctx, path)
		if err != nil {
			continue
		}

		if cntr != nil {
			return cntr, nil
		}
	}

	return nil, nil //nolint:nilnil
}

// KillContainer kills the container with the given ID.
func (o *Operator) KillContainer(ctx context.Context, containerID string) error {
	return o.client.ContainerKill(ctx, containerID)
}
