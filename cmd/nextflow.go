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

package cmd

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/VertebrateResequencing/wr/jobqueue"
	"github.com/VertebrateResequencing/wr/nextflowdsl"
	"github.com/spf13/cobra"
)

const (
	nextflowDefaultPollInterval       = 5 * time.Second
	nfStdoutFile                      = ".nf-stdout"
	nfStderrFile                      = ".nf-stderr"
	nfOutputMaxBytes            int64 = 1 << 20
)

var nextflowSleep = time.Sleep

type nextflowRunOptions struct {
	configPath          string
	paramsFilePath      string
	paramAssignments    []string
	runID               string
	containerRuntime    string
	containerRuntimeSet bool
	follow              bool
	pollInterval        time.Duration
	profile             string
}

type nextflowStatusOptions struct {
	runID    string
	workflow string
	Output   bool
}

func normalizeNextflowContainerRuntime(runtime string) (string, error) {
	switch runtime {
	case "", "singularity", "apptainer":
		return "singularity", nil
	case "docker":
		return "docker", nil
	default:
		return "", fmt.Errorf("unsupported container runtime %q", runtime)
	}
}

var (
	nextflowRunOpts    nextflowRunOptions
	nextflowStatusOpts nextflowStatusOptions

	nextflowCmd = &cobra.Command{
		Use:   "nextflow",
		Short: "Run Nextflow DSL workflows on wr",
	}

	nextflowRunCmd    = newNextflowRunCommand(&nextflowRunOpts)
	nextflowStatusCmd = newNextflowStatusCommand(&nextflowStatusOpts)
)

func init() {
	RootCmd.AddCommand(nextflowCmd)
	nextflowCmd.AddCommand(nextflowRunCmd)
	nextflowCmd.AddCommand(nextflowStatusCmd)
}

func newNextflowRunCommand(options *nextflowRunOptions) *cobra.Command {
	cmd := &cobra.Command{
		Use:          "run workflow.nf",
		Short:        "Parse and submit a Nextflow workflow to wr",
		SilenceUsage: true,
		Args:         cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			runOptions := *options
			runOptions.containerRuntimeSet = cmd.Flags().Changed("container-runtime")

			return runNextflowWorkflow(cmd.OutOrStdout(), args[0], runOptions)
		},
	}

	cmd.Flags().StringVarP(&options.configPath, "config", "c", "", "path to nextflow.config")
	cmd.Flags().StringVarP(&options.paramsFilePath, "params-file", "p", "", "path to params JSON/YAML")
	cmd.Flags().StringArrayVarP(&options.paramAssignments, "param", "P", nil, "override a workflow param as KEY=VALUE")
	cmd.Flags().StringVar(&options.runID, "run-id", "", "explicit workflow run id")
	cmd.Flags().StringVar(&options.containerRuntime, "container-runtime", "singularity", "container runtime to use: docker or singularity")
	cmd.Flags().BoolVarP(&options.follow, "follow", "f", false, "follow dynamic workflow progression")
	cmd.Flags().DurationVar(&options.pollInterval, "poll-interval", nextflowDefaultPollInterval, "polling interval when following")
	cmd.Flags().StringVar(&options.profile, "profile", "", "select a nextflow config profile")
	cmd.Flags().IntVar(&timeoutint, "timeout", 120, "how long (seconds) to wait to get a reply from 'wr manager'")

	return cmd
}

func runNextflowWorkflow(outputWriter io.Writer, workflowArg string, options nextflowRunOptions) error {
	if options.profile != "" && options.configPath == "" {
		return fmt.Errorf("--profile requires --config")
	}
	if outputWriter == nil {
		outputWriter = io.Discard
	}

	resolver := nextflowdsl.NewGitHubResolver("")
	workflowPath, resolvedRemotely, err := nextflowdsl.ResolveWorkflowPath(workflowArg, resolver)
	if err != nil {
		return err
	}

	wf, err := nextflowdsl.LoadWorkflowFile(workflowPath, resolver)
	if err != nil {
		return err
	}

	fileParams := map[string]any(nil)
	if options.paramsFilePath != "" {
		fileParams, err = nextflowdsl.LoadParams(options.paramsFilePath)
		if err != nil {
			return err
		}
	}

	cliParams, err := parseNextflowCLIParams(options.paramAssignments)
	if err != nil {
		return err
	}

	var cfg *nextflowdsl.Config
	if options.configPath != "" {
		cfg, err = loadNextflowConfig(options.configPath, nextflowdsl.MergeParams(fileParams, cliParams))
		if err != nil {
			return err
		}
	}

	containerRuntime := options.containerRuntime
	if !options.containerRuntimeSet && cfg != nil && cfg.ContainerEngine != "" {
		containerRuntime = cfg.ContainerEngine
	}
	if containerRuntime == "" {
		containerRuntime = "singularity"
	}
	containerRuntime, err = normalizeNextflowContainerRuntime(containerRuntime)
	if err != nil {
		return err
	}

	cwd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("get current working directory: %w", err)
	}

	runID := options.runID
	autoGeneratedRunID := runID == ""
	if autoGeneratedRunID {
		runID = generateNextflowRunID(workflowPath)
	}

	translateConfig := nextflowdsl.TranslateConfig{
		RunID:            runID,
		WorkflowName:     nextflowResolvedWorkflowName(workflowArg, workflowPath, resolvedRemotely),
		WorkflowPath:     workflowPath,
		Cwd:              cwd,
		ContainerRuntime: containerRuntime,
		Params:           nextflowdsl.MergeParams(fileParams, cliParams),
		Profile:          options.profile,
	}

	result, err := nextflowdsl.Translate(wf, cfg, translateConfig)
	if err != nil {
		return err
	}

	jq := connect(time.Duration(timeoutint) * time.Second)
	defer func() {
		err = jq.Disconnect()
		if err != nil {
			warn("Disconnecting from the server failed: %s", err)
		}
	}()

	if err = addNextflowJobs(jq, nextflowRepGroupPrefix(translateConfig.WorkflowName, translateConfig.RunID), result.Jobs); err != nil {
		return err
	}

	if autoGeneratedRunID {
		if _, err = fmt.Fprintf(outputWriter, "Run ID: %s\n", runID); err != nil {
			return err
		}
	}

	if !options.follow {
		return nil
	}

	return followNextflowWorkflow(jq, nextflowRepGroupPrefix(translateConfig.WorkflowName, translateConfig.RunID), result.Pending, translateConfig, options.pollInterval, outputWriter)
}

func loadNextflowConfig(path string, externalParams map[string]any) (*nextflowdsl.Config, error) {
	cfg, err := nextflowdsl.ParseConfigFromPathWithParams(path, externalParams)
	if err != nil {
		return nil, err
	}

	return cfg, nil
}

func parseNextflowCLIParams(assignments []string) (map[string]any, error) {
	params := make(map[string]any)

	for _, assignment := range assignments {
		key, value, ok := strings.Cut(assignment, "=")
		if !ok || key == "" {
			return nil, fmt.Errorf("invalid --param %q: expected KEY=VALUE", assignment)
		}

		assignNextflowCLIParam(params, strings.Split(key, "."), parseNextflowCLIParamValue(value))
	}

	return params, nil
}

func assignNextflowCLIParam(params map[string]any, path []string, value any) {
	current := params
	for index, part := range path {
		if index == len(path)-1 {
			current[part] = value
			return
		}

		next, ok := current[part].(map[string]any)
		if !ok {
			next = make(map[string]any)
			current[part] = next
		}

		current = next
	}
}

func parseNextflowCLIParamValue(value string) any {
	if value == "true" {
		return true
	}
	if value == "false" {
		return false
	}
	if intValue, err := strconv.Atoi(value); err == nil {
		return intValue
	}
	if floatValue, err := strconv.ParseFloat(value, 64); err == nil {
		return floatValue
	}

	return value
}

func generateNextflowRunID(workflowPath string) string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s:%d", workflowPath, time.Now().UnixNano())))

	return hex.EncodeToString(sum[:8])
}

func nextflowResolvedWorkflowName(workflowArg, workflowPath string, resolvedRemotely bool) string {
	if resolvedRemotely {
		if repoName, ok := nextflowRemoteWorkflowName(workflowArg); ok {
			return repoName
		}
	}

	return nextflowWorkflowName(workflowPath)
}

func nextflowRemoteWorkflowName(workflowArg string) (string, bool) {
	spec, _, _ := strings.Cut(strings.TrimSpace(workflowArg), "@")
	if spec == "" {
		return "", false
	}

	if strings.HasPrefix(spec, "https://") || strings.HasPrefix(spec, "http://") {
		parsed, err := url.Parse(spec)
		if err != nil || !strings.EqualFold(parsed.Host, "github.com") {
			return "", false
		}

		parts := strings.Split(strings.Trim(strings.TrimSuffix(parsed.Path, ".git"), "/"), "/")
		if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
			return "", false
		}

		return strings.Join(parts, "/"), true
	}

	parts := strings.Split(strings.Trim(spec, "/"), "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", false
	}

	return strings.Join(parts, "/"), true
}

func nextflowWorkflowName(path string) string {
	base := filepath.Base(path)
	trimmed := strings.TrimSuffix(base, filepath.Ext(base))
	if strings.EqualFold(trimmed, "main") {
		parent := filepath.Base(filepath.Dir(path))
		if parent != "" && parent != "." && parent != string(filepath.Separator) {
			return parent
		}
	}
	if trimmed == "" {
		return base
	}

	return trimmed
}

func followNextflowWorkflow(
	jq nextflowJobManager,
	repGroupPrefix string,
	pending []*nextflowdsl.PendingStage,
	tc nextflowdsl.TranslateConfig,
	pollInterval time.Duration,
	outputWriter io.Writer,
) error {
	remaining := append([]*nextflowdsl.PendingStage(nil), pending...)
	terminalSuccessPending := false
	terminalPendingPending := false
	displayedJobKeys := make(map[string]struct{})
	if outputWriter == nil {
		outputWriter = io.Discard
	}

	for {
		completeJobs, buriedJobs, incompleteJobs, err := nextflowWorkflowJobs(jq, repGroupPrefix)
		if err != nil {
			return err
		}

		allKnownJobs := make([]*jobqueue.Job, 0, len(completeJobs)+len(buriedJobs)+len(incompleteJobs))
		allKnownJobs = append(allKnownJobs, completeJobs...)
		allKnownJobs = append(allKnownJobs, buriedJobs...)
		allKnownJobs = append(allKnownJobs, incompleteJobs...)

		newlyTerminalJobs := make([]*jobqueue.Job, 0, len(completeJobs)+len(buriedJobs))
		for _, terminalJobs := range [][]*jobqueue.Job{completeJobs, buriedJobs} {
			for _, job := range terminalJobs {
				key := job.Key()
				if _, ok := displayedJobKeys[key]; ok {
					continue
				}

				newlyTerminalJobs = append(newlyTerminalJobs, job)
			}
		}

		if err = printJobsOutput(outputWriter, newlyTerminalJobs, allKnownJobs, nfOutputMaxBytes); err != nil {
			return err
		}
		for _, job := range newlyTerminalJobs {
			displayedJobKeys[job.Key()] = struct{}{}
		}

		progressed := false
		nextPending := remaining[:0]
		for _, stage := range remaining {
			completed, ready, completedErr := nextflowdsl.CompletedJobsForPending(stage, completeJobs, incompleteJobs)
			if completedErr != nil {
				return completedErr
			}
			if !ready {
				nextPending = append(nextPending, stage)
				continue
			}

			jobs, translateErr := nextflowdsl.TranslatePending(stage, completed, tc)
			if translateErr != nil {
				return translateErr
			}
			if addErr := addNextflowJobs(jq, repGroupPrefix, jobs); addErr != nil {
				return addErr
			}

			progressed = true
		}
		remaining = nextPending

		if progressed {
			terminalSuccessPending = false
			terminalPendingPending = false
			continue
		}

		if len(incompleteJobs) == 0 {
			if len(buriedJobs) > 0 {
				return fmt.Errorf("workflow %s failed with %d buried job(s)", tc.RunID, len(buriedJobs))
			}
			if len(remaining) == 0 {
				if terminalSuccessPending {
					return nil
				}

				terminalSuccessPending = true
				nextflowSleep(pollInterval)

				continue
			}
			if terminalPendingPending {
				return fmt.Errorf("workflow %s has unresolved pending stages", tc.RunID)
			}

			terminalPendingPending = true
			nextflowSleep(pollInterval)

			continue
		}

		terminalSuccessPending = false
		terminalPendingPending = false
		nextflowSleep(pollInterval)
	}
}

func nextflowWorkflowJobs(jq nextflowJobQuerier, repGroupPrefix string) ([]*jobqueue.Job, []*jobqueue.Job, []*jobqueue.Job, error) {
	completeJobs, err := jq.GetByRepGroupMatch(repGroupPrefix, jobqueue.RepGroupMatchPrefix, 0, jobqueue.JobStateComplete, false, false)
	if err != nil {
		return nil, nil, nil, err
	}

	buriedJobs, err := jq.GetByRepGroupMatch(repGroupPrefix, jobqueue.RepGroupMatchPrefix, 0, jobqueue.JobStateBuried, false, false)
	if err != nil {
		return nil, nil, nil, err
	}

	incompleteJobs, err := jq.GetIncompleteByRepGroupMatch(repGroupPrefix, jobqueue.RepGroupMatchPrefix, 0, "", false, false)
	if err != nil {
		return nil, nil, nil, err
	}

	activeIncompleteJobs := make([]*jobqueue.Job, 0, len(incompleteJobs))
	for _, job := range incompleteJobs {
		if job.State == jobqueue.JobStateBuried {
			continue
		}

		activeIncompleteJobs = append(activeIncompleteJobs, job)
	}

	return completeJobs, buriedJobs, activeIncompleteJobs, nil
}

func addNextflowJobs(jq nextflowJobManager, repGroupPrefix string, jobs []*jobqueue.Job) error {
	jobsToAdd, err := nextflowResumeJobs(jq, repGroupPrefix, jobs)
	if err != nil {
		return err
	}

	if len(jobsToAdd) == 0 {
		return nil
	}

	_, _, err = jq.Add(jobsToAdd, nil, true)

	return err
}

func nextflowResumeJobs(jq nextflowJobManager, repGroupPrefix string, jobs []*jobqueue.Job) ([]*jobqueue.Job, error) {
	if len(jobs) == 0 {
		return nil, nil
	}

	existingJobs, err := jq.GetByRepGroupMatch(repGroupPrefix, jobqueue.RepGroupMatchPrefix, 0, "", false, false)
	if err != nil {
		return nil, err
	}

	existingByRepGroup := make(map[string][]*jobqueue.Job, len(existingJobs))
	for _, job := range existingJobs {
		existingByRepGroup[job.RepGroup] = append(existingByRepGroup[job.RepGroup], job)
	}

	jobsToAdd := make([]*jobqueue.Job, 0, len(jobs))
	for _, job := range jobs {
		matchedJob, remaining := nextflowResumeMatch(existingByRepGroup[job.RepGroup], job)
		existingByRepGroup[job.RepGroup] = remaining

		shouldAdd, deleteKey, resumeErr := nextflowResumeAction(matchedJob)
		if resumeErr != nil {
			return nil, resumeErr
		}

		if deleteKey != "" {
			deleted, deleteErr := jq.Delete([]*jobqueue.JobEssence{{JobKey: deleteKey}})
			if deleteErr != nil {
				return nil, deleteErr
			}
			if deleted == 0 {
				return nil, fmt.Errorf("failed to delete buried job %s during resume", job.RepGroup)
			}
		}

		if shouldAdd {
			jobsToAdd = append(jobsToAdd, job)
		}
	}

	return jobsToAdd, nil
}

func nextflowResumeMatch(existingJobs []*jobqueue.Job, plannedJob *jobqueue.Job) (*jobqueue.Job, []*jobqueue.Job) {
	if len(existingJobs) == 0 {
		return nil, nil
	}

	for index, existingJob := range existingJobs {
		if existingJob.Key() == plannedJob.Key() {
			matchedJob := existingJobs[index]
			remaining := append([]*jobqueue.Job{}, existingJobs[:index]...)
			remaining = append(remaining, existingJobs[index+1:]...)

			return matchedJob, remaining
		}
	}
	if len(existingJobs) == 1 {
		existingJob := existingJobs[0]
		if existingJob.CwdMatters || plannedJob.CwdMatters {
			if existingJob.CwdMatters && plannedJob.CwdMatters && filepath.Clean(existingJob.Cwd) == filepath.Clean(plannedJob.Cwd) {
				return existingJob, nil
			}

			return nil, append([]*jobqueue.Job{}, existingJobs...)
		}

		return existingJob, nil
	}

	return nil, append([]*jobqueue.Job{}, existingJobs...)
}

func nextflowResumeAction(existingJob *jobqueue.Job) (bool, string, error) {
	if existingJob == nil {
		return true, "", nil
	}

	switch existingJob.State {
	case jobqueue.JobStateBuried:
		return true, existingJob.Key(), nil
	case jobqueue.JobStateDeleted:
		return true, "", nil
	case jobqueue.JobStateComplete,
		jobqueue.JobStateRunning,
		jobqueue.JobStateDependent,
		jobqueue.JobStateDelayed,
		jobqueue.JobStateReady,
		jobqueue.JobStateReserved:
		return false, "", nil
	case jobqueue.JobStateLost:
		return true, "", nil
	default:
		return true, "", nil
	}
}

func nextflowRepGroupPrefix(workflowName, runID string) string {
	return fmt.Sprintf("nf.%s.%s.", nextflowRepGroupToken(workflowName), nextflowRepGroupToken(runID))
}

func newNextflowStatusCommand(options *nextflowStatusOptions) *cobra.Command {
	cmd := &cobra.Command{
		Use:          "status",
		Short:        "Show Nextflow workflow progress on wr",
		SilenceUsage: true,
		Args:         cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runNextflowStatus(cmd.OutOrStdout(), *options)
		},
	}

	cmd.Flags().StringVarP(&options.runID, "run-id", "r", "", "filter to a specific workflow run id")
	cmd.Flags().StringVarP(&options.workflow, "workflow", "w", "", "filter to a specific workflow name")
	cmd.Flags().BoolVarP(&options.Output, "output", "o", false, "display captured output for complete and buried jobs")
	cmd.Flags().IntVar(&timeoutint, "timeout", 120, "how long (seconds) to wait to get a reply from 'wr manager'")

	return cmd
}

func runNextflowStatus(w io.Writer, options nextflowStatusOptions) error {
	if options.Output && options.runID == "" {
		return fmt.Errorf("--output requires --run-id")
	}

	jq := connect(time.Duration(timeoutint) * time.Second)
	defer func() {
		err := jq.Disconnect()
		if err != nil {
			warn("Disconnecting from the server failed: %s", err)
		}
	}()

	jobs, err := findNextflowStatusJobs(jq, options)
	if err != nil {
		return err
	}

	if len(jobs) == 0 {
		_, err = fmt.Fprintln(w, "no jobs found")
		return err
	}

	counts := aggregateNextflowProcessCounts(jobs)
	processes := make([]string, 0, len(counts))
	for process := range counts {
		processes = append(processes, process)
	}
	sort.Strings(processes)

	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	if _, err = fmt.Fprintln(tw, "Process\tPending\tRunning\tComplete\tBuried\tTotal"); err != nil {
		return err
	}

	for _, process := range processes {
		count := counts[process]
		if _, err = fmt.Fprintf(tw, "%s\t%d\t%d\t%d\t%d\t%d\n", process, count.Pending, count.Running, count.Complete, count.Buried, count.Total); err != nil {
			return err
		}
	}

	if err = tw.Flush(); err != nil {
		return err
	}

	if !options.Output {
		return nil
	}

	terminalJobs := make([]*jobqueue.Job, 0, len(jobs))
	for _, job := range jobs {
		if job.State != jobqueue.JobStateComplete && job.State != jobqueue.JobStateBuried {
			continue
		}

		terminalJobs = append(terminalJobs, job)
	}

	return printJobsOutput(w, terminalJobs, jobs, nfOutputMaxBytes)
}

func aggregateNextflowProcessCounts(jobs []*jobqueue.Job) map[string]nextflowProcessCounts {
	counts := make(map[string]nextflowProcessCounts)

	for _, job := range jobs {
		parsed, ok := parseNextflowRepGroup(job.RepGroup)
		if !ok {
			continue
		}

		count := counts[parsed.process]
		jobCount := 1 + job.Similar
		count.Total += jobCount

		switch job.State {
		case jobqueue.JobStateDelayed, jobqueue.JobStateReady, jobqueue.JobStateDependent:
			count.Pending += jobCount
		case jobqueue.JobStateReserved, jobqueue.JobStateRunning, jobqueue.JobStateLost:
			count.Running += jobCount
		case jobqueue.JobStateComplete:
			count.Complete += jobCount
		case jobqueue.JobStateBuried:
			count.Buried += jobCount
		}

		counts[parsed.process] = count
	}

	return counts
}

func parseNextflowRepGroup(repGroup string) (nextflowRepGroup, bool) {
	parts := strings.Split(repGroup, ".")
	if len(parts) < 4 || parts[0] != "nf" {
		return nextflowRepGroup{}, false
	}

	process := strings.Join(parts[3:], ".")
	if process == "" {
		return nextflowRepGroup{}, false
	}

	workflow, err := url.PathUnescape(parts[1])
	if err != nil {
		return nextflowRepGroup{}, false
	}
	runID, err := url.PathUnescape(parts[2])
	if err != nil {
		return nextflowRepGroup{}, false
	}

	return nextflowRepGroup{workflow: workflow, runID: runID, process: process}, true
}

func findNextflowStatusJobs(jq *jobqueue.Client, options nextflowStatusOptions) ([]*jobqueue.Job, error) {
	prefix := nextflowStatusPrefix(options)
	jobs, err := jq.GetByRepGroupMatch(prefix, jobqueue.RepGroupMatchPrefix, 0, "", false, false)
	if err != nil {
		return nil, err
	}

	filtered := make([]*jobqueue.Job, 0, len(jobs))
	for _, job := range jobs {
		parsed, ok := parseNextflowRepGroup(job.RepGroup)
		if !ok {
			continue
		}

		if options.workflow != "" && parsed.workflow != options.workflow {
			continue
		}

		if options.runID != "" && parsed.runID != options.runID {
			continue
		}

		filtered = append(filtered, job)
	}

	return filtered, nil
}

func nextflowStatusPrefix(options nextflowStatusOptions) string {
	if options.workflow != "" && options.runID != "" {
		return fmt.Sprintf("nf.%s.%s.", nextflowRepGroupToken(options.workflow), nextflowRepGroupToken(options.runID))
	}

	if options.workflow != "" {
		return fmt.Sprintf("nf.%s.", nextflowRepGroupToken(options.workflow))
	}

	return "nf."
}

func nextflowRepGroupToken(value string) string {
	return strings.ReplaceAll(url.PathEscape(value), ".", "%2E")
}

type nextflowJobQuerier interface {
	Add(jobs []*jobqueue.Job, envVars []string, ignoreComplete bool) (int, int, error)
	GetByRepGroupMatch(repgroup string, match jobqueue.RepGroupMatch, limit int, state jobqueue.JobState, getStd bool, getEnv bool) ([]*jobqueue.Job, error)
	GetIncompleteByRepGroupMatch(repgroup string, match jobqueue.RepGroupMatch, limit int, state jobqueue.JobState, getStd bool, getEnv bool) ([]*jobqueue.Job, error)
}

type nextflowJobManager interface {
	nextflowJobQuerier
	Delete(jes []*jobqueue.JobEssence) (int, error)
}

type nextflowRepGroup struct {
	workflow string
	runID    string
	process  string
}

func printJobsOutput(w io.Writer, displayJobs []*jobqueue.Job, allJobs []*jobqueue.Job, maxBytes int64) error {
	type jobOutput struct {
		process  string
		index    int
		hasIndex bool
		text     string
		cwd      string
	}

	processCounts := make(map[string]int)
	for _, job := range allJobs {
		parsed, ok := parseNextflowRepGroup(job.RepGroup)
		if !ok {
			continue
		}

		processCounts[parsed.process]++
	}

	outputs := make([]jobOutput, 0, len(displayJobs))
	for _, job := range displayJobs {
		parsed, ok := parseNextflowRepGroup(job.RepGroup)
		if !ok {
			continue
		}

		stdout, err := readCapturedOutput(filepath.Join(job.Cwd, nfStdoutFile), maxBytes)
		if err != nil {
			return err
		}
		stderr, err := readCapturedOutput(filepath.Join(job.Cwd, nfStderrFile), maxBytes)
		if err != nil {
			return err
		}

		formatted := formatJobOutput(jobOutputLabel(parsed.process, job.Cwd, processCounts[parsed.process] > 1), stdout, stderr)
		if formatted == "" {
			continue
		}

		index, hasIndex := instanceIndexFromCwd(job.Cwd)
		outputs = append(outputs, jobOutput{
			process:  parsed.process,
			index:    index,
			hasIndex: hasIndex,
			text:     formatted,
			cwd:      job.Cwd,
		})
	}

	sort.Slice(outputs, func(i, j int) bool {
		if outputs[i].process != outputs[j].process {
			return outputs[i].process < outputs[j].process
		}
		if outputs[i].hasIndex && outputs[j].hasIndex && outputs[i].index != outputs[j].index {
			return outputs[i].index < outputs[j].index
		}
		if outputs[i].hasIndex != outputs[j].hasIndex {
			return outputs[i].hasIndex
		}

		return outputs[i].cwd < outputs[j].cwd
	})

	for _, output := range outputs {
		if _, err := io.WriteString(w, output.text); err != nil {
			return err
		}
	}

	return nil
}

func readCapturedOutput(path string, maxBytes int64) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", nil
	}
	defer func() {
		_ = file.Close()
	}()

	data, err := io.ReadAll(io.LimitReader(file, maxBytes+1))
	if err != nil {
		return "", nil
	}

	truncated := int64(len(data)) > maxBytes
	if truncated {
		data = data[:maxBytes]
	}

	if len(strings.TrimSpace(string(data))) == 0 {
		return "", nil
	}

	output := string(data)
	if truncated {
		output += "\n[... output truncated ...]"
	}

	return output, nil
}

func formatJobOutput(label string, stdout, stderr string) string {
	var builder strings.Builder
	writeJobOutputSection(&builder, label, stdout)
	writeJobOutputSection(&builder, label+" (stderr)", stderr)

	return builder.String()
}

func writeJobOutputSection(builder *strings.Builder, label, content string) {
	if content == "" {
		return
	}

	trimmed := strings.TrimSuffix(content, "\n")
	if trimmed == "" {
		return
	}

	lines := strings.Split(trimmed, "\n")
	if len(lines) == 1 {
		builder.WriteString(label)
		builder.WriteString(" ")
		builder.WriteString(lines[0])
		builder.WriteString("\n")

		return
	}

	builder.WriteString(label)
	builder.WriteString("\n")
	for _, line := range lines {
		builder.WriteString("  ")
		builder.WriteString(line)
		builder.WriteString("\n")
	}
}

func jobOutputLabel(process string, cwd string, isMultiInstance bool) string {
	if isMultiInstance {
		if index, ok := instanceIndexFromCwd(cwd); ok {
			return fmt.Sprintf("[%s (%d)]", process, index)
		}
	}

	return fmt.Sprintf("[%s]", process)
}

func instanceIndexFromCwd(cwd string) (int, bool) {
	index, err := strconv.Atoi(filepath.Base(filepath.Clean(cwd)))
	if err != nil {
		return 0, false
	}

	return index, true
}

type nextflowProcessCounts struct {
	Pending  int
	Running  int
	Complete int
	Buried   int
	Total    int
}
