// Copyright © 2018 Genome Research Limited
// Author: Sendu Bala <sb10@sanger.ac.uk>.
//
//  This file is part of wr.
//
//  wr is free software: you can redistribute it and/or modify
//  it under the terms of the GNU Lesser General Public License as published by
//  the Free Software Foundation, either version 3 of the License, or
//  (at your option) any later version.
//
//  wr is distributed in the hope that it will be useful,
//  but WITHOUT ANY WARRANTY; without even the implied warranty of
//  MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
//  GNU Lesser General Public License for more details.
//
//  You should have received a copy of the GNU Lesser General Public License
//  along with wr. If not, see <http://www.gnu.org/licenses/>.

package cmd

import (
	"bufio"
	"encoding/json"
	goflag "flag"
	"fmt"
	"io"
	"os"
	"regexp"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/VertebrateResequencing/wr/internal"
	"github.com/VertebrateResequencing/wr/jobqueue"
	jqs "github.com/VertebrateResequencing/wr/jobqueue/scheduler"
	"github.com/spf13/cobra"
)

const lsfTimeFormat = "Jan 02 15:04"

var jobStateToLSFState = map[jobqueue.JobState]string{
	jobqueue.JobStateNew:       "PEND",
	jobqueue.JobStateDelayed:   "PEND",
	jobqueue.JobStateDependent: "PEND",
	jobqueue.JobStateReady:     "PEND",
	jobqueue.JobStateReserved:  "PEND",
	jobqueue.JobStateRunning:   "RUN",
	jobqueue.JobStateLost:      "UNKWN",
	jobqueue.JobStateBuried:    "EXIT",
	jobqueue.JobStateComplete:  "DONE",
}

// options for this cmd
var lsfNoHeader bool
var lsfFormat string
var lsfQueue string

// lsfCmd represents the lsf command.
var lsfCmd = &cobra.Command{
	Use:   "lsf",
	Short: "LSF emulation",
	Long: `LSF emulation.

Many existing pipelines and workflows may be written with the LSF scheduler in
mind, either hard-coded to use it exclusively, or supporting a number of
schedulers including LSF but not, say, OpenStack.

wr's LSF emulation lets you submit jobs to wr as if it was LSF, providing
compatibility with old pipelines. If the manager has been started in LSF mode,
this could result in greater efficiency compared to directly using the real bsub
command. If you've done a cloud deployment, this allows pipelines that know
nothing about the cloud to distribute their workload in that cloud environment.

NB: currently the emulation is extremely limited, supporting only the
interactive "console" mode where you run bsub without any arguments, and it only
supports single flags per #BSUB line, and it only pays attention to -J, -n and
-M flags. (This is sufficient for compatibility with 10x Genomic's cellranger
software (which has Maritan built in), and to work as the scheduler for
nextflow.) There is only one "queue", called 'wr'.

The best way to use this LSF emulation is not to call this command yourself
directly, but to use 'wr add --bsubs [other opts]' to add the command that you
expect will call 'bsub'. In cloud deployments, your --cloud_* and --mounts
options will be applied to any job added via bsub emulation, that is it
effectively emulates all the work being done on an LSF farm with shared disk.`,
}

// bsub sub-command emulates bsub.
var lsfBsubCmd = &cobra.Command{
	Use:   "bsub",
	Short: "Add a job using bsub syntax",
	Long:  `Add a job to the queue using bsub syntax.`,
	Run: func(cmd *cobra.Command, args []string) {
		wd, errg := os.Getwd()
		if errg != nil {
			die(errg.Error())
		}

		job := &jobqueue.Job{
			BsubMode:     deployment,
			RepGroup:     "bsub",
			Cwd:          wd,
			CwdMatters:   true,
			Requirements: &jqs.Requirements{Cores: 1, RAM: 1000, Time: 1 * time.Hour},
			Retries:      uint8(0),
		}

		// since bsub calls can't communicate possibly necessary cloud_* and
		// mount options for the job we're going to add, we read these from an
		// environment variable that got created when a job was added to the
		// queue with --bsub option; since this arrangement is in theory
		// "optional", we ignore errors
		if jsonStr := os.Getenv("WR_BSUB_CONFIG"); jsonStr != "" {
			configJob := &jobqueue.Job{}
			if err := json.Unmarshal([]byte(jsonStr), configJob); err == nil {
				job.MountConfigs = configJob.MountConfigs
				job.Requirements.Other = configJob.Requirements.Other
				job.BsubMode = configJob.BsubMode
				deployment = configJob.BsubMode
				initConfig()
			}
		}

		r := regexp.MustCompile(`^#BSUB\s+-(\w)\s+(.+)$`)
		// rMem := regexp.MustCompile(`mem[>=](\d+)`)

		fmt.Printf("bsub> ")
		scanner := bufio.NewScanner(os.Stdin)
		var possibleExe string
		for scanner.Scan() {
			line := strings.TrimSpace(scanner.Text())

			if strings.HasPrefix(line, "#") {
				matches := r.FindStringSubmatch(line)
				if matches != nil {
					// *** this does not support the (valid) inclusion of
					// multiple options per line
					switch matches[1] {
					case "J":
						job.RepGroup = matches[2]
					case "n":
						if n, err := strconv.Atoi(matches[2]); err == nil {
							job.Requirements.Cores = n
						}
					case "M":
						if n, err := strconv.Atoi(matches[2]); err == nil {
							job.Requirements.RAM = n
							job.Override = 2
						}
					}
				} else {
					job.Cmd += line + "\n"
				}
			} else {
				if possibleExe == "" {
					parts := strings.Split(line, " ")
					possibleExe = parts[0]
				}
				job.Cmd += line + "\n"
			}

			fmt.Printf("bsub> ")
		}

		if scanner.Err() != nil {
			die(scanner.Err().Error())
		}

		if job.Cmd == "" {
			fmt.Println("No command is specified. Job not submitted.")
			os.Exit(255)
		}

		if job.ReqGroup == "" {
			job.ReqGroup = possibleExe
		}

		// connect to the server
		jq := connect(10 * time.Second)
		var err error
		defer func() {
			err = jq.Disconnect()
			if err != nil {
				warn("Disconnecting from the server failed: %s", err)
			}
		}()

		// add the job to the queue
		inserts, _, err := jq.Add([]*jobqueue.Job{job}, os.Environ(), false)
		if err != nil {
			die(err.Error())
		}

		if inserts != 1 {
			fmt.Println("Duplicate command specified. Job not submitted.")
			os.Exit(255)
		}

		j, err := jq.GetByEssence(&jobqueue.JobEssence{Cmd: job.Cmd, Cwd: job.Cwd, MountConfigs: job.MountConfigs}, false, false)
		if err != nil {
			die(err.Error())
		}

		fmt.Printf("Job <%d> is submitted to default queue <wr>.\n", j.BsubID)
	},
}

type lsfFieldDisplay func(*jobqueue.Job) string

// bjobs sub-command emulates bjobs.
var lsfBjobsCmd = &cobra.Command{
	Use:   "bjobs",
	Short: "See jobs in bjobs format",
	Long: `See jobs that have been added using the lsf bsub command, using bjobs
syntax and being formatted the way bjobs display this information.

Only lists all incomplete jobs. Unlike real bjobs, does not list recently
completed jobs. Unlike real bjobs, does not truncate columns (always effectivly
in -w mode).

Only supports this limited set of real bjobs options:
-noheader
-o <output format>
-q <queue name>

The output format only supports simple listing of desired columns (not choosing
their width), and specifying the delimiter. The only columns supported are
JOBID, USER, STAT, QUEUE, FROM_HOST, EXEC_HOST, JOB_NAME and SUBMIT_TIME.
eg. -o 'JOBID STAT SUBMIT_TIME delimiter=","'

While -q can be provided, and that provided queue will be displayed in the
output, in reality there is only 1 queue called 'wr', so -q has no real function
other than providing compatability with real bjobs command line args.`,
	Run: func(cmd *cobra.Command, args []string) {
		user, err := internal.Username()
		if err != nil {
			die(err.Error())
		}

		// connect to the server
		jq := connect(10 * time.Second)
		defer func() {
			err = jq.Disconnect()
			if err != nil {
				warn("Disconnecting from the server failed: %s", err)
			}
		}()

		// set up viewing of the allowed fields
		fieldLookup := make(map[string]lsfFieldDisplay)
		fieldLookup["JOBID"] = func(job *jobqueue.Job) string {
			return strconv.Itoa(int(job.BsubID))
		}
		fieldLookup["USER"] = func(job *jobqueue.Job) string {
			return user
		}
		fieldLookup["STAT"] = func(job *jobqueue.Job) string {
			return jobStateToLSFState[job.State]
		}
		fieldLookup["QUEUE"] = func(job *jobqueue.Job) string {
			return lsfQueue
		}
		fieldLookup["FROM_HOST"] = func(job *jobqueue.Job) string {
			return jq.ServerInfo.Host
		}
		fieldLookup["EXEC_HOST"] = func(job *jobqueue.Job) string {
			return job.Host
		}
		fieldLookup["JOB_NAME"] = func(job *jobqueue.Job) string {
			return job.RepGroup
		}
		fieldLookup["SUBMIT_TIME"] = func(job *jobqueue.Job) string {
			return job.StartTime.Format(lsfTimeFormat)
		}

		// parse -o
		var delimiter string
		var fields []string
		var w io.Writer
		if lsfFormat != "" {
			// parse -o format
			re := regexp.MustCompile(`(?i)\s*delimiter=["'](.*)["']\s*`)
			matches := re.FindStringSubmatch(lsfFormat)
			if matches != nil {
				delimiter = matches[1]
				lsfFormat = re.ReplaceAllString(lsfFormat, "")
			} else {
				delimiter = " "
			}
			for _, field := range strings.Split(lsfFormat, " ") {
				field = strings.ToUpper(field)
				if _, exists := fieldLookup[field]; !exists {
					die("unsupported field '%s'", field)
				}
				fields = append(fields, field)
			}

			// custom format just uses a single delimiter between fields
			w = os.Stdout
		} else {
			// standard format uses aligned columns of the fields
			delimiter = "\t"
			fields = []string{"JOBID", "USER", "STAT", "QUEUE", "FROM_HOST", "EXEC_HOST", "JOB_NAME", "SUBMIT_TIME"}
			w = tabwriter.NewWriter(os.Stdout, 2, 2, 3, ' ', 0)
		}

		// get all incomplete jobs
		jobs, err := jq.GetIncomplete(0, "", false, false)
		if err != nil {
			die(err.Error())
		}

		// print out details about the ones that have BsubIDs
		found := false
		for _, job := range jobs {
			jid := job.BsubID
			if jid == 0 {
				continue
			}

			if !found {
				if !lsfNoHeader {
					// print header
					fmt.Fprintln(w, strings.Join(fields, delimiter))
				}
				found = true
			}

			var vals []string
			for _, field := range fields {
				vals = append(vals, fieldLookup[field](job))
			}
			fmt.Fprintln(w, strings.Join(vals, delimiter))
		}

		if lsfFormat == "" {
			tw := w.(*tabwriter.Writer)
			tw.Flush()
		}

		if !found {
			fmt.Println("No unfinished job found")
		}
	},
}

// bkill sub-command emulates bkill.
var lsfBkillCmd = &cobra.Command{
	Use:   "bkill",
	Short: "Kill jobs added using bsub",
	Long: `Kill jobs that have been added using the lsf bsub command.

Only supports providing jobIds as command line arguements. Does not currently
understand any of the options that real bkill does.

Note that if a given jobId is not currently in the queue, always just claims
that the job has already finished, even if an invalid jobId was supplied.`,
	Run: func(cmd *cobra.Command, args []string) {
		// convert args to uint64s
		desired := make(map[uint64]bool)
		for _, arg := range args {
			i, err := strconv.Atoi(arg)
			if err != nil {
				die("could not convert jobID [%s] to an int: %s", arg, err)
			}
			desired[uint64(i)] = true
		}
		if len(desired) == 0 {
			die("job ID must be specified")
		}

		// connect to the server
		jq := connect(10 * time.Second)
		var err error
		defer func() {
			err = jq.Disconnect()
			if err != nil {
				warn("Disconnecting from the server failed: %s", err)
			}
		}()

		// get all incomplete jobs *** this is hardly efficient...
		jobs, err := jq.GetIncomplete(0, "", false, false)
		if err != nil {
			die(err.Error())
		}

		// remove the matching ones
	JOBS:
		for _, job := range jobs {
			jid := job.BsubID
			if !desired[jid] {
				continue
			}

			if job.State == jobqueue.JobStateRunning {
				_, errk := jq.Kill([]*jobqueue.JobEssence{job.ToEssense()})
				if errk != nil {
					warn("error trying to kill job %d: %s", jid, errk)
					continue
				}

				// wait until it gets buried
				for {
					<-time.After(500 * time.Millisecond)
					got, errg := jq.GetByEssence(job.ToEssense(), false, false)
					if errg != nil {
						warn("error trying confirm job %d was killed: %s", jid, errg)
						continue JOBS
					}

					if got.State == jobqueue.JobStateBuried {
						break
					}
				}
			}

			_, errd := jq.Delete([]*jobqueue.JobEssence{job.ToEssense()})
			if errd != nil {
				warn("error trying to delete job %d: %s", jid, errd)
				continue
			}

			fmt.Printf("Job <%d> is being terminated\n", jid)
			delete(desired, jid)
		}

		for jid := range desired {
			fmt.Printf("Job <%d>: Job has already finished\n", jid)
		}
	},
}

func init() {
	// custom handling of LSF args with their single dashes
	args, lsfArgs := filterGoFlags(os.Args, map[string]bool{
		"noheader": false,
		"o":        true,
		"q":        true,
	})
	os.Args = args

	goflag.BoolVar(&lsfNoHeader, "noheader", false, "disable header output")
	goflag.StringVar(&lsfFormat, "o", "", "output format")
	goflag.StringVar(&lsfQueue, "q", "wr", "queue")
	if err := goflag.CommandLine.Parse(lsfArgs); err != nil {
		die("error parsing LSF args: ", err)
	}

	RootCmd.AddCommand(lsfCmd)
	lsfCmd.AddCommand(lsfBsubCmd)
	lsfCmd.AddCommand(lsfBjobsCmd)
	lsfCmd.AddCommand(lsfBkillCmd)
}

// filterGoFlags splits lsf args, which use single dash named args, from wr
// args, which use single dash to mean a set of shorthand flags.
func filterGoFlags(args []string, prefixes map[string]bool) ([]string, []string) {
	// from https://gist.github.com/doublerebel/8b95c5c118e958e495d2
	var goFlags []string
	for i := 0; 0 < len(args) && i < len(args); i++ {
		for prefix, hasValue := range prefixes {
			if strings.HasPrefix(args[i], "-"+prefix) {
				goFlags = append(goFlags, args[i])
				skip := 1
				if hasValue && i+1 < len(args) {
					goFlags = append(goFlags, args[i+1])
					skip = 2
				}
				if i+skip <= len(args) {
					args = append(args[:i], args[i+skip:]...)
				}
				i--
				break
			}
		}
	}
	return args, goFlags
}
