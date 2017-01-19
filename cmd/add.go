// Copyright © 2016 Genome Research Limited
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
	"github.com/VertebrateResequencing/wr/jobqueue"
	jqs "github.com/VertebrateResequencing/wr/jobqueue/scheduler"
	"github.com/pivotal-golang/bytefmt"
	"github.com/spf13/cobra"
	"io"
	"io/ioutil"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// options for this cmd
var reqGroup string
var cmdTime string
var cmdMem string
var cmdCPUs int
var cmdDisk int
var cmdOvr int
var cmdPri int
var cmdRet int
var cmdFile string
var cmdRepGroup string
var cmdDepGroups string
var cmdDeps string

// addCmdOpts is the struct we decode user's JSON options in to
type addCmdOpts struct {
	Cmd         string                 `json:"cmd"`
	Cwd         string                 `json:"cwd"`
	ReqGrp      string                 `json:"req_grp"`
	Memory      string                 `json:"memory"`
	Time        string                 `json:"time"`
	CPUs        *int                   `json:"cpus"`
	Disk        *int                   `json:"disk"`
	Override    *int                   `json:"override"`
	Priority    *int                   `json:"priority"`
	Retries     *int                   `json:"retries"`
	RepGrp      string                 `json:"rep_grp"`
	DepGrps     []string               `json:"dep_grps"`
	Deps        []string               `json:"deps"`
	CmdDeps     []*jobqueue.Dependency `json:"cmd_deps"`
	CloudOS     string                 `json:"cloud_os"`
	CloudUser   string                 `json:"cloud_user"`
	CloudScript string                 `json:"cloud_script"`
	CloudOSRam  *int                   `json:"cloud_os_ram"`
}

// addCmd represents the add command
var addCmd = &cobra.Command{
	Use:   "add",
	Short: "Add commands to the queue",
	Long: `Manually add commands you want run to the queue.

You can supply your commands by putting them in a text file (1 per line), or
by piping them in. In addition to the command itself, you can specify command-
specific options using a JSON object in (tab separated) column 2, or
alternatively have only a JSON object in column 1 that also specifies the
command as one of the name:value pairs. The possible options are:

cmd cwd req_grp memory time override cpus disk priority retries rep_grp dep_grps
deps cmd_deps cloud_os cloud_user cloud_os_ram cloud_script

If any of these (except the cloud ones) will be the same for all your commands,
you can instead specify them as flags (which are treated as defaults in the case
that they are unspecified in the text file, but otherwise ignored). The meaning
of each option is detailed below.

A JSON object can written by starting and ending it with curly braces. Names and
single values are put in double quotes (except for numbers, which are left bare)
and the pair separated with a colon, and pairs separated from each other with
commas. Options that take array values have their double-quoted values
separated by commas and enclosed in square brackets. For example (on one line):
{"cmd":"myexe -f input > output","cwd":"/path/to/cwd","priority":1,"dep_grps":
["dg2","dg3"],"deps":["dg1"]}

"cwd" is the directory to cd to before running the command. If none is
specified, the default will be your current directory right now. (If adding to a
remote cloud-deployed manager, then cwd will instead default to /tmp.)

"req_grp" is an arbitrary string that identifies the kind of commands you are
adding, such that future commands you add with this same requirements group are
likely to have similar memory and time requirements. It defaults to the basename
of the first word in your command, which it assumes to be the name of your
executable.

"memory" and "time" let you provide hints to wr manager so that it can do a
better job of spawning runners to handle these commands. "memory" values should
specify a unit, eg "100M" for 100 megabytes, or "1G" for 1 gigabyte. "time"
values should do the same, eg. "30m" for 30 minutes, or "1h" for 1 hour.

The manager learns how much memory and time commands in the same req_grp
actually used in the past, and will use its own values unless you set an
override. For this learning to work well, you should have reason to believe that
all the commands you add with the same req_grp will have similar memory and time
requirements, and you should pick the name in a consistent way such that you'll
use it again in the future.

For example, if you want to run an executable called "exop", and you know that
the memory and time requirements of exop vary with the size of its input file,
you might batch your commands so that all the input files in one batch have
sizes in a certain range, and then provide a req_grp that describes this, eg.
"exop.1-2Ginputs" for inputs in the 1 to 2 GB range.

(Don't name your req_grp after the expected requirements themselves, such as
"5GBram.1hr", because then the manager can't learn about your commands - it is
only learning about how good your estimates are! The name of your executable
should almost always be part of the req_grp name.)

"override" defines if your memory and time should be used instead of the
manager's estimate. Possible values are:
0 = do not override wr's learned values for memory and time (if any)
1 = override if yours are higher
2 = always override

"cpus" tells wr manager exactly how many CPU cores your command needs.

"disk" tells wr manager how much free disk space (in GB) your command needs. If
you know that where your command will store its outputs to will not run out of
disk space, set this to 0 to avoid unnecessary disk space checks (or possible
volume creation, in the case of cloud schedulers).
[disk space reservation and checking is not currently implemented, except for
the openstack scheduler which will create temporary volumes of the specified
size if necessary]

"priority" defines how urgent a particular command is; those with higher
priorities will start running before those with lower priorities. The range of
possible values is 0 (default) to 255. Commands with the same priority will be
started in the order they were added.

"retries" defines how many times a command will be retried automatically if it
fails. Automatic retries are helpful in the case of transient errors, or errors
due to running out of memory or time (when retried, they will be retried with
more memory/time reserved). Once this number of retries is reached, the command
will be 'buried' until you take manual action to fix the problem and press the
retry button in the web interface.

"rep_grp" is an arbitrary group you can give your commands so you can query
their status later. This is only used for reporting and presentation purposes
when viewing status.

"dep_grps" is an array of arbitrary names you can associate with a command, so
that you can then refer to this job (and others with the same dep_grp) in
another job's deps.

"deps" and "cmd_deps" define the dependencies of this command. The commands that
these refer to must complete before this command will start. The value for
"deps" is an array of the dep_grp of other commands. Dependencies specified in
this way are 'live', causing this command to be automatically re-run if any
commands with any of the dep_grps it is dependent upon get added to the queue.
The value for "cmd_deps" is an array of JSON objects with "cmd" and "cwd"
name:value pairs. These are static dependencies; once resolved they do not get
re-evaluated.

The "cloud_*" related options let you override the defaults of your cloud
deployment. For example, if you do 'wr cloud deploy --os "Ubuntu 16" --os_ram
2048 -u ubuntu -s ~/my_ubuntu_post_creation_script.sh', any commands you add
will by default run on cloud nodes running Ubuntu. If you set "cloud_os" to
"CentOS 7", "cloud_user" to "centos", "cloud_os_ram" to 4096, and "cloud_script"
to "~/my_centos_post_creation_script.sh", then this command will run on a cloud
node running CentOS (with at least 4GB ram).

NB: Your commands will run with the environment variables you had when you
added them, not the possibly different environment variables you could have in
the future when the commands actually get run.`,
	Run: func(combraCmd *cobra.Command, args []string) {
		// check the command line options
		if cmdFile == "" {
			die("--file is required")
		}
		if cmdRepGroup == "" {
			cmdRepGroup = "manually_added"
		}
		var cmdMB int
		var err error
		if cmdMem == "" {
			cmdMB = 0
		} else {
			mb, err := bytefmt.ToMegabytes(cmdMem)
			if err != nil {
				die("--memory was not specified correctly: %s", err)
			}
			cmdMB = int(mb)
		}
		var cmdDuration time.Duration
		if cmdTime == "" {
			cmdDuration = 0 * time.Second
		} else {
			cmdDuration, err = time.ParseDuration(cmdTime)
			if err != nil {
				die("--time was not specified correctly: %s", err)
			}
		}
		if cmdCPUs < 1 {
			cmdCPUs = 1
		}
		if cmdOvr < 0 || cmdOvr > 2 {
			die("--override must be in the range 0..2")
		}
		if cmdPri < 0 || cmdPri > 255 {
			die("--priority must be in the range 0..255")
		}
		if cmdRet < 0 || cmdRet > 255 {
			die("--retries must be in the range 0..255")
		}
		timeout := time.Duration(timeoutint) * time.Second

		var defaultDepGroups []string
		if cmdDepGroups != "" {
			defaultDepGroups = strings.Split(cmdDepGroups, ",")
		}

		var defaultDeps []*jobqueue.Dependency
		if cmdDeps != "" {
			cols := strings.Split(cmdDeps, "\\t")
			if len(cols)%2 != 0 {
				die("--deps must have an even number of tab-separated columns")
			}
			defaultDeps = colsToDeps(cols)
		}

		// open file or set up to read from STDIN
		var reader io.Reader
		if cmdFile == "-" {
			reader = os.Stdin
		} else {
			reader, err = os.Open(cmdFile)
			if err != nil {
				die("could not open file '%s': %s", cmdFile, err)
			}
			defer reader.(*os.File).Close()
		}

		// we'll default to pwd if the manager is on the same host as us, /tmp
		// otherwise
		jq, err := jobqueue.Connect(addr, "cmds", timeout)
		if err != nil {
			die("%s", err)
		}
		sstats, err := jq.ServerStats()
		if err != nil {
			die("even though I was able to connect to the manager, it failed to tell me its location")
		}
		var pwd string
		var pwdWarning int
		if jobqueue.CurrentIP()+":"+config.ManagerPort == sstats.ServerInfo.Addr {
			pwd, err = os.Getwd()
			if err != nil {
				die("%s", err)
			}
		} else {
			pwd = "/tmp"
			pwdWarning = 1
		}
		jq.Disconnect()

		// for network efficiency, read in all commands and create a big slice
		// of Jobs and Add() them in one go afterwards
		var jobs []*jobqueue.Job
		scanner := bufio.NewScanner(reader)
		defaultedRepG := false
		lineNum := 0
		for scanner.Scan() {
			lineNum++
			cols := strings.Split(scanner.Text(), "\t")
			colsn := len(cols)
			if colsn < 1 || cols[0] == "" {
				continue
			}
			if colsn > 2 {
				die("line %d has too many columns; check `wr add -h`", lineNum)
			}

			// determine all the options for this command
			var cmdOpts addCmdOpts
			var jsonErr error
			if colsn == 2 {
				jsonErr = json.Unmarshal([]byte(cols[1]), &cmdOpts)
				if jsonErr == nil {
					cmdOpts.Cmd = cols[0]
				}
			} else {
				if strings.HasPrefix(cols[0], "{") {
					jsonErr = json.Unmarshal([]byte(cols[0]), &cmdOpts)
				} else {
					cmdOpts = addCmdOpts{Cmd: cols[0]}
				}
			}

			if jsonErr != nil {
				die("line %d had a problem with the JSON: %s", lineNum, jsonErr)
			}

			var cmd, cwd, rg, repg string
			var mb, cpus, disk, override, priority, retries int
			var dur time.Duration
			var depGroups []string
			var deps *jobqueue.Dependencies

			cmd = cmdOpts.Cmd
			if cmd == "" {
				die("line %d does not specify a cmd", lineNum)
			}

			if cmdOpts.Cwd == "" {
				if cmdCwd != "" {
					cwd = cmdCwd
				} else {
					if pwdWarning == 1 {
						warn("command working directories defaulting to /tmp since the manager is running remotely")
						pwdWarning = 0
					}
					cwd = pwd
				}
			} else {
				cwd = cmdOpts.Cwd
			}

			if cmdOpts.RepGrp == "" {
				if reqGroup != "" {
					rg = reqGroup
				} else {
					parts := strings.Split(cmd, " ")
					rg = filepath.Base(parts[0])
				}
			} else {
				rg = cmdOpts.RepGrp
			}

			if cmdOpts.Memory == "" {
				mb = cmdMB
			} else {
				thismb, err := bytefmt.ToMegabytes(cmdOpts.Memory)
				if err != nil {
					die("line %d's memory value (%s) was not specified correctly: %s", lineNum, cmdOpts.Memory, err)
				}
				mb = int(thismb)
			}

			if cmdOpts.Time == "" {
				dur = cmdDuration
			} else {
				dur, err = time.ParseDuration(cmdOpts.Time)
				if err != nil {
					die("line %d's time value (%s) was not specified correctly: %s", lineNum, cmdOpts.Time, err)
				}
			}

			if cmdOpts.Override == nil {
				override = cmdOvr
			} else {
				override = *cmdOpts.Override
				if override < 0 || override > 2 {
					die("line %d's override value (%d) is not in the range 0..2", lineNum, override)
				}
			}

			if cmdOpts.CPUs == nil {
				cpus = cmdCPUs
			} else {
				cpus = *cmdOpts.CPUs
			}

			if cmdOpts.Disk == nil {
				disk = cmdDisk
			} else {
				disk = *cmdOpts.Disk
			}

			if cmdOpts.Priority == nil {
				priority = cmdPri
			} else {
				priority = *cmdOpts.Priority
				if priority < 0 || priority > 255 {
					die("line %d's priority value (%d) is not in the range 0..255", lineNum, priority)
				}
			}

			if cmdOpts.Retries == nil {
				retries = cmdRet
			} else {
				retries = *cmdOpts.Retries
				if retries < 0 || retries > 255 {
					die("line %d's retries value (%d) is not in the range 0..255", lineNum, retries)
				}
			}

			if cmdOpts.RepGrp == "" {
				repg = cmdRepGroup
				defaultedRepG = true
			} else {
				repg = cmdOpts.RepGrp
			}

			if len(cmdOpts.DepGrps) == 0 {
				depGroups = defaultDepGroups
			} else {
				depGroups = cmdOpts.DepGrps
			}

			if len(cmdOpts.Deps) == 0 && len(cmdOpts.CmdDeps) == 0 {
				deps = jobqueue.NewDependencies(defaultDeps...)
			} else {
				var theseDeps []*jobqueue.Dependency
				if len(cmdOpts.CmdDeps) > 0 {
					theseDeps = cmdOpts.CmdDeps
				}
				if len(cmdOpts.Deps) > 0 {
					for _, depgroup := range cmdOpts.Deps {
						theseDeps = append(theseDeps, jobqueue.NewDepGroupDependency(depgroup))
					}
				}
				deps = jobqueue.NewDependencies(theseDeps...)
			}

			// scheduler-specific options
			other := make(map[string]string)
			if cmdOpts.CloudOS != "" {
				other["cloud_os"] = cmdOpts.CloudOS
			}
			if cmdOpts.CloudUser != "" {
				other["cloud_user"] = cmdOpts.CloudUser
			}
			if cmdOpts.CloudScript != "" {
				var postCreation []byte
				postCreation, err = ioutil.ReadFile(cmdOpts.CloudScript)
				if err != nil {
					die("line %d's cloud_script value (%s) could not be read: %s", lineNum, cmdOpts.CloudScript, err)
				}
				other["cloud_script"] = string(postCreation)
			}
			if cmdOpts.CloudOSRam != nil {
				osRAM := *cmdOpts.CloudOSRam
				other["cloud_os_ram"] = strconv.Itoa(osRAM)
			}

			jobs = append(jobs, jobqueue.NewJob(cmd, cwd, rg, &jqs.Requirements{RAM: mb, Time: dur, Cores: cpus, Disk: disk, Other: other}, uint8(override), uint8(priority), uint8(retries), repg, depGroups, deps))
		}

		// connect to the server
		jq, err = jobqueue.Connect(addr, "cmds", timeout)
		if err != nil {
			die("%s", err)
		}
		defer jq.Disconnect()

		// add the jobs to the queue
		inserts, dups, err := jq.Add(jobs)
		if err != nil {
			die("%s", err)
		}

		if defaultedRepG {
			info("Added %d new commands (%d were duplicates) to the queue using default identifier '%s'", inserts, dups, cmdRepGroup)
		} else {
			info("Added %d new commands (%d were duplicates) to the queue", inserts, dups)
		}
	},
}

func init() {
	RootCmd.AddCommand(addCmd)

	// flags specific to this sub-command
	addCmd.Flags().StringVarP(&cmdFile, "file", "f", "-", "file containing your commands; - means read from STDIN")
	addCmd.Flags().StringVarP(&cmdRepGroup, "report_grp", "i", "manually_added", "reporting group for your commands")
	addCmd.Flags().StringVarP(&cmdDepGroups, "dep_grps", "e", "", "comma-separated list of dependency groups")
	addCmd.Flags().StringVarP(&cmdCwd, "cwd", "c", "", "working dir")
	addCmd.Flags().StringVarP(&reqGroup, "req_grp", "g", "", "group name for commands with similar reqs")
	addCmd.Flags().StringVarP(&cmdMem, "memory", "m", "1G", "peak mem est. [specify units such as M for Megabytes or G for Gigabytes]")
	addCmd.Flags().StringVarP(&cmdTime, "time", "t", "1h", "max time est. [specify units such as m for minutes or h for hours]")
	addCmd.Flags().IntVar(&cmdCPUs, "cpus", 1, "cpu cores needed")
	addCmd.Flags().IntVar(&cmdDisk, "disk", 0, "number of GB of disk space required [0 means do not check disk space] (default 0)")
	addCmd.Flags().IntVarP(&cmdOvr, "override", "o", 0, "[0|1|2] should your mem/time estimates override? (default 0)")
	addCmd.Flags().IntVarP(&cmdPri, "priority", "p", 0, "[0-255] command priority (default 0)")
	addCmd.Flags().IntVarP(&cmdRet, "retries", "r", 3, "[0-255] number of automatic retries for failed commands")
	addCmd.Flags().StringVarP(&cmdDeps, "deps", "d", "", "dependencies of your commands, in the form \"command1\\tcwd1\\tcommand2\\tcwd2...\" or \"dep_grp1,dep_grp2...\\tgroups\"")

	addCmd.Flags().IntVar(&timeoutint, "timeout", 30, "how long (seconds) to wait to get a reply from 'wr manager'")
}

// convert cmd,cwd or depgroups,"groups" columns in to Dependency
func colsToDeps(cols []string) (deps []*jobqueue.Dependency) {
	for i := 0; i < len(cols); i += 2 {
		if cols[i+1] == "groups" {
			for _, depgroup := range strings.Split(cols[i], ",") {
				deps = append(deps, jobqueue.NewDepGroupDependency(depgroup))
			}
		} else {
			deps = append(deps, jobqueue.NewCmdDependency(cols[i], cols[i+1]))
		}
	}
	return
}
