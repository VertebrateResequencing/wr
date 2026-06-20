# Issue Triage Lists

Scope: current open GitHub issues for `VertebrateResequencing/wr` on 2026-06-20, excluding open pull requests. One issue that appeared in an older paginated HTML view was already closed during this triage, so it is not included below.

## Can't Fix

- [#382 Add native support for public clouds such as AWS and GCP](https://github.com/VertebrateResequencing/wr/issues/382): This is a large cloud-provider and storage-placement feature that would need AWS/GCP credentials, infrastructure decisions, and real cloud validation.
- [#330 Runners failing due to "failed to create new OS thread"](https://github.com/VertebrateResequencing/wr/issues/330): The failure was tied to specific OpenStack flavors, OS images, and host limits, and the issue itself notes the practical mitigation was limiting zero-core runners.
- [#329 High memory usage when writing files to mount](https://github.com/VertebrateResequencing/wr/issues/329): This needs large S3 writes through MuxFys and cloud credentials to reproduce or measure meaningfully.
- [#328 Lost jobs when using singularity/s3, high runner cpu usage](https://github.com/VertebrateResequencing/wr/issues/328): The repro depends on OpenStack workers, Singularity images, S3 mounts, and long genomics jobs that are not available locally.
- [#323 Chunk reads from mount needs improving](https://github.com/VertebrateResequencing/wr/issues/323): The reported behavior depends on Ceph/S3 range-read behavior, iRODS/baton, and MuxFys under external load.
- [#321 Jobs fail if too many need mounts at once](https://github.com/VertebrateResequencing/wr/issues/321): This appears to involve per-user FUSE or remote mount limits and needs the production mount environment to validate.
- [#319 Reading files from mount can cause cmds to segfault](https://github.com/VertebrateResequencing/wr/issues/319): The repro combines MuxFys, remote object storage, bwa, samtools, and large reference data.
- [#318 Warning deleting mounted files we didn't read or create](https://github.com/VertebrateResequencing/wr/issues/318): This needs a cached writable S3 mount and remote delete behavior that cannot be exercised without storage credentials.
- [#315 Cloud deploy doesn't work with a cloudforms server?](https://github.com/VertebrateResequencing/wr/issues/315): The failure is specific to a CloudForms/OpenStack host and local networking configuration.
- [#314 Strange db performance issue, disk dependant](https://github.com/VertebrateResequencing/wr/issues/314): The symptoms are tied to Sanger LSF behavior and particular disks/filesystems, including Lustre and site hosts.
- [#313 Is there a db-backup related speed bottleneck?](https://github.com/VertebrateResequencing/wr/issues/313): The hang involves S3-backed database backups and FUSE behavior that require the original remote storage setup.
- [#299 Test manager resilience in cloud deployments](https://github.com/VertebrateResequencing/wr/issues/299): These tests require production OpenStack deployment, head-node deletion, and domain-IP updates.
- [#285 Cleanup for failed mounted jobs should delete output files](https://github.com/VertebrateResequencing/wr/issues/285): The fix depends on MuxFys exposing reliable remote-output tracking and needs mounted write tests.
- [#284 File Staging](https://github.com/VertebrateResequencing/wr/issues/284): This is a broad workflow/storage architecture feature rather than a contained bug fix.
- [#283 Refactor codebase](https://github.com/VertebrateResequencing/wr/issues/283): This asks for a full-codebase architectural refactor and 100% coverage, which is too open-ended to tackle as a single issue.
- [#192 Write Nextflow support](https://github.com/VertebrateResequencing/wr/issues/192): This is a separate Nextflow scheduler/integration project and the issue notes it is not strictly for this repository.
- [#117 Task Execution Service - GA4GH APIs](https://github.com/VertebrateResequencing/wr/issues/117): TES support would require API design, auth choices, and file input/output semantics beyond the current REST surface.
- [#73 OpenStack: Watch host resources to identify lockups](https://github.com/VertebrateResequencing/wr/issues/73): Validating this needs real OpenStack workers that exhibit the low-load lockup behavior.
- [#66 OpenStack - additional storage should be mounted as additional volume](https://github.com/VertebrateResequencing/wr/issues/66): This needs OpenStack block-device behavior and volume lifecycle testing.
- [#54 Feature - OpenStack volume passing between dependant jobs](https://github.com/VertebrateResequencing/wr/issues/54): Passing volumes between dependent cloud jobs is a major OpenStack orchestration feature.
- [#53 Cloud schedulers: better/no server reuse](https://github.com/VertebrateResequencing/wr/issues/53): This requires cloud scheduling policy changes and real OpenStack validation around reuse, isolation, and teardown.
- [#37 Workflows: add CWL support](https://github.com/VertebrateResequencing/wr/issues/37): CWL support would require an external workflow parser/runner layer, not a small change in wr.
- [#36 Alter cloud deployment max servers while live](https://github.com/VertebrateResequencing/wr/issues/36): This needs live OpenStack scheduler reconfiguration and remote-manager coordination.
- [#31 Resource Token system](https://github.com/VertebrateResequencing/wr/issues/31): Limit groups cover part of the need, but the full token server/rate-limit design is a large distributed feature.
- [#22 Long-running commands should have the option of being checkpointed](https://github.com/VertebrateResequencing/wr/issues/22): Checkpoint/restart depends heavily on OS and application support and has no obvious general implementation path here.

## Invalid

- [#327 SSH to trusty servers has network timeout issues](https://github.com/VertebrateResequencing/wr/issues/327): The report was specific to Ubuntu Trusty images and says switching to Bionic worked, so it is obsolete as a current wr issue.

## Solved

- [#453 wr manager in cloud mode fails to update stats of reserved cpus when OS image is not available](https://github.com/VertebrateResequencing/wr/issues/453): Current OpenStack spawning drops reserved quota counters via `usingQuotaCB`, and the changelog notes a fix for wrong quota after failed spawns.
- [#444 Wrong Cwd reported in status while running](https://github.com/VertebrateResequencing/wr/issues/444): Current job status uses `ActualCwd` recorded by the runner rather than inventing a fresh hashed cwd.
- [#400 --limit option for the wr status command is not ducumented](https://github.com/VertebrateResequencing/wr/issues/400): `wr status` help now documents `--limit` for details output.
- [#326 Adding a dependent job with --rerun results in unexpected behaviour](https://github.com/VertebrateResequencing/wr/issues/326): The issue body says the bad behavior was fixed, and current tests cover rerunning completed jobs.
- [#317 status webpage: lost contact jobs show wrong buttons](https://github.com/VertebrateResequencing/wr/issues/317): The current status page shows lost jobs with confirm-dead handling rather than the old mixed action set.
- [#296 Update or find alternative to go-daemon](https://github.com/VertebrateResequencing/wr/issues/296): `go.mod` now uses `github.com/sevlyar/go-daemon v0.1.6`, with no newer update reported by `go list -m -u`.
- [#292 Update OpenStack implementation with latest gophercloud abilities](https://github.com/VertebrateResequencing/wr/issues/292): The code is already on `gophercloud/v2 v2.12.0`, with no newer update reported.
- [#291 Disk space learning should ignore writes inside mounts](https://github.com/VertebrateResequencing/wr/issues/291): Current execution code excludes mounted directories from local cwd disk checks and tracks mount cache directories separately.
- [#297 Make binary smaller?](https://github.com/VertebrateResequencing/wr/issues/297): The Makefile now builds with stripped linker flags `-s -w`.
- [#92 Add a website front-page to readthedocs](https://github.com/VertebrateResequencing/wr/issues/92): The changelog records a new Read the Docs documentation site replacing the old README/wiki content.
- [#39 OpenStack scheduler: spawned servers only need 2 ports](https://github.com/VertebrateResequencing/wr/issues/39): Spawned OpenStack workers are configured with SSH and manager ports, not the web interface port, and the original port-quota concern was partly a misunderstanding.
- [#25 Status: bring up past commands and add paging](https://github.com/VertebrateResequencing/wr/issues/25): The current status UI has search and load-more pagination, while CLI/REST status paths support limits and offsets.
- [#16 Implement bash completion](https://github.com/VertebrateResequencing/wr/issues/16): The changelog documents `wr completion bash` support.
- [#15 Tests needed for WebI and Cmd](https://github.com/VertebrateResequencing/wr/issues/15): The repo now has dedicated WebI websocket tests and command tests, including `cmd/status_test.go`.

## Todo

- [#507 Option to only add the first x command in a file as a test](https://github.com/VertebrateResequencing/wr/issues/507): `wr add --file` exists, but there is no first-N/head option for trying only the first few commands from an input file.
- [#506 Table-like report mode for wr status](https://github.com/VertebrateResequencing/wr/issues/506): The current status command does not appear to offer a configurable table/column report mode, and adding one is a contained CLI formatting feature.
- [#502 Distinguish between command killed due to high memory usage and failed for another reason but had memory higher than expected](https://github.com/VertebrateResequencing/wr/issues/502): The current runner still reports `FailReasonRAM` whenever a failed command exceeded expected peak RAM, so the message/logic is a contained fix.
- [#310 Status webpage: times out](https://github.com/VertebrateResequencing/wr/issues/310): The current websocket client reports connection loss but does not auto-reconnect.
- [#316 Unexpected dependency behaviour](https://github.com/VertebrateResequencing/wr/issues/316): Unknown dependency groups still need either clearer help text or a deliberate "wait for future dep group" mode.
- [#302 Switch from gorilla to Gobwas?](https://github.com/VertebrateResequencing/wr/issues/302): The project still uses Gorilla websockets, so a local library-swap experiment is possible.
- [#301 Update error handling](https://github.com/VertebrateResequencing/wr/issues/301): Updating comparisons and type assertions to `errors.Is`/`errors.As` is a current, local cleanup.
- [#295 Use build tag osusergo?](https://github.com/VertebrateResequencing/wr/issues/295): Builds use `netgo` and `CGO_ENABLED=0`, but adding or deciding against `osusergo` remains a small build-policy task.
- [#290 Improve efficiency of client methods](https://github.com/VertebrateResequencing/wr/issues/290): Client methods can be audited locally for oversized request payloads.
- [#288 Show scheduler issues on the command line](https://github.com/VertebrateResequencing/wr/issues/288): Scheduler warnings exist for the web/REST paths but are not surfaced by `wr status` as requested.
- [#287 Extend LSF emulation](https://github.com/VertebrateResequencing/wr/issues/287): The current LSF command still documents limited bsub support, so adding normal command-line bsub syntax is a focused parser/CLI task.
- [#286 Impossible req warnings should appear in status](https://github.com/VertebrateResequencing/wr/issues/286): Current scheduler messages exist, but `wr status` should be checked and extended to expose impossible-resource details clearly.
- [#251 Implement log rotation](https://github.com/VertebrateResequencing/wr/issues/251): The manager still writes a configured log file without an obvious rotation policy.
- [#207 Allow to suspend and resume non-running jobs](https://github.com/VertebrateResequencing/wr/issues/207): Limit groups are a workaround, but a first-class suspended state remains implementable.
- [#197 Allow job modification using web/REST interfaces](https://github.com/VertebrateResequencing/wr/issues/197): `wr mod` exists, but the web UI and public REST API do not expose equivalent editing.
- [#194 wr mod: allow modification of dep groups and bsub mode](https://github.com/VertebrateResequencing/wr/issues/194): The current `mod` command explicitly excludes dependency groups and bsub mode.
- [#98 "Live" job introspection](https://github.com/VertebrateResequencing/wr/issues/98): The runner touches jobs, but live peak resource/stdout/stderr updates are still not sent as requested.
- [#72 Openstack failed job should retry on a different host](https://github.com/VertebrateResequencing/wr/issues/72): A normal user-command failure can still leave the host reusable, so retry-on-different-host behavior remains to be added.
- [#28 Dependencies: choose to make them un-"live"](https://github.com/VertebrateResequencing/wr/issues/28): There is no visible way to add or toggle a non-live dependency state.
- [#20 Status webpage: add rerun button](https://github.com/VertebrateResequencing/wr/issues/20): The web UI has retry for buried jobs but no rerun button for completed jobs.
- [#19 Cmd env vars and expected resource requirements should be editable](https://github.com/VertebrateResequencing/wr/issues/19): These edits are possible through `wr mod` but not through the status webpage.
- [#17 Cloud deployment should work from MacOS -> linux](https://github.com/VertebrateResequencing/wr/issues/17): Cloud deploy still uploads the local executable, so a Darwin client can still copy the wrong binary to Linux.

## Investigate

- [#448 Limits not always working?](https://github.com/VertebrateResequencing/wr/issues/448): The current limiter is substantially different, but lost-job/limit accounting needs a reproduction to know if the symptom remains.
- [#333 wr mod: too slow on lots of jobs](https://github.com/VertebrateResequencing/wr/issues/333): The reported timeout on about 11,705 jobs needs a current benchmark before deciding whether it still regresses.
- [#324 Strange start/end time issue with quick-running dependent jobs](https://github.com/VertebrateResequencing/wr/issues/324): Current stored times are richer, but status output still truncates some timestamps, so the dependency-order concern needs reproduction.
- [#322 wr status: can be unexpectedly slow](https://github.com/VertebrateResequencing/wr/issues/322): A large completed-job database should be generated locally to see where current status time is spent.
- [#309 Odd failure and status reporting of an --on_failure cmd](https://github.com/VertebrateResequencing/wr/issues/309): The behavior/reporting confusion needs a fresh minimal behavior repro.
- [#303 Improve allocation efficiency?](https://github.com/VertebrateResequencing/wr/issues/303): The old profiling notes were inconclusive, so current add/complete/mod workloads should be profiled before making changes.
- [#45 Status webpage: sometimes jobs appear pending when they aren't](https://github.com/VertebrateResequencing/wr/issues/45): The status UI has been rewritten, but this stale-count symptom should be checked with a current browser/websocket repro.
- [#12 Status webpage: refreshing while running causes problems](https://github.com/VertebrateResequencing/wr/issues/12): Current WebI tests cover several websocket races, but the exact browser refresh scenario still needs manual or browser-level reproduction.
