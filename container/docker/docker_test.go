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
	"bytes"
	"context"
	"io"
	"net"
	"os"
	"testing"

	"github.com/VertebrateResequencing/wr/container"
	cn "github.com/moby/moby/api/types/container"
	nw "github.com/moby/moby/api/types/network"
	"github.com/moby/moby/client"
	specs "github.com/opencontainers/image-spec/specs-go/v1"
	. "github.com/smartystreets/goconvey/convey"
)

const linuxOS = "linux"

// testReaderCloserStats is the dummy ReaderCloserStats data used for
// ContainerStats testing.
const testReaderCloserStats = `{
		"read":"2021-01-05T11:42:54.959351591Z",
		"preread":"2021-01-05T11:42:53.949728039Z",
		"pids_stats":{"current":4},
		"blkio_stats":{},
		"num_procs":0,
		"storage_stats":{},
		"cpu_stats":{
			"cpu_usage":{
				"total_usage":1244741231366,
				"percpu_usage":[924236203020,320505028346],
				"usage_in_kernelmode":9190000000,
				"usage_in_usermode":653150000000
			},
			"system_cpu_usage":2053540000000,
			"online_cpus":2,
			"throttling_data":{"periods":0,"throttled_periods":0,"throttled_time":0}
		},
		"precpu_stats":{},
		"memory_stats":{
			"usage":57921536,
			"max_usage":115904512,
			"stats":{
				"active_anon":1216512,
				"active_file":41766912,
				"cache":53268480,
				"dirty":135168,
				"hierarchical_memory_limit":9223372036854771712,
				"hierarchical_memsw_limit":9223372036854771712,
				"inactive_anon":0,
				"inactive_file":11354112,
				"mapped_file":3514368,
				"pgfault":97911,
				"pgmajfault":165,
				"pgpgin":66198,
				"pgpgout":52876,
			    "rss":1048576,
				"rss_huge":0,
				"total_active_anon":1216512,
				"total_active_file":41766912,
				"total_cache":53268480,
				"total_dirty":135168,
				"total_inactive_anon":0,
				"total_inactive_file":11354112,
				"total_mapped_file":3514368,
				"total_pgfault":97911,
				"total_pgmajfault":165,
				"total_pgpgin":66198,
				"total_pgpgout":52876,
				"total_rss":1048576,
				"total_rss_huge":0,
				"total_unevictable":0,
				"total_writeback":0,
				"unevictable":0,
				"writeback":0},
				"limit":2084458496
			},
			"name":"/test_container2",
			"id":"container_id2",
			"networks":{}
		}`

// createContainers creates and starts the test containers, given a list of
// container names.
func createContainers(ctx context.Context, cli *client.Client, containerNames []string) ([]string, error) {
	if err := pullUbuntuImage(ctx, cli); err != nil {
		return nil, err
	}

	return createAndStartNamedContainers(ctx, cli, containerNames)
}

func pullUbuntuImage(ctx context.Context, cli *client.Client) error {
	rc, err := cli.ImagePull(ctx, "ubuntu", client.ImagePullOptions{})
	if err != nil {
		return err
	}

	_, err = io.Copy(os.Stdout, rc)

	return err
}

func createAndStartNamedContainers(ctx context.Context, cli *client.Client, containerNames []string) ([]string, error) {
	cntrIDs := make([]string, len(containerNames))

	for idx, cname := range containerNames {
		containerID, err := createContainer(ctx, cli, cname)
		if err != nil {
			return nil, err
		}

		if err = startContainer(ctx, cli, containerID); err != nil {
			return nil, err
		}

		cntrIDs[idx] = containerID
	}

	return cntrIDs, nil
}

func createContainer(ctx context.Context, cli *client.Client, cname string) (string, error) {
	cbody, err := cli.ContainerCreate(ctx, client.ContainerCreateOptions{
		Config:           &cn.Config{Image: "ubuntu", Tty: true},
		HostConfig:       &cn.HostConfig{},
		NetworkingConfig: &nw.NetworkingConfig{},
		Platform:         &specs.Platform{Architecture: "amd64", OS: linuxOS},
		Name:             cname,
	})

	return cbody.ID, err
}

func startContainer(ctx context.Context, cli *client.Client, containerID string) error {
	_, err := cli.ContainerStart(ctx, containerID, client.ContainerStartOptions{})

	return err
}

// removeContainers removes all the containers given a list of containers as a
// part of clean up step.
func removeContainers(ctx context.Context, cli *client.Client, containerIDs []string) error {
	for _, cid := range containerIDs {
		_, err := cli.ContainerRemove(ctx, cid, client.ContainerRemoveOptions{Force: true})
		if err != nil {
			return err
		}
	}

	return nil
}

func TestDocker(t *testing.T) {
	ctx := context.Background()

	// Create a new docker client
	cli, err := client.NewClientWithOpts(client.FromEnv)
	if err != nil {
		t.Skip("skipping docker tests: ", err)
	}

	// Test if server is running, if not then skip the tests.
	_, err = cli.Ping(ctx, client.PingOptions{})
	if err != nil {
		t.Skip("skipping docker tests: ", err)
	}

	// create and start the test containers
	var cntrNames = []string{"container_1", "container_2"}

	cntrIDs, err := createContainers(ctx, cli, cntrNames)
	if err != nil {
		t.Skip("skipping docker tests: ", err)
	}

	Convey("Interactor implements container.Interactor", t, func() {
		var _ container.Interactor = (*Interactor)(nil)
	})

	Convey("Decode the Container stats", t, func() {
		Convey("for empty ReaderCloser stats", func() {
			emptyRC := io.NopCloser(bytes.NewReader([]byte("")))

			stats, err := decodeDockerContainerStats(emptyRC)
			So(stats, ShouldBeNil)
			So(err, ShouldNotBeNil)
		})

		Convey("for non-empty ReaderCloser stats", func() {
			nonEmptyRC := io.NopCloser(bytes.NewReader([]byte(testReaderCloserStats)))

			stats, err := decodeDockerContainerStats(nonEmptyRC)
			So(stats, ShouldNotBeNil)
			So(err, ShouldBeNil)
		})
	})

	Convey("Given a Docker Operator", t, func() {
		dockerInterator := NewInteractor(cli)
		dockerOperator := container.NewOperator(dockerInterator)

		Convey("it can get the list of containers", func() {
			cntrList, err := dockerOperator.GetCurrentContainers(ctx)
			So(len(cntrList), ShouldBeGreaterThanOrEqualTo, len(cntrIDs))
			So(err, ShouldBeNil)

			Convey("it can get the stats of a container", func() {
				cntrID := cntrIDs[0]
				stats, err := dockerInterator.ContainerStats(ctx, cntrID)
				So(err, ShouldBeNil)
				So(stats, ShouldNotBeNil)
			})
		})
	})

	Convey("Given a Docker Interator", t, func() {
		Convey("it can list the current containers", func() {
			Convey("when the docker client is valid", func() {
				dockerInterator := NewInteractor(cli)
				cntList, err := dockerInterator.ContainerList(ctx)
				So(err, ShouldBeNil)
				So(len(cntList), ShouldBeGreaterThanOrEqualTo, len(cntrIDs))

				Convey("it can get the stats of a container", func() {
					Convey("for a correct container ID", func() {
						cntrID := cntrIDs[0]
						stats, err1 := dockerInterator.ContainerStats(ctx, cntrID)
						So(err1, ShouldBeNil)
						So(stats, ShouldNotBeNil)
					})

					Convey("not for a wrong container ID ", func() {
						cntrID := "wrongID"
						stats, err1 := dockerInterator.ContainerStats(ctx, cntrID)
						So(err1, ShouldNotBeNil)
						So(stats, ShouldBeNil)
					})
				})

				Convey("it can kill a container", func() {
					cntrID := cntrIDs[0]
					err = dockerInterator.ContainerKill(ctx, cntrID)
					So(err, ShouldBeNil)

					remainList, err := dockerInterator.ContainerList(ctx)
					So(err, ShouldBeNil)
					So(remainList, ShouldNotBeNil)

					// Cleanup: Remove all the dummy container
					err = removeContainers(ctx, cli, cntrIDs)
					if err != nil {
						t.Log("Containers could not be removed: ", err)
					}
				})
			})

			Convey("not when the docker client is invalid", func() {
				badClient, err := client.NewClientWithOpts(client.FromEnv,
					client.WithDialContext(func(ctx context.Context, network, addr string) (net.Conn, error) {
						return nil, io.EOF
					}))
				So(err, ShouldBeNil)

				dockerBadInterator := NewInteractor(badClient)
				cntList, err := dockerBadInterator.ContainerList(ctx)
				So(err, ShouldNotBeNil)
				So(cntList, ShouldBeEmpty)
			})
		})
	})
}
