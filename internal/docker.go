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

	"github.com/docker/docker/api/types"
	docker "github.com/docker/docker/client"
)

// DockerStats asks docker for the current memory usage (RSS) and total CPU
// usage of the given container.
func DockerStats(dockerClient *docker.Client, containerID string) (memMB int, cpuSec int, err error) {
	stats, err := dockerClient.ContainerStats(context.Background(), containerID, false)
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
