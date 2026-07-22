# Phase 2: Concurrent RPC readers (section B)

Ref: [spec.md](../spec.md) section B1

## Instructions

Use the `orchestrator` skill to complete this phase, coordinating
subagents with the `go-implementor` and `go-reviewer` skills.

TDD throughout: the safety test (B1.1) is the N4 HARD REQUIREMENT and runs under
`-race`; each acceptance test must fail before and pass after. Build/test with
`-tags netgo`; unset ALL `OS_*` env vars for `make test` / `make race`;
GoConvey `So()` assertions; copyright headers on new files.

This phase comes AFTER Phase 1 because Phase 1 removed the per-transition
exclusive serialiser (`repGroupCounts.mu`) and the serial drainer; adding
concurrent readers then meaningfully raises admission throughput rather than
merely queuing behind a still-serialised hot path. Depends on Phase 1. It is a
single sequential item confined to `jobqueue/server.go`; no wire/protocol
change, so it stays compatible with existing runners and CLIs.

If concurrent handling on this socket proves genuinely unsafe (a documented wr
foot-gun), surface it as a BLOCKER per agent-conduct rather than switching
designs silently.

## Items

### Item 2.1: B1 - N concurrent RecvMsg readers on the existing socket

spec.md section: B1

Today a single reader admits one RPC at a time: `serveClients` loops
`receiveClientMessage` -> `sock.RecvMsg()` (server.go:2656/2671), dispatching
each to its own goroutine, so control/status RPCs (`wr status`, `wr limit`,
`wr suspend`) queue behind reserve/touch/archive traffic. Change admission to N
concurrent readers on the SAME `xrep` mangos socket (server.go:2396). No new
port, no wire change.

Design (proven safe against mangos v3.4.2 - spec "Concurrent readers"):

- `xrep.socket.OpenContext()` returns `protocol.ErrProtoOp`, so mangos Contexts
  are unavailable on this raw REP socket; the design is raw concurrent
  `RecvMsg()`, NOT Contexts.
- Concurrent `RecvMsg()` is safe: it snapshots channel refs under a brief lock
  then does a `select`-receive on the shared `recvQ` channel; Go channel
  receives fan out one message per receiver, so N reader goroutines admit
  distinct messages without cross-talk.
- Reply routing is unaffected: each received message carries its pipe-ID header
  (set by the pipe receiver); `reply` -> `s.sock.SendMsg(m)` (serverCLI.go:1977)
  uses that header, and `SendMsg` already runs concurrently today. Which reader
  admitted a request does not change which client gets the reply.
- Launch `numRPCReaders` goroutines running the `serveClients` loop
  (server.go:2538) sharing `stopClientHandling`. `clientHandlingDone` must close
  only after ALL readers exit (e.g. a `sync.WaitGroup`), so
  `waitForClientHandling` (server.go:1144) still blocks until serving has fully
  stopped.
- `numRPCReaders`: a small fixed constant (e.g. 4-8), documented; a package var
  so tests can lower/raise it. Not user-configurable (internal-only).

Tests in the new file `jobqueue/reliable2_readers_test.go`. Covers both B1
acceptance tests (map to Issues 1-3): (1) safety, `-race` - with
`numRPCReaders > 1` and M concurrent clients each issuing a distinct
request-reply RPC round-trip, every client receives exactly its own correct
reply (no misrouted, dropped, or duplicated reply) and the `-race` detector
reports no data race (this is the N4 HARD REQUIREMENT proof); (2) admission
fairness, SUPPORTING - with the server saturated by many concurrent goroutines
issuing reserve/touch RPCs in a tight loop, a control RPC (e.g.
`GetStatusByRepGroupMatch` or a limit/suspend call) returns within a bounded
time (low seconds, no 60s timeout) when `numRPCReaders > 1`, whereas with a
single reader (`numRPCReaders = 1`) it is starved. Test 2 SUPPORTS but is not
the sole evidence; the headline responsiveness claim is Tier B (real LSF at
scale, Phase 5).

- [ ] implemented
- [ ] reviewed

## Regression guards (KEEP surfaces, section E1)

Re-run after this phase; all must stay green under `-race` (spec.md section E1):

- Background recovery window tests (`recoverInBackground`/`isRecovering`/
  `ErrRecovering`/`rescheduleReadyAfterRecovery`).
- `jobqueue/subscription_test.go` (#503), `jobqueue/live_jtouch_test.go`
  (live RAM/CPU/STDOUT incl. ssh-to-host), the `JobUpdateResync` reconnect/
  resync tests, `jobqueue/suspend_resume_test.go` + `wr status --suspended`,
  `jobqueue/modify_validation_test.go`, `jobqueue/serverWebI_test.go`, the
  `wr add --sync` client test.
- `jobqueue/reliable2_keep_test.go`, `jobqueue/reliable2_completion_test.go`,
  `jobqueue/reliable2_lost_test.go`, `jobqueue/reliable2_dbcompat_test.go`.
- `make test`, `make race`, `make lint` all clean (with all `OS_*` env vars
  unset).
