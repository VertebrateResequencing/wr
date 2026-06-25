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

//nolint:goconst // Repeated package and lane names keep the suite plan tables auditable.
package testsuite

import (
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

const (
	defaultTimeout = "40m"
	laneOther      = 13
)

// Mode is the test-suite mode to run.
type Mode string

const (
	// ModeTest runs the normal test suite.
	ModeTest Mode = "test"
	// ModeRace runs the race-enabled test suite.
	ModeRace Mode = "race"
)

const (
	// LaneKindBinary runs a compiled Go test binary.
	LaneKindBinary LaneKind = "binary"
	// LaneKindGoTest runs go test over one or more packages.
	LaneKindGoTest LaneKind = "go-test"
)

// UnknownModeError reports an unsupported test-suite mode.
type UnknownModeError string

func (e UnknownModeError) Error() string {
	return fmt.Sprintf("unknown test suite mode %q", string(e))
}

// ParseMode turns a command-line mode into a supported Mode.
func ParseMode(raw string) (Mode, error) {
	switch Mode(raw) {
	case ModeTest:
		return ModeTest, nil
	case ModeRace:
		return ModeRace, nil
	default:
		return "", UnknownModeError(raw)
	}
}

// Plan is the complete set of work needed for one test-suite mode.
type Plan struct {
	Mode     Mode
	Compiles []Compile
	Serial   []Lane
	Parallel []Lane
}

// Compile describes one compiled test binary needed by split lanes.
type Compile struct {
	Name    string
	Package string
	Race    bool
}

// LaneKind describes how a Lane is executed.
type LaneKind string

// Lane describes one independently logged test-suite lane.
type Lane struct {
	Name        string
	Kind        LaneKind
	Package     string
	Packages    []string
	Dir         string
	Binary      string
	RunPattern  string
	SkipPattern string
	Env         map[string]string
	Nice        bool
	Race        bool
	Parallelism int
}

// NewPlan creates a test-suite execution plan from discovered module packages.
func NewPlan(mode Mode, module string, packages []string) Plan {
	plan := Plan{Mode: mode}
	excluded := specialPackages(module, mode)

	plan.Compiles = compilePlan(module, packages, mode)
	plan.Serial = serialLanes(module, packages, mode)
	plan.Parallel = append(plan.Parallel, splitPackageLanes(module, packages, mode)...)

	if other := otherLane(mode, packages, excluded); len(other.Packages) > 0 {
		plan.Parallel = append(plan.Parallel, other)
	}

	return plan
}

func specialPackages(module string, mode Mode) map[string]bool {
	excluded := map[string]bool{
		pkg(module, "client"):             true,
		pkg(module, "cmd"):                true,
		pkg(module, "jobqueue"):           true,
		pkg(module, "jobqueue/scheduler"): true,
	}

	if mode == ModeRace {
		excluded[pkg(module, "cloud")] = true
		excluded[pkg(module, "queue")] = true
	}

	return excluded
}

func compilePlan(module string, packages []string, mode Mode) []Compile {
	specs := []Compile{
		{Name: "jobqueue", Package: pkg(module, "jobqueue"), Race: mode == ModeRace},
		{Name: "client", Package: pkg(module, "client"), Race: mode == ModeRace},
		{Name: "cmd", Package: pkg(module, "cmd"), Race: mode == ModeRace},
		{Name: "scheduler", Package: pkg(module, "jobqueue/scheduler"), Race: mode == ModeRace},
	}

	if mode == ModeRace {
		specs = append(specs, Compile{Name: "cloud", Package: pkg(module, "cloud"), Race: true})
		specs = append(specs, Compile{Name: "jobqueue_runner", Package: pkg(module, "jobqueue"), Race: false})
	}

	return keepExistingCompiles(specs, packages)
}

func keepExistingCompiles(specs []Compile, packages []string) []Compile {
	known := packageSet(packages)
	kept := make([]Compile, 0, len(specs))

	for _, spec := range specs {
		if known[spec.Package] {
			kept = append(kept, spec)
		}
	}

	return kept
}

func serialLanes(module string, packages []string, mode Mode) []Lane {
	if mode != ModeRace || !packageSet(packages)[pkg(module, "queue")] {
		return nil
	}

	return []Lane{{
		Name:     "queue",
		Kind:     LaneKindGoTest,
		Packages: []string{pkg(module, "queue")},
		Race:     true,
	}}
}

func splitPackageLanes(module string, packages []string, mode Mode) []Lane {
	known := packageSet(packages)
	lanes := make([]Lane, 0, 32)

	if known[pkg(module, "jobqueue")] {
		lanes = append(lanes, jobqueueLanes(module)...)
	}

	if known[pkg(module, "client")] {
		lanes = append(lanes, clientLanes(module)...)
	}

	if known[pkg(module, "cmd")] {
		lanes = append(lanes, cmdLanes(module)...)
	}

	if known[pkg(module, "jobqueue/scheduler")] {
		lanes = append(lanes, schedulerLane(module))
	}

	if mode == ModeRace && known[pkg(module, "cloud")] {
		lanes = append(lanes, cloudLane(module))
	}

	return lanes
}

func otherLane(mode Mode, packages []string, excluded map[string]bool) Lane {
	return Lane{
		Name:        "other",
		Kind:        LaneKindGoTest,
		Packages:    otherPackages(packages, excluded),
		Env:         laneEnv(laneOther),
		Race:        mode == ModeRace,
		Parallelism: 4,
	}
}

func jobqueueLanes(module string) []Lane {
	lanes := make([]Lane, 0, 32)
	explicit := make([]string, 0, 80)

	for _, config := range jobqueueRunLaneConfigs() {
		lanes = append(lanes, jobqueueRunLane(module, config))
		explicit = append(explicit, config.tests...)
	}

	lanes = append(lanes, Lane{
		Name:        "jq_default",
		Kind:        LaneKindBinary,
		Package:     pkg(module, "jobqueue"),
		Dir:         "jobqueue",
		Binary:      "jobqueue",
		SkipPattern: exactTests(uniqueTests(explicit)...),
		Env:         laneEnv(9),
	})

	return lanes
}

type jobqueueRunLaneConfig struct {
	name  string
	lane  int
	tests []string
	shard string
}

func jobqueueRunLaneConfigs() []jobqueueRunLaneConfig {
	return []jobqueueRunLaneConfig{
		jqConfig("runner_lost_jobs", 0, "TestJobqueueRunnerModeEntrypoint", "TestJobqueueRunnerLostJobs"),
		jqShardConfig("runner_scheduling_a", 1, "a", "TestJobqueueRunnerScheduling"),
		jqShardConfig("signal_a", 2, "a", "TestJobqueueSignal"),
		jqConfig("production", 3, "TestJobqueueProduction"),
		jqShardConfig("jq_execution_retries", 4, "a", "TestJobqueueExecutionAndDependencyScenarios"),
		jqShardConfig("modify_a", 5, "a", "TestJobqueueModify"),
		jqConfig(
			"jqA1",
			6,
			"TestJobqueueLimitGroups",
			"TestREST",
			"TestJobqueueHighMem",
			"TestJobqueueUtils",
			"TestJobqueueModules",
		),
		jqConfig("server_webi", 7, "TestServerWebI"),
		jqConfig("jobqueue_basics", 8, "TestJobqueueBasics"),
		jqConfig("job_subscriptions", 26, "TestJobSubscriptions"),
		jqConfig("subscription_catchup", 27, "TestSubscriptionCatchUp", "TestSubscriptionReconnectResync"),
		jqConfig(
			"subscription_teardown",
			28,
			"TestSubscriptionTeardown",
			"TestSubscriptionAtLeastOnceDedup",
			"TestSubscriptionStateChangeEvents",
		),
		jqConfig("mock", 10, "TestJobqueueMockRunner"),
		jqShardConfig("signal_b", 14, "b", "TestJobqueueSignal"),
		jqShardConfig("jq_repgroup_dependencies", 15, "b", "TestJobqueueExecutionAndDependencyScenarios"),
		jqShardConfig("modify_b", 16, "b", "TestJobqueueModify"),
		jqShardConfig("runner_scheduling_b", 37, "b", "TestJobqueueRunnerScheduling"),
		jqShardConfig("jq_execution_details", 38, "c", "TestJobqueueExecutionAndDependencyScenarios"),
		jqConfig("runner_resource_learning", 39, "TestJobqueueRunnerResourceLearning"),
		jqConfig("runner_kill_requests", 40, "TestJobqueueRunnerKillRequests"),
		jqConfig("runner_auto_execution", 41, "TestJobqueueRunnerAutomaticExecution"),
		jqConfig("runner_failure_retry", 42, "TestJobqueueRunnerFailureRetry"),
		jqConfig(
			"jq_payload",
			18,
			"TestExecuteLiveStateSnapshots",
			"TestClientLifecycleRequestsTrimJobPayload",
			"TestClientTouchSendsLiveEndState",
			"TestClientSuspendResumeRequests",
			"TestServerRejectsKeyOnlyStartedRequest",
			"TestClientExecuteLiveTouchPayloads",
		),
		jqConfig("jq_sub_add", 19, "TestLiveJobUpdateCwd", "TestClientAddAndWait"),
		jqConfig(
			"jq_sub_live",
			20,
			"TestLiveJobSubscriptions",
			"TestSubscriptionBoundedIsolatedBuffer",
			"TestSubscriptionAuthorization",
		),
		jqConfig(
			"jq_sub_long",
			21,
			"TestSubscriptionLongPollOverExistingPort",
			"TestSubscriptionPerKeyTerminalEvents",
		),
		jqConfig("jq_sub_aggregate", 22, "TestSubscriptionRepGroupAggregate"),
		jqConfig(
			"jq_rest_extra",
			23,
			"TestRESTHTTPClientReuse",
			"TestRESTHTTPClientTimeout",
			"TestRESTURLUsesConnectedHost",
			"TestRESTTLSConfigCAPool",
			"TestRESTJobModificationEndpoint",
			"TestRESTJobModificationValidation",
			"TestRESTWaitingDepGroups",
			"TestServerWebISuspendedStatus",
		),
		jqConfig(
			"jq_dependency",
			24,
			"TestRerunDependentJobWaitsOnIncompleteDependencies",
			"TestSeenCompletedDepGroupsDoNotBlock",
			"TestNeverSeenDepGroupsWait",
			"TestAddWarnings",
			"TestGetIncompleteWaitingForDepGroups",
			"TestSameBatchAndLiveDepGroupReblocking",
			"TestCommandDependenciesStayStatic",
			"TestRerunReplacementReadyCallbackBlocksReserve",
		),
		jqConfig(
			"jq_status",
			25,
			"TestCaster",
			"TestStatusDetailsLiveCompatibility",
			"TestStatusDetailsLiveFields",
			"TestStatusDetailsLivePushUpdates",
			"TestStatusWSDetailsSubscriptionRace",
			"TestWebUIModificationStaticContract",
			"TestStatusPageLiveIntrospectionAssets",
			"TestStatusPageLivePushUpdateBehaviour",
			"TestRepGroupStatusCountsDoNotDecodeCompleteJobs",
			"TestClientRepGroupStatusCounts",
			"TestClientRepGroupStatusCountsIncludeSuspended",
		),
	}
}

func jqConfig(name string, lane int, tests ...string) jobqueueRunLaneConfig {
	return jobqueueRunLaneConfig{name: name, lane: lane, tests: tests}
}

func jqShardConfig(name string, lane int, shard string, test string) jobqueueRunLaneConfig {
	return jobqueueRunLaneConfig{name: name, lane: lane, tests: []string{test}, shard: shard}
}

func jobqueueRunLane(module string, config jobqueueRunLaneConfig) Lane {
	env := laneEnv(config.lane)

	if config.shard != "" {
		env["WR_TEST_SHARD"] = config.shard
	}

	return Lane{
		Name:       config.name,
		Kind:       LaneKindBinary,
		Package:    pkg(module, "jobqueue"),
		Dir:        "jobqueue",
		Binary:     "jobqueue",
		RunPattern: exactTests(config.tests...),
		Env:        env,
	}
}

func clientLanes(module string) []Lane {
	client := pkg(module, "client")
	explicit := []string{
		"TestScheduler",
		"TestSchedulerGetJobByKey",
		"TestSchedulerSubmitJobsAndReturnIDs",
		"TestSchedulerNewJobFromJSON",
		"TestSchedulerWaitForRunning",
		"TestSchedulerSubmitJobsOptions",
		"TestSchedulerSubmitJobsAndWait",
		"TestSchedulerWaitForJobs",
		"TestFakeScheduler",
		"TestPretendGetIncompleteByRepGroupEmptyRepGroup",
		"TestPretendGetByRepGroupEmptyRepGroup",
		"TestSchedulerPretendNewMethods",
		"TestSchedulerCompatibility",
	}

	return []Lane{
		{
			Name:       "client_a",
			Kind:       LaneKindBinary,
			Package:    client,
			Dir:        "client",
			Binary:     "client",
			RunPattern: exactTests("TestScheduler"),
			Env:        laneEnv(12),
		},
		{
			Name:    "client_wait",
			Kind:    LaneKindBinary,
			Package: client,
			Dir:     "client",
			Binary:  "client",
			RunPattern: exactTests(
				"TestSchedulerWaitForRunning",
				"TestSchedulerSubmitJobsAndWait",
			),
			Env: laneEnv(32),
		},
		{
			Name:       "client_wait_jobs",
			Kind:       LaneKindBinary,
			Package:    client,
			Dir:        "client",
			Binary:     "client",
			RunPattern: exactTests("TestSchedulerWaitForJobs"),
			Env:        laneEnv(33),
		},
		{
			Name:    "client_basics",
			Kind:    LaneKindBinary,
			Package: client,
			Dir:     "client",
			Binary:  "client",
			RunPattern: exactTests(
				"TestSchedulerGetJobByKey",
				"TestSchedulerSubmitJobsAndReturnIDs",
				"TestSchedulerNewJobFromJSON",
				"TestSchedulerSubmitJobsOptions",
				"TestFakeScheduler",
				"TestPretendGetIncompleteByRepGroupEmptyRepGroup",
				"TestPretendGetByRepGroupEmptyRepGroup",
				"TestSchedulerPretendNewMethods",
				"TestSchedulerCompatibility",
			),
			Env: laneEnv(34),
		},
		{
			Name:        "client_default",
			Kind:        LaneKindBinary,
			Package:     client,
			Dir:         "client",
			Binary:      "client",
			SkipPattern: exactTests(explicit...),
			Env:         laneEnv(17),
		},
	}
}

func cmdLanes(module string) []Lane {
	cmdPkg := pkg(module, "cmd")
	lanes := make([]Lane, 0, 8)
	explicit := make([]string, 0, 40)

	for _, config := range cmdRunLaneConfigs() {
		lanes = append(lanes, cmdRunLane(module, config))
		explicit = append(explicit, config.tests...)
	}

	lanes = append(lanes, Lane{
		Name:        "cmd_default",
		Kind:        LaneKindBinary,
		Package:     cmdPkg,
		Dir:         "cmd",
		Binary:      "cmd",
		SkipPattern: exactTests(uniqueTests(explicit)...),
		Env:         laneEnv(31),
	})

	return lanes
}

type cmdRunLaneConfig struct {
	name  string
	lane  int
	tests []string
}

func cmdRunLaneConfigs() []cmdRunLaneConfig {
	return []cmdRunLaneConfig{
		cmdConfig(
			"cmd_add",
			29,
			"TestAddQueuesAvoidDefault",
			"TestSynchronousAddDoesNotUsePollingHelper",
			"TestAddRemoteSameAsLocal",
			"TestSynchronousAddPrintsWarningsBeforeWaiting",
			"TestWaitForSynchronousJobReportsMissingTerminalJob",
			"TestAddHelpDocumentsDependencySemantics",
			"TestChangelogDocumentsDepGroupSemantics",
			"TestAddRemoteSameAsLocalCwdDefault",
			"TestAddCommandDependenciesDoNotWarnForMissingTargets",
			"TestAddWarnsForNeverSeenDepGroups",
			"TestAddDoesNotWarnForSeenDepGroups",
			"TestAddHeadKeepsFirstParsedCommands",
			"TestAddHeadZeroKeepsAllParsedCommands",
			"TestSynchronousAddPrintsStdoutAndExitsZero",
			"TestSynchronousAddExitsWithBuriedJobExitCode",
		),
		cmdConfig("cmd_resume", 30, "TestResumeCommand"),
		cmdConfig("cmd_suspend", 35, "TestSuspendCommand"),
		cmdConfig(
			"cmd_status",
			36,
			"TestLSFBjobsShowsSuspendedAsPending",
			"TestStatusTableUnicodeCells",
			"TestStatusTableFormatErrors",
			"TestStatusTableRows",
			"TestStatusLimitHelp",
			"TestStatusTableOutputHelp",
			"TestStatusPlainOutputHelp",
			"TestStatusOutputRetrievalNeeds",
			"TestStatusAlertTimeFormatting",
			"TestStatusHelpDocumentsMissingDepsFilter",
			"TestStatusSchedulerAlertsFooter",
			"TestStatusSchedulerAlertsFooterOutputModes",
			"TestStatusFiltersPendingAndDependentJobs",
			"TestStatusFiltersMissingDepGroups",
			"TestStatusDisplaysMissingDepGroups",
			"TestStatusShowsAndFiltersSuspendedJobs",
			"TestStatusSuspendedFilterValidation",
			"TestStatusTableOutput",
		),
	}
}

func cmdConfig(name string, lane int, tests ...string) cmdRunLaneConfig {
	return cmdRunLaneConfig{name: name, lane: lane, tests: tests}
}

func cmdRunLane(module string, config cmdRunLaneConfig) Lane {
	return Lane{
		Name:       config.name,
		Kind:       LaneKindBinary,
		Package:    pkg(module, "cmd"),
		Dir:        "cmd",
		Binary:     "cmd",
		RunPattern: exactTests(config.tests...),
		Env:        laneEnv(config.lane),
	}
}

func schedulerLane(module string) Lane {
	return Lane{
		Name:    "scheduler",
		Kind:    LaneKindBinary,
		Package: pkg(module, "jobqueue/scheduler"),
		Dir:     "jobqueue/scheduler",
		Binary:  "scheduler",
		Env:     conveyEnv(),
	}
}

func cloudLane(module string) Lane {
	return Lane{
		Name:    "cloud",
		Kind:    LaneKindBinary,
		Package: pkg(module, "cloud"),
		Dir:     "cloud",
		Binary:  "cloud",
		Env:     conveyEnv(),
	}
}

func otherPackages(packages []string, excluded map[string]bool) []string {
	other := make([]string, 0, len(packages))

	for _, packageName := range packages {
		if !excluded[packageName] {
			other = append(other, packageName)
		}
	}

	return other
}

func exactTests(tests ...string) string {
	quoted := make([]string, 0, len(tests))

	for _, test := range tests {
		quoted = append(quoted, regexp.QuoteMeta(test))
	}

	return "^(" + strings.Join(quoted, "|") + ")$"
}

func uniqueTests(tests []string) []string {
	seen := make(map[string]bool, len(tests))
	unique := make([]string, 0, len(tests))

	for _, test := range tests {
		if !seen[test] {
			seen[test] = true
			unique = append(unique, test)
		}
	}

	slices.Sort(unique)

	return unique
}

func packageSet(packages []string) map[string]bool {
	set := make(map[string]bool, len(packages))

	for _, packageName := range packages {
		set[packageName] = true
	}

	return set
}

func laneEnv(lane int) map[string]string {
	env := conveyEnv()
	env["WR_TEST_LANE"] = strconv.Itoa(lane)

	return env
}

func conveyEnv() map[string]string {
	return map[string]string{"GOCONVEY_REPORTER": "json"}
}

func pkg(module string, relative string) string {
	if relative == "" {
		return module
	}

	return module + "/" + relative
}
