# Incidental findings during customdb delivery

Branch-owned list of findings that came up during implementation and review
and are not part of the current item. Each is resolved before the PR is
called ready, or explicitly deferred with the owner.

- [ ] (from 0a.1 review) The rerun guard's member check has no `*db`-level
  test: changing the `archiveJobTx` `addStored` closure to `live != nil`
  survives every test. Add a bbolt test that archives a guarded dependent
  after `setDependents` but before the add's write commits, and asserts it
  is not kept live. Pre-existing gap at d1a4b6d7; C4 tests 9-11 cover the
  log store in Phase 1.
- [ ] (from 0a.1 review) spec.md's store code block declares
  `storeKindLog`/`storeKindBolt`, which Phase 0 omits as unused; add them
  in Phase 1 (1c.1/1e.4) when the log kind is first read.
- [ ] (from 0a.1 implementation) `cleanorder -min-diff` wants to move
  untouched declarations in `reliable4_status_limit_test.go` and
  `runner_lifecycle_test.go`; pre-existing ordering drift, left alone.
