# Reliability test harness

Temp tooling used to produce the measurements in `../testing.md`. Nothing here is
part of wr; it is the "implementer writing temp code" kit for reproducing the
symptoms and trialling the ideas.

## Build

```sh
# clean binaries for the A/B
go build -tags netgo -o /tmp/wr-reliable/bin/wr-head .
git worktree add --detach /tmp/wr-reliable/src-v0365 v0.36.5
( cd /tmp/wr-reliable/src-v0365 && go build -tags netgo -o /tmp/wr-reliable/bin/wr-v0365 . )

# safe binaries with env-gated test guards (apply reliability-hacks.patch first)
git apply .docs/reliable/harness/reliability-hacks.patch
go build -tags netgo -o /tmp/wr-reliable/bin/wr-head-safe .
git checkout -- jobqueue/server.go jobqueue/db.go     # revert; keep tree clean

# loadrunner (built inside each tree so its client transport matches the manager)
mkdir -p .tmp/reliable-tools/loadrunner && cp .docs/reliable/harness/loadrunner.go .tmp/reliable-tools/loadrunner/main.go
go build -o /tmp/wr-reliable/bin/loadrunner-head ./.tmp/reliable-tools/loadrunner/

# read-only DB inspector (separate module using go.etcd.io/bbolt)
```

## Env-gated guards in reliability-hacks.patch (safe binaries only)

- `WR_RELIABILITY_NOSCHED=1` — never spawn/`bsub` a runner; log the scheduler
  group it *would* use (`RELNOSCHED group=…`).
- `WR_RELIABILITY_KEEPDB=1` — don't wipe the dev DB on start (run on a real-DB copy).
- `WR_RELIABILITY_TIMING=1` — log startup phase timings (`RELSTARTUP`/`RELPRIOR`/`RELEQ`).

## Scripts (see `../testing.md` for the scenarios/metrics they feed)

- `exp1.sh` — S1 steady-state churn throughput (real runners, no guards).
- `exp_startup_ab.sh` — S2 kill-9 + restart with N running jobs (arg 7 = `local|lsf`).
- `exp_realdb_seed.sh` — S3 the real-DB add-time + restart seeding cost (LSF, safe).
- `exp_drive_ab.sh` — S5 high-concurrency archive-throughput decay.
- `loadrunner.go` — N concurrent "runners" driving reserve→start→touch→archive
  without executing anything (modes: drive/hold/ping).
- `inspect*.go` — read-only bbolt bucket/key/top-repgroup counts.

## Safety

Always: isolated `WR_MANAGERDIR`/ports, `--deployment development`,
`WR_RELIABILITY_NOSCHED=1` for anything touching the real DB, a *copy* of the real
DB (never the live one), verify `bjobs -w | grep wrd_` is 0 before and after.
