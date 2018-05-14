// Copyright © 2016, 2017 Genome Research Limited
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

/*
Package main is a stub for wr's command line interface, with the actual
implementation in the cmd package.

wr is a workflow runner. You use it to run the commands in your workflow easily,
automatically, reliably, with repeatability, and while making optimal use of
your available computing resources.

wr is implemented as a polling-free in-memory job queue with an on-disk acid
transactional embedded database, written in go.

Its main benefits over other software workflow management systems are its very
low latency and overhead, its high performance at scale, its real-time status
updates with a view on all your workflows on one screen, its permanent
searchable history of all the commands you have ever run, and its "live"
dependencies enabling easy automation of on-going projects.

Basics

Start up the manager daemon, which gives you a url you can view the web
interface on:

    wr manager start -s local

In addition to the "local" scheduler, which will run your commands on all
available cores of the local machine, you can also have it run your commands on
your LSF cluster or in your OpenStack environment (where it will scale the
number of servers needed up and down automatically).

Now, stick the commands you want to run in a text file and:

    wr add -f myCommands.txt

Arbitrarily complex workflows can be formed by specifying command dependencies.
Use the --help option of `wr add` for details.

Package Overview

wr's core is implemented in the queue package. This is the in-memory job queue
that holds commands that still need to be run. Its multiple sub-queues enable
certain guarantees: a given command will only get run by a single client at any
one time; if a client dies, the command will get run by another client instead;
if a command cannot be run, it is buried until the user takes action; if a
command has a dependency, it won't run until its dependencies are complete.

The jobqueue package provides client+server code for interacting with the
in-memory queue from the queue package, and by storing all new commands in an
on-disk database, provides an additional guarantee: that (dynamic) workflows
won't break because a job that was added got "lost" before it got run. It also
retains all completed jobs, enabling searching through of past workflows and
allowing for "live" dependencies, triggering the rerunning of previously
completed commands if their dependencies change.

The jobqueue package is also what actually does the main "work" of the system:
the server component knows how many commands need to be run and what their
resource requirements (memory, time, cpus etc.) are, and submits the appropriate
number of jobqueue runner clients to the job scheduler.

The jobqueue/scheduler package has the scheduler-specific code that ensures that
these runner clients get run on the configured system in the most efficient way
possible. Eg. for LSF, if we have 10 commands that need 2GB of memory to run,
we will submit a job array of size 10 with 2GB of memory reservation to LSF. The
most limited (and therefore potentially least contended) queue capable of
running the commands will be chosen. For OpenStack, the cheapest server (in
terms of cores and memory) that can run the commands will be spawned, and once
there is no more work to do on those servers, they get terminated to free up
resources.

The cloud package implements methods for interacting with cloud environments
such as OpenStack. The corresponding jobqueue/scheduler package uses these
methods to do their work.

The static subdirectory contains the html, css and javascript needed for the
web interface. See jobqueue/serverWebI.go for how the web interface backend is
implemented.

The internal package contains general utility functions, and most notably
config.go holds the code for how the command line interface deals with config
options.
*/
package main

import (
	"os"
	"path/filepath"

	"github.com/VertebrateResequencing/wr/cmd"
)

func main() {
	// handle our executable being a symlink named bsub, in which case call
	// `wr lsf bsub`; likewise for bjobs
	switch filepath.Base(os.Args[0]) {
	case "bsub":
		cmd.ExecuteLSF("bsub")
	case "bjobs":
		cmd.ExecuteLSF("bjobs")
	case "bkill":
		cmd.ExecuteLSF("bkill")
	default:
		// otherwise we call our root command, which handles everything else
		cmd.Execute()
	}
}
