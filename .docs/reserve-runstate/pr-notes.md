# Notes for the PR body

- Concurrent adds (item 2.3): If two adds of the same new job race and the
  second add's fresh copy is written after a reservation's run state is
  queued but before it is drained, the run state lands over that fresh copy,
  so until the job's next full write its stored non-run fields are the second
  add's; this window is no wider than today's stale full change, and a test
  (TestAddKeepsHandedOutRunState) pins the outcome.
- Compact on a 0-byte file (item 1.2): `wr manager compact` now fails on an
  empty file (the read-only version check cannot open it), where before it
  would have initialised and compacted it.
- Speed (item 2.2): every full live write now also deletes a run-state key;
  covered by phase 6's make speed / speed-full comparison.
