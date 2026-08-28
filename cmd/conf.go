/*******************************************************************************
 * Copyright (c) 2016-2022, 2025-2026 Genome Research Ltd.
 *
 * Author: Sendu Bala <sb10@sanger.ac.uk>
 * Author: [Theo Barber-Bany] <theobarberbany@gmail.com>
 * Author: Ashwini Chhipa <ac55@sanger.ac.uk>
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
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

const defaultYML = `# The format of this file is YAML

# managerport: What port should the wr manager listen on?
# This defaults to "xxxxx", where xxxxx is 1021 + 4*[your user id] + 0 if
# production or + 2 if development. Note, this is a string (quoted). The
# calculated default should hopefully give you port numbers that no other
# software or other user of wr on your machine is using.
# NB: It is very important to have different settings for your production
# manager and your development manager. If you have multiple people running
# wr on the same machine, and you explicitly set this instead of relying on
# the default, each individual person should have their own unique manager_port
# specified in their personal ~/.wr_config.development.yml and
# ~/.wr_config.production.yml files.
#
# Before being able to use wr you must start the manager by running 'wr
# manager'. It will start listening on the specified port on your local host.
# Your other invocations of 'wr' also use this option to know what port to
# connect to, but they'll only succeed if you run them from the same host you
# started the manager on, or if you have set the manager_host option to the
# host you started the manager on.
# wr commands that are spawned by the manager itself are given the real
# ip address of the host the manager is running on, so these commands do not
# need manager_host to be set.
# For multi-machine systems it is required that all hosts that could end up
# running a wr command be able to do tcp communication with the host you
# launch the manager on.
#managerport: "11301"

# managerweb: What port should the wr manager serve its web interface on?
# This defaults to "xxxxx", where xxxxx is 1021 + 4*[your user id] + 1 if
# production or + 3 if development. Note, this is a string (quoted). The
# calculated default should hopefully give you port numbers that no other
# software or other user of wr on your machine is using.
# NB: This must be different to the manager_port, and to anyone else's port
# choice on the same machine.
#managerweb: "11302"

# managerhost: What host was 'wr manager' started on?
# This is optional and defaults to "localhost".
#
# This option determines where wr commands (other than the manager command)
# try and connect to your wr manager. You only need to set this if you plan
# on running wr commands yourself on a host that is different to the one you
# you plan to start the wr manager on.
# For more details, see the notes for the manager_port option above.
managerhost: "localhost"

# managerdir: Where should the wr manager store its working files?
# This defaults to a directory prefixed with .wr in your home directory.
#
# The final directory name will be suffixed with "_[deployment]", eg. by default
# when developing the directory will be ~/.wr_development. For this reason
# you do not have to set this differently in your production and development
# config files. The other file-name-based configuration options like
# 'manager_pid_file' and 'manager_db_file' also do not need to be altered from
# their defaults.
#
# The files stored in here are, by default, the manager's pid file, log file and
# database related files. Files needed by 'wr cloud deploy' are also stored
# here.
managerdir: "~/.wr"

# managerpidfile: Where should wr manager store its pid file?
# This defaults to a file named "pid" in managerdir.
#
# You can set this to an absolute path to ignore managerdir; for example if
# you have the root permissions to set things up, you may prefer to set this to
# /var/run/wr/pid
managerpidfile: "pid"

# managerlogfile: Where should wr manager store its log file?
# This defaults to a file named "log" in managerdir.
#
# You can set this to an absolute path to ignore managerdir; for example if
# you have the root permissions to set things up, you may prefer to set this to
# /var/log/wr/pid
managerlogfile: "log"

# managerdbfile: Where should wr manager store its database file?
# This defaults to a file named "db" in managerdir.
#
# You can set this to an absolute path to ignore managerdir. If doing this, be
# certain to set a different value in your production and development config
# files.
#
# Note that you may need quite a lot of disk space for this, especially after
# you've run millions of jobs, since a permanent record of everything you've
# done is held in this file.
#
# WARNING: the database file will eventually contain your environment variables,
# so you should secure this file and not make it public if you have passwords
# set as the values of environment variables.
managerdbfile: "db"

# managerdbbkfile: Where should wr manager back up its database file?
# This defaults to a file named "db_bk" in managerdir.
#
# You can set this to an absolute path to ignore managerdir (and ideally you
# should set this to a path on a different disk or better yet a different
# machine).
#
# Database backups are not carried out in development, so this is ignored in
# development config files.
#
# For cloud deployments it is recommended to back up to S3. Specify an S3
# location like: s3://mybucket/subpath/my_wr_db.backup
# If your credentials are specified in a non-default profile you can instead say
# something like: s3://profile_name@mybucket/subpath/my_wr_db.backup
# Credential specification is as per 'wr mount -h' (basically, have an ~/.s3cfg
# file). Ensure that only you have read permission for the S3 location you
# specify.
# NB: for S3 backups to work, you must be able to carry out fuse mounts, which
# means fuse-utils must be installed, and /etc/fuse.conf should have
# user_allow_other set.
#
# Note that you may need quite a lot of disk space for this, as when a
# new backup starts it is written to a temp file in the directory you specify
# before replacing the file at the path you specified, so peaking to 2x disk
# usage.
managerdbbkfile: "db_bk"

# managerdbbatchdelay: How many milliseconds may the manager's database wait to
# coalesce its BoltDB Batch() writes - not its job add, job state-change or job
# archive writes - into a single disk commit?
#
# This sets BoltDB's MaxBatchDelay, which applies only to the writes the
# manager makes through bolt's Batch() call: Batch() calls arriving from
# different goroutines within this window are grouped into one commit, with one
# fsync. Durability is unchanged whatever you set: every commit is still
# fsync'd to disk.
#
# It does NOT govern the manager's three busiest write paths. Job state
# changes, job archiving (a job finishing) and job adding each have their own
# single coalescing writer goroutine, which folds everything pending into one
# bolt Update() transaction; that coalesces ACROSS a commit rather than within
# a fixed window, and this knob cannot reach it.
#
# What it does still govern:
#   - defining or changing limit groups (db.storeLimitGroups): wr limit, and
#     the limit groups an added job declares
#   - storing a client environment the database has not seen before
#     (db.storeEnv's db.store): once per distinct environment, on add
#   - removing jobs (db.deleteLiveJobs): wr remove, plus one write per job for
#     jobs carrying the Remove behaviour (added with eg. --on_failure
#     '[{"remove":true}]'), which the manager deletes as it buries them
#   - modifying jobs (db.modifyLiveJobs): wr mod
#   - one add whose jobs do not fit a single write transaction
#     (db.storeNewJobDataChunked), which is split into several
#
# All of those are occasional except one: the Remove behaviour fires once per
# job, not once per command you run, so a workload that uses it and buries jobs
# in bulk is the one case here that gives a wider window plenty to combine.
#
# A value of 0 (the default) uses wr's built-in default of 10ms (bbolt's own
# default). Raising it (for example to 25-50ms) can only help if BOTH of the
# following hold: the manager directory (managerdir) is on storage with high
# fsync latency, such as a networked filesystem (NFS, Lustre, etc.), where each
# commit is far more expensive; AND enough of the writes listed above are in
# flight at once for a wider window to have something to combine. Otherwise you
# only add latency to each of them.
#
# For the add path the measured verdict is the opposite. A 500ms window was
# tried as a fix for a production add-latency collapse and refuted: it reached
# 96.64 adds/s against a ~100/s pass mark, and cost the low-concurrency case
# its latency (p50 124ms -> 480ms), because the add path's own coalescing
# writer supersedes the window (see .docs/reliable4/addstorm-fix-trials.md,
# sections T1 and SUMMARY).
managerdbbatchdelay: 0

# managerdbbatchsize: How many of the manager's BoltDB Batch() writes - not its
# job add, job state-change or job archive writes - may coalesce into a single
# disk commit before committing early?
#
# This is the companion cap to managerdbbatchdelay, with exactly the same
# scope: it reaches the Batch() writes listed above and no others. Once this
# many of them have accumulated, the batch commits immediately rather than
# waiting out the delay. It should comfortably exceed the number of writes you
# expect to be in flight at once so that the delay, not this cap, governs
# coalescing.
#
# A value of 0 (the default) uses wr's built-in default of 10000.
managerdbbatchsize: 0

# managertokenfile: Where should the manager store the authentication token?
# This defaults to a file named "client.token" in managerdir.
#
# You can set this to an absolute path to ignore managerdir. It should be on a
# shared disk (or copied, which cloud schedulers do for you) so that clients
# running on any machine in your cluster are able to read the file.
#
# When the manager starts it returns an authentication token that must be
# supplied when interacting with the manager via the web interface or REST API.
# The token is also stored in this file, which the CLI commands will read in
# order to get the token. The file will only be readable by the person who
# starts the manager, and so in this way the manager will only be usable by that
# person (or anyone they choose to share the token with).
managertokenfile: "client.token"

# managercertfile: Where is the certificate PEM file the manager should use?
# This defaults to a file named "cert.pem" in managerdir.
#
# You can set this to an absolute path to ignore managerdir.
#
# If this file or managerkeyfile do not exist, a certificate will be generated
# for you. This file is used along with managerkeyfile to secure access to the
# web interface using TLS. A generated certificate will result in a security
# warning in your browser when using the web interface that you will have to
# allow an exception for.
# It will also create managercafile (see below), which can be used by other
# clients to establish trust in this certificate. Note that generation will fail
# if managercafile or managerkeyfile already exist.
#
# The generated certificate is valid for 1 year. After that you'll need to
# delete it and have wr generate a new one the next time you start the manager.
#
# If you're using your own certificate and key, you should note that for cloud
# deployments these are copied to the cloud server for use by the manager there;
# you may wish to create a certificate and key dedicated to wr, incase access to
# your cloud is compromised.
managercertfile: "cert.pem"

# managerkeyfile: Where is the key PEM file the manager should use?
# This defaults to a file named "key.pem" in managerdir.
#
# You can set this to an absolute path to ignore managerdir.
#
# If this file or managercertfile do not exist, a key will be generated for you;
# see notes for managercertfile.
managerkeyfile: "key.pem"

# managercafile: Where is the CA (certificate authority) PEM file stored?
# This defaults to a file named "ca.pem" in managerdir.
#
# You can set this to an absolute path to ignore managerdir.
#
# If managercertfile and managerkeyfile are generated because they don't exist,
# this file will also be created (generation will fail if this file already
# exists). It contains the certificate for a CA that was used to sign
# managercertfile. This ca.pem can then be passed to clients to establish trust
# in managercertfile, eg. 'curl --cacert ~/.wr_production/ca.pem [...]'.
#
# If you're using your own managercertfile and managerkeyfile, you should set
# this to the cert of the CA you used to sign your managercertfile, if any
# client might run on a machine that does not have this CA cert installed at the
# usual location for that machine's Operating System (eg. when doing a cloud
# deployment to OpenStack and using an internal CA).
managercafile: "ca.pem"

# managercertdomain: What domain should clients use for verifying the TLS cert?
# This defaults to "localhost".
#
# This domain is used by wr command line clients to verify that the certifcate
# of the manager is valid. It is also displayed as the domain to connect to
# after you start the manager (it is up to you to ensure that the domain points
# to the machine you started the manager on).
#
# If managercertfile was generated by wr, then using "localhost" is fine, even
# for clients that aren't running on the same host as the manager.
#
# If using your own managercertfile, you will need to specify a domain that your
# certifcate is valid for.
managercertdomain: "localhost"

# managersetdomainip: Should your domain's IP be set after the manager starts?
# This defaults to false, meaning nothing is attempted. It is overridden by the
# --set_domain_ip option to 'wr manager start' and 'wr cloud deploy'.
#
# Making this option true will result in infoblox being used to first delete all
# A records for managercertdomain, then create an A record for managercertdomain
# that points to the IP address of the server that the manager was started on.
#
# The above will happen only after a successful 'wr manager start' or
# 'wr cloud deploy', and is an easy alternative to the deploysuccessscript
# option for the later.
#
# Requires the environment variables INFOBLOX_HOST, INFOBLOX_USER and
# INFOBLOX_PASS to be set. Your infoblox account will need permission to alter A
# records for managercertdomain.
# managersetdomainip: false

# managerremotesameaslocal: Should 'wr add' treat remote managers like local
# managers when choosing defaults?
# This defaults to false, preserving remote jobs' usual base environment and
# defaulting the working directory to /tmp unless cwd_matters is enabled. Making
# this option true means remote adds use the submitter's current environment as
# the base and default to the submitter's current working directory, matching
# local-manager adds.
managerremotesameaslocal: false

# managerumask: What umask should be used when wr manager creates files?
# This defaults to 007 (user+group read+writable, no access to others).
# Note, this is a number (no quotes).
#
# Here are examples of alternative umasks:
# 022 = world readable, user read+writeable
# 002 = world readable, user+group read+writeable
managerumask: 007

# managerscheduler: What job scheduler should be used to run 'wr runner'?
# This defaults to "local" and is overridden by the --scheduler option to
# 'wr manager start'.
#
# "local" means run everything on the local machine.
# "lsf" means submit to LSF using 'bsub'.
# "openstack" means spawn additional openstack servers in the current network
# as necessary to run your commands, and destroy them afterwards. NB: this only
# works if you are starting the manager on an OpenStack server!
managerscheduler: "local"

# manageruploaddir: Where should the wr manager store uploaded files?
# This defaults to a dir named "uploads" in managerdir.
#
# This option is only relevant when you are using a cloud scheduler such as
# OpenStack.
#
# This directory may be used to store a small handful of small files such as
# cloud script and cloud config files, when --cloud_script or
# --cloud_config_files options are passed to "wr add".
manageruploaddir: "uploads"

# runnerexecshell: What shell should be used to run commands in?
# This defaults to bash, regardless of your current shell.
#
# Avoid the use of dash on Ubuntu, which is its default sh; bash is STRONGLY
# recommended.
runnerexecshell: "bash"

# privatekeypath: path to your private key.
# This defaults to ~/.ssh/id_rsa.
#
# This may be used by some schedulers (currently only LSF) to ssh to servers in
# order to check on jobs that lose contact with the wr manager.
#
# When an LSF job stops touching the manager within its time-to-release, the
# manager sshes to the job's host (as you, using this key) to check if the
# job's process is really dead before reclaiming it. If it cannot confirm the
# process is dead, the job is left occupying its slot (including any limit-group
# slot). So if this check never succeeds, lost jobs are never reclaimed, limit
# groups fill up with dead-but-uncleared jobs, and scheduling can grind to a
# halt. The exact command the manager runs over ssh is:
#
#   ps -o stat= -p <pid> 2>/dev/null || test $? -eq 1
#
# and it treats EMPTY output as "process is dead" (a non-empty process state, or
# any ssh error, means "still running / cannot confirm").
#
# For security you may want to restrict this key so it can ONLY run that ps
# check on your farm nodes, via a forced command in the remote
# ~/.ssh/authorized_keys. IMPORTANT: the forced command must reproduce the raw
# 'ps -o stat=' output above (empty for a dead pid) - a wrapper that instead
# returns a transformed value (e.g. a line count from '... | wc -l') will make
# every check look like "still running" and cause the stall described above. A
# working example (single line; substitute your own key and comment):
#
` +
	`#   command="p=$(echo \"$SSH_ORIGINAL_COMMAND\" | grep -oE '[-]p [0-9]+' | grep -oE '[0-9]+' | head -1); ` +
	`ps -o stat= -p \"${p:-0}\" 2>/dev/null || test $? -eq 1" ` +
	`ssh-ed25519 AAAA...your-public-key... wr lost-job ps check` + `
#
# This extracts only the pid from whatever command wr sends and runs the ps
# check on it, so the key cannot be used to run anything else, while still
# returning exactly what the manager expects.
#
# BACKSTOP KILL (optional): wr can additionally force-kill a wedged runner that
# has gone silent for far longer than any plausible archive delay
# (ServerConfig.Timings.LostRunnerBackstop, default 1h), so its limit-group slot
# is reclaimed rather than held indefinitely. To ENABLE that in production the key
# must ALSO permit 'kill -9 <pid>'. If you keep the ps-only example above, the
# backstop simply degrades to a harmless ps (no kill) and wedged jobs stay parked
# exactly as they do today - no regression. To enable the kill, install an UPDATED
# forced command that branches on what wr sends (single line; substitute your own
# key and comment):
#
` +
	`#   command="c=\"$SSH_ORIGINAL_COMMAND\"; p=$(echo \"$c\" | grep -oE '[-]p [0-9]+' | ` +
	`grep -oE '[0-9]+' | head -1); case \"$c\" in kill*) kill -9 \"${p:-0}\" 2>/dev/null || true;; ` +
	`*) ps -o stat= -p \"${p:-0}\" 2>/dev/null || test $? -eq 1;; esac" ` +
	`ssh-ed25519 AAAA...your-public-key... wr lost-job ps + backstop kill` + `
#
# This still only ever runs 'ps' or 'kill -9' on a digits-only pid extracted from
# what wr sends - no arbitrary commands, no injection. wr sends either the ps check
# 'ps -o stat= -p <pid> 2>/dev/null || test $? -eq 1' (matched by the *) branch) or
# the kill 'kill -9 <pid> 2>/dev/null || true # wr-kill -p <pid>' (matched by the
# kill*) branch); both carry the pid in the same '-p <pid>' token the extractor
# reads. And because the forced command runs as the login user (not root), 'kill
# -9' can only signal THAT user's own processes - exactly the wr runners you already
# own on that node - so the added privilege over ps-only is modest.
privatekeypath: "~/.ssh/id_rsa"

# cloudflavor: What server flavors can be automatically picked?
# Without being set, any available flavor can be picked. It is overridden by
# the --flavor option to 'wr cloud deploy' and the --cloud_flavor option of
# 'wr manager start'.
# Note, this is regular expression in a string, and could be something like
# "^m.*$" to only pick flavors that have names beginning with the letter 'm'.
#
# This option is only relevant when you are using a cloud scheduler such as
# OpenStack.
#
# wr will pick the cheapest (smallest number of cores and RAM) server flavor
# available to run a command, that is capable of running the command (according
# to wr's knowledge of how much RAM and how many cores it needs to run).
# cloudflavor: ""

# cloudflavormanager: What server flavors can be used for the manager?
# Without being set, any available flavor can be picked. It is overridden by
# the --manager_flavor option to 'wr cloud deploy'.
# 
# This is like cloudflavor, but only applies to the first server created on
# which 'wr manager' is started.
#
# If blank, defaults to the value of cloudflavor.
# cloudflavormanager: ""

# cloudflavorsets: What server flavors are assigned to different hardware?
# Without being provided, all flavors are assumed to be able to be used on all
# available hardware. This is overridden by the --flavor_sets option to 'wr
# cloud deploy' and the --cloud_flavor_sets option of 'wr manager start'.
# Note, this takes the form f1,f2;f3,f4 to describe flavors f1 and f2 being in
# the same set, and flavors f3 and f4 being in a different set. Flavors in the
# same set should be those that can only be used on a certain subset of your
# hardware, and flavors in a different set should be those that can only be
# brought up on a diffferent subset of hardware. The flavor names are treated
# as regular expressions, so you can have 1 expression that matches all the
# flavors in a set.
#
# This option is only relevant when you are using a cloud scheduler such as
# OpenStack.
#
# wr will pick a certain flavor to run a job on, as per the cloudflavor option.
# If it isn't possible to create a server with that flavor due to lack of
# physical hardware, wr will repick a flavor (again as per the cloudflavor
# option), but this time excluding flavors in the same flavor set as the initial
# pick. This is repeated until a flavor is found in a set where there is
# sufficient hardware to create a new server to run the job.
#
# If a flavor is picked that isn't in one of your flavor sets (or if you only
# have one set), it will never be repicked.
# cloudflavorsets: ""

# cloudkeepalive: How long should idle spawned server stay alive?
# This defaults to 120. It is overridden by the --keepalive option to
# 'wr cloud deploy' and the --cloud_keepalive option of 'wr manager start'.
# Note, this is a number (no quotes) of seconds.
#
# This option is only relevant when you are using a cloud scheduler such as
# OpenStack.
#
# The benefit of keeping idle servers alive is that if you subsequently add jobs
# that can run on an idle server, that server will get used and you won't have
# wait for a new server to be spawned. After cloudkeepalive seconds, idle
# servers are terminated.
#
# A value of 0 turns off the termination of idle servers (not recommended).
cloudkeepalive: 120

# cloudautoconfirmdead: How long should dead spawned servers be kept?
# This defaults to 30. It is overridden by the --auto_confirm_dead option to
# 'wr cloud deploy' and the --cloud_auto_confirm_dead option of
# 'wr manager start'.
# Note, this is a number (no quotes) of minutes.
#
# This option is only relevant when you are using a cloud scheduler such as
# OpenStack.
#
# When wr spawns a new server on which to execute commands, it periodically
# checks if it can still SSH to it. If not, the server is considered "dead". If
# this was due to a temporary networking issue or similar, then later on the
# server could become responsive again and it will no longer be considered dead.
# A server will also be considered dead (permanently) if a wr process running on
# it is killed.
# 
# In the former case it may not be possible to execute new commands on the
# server, and in the latter case wr will not even try to do so. But since the
# server still exists, it may be using up your quota, preventing the creation
# of a new server on which to run commands.
#
# If you wish to investigate all instances of your servers becoming "dead", you
# can use 'wr cloud servers' to find out about them and manually confirm them
# dead, destroying them if they still exist.
#
# For unattended usage, however, you can configure this to a > 0 value and wr
# will automatically confirm the servers dead if they remain dead for the given
# number of minutes. It is suggested you set this to a high value like 30, to
# allow time for manual investigation if desired, or for temporary issues to
# resolve themselves.
#
# A value of 0 turns off this behaviour of automatically confirming servers are
# dead.
cloudautoconfirmdead: 30

# cloudservers: How many additional cloud servers can be spawned?
# This defaults to -1. It is overridden by the --max_servers option to
# 'wr cloud deploy' and the --cloud_servers option of 'wr manager start'.
# Note, this is a number (no quotes).
#
# This option is only relevant when you are using a cloud scheduler such as
# OpenStack.
#
# -1 means there is no limit (other than your quota in the cloud) to the number
# of servers that wr will spawn in order to run your commands. Wr will scale up
# and down the number of servers as needed.
# 0 means don't spawn any servers; jobs will only run on the same server that
# the manager is running on (if possible).
#
# If this cloudservers value gets used as the default of 'wr cloud deploy', it
# is incremented by 1, since deploy's --max_servers option has a slightly
# different meaning to start's --cloud_servers option, as it includes the
# initial server that gets created to run 'wr manager'.
cloudservers: -1

# cloudspawns: How many cloud servers can be spawned simultaneously?
# This defaults to 10. It is overridden by the --max_spawns option to
# 'wr cloud deploy' and the --cloud_spawns option of 'wr manager start'.
# Note, this is a number (no quotes).
#
# This option is only relevant when you are using a cloud scheduler such as
# OpenStack.
#
# 0 means there is no limit to the number of servers that wr will spawn
# simultaneously. 1 means they will be spawned sequentually. This may be more
# reliable, but will result in slow scale-up.
cloudspawns: 10

# cloudcidr: What should be the CIDR of the created subnet?
# This defaults to "192.168.64.0/18". It is overridden by the --network_cidr
# option to 'wr cloud deploy' and the --cloud_cidr option of 'wr manager start'.
#
# This option is only relevant when you are using a cloud scheduler such as
# OpenStack.
#
# wr creates a network and subnet in the cloud in which any spawned servers are
# created. The CIDR determines the possible IP addresses the spawned servers can
# have. For example, with the default CIDR you will be able to spawn 16384
# servers with IPs starting from 192.168.64.0 and going up to 192.168.127.255.
cloudcidr: "192.168.64.0/18"

# cloudgateway: What should be the gateway IP of the created subnet?
# This defaults to "192.168.64.1". It is overridden by the --network_gateway_ip
# option to 'wr cloud deploy' and the --cloud_gateway_ip option of
# 'wr manager start'.
#
# This option is only relevant when you are using a cloud scheduler such as
# OpenStack.
#
# wr creates a network and subnet in the cloud in which any spawned servers are
# created. The subnet needs a gateway, and you should normally set its IP to the
# start of the range of your cloudcidr.
cloudgateway: "192.168.64.1"

# clouddns: What DNS name servers should be configured on spawned servers?
# This defaults to "8.8.4.4,8.8.8.8". It is overridden by the --network_dns
# option to 'wr cloud deploy' and the --cloud_dns option of 'wr manager start'.
# Note, this is a comma separated string of 1 or more name servers.
#
# This option is only relevant when you are using a cloud scheduler such as
# OpenStack.
#
# When wr spawns a server to run commands, the server will usually only function
# correctly if it has DNS name servers configured on it (even if your command
# does not access the internet). The default is to use Google's free name
# servers.
clouddns: "8.8.4.4,8.8.8.8"

# cloudos: What OS image should be used for spawned servers?
# This defaults to "bionic-server". It is overridden by the --os option to
# 'wr cloud deploy' and the --cloud_os option of 'wr manager start'.
# Note, this is the string prefix name or complete ID of an image that is
# available to you.
#
# This option is only relevant when you are using a cloud scheduler such as
# OpenStack.
cloudos: "bionic-server"

# clouduser: What username should be used to log in to cloudos images?
# This defaults to "ubuntu". It is overridden by the --username option to
# 'wr cloud deploy' and the --cloud_username option of 'wr manager start'.
#
# This option is only relevant when you are using a cloud scheduler such as
# OpenStack.
#
# The OS image you chose via cloudos will likely only have a single special
# user that can log in to it. You must specify that username here.
clouduser: "ubuntu"

# cloudram: How much RAM must a server have to run cloudos?
# This defaults to 2048. It is overridden by the --os_ram option to
# 'wr cloud deploy' and the --cloud_ram option of 'wr manager start'.
# Note, this is a number (no quotes) in MB.
#
# This option is only relevant when you are using a cloud scheduler such as
# OpenStack.
#
# This option affects how picking of flavors for new servers works. If a command
# only needs 100MB to run, but the cloudram is set to 2048, then only server
# flavors with at least 2GB of ram will get chosen.
cloudram: 2048

# clouddisk: What should the minimum disk space of spawned servers be?
# This defaults to 1. It is overridden by the --os_disk option to
# 'wr cloud deploy' and the --cloud_disk option of 'wr manager start'.
# Note, this is a number (no quotes) in GB.
#
# This option is only relevant when you are using a cloud scheduler such as
# OpenStack.
#
# The cheapest server flavor will be chosen for your commands as normal (see
# cloudflavor for details). If that flavor has disk space greater than or
# equal to clouddisk, nothing special happens (and you'll get a server with
# likely fast disk speeds). If the flavor has less disk space than clouddisk,
# a temporary volume will be created of clouddisk size and associated with the
# new server. The volume will get deleted when the server is deleted.
clouddisk: 1

# cloudscript: What script should run on newly spawned servers?
# If unset, nothing is run. It is overridden by the --script option to
# 'wr cloud deploy' and the --cloud_script option of 'wr manager start'. (It is
# NOT used as the default of --cloud_script for 'wr add'.)
# Note, this is the absolute path to a local bash script.
#
# This option is only relevant when you are using a cloud scheduler such as
# OpenStack.
#
# When wr spawns a new server, cloudscript will be run on it when the server
# first boots up. Note that there is a time limit of 15 mins for the script to
# run. If your script installs lots of software and is exceeding the limit for
# that reason, consider creating a new image instead and setting cloudos.
# cloudscript: ""

# clouddestroyscript: What script should run on servers before destoying them?
# If unset, nothing is run. It is overridden by the --destroy_script option to
# 'wr cloud deploy' and the --cloud_destroy_script option of 'wr manager start'.
# Note, this is the absolute path to a local bash script.
#
# This option is only relevant when you are using a cloud scheduler such as
# OpenStack.
#
# When wr destoys a server it created, clouddestroyscript will be run on it
# first. Note that there is a time limit of 15 mins for the script to run.
# clouddestroyscript: ""

# cloudconfigfiles: What config files should be copied to newly spawned servers?
# This defaults to "~/.s3cfg,~/.aws/credentials,~/.aws/config". It is overridden
# by the --config_files option to 'wr cloud deploy', and the
# --cloud_config_files option of 'wr manager start'. (It is NOT used as the
# default of --cloud_config_files for 'wr add'.)
# Note, this is a comma separated string of paths.
#
# This option is only relevant when you are using a cloud scheduler such as
# OpenStack.
#
# If you specify absolute paths, the file will be copied to the same absolute
# path on spawned cloud servers. For files in your home directory which you want
# to be placed in the home directory of the cloud servers, use the ~/ prefix.
#
# If local path and desired remote path are unrelated, the source and
# destination paths can be separated with a colon, eg.
# "~/.s3cfg.openstack:~/.s3cfg".
#
# Examples of files you might need to copy over are your s3 configuration files.
# You'll need these on your cloud servers if you plan on 'wr add'ing any
# commands with --mounts.
#
# If you specify files that don't exist locally, they are silently ignored.
cloudconfigfiles: "~/.s3cfg,~/.aws/credentials,~/.aws/config"

# deploysuccessscript: What script should run locally after cloud deploy?
# If unset, nothing is run. It is overridden by the --on_success option to
# 'wr cloud deploy'.
# Note, this is the absolute path to an executable.
#
# After you run 'wr cloud deploy', if it succeeds, the executable you supply
# here will run with the environment variables WR_MANAGERIP and
# WR_MANAGERCERTDOMAIN set. Your executable might update your local DNS entries
# so that you can access wr's REST API using the domain that your TLS
# certificate is valid for.
# deploysuccessscript: ""
`

// options for this cmd.
var confDefault bool

// confCmd represents the conf command.
var confCmd = &cobra.Command{
	Use:   "conf",
	Short: "See wr's configuration",
	Long: `See the configuration values wr will use.

(Note, these are the values based on your current environment, not the values
that a particular manager used when it started.)

This command also shows where a particular value was defined.

For a list of all possible configuration settings, their descriptions and
default values in the yml format suitable for using as one of your config files,
use the --default option.

wr will load its configuration settings from one or more files named
.wr_config[.production|.development].yml found in these directories, in order of
precedence:
1) The current directory
2) Your home directory
3) The directory pointed to by the environment variable $WR_CONFIG_DIR

.wr_config.yml files are always read, and can be used to define settings common
to both production and development deployments.
.wr_config.production.yml files are only read in a production context:
either a --deployment production option has been passed to the wr executable, or
the environment variable $WR_DEPLOYMENT has been set to 'production'.
A similar story applies for .wr_config.development.yml files, which are used
when things are set to 'development'.
The default deployment is production (unless you're in the git repository for
wr, in which case it is development).

You can set or override any given config setting using an environment variable
named like: WR_<setting name in caps>. Eg. to define the managerscheduler
option you might do:
export WR_MANAGERSCHEDULER="lsf"

Note that all worker nodes need to be able to see your desired set of config
files, so either define them in environment variables or put the config files
on a disc that is mounted and shared across all your compute nodes. In cloud
deployments where wr itself creates compute nodes, a config file will be created
on new nodes automatically.`,
	Run: func(_ *cobra.Command, _ []string) {
		if confDefault {
			fmt.Print(defaultYML)
			os.Exit(0)
		}

		fmt.Printf("%s", config)
	},
}

func init() {
	RootCmd.AddCommand(confCmd)

	// flags specific to this sub-command
	confCmd.Flags().BoolVarP(&confDefault, "default", "d", false, "print default config yml file to STDOUT")
}
