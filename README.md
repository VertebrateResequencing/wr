wr - workflow runner
======

This is an experimental reimplementation of
https://github.com/VertebrateResequencing/vr-pipe/
in the Go programming language.

[![Go Report Card](https://goreportcard.com/badge/github.com/VertebrateResequencing/wr)](https://goreportcard.com/report/github.com/VertebrateResequencing/wr)
[![GoDoc](https://godoc.org/github.com/VertebrateResequencing/wr?status.svg)](https://godoc.org/github.com/VertebrateResequencing/wr)
develop branch: 
[![Build Status](https://travis-ci.org/VertebrateResequencing/wr.svg?branch=develop)](https://travis-ci.org/VertebrateResequencing/wr)
[![Coverage Status](https://coveralls.io/repos/github/VertebrateResequencing/wr/badge.svg?branch=develop)](https://coveralls.io/github/VertebrateResequencing/wr?branch=develop)

***DO NOT USE YET!***

But if you want to be adventurous and provide feedback...

Download
--------
[![gorelease](https://dn-gorelease.qbox.me/gorelease-download-blue.svg)](https://gobuild.io/VertebrateResequencing/wr/master)

Alternatively, build it yourself:

1. Install go on your machine and setup the environment according to:
[golang.org/doc/install](https://golang.org/doc/install)
(make sure to set your `$GOPATH`). An example way of setting up a personal Go
installation in your home directory would be:

        wget "https://storage.googleapis.com/golang/go1.7.1.linux-amd64.tar.gz"
        tar -xvzf go1.7.1.linux-amd64.tar.gz && rm go1.7.1.linux-amd64.tar.gz
        export GOROOT=$HOME/go
        export PATH=$PATH:$GOROOT/bin
        mkdir work
        export GOPATH=$HOME/work
        export PATH=$GOPATH/bin:$PATH

2. Download, compile, and install wr:

        go get -u -d -tags netgo github.com/VertebrateResequencing/wr
        cd $GOPATH/src/github.com/VertebrateResequencing/wr
        make

3. The `wr` executable should now be in `$GOPATH/bin`

If you don't have 'make' installed, you can instead replace step 2 above with
just `go get -u -tags netgo github.com/VertebrateResequencing/wr`, but note that
`wr version` will not work.

What's wrong with the original Perl version?
--------------------------------------------
* It's difficult to install due to the large set of CPAN dependencies
* It's very slow due to the use of Moose
* It's very slow due to the use of DBIx::Class
* It doesn't scale well due to the current way it uses MySQL

Why Go?
-------
* It's basically as easy to write as Perl
* It has built-in packages equivalent to most of the critical CPAN modules
* It has better interfaces and function signatures than Moose
* It will be faster, both due to compilation and re-factoring database usage
* It will be easy to install: distribute a statically-linked compiled binary

Implemented so far
------------------
* Adding manually generated commands to the manager's queue
* Automatically running those commands on the local machine, or via LSF
  or OpenStack
* Getting the status of your commands
* Manually retrying failed commands
* Automatic retrying of failed commands, using more memory/time reservation
  as necessary
* Learning of how much memory and time commands take for best resource
  utilization
* Draining the queue if you want to stop the system as gracefully as
  possible, and recovering from drains, stops and crashes

Not yet implemented
-------------------
* While the help mentions workflows, nothing workflow-related has been
  implemented (no job dependecies)
* Get a complete listing of all commands with a given id
* Database backups
* Checkpointing for long running commands
* Security (anyone with an account on your machine can use your
  manager)
* Re-run button in web interface for successfully completed commands
* Ability to alter expected memory and time or change env-vars of commands

Usage instructions
------------------
The download .zip should contain the wr executable, this README.md and an
example config file called wr_config.yml, which details all the config
options available. The main things you need to know are:

* You can use the wr executable directly from where you extracted it, or
  move it to where you normally install software to.
* Use the -h option on wr and all its sub commands to get further help
  and instructions.
* The default config should be fine for most people, but if you want to change
  something, copy the example config file to ~/.wr_config.yml and make
  changes to that. Alternatively, as the example config file explains, add
  environment variables to your shell login script and then source it.
* The wr executable must be available at that same absolute path on all
  compute nodes in your cluster, so you either need to place it on a shared
  disk, or install it in the same place on all machines (eg. have it as part of
  your OS image). If you use config files, these must also be readable by all
  nodes (when you don't have a shared disk, it's best to configure using
  environment variables).
* If you are ssh tunnelling to the node where you are running wr and wish
  to use the web interface, you will have to forward the host and port that it
  tells you the web interface can be reached on, and/or perhaps also dynamic
  forward using something like nc. An example .ssh/config is at the end of this
  document.

Right now, with the limited functionality available, you will run something like
(change the options as appropriate):

* wr manager start -s lsf
* wr add -f cmds_in_a_file.txt -m 1G -t 2h -i my_first_cmds -r mycmd_x_mode
* [view status on the web interface]
* wr manager stop

(It isn't necessary to stop the manager; you can just leave it running forever.)

Example .ssh/config
-------------------
If you're having difficulty accessing the web frontend via an ssh tunnel, the
following example config file may help. (In this example, 11302 is the web
interface port.)

Host ssh.myserver.org
LocalForward 11302 login.internal.myserver.org:11302
DynamicForward 20002
ProxyCommand none
Host *.internal.myserver.org
User myusername
ProxyCommand nc -X 5 -x localhost:20002 %h %p

You'll then be able to access the website at
http://login.internal.myserver.org:11302 or perhaps http://localhost:11302
