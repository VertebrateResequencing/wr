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

// Tests that a command dependency (wr add --cmd_deps, or a REST "cmd"/"cwd"
// dependency), which names only a Cmd and maybe a Cwd, waits for a live job
// with that Cmd whatever container image or mounts the job was added with. The
// container and mount fields are only faked: nothing here runs a container or
// mounts anything, since a server with no RunnerCmd runs only what the test
// reserves itself.

import (
	"context"
	"os"
	"slices"
	"testing"
	"time"

	"github.com/VertebrateResequencing/wr/queue"
	. "github.com/smartystreets/goconvey/convey"
)

const (
	dvCmd         = "echo dependency variants dependency"
	dvRepGroup    = "dependency-variants"
	dvImage       = "dependency-variants-image:1"
	dvOtherImage  = "dependency-variants-image:2"
	dvCwd         = "/dependency/variants/cwd"
	dvOtherCwd    = "/dependency/variants/other"
	dvMountTarget = "dependency-variants-bucket/path"

	dvCommandKey    = "command key"
	dvDockerKey     = "docker job key"
	dvDockerRekeyed = "rekeyed docker job key"
	dvMountsKey     = "mounts job key"
	dvPlainKey      = "plain job key"
)

func TestCommandDependencyMatchesContainerAndMountJobs(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given a server and a command dependency on a Cmd", t, func() {
		d := dgrStartServer(ctx)

		defer d.stop(ctx)

		jq := d.connect()

		defer disconnect(jq)

		Convey("A job added with a docker image is waited for", func() {
			dep := dvDependency(d)
			dep.WithDocker = dvImage

			dvSoWaitsForExactly(d, jq, dvDependent(d, "docker", ""), dep)
		})

		Convey("A job added with a singularity image is waited for", func() {
			dep := dvDependency(d)
			dep.WithSingularity = dvImage

			dvSoWaitsForExactly(d, jq, dvDependent(d, "singularity", ""), dep)
		})

		Convey("A job added with mounts is waited for", func() {
			dep := dvDependency(d)
			dep.MountConfigs = MountConfigs{{Targets: []MountTarget{{Path: dvMountTarget}}}}

			dvSoWaitsForExactly(d, jq, dvDependent(d, "mounts", ""), dep)
		})

		Convey("Every live job with the Cmd is waited for, and a finished one releases only itself", func() {
			plain := dvDependency(d)
			plain.Priority = dgaLoudPriority
			docker := dvDependency(d)
			docker.WithDocker = dvImage
			singularity := dvDependency(d)
			singularity.WithSingularity = dvOtherImage
			dgrAddJobs(jq, []*Job{plain, docker, singularity})

			dependent := dvDependent(d, "ambiguous", "")
			dgrAddJobs(jq, []*Job{dependent})

			So(dvItemDeps(d.server, dependent.Key()), ShouldResemble,
				dvSorted(plain.Key(), docker.Key(), singularity.Key()))

			dgaExecuteReserved(ctx, d, jq, plain.Key())

			item, err := d.server.q.Get(dependent.Key())
			So(err, ShouldBeNil)
			So(dvSorted(item.UnresolvedDependencies()...), ShouldResemble,
				dvSorted(docker.Key(), singularity.Key()))
			So(dgaItemState(d.server, dependent.Key()), ShouldEqual, queue.ItemStateDependent)

			Convey("and archiving the rest empties the index and releases the dependent", func() {
				for range 2 {
					dvArchiveReserved(jq)
				}

				So(d.server.depGroups.liveCommandVariants(docker.commandKey(docker.Key())), ShouldBeEmpty)
				So(dgaItemState(d.server, dependent.Key()), ShouldEqual, queue.ItemStateReady)
			})
		})

		Convey("A Cwd is matched only as the job's key would match it", func() {
			dep := dvDependency(d)
			dep.WithDocker = dvImage
			dep.CwdMatters = true
			dep.Cwd = dvCwd
			dgrAddJobs(jq, []*Job{dep})

			sameCwd := dvDependent(d, "same cwd", dvCwd)
			otherCwd := dvDependent(d, "other cwd", dvOtherCwd)
			noCwd := dvDependent(d, "no cwd", "")
			dgrAddJobs(jq, []*Job{sameCwd, otherCwd, noCwd})

			So(dvItemDeps(d.server, sameCwd.Key()), ShouldResemble, []string{dep.Key()})
			So(dvItemDeps(d.server, otherCwd.Key()), ShouldBeEmpty)
			So(dvItemDeps(d.server, noCwd.Key()), ShouldBeEmpty)
		})

		Convey("An essence naming an image still names only the job with that image", func() {
			docker := dvDependency(d)
			docker.WithDocker = dvImage
			other := dvDependency(d)
			other.WithDocker = dvOtherImage
			dgrAddJobs(jq, []*Job{docker, other})

			dependent := d.job("echo dependency variants exact", dvRepGroup)
			dependent.Dependencies = Dependencies{{Essence: &JobEssence{Cmd: dvCmd, WithDocker: dvImage}}}
			dgrAddJobs(jq, []*Job{dependent})

			So(dvItemDeps(d.server, dependent.Key()), ShouldResemble, []string{docker.Key()})
		})

		Convey("A container job's dependents survive a restart", func() {
			dep := dvDependency(d)
			dep.WithDocker = dvImage
			dgrAddJobs(jq, []*Job{dep})

			dependent := dvDependent(d, "restart", "")
			dgrAddJobs(jq, []*Job{dependent})

			disconnect(jq)
			d.restart(ctx)
			jq = d.connect()

			So(dvItemDeps(d.server, dependent.Key()), ShouldResemble, []string{dep.Key()})

			Convey("and a new dependent added after recovery also waits for it", func() {
				later := dvDependent(d, "after restart", "")
				dgrAddJobs(jq, []*Job{later})

				So(dvItemDeps(d.server, later.Key()), ShouldResemble, []string{dep.Key()})
			})
		})

		Convey("A container job whose image is modified is found under its new key", func() {
			dep := dvDependency(d)
			dep.WithDocker = dvImage
			dgrAddJobs(jq, []*Job{dep})

			jm := NewJobModifer()
			jm.SetWithDocker(dvOtherImage)
			newKey := dgaModify(jq, dep, jm)
			So(newKey, ShouldNotEqual, dep.Key())

			dependent := dvDependent(d, "modified", "")
			dgrAddJobs(jq, []*Job{dependent})

			So(dvItemDeps(d.server, dependent.Key()), ShouldResemble, []string{newKey})
		})

		Convey("A deleted container job is not waited for", func() {
			dep := dvDependency(d)
			dep.WithDocker = dvImage
			dgrAddJobs(jq, []*Job{dep})

			deleted, err := jq.Delete([]*JobEssence{{JobKey: dep.Key()}})
			So(err, ShouldBeNil)
			So(deleted, ShouldEqual, 1)
			So(d.server.depGroups.liveCommandVariants(dep.commandKey(dep.Key())), ShouldBeEmpty)

			dependent := dvDependent(d, "deleted", "")
			dgrAddJobs(jq, []*Job{dependent})

			So(dvItemDeps(d.server, dependent.Key()), ShouldBeEmpty)
		})
	})
}

func TestCommandVariantsHoldsNothingOnceForgotten(t *testing.T) {
	Convey("Given an empty commandVariants", t, func() {
		var v commandVariants

		v.init()

		Convey("recording, rekeying and forgetting every job leaves no entry behind", func() {
			v.record(dvDockerKey, dvCommandKey)
			v.record(dvMountsKey, dvCommandKey)
			v.record(dvDockerKey, dvCommandKey)
			v.record(dvPlainKey, dvPlainKey)
			So(dvSorted(v.of(dvCommandKey)...), ShouldResemble, dvSorted(dvDockerKey, dvMountsKey))
			So(v.of(dvPlainKey), ShouldBeEmpty)

			v.rekey(dvDockerKey, dvDockerRekeyed, dvCommandKey)
			So(dvSorted(v.of(dvCommandKey)...), ShouldResemble, dvSorted(dvDockerRekeyed, dvMountsKey))

			v.rekey(dvMountsKey, dvCommandKey, dvCommandKey)
			So(v.of(dvCommandKey), ShouldResemble, []string{dvDockerRekeyed})

			v.forget(dvDockerRekeyed)
			v.forget(dvDockerRekeyed)
			v.forget(dvPlainKey)

			So(dvIndexEntries(&v), ShouldEqual, 0)
		})
	})
}

// dvIndexEntries returns how many entries both of v's maps hold, so a test can
// see that forgetting a job deletes its entries rather than leaving them empty.
func dvIndexEntries(v *commandVariants) int {
	entries := 0

	for i := range depGroupShards {
		entries += len(v.byCommand[i].members) + len(v.byJob[i].command)
	}

	return entries
}

// dvArchiveReserved reserves the next ready job and archives it as a success,
// without running its command.
func dvArchiveReserved(jq *Client) {
	job, err := jq.Reserve(dgrReserveWait)
	So(err, ShouldBeNil)
	So(job, ShouldNotBeNil)
	So(jq.Started(job, os.Getpid()), ShouldBeNil)
	So(jq.Archive(job, &JobEndState{Exited: true, EndTime: time.Now()}), ShouldBeNil)
}

// dvDependency returns a job running dvCmd, for the caller to give container or
// mount fields.
func dvDependency(d *dgrServer) *Job {
	return d.job(dvCmd, dvRepGroup)
}

// dvSoWaitsForExactly adds dep and then dependent, and asserts that dependent is
// waiting on dep and nothing else.
func dvSoWaitsForExactly(d *dgrServer, jq *Client, dependent, dep *Job) {
	dgrAddJobs(jq, []*Job{dep})
	dgrAddJobs(jq, []*Job{dependent})

	So(dvItemDeps(d.server, dependent.Key()), ShouldResemble, []string{dep.Key()})
	So(dgaItemState(d.server, dependent.Key()), ShouldEqual, queue.ItemStateDependent)
}

// dvDependent returns a job with a command dependency on dvCmd and cwd, built
// the way wr add --cmd_deps builds one. name makes its own Cmd, and so its key,
// unique.
func dvDependent(d *dgrServer, name, cwd string) *Job {
	job := d.job("echo dependency variants dependent "+name, dvRepGroup)
	job.Dependencies = Dependencies{NewEssenceDependency(dvCmd, cwd)}

	return job
}

// dvItemDeps returns the sorted queue dependency keys of the job with this key.
func dvItemDeps(s *Server, key string) []string {
	item, err := s.q.Get(key)
	So(err, ShouldBeNil)

	return dvSorted(item.Dependencies()...)
}

func dvSorted(keys ...string) []string {
	sorted := slices.Clone(keys)
	slices.Sort(sorted)

	return sorted
}
