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

package docker

import (
	"context"
	"encoding/json"
	"io"

	"github.com/VertebrateResequencing/wr/container"
	"github.com/VertebrateResequencing/wr/math/convert"
	cn "github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"
)

// Interator implements the container/Interactor interface for docker.
type Interactor struct {
	dockerClient *client.Client
}

// NewInteractor creates a new docker interactor, working on docker containers
// using the supplied client.
func NewInteractor(cl *client.Client) *Interactor {
	return &Interactor{
		dockerClient: cl,
	}
}

// ContainerList implements the Interactor interface method, which returns the
// list of containers.
func (i *Interactor) ContainerList(ctx context.Context) ([]*container.Container, error) {
	containerList, err := i.dockerClient.ContainerList(ctx, client.ContainerListOptions{})
	if err != nil {
		return nil, err
	}

	customCntrList := make([]*container.Container, len(containerList.Items))

	for idx, cntr := range containerList.Items {
		newCntr := &container.Container{ID: cntr.ID, Names: cntr.Names}
		newCntr.TrimNamePrefixes()
		customCntrList[idx] = newCntr
	}

	return customCntrList, nil
}

// ContainerStats implements the Interactor interface method, which returns the
// container stats with the given id.
func (i *Interactor) ContainerStats(ctx context.Context,
	containerID string) (*container.Stats, error) {
	stats, err := i.dockerClient.ContainerStats(ctx, containerID, client.ContainerStatsOptions{})
	if err != nil {
		return nil, err
	}

	return decodeDockerContainerStats(stats.Body)
}

// decodeDockerContainerStats takes Docker container stats and decodes them to return
// the current memory usage (RSS, in MB) and total CPU (in seconds).
func decodeDockerContainerStats(containerStats io.ReadCloser) (*container.Stats, error) {
	var ds *cn.StatsResponse

	err := json.NewDecoder(containerStats).Decode(&ds)
	closeErr := containerStats.Close()
	if err != nil {
		return nil, err
	}

	currentCustomStats := &container.Stats{}
	currentCustomStats.MemoryMB = convert.BytesToMB(ds.MemoryStats.Stats["rss"])
	currentCustomStats.CPUSec = convert.NanosecondsToSec(ds.CPUStats.CPUUsage.TotalUsage)

	return currentCustomStats, closeErr
}

// ContainerKill implements the Interactor interface method, which kills the
// container with the given id.
func (i *Interactor) ContainerKill(ctx context.Context, containerID string) error {
	_, err := i.dockerClient.ContainerKill(ctx, containerID, client.ContainerKillOptions{Signal: "SIGKILL"})

	return err
}
