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
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/VertebrateResequencing/wr/jobqueue"
	jqs "github.com/VertebrateResequencing/wr/jobqueue/scheduler"
	"github.com/spf13/cobra"
)

// options for this cmd

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
software.)

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
		wd, err := os.Getwd()
		if err != nil {
			die(err.Error())
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
				}
			} else {
				if job.Cmd == "" {
					job.Cmd = line
				} else {
					job.Cmd += "; " + line
				}
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
			parts := strings.Split(job.Cmd, " ")
			job.ReqGroup = filepath.Base(parts[0])
		}

		// connect to the server
		jq := connect(10 * time.Second)
		defer jq.Disconnect()

		// add the job to the queue
		inserts, _, err := jq.Add([]*jobqueue.Job{job}, os.Environ(), true)
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

// bjobs sub-command emulates bjobs.
var lsfBjobsCmd = &cobra.Command{
	Use:   "bjobs",
	Short: "See jobs in bjobs format",
	Long: `See jobs that have been added using the lsf bsub command, using bjobs
syntax and being formatted the way bjobs display this information.

NB: Not yet implemented.`,
	Run: func(cmd *cobra.Command, args []string) {
		fmt.Println("bjobs not yet implemented")
		os.Exit(-1)
	},
}

func init() {
	RootCmd.AddCommand(lsfCmd)
	lsfCmd.AddCommand(lsfBsubCmd)
	lsfCmd.AddCommand(lsfBjobsCmd)

	// flags specific to these sub-commands
	// defaultConfig := internal.DefaultConfig()
	// managerStartCmd.Flags().BoolVarP(&foreground, "foreground", "f", false, "do not daemonize")
	// managerStartCmd.Flags().StringVarP(&scheduler, "scheduler", "s", defaultConfig.ManagerScheduler, "['local','lsf','openstack'] job scheduler")
	// managerStartCmd.Flags().IntVarP(&osRAM, "cloud_ram", "r", defaultConfig.CloudRAM, "for cloud schedulers, ram (MB) needed by the OS image specified by --cloud_os")
}
