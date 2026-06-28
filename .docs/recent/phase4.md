# Phase 4: CLI wiring + help (cmd/status.go)

Ref: [spec.md](spec.md) sections C1, C2, C3, C4, C6

## Dependencies

Depends on Phase 2 (`Client.GetRecent`) and Phase 3
(`parseRecentDuration`). Start only after both are reviewed.

## Instructions

Use the `orchestrator` skill to complete this phase, coordinating
subagents with the `go-implementor` and `go-reviewer` skills.

The pure-function items (C2 `countGetJobArgs`, C3
`validateStatusStateFilters`, C5 `parseRecentDuration` from Phase 3) are
unit-testable without a server; the end-to-end items (C1, C4) drive
`statusCmd.Run` against a live test server via `startStatusTestServer` +
`runStatusForTest`; C6 asserts rendered help via
`commandHelpForTest(statusCmd)` (no server). Extend `resetStatusForTest`
to reset `cmdRecent` and the `recent` flag to `""`.

The items below share `cmd/status.go` and are tightly coupled (the same
`--recent` flag and `Run` wiring), so implement them as one sequential
pass in the order listed, then review the phase together.

## Items

### Item 4.1: C2 - --recent is mutually exclusive with -f/-i/-l

spec.md section: C2

Add package var `cmdRecent string` and the `--recent` flag
(`StringVar`, long flag only). Update `countGetJobArgs()` to add
`if cmdRecent != "" { set++ }`, and update the mutual-exclusion `die`
message constant to mention `--recent` and "mutually exclusive" (e.g.
"-f, -i, -l and --recent are mutually exclusive; only specify one of
them"). File: `cmd/status.go`. Test file: `cmd/status_test.go`. Covering
all 5 acceptance tests from C2.

- [x] implemented
- [x] reviewed

### Item 4.2: C3 - --recent rejects state filters

spec.md section: C3

Add sentinel `errStatusStateFiltersRecent` (message mentions "state
filters" and "--recent") and a `case cmdRecent != "":` in
`validateStatusStateFilters`, checked before the existing file/cmdline
cases, so any state filter (including missing-deps) combined with
`--recent` is rejected, while no filter set returns nil. File:
`cmd/status.go`. Test file: `cmd/status_test.go`. Covering all 3
acceptance tests from C3.

- [x] implemented
- [x] reviewed

### Item 4.3: C1 - --recent flag selects recent archived jobs end-to-end

spec.md section: C1

Wire `--recent` into the job-selection path: make
`statusRequiresFullJobFetch()` return true when `cmdRecent != ""` (recent
has no fast-status summary), add a `case cmdRecent != "":` in `getJobs`
that parses with `parseRecentDuration` (die on error) and calls
`jq.GetRecent(period, statusLimit, "", statusOutputGetsStd(outputFormat),
showEnv)`. The existing `--limit`/grouping, `showEnv` gating and all `-o`
switch arms apply unchanged, rendering recent results like the other
modes (details/plain/json). File: `cmd/status.go`. Test file:
`cmd/status_test.go`. Covering all 3 acceptance tests from C1. Depends on
Items 4.1 and 4.2 (and Phases 2 and 3).

- [x] implemented
- [x] reviewed

### Item 4.4: C4 - --limit, --std/--env and --host honoured

spec.md section: C4

Confirm via tests that recent results obey `--limit` (the "+ N other
commands with the same status" grouping), `--std`/`--env`, and the
client-side `--host` post-filter, using the same code paths as the other
modes (no new wiring beyond C1; the existing post-filter and limit blocks
apply). File: `cmd/status.go`. Test file: `cmd/status_test.go`. Covering
all 2 acceptance tests from C4. Depends on Item 4.3.

- [x] implemented
- [x] reviewed

### Item 4.5: C6 - long help documents --recent

spec.md section: C6

Extend `statusCmd.Long`: update the opening selection sentence to list
`-f, -l, -i or --recent`; add a paragraph documenting `--recent
<duration>` (returns jobs that finished/were archived within the last
duration across all report groups; mutually exclusive with -f/-i/-l;
accepts Go duration units plus `d` (days) and `w` (weeks); state filters
unsupported; `--limit`, `--std`/`--env` and `--host` honoured; example
"`--recent 1w` reports jobs that finished in the last week"); and make the
`--recent` flag usage string mention the `d`/`w` units. Test file:
`cmd/status_test.go` (via `compactWhitespace(commandHelpForTest(t,
statusCmd))`). Covering all 4 acceptance tests from C6. Depends on Item
4.1 (flag must exist).

- [x] implemented
- [x] reviewed
