# Phase 1 gate: A4 on the log store, and fuzzing (item 1g.1)

Gate (spec.md, Implementation Order, Phase 1, Gates): A4 on the log store,
16 rounds local and 16 on NFS, plus 20 damaged copies each, 0 missing;
`FuzzScan` and `FuzzFlatSpec` 10 minutes each with no crasher. This is the
first run of A4 acceptance test 2 (damaged copies), deferred from item
0d.1.

Run on 2026-11-09 on farm22-wrstat01 (8 cores, 91GB), go1.27.1, HEAD
`76661645` on `customdb-f397b547`, where the log store is the default.
Every part passes. No product or test code changed.

Trimmed outputs are in [phase1-gate-1g1/](phase1-gate-1g1/).

## A4

### Method

The test machinery already existed: `TestStoreCrash`
(`jobqueue/store_crash_test.go`) runs acceptance tests 1-4 against each
store kind `WR_TEST_STORE` selects, which is the log store by default.
For the log store, each kill round copies the store as the killed child
left it into `killed.<round>` before the reopen, and
`checkDamagedCrashCopies` makes 20 copies of that, alternately truncating
the last wal file at a random offset and flipping one random byte after
its header frame. It requires each copy to open and recover with no
error, and every recovered live or complete job to be in a state the
child wrote for it (the intents file). That is acceptance test 2.

Both runs used one scratch `HOME` with the shared module and build caches,
and every `OS_*` variable unset:

```bash
(unset $(compgen -v | grep '^OS_'); export GOMODCACHE=$(go env GOMODCACHE) GOCACHE=$(go env GOCACHE); \
 HOME=<scratch>/home WR_CRASHTEST_ROUNDS=16 WR_CRASHTEST_DIR=<dir> CGO_ENABLED=1 \
 /usr/bin/time -v go test -tags netgo -count 1 -timeout 0 -v -run '^TestStoreCrash$' ./jobqueue)
```

`<dir>` was a local scratchpad directory for the local run and
`/nfs/hgi/wr/sb10-bigdb/customdb/1g1/nfs` for the NFS run. The two runs
overlapped in time (local 16:54-17:00, NFS 16:54-17:07), and `FuzzScan`
overlapped the last 3 minutes of the NFS run. The test removed its round
directories at the end; the NFS work directory was then removed.

`WR_CRASHTEST_ROUNDS=16` applies to the plain kill rounds and to the
backup-tick rounds. The ackEarly (test 3) and dropped-release (test 4)
scenarios run their fixed round counts.

### Results

| | local | NFS |
| --- | --- | --- |
| `TestStoreCrash` | PASS, 387.8s | PASS, 810.0s |
| kill rounds, each with 0 missing | 16 | 16 |
| acked keys, all rounds | 36,023 | 18,300 |
| recovered after round 15 (live + complete) | 4,740 + 31,560 | 3,867 + 14,433 |
| unacked calls per round (min-max) | 70-463 | 46-497 |
| kill delay per round | 1.56-4.25s | 1.58-3.96s |
| damaged copies recovered (test 2) | 320 | 320 |
| copies with a recovered state the child never wrote | 0 | 0 |
| backup-tick rounds, each with 0 missing | 16 | 16 |
| ackEarly round missing (test 3, must be > 0) | 3 | 2 |
| dropped-release missing (test 4, must be > 0) | 28, round 0 | 75, round 0 |
| peak RSS | 535MB | 529MB |

Test 1: every one of the 32 kill rounds recovered every acked (key,
attempts, step) as complete or live at or after its ack, and left at
least one call unacked, as the test asserts.

Test 2: the "damaged copies recovered" row counts the
`recovering: opened database` warnings whose path ends in
`killed.<round>.damaged.<i>`; 16 rounds of 20 copies is 320. The damage
cut real records: across the 320 local copies recovery logged 319
`recovery truncated a wal file's damaged tail` warnings and 256
`recovery moved history's folded position back to the log's damaged end`
warnings (317 and 163 on NFS). Each of the latter has a `dropped` count of
live jobs dropped because the cut lost the delete that followed their
completion (0 in 3 local and 7 NFS cases). The rewind lines carry no path,
so they are attributed to the damaged copies by inference. Every copy still
opened without error and held only states the child wrote.

Tests 3 and 4 show the test can fail: a store that acknowledges before its
write, and one that drops acknowledged releases, both report missing keys.

The NFS run also logged 99 `db writer` warnings from the fold writer's
periodic stats (`writer=fold`, max hold up to about 270ms). They are
periodic timing reports, not errors.

## Fuzzing

```bash
go test -tags netgo -count 1 -timeout 0 -run '^$' -fuzz '^FuzzScan$' -fuzztime 10m -parallel 6 ./jobqueue/wal
go test -tags netgo -count 1 -timeout 0 -run '^$' -fuzz '^FuzzFlatSpec$' -fuzztime 10m ./jobqueue
```

| Target | Workers | Execs | New interesting | Result |
| --- | --- | --- | --- | --- |
| `FuzzScan` | 6 | 1,621,718 | 72 (total 333) | PASS, 601.5s |
| `FuzzFlatSpec` | 8 | 13,031,492 | 152 (total 161) | PASS, 601.1s |

Neither found a crasher, and no `testdata/fuzz` file was written.
`FuzzScan` ran with 6 workers because it overlapped the NFS crash run.

## Files

- [a4-local.txt](phase1-gate-1g1/a4-local.txt),
  [a4-nfs.txt](phase1-gate-1g1/a4-nfs.txt): the `go test -v` lines of each
  run with the store's log lines dropped, then the dropped log lines
  counted by message, the damaged-copy count and `time -v`.
- [fuzzscan.txt](phase1-gate-1g1/fuzzscan.txt),
  [fuzzflatspec.txt](phase1-gate-1g1/fuzzflatspec.txt): the full fuzz
  output.
