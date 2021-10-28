wr - workflow runner
====================

[![Gitter](https://camo.githubusercontent.com/da2edb525cde1455a622c58c0effc3a90b9a181c/68747470733a2f2f6261646765732e6769747465722e696d2f4a6f696e253230436861742e737667)](https://gitter.im/wtsi-wr??utm_source=badge&utm_medium=badge&utm_campaign=pr-badge&utm_content=body_badge)
[![GoDoc](https://godoc.org/github.com/VertebrateResequencing/wr?status.svg)](https://godoc.org/github.com/VertebrateResequencing/wr)
[![Go Report Card](https://goreportcard.com/badge/github.com/VertebrateResequencing/wr)](https://goreportcard.com/report/github.com/VertebrateResequencing/wr)
[![Build Status](https://travis-ci.org/VertebrateResequencing/wr.svg?branch=master)](https://travis-ci.org/VertebrateResequencing/wr)

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

Download
--------
[![download](https://img.shields.io/badge/download-wr-green.svg)](https://github.com/VertebrateResequencing/wr/releases)

Alternatively, build it yourself (see go.mod for the minimum version of go
required):

        git clone https://github.com/VertebrateResequencing/wr.git
        cd wr
        make

The `wr` executable should now be in `$HOME/go/bin`.

Documentation
-------------

Complete usage information is available using the `-h` option to wr and its
sub-commands.

Guided usage, tips, notes and tutorials are available here:
https://workflow-runner.readthedocs.io/
