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

//nolint:goconst // Test cases repeat lane names to document planner behaviour.
package testsuite

import (
	"context"
	"regexp"
	"slices"
	"testing"

	. "github.com/smartystreets/goconvey/convey"
)

const testModule = "example.com/wr"

func TestPlannerCoversDiscoveredPackages(t *testing.T) {
	Convey("special packages are split and all other packages stay automatic", t, func() {
		packages := []string{
			testModule,
			pkg(testModule, "client"),
			pkg(testModule, "client/testing"),
			pkg(testModule, "cloud"),
			pkg(testModule, "jobqueue"),
			pkg(testModule, "jobqueue/scheduler"),
			pkg(testModule, "newpkg"),
		}

		plan := NewPlan(ModeTest, testModule, packages)
		other := laneNamed(plan, "other")

		So(other.Packages, ShouldResemble, []string{
			testModule,
			pkg(testModule, "client/testing"),
			pkg(testModule, "cloud"),
			pkg(testModule, "newpkg"),
		})
		So(coveredPackages(plan), ShouldResemble, packages)
	})
}

func TestPlannerCoversRacePackagesAutomatically(t *testing.T) {
	Convey("race mode covers every discovered package with the same split package planner", t, func() {
		packages := []string{
			testModule,
			pkg(testModule, "client"),
			pkg(testModule, "cloud"),
			pkg(testModule, "jobqueue"),
			pkg(testModule, "jobqueue/scheduler"),
			pkg(testModule, "newpkg"),
			pkg(testModule, "queue"),
		}

		plan := NewPlan(ModeRace, testModule, packages)

		So(laneNamed(plan, "queue").Race, ShouldBeTrue)
		So(laneNamed(plan, "cloud").Kind, ShouldEqual, LaneKindBinary)
		So(laneNamed(plan, "other").Packages, ShouldResemble, []string{testModule, pkg(testModule, "newpkg")})
		So(coveredPackages(plan), ShouldResemble, packages)
		So(jobqueueLaneSignatures(plan), ShouldResemble, jobqueueLaneSignatures(NewPlan(ModeTest, testModule, packages)))
	})
}

func TestPlannerCompilesSharedRaceRunnerBinary(t *testing.T) {
	Convey("race mode compiles one non-race jobqueue helper for runner subprocesses", t, func() {
		plan := NewPlan(ModeRace, testModule, []string{pkg(testModule, "jobqueue")})

		So(compileNames(plan.Compiles), ShouldResemble, []string{"jobqueue", "jobqueue_runner"})
	})

	Convey("normal test mode reuses the running jobqueue test binary", t, func() {
		plan := NewPlan(ModeTest, testModule, []string{pkg(testModule, "jobqueue")})

		So(compileNames(plan.Compiles), ShouldResemble, []string{"jobqueue"})
	})
}

func TestPlannerCoversJobqueueTestsByExactName(t *testing.T) {
	Convey("known prefix-collision tests are explicit and future tests fall into the default lane", t, func() {
		plan := NewPlan(ModeTest, testModule, []string{pkg(testModule, "jobqueue")})

		So(jobqueueLanesForTest(plan, "TestREST"), ShouldResemble, []string{"jqA1"})
		So(jobqueueLanesForTest(plan, "TestRESTJobModificationEndpoint"), ShouldResemble, []string{"jq_rest_extra"})
		So(jobqueueLanesForTest(plan, "TestServerWebISuspendedStatus"), ShouldResemble, []string{"jq_rest_extra"})
		So(jobqueueLanesForTest(plan, "TestRESTFutureCase"), ShouldResemble, []string{"jq_default"})
	})
}

func TestPlannerPreservesShardLanes(t *testing.T) {
	Convey("tests split by WR_TEST_SHARD still get both shard lanes", t, func() {
		plan := NewPlan(ModeTest, testModule, []string{pkg(testModule, "jobqueue")})

		So(jobqueueLanesForTest(plan, "TestJobqueueSignal"), ShouldResemble, []string{"signal_a", "signal_b"})
		So(laneNamed(plan, "signal_a").Env["WR_TEST_SHARD"], ShouldEqual, "a")
		So(laneNamed(plan, "signal_b").Env["WR_TEST_SHARD"], ShouldEqual, "b")
	})
}

func TestRunnerPassesSharedRunnerBinaryToJobqueueLanes(t *testing.T) {
	Convey("jobqueue binary lanes receive the shared runner helper path", t, func() {
		lane := Lane{Binary: "jobqueue", Env: map[string]string{"WR_TEST_LANE": "1"}}

		env := laneEnvWithBinaries(lane, map[string]string{"jobqueue_runner": "/tmp/wr-runner.test"})

		So(env[envTestRunnerBinary], ShouldEqual, "/tmp/wr-runner.test")
		So(env["WR_TEST_LANE"], ShouldEqual, "1")
		So(lane.Env[envTestRunnerBinary], ShouldBeBlank)
	})

	Convey("other lanes are not given a jobqueue-specific helper path", t, func() {
		lane := Lane{Binary: "client", Env: map[string]string{"WR_TEST_LANE": "2"}}

		env := laneEnvWithBinaries(lane, map[string]string{"jobqueue_runner": "/tmp/wr-runner.test"})

		So(env[envTestRunnerBinary], ShouldBeBlank)
	})
}

func TestPlannerOmitsEmptyOtherLane(t *testing.T) {
	Convey("a plan made only of split packages does not run an accidental root-package lane", t, func() {
		plan := NewPlan(ModeTest, testModule, []string{pkg(testModule, "jobqueue")})

		So(laneNamed(plan, "other").Name, ShouldBeBlank)
		So(coveredPackages(plan), ShouldResemble, []string{pkg(testModule, "jobqueue")})
	})
}

func TestDefaultParallelismIsBounded(t *testing.T) {
	Convey("the default runner cap avoids unbounded integration-lane fan-out", t, func() {
		t.Setenv(envMaxParallel, "")

		So(defaultParallelLimit(minDefaultParallel-1), ShouldEqual, minDefaultParallel-1)
		So(defaultParallelLimit(maxDefaultParallel+20), ShouldBeBetweenOrEqual, minDefaultParallel, maxDefaultParallel)
		So(maxParallel(maxDefaultParallel+20), ShouldEqual, defaultParallelLimit(maxDefaultParallel+20))
	})

	Convey("the default cap scales down on small CI hosts", t, func() {
		So(defaultParallelLimitForCPU(100, 1), ShouldEqual, 6)
		So(defaultParallelLimitForCPU(100, 2), ShouldEqual, 12)
		So(defaultParallelLimitForCPU(100, 4), ShouldEqual, maxDefaultParallel)
		So(defaultParallelLimitForCPU(100, 8), ShouldEqual, maxDefaultParallel)
	})

	Convey("callers can override the cap for profiling", t, func() {
		t.Setenv(envMaxParallel, "7")

		So(maxParallel(maxDefaultParallel+20), ShouldEqual, 7)
	})

	Convey("invalid caller overrides fall back to the default cap", t, func() {
		laneCount := maxDefaultParallel + 20

		t.Setenv(envMaxParallel, "many")
		So(maxParallel(laneCount), ShouldEqual, defaultParallelLimit(laneCount))

		t.Setenv(envMaxParallel, "0")
		So(maxParallel(laneCount), ShouldEqual, defaultParallelLimit(laneCount))

		t.Setenv(envMaxParallel, "-1")
		So(maxParallel(laneCount), ShouldEqual, defaultParallelLimit(laneCount))
	})

	Convey("caller overrides above the lane count clamp to available lanes", t, func() {
		t.Setenv(envMaxParallel, "200")

		So(maxParallel(7), ShouldEqual, 7)
	})
}

func TestCompileParallelismScalesWithCPUs(t *testing.T) {
	Convey("test binaries compile sequentially on one-core hosts", t, func() {
		So(compileParallelismForCPU(4, 1), ShouldEqual, 1)
	})

	Convey("test binaries compile concurrently on multi-core hosts", t, func() {
		So(compileParallelismForCPU(4, 2), ShouldEqual, 2)
		So(compileParallelismForCPU(4, 8), ShouldEqual, 4)
	})
}

func TestPortLaneRangesStayBelowDefaultEphemeralPorts(t *testing.T) {
	Convey("the highest selectable lane range fits below the default Linux ephemeral range", t, func() {
		plan := NewPlan(ModeTest, testModule, []string{
			pkg(testModule, "client"),
			pkg(testModule, "cmd"),
			pkg(testModule, "jobqueue"),
			pkg(testModule, "jobqueue/scheduler"),
		})

		maxLane := maxPlanLane(plan)
		maxBase, err := maxRunPortBase(maxLane, defaultEphemeralStart)
		So(err, ShouldBeNil)

		maxPort := maxBase + ((maxLane + 1) * lanePortSpan)
		So(maxPort, ShouldBeLessThan, defaultEphemeralStart)
	})

	Convey("a caller-provided lane base inside the ephemeral range is rejected", t, func() {
		plan := Plan{Parallel: []Lane{{Env: laneEnv(1)}}}

		err := validateRunPortBaseWithEphemeralStart(context.Background(), plan, defaultEphemeralStart, defaultEphemeralStart)

		So(err, ShouldNotBeNil)
	})
}

func TestRunnerPrioritizesLongLanes(t *testing.T) {
	Convey("long lanes start before short lanes when parallelism is capped", t, func() {
		lanes := prioritizedLanes([]Lane{
			{Name: "jq_payload"},
			{Name: "client_wait"},
			{Name: "other"},
			{Name: "cmd_add"},
			{Name: "runner_lost_jobs"},
		})

		So(laneNames(lanes), ShouldResemble, []string{
			"runner_lost_jobs",
			"other",
			"client_wait",
			"cmd_add",
			"jq_payload",
		})
	})
}

func laneNamed(plan Plan, name string) Lane {
	for _, lane := range allLanes(plan) {
		if lane.Name == name {
			return lane
		}
	}

	return Lane{}
}

func laneNames(lanes []Lane) []string {
	names := make([]string, 0, len(lanes))

	for _, lane := range lanes {
		names = append(names, lane.Name)
	}

	return names
}

func compileNames(compiles []Compile) []string {
	names := make([]string, 0, len(compiles))

	for _, compile := range compiles {
		names = append(names, compile.Name)
	}

	return names
}

func allLanes(plan Plan) []Lane {
	lanes := make([]Lane, 0, len(plan.Serial)+len(plan.Parallel))
	lanes = append(lanes, plan.Serial...)
	lanes = append(lanes, plan.Parallel...)

	return lanes
}

func coveredPackages(plan Plan) []string {
	seen := make(map[string]bool)
	covered := make([]string, 0)

	for _, lane := range allLanes(plan) {
		if lane.Package != "" && !seen[lane.Package] {
			covered = append(covered, lane.Package)
			seen[lane.Package] = true
		}

		for _, packageName := range lane.Packages {
			if !seen[packageName] {
				covered = append(covered, packageName)
				seen[packageName] = true
			}
		}
	}

	slices.Sort(covered)

	return covered
}

func jobqueueLanesForTest(plan Plan, testName string) []string {
	names := make([]string, 0)

	for _, lane := range allLanes(plan) {
		if lane.Package == pkg(testModule, "jobqueue") && laneWouldRunTest(lane, testName) {
			names = append(names, lane.Name)
		}
	}

	return names
}

func laneWouldRunTest(lane Lane, testName string) bool {
	if lane.RunPattern != "" {
		return regexp.MustCompile(lane.RunPattern).MatchString(testName)
	}

	if lane.SkipPattern != "" {
		return !regexp.MustCompile(lane.SkipPattern).MatchString(testName)
	}

	return true
}

func jobqueueLaneSignatures(plan Plan) []string {
	signatures := make([]string, 0)

	for _, lane := range allLanes(plan) {
		if lane.Package == pkg(testModule, "jobqueue") {
			signatures = append(signatures, lane.Name+"|"+lane.RunPattern+"|"+lane.SkipPattern)
		}
	}

	return signatures
}
