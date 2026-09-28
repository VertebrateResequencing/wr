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
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sync"
	"testing"
	"time"

	. "github.com/smartystreets/goconvey/convey"
)

const testModule = "example.com/wr"

func TestGoTestLaneHonoursRunAndSkipPatterns(t *testing.T) {
	Convey("go-test lanes pass run and skip filters to go test", t, func() {
		lane := Lane{
			Kind:        LaneKindGoTest,
			Packages:    []string{pkg(testModule, "cloud")},
			RunPattern:  exactTests("TestOpenStack"),
			SkipPattern: exactTests("TestOther"),
			Race:        true,
			Parallelism: 2,
		}

		So(goTestArgs(lane), ShouldResemble, []string{
			"test", "-tags", "netgo", "-timeout", defaultTimeout, "--count", "1", "-failfast", "-v",
			"-race", "-run", "^(TestOpenStack)$", "-skip", "^(TestOther)$", "-p", "2",
			pkg(testModule, "cloud"),
		})
	})
}

func TestRunnerStartsLanesInPriorityOrder(t *testing.T) {
	Convey("work capped to one at a time runs in the order it was given", t, func() {
		const (
			items  = 45
			limit  = 1
			rounds = 50
		)

		want := make([]int, items)
		for i := range want {
			want[i] = i
		}

		inOrder := 0

		for range rounds {
			var mu sync.Mutex

			started := make([]int, 0, items)

			runInOrder(items, limit, func(index int) {
				mu.Lock()
				defer mu.Unlock()

				started = append(started, index)
			})

			if slices.Equal(started, want) {
				inOrder++
			}
		}

		So(inOrder, ShouldEqual, rounds)
	})

	Convey("capped parallel work reaches the cap and never runs more than it at once", t, func() {
		const (
			items       = 45
			limit       = 4
			capTimeout  = 30 * time.Second
			excessGrace = 50 * time.Millisecond
		)

		var (
			mu      sync.Mutex
			running int
			peak    int
			ran     int
		)

		// each call waits here until limit calls are running at once, so the cap
		// is reached however the calls are scheduled; a pool that never runs
		// limit at once lets them go on after capTimeout, failing the test. The
		// calls are held for excessGrace more once the cap is reached, so that a
		// pool running more than limit at once has time to show it. A correct
		// pool cannot fail because of either wait.
		capReached := make(chan struct{})
		releaseOnce := sync.Once{}

		timeout, cancel := context.WithTimeout(context.Background(), capTimeout)
		defer cancel()

		runInOrder(items, limit, func(int) {
			mu.Lock()
			running++
			ran++
			peak = max(peak, running)

			if running == limit {
				releaseOnce.Do(func() { time.AfterFunc(excessGrace, func() { close(capReached) }) })
			}
			mu.Unlock()

			select {
			case <-capReached:
			case <-timeout.Done():
			}

			mu.Lock()
			running--
			mu.Unlock()
		})

		So(ran, ShouldEqual, items)
		So(peak, ShouldBeLessThanOrEqualTo, limit)
		So(peak, ShouldEqual, limit)
	})
}

func TestPlannerCoversDiscoveredPackages(t *testing.T) {
	Convey("special packages are split and all other packages stay automatic", t, func() {
		disableLiveIntegrationEnv(t)

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
		disableLiveIntegrationEnv(t)

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
		disableLiveIntegrationEnv(t)

		plan := NewPlan(ModeRace, testModule, []string{pkg(testModule, "jobqueue")})

		So(compileNames(plan.Compiles), ShouldResemble, []string{"jobqueue", "jobqueue_runner"})
	})

	Convey("normal test mode reuses the running jobqueue test binary", t, func() {
		disableLiveIntegrationEnv(t)

		plan := NewPlan(ModeTest, testModule, []string{pkg(testModule, "jobqueue")})

		So(compileNames(plan.Compiles), ShouldResemble, []string{"jobqueue"})
	})
}

func TestPlannerCoversJobqueueTestsByExactName(t *testing.T) {
	Convey("known prefix-collision tests are explicit and future tests fall into the default lane", t, func() {
		disableLiveIntegrationEnv(t)

		plan := NewPlan(ModeTest, testModule, []string{pkg(testModule, "jobqueue")})

		So(jobqueueLanesForTest(plan, "TestREST"), ShouldResemble, []string{"jqA1"})
		So(jobqueueLanesForTest(plan, "TestRESTJobModificationEndpoint"), ShouldResemble, []string{"jq_rest_extra"})
		So(jobqueueLanesForTest(plan, "TestServerWebISuspendedStatus"), ShouldResemble, []string{"jq_rest_extra"})
		So(jobqueueLanesForTest(plan, "TestRESTFutureCase"), ShouldResemble, []string{"jq_default_l_r"})
	})
}

func TestPlannerSplitsJobqueueTestFamiliesFromTheDefaultLane(t *testing.T) {
	Convey("each large jobqueue test family gets its own lane, and every test runs in exactly one lane", t, func() {
		disableLiveIntegrationEnv(t)

		plan := NewPlan(ModeTest, testModule, []string{pkg(testModule, "jobqueue")})

		So(jobqueueLanesForTest(plan, "TestReliable2FutureCase"), ShouldResemble, []string{"jq_reliable2"})
		So(jobqueueLanesForTest(plan, "TestReliable4FutureCase"), ShouldResemble, []string{"jq_reliable4"})
		So(jobqueueLanesForTest(plan, "TestDepGranularityFutureCase"), ShouldResemble, []string{"jq_dep_granularity"})
		So(jobqueueLanesForTest(plan, "TestReliable3FutureCase"), ShouldResemble, []string{"jq_default_l_r"})
		So(jobqueueLanesForTest(plan, "TestFutureCase"), ShouldResemble, []string{"jq_default_a_k"})

		for _, config := range jobqueueRunLaneConfigs() {
			if config.shard != "" {
				continue
			}

			for _, test := range config.tests {
				So(jobqueueLanesForTest(plan, test), ShouldResemble, []string{config.name})
			}
		}
	})
}

func TestPlannerSplitsTheDefaultJobqueueLaneByName(t *testing.T) {
	Convey("leftover jobqueue tests split by the letter after Test, and jq_default takes the rest", t, func() {
		disableLiveIntegrationEnv(t)

		plan := NewPlan(ModeTest, testModule, []string{pkg(testModule, "jobqueue")})

		for test, lane := range map[string]string{
			"TestAFutureCase": "jq_default_a_k",
			"TestKFutureCase": "jq_default_a_k",
			"TestLFutureCase": "jq_default_l_r",
			"TestRFutureCase": "jq_default_l_r",
			"TestSFutureCase": "jq_default",
			"TestZFutureCase": "jq_default",
			"Test_FutureCase": "jq_default",
			"Test9FutureCase": "jq_default",
		} {
			So(jobqueueLanesForTest(plan, test), ShouldResemble, []string{lane})
		}

		So(jobqueueLanesForTest(plan, "TestCaster"), ShouldResemble, []string{"jq_status"})
		So(jobqueueLanesForTest(plan, "TestReliable2FutureCase"), ShouldResemble, []string{"jq_reliable2"})
		So(jobqueueLanesForTest(plan, "TestSubscriptionCatchUp"), ShouldResemble, []string{"subscription_catchup"})
	})
}

func TestPlannerPreservesShardLanes(t *testing.T) {
	Convey("tests split by WR_TEST_SHARD still get both shard lanes", t, func() {
		disableLiveIntegrationEnv(t)

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

func TestPlannerSerializesLiveOpenStackTestsWhenConfigured(t *testing.T) {
	packages := []string{
		pkg(testModule, "cloud"),
		pkg(testModule, "jobqueue"),
		pkg(testModule, "jobqueue/scheduler"),
	}

	Convey("live OpenStack test functions move to serial lanes and stay covered", t, func() {
		disableLiveS3MountEnv(t)
		enableLiveOpenStackEnv(t)

		plan := NewPlan(ModeTest, testModule, packages)

		So(lanesForTestIn(plan.Serial, pkg(testModule, "cloud"), "TestOpenStack"),
			ShouldResemble, []string{"cloud_openstack"})
		So(lanesForTestIn(plan.Serial, pkg(testModule, "jobqueue/scheduler"), "TestOpenstack"),
			ShouldResemble, []string{"scheduler_openstack"})
		So(lanesForTestIn(plan.Serial, pkg(testModule, "jobqueue"), "TestJobqueueWithOpenStack"),
			ShouldResemble, []string{"jobqueue_openstack"})
		So(lanesForTestIn(plan.Parallel, pkg(testModule, "cloud"), "TestOpenStack"), ShouldBeEmpty)
		So(lanesForTestIn(plan.Parallel, pkg(testModule, "jobqueue/scheduler"), "TestOpenstack"), ShouldBeEmpty)
		So(lanesForTestIn(plan.Parallel, pkg(testModule, "jobqueue"), "TestJobqueueWithOpenStack"), ShouldBeEmpty)
		So(coveredPackages(plan), ShouldResemble, packages)
	})

	Convey("without the live OpenStack environment the normal parallel plan is unchanged", t, func() {
		disableLiveIntegrationEnv(t)

		plan := NewPlan(ModeTest, testModule, packages)

		So(plan.Serial, ShouldBeEmpty)
		So(lanesForTestIn(plan.Parallel, pkg(testModule, "cloud"), "TestOpenStack"), ShouldResemble, []string{"other"})
		So(lanesForTestIn(plan.Parallel, pkg(testModule, "jobqueue/scheduler"), "TestOpenstack"),
			ShouldResemble, []string{"scheduler"})
		So(lanesForTestIn(plan.Parallel, pkg(testModule, "jobqueue"), "TestJobqueueWithOpenStack"),
			ShouldResemble, []string{"jq_default_a_k"})
		So(coveredPackages(plan), ShouldResemble, packages)
	})
}

func TestPlannerSerializesLiveS3MountTestsWhenConfigured(t *testing.T) {
	packages := []string{pkg(testModule, "jobqueue")}

	Convey("live S3 mount tests move to a serial lane and stay covered", t, func() {
		enableLiveS3MountEnv(t)
		disableLiveOpenStackEnv(t)

		plan := NewPlan(ModeTest, testModule, packages)

		So(lanesForTestIn(plan.Serial, pkg(testModule, "jobqueue"), "TestJobqueueWithMounts"),
			ShouldResemble, []string{"jobqueue_mounts"})
		So(lanesForTestIn(plan.Parallel, pkg(testModule, "jobqueue"), "TestJobqueueWithMounts"), ShouldBeEmpty)
		So(coveredPackages(plan), ShouldResemble, packages)
	})

	Convey("without live S3 mount prerequisites the normal parallel plan is unchanged", t, func() {
		disableLiveIntegrationEnv(t)

		plan := NewPlan(ModeTest, testModule, packages)

		So(lanesForTestIn(plan.Serial, pkg(testModule, "jobqueue"), "TestJobqueueWithMounts"), ShouldBeEmpty)
		So(lanesForTestIn(plan.Parallel, pkg(testModule, "jobqueue"), "TestJobqueueWithMounts"),
			ShouldResemble, []string{"jq_default_a_k"})
		So(coveredPackages(plan), ShouldResemble, packages)
	})
}

func enableLiveS3MountEnv(t *testing.T) {
	t.Helper()

	home := t.TempDir()

	t.Setenv(envS3MountPath, "s3://wr-test-bucket")
	t.Setenv("HOME", home)

	So(os.WriteFile(filepath.Join(home, s3ConfigFile), []byte("[default]\n"), 0600), ShouldBeNil)
}

func enableLiveOpenStackEnv(t *testing.T) {
	t.Helper()

	t.Setenv(envOpenStackPrefix, "prefix")
	t.Setenv(envOpenStackUsername, "openstack-user")
	t.Setenv(envOpenStackLocalUsername, "local-user")
	t.Setenv(envOpenStackFlavorRegex, "tiny")
}

func lanesForTestIn(lanes []Lane, packageName string, testName string) []string {
	names := make([]string, 0)

	for _, lane := range lanes {
		if laneCoversPackage(lane, packageName) && laneWouldRunTest(lane, testName) {
			names = append(names, lane.Name)
		}
	}

	return names
}

func laneCoversPackage(lane Lane, packageName string) bool {
	return lane.Package == packageName || slices.Contains(lane.Packages, packageName)
}

func TestPlannerOmitsEmptyOtherLane(t *testing.T) {
	Convey("a plan made only of split packages does not run an accidental root-package lane", t, func() {
		disableLiveIntegrationEnv(t)

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
		disableLiveIntegrationEnv(t)

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

func disableLiveIntegrationEnv(t *testing.T) {
	t.Helper()

	disableLiveOpenStackEnv(t)
	disableLiveS3MountEnv(t)
}

func disableLiveOpenStackEnv(t *testing.T) {
	t.Helper()

	for _, key := range []string{
		envOpenStackPrefix,
		envOpenStackUsername,
		envOpenStackLocalUsername,
		envOpenStackFlavorRegex,
	} {
		t.Setenv(key, "")
	}
}

func disableLiveS3MountEnv(t *testing.T) {
	t.Helper()

	t.Setenv(envS3MountPath, "")
	t.Setenv("HOME", t.TempDir())
}

func TestRunnerPrioritizesLongLanes(t *testing.T) {
	Convey("long lanes start before short lanes when parallelism is capped", t, func() {
		lanes := prioritizedLanes([]Lane{
			{Name: "unlisted"},
			{Name: "client_wait"},
			{Name: "other"},
			{Name: "cmd_default"},
			{Name: "cmd_add"},
			{Name: "jq_default"},
			{Name: "jq_reliable4"},
		})

		So(laneNames(lanes), ShouldResemble, []string{
			"jq_reliable4",
			"jq_default",
			"other",
			"cmd_default",
			"client_wait",
			"cmd_add",
			"unlisted",
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
	if lane.RunPattern != "" && !regexp.MustCompile(lane.RunPattern).MatchString(testName) {
		return false
	}

	if lane.SkipPattern != "" && regexp.MustCompile(lane.SkipPattern).MatchString(testName) {
		return false
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
