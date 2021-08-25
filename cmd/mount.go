// Copyright © 2017, 2018 Genome Research Limited
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
	"encoding/json"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/VertebrateResequencing/muxfys/v4"
	"github.com/VertebrateResequencing/wr/jobqueue"
	"github.com/inconshreveable/log15"
	"github.com/sb10/l15h"
	"github.com/sevlyar/go-daemon"
	"github.com/spf13/cobra"
)

// options for this cmd
var mountSimple string
var mountJSON string
var mountVerbose bool

// mountCmd represents the mount command
var mountCmd = &cobra.Command{
	Use:   "mount",
	Short: "Mount an S3 bucket",
	Long: `Test mounting of S3 buckets.

'wr add' can take mount options if your commands need to read from/ write to
S3 buckets. Before supplying these mount options to 'wr add', you can use this
command to test that your mount options work.

You can also use this as a quick, easy and high performance way of mounting an
S3 bucket for general use, but note that it is only designed as a temporary
mount since it won't notice externally altered or added files in directories you
already accessed. It also only allows yourself access to the files.

This command runs as a daemon, meaning it will run in the background.
When you're finished with your mount, you must either send SIGTERM to its
process id (using the 'kill' command) or run it in -f mode and kill it with
ctrl-c.
NB: if you are writing to your mount point, it's very important to kill it
cleanly using one of these methods once you're done, since uploads only occur
when you do this!

For mounting to work, you must be able to carry out fuse mounts, which means
fuse-utils must be installed, and /etc/fuse.conf should have user_allow_other
set. An easy way to enable it is to run:
sudo perl -i -pne 's/#user_allow_other/user_allow_other/;' /etc/fuse.conf


--mounts is a convenience option that lets you specify your mounts in the common
case that you wish the contents of 1 or more remote directories to be accessible
from a single local directory ('mnt' when using this command, the command
working directory when using 'wr add'). For anything more complicated you'll
need to use --mount_json. You can't use both --mounts and --mount_json at once.
The format is a comma-separated list of [c|u][r|w]:[profile@]bucket[/path]
strings. The first character as 'c' means to turn on caching, while 'u' means
uncached. The second character as 'r' means read-only, while 'w' means writeable
(only one of them can have w). After the colon you can optionally specify the
profile name followed by the @ symbol, followed by the required remote bucket
name and ideally the path to the deepest subdirectory that contains the data you
wish to access.


--mount_json is the JSON string for an array of Config objects describing all
your mount parameters.
A JSON array begins and ends with a square bracket, and each item is separated
with a comma.
A JSON object can be written by starting and ending it with curly braces.
Parameter names and their values are put in double quotes (except for numbers,
which are left bare, booleans where you write, unquoted, true or false, and
arrays as described previously), and the pair separated with a colon, and pairs
separated from each other with commas.
For example (all on one line): --mounts '[{"Mount":"/tmp/wr_mnt","Targets":
[{"Profile":"default","Path":"mybucket/subdir","Write":true}]}]'
The paragraphs below describe all the possible Config object parameters.

Mount is the local directory on which to mount your Target(s). It can be (in)
any directory you're able to write to. If the directory doesn't exist, wr will
try to create it first. Otherwise, it must be empty. If not supplied, defaults
to the subdirectory "mnt" in the current working directory (under 'wr add', if
--cwd_matters has not been set, then instead the actual working directory is
used as the mount point). Note that if specifying multiple Config objects, they
must each have a different Mount (and so only one of them can have Mount
undefined).

CacheBase is the parent directory to use for the CacheDir of any Targets
configured with Cache on, but CacheDir undefined. If CacheBase is also
undefined, the cache directories will be made in the current working directory
(under 'wr add', if --cwd_matters has not been set, then instead the cache
directories will be in a sister directory of the actual working directory).

Retries is the number of retries wr should attempt when it encounters errors in
trying to access your remote S3 bucket. At least 3 is recommended. It defaults
to 10 if not provided.

Verbose is a boolean, which if true, would make wr store timing information on
all remote calls as lines of all job STDERR that use the mount. Errors always
appear there. This has no effect on what you see when using this command to test
your mount; instead use the global -v command line argument to see the same
things.

Targets is an array of Target objects which define what you want to access at
your Mount. It's an array to allow you to multiplex different buckets (or
different subdirectories of the same bucket) so that it looks like all their
data is in the same place, for easier access to files in your mount. You can
only have one of these configured to be writeable. (If you don't want to
multiplex but instead want multiple different mount points, you specify a single
Target in this array, and have multiple Config objects in your top level array.)
The remaining paragraphs describe the possible parameters for Target objects.

Profile is the S3 configuration profile name to use. If not supplied, the value
of the $AWS_DEFAULT_PROFILE or $AWS_PROFILE environment variables is used, and
if those are unset it defaults to "default".
wr will look at a number of standard S3 configuration files and environment
variables to determine the scheme, domain, region and authentication details to
connect to S3 with. All possible sources are checked to fill in any missing
values from more preferred sources.
The preferred file is ~/.s3cfg, since this is the only config file type that
allows the specification of a custom domain. This file is Amazon's s3cmd config
file, described here: http://s3tools.org/kb/item14.htm. wr will look at the
access_key, secret_key, use_https and host_base options under the section with
the given Profile name. If you don't wish to use any other config files or
environment variables, you can add the non-standard region option to this file
if you need to specify a specific region.
The next file checked is the one pointed to by the $AWS_SHARED_CREDENTIALS_FILE
environment variable, or ~/.aws/credentials. This file is described here:
http://docs.aws.amazon.com/cli/latest/userguide/cli-chap-getting-started.html.
wr will look at the aws_access_key_id and aws_secret_access_key options under
the section with the given Profile name.
wr also checks the file pointed to by the $AWS_CONFIG_FILE environment variable,
or ~/.aws/config, described in the previous link. From here the region option is
used from the section with the given Profile name. If you don't wish to use a
~/.s3cfg file but do need to specify a custom domain, you can add the
non-standard host_base and use_https options to this file instead.
As a last resort, ~/.awssecret is checked. This is s3fs's config file, and
consists of a single line with your access key and secret key separated by a
colon.
If set, the environment variables $AWS_ACCESS_KEY_ID, $AWS_SECRET_ACCESS_KEY and
$AWS_DEFAULT_REGION override corresponding options found in any config file.

Path (required) is the name of your S3 bucket, optionally followed URL-style
(separated with forward slashes) by sub-directory names. The highest performance
is gained by specifying the deepest path under your bucket that holds all the
files you wish to access.

Cache is a boolean, which if true, turns on data caching of any data retrieved,
or any data you wish to upload.

CacheDir is the local directory to store cached data. If this parameter is
supplied, Cache is forced true and so doesn't need to be provided. If this
parameter is not supplied but Cache is true, the directory will be a unique
directory in CacheBase, which will get deleted on unmount.

Write is a boolean, which if true, makes the mount point writeable. If you
don't intend to write to a mount, just leave this parameter out. Note that when
not cached, only serial writes are possible.`,
	Run: func(cmd *cobra.Command, args []string) {
		// set up logging
		logLevel := log15.LvlWarn
		if mountVerbose {
			logLevel = log15.LvlInfo
		}
		muxfys.SetLogHandler(log15.LvlFilterHandler(logLevel, l15h.CallerInfoHandler(log15.StderrHandler)))

		// now daemonize unless in foreground mode
		if foreground {
			mountAndWait()
		} else {
			dContext := &daemon.Context{
				WorkDir: "/",
			}

			child, err := dContext.Reborn()
			if err != nil {
				die("failed to daemonize: %s", err)
			}

			if child == nil {
				// daemonized child, that will run until signalled to stop
				defer func() {
					err := dContext.Release()
					if err != nil {
						warn("daemon release failed: %s", err)
					}
				}()
				mountAndWait()
			}
		}
	},
}

func init() {
	RootCmd.AddCommand(mountCmd)

	// flags specific to this sub-command
	mountCmd.Flags().StringVarP(&mountJSON, "mount_json", "j", "", "mount parameters JSON (see --help)")
	mountCmd.Flags().StringVarP(&mountSimple, "mounts", "m", "", "comma-separated list of [c|u][r|w]:bucket[/path] (see --help)")
	mountCmd.Flags().BoolVarP(&foreground, "foreground", "f", false, "do not daemonize")
	mountCmd.Flags().BoolVarP(&mountVerbose, "verbose", "v", false, "print timing info on all remote calls")
}

// mountAndWait does the main work of this cmd.
func mountAndWait() {
	// mount everything
	var mounted []*muxfys.MuxFys
	for _, mc := range mountParse(mountJSON, mountSimple) {
		var rcs []*muxfys.RemoteConfig
		for _, mt := range mc.Targets {
			accessorConfig, err := muxfys.S3ConfigFromEnvironment(mt.Profile, mt.Path)
			if err != nil {
				die("had a problem reading S3 config values from the environment: %s", err)
			}
			accessor, err := muxfys.NewS3Accessor(accessorConfig)
			if err != nil {
				die("had a problem creating an S3 accessor: %s", err)
			}

			rc := &muxfys.RemoteConfig{
				Accessor:  accessor,
				CacheData: mt.Cache,
				CacheDir:  mt.CacheDir,
				Write:     mt.Write,
			}

			rcs = append(rcs, rc)
		}

		retries := 10
		if mc.Retries > 0 {
			retries = mc.Retries
		}

		cfg := &muxfys.Config{
			Mount:     mc.Mount,
			CacheBase: mc.CacheBase,
			Retries:   retries,
			Verbose:   mc.Verbose,
		}

		fs, err := muxfys.New(cfg)
		if err != nil {
			die("bad configuration: %s\n", err)
		}

		err = fs.Mount(rcs...)
		if err != nil {
			die("could not mount: %s\n", err)
		}

		mounted = append(mounted, fs)
		// (we can't use each fs's UnmountOnDeath() function because they
		// won't wait for each other)
	}

	// wait for death
	if len(mounted) > 0 {
		deathSignals := make(chan os.Signal, 2)
		signal.Notify(deathSignals, os.Interrupt, syscall.SIGTERM)
		<-deathSignals
		for _, fs := range mounted {
			err := fs.Unmount()
			if err != nil {
				fs.Error("Failed to unmount", "err", err)
			}
		}
		return
	}
}

// mountParse takes possible json string or simple string (as per `wr mount -h`)
// and parses exactly 1 of them to a MountConfig for each mount defined.
func mountParse(jsonString, simpleString string) jobqueue.MountConfigs {
	if jsonString == "" && simpleString == "" {
		die("--mounts or --mount_json is required")
	}
	if jsonString != "" && simpleString != "" {
		die("--mounts and --mount_json are mutually exclusive")
	}

	if jsonString != "" {
		return mountParseJSON(jsonString)
	}

	return mountParseSimple(simpleString)
}

// mountParseJSON takes a json string (as per `wr mount --help`) and parses it
// to a MountConfig for each mount defined.
func mountParseJSON(jsonString string) jobqueue.MountConfigs {
	var mcs jobqueue.MountConfigs
	err := json.Unmarshal([]byte(jsonString), &mcs)
	if err != nil {
		die("had a problem with the provided mount JSON (%s): %s", jsonString, err)
	}

	return mcs
}

// mountParseSimple takes a comma-separated list of [c|u][r|w]:bucket[/path] and
// parses it to a MountConfig in a MountConfigs (to match the output type of
// mountParseJSON).
func mountParseSimple(simpleString string) jobqueue.MountConfigs {
	ss := strings.Split(simpleString, ",")
	targets := make([]jobqueue.MountTarget, 0, len(ss))
	for _, simple := range ss {
		parts := strings.Split(simple, ":")
		if len(parts) != 2 || len(parts[0]) != 2 {
			die("'%s' was not in the right format", simple)
		}

		var cache, write bool
		switch parts[0][0] {
		case 'c':
			cache = true
		case 'u':
			cache = false
		default:
			die("'%s' did not start with c or u", simple)
		}
		switch parts[0][1] {
		case 'w':
			write = true
		case 'r':
			write = false
		default:
			die("'%s' did not specify w or r", simple)
		}

		path := parts[1]
		var profile string
		if strings.Contains(path, "@") {
			parts := strings.Split(path, "@")
			profile = parts[0]
			path = parts[1]
		}

		mt := jobqueue.MountTarget{
			Path:  path,
			Cache: cache,
			Write: write,
		}
		if profile != "" {
			mt.Profile = profile
		}

		targets = append(targets, mt)
	}

	var mcs jobqueue.MountConfigs

	return append(mcs, jobqueue.MountConfig{Targets: targets})
}
