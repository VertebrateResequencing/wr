// Copyright © 2018 Genome Research Limited
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

package internal

// this file has functions for dealing with docker

import (
	"context"
	"encoding/json"
	"io/ioutil"
	"os"
	"path/filepath"
	"strings"

	"github.com/docker/docker/api/types"
	docker "github.com/docker/docker/client"
)

// DockerClient offers some methods for querying docker. You must use the
// NewDockerClient() method to make one, or the methods won't work.
type DockerClient struct {
	client             *docker.Client
	existingContainers map[string]bool
}

// NewDockerClient creates a new DockerClient, connecting to docker using
// the standard environment variables to define options.
func NewDockerClient() (*DockerClient, error) {
	client, err := docker.NewClientWithOpts(docker.FromEnv)
	if err != nil {
		return nil, err
	}
	return &DockerClient{
		client:             client,
		existingContainers: make(map[string]bool),
	}, nil
}

// GetCurrentContainers returns current containers.
func (d *DockerClient) GetCurrentContainers() ([]types.Container, error) {
	return d.client.ContainerList(context.Background(), types.ContainerListOptions{})
}

// RememberCurrentContainerIDs calls GetCurrentContainerIDs() and stores the
// results, for the benefit of a future GetNewDockerContainerID() call.
func (d *DockerClient) RememberCurrentContainerIDs() error {
	containers, err := d.GetCurrentContainers()
	if err != nil {
		return err
	}
	for _, container := range containers {
		d.existingContainers[container.ID] = true
	}
	return nil
}

// GetNewDockerContainerID returns the first docker container ID that now exists
// that wasn't previously remembered by a RememberCurrentContainerIDs() call.
func (d *DockerClient) GetNewDockerContainerID() (string, error) {
	containers, err := d.GetCurrentContainers()
	if err != nil {
		return "", err
	}
	for _, container := range containers {
		if !d.existingContainers[container.ID] {
			return container.ID, nil
		}
	}
	return "", nil
}

// GetNewDockerContainerIDByName returns the ID of the container with the given
// name. Name can be the --name option given to docker when you created the
// container, or it can be the file path you gave to --cidfile. If you have some
// other process that generates a --cidfile name for you, your file path name
// can contain ? (any non-separator character) and * (any number of
// non-separator characters) wildcards. The first file to match the glob that
// also contains a valid id is the one used.
//
// In the case that name is relative file path, the second argument is the
// absolute path to the working directory where your container was created.
//
// In the case that name is the name of a container, only new containers (those
// not remembered by the last RememberCurrentContainerIDs() call) are
// considered when searching for a container with that name.
func (d *DockerClient) GetNewDockerContainerIDByName(name string, dir string) (string, error) {
	// name might be a literal file path
	cidPath := name
	if !strings.HasPrefix(cidPath, "/") {
		cidPath = filepath.Join(dir, cidPath)
	}
	_, err := os.Stat(cidPath)
	if err == nil {
		id, errc := d.cidPathToID(cidPath)
		if errc != nil {
			return "", errc
		}
		if id != "" {
			return id, nil
		}
	}

	// or might be a container name
	id, err := d.nameToID(name)
	if err != nil {
		return "", err
	}
	if id != "" {
		return id, nil
	}

	// or it might be a file path with globs
	return d.cidPathGlobToID(cidPath)
}

// cidPathToID takes the absolute path to a file that exists, reads the first
// line, and checks that it is the ID of a current container. If so, returns
// that ID.
func (d *DockerClient) cidPathToID(cidPath string) (string, error) {
	b, err := ioutil.ReadFile(cidPath)
	if err != nil {
		return "", err
	}
	id := strings.TrimSuffix(string(b), "\n")
	ok, err := d.verifyID(id)
	if err != nil {
		return "", err
	}
	if ok {
		return id, nil
	}
	return "", nil
}

// verifyID checks if the given id is the ID of a current container.
func (d *DockerClient) verifyID(id string) (bool, error) {
	containers, err := d.GetCurrentContainers()
	if err != nil {
		return false, err
	}
	for _, container := range containers {
		if container.ID == id {
			return true, nil
		}
	}
	return false, nil
}

// name to ID looks up current containers for one with the given name,
// restricted to new containers.
func (d *DockerClient) nameToID(name string) (string, error) {
	containers, err := d.GetCurrentContainers()
	if err != nil {
		return "", err
	}
	for _, container := range containers {
		if !d.existingContainers[container.ID] {
			for _, cname := range container.Names {
				cname = strings.TrimPrefix(cname, "/")
				if cname == name {
					return container.ID, nil
				}
			}
		}
	}
	return "", nil
}

// cidPathGlobToID is like cidPathToID, but cidPath (which should not be
// relative) can contain standard glob characters such as ? and * and matching
// files will be checked until 1 contains a valid id, which gets returned.
func (d *DockerClient) cidPathGlobToID(cidGlobPath string) (string, error) {
	paths, err := filepath.Glob(cidGlobPath)
	if err != nil {
		return "", err
	}
	for _, path := range paths {
		id, err := d.cidPathToID(path)
		if err != nil {
			return "", err
		}
		if id != "" {
			return id, nil
		}
	}
	return "", nil
}

// ContainerStats asks docker for the current memory usage (RSS) and total CPU
// usage of the container with the given id.
func (d *DockerClient) ContainerStats(containerID string) (memMB int, cpuSec int, err error) {
	stats, err := d.client.ContainerStats(context.Background(), containerID, false)
	if err != nil {
		return 0, 0, err
	}

	var ds *types.Stats
	err = json.NewDecoder(stats.Body).Decode(&ds)
	if err != nil {
		return 0, 0, err
	}

	memMB = int(ds.MemoryStats.Stats["rss"] / 1024 / 1024)     // bytes to MB
	cpuSec = int(ds.CPUStats.CPUUsage.TotalUsage / 1000000000) // nanoseconds to seconds

	err = stats.Body.Close()
	return memMB, cpuSec, err
}

// KillContainer kills the container with the given ID.
func (d *DockerClient) KillContainer(containerID string) error {
	return d.client.ContainerKill(context.Background(), containerID, "SIGKILL")
}
