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

// Add- and modify-path tests for dep-group dependency granularity (spec D1 and
// D2). Both paths maintain the per-group live-member state, so a dependency on a
// dep group costs one opaque depgroup:G key however many members the group has,
// and a group the operation leaves with no live member releases its waiters
// there and then.
//
// The fixtures reuse the dgr* server helpers from
// depgranularity_recovery_test.go: a development server with its own ports,
// manager directory and database, and no ServerConfig RunnerCmd, so nothing runs
// a job the test did not reserve itself. That is what makes each sub-queue
// assertion below deterministic without polling; where more than one job is
// ready, the one the test means to run is given dgaLoudPriority so the reserve
// picks it.

import (
	"context"
	"maps"
	"slices"
	"strconv"
	"testing"

	"github.com/VertebrateResequencing/wr/queue"
	. "github.com/smartystreets/goconvey/convey"
)

const (
	// dgaMembers and dgaWaiters are D1 test 1's fixture: one dep group with 200
	// live members, and 500 live jobs waiting on it. Pre-change, one more member
	// gave those waiters dgaMembers*dgaWaiters = 100,000 dependency keys between
	// them.
	dgaMembers = 200
	dgaWaiters = 500

	// dgaBigMembers is D1 test 2's larger membership, and dgaTxRatio the factor by
	// which its add may exceed the dgaMembers add's bolt transaction count. The
	// guard is against an implementation that reads per member;
	// retrieveIncompleteJobKeysByDepGroup already opened one View per waiter
	// whatever the membership, so the ratio was ~1.0 pre-change too.
	dgaBigMembers = 2000
	dgaTxRatio    = 1.25

	// dgaTxAdds is how many one-member adds each side of that comparison
	// measures. Stats().TxN counts every read transaction the process makes, and
	// the server reads the database in the background too, so one add's couple of
	// reads is noise-dominated while dgaTxAdds of them are not.
	dgaTxAdds = 20

	dgaGroup          = "depgranularity-add-group"
	dgaBatchGroup     = "depgranularity-add-batch-group"
	dgaNeverSeenGroup = "depgranularity-add-never-seen-group"
	dgaOtherGroup     = "depgranularity-add-other-group"

	dgaMemberRepGroup = "depgranularity-add-members"
	dgaWaiterRepGroup = "depgranularity-add-waiters"

	// dgaSoleName names the single member of the fixtures that have one, dgaExtra
	// the further member D1 test 1 adds, and dgaJoiner the new member of the group
	// that D1 test 6's batch joins as it drops another job from it.
	dgaSoleName   = "sole"
	dgaExtraName  = "extra"
	dgaJoinerName = "joiner"

	// dgaModifiedCmd and dgaRESTModifiedCmd are what D2 tests 4 and 5 modify their
	// member's Cmd to, which is what changes its key.
	dgaModifiedCmd     = "echo dga modified member"
	dgaRESTModifiedCmd = "echo dga rest modified member"

	// dgaLoudPriority puts one job at the head of the ready sub-queue, so that a
	// Reserve still picks the job the test means to run when another is ready too.
	dgaLoudPriority = 255
)

// TestDepGranularityAddDepGroupWaiters covers D1 acceptance test 1: adding one
// more member to a dep group that many live jobs wait on costs one dependency key
// per waiter, not one per waiter per member.
func TestDepGranularityAddDepGroupWaiters(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given a live dep group with many members and many waiters on it", t, func() {
		d := dgrStartServer(ctx)

		defer d.stop(ctx)

		jq := d.connect()

		defer disconnect(jq)

		memberKeys := dgrAddJobs(jq, dgaMemberJobs(d, dgaGroup, dgaMembers))
		waiterKeys := dgrAddJobs(jq, dgaWaiterJobs(d, dgaGroup, dgaWaiters))

		So(memberKeys, ShouldHaveLength, dgaMembers)
		So(waiterKeys, ShouldHaveLength, dgaWaiters)

		before := d.server.depGroups.memberships()

		Convey("Adding one more member gives every waiter the one group key", func() {
			dgrAddJobs(jq, []*Job{dgaMemberJob(d, dgaGroup, dgaExtraName)})

			exactlyOne, total := dgrItemDeps(d.server, waiterKeys, depGroupDependencyKey(dgaGroup))
			So(exactlyOne, ShouldEqual, dgaWaiters)
			So(total, ShouldEqual, dgaWaiters)

			So(d.server.depGroups.memberships()-before, ShouldEqual, 1)
			So(d.server.q.Stats().Dependant, ShouldEqual, dgaWaiters)
		})
	})
}

// TestDepGranularityAddTransactionCost covers D1 acceptance test 2, a regression
// guard: what one more member costs in bolt transactions does not scale with the
// group's membership.
func TestDepGranularityAddTransactionCost(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given the same add against a small and a large dep group", t, func() {
		small := dgaAddMemberTxCost(ctx, dgaMembers)
		big := dgaAddMemberTxCost(ctx, dgaBigMembers)

		Convey("The larger group's add costs no more transactions", func() {
			So(small, ShouldBeGreaterThan, 0)
			So(float64(big), ShouldBeLessThanOrEqualTo, dgaTxRatio*float64(small))
		})
	})
}

// dgaAddMemberTxCost builds a dep group of the given size with dgaWaiters
// waiters on it, then returns how many bolt read transactions dgaTxAdds further
// one-member adds cost.
func dgaAddMemberTxCost(ctx context.Context, members int) int {
	d := dgrStartServer(ctx)

	defer d.stop(ctx)

	jq := d.connect()

	defer disconnect(jq)

	So(dgrAddJobs(jq, dgaMemberJobs(d, dgaGroup, members)), ShouldHaveLength, members)
	So(dgrAddJobs(jq, dgaWaiterJobs(d, dgaGroup, dgaWaiters)), ShouldHaveLength, dgaWaiters)

	before := d.server.db.bolt.Stats().TxN
	added := 0

	for i := range dgaTxAdds {
		inserts, _, err := jq.Add([]*Job{dgaMemberJob(d, dgaGroup, dgaExtraName+strconv.Itoa(i))}, envVars, true)
		So(err, ShouldBeNil)

		added += inserts
	}

	txns := d.server.db.bolt.Stats().TxN - before

	So(added, ShouldEqual, dgaTxAdds)

	return txns
}

// TestDepGranularityAddSameBatchMemberAndWaiter covers D1 acceptance test 3, a
// regression guard: a member of a new group and a job depending on that group,
// added in one call, see each other whichever order they are listed in.
func TestDepGranularityAddSameBatchMemberAndWaiter(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given one add call carrying a new group's member and a job depending on that group", t, func() {
		Convey("The dependent blocks on the member when the member is listed first", func() {
			dgaSameBatchCheck(ctx, false)
		})

		Convey("The dependent blocks on the member when the dependent is listed first", func() {
			dgaSameBatchCheck(ctx, true)
		})
	})
}

// dgaSameBatchCheck adds a member of a previously unseen dep group and a job
// depending on that group in one call, the dependent first if waiterFirst, and
// checks the dependent waits for the member.
func dgaSameBatchCheck(ctx context.Context, waiterFirst bool) {
	d := dgrStartServer(ctx)

	defer d.stop(ctx)

	jq := d.connect()

	defer disconnect(jq)

	member := dgaMemberJob(d, dgaBatchGroup, dgaSoleName)
	waiter := dgaWaiterJob(d, dgaBatchGroup, dgaSoleName)

	jobs := []*Job{member, waiter}
	if waiterFirst {
		jobs = []*Job{waiter, member}
	}

	dgrAddJobs(jq, jobs)

	So(dgaItemState(d.server, waiter.Key()), ShouldEqual, queue.ItemStateDependent)
	So(dgaItemState(d.server, member.Key()), ShouldEqual, queue.ItemStateReady)

	dgaExecuteReserved(ctx, d, jq, member.Key())

	So(dgaItemState(d.server, waiter.Key()), ShouldEqual, queue.ItemStateReady)
}

// TestDepGranularityAddNeverSeenGroupMember covers D1 acceptance test 4, a
// regression guard: the add path unblocks a job waiting on a never-seen group
// correctly, replacing the group it was waiting for with a real dependency on it.
func TestDepGranularityAddNeverSeenGroupMember(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given a live job waiting on a dep group no job has ever been added with", t, func() {
		d := dgrStartServer(ctx)

		defer d.stop(ctx)

		jq := d.connect()

		defer disconnect(jq)

		waiter := dgaWaiterJob(d, dgaNeverSeenGroup, dgaSoleName)
		dgrAddJobs(jq, []*Job{waiter})

		So(dgaItemState(d.server, waiter.Key()), ShouldEqual, queue.ItemStateDependent)
		So(dgrOnlyJob(jq, dgaWaiterRepGroup).WaitingForDepGroups, ShouldResemble, []string{dgaNeverSeenGroup})

		Convey("Adding a member of that group keeps it blocked, on a group that now exists", func() {
			member := dgaMemberJob(d, dgaNeverSeenGroup, dgaSoleName)
			dgrAddJobs(jq, []*Job{member})

			So(dgaItemState(d.server, waiter.Key()), ShouldEqual, queue.ItemStateDependent)
			So(dgrOnlyJob(jq, dgaWaiterRepGroup).WaitingForDepGroups, ShouldBeEmpty)

			dgaExecuteReserved(ctx, d, jq, member.Key())

			So(dgaItemState(d.server, waiter.Key()), ShouldEqual, queue.ItemStateReady)
		})
	})
}

// TestDepGranularityAddRerunDropsDepGroup covers D1 acceptance test 5: re-adding
// a live job with --rerun and no DepGroups leaves its group with no live member,
// which releases that group's waiters at add time. That is a documented
// consequence, not a bug: the old lookups are never deleted on this path, so a
// rebuild from the decoded record would release those waiters at the next restart
// regardless, and matching it here keeps the running manager and its own restart
// in agreement.
func TestDepGranularityAddRerunDropsDepGroup(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given a dep group whose only live member has a waiter", t, func() {
		d, jq, _, waiter := dgaOneMemberFixture(ctx)

		defer d.stop(ctx)

		defer disconnect(jq)

		Convey("Re-adding that member with --rerun and no dep groups releases the waiter at add time", func() {
			rerun := dgaMemberJob(d, dgaGroup, dgaSoleName)
			rerun.DepGroups = nil

			dgaAddRerun(jq, []*Job{rerun})

			So(d.server.depGroups.hasMembers(dgaGroup), ShouldBeFalse)
			So(dgaItemState(d.server, waiter.Key()), ShouldEqual, queue.ItemStateReady)
		})
	})
}

// TestDepGranularityAddDropAndJoinInOneCall covers D1 acceptance test 6's
// end-to-end outcome: one call that both drops a group from one job and adds
// another member of it leaves that group's waiters blocked, and releases them
// only once the new member completes.
//
// It cannot discriminate the two-pass ordering, and not by timing accident:
// joining the group puts it in prepareNewJobs' declared-depGroups set
// (db.go:2318-2322), so retrieveDependentJobs returns the live waiter and
// q.Update repairs a single replace pass' early release unconditionally. The
// harm one pass really does - the waiter going ready in between, where a runner
// can reserve it ahead of the new member - is only visible at the membership
// seam, which TestDepGranularityAddDropAndJoinMembershipPass drives.
func TestDepGranularityAddDropAndJoinInOneCall(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given a dep group whose only live member has a waiter", t, func() {
		d, jq, _, waiter := dgaOneMemberFixture(ctx)

		defer d.stop(ctx)

		defer disconnect(jq)

		Convey("One call dropping the group from that member and adding a new one keeps the waiter blocked", func() {
			rerun := dgaMemberJob(d, dgaGroup, dgaSoleName)
			rerun.DepGroups = nil

			joiner := dgaMemberJob(d, dgaGroup, dgaJoinerName)
			joiner.Priority = dgaLoudPriority

			dgaAddRerun(jq, []*Job{rerun, joiner})

			So(dgaItemState(d.server, waiter.Key()), ShouldEqual, queue.ItemStateDependent)
			So(d.server.depGroups.hasMembers(dgaGroup), ShouldBeTrue)
			So(d.server.depGroups.memberships(), ShouldEqual, 1)
			So(dgaGroupMembers(d.server.depGroups, dgaGroup), ShouldResemble, []string{joiner.Key()})

			Convey("And releases it once that new member completes", func() {
				dgaExecuteReserved(ctx, d, jq, joiner.Key())

				So(dgaItemState(d.server, waiter.Key()), ShouldEqual, queue.ItemStateReady)
			})
		})
	})
}

// TestDepGranularityAddDropAndJoinMembershipPass covers the ordering half of D1
// acceptance test 6 at the seam where the ordering is decidable, and so pins the
// two-pass STRUCTURE of updateDepGroupMembershipForNewJobs: a refactor that
// inlines the register pass into the replace loop, or swaps the two, fails here
// and nowhere else. In a whole Client.Add the early release a single replace
// pass causes is repaired by the dependency refresh that follows it, so the
// group's waiters end up in the right sub-queue either way; what one pass really
// costs is that they go ready in between, where a runner can reserve them ahead
// of the new member and then have the repairing q.Update yank the item out from
// under its own runner. Updating the batch's membership on its own leaves that
// visible.
func TestDepGranularityAddDropAndJoinMembershipPass(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given a dep group whose only live member has a waiter", t, func() {
		d, jq, _, waiter := dgaOneMemberFixture(ctx)

		defer d.stop(ctx)

		defer disconnect(jq)

		Convey("Updating a batch that drops the group and joins it keeps the waiter dependent", func() {
			rerun := dgaMemberJob(d, dgaGroup, dgaSoleName)
			rerun.DepGroups = nil

			joiner := dgaMemberJob(d, dgaGroup, dgaJoinerName)

			d.server.updateDepGroupMembershipForNewJobs(ctx, []*Job{rerun, joiner})

			So(dgaItemState(d.server, waiter.Key()), ShouldEqual, queue.ItemStateDependent)
			So(dgaGroupMembers(d.server.depGroups, dgaGroup), ShouldResemble, []string{joiner.Key()})
		})
	})
}

// TestDepGranularityModifyOutOfDepGroup covers D2 acceptance test 1: modifying a
// group's last live member out of it releases that group's waiters at modify
// time. That is a deliberate change in when waiters are released.
func TestDepGranularityModifyOutOfDepGroup(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given a dep group whose only live member has a waiter", t, func() {
		d, jq, member, waiter := dgaOneMemberFixture(ctx)

		defer d.stop(ctx)

		defer disconnect(jq)

		Convey("Modifying that member into another group releases the waiter at modify time", func() {
			dgaModify(jq, member, dgaDepGroupsModifier([]string{dgaOtherGroup}))

			So(d.server.depGroups.hasMembers(dgaGroup), ShouldBeFalse)
			So(d.server.depGroups.hasMembers(dgaOtherGroup), ShouldBeTrue)
			So(dgaItemState(d.server, waiter.Key()), ShouldEqual, queue.ItemStateReady)
		})
	})
}

// TestDepGranularityModifyLastMemberOut covers D2 acceptance test 2: with two
// live members, modifying the first out leaves the group's waiter blocked, and
// only the second leaves the group empty.
func TestDepGranularityModifyLastMemberOut(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given a dep group with two live members and a waiter", t, func() {
		d := dgrStartServer(ctx)

		defer d.stop(ctx)

		jq := d.connect()

		defer disconnect(jq)

		members := dgaMemberJobs(d, dgaGroup, 2)
		waiter := dgaWaiterJob(d, dgaGroup, dgaSoleName)

		dgrAddJobs(jq, members)
		dgrAddJobs(jq, []*Job{waiter})

		So(dgaItemState(d.server, waiter.Key()), ShouldEqual, queue.ItemStateDependent)

		Convey("The waiter is released only when the second member is modified out", func() {
			dgaModify(jq, members[0], dgaDepGroupsModifier([]string{dgaOtherGroup}))

			So(d.server.depGroups.hasMembers(dgaGroup), ShouldBeTrue)
			So(dgaItemState(d.server, waiter.Key()), ShouldEqual, queue.ItemStateDependent)

			dgaModify(jq, members[1], dgaDepGroupsModifier([]string{dgaOtherGroup}))

			So(d.server.depGroups.hasMembers(dgaGroup), ShouldBeFalse)
			So(dgaItemState(d.server, waiter.Key()), ShouldEqual, queue.ItemStateReady)
		})
	})
}

// dgaMemberJobs returns count jobs belonging to depGroup.
func dgaMemberJobs(d *dgrServer, depGroup string, count int) []*Job {
	jobs := make([]*Job, 0, count)

	for i := range count {
		jobs = append(jobs, dgaMemberJob(d, depGroup, strconv.Itoa(i)))
	}

	return jobs
}

// dgaWaiterJobs returns count jobs depending on depGroup.
func dgaWaiterJobs(d *dgrServer, depGroup string, count int) []*Job {
	jobs := make([]*Job, 0, count)

	for i := range count {
		jobs = append(jobs, dgaWaiterJob(d, depGroup, strconv.Itoa(i)))
	}

	return jobs
}

// TestDepGranularityModifyIntoNeverSeenGroup covers D2 acceptance test 3: a job
// waiting on a never-seen group - the only way a waiter blocks on a group with no
// live member - waits for a job modified into that group, rather than being
// wedged forever as it was pre-change.
func TestDepGranularityModifyIntoNeverSeenGroup(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given a live job waiting on a never-seen dep group, and a live job in no group", t, func() {
		d := dgrStartServer(ctx)

		defer d.stop(ctx)

		jq := d.connect()

		defer disconnect(jq)

		waiter := dgaWaiterJob(d, dgaNeverSeenGroup, dgaSoleName)
		joiner := d.job("echo dga joiner", dgaMemberRepGroup)
		dgrAddJobs(jq, []*Job{waiter, joiner})

		So(dgaItemState(d.server, waiter.Key()), ShouldEqual, queue.ItemStateDependent)

		Convey("Modifying that job into the group leaves the waiter blocked until it completes", func() {
			dgaModify(jq, joiner, dgaDepGroupsModifier([]string{dgaNeverSeenGroup}))

			So(d.server.depGroups.hasMembers(dgaNeverSeenGroup), ShouldBeTrue)
			So(dgaItemState(d.server, waiter.Key()), ShouldEqual, queue.ItemStateDependent)

			dgaExecuteReserved(ctx, d, jq, joiner.Key())

			So(dgaItemState(d.server, waiter.Key()), ShouldEqual, queue.ItemStateReady)
		})
	})
}

// TestDepGranularityModifyChangesMemberKey covers D2 acceptance test 4: a modify
// that changes a member's key moves its membership to the new key and drops the
// old one, so the group is neither released early nor left wedged on a phantom
// member.
func TestDepGranularityModifyChangesMemberKey(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given a dep group whose only live member has a waiter", t, func() {
		d, jq, member, waiter := dgaOneMemberFixture(ctx)

		defer d.stop(ctx)

		defer disconnect(jq)

		Convey("Changing that member's command moves its membership to its new key", func() {
			jm := &JobModifier{}
			jm.SetCmd(dgaModifiedCmd)

			newKey := dgaModify(jq, member, jm)
			So(newKey, ShouldNotEqual, member.Key())

			dgaSoMembershipMoved(d.server, newKey, waiter.Key())

			Convey("And completing the modified member releases the waiter", func() {
				dgaExecuteReserved(ctx, d, jq, newKey)

				So(dgaItemState(d.server, waiter.Key()), ShouldEqual, queue.ItemStateReady)
			})
		})
	})
}

// TestDepGranularityRESTModifyChangesMemberKey covers D2 acceptance test 5: the
// same, driven through the REST path's storeModifiedJobs in-package with a
// modifier that sets neither dependencies nor priority. It is the assertion that
// the hook sits above that path's DependenciesSet || PrioritySet guard rather
// than inside it.
func TestDepGranularityRESTModifyChangesMemberKey(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given a dep group whose only live member has a waiter", t, func() {
		d, jq, member, waiter := dgaOneMemberFixture(ctx)

		defer d.stop(ctx)

		defer disconnect(jq)

		Convey("The REST modify path moves its membership to its new key", func() {
			jm := &JobModifier{}
			jm.SetCmd(dgaRESTModifiedCmd)
			So(jm.DependenciesSet, ShouldBeFalse)
			So(jm.PrioritySet, ShouldBeFalse)

			newKey := dgaStoreModifiedJobs(ctx, d.server, member.Key(), jm)
			So(newKey, ShouldNotEqual, member.Key())

			dgaSoMembershipMoved(d.server, newKey, waiter.Key())

			Convey("And completing the modified member releases the waiter", func() {
				dgaExecuteReserved(ctx, d, jq, newKey)

				So(dgaItemState(d.server, waiter.Key()), ShouldEqual, queue.ItemStateReady)
			})
		})
	})
}

// dgaOneMemberFixture starts a server holding one live member of dgaGroup and one
// live job waiting on that group, returning both jobs, the server and a connected
// client. The caller stops the server and disconnects the client.
func dgaOneMemberFixture(ctx context.Context) (*dgrServer, *Client, *Job, *Job) {
	d := dgrStartServer(ctx)
	jq := d.connect()

	member := dgaMemberJob(d, dgaGroup, dgaSoleName)
	waiter := dgaWaiterJob(d, dgaGroup, dgaSoleName)

	dgrAddJobs(jq, []*Job{member})
	dgrAddJobs(jq, []*Job{waiter})

	So(dgaItemState(d.server, waiter.Key()), ShouldEqual, queue.ItemStateDependent)

	return d, jq, member, waiter
}

// dgaStoreModifiedJobs drives the REST modify path in-package, the technique E6
// uses for getij, so no HTTP harness is needed, and returns the job's new key.
func dgaStoreModifiedJobs(ctx context.Context, s *Server, key string, jm *JobModifier) string {
	jobs, byOldKey := s.editableQueueJobs([]string{key})
	So(jobs, ShouldHaveLength, 1)

	modified, err := jm.Modify(jobs, s)
	So(err, ShouldBeNil)
	So(modified, ShouldHaveLength, 1)

	So(s.storeModifiedJobs(ctx, modified, byOldKey, jm), ShouldBeNil)

	return slices.Collect(maps.Keys(modified))[0]
}

// dgaAddRerun adds jobs the way wr add --rerun does, with ignoreComplete false,
// so a job that is already live is stored again rather than filtered out as a
// duplicate.
func dgaAddRerun(jq *Client, jobs []*Job) {
	_, _, err := jq.Add(jobs, envVars, false)
	So(err, ShouldBeNil)
}

// dgaModify modifies the job through the Go client API, the only way a
// DepGroups modification is reachable (wr mod --dep_grps does not exist), and
// returns its new key.
func dgaModify(jq *Client, job *Job, jm *JobModifier) string {
	modified, err := jq.Modify([]*JobEssence{{JobKey: job.Key()}}, jm)
	So(err, ShouldBeNil)
	So(modified, ShouldHaveLength, 1)

	return slices.Collect(maps.Keys(modified))[0]
}

// dgaSoMembershipMoved asserts that dgaGroup's one live member is newKey, with no
// phantom left behind, and that waiterKey is still waiting for the group: neither
// released at modify time nor wedged.
func dgaSoMembershipMoved(s *Server, newKey, waiterKey string) {
	So(dgaGroupMembers(s.depGroups, dgaGroup), ShouldResemble, []string{newKey})
	So(s.depGroups.hasMembers(dgaGroup), ShouldBeTrue)
	So(s.depGroups.memberships(), ShouldEqual, 1)
	So(dgaItemState(s, waiterKey), ShouldEqual, queue.ItemStateDependent)
}

// dgaDepGroupsModifier returns a modifier that sets only DepGroups, so it sets
// neither dependencies nor priority and never passes either modify path's
// DependenciesSet || PrioritySet guard.
func dgaDepGroupsModifier(depGroups []string) *JobModifier {
	jm := &JobModifier{}
	jm.SetDepGroups(depGroups)

	return jm
}

// dgaExecuteReserved reserves the next ready job, which must be the one named,
// and runs it to completion, which archives it.
func dgaExecuteReserved(ctx context.Context, d *dgrServer, jq *Client, key string) {
	job, err := jq.Reserve(dgrReserveWait)
	So(err, ShouldBeNil)
	So(job, ShouldNotBeNil)
	So(job.Key(), ShouldEqual, key)

	execute(ctx, jq, job, d.config.RunnerExecShell)
}

// dgaMemberJob returns one job belonging to depGroup, named so its command, and
// so its key, is its own.
func dgaMemberJob(d *dgrServer, depGroup, name string) *Job {
	job := d.job("echo dga member "+name, dgaMemberRepGroup)
	job.DepGroups = []string{depGroup}

	return job
}

// dgaWaiterJob returns one job depending on depGroup and belonging to no group of
// its own, so a fixture's whole membership is its members.
func dgaWaiterJob(d *dgrServer, depGroup, name string) *Job {
	job := d.job("echo dga waiter "+name+" "+depGroup, dgaWaiterRepGroup)
	job.Dependencies = Dependencies{NewDepGroupDependency(depGroup)}

	return job
}

// dgaGroupMembers returns the job keys held as depGroup's live members, sorted.
// The server has no public view of them, and "the new key and not the old one"
// is what D2 tests 4 and 5 have to assert.
func dgaGroupMembers(m *depGroupMembers, depGroup string) []string {
	shard := m.groupShard(depGroup)

	shard.mu.Lock()
	defer shard.mu.Unlock()

	return slices.Sorted(maps.Keys(shard.members[depGroup]))
}

// dgaItemState returns the sub-queue this job's queue item is in, which is where
// the client's view of its state comes from.
func dgaItemState(s *Server, key string) queue.ItemState {
	item, err := s.q.Get(key)
	So(err, ShouldBeNil)
	So(item, ShouldNotBeNil)

	return item.Stats().State
}
