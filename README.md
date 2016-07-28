vrpipe
======

***DO NOT USE YET!***

But if you want to be adventurous and provide feedback...
[![gorelease](https://dn-gorelease.qbox.me/gorelease-download-blue.svg)](https://gobuild.io/sb10/vrpipe/master)

This is an experimental reimplementation of
https://github.com/VertebrateResequencing/vr-pipe/
in the Go programming language

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
* Automatically running those commands on the local machine or via LSF
* Getting the status of your commands
* Manually retrying failed commands
* Automatic retrying of failed commands, using more memory/time reservation
  as necessary
* Learning of how much memory and time commands take for best resource
  utilization
* Draining the queue if you want to stop the system as gracefully as
  possible, and recoving from drains, stops and crashes

Not yet implemented
-----------------------------------
* While the help mentions pipelines, nothing pipeline-related has been
  implemented (no job dependecies)
* Get a complete listing of all commands with a given id
* Database backups
* Checkpointing for long running commands
* Security (anyone with an account on your machine can use your
  manager)
* Re-run button in web interface for successfully completed commands
* Ability to alter memory/time/env-vars of commands
