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

// Delete- and kick-path tests for dep-group dependency granularity (spec D3 and
// D4). Deleting a job that others depend on through a dep group must be skipped
// exactly as it is when they depend on its own key, because queue.Remove
// satisfies dependants; and kicking a buried job whose dep group has a live
// member must put it back in the dependent sub-queue rather than in ready.
//
// Both hold pre-change too - today a member's own key is what its dependants
// depend on - so these are the gates that fail if the group edges land without
// the re-derived guard (D3) or without B2's existence proxies (D4). The fixtures
// reuse the dgr* and dga* helpers from the recovery and add test files.

import (
	"context"
	"testing"

	"github.com/VertebrateResequencing/wr/queue"
	. "github.com/smartystreets/goconvey/convey"
)

const (
	dgdGroup = "depgranularity-remove-group"

	dgdParentRepGroup = "depgranularity-remove-parents"
	dgdChildRepGroup  = "depgranularity-remove-children"
	dgdBuriedRepGroup = "depgranularity-remove-buried"

	// dgdSecondName names the second member of the two-member fixture D3 test 5
	// deletes one of.
	dgdSecondName = "second"
)

// TestDepGranularityDeleteDepGroupParent covers D3 acceptance tests 1, 2 and 3:
// a job others depend on through a dep group is skipped unless those dependants
// are deleted with it, in either order.
func TestDepGranularityDeleteDepGroupParent(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given a live member of a dep group and a live job depending on that group", t, func() {
		Convey("Deleting only the member deletes nothing and leaves the dependant waiting", func() {
			d, jq, parent, child := dgdParentChildFixture(ctx)

			defer d.stop(ctx)

			defer disconnect(jq)

			deleted, err := jq.Delete(dgdEssences(parent))
			So(err, ShouldBeNil)
			So(deleted, ShouldEqual, 0)

			So(dgaItemState(d.server, parent.Key()), ShouldEqual, queue.ItemStateReady)
			So(dgaItemState(d.server, child.Key()), ShouldEqual, queue.ItemStateDependent)
			So(d.server.depGroups.hasMembers(dgdGroup), ShouldBeTrue)
		})

		Convey("Deleting both in one call, the dependant listed first, deletes both", func() {
			dgdSoDeletesBoth(ctx, false)
		})

		Convey("Deleting both in one call, the member listed first, deletes both", func() {
			dgdSoDeletesBoth(ctx, true)
		})
	})
}

// TestDepGranularityDeleteUnwaitedParent covers D3 acceptance test 4: a dep
// group's member with no waiters at all deletes cleanly, and satisfying the
// group's key with nothing depending on it is not an error.
func TestDepGranularityDeleteUnwaitedParent(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given a live member of a dep group that nothing waits on", t, func() {
		d := dgrStartServer(ctx)

		defer d.stop(ctx)

		jq := d.connect()

		defer disconnect(jq)

		parent := dgdParentJob(d, dgaSoleName)
		dgrAddJobs(jq, []*Job{parent})

		So(d.server.depGroups.hasMembers(dgdGroup), ShouldBeTrue)

		Convey("Deleting it removes it and satisfying its group's key is no error", func() {
			deleted, err := jq.Delete(dgdEssences(parent))
			So(err, ShouldBeNil)
			So(deleted, ShouldEqual, 1)

			So(d.server.q.Stats().Items, ShouldEqual, 0)
			So(d.server.depGroups.hasMembers(dgdGroup), ShouldBeFalse)
			So(d.server.q.SatisfyDependency(ctx, depGroupDependencyKey(dgdGroup)), ShouldBeNil)
		})
	})
}

// TestDepGranularityDeleteOneOfTwoMembers covers D3 acceptance test 5: one of two
// members deleted alone is still skipped, because the group its dependant waits
// on still has a waiter - which is what its own key having a dependant did
// pre-change.
func TestDepGranularityDeleteOneOfTwoMembers(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given two live members of a dep group and a live job depending on that group", t, func() {
		d := dgrStartServer(ctx)

		defer d.stop(ctx)

		jq := d.connect()

		defer disconnect(jq)

		first := dgdParentJob(d, dgaSoleName)
		second := dgdParentJob(d, dgdSecondName)
		child := dgdChildJob(d)

		dgrAddJobs(jq, []*Job{first, second, child})

		So(dgaItemState(d.server, child.Key()), ShouldEqual, queue.ItemStateDependent)

		Convey("Deleting one member alone deletes nothing", func() {
			deleted, err := jq.Delete(dgdEssences(first))
			So(err, ShouldBeNil)
			So(deleted, ShouldEqual, 0)

			So(dgaItemState(d.server, first.Key()), ShouldEqual, queue.ItemStateReady)
			So(d.server.depGroups.memberships(), ShouldEqual, 2)
		})
	})
}

// TestDepGranularityKickWithoutLiveMembers covers D4 acceptance test 1: a buried
// job whose dep group has no live member has no dependencies, so kicking it
// reaches the ready sub-queue and it can be reserved.
func TestDepGranularityKickWithoutLiveMembers(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given a buried job whose dep group has no live member", t, func() {
		d := dgrStartServer(ctx)

		defer d.stop(ctx)

		jq := d.connect()

		defer disconnect(jq)

		buried := dgdBuryFixture(ctx, d, jq)

		Convey("Kicking it makes it ready and reservable", func() {
			kicked, err := jq.Kick(dgdEssences(buried))
			So(err, ShouldBeNil)
			So(kicked, ShouldEqual, 1)

			So(dgaItemState(d.server, buried.Key()), ShouldEqual, queue.ItemStateReady)

			reserved, err := jq.Reserve(dgrReserveWait)
			So(err, ShouldBeNil)
			So(reserved, ShouldNotBeNil)
			So(reserved.Key(), ShouldEqual, buried.Key())
		})
	})
}

// TestDepGranularityKickIntoDependent covers D4 acceptance test 2: a buried job
// re-blocked by a new member of its dep group kicks into the dependent sub-queue,
// not into ready, and becomes ready when that member completes.
func TestDepGranularityKickIntoDependent(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given a buried job whose dep group gained a live member after it was buried", t, func() {
		d := dgrStartServer(ctx)

		defer d.stop(ctx)

		jq := d.connect()

		defer disconnect(jq)

		buried := dgdBuryFixture(ctx, d, jq)

		joiner := dgdParentJob(d, dgaJoinerName)
		dgrAddJobs(jq, []*Job{joiner})

		So(d.server.depGroups.hasMembers(dgdGroup), ShouldBeTrue)
		So(dgaItemState(d.server, buried.Key()), ShouldEqual, queue.ItemStateBury)

		Convey("Kicking it puts it in the dependent sub-queue until that member completes", func() {
			kicked, err := jq.Kick(dgdEssences(buried))
			So(err, ShouldBeNil)
			So(kicked, ShouldEqual, 1)

			So(dgaItemState(d.server, buried.Key()), ShouldEqual, queue.ItemStateDependent)
			So(d.server.q.Stats().Dependant, ShouldEqual, 1)

			dgaExecuteReserved(ctx, d, jq, joiner.Key())

			So(dgaItemState(d.server, buried.Key()), ShouldEqual, queue.ItemStateReady)
		})
	})
}

// dgdBuryFixture completes the dep group's only member, so the group has been
// seen but has no live member, then adds a job depending on that group and runs
// it to failure so it is buried. It returns that buried job.
func dgdBuryFixture(ctx context.Context, d *dgrServer, jq *Client) *Job {
	parent := dgdParentJob(d, dgaSoleName)
	dgrAddJobs(jq, []*Job{parent})
	dgaExecuteReserved(ctx, d, jq, parent.Key())

	So(d.server.depGroups.hasMembers(dgdGroup), ShouldBeFalse)

	buried := dgdBuriedJob(d)
	dgrAddJobs(jq, []*Job{buried})

	So(dgaItemState(d.server, buried.Key()), ShouldEqual, queue.ItemStateReady)

	reserved, err := jq.Reserve(dgrReserveWait)
	So(err, ShouldBeNil)
	So(reserved, ShouldNotBeNil)
	So(reserved.Key(), ShouldEqual, buried.Key())

	execute(ctx, jq, reserved, d.config.RunnerExecShell, true)

	So(dgaItemState(d.server, buried.Key()), ShouldEqual, queue.ItemStateBury)

	return buried
}

// dgdParentJob returns a live job belonging to dgdGroup.
func dgdParentJob(d *dgrServer, name string) *Job {
	job := d.job("echo dgd parent "+name, dgdParentRepGroup)
	job.DepGroups = []string{dgdGroup}

	return job
}

// dgdChildJob returns a live job depending on dgdGroup.
func dgdChildJob(d *dgrServer) *Job {
	job := d.job("echo dgd child", dgdChildRepGroup)
	job.Dependencies = Dependencies{NewDepGroupDependency(dgdGroup)}

	return job
}

// dgdBuriedJob returns a job that fails when run, so executing it buries it,
// depending on dgdGroup.
func dgdBuriedJob(d *dgrServer) *Job {
	job := d.job("false # dgd buried", dgdBuriedRepGroup)
	job.Dependencies = Dependencies{NewDepGroupDependency(dgdGroup)}

	return job
}

// dgdSoDeletesBoth deletes a dep group's member and the job depending on that
// group in one call, the member first if parentFirst, which is the order that
// makes the skip-and-walk loop come back for the skipped member.
func dgdSoDeletesBoth(ctx context.Context, parentFirst bool) {
	d, jq, parent, child := dgdParentChildFixture(ctx)

	defer d.stop(ctx)

	defer disconnect(jq)

	jes := dgdEssences(child, parent)
	if parentFirst {
		jes = dgdEssences(parent, child)
	}

	deleted, err := jq.Delete(jes)
	So(err, ShouldBeNil)
	So(deleted, ShouldEqual, len(jes))

	So(d.server.q.Stats().Items, ShouldEqual, 0)
	So(d.server.depGroups.hasMembers(dgdGroup), ShouldBeFalse)
}

// dgdParentChildFixture starts a server holding one live member of dgdGroup and
// one live job depending on that group. The caller stops the server and
// disconnects the client.
func dgdParentChildFixture(ctx context.Context) (*dgrServer, *Client, *Job, *Job) {
	d := dgrStartServer(ctx)
	jq := d.connect()

	parent := dgdParentJob(d, dgaSoleName)
	child := dgdChildJob(d)

	dgrAddJobs(jq, []*Job{parent, child})

	So(dgaItemState(d.server, child.Key()), ShouldEqual, queue.ItemStateDependent)

	return d, jq, parent, child
}

// dgdEssences names these jobs, in the order given, which is the order the
// delete path works through them in.
func dgdEssences(jobs ...*Job) []*JobEssence {
	jes := make([]*JobEssence, 0, len(jobs))

	for _, job := range jobs {
		jes = append(jes, &JobEssence{JobKey: job.Key()})
	}

	return jes
}
