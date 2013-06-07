vrpipe
======

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