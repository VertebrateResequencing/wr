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
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/VertebrateResequencing/wr/container"
	cn "github.com/moby/moby/api/types/container"
	nw "github.com/moby/moby/api/types/network"
	"github.com/moby/moby/client"
	. "github.com/smartystreets/goconvey/convey"
)

// testImage is the image the tests that need a real docker daemon run. It is
// alpine, not ubuntu, because all they need is a container that stays up, and
// alpine is a 13MB pull rather than a 160MB one; it is also what
// container/run_test.go's real tests use, so a developer running ./container/...
// needs one image and not two.
//
// It is pinned by digest so that pulling it cannot re-point a developer's own
// alpine:latest: docker only writes a tag when it pulls one, and this asks for
// a digest. The digest is that of the multi-platform index alpine:latest
// pointed at on 2026-09-09 (alpine 3.24.1), so it resolves on any architecture
// the daemon runs.
const testImage = "alpine@sha256:28bd5fe8b56d1bd048e5babf5b10710ebe0bae67db86916198a6eec434943f8b"

// testAPIVersion is the docker API version our fake daemon serves; the moby
// client asks for a pinned version directly instead of negotiating one.
const testAPIVersion = "1.51"

// testFakeDaemonBound is how long our fake daemon will wait for a client's
// request headers before giving up on it.
const testFakeDaemonBound = 30 * time.Second

var errCloseStats = errors.New("close stats")

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

type trackingReadCloser struct {
	io.Reader
	closeErr error
	closed   bool
}

func (t *trackingReadCloser) Close() error {
	t.closed = true

	return t.closeErr
}

func TestDockerDecodeContainerStats(t *testing.T) {
	Convey("Decode the Container stats", t, func() {
		Convey("for empty ReaderCloser stats", func() {
			emptyRC := &trackingReadCloser{Reader: bytes.NewReader([]byte(""))}

			stats, err := decodeDockerContainerStats(emptyRC)
			So(stats, ShouldBeNil)
			So(err, ShouldNotBeNil)
			So(emptyRC.closed, ShouldBeTrue)
		})

		Convey("returning the decode error when closing malformed stats fails", func() {
			malformedRC := &trackingReadCloser{
				Reader:   bytes.NewReader([]byte("}")),
				closeErr: errCloseStats,
			}

			stats, err := decodeDockerContainerStats(malformedRC)
			So(stats, ShouldBeNil)
			So(err, ShouldNotEqual, errCloseStats)
			So(malformedRC.closed, ShouldBeTrue)
		})

		Convey("for non-empty ReaderCloser stats", func() {
			nonEmptyRC := io.NopCloser(bytes.NewReader([]byte(testReaderCloserStats)))

			stats, err := decodeDockerContainerStats(nonEmptyRC)
			So(stats, ShouldNotBeNil)
			So(err, ShouldBeNil)
		})

		Convey("returning the close error after decoding stats", func() {
			nonEmptyRC := &trackingReadCloser{
				Reader:   bytes.NewReader([]byte(testReaderCloserStats)),
				closeErr: errCloseStats,
			}

			stats, err := decodeDockerContainerStats(nonEmptyRC)
			So(stats, ShouldNotBeNil)
			So(err, ShouldEqual, errCloseStats)
			So(nonEmptyRC.closed, ShouldBeTrue)
		})
	})
}

// listingDockerSocket serves a fake docker API on a unix socket that answers
// every container listing with the given containers, returning the DOCKER_HOST
// value that addresses it. It lets a listing be tested without docker.
func listingDockerSocket(t *testing.T, containers []cn.Summary) string {
	t.Helper()

	sock := filepath.Join(t.TempDir(), "docker.sock")

	var listenConfig net.ListenConfig

	listener, err := listenConfig.Listen(context.Background(), "unix", sock)
	So(err, ShouldBeNil)

	listing, err := json.Marshal(containers)
	So(err, ShouldBeNil)

	server := &http.Server{
		ReadHeaderTimeout: testFakeDaemonBound,
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")

			//nolint:errcheck // a client that has gone away is not this fake's problem.
			w.Write(listing)
		}),
	}

	go func() {
		//nolint:errcheck // Serve only ever ends with the error from our Close below.
		server.Serve(listener)
	}()

	t.Cleanup(func() {
		_ = server.Close()
	})

	return "unix://" + sock
}

func pullTestImage(ctx context.Context, cli *client.Client) error {
	rc, err := cli.ImagePull(ctx, testImage, client.ImagePullOptions{})
	if err != nil {
		return err
	}

	_, copyErr := io.Copy(os.Stdout, rc)
	closeErr := rc.Close()

	if copyErr != nil {
		return copyErr
	}

	return closeErr
}

func TestDockerContainerLabels(t *testing.T) {
	ctx := context.Background()

	Convey("Given a docker daemon with a labelled and an unlabelled container", t, func() {
		t.Setenv("DOCKER_HOST", listingDockerSocket(t, []cn.Summary{
			{
				ID:     "labelledid",
				Names:  []string{"/thejobskey"},
				Labels: map[string]string{container.JobKeyLabel: "thejobskey"},
			},
			{ID: "unlabelledid", Names: []string{"/someone_elses"}},
		}))
		t.Setenv("DOCKER_API_VERSION", testAPIVersion)

		cli, err := client.New(client.FromEnv)
		So(err, ShouldBeNil)

		Convey("Listing them reports the labels, so a caller can recognise its own container", func() {
			cntrs, err := NewInteractor(cli).ContainerList(ctx)
			So(err, ShouldBeNil)
			So(len(cntrs), ShouldEqual, 2)

			So(cntrs[0].ID, ShouldEqual, "labelledid")
			So(cntrs[0].Names, ShouldResemble, []string{"thejobskey"})

			value, set := cntrs[0].Label(container.JobKeyLabel)
			So(set, ShouldBeTrue)
			So(value, ShouldEqual, "thejobskey")

			So(cntrs[1].ID, ShouldEqual, "unlabelledid")

			_, set = cntrs[1].Label(container.JobKeyLabel)
			So(set, ShouldBeFalse)
		})
	})
}

// createContainers creates and starts the test containers, given a list of
// container names, and arranges for each one it creates to be removed when the
// test ends.
func createContainers(ctx context.Context, t *testing.T, cli *client.Client,
	containerNames []string) ([]string, error) {
	t.Helper()

	if err := pullTestImage(ctx, cli); err != nil {
		return nil, err
	}

	return createAndStartNamedContainers(ctx, t, cli, containerNames)
}

func createAndStartNamedContainers(ctx context.Context, t *testing.T, cli *client.Client,
	containerNames []string) ([]string, error) {
	t.Helper()

	cntrIDs := make([]string, 0, len(containerNames))

	for _, cname := range containerNames {
		containerID, err := createContainer(ctx, cli, cname)
		if err != nil {
			return cntrIDs, err
		}

		// registered before the start, because a container that fails to start
		// still exists and still holds its name.
		removeContainerAtEnd(t, cli, containerID)

		if err = startContainer(ctx, cli, containerID); err != nil {
			return cntrIDs, err
		}

		cntrIDs = append(cntrIDs, containerID)
	}

	return cntrIDs, nil
}

func createContainer(ctx context.Context, cli *client.Client, cname string) (string, error) {
	cbody, err := cli.ContainerCreate(ctx, client.ContainerCreateOptions{
		Config:           &cn.Config{Image: testImage, Tty: true},
		HostConfig:       &cn.HostConfig{},
		NetworkingConfig: &nw.NetworkingConfig{},
		Name:             cname,
	})

	return cbody.ID, err
}

// removeContainerAtEnd force-removes the given container when the test ends,
// however it ends, so that a failed assertion or a skip cannot leave a
// container running on the developer's daemon.
func removeContainerAtEnd(t *testing.T, cli *client.Client, containerID string) {
	t.Helper()

	t.Cleanup(func() {
		_, err := cli.ContainerRemove(context.Background(), containerID,
			client.ContainerRemoveOptions{Force: true})
		if err != nil {
			t.Logf("container %s could not be removed: %s", containerID, err)
		}
	})
}

func startContainer(ctx context.Context, cli *client.Client, containerID string) error {
	_, err := cli.ContainerStart(ctx, containerID, client.ContainerStartOptions{})

	return err
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
	cntrIDs, err := createContainers(ctx, t, cli, testContainerNames(t, 2))
	if err != nil {
		t.Skip("skipping docker tests: ", err)
	}

	Convey("Interactor implements container.Interactor", t, func() {
		var _ container.Interactor = (*Interactor)(nil)
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

// testContainerNames returns the given number of container names unique to this
// test run, derived from the temporary directory the test framework made for it
// the way container/run_test.go's realTestContainerName is, so that these tests
// only ever create and destroy containers of their own: not a developer's, and
// not those of a run happening at the same time.
func testContainerNames(t *testing.T, n int) []string {
	t.Helper()

	prefix := filepath.Base(filepath.Dir(t.TempDir()))

	names := make([]string, n)
	for i := range n {
		names[i] = fmt.Sprintf("%s_%d", prefix, i+1)
	}

	return names
}
