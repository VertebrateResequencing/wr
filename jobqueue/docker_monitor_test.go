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
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/VertebrateResequencing/wr/clog"
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

	// dockerTestAPIVersion is the docker API version our fake daemon serves; the
	// moby client asks for a pinned version directly instead of negotiating one.
	dockerTestAPIVersion = "1.51"

	// dockerTestJobKey stands in for the Key() of the job whose container is
	// being monitored, which is the value wr labels a container it starts for
	// that job with.
	dockerTestJobKey = "thisjobskey"

	// dockerTestOtherJobKey is the same for a different job, whose container
	// must never be adopted by the job under test.
	dockerTestOtherJobKey = "anotherjobskey"

	// dockerTestJobsContainerID is the id the tests give the container that is
	// the job's own, so that an assertion can name the only container that may
	// be monitored, and killed, with that job.
	dockerTestJobsContainerID = "jobs"
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

		Convey("A checker that reports finishing twice does not block on the second report", func() {
			finishedCh := make(chan bool, 1)

			go func() {
				rendezvous.finished()
				rendezvous.finished()

				finishedCh <- true
			}()

			select {
			case <-finishedCh:
			case <-time.After(dockerTestBound):
				So("the second finished() blocked", ShouldBeBlank)
			}
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

			go func() {
				mem, _ := dm.resolveContainerMem(ctx, "/tmp", 100)

				memCh <- mem
			}()

			mem, returned := awaitMem(memCh)
			So(returned, ShouldBeTrue)
			So(mem, ShouldEqual, 100)
			So(dm.failures, ShouldEqual, 1)

			Convey("And monitoring is given up on after repeated failures", func() {
				for range dockerFailureTolerance + 2 {
					dm.resolveContainerMem(ctx, "/tmp", 100)
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

			memCh := make(chan int, 1)

			go func() {
				mem, _ := dm.resolveContainerMem(ctx, "/tmp", 100)

				memCh <- mem
			}()

			mem, returned := awaitMem(memCh)
			So(returned, ShouldBeTrue)
			So(mem, ShouldEqual, 100)
			So(dm.containerID, ShouldBeBlank)
			So(dm.failures, ShouldEqual, 1)
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
				_, err := newDockerMonitor(ctx, "?", dockerTestJobKey, stalled, dockerTestCallTimeout)
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

// awaitMem waits up to dockerTestBound for a memory reading to arrive on ch,
// returning it and whether it arrived in time.
func awaitMem(ch <-chan int) (int, bool) {
	select {
	case mem := <-ch:
		return mem, true
	case <-time.After(dockerTestBound):
		return 0, false
	}
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
// MB, exist. It carries no labels, like a container started by anything other
// than wr.
func (f *fakeInteractor) add(id, name string, memMB int) {
	f.addLabelled(id, name, memMB, nil)
}

// addForJob is like add, but the container carries the label wr puts on a
// container it starts for the job with the given key.
func (f *fakeInteractor) addForJob(id, name, jobKey string, memMB int) {
	f.addLabelled(id, name, memMB, map[string]string{container.JobKeyLabel: jobKey})
}

// addLabelled makes a container with the given id, name and labels, using the
// given memory in MB, exist.
func (f *fakeInteractor) addLabelled(id, name string, memMB int, labels map[string]string) {
	f.mu.Lock()
	defer f.mu.Unlock()

	cntr := &container.Container{ID: id, Names: []string{"/" + name}, Labels: labels}
	cntr.TrimNamePrefixes()

	f.containers = append(f.containers, cntr)
	f.mems[id] = memMB
}

// remove makes the container with the given id stop existing, as one that
// exits during a job's run does.
func (f *fakeInteractor) remove(id string) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.containers = slices.DeleteFunc(f.containers, func(cntr *container.Container) bool {
		return cntr.ID == id
	})

	delete(f.mems, id)
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

		dm, err := newDockerMonitor(ctx, "mytool", dockerTestJobKey, fake, dockerTestCallTimeout)
		So(err, ShouldBeNil)

		Convey("It is not monitored, so nothing of it can be killed", func() {
			mem, cpu := dm.resolveContainerMem(ctx, "/tmp", 100)
			So(dm.failures, ShouldEqual, 0)
			So(mem, ShouldEqual, 100)
			So(cpu, ShouldEqual, 0)
			So(fake.killedIDs(), ShouldBeEmpty)
		})

		Convey("But a container of that name that appears afterwards is monitored", func() {
			fake.add(dockerTestJobsContainerID, "mytool", 300)

			mem, _ := dm.resolveContainerMem(ctx, "/tmp", 100)
			So(dm.failures, ShouldEqual, 0)
			So(mem, ShouldEqual, 300)

			Convey("And a kill request kills only that container", func() {
				So(dm.killContainer(ctx), ShouldBeNil)
				So(fake.killedIDs(), ShouldResemble, []string{dockerTestJobsContainerID})
			})
		})
	})

	Convey("Given a cidfile naming a container that is already running", t, func() {
		fake := newFakeInteractor()
		fake.add("preexisting", "someone_elses", 500)

		dir := t.TempDir()
		cidPath := filepath.Join(dir, "job.cid")
		So(os.WriteFile(cidPath, []byte("preexisting\n"), 0600), ShouldBeNil)

		dm, err := newDockerMonitor(ctx, cidPath, dockerTestJobKey, fake, dockerTestCallTimeout)
		So(err, ShouldBeNil)

		Convey("It is not monitored", func() {
			mem, _ := dm.resolveContainerMem(ctx, dir, 100)
			So(dm.failures, ShouldEqual, 0)
			So(mem, ShouldEqual, 100)
		})

		Convey("But the container the job goes on to create is monitored", func() {
			fake.add(dockerTestJobsContainerID, "jobs_own", 300)
			So(os.WriteFile(cidPath, []byte(dockerTestJobsContainerID+"\n"), 0600), ShouldBeNil)

			mem, _ := dm.resolveContainerMem(ctx, dir, 100)
			So(dm.failures, ShouldEqual, 0)
			So(mem, ShouldEqual, 300)
		})
	})

	Convey("Given a monitor of the first container to appear, with one already running", t, func() {
		fake := newFakeInteractor()
		fake.add("preexisting", "someone_elses", 500)

		dm, err := newDockerMonitor(ctx, "?", dockerTestJobKey, fake, dockerTestCallTimeout)
		So(err, ShouldBeNil)

		Convey("The already running one is not monitored", func() {
			mem, _ := dm.resolveContainerMem(ctx, "/tmp", 100)
			So(dm.failures, ShouldEqual, 0)
			So(mem, ShouldEqual, 100)
		})

		Convey("The next one to appear is monitored", func() {
			fake.add(dockerTestJobsContainerID, "jobs_own", 300)

			mem, _ := dm.resolveContainerMem(ctx, "/tmp", 100)
			So(dm.failures, ShouldEqual, 0)
			So(mem, ShouldEqual, 300)
		})
	})

	Convey("Given a monitor of the first container to appear, and 2 unlabelled ones appearing", t, func() {
		fake := newFakeInteractor()
		fake.add("preexisting", "was_here_first", 500)

		dm, err := newDockerMonitor(ctx, "?", dockerTestJobKey, fake, dockerTestCallTimeout)
		So(err, ShouldBeNil)

		// a co-tenant's container appears first, then the job's own one, and
		// nothing about either of them says which is which
		fake.add("cotenants", "someone_elses_tool", 900)
		fake.add(dockerTestJobsContainerID, "jobs_own", 300)

		buff := clog.ToBufferAtLevel("warn")

		defer clog.ToDefault()

		Convey("Neither is monitored, so neither one's memory is charged to the job", func() {
			mem, cpu := dm.resolveContainerMem(ctx, "/tmp", 100)
			So(dm.failures, ShouldEqual, 0)
			So(mem, ShouldEqual, 100)
			So(cpu, ShouldEqual, 0)

			Convey("And a kill request kills neither of them", func() {
				So(dm.killContainer(ctx), ShouldBeNil)
				So(fake.killedIDs(), ShouldBeEmpty)
			})

			Convey("And a warning says the job's usage is under-reported", func() {
				So(buff.String(), ShouldContainSubstring, "lvl=warn")
				So(buff.String(), ShouldContainSubstring, "more than one container appeared while it ran")
				So(buff.String(), ShouldContainSubstring, "so its usage will be under-reported")
				So(buff.String(), ShouldContainSubstring, "no container will be killed with the job")

				// the ids let a user find the containers the warning is about,
				// so the warning has to actually carry them
				So(buff.String(), ShouldContainSubstring, "cotenants[someone_elses_tool]")
				So(buff.String(), ShouldContainSubstring, dockerTestJobsContainerID+"[jobs_own]")
			})
		})
	})

	Convey("Given a monitor of the first container to appear, and a new one labelled as another job's", t, func() {
		fake := newFakeInteractor()
		fake.add("preexisting", "was_here_first", 500)

		dm, err := newDockerMonitor(ctx, "?", dockerTestJobKey, fake, dockerTestCallTimeout)
		So(err, ShouldBeNil)

		fake.addForJob("anothers", "another_jobs_own", dockerTestOtherJobKey, 900)
		fake.add(dockerTestJobsContainerID, "jobs_own", 300)

		buff := clog.ToBufferAtLevel("warn")

		defer clog.ToDefault()

		Convey("The other job's is ruled out, so the one that could be this job's is monitored", func() {
			mem, _ := dm.resolveContainerMem(ctx, "/tmp", 100)
			So(dm.failures, ShouldEqual, 0)
			So(mem, ShouldEqual, 300)
			So(buff.String(), ShouldBeBlank)

			Convey("And a kill request kills only that container", func() {
				So(dm.killContainer(ctx), ShouldBeNil)
				So(fake.killedIDs(), ShouldResemble, []string{dockerTestJobsContainerID})
			})
		})
	})

	Convey("Given a monitor of the first container to appear, and a new one labelled as this job's", t, func() {
		fake := newFakeInteractor()
		fake.add("preexisting", "was_here_first", 500)

		dm, err := newDockerMonitor(ctx, "?", dockerTestJobKey, fake, dockerTestCallTimeout)
		So(err, ShouldBeNil)

		Convey("It is monitored when another new container appeared before it", func() {
			fake.add("cotenants", "someone_elses_tool", 900)
			fake.addForJob(dockerTestJobsContainerID, "jobs_own", dockerTestJobKey, 300)

			soOnlyTheJobsContainerIsMonitored(ctx, dm, fake)
		})

		Convey("It is monitored when another new container appeared after it", func() {
			fake.addForJob(dockerTestJobsContainerID, "jobs_own", dockerTestJobKey, 300)
			fake.add("cotenants", "someone_elses_tool", 900)

			soOnlyTheJobsContainerIsMonitored(ctx, dm, fake)
		})
	})

	Convey("Given a monitor of the first container to appear, and none appearing", t, func() {
		fake := newFakeInteractor()
		fake.add("preexisting", "was_here_first", 500)

		dm, err := newDockerMonitor(ctx, "?", dockerTestJobKey, fake, dockerTestCallTimeout)
		So(err, ShouldBeNil)

		buff := clog.ToBufferAtLevel("warn")

		defer clog.ToDefault()

		Convey("Nothing is monitored, and nothing is said about a container that never came", func() {
			mem, _ := dm.resolveContainerMem(ctx, "/tmp", 100)
			So(dm.failures, ShouldEqual, 0)
			So(mem, ShouldEqual, 100)
			So(buff.String(), ShouldBeBlank)

			Convey("And a kill request made anyway kills nothing", func() {
				So(dm.killContainer(ctx), ShouldBeNil)
				So(fake.killedIDs(), ShouldBeEmpty)
			})
		})
	})

	Convey("Given a monitor that has refused to choose between 2 new containers", t, func() {
		fake := newFakeInteractor()
		fake.add("preexisting", "was_here_first", 500)

		dm, err := newDockerMonitor(ctx, "?", dockerTestJobKey, fake, dockerTestCallTimeout)
		So(err, ShouldBeNil)

		fake.add("cotenants", "someone_elses_tool", 900)
		fake.add(dockerTestJobsContainerID, "jobs_own", 300)

		buff := clog.ToBufferAtLevel("warn")

		defer clog.ToDefault()

		mem, _ := dm.resolveContainerMem(ctx, "/tmp", 100)
		So(mem, ShouldEqual, 100)

		Convey("One of them exiting does not make the other one the job's", func() {
			fake.remove("cotenants")

			mem, _ = dm.resolveContainerMem(ctx, "/tmp", 100)
			So(dm.failures, ShouldEqual, 0)
			So(mem, ShouldEqual, 100)

			So(dm.killContainer(ctx), ShouldBeNil)
			So(fake.killedIDs(), ShouldBeEmpty)

			Convey("And the warning is not repeated on every check", func() {
				So(strings.Count(buff.String(), "more than one container appeared while it ran"),
					ShouldEqual, 1)
			})
		})
	})
}

// soOnlyTheJobsContainerIsMonitored asserts that dm has adopted the container
// the test gave dockerTestJobsContainerID and 300MB of memory, and that a kill
// request kills that container alone.
func soOnlyTheJobsContainerIsMonitored(ctx context.Context, dm *dockerMonitor, fake *fakeInteractor) {
	mem, _ := dm.resolveContainerMem(ctx, "/tmp", 100)
	So(dm.failures, ShouldEqual, 0)
	So(mem, ShouldEqual, 300)

	So(dm.killContainer(ctx), ShouldBeNil)
	So(fake.killedIDs(), ShouldResemble, []string{dockerTestJobsContainerID})
}

// failingListDockerSocket serves a fake docker API on a unix socket, returning
// the DOCKER_HOST value that addresses it. It answers the first container
// listing (so that a monitor can be created for a job) and fails every listing
// after that, which is what a docker daemon hiccup during a job's run looks
// like. It needs no docker.
func failingListDockerSocket(t *testing.T) string {
	t.Helper()

	sock := filepath.Join(t.TempDir(), "docker.sock")

	var listenConfig net.ListenConfig

	listener, err := listenConfig.Listen(context.Background(), "unix", sock)
	So(err, ShouldBeNil)

	var (
		mu    sync.Mutex
		lists int
	)

	server := &http.Server{
		ReadHeaderTimeout: dockerTestBound,
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !strings.HasSuffix(r.URL.Path, "/containers/json") {
				http.Error(w, `{"message":"not implemented by this fake"}`, http.StatusNotFound)

				return
			}

			mu.Lock()
			lists++
			first := lists == 1
			mu.Unlock()

			if !first {
				http.Error(w, `{"message":"a transient docker problem"}`, http.StatusInternalServerError)

				return
			}

			w.Header().Set("Content-Type", "application/json")

			//nolint:errcheck // a client that has gone away is not this fake's problem.
			w.Write([]byte("[]"))
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

// soBehavioursRan asserts whether the `run` behaviour that creates the marker
// file at path ran, naming the behaviour set in any failure so that the output
// says which one wrongly ran or wrongly did not.
func soBehavioursRan(name, path string, wanted bool) {
	_, err := os.Stat(path)

	So(name+" behaviours ran: "+strconv.FormatBool(err == nil), ShouldEqual,
		name+" behaviours ran: "+strconv.FormatBool(wanted))
}

func TestDockerLookupErrorNotJobFailure(t *testing.T) {
	if runnermode || servermode {
		return
	}

	Convey("Given a job with success and failure behaviours, whose command succeeds", t, func() {
		markers := t.TempDir()
		successMarker := filepath.Join(markers, "success")
		failureMarker := filepath.Join(markers, "failure")

		client := newLiveExecuteCaptureClient(&liveTouchCapture{})
		job := liveExecuteJob(client, liveExecuteCwd(t), "sleep 1.5")
		job.Behaviours = Behaviours{
			{When: OnSuccess, Do: Run, Arg: "touch " + successMarker},
			{When: OnFailure, Do: Run, Arg: "touch " + failureMarker},
		}

		Convey("Only its success behaviours are triggered", func() {
			So(client.Execute(context.Background(), job, "/bin/bash"), ShouldBeNil)

			soBehavioursRan("on_failure", failureMarker, false)
			soBehavioursRan("on_success", successMarker, true)
		})

		Convey("A docker container lookup that fails while it runs does not make it a failure", func() {
			t.Setenv("DOCKER_HOST", failingListDockerSocket(t))
			t.Setenv("DOCKER_API_VERSION", dockerTestAPIVersion)

			job.MonitorDocker = "mytool"

			So(client.Execute(context.Background(), job, "/bin/bash"), ShouldBeNil)

			soBehavioursRan("on_failure", failureMarker, false)
			soBehavioursRan("on_success", successMarker, true)
		})
	})
}
