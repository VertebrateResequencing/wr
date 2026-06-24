# Feature: Live job introspection

## Issue

Issue #98 asks for live peak memory and CPU information for running jobs,
recent stdout/stderr, and a quick way to reach the host where a job is running.

Live walltime and live state updates already exist. The runner touches the
manager periodically, and job subscriptions exist. Missing pieces are live
resource usage and output-tail data during a run, plus an SSH convenience.

## Required behaviour

Extend the runner touch/subscription path so running jobs expose live
introspection data.

- On every existing touch heartbeat, send current peak RAM, CPU information,
  and the most recent stdout/stderr tail from the last heartbeat.
- The stdout/stderr tail must have a fixed compressed size limit so touch
  payload size is bounded.
- Surface the live metrics and output tail to clients that already receive
  live job updates.
- Surface the data in the web UI for running jobs.
- Display an SSH command that lets a user reach the host and working directory
  for the running job, for example an `ssh ... && cd ...` command.
- Do not implement an embedded web terminal in v1.
- Gate the feature on HTTPS/auth being enabled.
- Keep existing live status behaviour working when the new data is absent,
  older runners connect, or auth gating disables the feature.

## Notes

The touch frequency stays at the current configured touch interval. No new
polling loop is required.

Spec questions should be surfaced to the human only if they require a product
or maintainer choice. Implementation details should be decided from existing
wr patterns or sensible defaults and recorded in the spec.
