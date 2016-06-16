vrpipe
======

***DO NOT USE YET!***

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

Critical things not yet implemented
-----------------------------------
* If a command fails, there's no interface to let you "kick" (retry) it
* While there are 3 automatic retries, there is no automatic retrying with
  higher memory or time, if it failed for those reasons
* Learning of how much memory and time commands use is not yet implemented
* If you stop the manager while there are incomplete jobs, when you restart
  the manager, those incomplete jobs are not recovered and processed
* The status web page "works", but is unstyled and practically unusable
* While the help mentions pipelines, nothing pipeline-related has been
  implemented