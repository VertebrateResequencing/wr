# Manual verification checklist

A list of manual tests to confirm each change actually works. Tick a box once
you've run the test and seen the expected result.

## Before you start

- Build `wr` from the branch under test and start a manager (development
  deployment): `wr manager start`. It prints the web interface URL (default
  `https://localhost:11302/`); accept the self-signed certificate.
- `wr add` reads commands from STDIN (one per line) or from a file with `-f`.
  `-i` sets the reporting group that `wr status -i` and the web page filter on.
- For the `curl`/REST tests, the auth token is in
  `~/.wr_development/client.token`: `TOKEN=$(cat ~/.wr_development/client.token)`.
- A throwaway "always fails" command file is handy for several tests:
  `printf 'exit 1\n' > /tmp/fail.txt`.
- One feature (**suspend / resume**, test 11) is not on `develop` yet — build
  its own feature branch to test it.

---

## A. Adding commands (`wr add`)

- [x] **1. Only add the first N commands from a file**
  1. `printf 'echo 1\necho 2\necho 3\necho 4\necho 5\n' > /tmp/cmds.txt`
  2. `wr add -f /tmp/cmds.txt --head 2 -i headtest`
  3. Confirm it reports `Added 2 new commands ...` and `wr status -i headtest -o c`
     shows **2** jobs total, not 5.
  4. Confirm `wr add -f /tmp/cmds.txt --head 0 -i headtest0` adds all **5**, and
     `wr add -f /tmp/cmds.txt --head -1 -i x` errors with `--head can't be negative`.

- [x] **2. Re-running a command that has incomplete dependencies waits (not skipped, not run immediately)**
  1. `echo 'echo parent' | wr add -i dep-parent -e groupA` then
     `echo 'echo child' | wr add -i dep-child -d groupA`. Wait until
     `wr status -i dep-child -o c` shows `complete: 1`.
  2. Make `groupA` incomplete again with a long carrier:
     `echo 'sleep 300' | wr add -i dep-parent -e groupA`.
  3. Re-add the identical child with rerun:
     `echo 'echo child' | wr add -i dep-child -d groupA --rerun`.
  4. Confirm the add reports it as **1 new command** (it is _not_ counted as a
     duplicate), and `wr status -i dep-child` shows it as
     `Status: dependent on other jobs` — i.e. it is **waiting**, not running.
  5. Confirm it only runs after the `sleep 300` carrier finishes.

- [x] **3. Depending on a dependency group that doesn't exist yet blocks (and is visible)**
  1. `echo 'echo waiter' | wr add -i futuretest -d futuregrp`
  2. Confirm a warning is printed to stderr at add time:
     `dependency group "futuregrp" has not been seen; dependent job(s) will wait until it appears`.
  3. Confirm it does **not** run: `wr status -i futuretest` shows
     `Status: waiting on dep group(s) not yet seen: futuregrp`.
  4. Confirm the filter lists it: `wr status --missing_deps` includes this job.
  5. Make the group appear: `echo 'echo appeared' | wr add -i futurecarrier -e futuregrp`.
  6. Confirm that once the carrier completes, the waiting job becomes runnable,
     runs, and no longer appears under `wr status --missing_deps`.

- [x] **4. Remote manager can use local-style cwd and environment (opt-in)**
      _(Needs a remote/cloud-deployed manager — against a local manager there is no
      difference.)_
  1. With the option OFF (default), add a job to the remote manager and check
     `wr status -i ... -o d`: its working directory defaults to `/tmp` and your
     submitting environment is not carried.
  2. Turn it on, either per-add with `--remote_same_as_local`, or via config
     `managerremotesameaslocal: true` / env `WR_MANAGERREMOTESAMEASLOCAL=true`.
  3. Re-add and confirm in `wr status -o d` that the job now defaults to **your
     current working directory** (not `/tmp`) and uses your submitting
     environment, exactly like a local-manager add (e.g. an `echo $MYVAR` command
     prints an exported `MYVAR` only when the option is on).

---

## B. Command-line status (`wr status`)

- [x] **5. Table output mode**
  1. `printf 'echo a\necho b\n' | wr add -i tabletest`
  2. `wr status -i tabletest -o table` (or `-o t`). Confirm an aligned table with
     header columns: `Command  ID  Status  Attempts  Host  Requirements group  Count`.
  3. Customise columns:
     `WR_STATUS_FORMAT="status:9 count:5 command:30" wr status -i tabletest -o t`.
     Confirm only those columns appear, in that order and at those widths.
     (Valid field names: `command`/`cmd`, `id`/`jobid`/`key`, `status`/`state`,
     `attempts`/`tries`, `host`, `reqgroup`/`requirements`, `count`/`similar`.)

- [x] **6. Scheduler problems shown as a footer on the command line**
  1. Cause a scheduler issue, e.g. ask for impossible resources:
     `echo 'echo toobig' | wr add -i toobig --cpus 100000` (or `-m 100000G`).
     - This does not cause a scheduler alert since the job just gets buried with
       "resource requirements cannot be met". Did work in openstack.
  2. Run `wr status` (details), `-o summary`, or `-o table`. Confirm the output
     ends with a `Scheduler alerts:` section listing entries such as
     `- Scheduler Issue: <message>` and, where applicable,
     `- Bad server: <name> (<id>, <ip>) ...`.
  3. Confirm the section is absent when there are no issues, and that it is **not**
     shown in `-o counts`, `-o plain` or `-o json` modes.

- [x] **7. Quick jobs report distinct start and end times**
      _(The CLI rounds times to whole seconds, so check via the API or web timeline.)_
  1. `echo 'echo quick' | wr add -i quicktime`; wait for it to complete.
  2. `TOKEN=$(cat ~/.wr_development/client.token)`
  3. `curl -ks -H "Authorization: Bearer $TOKEN" "https://localhost:11302/rest/v1/jobs/quicktime?state=complete" | jq '.[]|{Started,Ended,Walltime}'`
  4. Confirm `Started` and `Ended` are **different** numbers (nanosecond
     timestamps) and `Walltime` is greater than 0 (≈ `(Ended-Started)/1e9`) — i.e.
     a sub-second job no longer shows `Started == Ended`.

- [x] **8. Counts and summary stay fast on large histories**
  1. On a manager with many completed jobs in one report group (tens of
     thousands+), time them: `time wr status -i <biggroup> -o c` and
     `time wr status -i <biggroup> -o summary`.
  2. Confirm both return in about a second (not tens of seconds or a client
     timeout) and the printed counts match what `-o d` reports.
     _(To create load: `seq 1 30000 | sed 's/^/echo /' | wr add -i biggroup`, then
     wait for completion.)_

---

## C. Modifying commands (`wr mod`)

- [x] **9. `wr mod` is fast with many jobs**
  1. Add many jobs that stay queued (block them on an unseen group so they don't
     run): `seq 1 15000 | sed 's/^/echo /' | wr add -i bigmod -d neverappears`.
     Confirm `wr status -i bigmod -o c` shows ~15000 waiting.
  2. `time wr mod -i bigmod -p 50`
  3. Confirm it completes in a few seconds (not ~2 minutes / a timeout) and the
     change applied (`wr status -i bigmod -o d` shows priority 50).

---

## D. Failure reporting

- [ ] **10. "Killed for memory" vs "failed for another reason but used extra memory"**
  1. Put a command that uses lots of memory then exits non-zero in a file
     (avoids shell quoting):
     `printf '%s\n' 'perl -e '\''my $x = "A" x (200*1024*1024); exit 1'\''' > /tmp/memcmd.txt`
     (or any command that allocates well over 10M and exits non-zero).
  2. `wr add -f /tmp/memcmd.txt -i memtest -m 10M`
  3. After it fails, `wr status -i memtest -o d`. Confirm the failure reason is the
     **real** reason with a memory note appended:
     `command exited non-zero; note: command used too much RAM` — **not** just
     `command used too much RAM`.
     - The note did not appear; memory did get bumped though. Fixed. Need to
       also test in LSF mode to see the message from a real memory kill
  4. Confirm the memory requirement grew for the retry: `wr status -i memtest -o d`
     shows an expected RAM well above the original 10M, even though this was not a
     memory kill. (A genuine OOM-killed job would instead report
     `command used too much RAM` as the reason itself.)

---

## E. Suspending and resuming

- [x] **11. Suspend and resume queued commands**
  1. Add a job that stays non-running by waiting on an unseen group (the
     "has not been seen" warning here is expected):
     `echo 'echo hello' | wr add -i suspendtest -d holdgroup`.
  2. `wr suspend -i suspendtest`. Confirm:
     `Suspended 1 queued commands (out of 1 matching)`.
  3. Confirm the state: `wr status -i suspendtest -o plain` prints `<key>\tsuspended`,
     and `wr status --suspended -o c` shows `suspended: 1`. On the web page the job
     shows in a **suspended** section using the delayed (yellow/warning) colour,
     and its detail reads `suspended - use wr resume to make it schedulable again`.
  4. Confirm it does not run (wait a bit; still suspended).
  5. `wr resume -i suspendtest`. Confirm:
     `Resumed 1 suspended commands (out of 1 matching)`; the job returns to its
     normal waiting state.
  6. Satisfy the dependency: `echo 'echo carrier' | wr add -i holdcarrier -e holdgroup`;
     confirm the resumed job then runs to completion.

---

## F. Status web page

_(Open the URL the manager prints, e.g. `https://localhost:11302/`.)_

- [x] **12. The status page reconnects on its own**
  1. Open the status page; confirm jobs and counts load.
  2. Stop the manager. Confirm a banner appears:
     `Connection to the manager has been lost!`.
  3. **Without refreshing**, start the manager again. Confirm that within a few
     seconds the page reconnects by itself, the banner clears, and the counts /
     job list resync to the current state — no manual refresh needed.
     - banners do not clear. Fixed

- [x] **13. Rerun button on completed commands**
  1. `echo 'echo done' | wr add -i reruntest`; wait for it to complete.
  2. On the status page, open the completed group. Confirm a blue **Rerun** button
     is shown for the completed command.
  3. Click it. Confirm a confirmation dialog titled **"Rerun Completed Commands"**
     appears (same style as the remove confirmation), with Cancel and Rerun buttons
     (or "Rerun all" / "Rerun 1" when several similar jobs exist).
  4. Confirm. Confirm the command is re-added and runs again.

- [x] **14. Live memory, CPU, output and ssh command for running commands**
  1. Add a longer job that uses memory and prints output over time:
     `printf '%s\n' 'bash -c '\''for i in $(seq 1 60); do echo "progress $i"; head -c 5000000 /dev/zero | wc -c >/dev/null; sleep 1; done'\''' > /tmp/livecmd.txt`
     then `wr add -f /tmp/livecmd.txt -i livetest -t 5m -m 1G`.
  2. On the status page, open the running job. Confirm that, updating live on each
     heartbeat, it shows: a live **peak RAM** (turns red if it exceeds the expected
     RAM), a live **CPU** time, a live **wall time** that counts up each second,
     **STDOUT**/**STDERR** tail panels with the latest output, and an
     **ssh command** (`ssh user@host && cd <dir>`) with a copy button.
     - stdout appears, but ram is not shown. `wr status` also doesn't show
       anything. Fixed.
  3. (This live detail is served only over the authenticated https interface.)

- [x] **15. Edit a command from the web page**
  1. Get a job into a non-running incomplete state, e.g. bury one:
     `wr add -f /tmp/fail.txt -i modtest -r 0` (fails → buried).
  2. On the status page, open that job. Confirm a blue **Modify** button is shown
     (it appears for delayed / ready / dependent / buried jobs — not running or
     complete).
  3. Click **Modify**. Confirm a **"Modify Job"** dialog opens with the job's fields
     pre-filled (command, working dir, RAM, time, cores, disk, priority, retries,
     limit groups, dep groups, behaviours, env overrides, ...).
  4. Change a couple of fields (e.g. RAM to `512M`, add env override `FOO=bar`) and
     Save. Confirm the dialog closes and the job shows the new values.
  5. Make an invalid edit (e.g. RAM `abc`, or priority `256`) and Save. Confirm an
     error popup/alert appears in the dialog and it stays open so you can fix it.

---

## G. REST API

- [x] **16. Modify a command over REST**
  1. Get a job into a modifiable state (delayed / ready / dependent / buried) and
     note its key (`wr status -i modtest -o d`, or list via REST).
  2. `TOKEN=$(cat ~/.wr_development/client.token)`
  3. Change its memory (PATCH):
     `curl -ks -X PATCH -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" -d '{"memory":"512M"}' "https://localhost:11302/rest/v1/jobs/<job-key>"`.
     Confirm **HTTP 200** with a JSON body whose `jobs` entry shows the updated
     value, and that `wr status` reflects the change.
  4. Invalid edit: `-d '{"priority":256}'` → confirm **HTTP 400** with body
     `priority value (256) is not in the range 0..255`.
  5. Target a running/complete job → confirm it is refused (**404** `job not found`
     if nothing matches, or **409** `no editable jobs matched` if matched but not
     editable).

---

## H. Logging and configuration

- [x] **17. Log rotation**
  1. Start the manager with a tiny max log size and compression off so rotation is
     easy to see: `WR_LOGSMAXSIZEMB=1 WR_LOGSCOMPRESS=false wr manager start`.
  2. Generate log volume (add/run plenty of jobs) until the manager log passes ~1MB.
  3. Confirm rotation in the deployment directory (`~/.wr_development/`): alongside
     the live `log` file you see timestamped backups like
     `log-2026-06-24T10-30-45.123` (with `.gz` if compression is on).
     Defaults when unset: 500 MB max size, 3 backups, 28 days, compression on.
  4. Confirm runner file logs rotate too: start with
     `--runner_filelog <path>` plus the same tiny size, and confirm rotated
     runner-log files appear in the same way.

---

## I. Sanity checks (nothing user-visible should have changed)

- [x] **18. Errors are still reported clearly**
  1. With no manager running, `wr status` → confirm a clear connection error, not a
     panic or stack trace.
  2. `echo 'echo x' | wr add -m notabytesize` → confirm a clear error message about
     the bad memory value, not a crash.

- [x] **19. Completing, retrying, burying and heartbeating still work**
  1. Complete: `echo 'echo ok' | wr add -i trim-ok` → reaches `complete`.
  2. Bury then retry: `wr add -f /tmp/fail.txt -i trim-bury -r 0` → reaches
     `buried`; `wr retry -i trim-bury` → it runs again.
  3. Heartbeat: run a longer job (e.g. test 14's job) and confirm
     `wr status -o d` keeps updating its running stats (peak RAM, CPU, wall time)
     while it runs.

---

## J. Cloud-only (needs an OpenStack/cloud deployment)

- [x] **20. OpenStack doesn't leak reserved quota on a bad image**
  1. Deploy a cloud manager configured to use an OS image name that doesn't exist /
     isn't available.
     - impossible to test
  2. Add several jobs that require spawning new servers.
  3. Confirm spawns fail (bad image) but the manager stays responsive and the
     reserved-resource counters (reserved cores / RAM / instances) do **not** keep
     climbing with each failed spawn — the reservation is released every time, so
     the manager never locks up from exhausted quota.
     _(No cloud to hand? The release-on-failure path is covered by the automated
     scheduler tests: `go test ./jobqueue/scheduler/`.)_
