# Web status-bar flicker / transient overcount under a fast burst

## Status

**Fixed** (this PR). The flicker/overcount — and a worse, previously-unnoticed
*permanent* divergence on a browser that connects mid-burst — are resolved by a
purely **client-side**, order-independent occupancy reconciliation in
`jobqueue/static/js/wr/websocket-handler.js`. There is **no server change**, so
the reliability constraints that motivated reverting #533 are untouched. See
[`solution.md`](solution.md) for the fix (and why other approaches were
rejected) and `DEVELOPERS.md` §2 rule 10.

The rest of this document is the **original problem statement** the fix
addresses, retained for context. Before the fix this was a known, accepted,
**cosmetic and self-correcting** residual behaviour: the deliberately-accepted
v0.36.5-quality tradeoff that came back when #533 was reverted on the
`reliable2` branch (now merged to `develop`). Read the present-tense
descriptions below as "how it behaved before this PR".

## What you see

Open the web UI's status page and add a large batch of very fast jobs (e.g.
10000 `echo`s). While the batch is completing you may see, in the browser:

- **Flicker** — a repgroup's progress bar briefly dips/zeroes and repaints on
  successive updates instead of growing smoothly.
- **Transient overcount** — the displayed total for a repgroup (or the `+all+`
  aggregate) momentarily reads *higher* than the number of jobs actually added,
  before settling.

Both artifacts **resolve on their own**: once the burst drains, the bar
converges to the true, correct totals. A page refresh / reconnect also re-seeds
to the correct state immediately. Nothing is stuck, lost, frozen, or
permanently wrong.

## What this is NOT (the important distinction)

The old, severe bugs (`.docs/bugfixes/260625-5/6/7`, and the freeze fixed in
`260723-1`) were **wrong _and permanent_**: the bar would stick at "1 running"
forever, lose jobs, or freeze short of completion, and only a full reconnect —
sometimes not even that — would recover it. Those are **fixed** and cannot be
reproduced on current code (verified empirically: a client connected throughout
a run converges to the true totals; mass removal shows `deleted:N`, not a stuck
`ready:N`; a 10000-job burst converges to `complete:10000` with nothing
dropped).

This remaining issue is **transient _and self-correcting_**: briefly twitchy
during the burst, always correct once it settles. Same visual family, opposite
severity.

## Why it happens (mechanism, not a fix)

The status web feed sends `jstateCount` **deltas** — per-transition
`from→to` increments — over the never-drop `caster`. The transport is now
lossless (that is what `260723-1` fixed), so no delta is ever dropped and the
reconstructed total is always eventually exact.

The flicker/overcount is a **client-side rendering-timing artifact**, not a
transport loss:

- Under a fast burst, many deltas for *different* transitions
  (`ready→running`, `running→complete`, …) arrive interleaved and are applied
  one at a time. Between applications the summed state can momentarily sit above
  or below the settled value, and the bar repaints each intermediate state.
- The initial scan-on-connect seeds counts (incomplete-only for `+all+`,
  per-RepGroup adds complete via `getCompleteJobsByRepGroup`); deltas then layer
  on top. During a burst the seed + in-flight deltas can briefly double-count a
  job that is transitioning right as the seed is taken.

Under #533 an absolute server-side counter (`repGroupCounts` / `jstateAbsolute`)
made the displayed count exact and eliminated these artifacts — but that
machinery is exactly what caused the serious reliability failures (a
server-wide exclusive lock on the per-transition hot path, cold-scanning
completed-job history on startup). Reverting #533 (Option R) restored the
delta-only feed and, with it, the pre-#533 transient flicker. See
`DEVELOPERS.md` §2 rule 10 and `.docs/reliable2/`.

The browser-fixture guards that used to assert the *transient* quality
(`status-page-stale-counts`, `status-page-snapshot-twitch`,
`repgroup-flicker-overcount`) were removed as revert casualties, consistent with
this being the accepted tradeoff. The fixtures guarding the *permanent* failure
modes (`repgroup-bar-flicker`, `removed-jobs-refresh`,
`completed-repgroup-visibility`) remain and are green.

## Reproduction

### The `wrdev.sh` script does NOT show this artifact — and why

`developers/wrdev.sh web-burst [N]` exists for the *related* problem (the
permanent freeze). It drives a **slow `wsprobe`** — a deterministic client that
consumes the feed in FIFO order and reconstructs the final count. That proves
the transport is lossless and converges (i.e. the permanent bug is gone), and it
is the right tool for that job. But `wsprobe` does not *render* intermediate
states the way a browser does, so it will **never display the flicker or the
transient overcount**. Running `web-burst` and seeing it converge to
`complete=N` is expected and is *not* evidence the flicker is absent.

The flicker is a **real-browser rendering-timing** effect, so it must be
reproduced in a browser.

### Manual browser reproduction

1. Start an isolated local manager (safe; never touches production):

   ```
   developers/wrdev.sh start local
   ```

   Note the printed web URL + token, e.g.
   `https://<host>:51781/?token=<token>`.

2. Open that URL in a real browser and go to the **status** page. Leave it open
   and connected *before* adding jobs (the artifact is most visible on an
   already-connected client watching the transitions live).

3. In another terminal, add a large batch of very fast jobs to the same
   isolated manager:

   ```
   export WR_CONFIG_DIR="$HOME/wr-devtest/config"
   perl -e 'for (1..10000){print "echo $_\n"}' \
     | "$HOME/wr-devtest/wr" add -i echo --cwd_matters --deployment development
   ```

   `--cwd_matters` makes the jobs run **faster** (each job runs directly in the
   cwd instead of wr creating and cleaning up a per-job working directory), so
   the burst completes quicker and the flicker window is denser and easier to
   catch.

   (Or add via whatever config the `start local` run reported. The point is: a
   single big batch of near-instant commands, added while the browser is
   watching.)

4. Watch the `echo` repgroup's bar (and the `+all+` aggregate) **during**
   completion. Look for the bar dipping/zeroing and repainting, and/or the total
   briefly exceeding 10000. Within a second or two of the burst draining, it
   settles to the correct `complete: 10000`.

5. Confirm the CLI agrees the run is genuinely fine throughout:

   ```
   "$HOME/wr-devtest/wr" status -i echo -o counts --deployment development
   ```

   The CLI is authoritative and shows the true state; the transient browser
   discrepancy is purely a rendering artifact.

6. Tear down:

   ```
   developers/wrdev.sh clean
   ```

### Making it easier to see

- A **slower / busier client** (throttled network in devtools, a loaded
  machine, or a larger N) widens the window in which intermediate states are
  rendered, making the flicker more obvious.
- Watching a **single repgroup** bar rather than the aggregate isolates the
  per-bar repaint behaviour.
- Comparing side-by-side against **v0.36.5** shows the same transient behaviour
  there (this is a restoration of that behaviour, not something new to
  `reliable2`).

## Pointers

- `DEVELOPERS.md` §2 rule 3 (never-drop delta feed) and rule 10
  (internal-only; the accepted web-count flicker exception).
- `.docs/bugfixes/260723-1.md` — the never-drop caster fix (removed the
  *permanent* freeze; left this *transient* flicker).
- `.docs/bugfixes/260625-5.md`, `260625-6.md`, `260625-7.md` — the original
  severe reports (now fixed) that motivated #533.
- `.docs/reliable2/` — the Option R revert of #533 and why the absolute counter
  was removed.
