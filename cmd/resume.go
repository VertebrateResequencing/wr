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
	"github.com/VertebrateResequencing/wr/jobqueue"
	"github.com/spf13/cobra"
)

// resumeCmd represents the resume command.
var resumeCmd = &cobra.Command{
	Use:   "resume",
	Short: "Resume suspended commands",
	Long: `You can resume commands you've previously suspended with "wr suspend"
using this command.

Specify one of the flags -f, -l, -i or -a to choose which commands you want to
resume. Amongst those, only currently suspended jobs will be affected. With -a,
all suspended jobs are resumed.

-i is the report group (-i) you supplied to "wr add" when you added the job(s)
you want to now resume. Combining with -z lets you resume jobs in multiple
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
		if err := runResumeCommand(); err != nil {
			die("%s", err)
		}
	},
}

func runResumeCommand() error {
	return runSelectedJobsCommand(selectedJobsCommand{
		resultVerb:          "Resumed",
		matchingDescription: "suspended commands",
		actionError:         "failed to resume desired jobs",
		allState:            jobqueue.JobStateSuspended,
		action: func(jq *jobqueue.Client, jes []*jobqueue.JobEssence) (int, error) {
			return jq.Resume(jes)
		},
	})
}

func init() {
	RootCmd.AddCommand(resumeCmd)
	addSelectedJobsFlags(resumeCmd, "resume", "resume all suspended jobs")
}
