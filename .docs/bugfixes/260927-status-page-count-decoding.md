# Status page count decoding

- [x] Before this fix, `writeStatusCountSeed` (`jobqueue/serverWebI.go`)
  called `getCompleteJobsByRepGroup` for every live rep group. That fully
  decoded every archived record of the group, `Cmd` and env included, only to
  count them, and every such record is `complete`. It did this on each page
  load and on each websocket reconnect, holding that connection's write mutex,
  though `retrieveCompleteJobStatusByRepGroup(rg, false)` already counted
  without decoding.
  - Source: prodsim soak finding 3 (`.docs/bugfixes/260927-prodsim-findings.md`
    on branch `prodsim`). With 3 simulated users the seed path took 9.7-14% of
    manager CPU, and `ws_seed` latency rose to 3.1-8.4s mean (max 37s).
  - Red command:
    `CGO_ENABLED=1 go test -tags netgo --count 1 ./jobqueue -run TestStatusSeedCountsWithoutDecodingHistory`
    (exit 1). The test seeds 500 archived jobs into a rep group with 3 live
    dependent jobs, sends the page's "current" request over the status
    websocket, and counts full archived decodes with `db.archivedDecodes`:

    ```
    Line 91:
    Expected: 0
    Actual:   500
    (Should equal)!
    ```

    With only the decode assertion disabled, the same test passed on the
    unfixed code, so its count assertions (per-state seed counts equal the full
    decode's `statusStateCounts` plus the live jobs) describe what the page got
    before the fix.
  - Fix, in `jobqueue/serverWebI.go`: `writeStatusCountSeed` adds
    `retrieveCompleteJobStatusByRepGroup(rg, false).Counts` instead of
    `statusStateCounts(getCompleteJobsByRepGroup(rg))`. Both skip a key that
    is live again (being re-run), and every archived record is `complete`, so
    the page gets the same counts. The seed shows nothing else from the
    history.
  - Test: `jobqueue/status_seed_decode_test.go`. After the fix it passes with 0
    decodes and the same per-state counts.
  - Audit of the other web and REST status paths that read archived history:
    - `wr status -o counts` and the other summary requests
      (`getStatusByRepGroup`) already use the count-only lookup unless details
      are asked for.
    - The web page's rerun (`completedJobsByRepGroup`) filters on each job's
      exit code and fail reason, so it needs the decoded jobs.
    - The web page's job details (`sendJobDetails`), REST
      `/rest/v1/jobs/<repgroup>` and `wr status -i` return the jobs
      themselves.
    - Subscription catch-up (`subscriptionCatchUpRepGroupRecords`) needs each
      job's key and end state.
    - The REST modification target already skips history.

    None of these decodes only to count, so nothing else changed.
