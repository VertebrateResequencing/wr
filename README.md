wr - workflow runner
====================

[![Gitter](https://camo.githubusercontent.com/da2edb525cde1455a622c58c0effc3a90b9a181c/68747470733a2f2f6261646765732e6769747465722e696d2f4a6f696e253230436861742e737667)](https://gitter.im/wtsi-wr??utm_source=badge&utm_medium=badge&utm_campaign=pr-badge&utm_content=body_badge)
[![GoDoc](https://godoc.org/github.com/VertebrateResequencing/wr?status.svg)](https://godoc.org/github.com/VertebrateResequencing/wr)
[![Go Report Card](https://goreportcard.com/badge/github.com/VertebrateResequencing/wr)](https://goreportcard.com/report/github.com/VertebrateResequencing/wr)
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

Furthermore, wr has best-in-class support for OpenStack, providing incredibly
easy deployment and auto-scaling without you having to know anything about
OpenStack. For use in clouds such as AWS, GCP and others, wr also has the
built-in ability to self-deploy to any Kubernetes cluster. And it has built-in
support for mounting S3-like object stores, providing an easy way of running
commands against remote files whilst enjoying [high
performance](https://github.com/VertebrateResequencing/muxfys).


***Current Status***

wr is still being actively developed, with some significant features
unimplemented, but is production ready.

It is being used in production by multiple groups at the Sanger Institute in a
number of different ways:
* Custom workflow script modified to call `wr add` with dependency infomation.
* Nextflow front end with a wr backend (see the [wiki](https://github.com/VertebrateResequencing/wr/wiki/Nextflow))
* Cromwell front end with a wr backend (see the [wiki](https://github.com/VertebrateResequencing/wr/wiki/Cromwell))

wr has processed hundreds of TB of data and run millions of commands.

Download
--------
[![download](https://img.shields.io/badge/download-wr-green.svg)](https://github.com/VertebrateResequencing/wr/releases)

Alternatively, build it yourself (at least v1.16 of go is required):

1. Install go on your machine according to:
[golang.org/doc/install](https://golang.org/doc/install)
An example way of setting up a personal Go installation in your home directory
would be:

        export GOV=1.16.3
        wget https://dl.google.com/go/go$GOV.linux-amd64.tar.gz
        tar -xvzf go$GOV.linux-amd64.tar.gz && rm go$GOV.linux-amd64.tar.gz
        export PATH=$PATH:$HOME/go/bin

2. Download, compile, and install wr (not inside $GOPATH, if you set that):

        git clone https://github.com/VertebrateResequencing/wr.git
        cd wr
        make

3. The `wr` executable should now be in `$HOME/go/bin`

If you don't have `make` installed and don't mind if `wr version` will not work,
you can instead replace `make` above with:

    go install -tags netgo

Usage instructions
------------------
The download .zip should contain the wr executable, this README.md, and a
CHANGELOG.md. Running `wr conf --default` will print an example config file
which details all the config options available. The main things you need to
know are:

* You can use the wr executable directly from where you extracted it, or
  move it to where you normally install software to.
* Use the -h option on wr and all its sub commands to get further help
  and instructions.
* The default config should be fine for most people, but if you want to change
  something, run `wr conf --default > ~/.wr_config.yml` and make changes to 
  that. Alternatively, as the example config file explains, add environment
  variables to your shell login script and then source it.
  If you'll be using OpenStack, it is strongly recommended to configure
  database backups to go to S3.
* The wr executable must be available at that same absolute path on all
  compute nodes in your cluster, so you need to place it on a shared disk or
  install it in the same place on all machines. In cloud environments, the wr
  executable is copied for you to new cloud servers, so it doesn't need to be
  part of your OS images. If you use config files, these must also be readable
  by all nodes (when you don't have a shared disk, it's best to configure using
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

Note that for viewing the web interface, your browser will raise a security
warning, since by default wr will generate and use its own self-signed
certificate. So the first time you view it you will need to allow an exception.
See the [wiki](https://github.com/VertebrateResequencing/wr/wiki/Security) for
more details regarding security.

For usage on OpenStack, while you can bring up your own OpenStack server, ssh
there and run `wr manager start -s openstack [options]` as normal it's easier
to:

* wr cloud deploy [options]
* wr add [options]
* [view status on the web interface]
* wr cloud teardown

This way, you don't have to directly interact with OpenStack at all, or even
know how it works.

For usage in a Kubernetes cluster, you can similarly:

* wr k8s deploy [options]
* wr add [options]
* [view status on the web interface]
* wr k8s teardown

If you have any problems getting things to start up, check out the
[wiki](https://github.com/VertebrateResequencing/wr/wiki) for additional
guidance.

An alternative way of interacting with wr is to use it's REST API, also
documented on the
[wiki](https://github.com/VertebrateResequencing/wr/wiki/REST-API)

Performance considerations
--------------------------
For the most part, you should be able to throw as many jobs at wr as you like,
running on as many compute nodes as you have available, and trust that wr will
cope. There are no performance-related parameters to fiddle with: fast mode is
always on!

However you should be aware that wr's performance will typically be limited by
that of the disk you configure wr's database to be stored on (by default it is
stored in your home directory), since to ensure that workflows don't break and
recovery is possible after crashes or power outages, every time you add jobs to
wr, and every time you finish running a job, before the operation completes it
must wait for the job state to be persisted to disk in the database.

This means that in extreme edge cases, eg. you're trying to run thousands of
jobs in parallel, each of which completes in milliseconds, each of which want to
add new jobs to the system, you could become limited by disk performance if
you're using old or slow hardware.

You're unlikely to see any performance degradation even in extreme edge cases
if using an SSD and a modern disk controller. Even an NFS mount could give more
than acceptable performance.

But an old spinning disk or an old disk controller (eg. limited to 100MB/s)
could cause things to slow to a crawl in this edge case. "High performance" disk
systems like Lustre should also be avoided, since these tend to have incredibly
bad performance when dealing with many tiny writes to small files.

If this is the only hardware you have available to you, you can half the impact
of disk performance by reorganising your workflow such that you add all your
jobs in a single `wr add` call, instead of calling `wr add` many times with
subsets of those jobs.

Implemented so far
------------------
* Adding manually generated commands to the manager's queue.
* Automatically running those commands on the local machine, or via LSF
  or OpenStack.
* Mounting of S3-like object stores.
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
  dependencies). Common Workflow Language (CWL) compatibility is planned.
* Get a complete listing of all commands with a given id via the webpage.
* Checkpointing for long running commands.
* Re-run button in web interface for successfully completed commands.
* Ability to alter expected memory and time or change env-vars of commands.

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
https://login.internal.myserver.org:11302 or perhaps https://localhost:11302
