# Phase 1 runtime acquisition contract conflict

Status: contract resolved by the reviewed runtime packaging amendment.
Implementation has not yet demonstrated successful acquisition. The
historical failure and evidence below remain unchanged.

The amended spec passed two feature and two proofreading reviews. Updated
phases 1, 3, 4, 5, and 6 passed independent review; phase 2 did not require
changes. See progress.md and reviews/packaging-phases.md for the review
records. Resume the sequential schema/CLI and acquisition handoffs in the
revised phase1.md. Actual acquisition and offline evidence remain gates.

## Requested behaviour

Acquire and validate the pinned Nextflow 26.04.6 runtime distribution,
record its actual dependency closure, and prove offline input integrity.
The reviewed spec also rejects duplicate archive members without
distinguishing extracted source archives from executable runtime bundles.

## Observed failure

The implementor's actual acquisition verified the published runtime hash,
then rejected the official bundle because it contains duplicate ZIP metadata
members named META-INF/groovy-release-info.properties. The candidate
transaction was discarded and no acquisition counts were awarded.

Evidence files are under .tmp/agent/conformance/:

- acquire.json records complete=false, zero counts, and E_SOURCE_PATH.
- acquire.stderr records the duplicate runtime entry and exit status 2.

The implementor also reports shaded classes rather than a nested-JAR layout.
The required dependency-closure interpretation needs source-based review
before implementation proceeds. No runtime acquisition or oracle success
is claimed. Independent schema/tests work may continue.

## Proposed engineering resolution

Research the exact verified distribution and launcher. Specify artifact
identity and dependency closure according to their real packaging. Keep
strict path, traversal, and duplicate-member checks for archives the tool
extracts. If the runtime is retained and executed as an opaque verified
artifact, define its integrity checks separately, with adversarial tests.
Do not silently ignore duplicates in an extraction path.

A fresh spec author must make a bounded correction, independent reviewers
must validate it, and affected phase instructions must be re-reviewed before
the runtime acquisition work resumes. A different runtime artifact is an
alternative only if it is pinned and fulfils the same oracle contract.
