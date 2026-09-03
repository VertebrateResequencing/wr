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

package jobqueue

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/VertebrateResequencing/wr/container"
	. "github.com/smartystreets/goconvey/convey"
)

// errNoSuchContainer is what a fake interactor returns for a container that
// does not exist, as docker would.
var errNoSuchContainer = errors.New("no such container")

const (
	// dockerTestCallTimeout is the per-docker-call timeout we give monitors
	// under test, so that a test of the give-up behaviour is quick.
	dockerTestCallTimeout = 100 * time.Millisecond

	// dockerTestBound is how long we let something that must not block for ever
	// take before failing the test. It is generous: exceeding it means the code
	// under test hung, and we want that to fail an assertion rather than hang
	// the whole suite.
	dockerTestBound = 30 * time.Second
)

func TestCheckingRendezvous(t *testing.T) {
	Convey("Given a checking rendezvous whose checker never finishes", t, func() {
		rendezvous := newCheckingRendezvous()

		Convey("Waiting for it gives up instead of blocking for ever", func() {
			awaited := make(chan bool, 1)

			go func() {
				awaited <- rendezvous.await(dockerTestCallTimeout)
			}()

			select {
			case finished := <-awaited:
				So(finished, ShouldBeFalse)
			case <-time.After(dockerTestBound):
				So("await() blocked", ShouldBeBlank)
			}

			Convey("And a checker that finishes later does not block for ever", func() {
				finishedCh := make(chan bool, 1)

				go func() {
					rendezvous.finished()

					finishedCh <- true
				}()

				select {
				case <-finishedCh:
				case <-time.After(dockerTestBound):
					So("finished() blocked", ShouldBeBlank)
				}
			})
		})

		Convey("Waiting for a checker that does finish reports that it finished", func() {
			go rendezvous.finished()

			So(rendezvous.await(dockerTestBound), ShouldBeTrue)
		})
	})
}

// stallingInteractor is a container.Interactor that simulates a docker daemon
// which accepts calls but never answers them: every call blocks until the
// caller's context is cancelled (which is how the real moby client behaves,
// since it makes ctx-bound HTTP requests).
type stallingInteractor struct {
	mu    sync.Mutex
	calls int
}

// callCount returns how many calls have been made to this interactor.
func (s *stallingInteractor) callCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.calls
}

// stall blocks until ctx is cancelled, then returns ctx's error.
func (s *stallingInteractor) stall(ctx context.Context) error {
	s.mu.Lock()
	s.calls++
	s.mu.Unlock()

	<-ctx.Done()

	return ctx.Err()
}

func (s *stallingInteractor) ContainerList(ctx context.Context) ([]*container.Container, error) {
	return nil, s.stall(ctx)
}

func (s *stallingInteractor) ContainerStats(ctx context.Context, _ string) (*container.Stats, error) {
	return nil, s.stall(ctx)
}

func (s *stallingInteractor) ContainerKill(ctx context.Context, _ string) error {
	return s.stall(ctx)
}

func TestDockerMonitorUnresponsiveDaemon(t *testing.T) {
	ctx := context.Background()

	Convey("Given a docker daemon that accepts calls but never answers them", t, func() {
		stalled := &stallingInteractor{}

		Convey("Getting a monitored container's memory does not block for ever", func() {
			dm := &dockerMonitor{
				operator:      container.NewOperator(stalled),
				interactor:    stalled,
				monitorDocker: "mycontainer",
				callTimeout:   dockerTestCallTimeout,
				containerID:   "alreadyfound",
			}

			memCh := make(chan int, 1)
			errCh := make(chan error, 1)

			go func() {
				mem, _, err := dm.resolveContainerMem(ctx, "/tmp", 100)

				memCh <- mem

				errCh <- err
			}()

			statsErr, returned := awaitErr(errCh)
			So(returned, ShouldBeTrue)
			So(statsErr, ShouldBeNil)
			So(<-memCh, ShouldEqual, 100)

			Convey("And monitoring is given up on after repeated failures", func() {
				for range dockerFailureTolerance + 2 {
					_, _, err := dm.resolveContainerMem(ctx, "/tmp", 100)
					So(err, ShouldBeNil)
				}

				So(stalled.callCount(), ShouldEqual, dockerFailureTolerance)
			})
		})

		Convey("Finding a monitored container by name does not block for ever", func() {
			dm := &dockerMonitor{
				operator:      container.NewOperator(stalled),
				interactor:    stalled,
				monitorDocker: "mycontainer",
				callTimeout:   dockerTestCallTimeout,
			}

			errCh := make(chan error, 1)

			go func() {
				_, _, err := dm.resolveContainerMem(ctx, "/tmp", 100)

				errCh <- err
			}()

			err, returned := awaitErr(errCh)
			So(returned, ShouldBeTrue)
			So(err, ShouldNotBeNil)
			So(dm.containerID, ShouldBeBlank)
		})

		Convey("Killing a monitored container does not block for ever", func() {
			dm := &dockerMonitor{
				operator:    container.NewOperator(stalled),
				interactor:  stalled,
				callTimeout: dockerTestCallTimeout,
				containerID: "alreadyfound",
			}

			errCh := make(chan error, 1)

			go func() {
				errCh <- dm.killContainer(ctx)
			}()

			err, returned := awaitErr(errCh)
			So(returned, ShouldBeTrue)
			So(err, ShouldNotBeNil)
		})

		Convey("Creating a monitor does not block for ever", func() {
			errCh := make(chan error, 1)

			go func() {
				_, err := newDockerMonitor(ctx, "?", stalled, dockerTestCallTimeout)
				errCh <- err
			}()

			err, returned := awaitErr(errCh)
			So(returned, ShouldBeTrue)
			So(err, ShouldNotBeNil)
		})
	})

	Convey("Given a wedged docker daemon on a socket, our docker client gives up on it", t, func() {
		t.Setenv("DOCKER_HOST", stallingDockerSocket(t))

		interactor, err := newDockerInteractor(dockerTestCallTimeout)
		So(err, ShouldBeNil)

		errCh := make(chan error, 1)

		go func() {
			_, errS := interactor.ContainerStats(ctx, "someid")
			errCh <- errS
		}()

		statsErr, returned := awaitErr(errCh)
		So(returned, ShouldBeTrue)
		So(statsErr, ShouldNotBeNil)
	})
}

// awaitErr waits up to dockerTestBound for an error to arrive on ch, returning
// it and whether it arrived in time.
func awaitErr(ch <-chan error) (error, bool) {
	select {
	case err := <-ch:
		return err, true
	case <-time.After(dockerTestBound):
		return nil, false
	}
}

// stallingDockerSocket creates a unix socket that accepts connections and never
// answers, returning the DOCKER_HOST value that addresses it. It simulates a
// wedged docker daemon without needing docker.
func stallingDockerSocket(t *testing.T) string {
	t.Helper()

	sock := filepath.Join(t.TempDir(), "docker.sock")

	var listenConfig net.ListenConfig

	listener, err := listenConfig.Listen(context.Background(), "unix", sock)
	So(err, ShouldBeNil)

	t.Cleanup(func() {
		listener.Close()
	})

	go func() {
		var conns []net.Conn

		defer func() {
			for _, conn := range conns {
				conn.Close()
			}
		}()

		for {
			conn, errA := listener.Accept()
			if errA != nil {
				return
			}

			conns = append(conns, conn)
		}
	}()

	return "unix://" + sock
}

// fakeInteractor is a container.Interactor over a set of containers the test
// controls, recording which of them get killed.
type fakeInteractor struct {
	mu         sync.Mutex
	mems       map[string]int
	containers []*container.Container
	killed     []string
}

// newFakeInteractor creates a fakeInteractor with no containers.
func newFakeInteractor() *fakeInteractor {
	return &fakeInteractor{mems: make(map[string]int)}
}

// add makes a container with the given id and name, using the given memory in
// MB, exist.
func (f *fakeInteractor) add(id, name string, memMB int) {
	f.mu.Lock()
	defer f.mu.Unlock()

	cntr := &container.Container{ID: id, Names: []string{"/" + name}}
	cntr.TrimNamePrefixes()

	f.containers = append(f.containers, cntr)
	f.mems[id] = memMB
}

// killedIDs returns the ids of the containers that have been killed.
func (f *fakeInteractor) killedIDs() []string {
	f.mu.Lock()
	defer f.mu.Unlock()

	return slices.Clone(f.killed)
}

func (f *fakeInteractor) ContainerList(_ context.Context) ([]*container.Container, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	return slices.Clone(f.containers), nil
}

func (f *fakeInteractor) ContainerStats(_ context.Context, containerID string) (*container.Stats, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	memMB, exists := f.mems[containerID]
	if !exists {
		return nil, errNoSuchContainer
	}

	return &container.Stats{MemoryMB: memMB}, nil
}

func (f *fakeInteractor) ContainerKill(_ context.Context, containerID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.killed = append(f.killed, containerID)

	return nil
}

func TestDockerMonitorAdoption(t *testing.T) {
	ctx := context.Background()

	Convey("Given a container already running under the name a job asks to monitor", t, func() {
		fake := newFakeInteractor()
		fake.add("preexisting", "mytool", 500)

		dm, err := newDockerMonitor(ctx, "mytool", fake, dockerTestCallTimeout)
		So(err, ShouldBeNil)

		Convey("It is not monitored, so nothing of it can be killed", func() {
			mem, cpu, errr := dm.resolveContainerMem(ctx, "/tmp", 100)
			So(errr, ShouldBeNil)
			So(mem, ShouldEqual, 100)
			So(cpu, ShouldEqual, 0)
			So(fake.killedIDs(), ShouldBeEmpty)
		})

		Convey("But a container of that name that appears afterwards is monitored", func() {
			fake.add("jobs", "mytool", 300)

			mem, _, errr := dm.resolveContainerMem(ctx, "/tmp", 100)
			So(errr, ShouldBeNil)
			So(mem, ShouldEqual, 300)

			Convey("And a kill request kills only that container", func() {
				So(dm.killContainer(ctx), ShouldBeNil)
				So(fake.killedIDs(), ShouldResemble, []string{"jobs"})
			})
		})
	})

	Convey("Given a cidfile naming a container that is already running", t, func() {
		fake := newFakeInteractor()
		fake.add("preexisting", "someone_elses", 500)

		dir := t.TempDir()
		cidPath := filepath.Join(dir, "job.cid")
		So(os.WriteFile(cidPath, []byte("preexisting\n"), 0600), ShouldBeNil)

		dm, err := newDockerMonitor(ctx, cidPath, fake, dockerTestCallTimeout)
		So(err, ShouldBeNil)

		Convey("It is not monitored", func() {
			mem, _, errr := dm.resolveContainerMem(ctx, dir, 100)
			So(errr, ShouldBeNil)
			So(mem, ShouldEqual, 100)
		})

		Convey("But the container the job goes on to create is monitored", func() {
			fake.add("jobs", "jobs_own", 300)
			So(os.WriteFile(cidPath, []byte("jobs\n"), 0600), ShouldBeNil)

			mem, _, errr := dm.resolveContainerMem(ctx, dir, 100)
			So(errr, ShouldBeNil)
			So(mem, ShouldEqual, 300)
		})
	})

	Convey("Given a monitor of the first container to appear, with one already running", t, func() {
		fake := newFakeInteractor()
		fake.add("preexisting", "someone_elses", 500)

		dm, err := newDockerMonitor(ctx, "?", fake, dockerTestCallTimeout)
		So(err, ShouldBeNil)

		Convey("The already running one is not monitored", func() {
			mem, _, errr := dm.resolveContainerMem(ctx, "/tmp", 100)
			So(errr, ShouldBeNil)
			So(mem, ShouldEqual, 100)
		})

		Convey("The next one to appear is monitored", func() {
			fake.add("jobs", "jobs_own", 300)

			mem, _, errr := dm.resolveContainerMem(ctx, "/tmp", 100)
			So(errr, ShouldBeNil)
			So(mem, ShouldEqual, 300)
		})
	})
}
