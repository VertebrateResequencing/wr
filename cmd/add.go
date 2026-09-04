/*******************************************************************************
 * Copyright (c) 2016-2021, 2024-2026 Genome Research Ltd.
 *
 * Author: Sendu Bala <sb10@sanger.ac.uk>
 * Author: [Theo Barber-Bany] <theobarberbany@gmail.com>
 * Author: Ashwini Chhipa <ac55@sanger.ac.uk>
 * Author: Rosie Kern
 * Author: Michael Woolnough <mw31@sanger.ac.uk>
 * Author: pn10-sanger <pn10@sanger.ac.uk>
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
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"code.cloudfoundry.org/bytefmt"
	"github.com/VertebrateResequencing/wr/internal"
	"github.com/VertebrateResequencing/wr/jobqueue"
	jscheduler "github.com/VertebrateResequencing/wr/jobqueue/scheduler"
	"github.com/spf13/cobra"
)

// maxScanTokenSize defines the size of bufio scan's buffer, enabling us to
// parse very long lines - longer than the max length of a command supported by
// shells such as bash.
const maxScanTokenSize = 4096 * 1024

const (
	overrideNo     = 0
	overrideHigher = 1
	overrideAlways = 2
)

const (
	// defaultAddRetries is the default value for the add command's --retries
	// flag.
	defaultAddRetries = 3

	// defaultAddTimeout is the default value (in seconds) for the add command's
	// --timeout flag.
	defaultAddTimeout = 120

	// maxCmdFileColumns is the maximum number of tab-separated columns a line in
	// a commands file may have (the command, then an optional JSON object).
	maxCmdFileColumns = 2

	// configFilePathParts is the number of colon-separated parts in a config
	// file spec that specifies both a source and a destination path.
	configFilePathParts = 2
)

var (
	errSynchronousJobMissing         = errors.New("synchronous job missing after terminal update")
	errSynchronousSubscriptionClosed = errors.New("subscription closed before synchronous job completed")
)

// options for this cmd.
var (
	reqGroup                string
	cmdTime                 string
	cmdMem                  string
	cmdCPUs                 float64
	cmdDisk                 int
	cmdOvr                  string
	cmdPri                  int
	cmdRet                  int
	cmdFile                 string
	cmdHead                 int
	cmdCwdMatters           bool
	cmdCwdMattersChanged    *bool
	cmdChangeHome           bool
	cmdRepGroup             string
	cmdGroup                string
	cmdLimitGroups          string
	cmdModules              string
	cmdDepGroups            string
	cmdCmdDeps              string
	cmdGroupDeps            string
	cmdOnFailure            string
	cmdOnSuccess            string
	cmdOnExit               string
	cmdEnv                  string
	cmdRemoteSameAsLocal    bool
	cmdReRun                bool
	cmdOsPrefix             string
	cmdOsUsername           string
	cmdOsRAM                int
	cmdBsubMode             bool
	cmdPostCreationScript   string
	cmdCloudConfigs         string
	cmdCloudSharedDisk      bool
	cmdFlavor               string
	cmdQueue                string
	cmdQueuesAvoidAdd       string
	cmdMisc                 string
	cmdMonitorDocker        string
	cmdWithDocker           string
	cmdWithSingularity      string
	cmdContainerMounts      string
	cmdNoRetry              string
	cmdDisableRelativeCheck bool
	rtimeoutint             int
	simpleOutput            bool
	syncMode                bool
)

// addCmd represents the add command.
var addCmd = &cobra.Command{
	Use:   "add",
	Short: "Add commands to the queue",
	//nolint:dupword // help text intentionally repeats date/time format tokens
	Long: `Manually add commands you want run to the queue.

In normal usage, after you add commands to the queue, this will tell you how
many were added and exit immediately (probably before the commands start to be
executed). With --simple, it will return the ids of the jobs you added and exit.
In both cases, the commands will be executed by the manager at some point in the
future.

You can also supply just a single command and the --sync option (for
"synchronous add"), which will result in this command not exiting until the
manager has finished executing the command. This command will then exit with
your command's exit code and output the head and tail of its STDOUT and STDERR
if it had failed. You might use synchronous mode within a simple script that
runs other commands but needs one of them executed by wr in your cluster
environment, and if your script can't easily cope with wr's normally asynchonous
behaviour.

You can supply your commands by putting them in a text file (1 per line), or
by piping them in. In addition to the command itself, you can specify command-
specific options using a JSON object in (tab separated) column 2, or
alternatively have only a JSON object in column 1 that also specifies the
command as one of the name:value pairs. The possible options are:

cmd cwd cwd_matters change_home on_failure on_success on_exit mounts req_grp
memory time override cpus disk queue misc priority retries rep_grp dep_grps deps
cmd_deps monitor_docker with_docker with_singularity container_mounts cloud_os
cloud_username cloud_ram cloud_script cloud_config_files cloud_flavor
cloud_shared env bsub_mode modules limit_grps queues_avoid reserve_timeout
no_retry_over_walltime

If any of these will be the same for all your commands, you can instead specify
them as flags (which are treated as defaults in the case that they are
unspecified in the text file, but otherwise ignored). The meaning of each option
is detailed below.

A JSON object can written by starting and ending it with curly braces. Names and
single values are put in double quotes (except for numbers, which are left bare,
and booleans, where you write 'true' or 'false' without quotes) and the pair
separated with a colon, and pairs separated from each other with commas. Options
that take array values have their double-quoted values separated by commas and
enclosed in square brackets. For example (on one line): {"cmd":"myexe -f input >
output","cwd":"/path/to/cwd","priority":1,"dep_grps":["dg2","dg3"],"deps":
["dg1"]}

"cwd" determines the directory to cd to before running the command (the 'command
working directory'). If none is specified, the default will be your current
directory right now. (If adding to a remote cloud-deployed manager, then cwd
will instead default to /tmp.) If you are adding from inside a running wr
command, the default is instead that command's own job's cwd, so that the new
job's working directory is a sibling of the adding job's and not inside it (an
adding job's cleanup behaviour would otherwise delete the new job's work).

"cwd_matters" by default is false, causing "cwd" to taken as the parent
directory to create a unique working directory inside. This unique directory can
be deleted after the cmd finishes running (according to cleanup behaviour), and
enables tracking of how much disk space your cmd uses. If using mounts and not
specifying a mount point, the mount point will be the actual working directory.
It also sets $TMPDIR to a sister directory of the actual working directory, and
this is always deleted after the cmd runs. If, on the other hand, you set
cwd_matters, then "cwd" is the literal command working directory, you can't
clean up afterwards, you don't get disk space tracking and undefined mounts are
mounted in the "mnt" subdirectory of cwd. One benefit is that any output files
that your command creates with relative paths will be easy to find since they'll
be relative to your own set cwd path (otherwise you'd have to find out the
actual cwd value in the status of a job). It also lets you specify relative
paths to your input files in your cmd, assuming they are in your cwd.

Because cwd_matters false means commands with relative paths in them won't
work, and this is unexpected by new users, wr tries to detect this situation
and warns about it. It's possible for wr to get this wrong, however, so there is
a flag to disable this check: --disable_relative_check.

"change_home" only has an effect when "cwd_matters" is false. If enabled, sets
the $HOME environment variable to the actual command working directory before
running the cmd.

"group" specifies which unix group the command should run as; if no value is
set, the users default unix group is used.

"on_failure" determines what behaviours are triggered if your cmd exits non-0.
Behaviours are described using an array of objects, where each object has a key
corresponding to the name of the desired behaviour, and the relevant value. The
currently available behaviours are: "cleanup_all", which takes a boolean value
and if true will completely delete the actual working directory created when
cwd_matters is false (when cwd_matters is true there is no such directory, so
cleanup_all and cleanup would do nothing, and wr discards them instead of
storing them on the job, ie. they won't appear in its status); "cleanup", which
is like cleanup_all except that it doesn't delete files that have been specified
as inputs or outputs [since you can't currently specify this, the current
behaviour is identical to cleanup_all]; "run", which takes a string command to
run after the main cmd runs; and "remove", which takes a boolean value and if
true that means that if the cmd gets buried, it will then immediately be
removed from the queue (useful for Cromwell compatibility).
For example [{"run":"cp error.log /shared/logs/this.log"},{"cleanup":true}]
would copy a log file that your cmd generated to describe its problems to some
shared location and then delete all files created by your cmd.

"on_success" is exactly like on_failure, except that the behaviours trigger when
your cmd exits 0.

"on_exit" is exactly like on_failure, except that the behaviours trigger when
your cmd exits, regardless of exit code. These behaviours will trigger after any
behaviours defined in on_failure or on_success.

"mounts" (or the --mount_json option) describes the remote file systems or
object stores you would like to be fuse mounted locally before running your
command. See the help text for 'wr mount' for an explanation of how to formulate
the value. Your mounts will be unmounted after the triggering of any behaviours,
so your "run" behaviours will be able to read from or write to anything in your
mount point(s). The "cleanup" and "cleanup_all" behaviours, however, will ignore
your mounted directories and any mount cache directories, so that nothing on
your remote file systems gets deleted. Unmounting will get rid of them though,
so you would still end up with a "cleaned" workspace - except for a cache
directory you named yourself, which unmounting deliberately leaves in place, and
which will therefore be left behind along with the workspace holding it.

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

"override" defines if your memory, disk or time should be used instead of the
manager's estimate. Possible values are:
0 | no | n = do not override wr's learned values for memory, disk and time (if
             any)
1 | higher | h = override if yours are higher
2 | always | a = always override specified resource(s)
(If you choose to override eg. only disk, then the learned value for memory and
time will be used. If you want to override all 3 resources to disable learning
completly, you must explicitly supply non-zero values for memory and time and 0
or more for disk.)

"cpus" tells wr manager exactly how many CPU cores your command needs.

"disk" tells wr manager how much free disk space (in GB) your command needs.
Disk space reservation only applies to the OpenStack schedulers which will
create temporary volumes of the specified size if necessary. Note that disk
space usage checking and learning only occurs for jobs where cwd doesn't matter
(is a unique directory), and ignores the contents of mounted directories.

"queue" tells wr which queue a job should be submitted to, when using a job
scheduler that has queues (eg. LSF). If queue is not specified, wr will use
heuristics to pick the most appropriate queue based on the time, memory and cpu
requirements of the job. If a comma-separated list of queue names is supplied,
we will limit its picking to be amongst those.

"queues_avoid" is comma-separated list of substrings found in queue names that
should not be submitted to, when using a job scheduler that has queues (eg. LSF)
and not picking an explicit --queue yourself.

"misc" will be used as-is to form the command line used to submit jobs to
external job schedulers (eg. LSF). For example, --misc '-R avx' might result
in a command line containing: bsub -R avx. Do not specify memory requirements,
scheduler output files or queue details, since these will be included in the
scheduler command line for you. Make sure to include a space between flags and
values, ie. --misc '-R "avx"', not --misc '-R"avx"'. Consider this complicated
LSF bsub command:
bsub -M ${MEMORY} -R"select[(hname!='qpg-gpu-01') && (hname!='qpg-gpu-02') && 
	(mem>${MEMORY})] rusage[mem=${MEMORY}]" -q gpu-normal
	-gpu "num=1:mig=2:aff=no" -o "%J.out" -e "%J.err" ./command.sh
When using wr add, this becomes:
echo "./command.sh" | wr add -m ${MEMORY}M --queue gpu-normal
	--misc "-R \"select[(hname!='qpg-gpu-01') && (hname!='qpg-gpu-02')]\"
	-gpu num=1:mig=2:aff=no"

"priority" defines how urgent a particular command is; those with higher
priorities will start running before those with lower priorities. The range of
possible values is 0 (default, for lowest priority) to 255 (highest priority).
Commands with the same priority will be started in the order they were added.
(Note, however, that order of starting is only guaranteed to hold true amongst
jobs with similar resource requirements, since your chosen job scheduler may,
for example, run your highest priority job on a machine where it takes up 90% of
memory, and then find another job to run on that machine that needs 10% or less
memory - and that job might be one of your low priority ones.)

"retries" defines how many times a command will be retried automatically if it
fails. Automatic retries are helpful in the case of transient errors, or errors
due to running out of memory or time (when retried, they will be retried with
more memory/time reserved). Once this number of retries is reached, the command
will be 'buried' until you take manual action to fix the problem and press the
retry button in the web interface or use "wr retry". When a command fails, if
there are retries remaining, before the command is run again there will be a
delay, and the length of the delay depends on the number of attempts so far,
increasing from 30s by a factor of 2 each attempt, up to a maximuim of 1hr. The
delay time is also jittered by up to 30s, to avoid the thundering herd problem.

"no_retry_over_walltime" defines a time which if a command runs longer than and
fails, it will be immediately buried, regardless of the "retries" value. This is
useful for commands that might fail quickly due to some transient initialization
issue, and would likely succeed if retried, but are always expected to fail if
they get past initialization and then fail. The default value of 0 time disables
this feature and jobs will always retry according to "retries".

"rep_grp" is an arbitrary group you can give your commands so you can query
their status later. This is only used for reporting and presentation purposes
when viewing status.

"limit_grps" is an array of arbitrary names you can associate with a command,
that can be used to limit the number of jobs that run at once in the same group.
You can optionally suffix a group name with :n where n is a integer new limit
for that group. 0 prevents jobs in that group running at all. -1 makes jobs in
that group unlimited. If no limit number is suffixed, groups will be unlimited
until a limit is set with the "wr limit" command.

In addition, you can specify a limit group in one of the following formats to
only allow your job to run at specific times:
	hh:mm:ss < time
	time < hh:mm:ss
	hh:mm:ss < time < hh:mm:ss

	YYYY-MM-DD hh:mm:ss < datetime
	datetime < YYYY-MM-DD hh:mm:ss
	YYYY-MM-DD hh:mm:ss < datetime < YYYY-MM-DD hh:mm:ss

…replacing the 'hh:mm:ss' and 'YYYY-MM-DD hh:mm:ss' placeholders as appropriate.

For example, to run a job on any day between 17:00 and 20:00, you could supply
the following limit group:

17:00:00 < time < 20:00:00

Or, to start your job after 12:00 on 1st of July, 2020, you could supply the
following limit group:

2020-07-01 12:00:00 < datetime

With the above formats, the job will only be able to start if it satisfies the
format given. Jobs can run past valid times.

"modules" is an array of environment module names that should be loaded before
running the Cmd. "module load --force <modules>" will be used.

"dep_grps" is an array of arbitrary names you can associate with a command, so
that you can then refer to this job (and others with the same dep_grp) in
another job's deps.

"deps" or "cmd_deps" define the dependencies of this command. The commands that
these refer to must complete before this command will start. The value for
"deps" is an array of the dep_grp of other commands. Dependencies specified in
this way are 'live'. Dep-group dependencies from "deps" and --deps wait even
when the dep-group has not appeared yet, and cause this command to be
automatically re-run if any commands with any of the dep_grps it is dependent
upon get added to the queue.
The value for "cmd_deps" is an array of JSON objects with "cmd" and "cwd"
name:value pairs (if cwd doesn't matter for a cmd, provide it as an empty
string). Command dependencies from "cmd_deps" and --cmd_deps keep static
behaviour; once resolved they do not get re-evaluated.

"monitor_docker" turns on monitoring of a docker container identified by the
given string, which could be the container's --name or path to its --cidfile. If
the string contains ? or * symbols and doesn't match a name or file name
literally, those symbols will be treated as wildcards (any single character, or
any number of any character, respectively) in a search for the first matching
file name containing a valid container id, to be treated as the --cidfile.
This will add the container's peak RAM and total CPU usage to the reported RAM
and CPU usage of this job. If the special argument "?" is supplied, monitoring
will apply to the first new docker container that appears after the command
starts to run. NB: in ? mode, if multiple jobs that run docker containers start
running at the same time on the same machine, the reported stats could be wrong
for one or more of those jobs. Requires that docker is installed on the machine
where the job will run (and that the command uses docker to run a container).
NB: does not handle monitoring of multiple docker containers run by a single
command. A side effect of monitoring a container is that if you use wr to kill
the job for this command, wr will also kill the container.

"with_docker" takes an image name/location and is a convenience feature that
will run your command by piping it into 'docker run -i [image] /bin/sh'. Docker
must therefore be installed on worker nodes for this to work. The image will be
automatically pulled if it is missing. The container is created with cwd mounted
and set to the workdir. Any mounts specified by "container_mounts" will also be
mounted inside the container. Any environment variables you explicitly override
with "env" will be set inside the container, but not other environment variables
you have set at the time you add the command. Finally, "monitor_docker" will be
overridden and set to monitor the container wr creates. If you would need to run
your docker container with additional options or in a different way, don't use
"with_docker", and instead have command be your own 'docker run [...]' command,
and set "monitor_docker" as appropriate.

"with_singularity" takes an image name/location and is a convenience feature
that will run your command by piping it into 'singularity shell [image]'.
Singularity must therefore be installed and in the $PATH of worker nodes for
this to work. The image will be automatically pulled if it is missing. The
container is created with cwd mounted and set to current directory inside the
container. Any mounts specified by "container_mounts" will also be mounted
inside the container. All current and overridden environment variables will be
set inside the container. If you would need to run your singularity container
with additional options or in a different way, don't use "with_singularity", and
instead have command be your own 'singularity [...]' command.
It's not valid to set both with_docker and with_singularity; if you do, only
with_docker will be obeyed.

"container_mounts" is a comma separated list of
/outside/container:/inside/container mount definitions, for use with either
"with_docker" or "with_singularity". The :/inside path is optional, defaulting
to the same as the outside path. It will result in the outside paths being
readable and writable inside the container at the inside paths.

The "cloud_*" related options let you override the defaults of your cloud
deployment. For example, if you do 'wr cloud deploy --os "Ubuntu 16" --os_ram
2048 -u ubuntu -s ~/my_ubuntu_post_creation_script.sh', any commands you add
will by default run on cloud nodes running Ubuntu. If you set "cloud_os" to
"CentOS 7", "cloud_username" to "centos", "cloud_ram" to 4096, and
"cloud_script" to "~/my_centos_post_creation_script.sh", then this command will
run on a cloud node running CentOS (with at least 4GB ram). If you set
"cloud_flavor" then the command will only run on a server with that exact
flavor (normally the cheapest flavor is chosen for you based on the command's
resource requirements). The format for cloud_config_files is described under the
help text for "wr cloud deploy"'s --config_files option. The per-job config
files you specify will be treated as in addition to any specified during cloud
deploy or when starting the manager. Note that your cloud_script must complete
within 15 mins; if your script is slow because it installs a lot of software,
consider creating a new image instead and using cloud_os.

"cloud_shared" only works when using a cloud scheduler where both the manager
and jobs will run on Ubuntu. It will cause /shared on the manager's server to be
NFS shared to /shared mounted on the server where your job runs. This gives you
an easy way of having a shared disk in the cloud, but the size of that disk is
limited to the size of the manager's volume. Performance may also be poor. This
is only intended when you need a little bit of shared state between jobs, not
for writing lots of large files. (If you need a high performance shared disk,
don't use this option, and instead set up your own shared filesystem, eg.
GlusterFS, and specify a cloud_script that mounts it.)

"env" is an array of "key=value" environment variables, which override or add to
the environment variables the command will see when it runs. The base variables
that are overwritten depend on if you run 'wr add' on the same machine as you
started the manager (local, vs remote). In the local case, commands will use
base variables as they were at the moment in time you run 'wr add', so to set a
certain environment variable for all commands, you could instead just set it
prior to calling 'wr add'. In the remote case the command will use base
variables as they were on the machine where the command is executed when that
machine was started, unless --remote_same_as_local or the
ManagerRemoteSameAsLocal config option is enabled.

"bsub_mode" is a boolean that results in the job being assigned a unique (for
this manager session) job id, and turns on bsub emulation, which means that if
your Cmd calls bsub, it will instead result in a command being added to wr. The
new job will have this job's mount and cloud_* options.

Because those new jobs are given this job's working directory as their own
literal one, "bsub_mode" defaults "cwd_matters" to true, so that this job's
working directory is your own directory and not one wr made and may delete. An
explicit --cwd_matters=false overrides that default.

NB: When running with sudo that is configured to not pass through environmental
variables, you must have a wr config file, accessible from the working
directory, with ManagerHost, ManagerPort, and ManagerCertDomain set.`,
	Run: func(combraCmd *cobra.Command, _ []string) {
		// check the command line options
		if cmdFile == "" {
			die("--file is required")
		}

		if cmdWithDocker != "" && cmdWithSingularity != "" {
			die("--with_docker and --with_singularity are mutually exclusive")
		}

		if cmdHead < 0 {
			die("--head can't be negative")
		}

		timeout := time.Duration(timeoutint) * time.Second
		jq := connect(timeout)

		var err error
		defer func() {
			err = jq.Disconnect()
			if err != nil {
				warn("Disconnecting from the server failed: %s", err)
			}
		}()

		remoteSameAsLocalEnabled := remoteSameAsLocal(combraCmd.Flags().Changed("remote_same_as_local"))
		jobs, isLocal, defaultedRepG := parseCmdFile(jq, combraCmd.Flags().Changed("disk"), remoteSameAsLocalEnabled)

		if syncMode && len(jobs) != 1 {
			die("You must add exactly 1 command when using synchronous mode.")
		}

		envVars := addEnvVars(isLocal, remoteSameAsLocalEnabled)

		// add the jobs to the queue *** should add at most 1,000,000 jobs at a
		// time to avoid time out issues...
		if simpleOutput {
			ids, warnings, err := jq.AddAndReturnIDsWithWarnings(jobs, envVars, !cmdReRun)
			if err != nil {
				die("%s", err)
			}

			printAddWarnings(warnings)

			if len(ids) == 0 {
				os.Exit(1)
			}

			for _, id := range ids {
				fmt.Printf("%s\n", id)
			}
		} else if syncMode {
			synchronousAdd(jq, jobs[0], envVars, !cmdReRun)
		} else {
			inserts, dups, warnings, err := jq.AddWithWarnings(jobs, envVars, !cmdReRun)
			if err != nil {
				die("%s", err)
			}

			printAddWarnings(warnings)

			if defaultedRepG {
				fmt.Printf(
					"Added %d new commands (%d were duplicates) to the queue using default identifier '%s'\n",
					inserts,
					dups,
					cmdRepGroup,
				)
			} else {
				fmt.Printf("Added %d new commands (%d were duplicates) to the queue\n", inserts, dups)
			}
		}
	},
}

func init() {
	RootCmd.AddCommand(addCmd)

	// flags specific to this sub-command
	addCmdCoreFlags()
	addCmdResourceFlags()
	addCmdContainerFlags()
	addCmdCloudFlags()
	addCmdMiscFlags()

	err := addCmd.Flags().MarkHidden("reserve_timeout")
	if err != nil {
		die("cloud not hide reserver_timeout option: %s", err)
	}
}

// addCmdCoreFlags registers the core input/identification flags for the add
// command.
func addCmdCoreFlags() {
	flags := addCmd.Flags()

	flags.StringVarP(&cmdFile, "file", "f", "-",
		"file containing your commands; - means read from STDIN")
	flags.IntVar(&cmdHead, "head", 0,
		"only add the first N parsed commands from the command file; 0 means all")
	flags.StringVarP(&cmdRepGroup, "rep_grp", "i", "manually_added", "reporting group for your commands")
	flags.StringVarP(&cmdLimitGroups, "limit_grps", "l", "", "comma-separated list of limit groups")
	flags.StringVar(&cmdModules, "modules", "", "comma-separated list of environment modules to load")
	flags.StringVarP(&cmdDepGroups, "dep_grps", "e", "", "comma-separated list of dependency groups")
	flags.StringVarP(&cmdCwd, "cwd", "c", "", "base for the command's working dir")
	flags.BoolVar(&cmdCwdMatters, "cwd_matters", false, "--cwd should be used as the actual working directory")

	// cwdMatters() has to know whether --cwd_matters was GIVEN, not just its
	// value, and cannot ask addCmd: addCmd's own initialiser refers to
	// parseCmdFile, so a reference back to addCmd from anything parseCmdFile
	// calls is an initialisation cycle.
	cmdCwdMattersChanged = &flags.Lookup("cwd_matters").Changed
	flags.BoolVar(&cmdChangeHome, "change_home", false,
		"when not --cwd_matters, set $HOME to the actual working directory")
	flags.StringVar(&cmdGroup, "group", "", "unix group to start the command as")
}

// addCmdResourceFlags registers the resource-requirement and scheduling flags
// for the add command.
func addCmdResourceFlags() {
	flags := addCmd.Flags()

	flags.StringVarP(&reqGroup, "req_grp", "g", "", "group name for commands with similar reqs")
	flags.StringVarP(&cmdMem, "memory", "m", "1G",
		"peak mem est. [specify units such as M for Megabytes or G for Gigabytes]")
	flags.StringVarP(&cmdTime, "time", "t", "1h",
		"max time est. [specify units such as m for minutes or h for hours]")
	flags.Float64Var(&cmdCPUs, "cpus", 1, "cpu cores needed")
	flags.IntVar(&cmdDisk, "disk", 0, "number of GB of disk space required (default 0)")
	flags.StringVarP(&cmdOvr, "override", "o", "no",
		"[0|no|1|higher|2|always] should your mem/time estimates override? (default no)")
	flags.IntVarP(&cmdPri, "priority", "p", 0, "[0-255] command priority (default 0)")
	flags.IntVarP(&cmdRet, "retries", "r", defaultAddRetries,
		"[0-255] number of automatic retries for failed commands")
	flags.StringVarP(&cmdNoRetry, "no_retry_over_walltime", "n", "",
		"do not retry if cmd runs longer than this [specify units such as m for minutes or h for hours]")
	flags.StringVar(&cmdCmdDeps, "cmd_deps", "",
		"static command dependencies, in the form \"command1,cwd1,command2,cwd2...\"")
	flags.StringVarP(&cmdGroupDeps, "deps", "d", "",
		"live dep-group dependencies, in the form \"dep_grp1,dep_grp2...\"; unseen groups wait")
	flags.StringVar(&cmdQueue, "queue", "", "name of queue to submit to, for schedulers with queues")
	flags.StringVar(&cmdQueuesAvoidAdd, "queues_avoid", "interactive",
		"comma-separated list of substrings found in queues that should not be submitted to, "+
			"for schedulers with queues")
	flags.StringVar(&cmdMisc, "misc", "", "miscellaneous options to pass through to scheduler when submitting")
}

// addCmdContainerFlags registers the docker/singularity/mount related flags for
// the add command.
func addCmdContainerFlags() {
	flags := addCmd.Flags()

	flags.StringVar(&cmdMonitorDocker, "monitor_docker", "",
		"monitor resource usage of docker container with given --name or --cidfile path")
	flags.StringVar(&cmdWithDocker, "with_docker", "",
		"run the cmd inside a docker container running this image")
	flags.StringVar(&cmdWithSingularity, "with_singularity", "",
		"run the cmd inside a singularity container running this image")
	flags.StringVar(&cmdContainerMounts, "container_mounts", "",
		"mount additional locations inside your container")
	flags.StringVarP(&mountJSON, "mount_json", "j", "",
		"remote file systems to mount, in JSON format; see 'wr mount -h'")
	flags.StringVar(&mountSimple, "mounts", "",
		"remote file systems to mount, as a ,-separated list of [c|u][r|w]:bucket[/path]; see 'wr mount -h'")
}

// addCmdCloudFlags registers the cloud_* override flags for the add command.
func addCmdCloudFlags() {
	flags := addCmd.Flags()

	flags.StringVar(&cmdOsPrefix, "cloud_os", "",
		"in the cloud, prefix name of the OS image servers that run the commands must use")
	flags.StringVar(&cmdOsUsername, "cloud_username", "",
		"in the cloud, username needed to log in to the OS image specified by --cloud_os")
	flags.IntVar(&cmdOsRAM, "cloud_ram", 0,
		"in the cloud, ram (MB) needed by the OS image specified by --cloud_os")
	flags.StringVar(&cmdFlavor, "cloud_flavor", "",
		"in the cloud, exact name of the server flavor that the commands must run on")
	flags.StringVar(&cmdPostCreationScript, "cloud_script", "",
		"in the cloud, path to a start-up script that will be run on the servers created to run these commands")
	flags.StringVar(&cmdCloudConfigs, "cloud_config_files", "",
		"in the cloud, comma separated paths of config files to copy to servers created to run these commands")
	flags.BoolVar(&cmdCloudSharedDisk, "cloud_shared", false, "mount /shared")
}

// addCmdMiscFlags registers the remaining behavioural flags for the add
// command.
func addCmdMiscFlags() {
	flags := addCmd.Flags()

	flags.StringVar(&cmdOnFailure, "on_failure", "", "behaviours to carry out when cmds fails, in JSON format")
	flags.StringVar(&cmdOnSuccess, "on_success", "", "behaviours to carry out when cmds succeed, in JSON format")
	flags.StringVar(&cmdOnExit, "on_exit", `[{"cleanup":true}]`,
		"behaviours to carry out when cmds finish running, in JSON format")
	flags.StringVar(&cmdEnv, "env", "",
		"comma-separated list of key=value environment variables to set before running the commands")
	flags.BoolVar(&cmdRemoteSameAsLocal, "remote_same_as_local", false,
		"make remote-manager adds use the same cwd and environment behaviour as local-manager adds")
	flags.BoolVar(&cmdReRun, "rerun", false,
		"re-run any commands that you add that had been previously added and have since completed")
	flags.BoolVar(&cmdBsubMode, "bsub", false, "enable bsub emulation mode; implies --cwd_matters")
	flags.BoolVar(&cmdDisableRelativeCheck, "disable_relative_check", false,
		"disable the relative path checking when cwd_matters is false")
	flags.IntVar(&timeoutint, "timeout", defaultAddTimeout,
		"how long (seconds) to wait to get a reply from 'wr manager'")
	flags.IntVar(&rtimeoutint, "reserve_timeout", 1,
		"how long (seconds) to wait before a runner exits when there is no more work'")
	flags.BoolVarP(&simpleOutput, "simple", "s", false, "simplify output to only queued job ids")
	flags.BoolVar(&syncMode, "sync", false,
		"add a single job and wait for it to finish being executed by the manager")
}

func remoteSameAsLocal(flagChanged bool) bool {
	if flagChanged {
		return cmdRemoteSameAsLocal
	}

	return config != nil && config.ManagerRemoteSameAsLocal
}

func addEnvVars(isLocal bool, remoteSameAsLocal bool) []string {
	if isLocal || remoteSameAsLocal {
		return os.Environ()
	}

	return nil
}

type jobUpdateSubscription interface {
	Updates() <-chan *jobqueue.JobUpdate
	Unsubscribe()
}

type synchronousAddClient struct {
	*jobqueue.Client
}

func (c synchronousAddClient) SubscribeToJobKeys(ctx context.Context, keys []string) (jobUpdateSubscription, error) {
	return c.Client.SubscribeToJobKeys(ctx, keys)
}

func waitForSynchronousJob(ctx context.Context, jq synchronousAddWaiter, key string) (*jobqueue.Job, error) {
	sub, err := jq.SubscribeToJobKeys(ctx, []string{key})
	if err != nil {
		return nil, err
	}
	defer sub.Unsubscribe()

	for {
		update, err := receiveSynchronousJobUpdate(ctx, sub.Updates())
		if err != nil {
			return nil, err
		}

		if !isSynchronousTerminalUpdate(update, key) {
			continue
		}

		return getSynchronousJobByKey(jq, key)
	}
}

func receiveSynchronousJobUpdate(ctx context.Context, updates <-chan *jobqueue.JobUpdate) (*jobqueue.JobUpdate, error) {
	select {
	case update, ok := <-updates:
		if !ok {
			return nil, errSynchronousSubscriptionClosed
		}

		return update, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func isSynchronousTerminalUpdate(update *jobqueue.JobUpdate, key string) bool {
	if update == nil || update.Kind != jobqueue.JobUpdateTerminal || update.Key != key {
		return false
	}

	return update.State == jobqueue.JobStateComplete || update.State == jobqueue.JobStateBuried
}

func getSynchronousJobByKey(jq synchronousAddWaiter, key string) (*jobqueue.Job, error) {
	job, err := jq.GetByEssence(&jobqueue.JobEssence{JobKey: key}, true, false)
	if err != nil {
		return nil, err
	}

	if job == nil {
		return nil, fmt.Errorf("%w: %s", errSynchronousJobMissing, key)
	}

	return job, nil
}

func addAndWaitSynchronousJob(
	jq synchronousAddWaiter,
	job *jobqueue.Job,
	envVars []string,
	ignoreComplete bool,
) (*jobqueue.Job, bool) {
	ctx := context.Background()

	ids, warnings, err := jq.AddAndReturnIDsWithWarnings([]*jobqueue.Job{job}, envVars, ignoreComplete)
	if err != nil {
		die("%s", err)
	}

	printAddWarnings(warnings)

	if len(ids) == 0 {
		return nil, false
	}

	job, err = waitForSynchronousJob(ctx, jq, ids[0])
	if err != nil {
		die("%s", err)
	}

	return job, true
}

func printAddWarnings(warnings jobqueue.AddWarnings) {
	for _, group := range warnings.NeverSeenDepGroups {
		fmt.Fprintf(
			os.Stderr,
			"dependency group %q has not been seen; dependent job(s) will wait until it appears\n",
			group,
		)
	}
}

func printSynchronousJobOutput(job *jobqueue.Job) {
	stdout, err := job.StdOut()
	if err == nil && stdout != "" {
		fmt.Println(stdout)
	}

	stderr, err := job.StdErr()
	if err == nil && stderr != "" {
		fmt.Fprintln(os.Stderr, stderr)
	}
}

type synchronousAddWaiter interface {
	AddAndReturnIDsWithWarnings(jobs []*jobqueue.Job, envVars []string,
		ignoreComplete bool) ([]string, jobqueue.AddWarnings, error)
	SubscribeToJobKeys(ctx context.Context, keys []string) (jobUpdateSubscription, error)
	GetByEssence(essence *jobqueue.JobEssence, getStd bool, getEnv bool) (*jobqueue.Job, error)
}

// convert cmd,cwd columns in to Dependency.
func colsToDeps(cols []string) (deps jobqueue.Dependencies) {
	for i := 0; i < len(cols); i += 2 {
		deps = append(deps, jobqueue.NewEssenceDependency(cols[i], cols[i+1]))
	}

	return
}

// convert group1,group2,... in to a Dependency.
func groupsToDeps(groups string) (deps jobqueue.Dependencies) {
	for depgroup := range strings.SplitSeq(groups, ",") {
		deps = append(deps, jobqueue.NewDepGroupDependency(depgroup))
	}

	return
}

// parseMemoryMBOrDie converts a --memory style value (eg. "1G") to megabytes,
// returning 0 for an empty value and calling die() if the value is invalid or
// too large to represent.
func parseMemoryMBOrDie(value string) int {
	if value == "" {
		return 0
	}

	mb, err := bytefmt.ToMegabytes(value)
	if err != nil {
		die("--memory was not specified correctly: %s", err)
	}

	ram, err := strconv.Atoi(strconv.FormatUint(mb, 10))
	if err != nil {
		die("--memory was not specified correctly: %s", err)
	}

	return ram
}

// parseDurationOrDie converts a --time style value (eg. "1h") to a Duration,
// returning 0 for an empty value and calling die() (referencing flagName) if
// the value is invalid.
func parseDurationOrDie(value, flagName string) time.Duration {
	if value == "" {
		return 0 * time.Second
	}

	d, err := time.ParseDuration(value)
	if err != nil {
		die("%s was not specified correctly: %s", flagName, err)
	}

	return d
}

// defaultCwd determines the default command working directory to use when
// --cwd was not supplied. It returns the empty string (and no warning) when
// --cwd was supplied. Otherwise it returns addFromDir() when the manager is
// local, cwdMatters(), or remote adds should behave like local adds, and /tmp
// (with remoteWarning true) when the manager is remote.
func defaultCwd(isLocal, remoteSameAsLocal bool) (pwd string, remoteWarning bool) {
	if cmdCwd != "" {
		return "", false
	}

	wd := addFromDir()

	if isLocal || cwdMatters() || remoteSameAsLocal {
		return wd, false
	}

	return "/tmp", true
}

// addFromDir returns the directory this add should be treated as having been
// made from: the cwd of the job we are running inside, if we are running inside
// one, and the actual current directory otherwise.
//
// A job's command runs in a disposable working directory wr made below the job's
// cwd, so os.Getwd() inside a job is that working directory. Defaulting a new
// job to it would put the new job's working directory inside the adding job's,
// where the adding job's cleanup behaviour deletes the new job's live work, and
// where a still-running new job stops the adding job's working directory ever
// being reclaimed. The runner tells us the adding job's own cwd so that we can
// default to that instead, making the new job's working directory a sibling of
// the adding job's.
//
// A relative value is ignored because it would resolve inside the working
// directory we are trying to avoid.
func addFromDir() string {
	if jobCwd := os.Getenv(jobqueue.JobCwdEnvVar); filepath.IsAbs(jobCwd) {
		return jobCwd
	}

	wd, err := os.Getwd()
	if err != nil {
		die("%s", err)
	}

	return wd
}

// cwdMatters reports whether --cwd should be taken as the literal command
// working directory rather than as the parent to create one below.
//
// --bsub defaults it to true. A bsub-mode job's Cmd submits its children with
// `wr bsub`, which gives them the submitting Cmd's own working directory as
// their Cwd, with CwdMatters true (cmd/lsf.go) - so a wr-created working
// directory for the parent becomes the children's shared submission directory,
// and the parent's cleanup behaviour deletes their live work. The parent is the
// only job in a --bsub tree that creates a working directory, the children being
// cwd_matters already, so not creating it removes the whole class.
//
// An explicit --cwd_matters=false still wins.
func cwdMatters() bool {
	given := cmdCwdMattersChanged != nil && *cmdCwdMattersChanged

	if cmdBsubMode && !given {
		return true
	}

	return cmdCwdMatters
}

// parseJobViaJSON converts the columns of a single commands-file line into a
// JobViaJSON. With two columns the second is a JSON object and the first is the
// command; with one column the value is either a JSON object (if it starts with
// '{') or a bare command.
func parseJobViaJSON(cols []string, colsn int) (*jobqueue.JobViaJSON, error) {
	var jvj *jobqueue.JobViaJSON

	if colsn == maxCmdFileColumns {
		if err := json.Unmarshal([]byte(cols[1]), &jvj); err != nil {
			return nil, err
		}

		jvj.Cmd = cols[0]

		return jvj, nil
	}

	if !strings.HasPrefix(cols[0], "{") {
		return &jobqueue.JobViaJSON{Cmd: cols[0]}, nil
	}

	if err := json.Unmarshal([]byte(cols[0]), &jvj); err != nil {
		return nil, err
	}

	return jvj, nil
}

// lsfResourceInvalid reports whether the job specifies a scheduler_misc
// resource string that is invalid for the LSF scheduler.
func lsfResourceInvalid(scheduler string, job *jobqueue.Job, validator jscheduler.BsubValidator) bool {
	sm := job.Requirements.Other["scheduler_misc"]
	if sm == "" || scheduler != schedulerLSF {
		return false
	}

	return !validator.Validate(sm, job.Requirements.Other["scheduler_queue"])
}

// parseCmdFile reads the given cmd file to get desired jobs, modified by
// defaults specified in other command line args. Returns job slice, bool for if
// the manager is on the same host as us, and bool for if any job defaulted to
// the default repgrp.
//
//nolint:gocognit,gocyclo,cyclop,funlen,maintidx // Legacy parser is broad; this change only threads remote defaults.
func parseCmdFile(jq *jobqueue.Client, diskSet bool, remoteSameAsLocal bool) ([]*jobqueue.Job, bool, bool) {
	var isLocal bool

	currentIP, errc := internal.CurrentIP("")
	if errc != nil {
		warn("Could not get current IP: %s", errc)
	}

	if currentIP+":"+config.ManagerPort == jq.ServerInfo.Addr {
		isLocal = true
	}

	// if the manager is remote, copy over any cloud config files to unique
	// locations, and adjust cloudConfigFiles to make sense from the manager's
	// perspective
	if !isLocal && cmdCloudConfigs != "" {
		cmdCloudConfigs = copyCloudConfigFiles(jq, cmdCloudConfigs)
	}

	bsubMode := ""
	if cmdBsubMode {
		bsubMode = deployment
	}

	if cmdCPUs < 0 {
		die("--cpus can't be negative")
	}

	jd := &jobqueue.JobDefaults{
		RepGrp:               cmdRepGroup,
		ReqGrp:               reqGroup,
		Group:                cmdGroup,
		Cwd:                  cmdCwd,
		CwdMatters:           cwdMatters(),
		ChangeHome:           cmdChangeHome,
		CPUs:                 cmdCPUs,
		Disk:                 cmdDisk,
		DiskSet:              diskSet,
		Override:             overrideStringToInt(cmdOvr),
		Priority:             cmdPri,
		Retries:              cmdRet,
		Env:                  cmdEnv,
		MonitorDocker:        cmdMonitorDocker,
		WithDocker:           cmdWithDocker,
		WithSingularity:      cmdWithSingularity,
		ContainerMounts:      cmdContainerMounts,
		CloudOS:              cmdOsPrefix,
		CloudUser:            cmdOsUsername,
		CloudScript:          cmdPostCreationScript,
		CloudConfigFiles:     cmdCloudConfigs,
		CloudOSRam:           cmdOsRAM,
		CloudFlavor:          cmdFlavor,
		CloudShared:          cmdCloudSharedDisk,
		SchedulerQueue:       cmdQueue,
		SchedulerQueuesAvoid: cmdQueuesAvoidAdd,
		SchedulerMisc:        cmdMisc,
		BsubMode:             bsubMode,
		RTimeout:             rtimeoutint,
	}

	if jd.RepGrp == "" {
		jd.RepGrp = "manually_added"
	}

	jd.Memory = parseMemoryMBOrDie(cmdMem)
	jd.Time = parseDurationOrDie(cmdTime, "--time")
	jd.NoRetriesOverWalltime = parseDurationOrDie(cmdNoRetry, "--no_retry_over_walltime")

	if cmdLimitGroups != "" {
		jd.LimitGroups = strings.Split(cmdLimitGroups, ",")
	}

	if cmdModules != "" {
		jd.Modules = strings.Split(cmdModules, ",")
	}

	if cmdDepGroups != "" {
		jd.DepGroups = strings.Split(cmdDepGroups, ",")
	}

	if cmdCmdDeps != "" {
		cols := strings.Split(cmdCmdDeps, ",")
		if len(cols)%2 != 0 {
			die("--cmd_deps must have an even number of comma-separated entries")
		}

		jd.Deps = colsToDeps(cols)
	}

	if cmdGroupDeps != "" {
		jd.Deps = append(jd.Deps, groupsToDeps(cmdGroupDeps)...)
	}

	if cmdOnFailure != "" {
		var bjs jobqueue.BehavioursViaJSON

		err := json.Unmarshal([]byte(cmdOnFailure), &bjs)
		if err != nil {
			die("bad --on_failure: %s", err)
		}

		jd.OnFailure = bjs.Behaviours(jobqueue.OnFailure)
	}

	if cmdOnSuccess != "" {
		var bjs jobqueue.BehavioursViaJSON

		err := json.Unmarshal([]byte(cmdOnSuccess), &bjs)
		if err != nil {
			die("bad --on_success: %s", err)
		}

		jd.OnSuccess = bjs.Behaviours(jobqueue.OnSuccess)
	}

	if cmdOnExit != "" {
		var bjs jobqueue.BehavioursViaJSON

		err := json.Unmarshal([]byte(cmdOnExit), &bjs)
		if err != nil {
			die("bad --on_exit: %s", err)
		}

		jd.OnExit = bjs.Behaviours(jobqueue.OnExit)
	}

	if mountJSON != "" || mountSimple != "" {
		jd.MountConfigs = mountParse(mountJSON, mountSimple)
	}

	// open file or set up to read from STDIN
	var reader io.Reader = os.Stdin

	if cmdFile != "-" {
		file, erro := os.Open(cmdFile)
		if erro != nil {
			die("could not open file '%s': %s", cmdFile, erro)
		}

		defer internal.LogClose(context.Background(), file, "cmds file", "path", cmdFile)

		reader = file
	}

	// we'll default to pwd if the manager is on the same host as us, or if
	// cwd matters, or if remote adds should behave like local adds; /tmp
	// otherwise (and cmdCwd has not been supplied).
	pwd, remoteWarning := defaultCwd(isLocal, remoteSameAsLocal)

	// for network efficiency, read in all commands and create a big slice
	// of Jobs and Add() them in one go afterwards
	var jobs []*jobqueue.Job

	scanner := bufio.NewScanner(reader)
	buf := make([]byte, maxScanTokenSize)
	scanner.Buffer(buf, maxScanTokenSize)

	defaultedRepG := false
	lineNum := 0
	validator := make(jscheduler.BsubValidator)
	warnedAboutRelative := false
	cwdContents := make(map[string]map[string]bool)

	for scanner.Scan() {
		lineNum++
		cols := strings.Split(scanner.Text(), "\t")

		colsn := len(cols)
		if colsn < 1 || cols[0] == "" {
			continue
		}

		if colsn > maxCmdFileColumns {
			die("line %d has too many columns; check `wr add -h`", lineNum)
		}

		// determine all the options for this command
		jvj, jsonErr := parseJobViaJSON(cols, colsn)
		if jsonErr != nil {
			die("line %d had a problem with the JSON: %s", lineNum, jsonErr)
		}

		if jvj.CPUs != nil && *jvj.CPUs < 0 {
			die("line %d has a negative cpus count", lineNum)
		}

		if jvj.Cwd == "" && jd.Cwd == "" {
			if remoteWarning {
				warn("command working directories defaulting to %s since the manager is running remotely", pwd)
			}

			jd.Cwd = pwd
		}

		if jvj.RepGrp == "" {
			defaultedRepG = true
		}

		if !isLocal && jvj.CloudConfigFiles != "" {
			jvj.CloudConfigFiles = copyCloudConfigFiles(jq, jvj.CloudConfigFiles)
		}

		job, errf := jvj.Convert(jd)
		if errf != nil {
			die("line %d had a problem: %s", lineNum, errf)
		}

		if lsfResourceInvalid(jq.ServerInfo.Scheduler, job, validator) {
			die("invalid lsf resource string")
		}

		checkForRelativePathsInNonCwdMatters(&warnedAboutRelative, cwdContents, job)

		jobs = append(jobs, job)
		if cmdHead > 0 && len(jobs) >= cmdHead {
			break
		}
	}

	serr := scanner.Err()
	if serr != nil {
		die("failed to read whole file: %s", serr.Error())
	}

	return jobs, isLocal, defaultedRepG
}

// copyCloudConfigFiles copies local config files to the manager's machine to a
// path based on the file's MD5, and then returns an altered input value to use
// the MD5 paths as the sources, keeping the desired destinations. It does not
// alter path specs for config files that don't exist locally.
func copyCloudConfigFiles(jq *jobqueue.Client, configFiles string) string {
	cfs := strings.Split(configFiles, ",")

	remoteConfigFiles := make([]string, 0, len(cfs))
	for _, cf := range cfs {
		remoteConfigFiles = append(remoteConfigFiles, copyCloudConfigFile(jq, cf))
	}

	return strings.Join(remoteConfigFiles, ",")
}

// copyCloudConfigFile copies a single local config file to the manager's
// machine to an MD5-based path, returning a spec using that path as the source
// while keeping the desired destination. The original spec is returned
// unchanged if the local file doesn't exist or the upload fails.
func copyCloudConfigFile(jq *jobqueue.Client, cf string) string {
	parts := strings.Split(cf, ":")
	local := internal.TildaToHome(parts[0])

	if _, err := os.Stat(local); err != nil {
		return cf
	}

	desired := parts[0]
	if len(parts) == configFilePathParts {
		desired = parts[1]
	}

	remote, err := jq.UploadFile(local, "")
	if err != nil {
		warn("failed to upload [%s] to a unique location: %s", local, err)

		return cf
	}

	return remote + ":" + desired
}

// checkForRelativePathsInNonCwdMatters checks the cmd of jobs where cwd doesn't
// matter and warns once if it contains relative paths.
func checkForRelativePathsInNonCwdMatters(
	warnedAboutRelative *bool, cwdContents map[string]map[string]bool, job *jobqueue.Job) {
	if job.CwdMatters || cmdDisableRelativeCheck || *warnedAboutRelative {
		return
	}

	filesInDir, ok := cwdContents[job.Cwd]
	if !ok {
		filesInDir = internal.GetFilesInDir(job.Cwd)
		cwdContents[job.Cwd] = filesInDir
	}

	if internal.CmdlineHasRelativePaths(filesInDir, job.Cwd, job.Cmd) {
		warn("a job may fail because it seems to contain relative paths, but the job would run in " +
			"a newly created unique sub-directory.\n" +
			"Use absolute paths instead, or say --cwd_matters.\n" +
			"You can also use --disable_relative_check to disable this warning.")

		*warnedAboutRelative = true
	}
}

// synchronousAdd adds one job and waits for it to complete, then outputs its
// stdout&err and exits with its exit code.
func synchronousAdd(jq *jobqueue.Client, job *jobqueue.Job, envVars []string, ignoreComplete bool) {
	synchronousAddWithExit(synchronousAddClient{Client: jq}, job, envVars, ignoreComplete, os.Exit)
}

func synchronousAddWithExit(jq synchronousAddWaiter, job *jobqueue.Job, envVars []string, ignoreComplete bool,
	exit func(int)) {
	job, added := addAndWaitSynchronousJob(jq, job, envVars, ignoreComplete)
	if !added {
		exit(1)

		return
	}

	printSynchronousJobOutput(job)
	exit(job.Exitcode)
}

func overrideStringToInt(input string) int {
	switch input {
	case "0", "no", "n":
		return overrideNo
	case "1", "higher", "h":
		return overrideHigher
	case "2", "always", "a":
		return overrideAlways
	default:
		die("invalid override value")

		return -1
	}
}
