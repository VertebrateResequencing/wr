# TestDBEncodeJob allocation bound flakes under -race

- Branch: `encodejob-race-2a0b91ec`
- Base: `origin/develop` at `fc3f8a5d`
- Queue owner: this branch and this checklist

Quality gates, with `nice -n 19`, `GOFLAGS=-p=2` and `GOCACHE` outside the
home directory: `golangci-lint run ./jobqueue/`, `cleanorder -min-diff` on
edited files, and the red command below in race and plain modes.

- [x] TestDBEncodeJob (jobqueue/db_encode_test.go, assertion ~line 83) fails
  occasionally under -race: `Expected '3128' to be less than '3072'` (an
  allocation/bytes bound averaged over jobEncodeRuns encodes). Seen 2 of 40
  under -race on develop; plain runs pass.
  - Source: deferred incidental in `261002-client-restart-2ca6d42f.md` (full
    jobqueue `-race` run during PR #665).
  - Red: `CGO_ENABLED=1 go test -race -tags netgo -count 300 ./jobqueue/ -run
    '^TestDBEncodeJob$'` exits 1, 21 of 300 runs failing, e.g.
    `Expected '3090' to be less than '3072' (but it wasn't)!`. At `-count 40`
    it failed 1 of 40.
  - Cause: under `-race`, `sync.Pool.Put` drops a random quarter of what it is
    given (`runtime_randn(4) == 0` in `sync/pool.go`), so about one encode in
    four makes a new Encoder. Per-encode bytes on 1 P, 200 encodes: plain
    mode 2312 B for nearly all; race mode 2312 B for 143 to 160 and 4864 B
    (a new Encoder) for 40 to 56. The mean, about 2950 B, sits just under the
    3072 B bound, so a run with more drops than usual crosses it. The race
    detector's instrumentation is not the cause.
  - Fix: `jobEncodeMedianBytes` measures each encode on its own and asserts
    on the median instead of the mean. The median stays at a reused Encoder's
    cost unless at least half the encodes make a new one, which happens on
    every encode if `db.encode` stops reusing Encoders (the #664 regression
    the bound guards). The bound is unchanged and the test still runs under
    `-race`.
  - Mutant: `ok = false` after `db.encoders.Get()` in `db.encode` (always a
    new Encoder) fails 5 of 5 plain (`Expected '4496' to be less than
    '3072'`) and 5 of 5 under `-race` (`'4864'`).
  - Green: the red command at `-count 300` exits 0; `-race -count 100` exits
    0; plain `-count 50` exits 0. `golangci-lint run ./jobqueue/`: 0 issues.
  - Files: `jobqueue/db_encode_test.go`.

## Delivery queue

- encodejob-race-2a0b91ec -> develop, no dependencies. Status: fix reviewed
  (PASS); running make lint/test/race, then PR and pr-resolver.
