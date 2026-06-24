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
	"errors"
	"fmt"
	"time"

	"github.com/VertebrateResequencing/wr/jobqueue"
	"github.com/spf13/cobra"
)

const selectedJobsDefaultTimeout = 120

var (
	errSelectedJobsNeedSelector = errors.New("1 of -f, -i, -l or -a is required")
	errSelectedJobsExclusive    = errors.New("-f, -i, -l and -a are mutually exclusive")
	errSelectedJobsNeedID       = errors.New("-z and -y require -i")
	errSelectedJobsNoMatch      = errors.New("no matching jobs found")
)

// suspendCmd represents the suspend command.
var suspendCmd = &cobra.Command{
	Use:   "suspend",
	Short: "Suspend queued commands",
	Long: `You can suspend queued commands you've previously added with "wr add"
that are not currently running using this command.

Specify one of the flags -f, -l, -i or -a to choose which commands you want to
suspend. Amongst those, only currently delayed, ready, or dependent jobs will be
affected.

-i is the report group (-i) you supplied to "wr add" when you added the job(s)
you want to now suspend. Combining with -z lets you suspend jobs in multiple
report groups, assuming you have arranged that related groups share some
substring. Alternatively -y lets you specify -i as the internal job id reported
during "wr status".

The file to provide -f is in the format taken by "wr add".

In -f and -l mode you must provide the cwd the commands were set to run in, if
CwdMatters (and must NOT be provided otherwise). Likewise provide the mounts
options that were used when the command was added, if any. You can do this by
using the -c and --mounts/--mounts_json options in -l mode, or by providing the
same file you gave to "wr add" in -f mode.`,
	Run: func(_ *cobra.Command, _ []string) {
		if err := runSuspendCommand(); err != nil {
			die("%s", err)
		}
	},
}

type selectedJobsCommand struct {
	resultVerb          string
	matchingDescription string
	actionError         string
	allState            jobqueue.JobState
	action              func(*jobqueue.Client, []*jobqueue.JobEssence) (int, error)
}

func runSuspendCommand() error {
	return runSelectedJobsCommand(selectedJobsCommand{
		resultVerb:          "Suspended",
		matchingDescription: "queued commands",
		actionError:         "failed to suspend desired jobs",
		action: func(jq *jobqueue.Client, jes []*jobqueue.JobEssence) (int, error) {
			return jq.Suspend(jes)
		},
	})
}

func runSelectedJobsCommand(spec selectedJobsCommand) error {
	if err := validateSelectedJobsCommand(); err != nil {
		return err
	}

	timeout := time.Duration(timeoutint) * time.Second

	jq := connect(timeout)
	defer disconnectSelectedJobsClient(jq)

	jobs := getSelectedJobs(jq, spec.allState)
	if len(jobs) == 0 {
		return errSelectedJobsNoMatch
	}

	changed, err := spec.action(jq, jobsToJobEssenses(jobs))
	if err != nil {
		return fmt.Errorf("%s: %w", spec.actionError, err)
	}

	fmt.Printf("%s %d %s (out of %d matching)\n", spec.resultVerb, changed, spec.matchingDescription, len(jobs))

	return nil
}

func validateSelectedJobsCommand() error {
	if (cmdIDIsSubStr || cmdIDIsInternal) && cmdIDStatus == "" {
		return errSelectedJobsNeedID
	}

	set := countGetJobArgs()
	if set > 1 {
		return errSelectedJobsExclusive
	}

	if set == 0 {
		return errSelectedJobsNeedSelector
	}

	return nil
}

func disconnectSelectedJobsClient(jq *jobqueue.Client) {
	if err := jq.Disconnect(); err != nil {
		warn("Disconnecting from the server failed: %s", err)
	}
}

func getSelectedJobs(jq *jobqueue.Client, allState jobqueue.JobState) []*jobqueue.Job {
	var state jobqueue.JobState
	if cmdAll {
		state = allState
	}

	return getJobs(jq, state, cmdAll, 0, false, false)
}

func init() {
	RootCmd.AddCommand(suspendCmd)
	addSelectedJobsFlags(suspendCmd, "suspend", "suspend all live jobs")
}

func addSelectedJobsFlags(command *cobra.Command, verb, allHelp string) {
	flags := command.Flags()
	flags.BoolVarP(&cmdAll, "all", "a", false, allHelp)
	flags.StringVarP(
		&cmdFileStatus, "file", "f", "",
		fmt.Sprintf("file containing commands you want to %s; - means read from STDIN", verb),
	)
	flags.StringVarP(&cmdIDStatus, "identifier", "i", "", "identifier of the commands you want to "+verb)
	flags.BoolVarP(
		&cmdIDIsSubStr, "search", "z", false,
		"treat -i as a substring to match against all report groups",
	)
	flags.BoolVarP(&cmdIDIsInternal, "internal", "y", false, "treat -i as an internal job id")
	flags.StringVarP(&cmdLine, "cmdline", "l", "", "a command line you want to "+verb)
	flags.StringVarP(
		&cmdCwd, "cwd", "c", "",
		"working dir that the command(s) specified by -l or -f were set to run in",
	)
	flags.StringVarP(
		&mountJSON, "mount_json", "j", "",
		"mounts that the command(s) specified by -l or -f were set to use (JSON format)",
	)
	flags.StringVar(
		&mountSimple, "mounts", "",
		"mounts that the command(s) specified by -l or -f were set to use (simple format)",
	)
	flags.IntVar(
		&timeoutint, "timeout", selectedJobsDefaultTimeout,
		"how long (seconds) to wait to get a reply from 'wr manager'",
	)
}
