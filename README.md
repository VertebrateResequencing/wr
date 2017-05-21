wr - workflow runner
====================

[![Go Report Card](https://goreportcard.com/badge/github.com/VertebrateResequencing/wr)](https://goreportcard.com/report/github.com/VertebrateResequencing/wr)
[![GoDoc](https://godoc.org/github.com/VertebrateResequencing/wr?status.svg)](https://godoc.org/github.com/VertebrateResequencing/wr)
develop branch: 
[![Build Status](https://travis-ci.org/VertebrateResequencing/wr.svg?branch=develop)](https://travis-ci.org/VertebrateResequencing/wr)
[![Coverage Status](https://coveralls.io/repos/github/VertebrateResequencing/wr/badge.svg?branch=develop)](https://coveralls.io/github/VertebrateResequencing/wr?branch=develop)

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

***DO NOT USE YET!***

wr is in early beta, with some significant features unimplemented, and the
possibility of significant bugs. However, for simple usage, for example easily
running your own manually-specified commands in an OpenStack environment, it is
probably safe to use.

So if you want to be adventurous and provide feedback...

Download
--------
[![download](https://img.shields.io/badge/download-wr-green.svg)](https://github.com/VertebrateResequencing/wr/releases)

Alternatively, build it yourself (at least v1.8 of go is required):

1. Install go on your machine and setup the environment according to:
[golang.org/doc/install](https://golang.org/doc/install)
(make sure to set your `$GOPATH`). An example way of setting up a personal Go
installation in your home directory would be:

        wget "https://storage.googleapis.com/golang/go1.8.linux-amd64.tar.gz"
        tar -xvzf go1.8.linux-amd64.tar.gz && rm go1.8.linux-amd64.tar.gz
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

If you don't have `make` installed and don't mind if `wr version` will not work,
you can instead replace `make` above with:

    curl -s https://glide.sh/get | sh
    $GOPATH/bin/glide install
    go install -tags netgo

Usage instructions
------------------
The download .zip should contain the wr executable, this README.md, a
CHANGELOG.md and an example config file called wr_config.yml, which details all
the config options available. The main things you need to know are:

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

For usage on OpenStack, while you can bring up your own OpenStack server, ssh
there and run `wr manager start -s openstack [options]` as normal it's easier
to:

* wr cloud deploy [options]
* wr add [options]
* [view status on the web interface]
* wr cloud teardown

This way, you don't have to directly interact with OpenStack at all, or even
know how it works.

If you have any problems getting things to start up, check out the
[wiki](https://github.com/VertebrateResequencing/wr/wiki) for additional
guidance.

Implemented so far
------------------
* Adding manually generated commands to the manager's queue.
* Automatically running those commands on the local machine, or via LSF
  or OpenStack.
* Getting the status of your commands.
* Manually retrying failed commands.
* Automatic retrying of failed commands, using more memory/time reservation
  as necessary.
* Learning of how much memory and time commands take for best resource
  utilization.
* Draining the queue if you want to stop the system as gracefully as
  possible, and recovering from drains, stops and crashes.
* Specifying command dependencies, and allowing for automation by these
  dependencies being "live", automatically re-running commands if their
  dependencies get re-run or added to.

Not yet implemented
-------------------
* While the help mentions workflows, nothing workflow-related has been
  implemented (though you can manually build a workflow by specifying command
  dependencies).
* Get a complete listing of all commands with a given id via the webpage.
* Database backups.
* Checkpointing for long running commands.
* Security (anyone with an account on your machine can use your
  manager).
* Re-run button in web interface for successfully completed commands.
* Ability to alter expected memory and time or change env-vars of commands.

Background
----------

wr is aimed at replacing [VRPipe](https://github.com/VertebrateResequencing/vr-pipe/)
which has the following problems:

* It's difficult to install due to the large set of CPAN dependencies.
* It's very slow due to the use of Moose.
* It's very slow due to the use of DBIx::Class.
* It doesn't scale well due to the current way it uses MySQL.

It's written in Go because:

* It's basically as easy to write as Perl.
* It has built-in packages equivalent to most of the critical CPAN modules.
* It has better interfaces and function signatures than Moose.
* It is faster, both due to compilation and re-factoring database usage.
* It is easier to install: distribute a statically-linked compiled binary.

Example .ssh/config
-------------------
If you're having difficulty accessing the web frontend via an ssh tunnel, the
following example ~/.ssh/config file may help. (In this example, 11302 is the
web interface port that wr tells you about.)

    Host ssh.myserver.org
    LocalForward 11302 login.internal.myserver.org:11302
    DynamicForward 20002
    ProxyCommand none
    Host *.internal.myserver.org
    User myusername
    ProxyCommand nc -X 5 -x localhost:20002 %h %p

You'll then be able to access the website at
http://login.internal.myserver.org:11302 or perhaps http://localhost:11302
