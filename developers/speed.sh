#!/usr/bin/env bash
#
# speed.sh - the hot-path performance gate behind `make speed` and
# `make speed-full`. See ../DEVELOPERS.md ("Performance gate").
#
# It runs the Go benchmarks that cover add, reserve, touch, release, archive and
# status, and the farm-safe local wrdev.sh speed scenarios, for the tree under
# test (HEAD) and, unless SPEED_BASE=none, for a baseline (BASE) built in a
# temporary git worktree. BASE and HEAD rounds are interleaved so drift in host
# load hits both alike. benchstat compares them; a change worse than
# SPEED_THRESHOLD percent at p < SPEED_ALPHA fails the gate, as does any
# scenario that fails its own absolute thresholds on HEAD.
#
#   developers/speed.sh [quick|full]
#
# quick (make speed): the benchmarks, plus report-storm and
#   dep-granularity-check. About 10 minutes with a baseline.
# full (make speed-full): quick, plus add-storm, archive-rate and
#   archive-ceiling on copies of big production-shaped DBs. About an hour with
#   a baseline. Needs SPEED_BIG_DB, or WR_AS_DB / WR_ARCHRATE_DB / WR_AC_DB for
#   each scenario.
#
# Knobs (env or make variables):
#   SPEED_BASE=<ref>|<dir>|none  baseline; default the merge-base of HEAD and
#                           origin/develop. A directory is used as a prepared
#                           tree as-is.
#   SPEED_HEAD=<ref>|<dir>  tree under test; default this working tree,
#                           uncommitted changes included.
#   SPEED_COUNT=6           benchmark rounds per tree (benchstat needs >= 6 to
#                           call a 10% change significant).
#   SPEED_BENCHTIME=1s      -benchtime per benchmark per round.
#   SPEED_BENCH=<regex>     -bench regex for every package, in place of the
#                           default hot-path set.
#   SPEED_SCENARIOS="a b"   scenarios to run; "none" to skip them.
#   SPEED_SCENARIO_COUNT=1  scenario rounds per tree. One round is not enough
#                           for benchstat to call anything significant, so with
#                           one round scenarios only gate on their own
#                           thresholds; their numbers are still compared.
#   SPEED_THRESHOLD=10      percent worsening that fails the gate.
#   SPEED_ALPHA=0.05        significance needed for a worsening to fail it.
#   SPEED_DIR=<dir>         work dir (default ${TMPDIR:-/tmp}/wr-speed-$USER);
#                           each run writes <dir>/run-<epoch>/.
#   SPEED_KEEP=1            keep the baseline worktree, test binaries and
#                           fixtures.
#
# NOT part of make test, make race or CI. Developer tooling only.

set -euo pipefail

MODE="${1:-quick}"
case "$MODE" in (quick|full) ;; (*) echo "usage: $0 [quick|full]" >&2; exit 2 ;; esac

REPO="$(git -C "$(dirname "${BASH_SOURCE[0]}")" rev-parse --show-toplevel)"
WRDEV="$REPO/developers/wrdev.sh"
SPEED_DIR="${SPEED_DIR:-${TMPDIR:-/tmp}/wr-speed-$(id -un)}"
SPEED_COUNT="${SPEED_COUNT:-6}"
SPEED_BENCHTIME="${SPEED_BENCHTIME:-1s}"
SPEED_BENCH="${SPEED_BENCH:-}"
SPEED_SCENARIO_COUNT="${SPEED_SCENARIO_COUNT:-1}"
SPEED_THRESHOLD="${SPEED_THRESHOLD:-10}"
SPEED_ALPHA="${SPEED_ALPHA:-0.05}"
BENCHSTAT_VERSION="${BENCHSTAT_VERSION:-v0.0.0-20260929162123-406019bb8b68}"

QUICK_SCENARIOS="report-storm dep-granularity-check"
FULL_SCENARIOS="$QUICK_SCENARIOS add-storm archive-rate archive-ceiling"
if [ "$MODE" = full ]; then SPEED_SCENARIOS="${SPEED_SCENARIOS:-$FULL_SCENARIOS}"; fi
SPEED_SCENARIOS="${SPEED_SCENARIOS:-$QUICK_SCENARIOS}"
[ "$SPEED_SCENARIOS" = none ] && SPEED_SCENARIOS=""

die() { echo "speed: $*" >&2; exit 1; }
for s in $SPEED_SCENARIOS; do
  case " $FULL_SCENARIOS " in (*" $s "*) ;; (*) die "unknown scenario $s (the local ones are: $FULL_SCENARIOS)" ;; esac
done
for s in $SPEED_SCENARIOS; do
  case "$s" in
    add-storm) [ -n "${WR_AS_DB:-${SPEED_BIG_DB:-}}" ] || die "add-storm needs SPEED_BIG_DB or WR_AS_DB" ;;
    archive-rate) [ -n "${WR_ARCHRATE_DB:-${SPEED_BIG_DB:-}}" ] || die "archive-rate needs SPEED_BIG_DB or WR_ARCHRATE_DB" ;;
    archive-ceiling) [ -n "${WR_AC_DB:-${SPEED_BIG_DB:-}}" ] || die "archive-ceiling needs SPEED_BIG_DB or WR_AC_DB" ;;
  esac
done

# The benchmarks, per package. Top-level names are anchored; sub-benchmarks
# all run.
BENCH_PKGS=(jobqueue queue limiter)
declare -A BENCH_RE=(
  [jobqueue]='^Benchmark(AddJobs|UpdateJobState|ArchiveJobs|ArchiveSpacedArrivals|ReadyBacklogSnapshot|RepGroupStatusDetails|JobKey|ModifyLiveJobsReverseLookup|JobCleanup|JobCleanupDepth1|JobCleanupDepth8|AddDepGroupMember)$'
  [queue]='^BenchmarkQueueLifecycle$'
  [limiter]='^BenchmarkLimiter'
)

unset $(compgen -v | grep '^OS_') 2>/dev/null || true

RUN="$SPEED_DIR/run-$(date +%s)"
mkdir -p "$RUN"
WORKTREES=()
cleanup() {
  [ "${SPEED_KEEP:-0}" = 1 ] && return 0
  rm -f "$RUN"/*.test "$RUN"/wrdev-*/wr "$RUN"/wrdev-*/wsprobe "$RUN"/wrdev-*/depgran_fixture_db
  for w in "${WORKTREES[@]}"; do git -C "$REPO" worktree remove --force "$w" >/dev/null 2>&1 || true; done
}
trap cleanup EXIT

# --- benchstat, in a temp GOBIN with its own module cache --------------------
BENCHSTAT="$SPEED_DIR/tools/benchstat-$BENCHSTAT_VERSION"
if [ ! -x "$BENCHSTAT" ]; then
  echo "installing benchstat $BENCHSTAT_VERSION into $SPEED_DIR/tools"
  mkdir -p "$SPEED_DIR/tools"
  ( cd "$SPEED_DIR/tools" && GOBIN="$SPEED_DIR/tools/bin" GOMODCACHE="$SPEED_DIR/tools/mod" \
      GOFLAGS=-modcacherw go install "golang.org/x/perf/cmd/benchstat@$BENCHSTAT_VERSION" ) \
    || die "could not install benchstat"
  mv "$SPEED_DIR/tools/bin/benchstat" "$BENCHSTAT"
fi

# --- trees --------------------------------------------------------------------
# tree_dir <label> <ref|dir> sets TREE_DIR to a directory holding that tree.
tree_dir() {
  local label="$1" spec="$2"
  if [ -d "$spec" ]; then TREE_DIR=$(cd "$spec" && pwd); return 0; fi
  local sha; sha=$(git -C "$REPO" rev-parse --verify --quiet "$spec^{commit}") \
    || die "$label '$spec' is neither a directory nor a commit (git fetch origin?)"
  TREE_DIR="$RUN/tree-$label"
  git -C "$REPO" worktree add --detach "$TREE_DIR" "$sha" >/dev/null 2>&1 || die "could not make a worktree of $spec"
  WORKTREES+=("$TREE_DIR")
}

HEAD_DIR="$REPO"
if [ -n "${SPEED_HEAD:-}" ]; then tree_dir head "$SPEED_HEAD"; HEAD_DIR="$TREE_DIR"; fi
LABELS=(head)
if [ -z "${SPEED_BASE:-}" ]; then
  SPEED_BASE=$(git -C "$REPO" merge-base HEAD origin/develop 2>/dev/null) \
    || die "no SPEED_BASE given and no merge-base with origin/develop; set SPEED_BASE=<ref> or none"
fi
BASE_DIR=""
if [ "$SPEED_BASE" != none ]; then
  tree_dir base "$SPEED_BASE"; BASE_DIR="$TREE_DIR"
  LABELS=(base head)
fi
dir_of() { if [ "$1" = base ]; then echo "$BASE_DIR"; else echo "$HEAD_DIR"; fi; }
desc_of() { git -C "$(dir_of "$1")" describe --tags --always --dirty 2>/dev/null || echo "$(dir_of "$1")"; }

echo "speed ($MODE): results in $RUN"
for l in "${LABELS[@]}"; do echo "  $l: $(desc_of "$l") ($(dir_of "$l"))"; done

# --- benchmarks -----------------------------------------------------------------
for l in "${LABELS[@]}"; do
  for p in "${BENCH_PKGS[@]}"; do
    ( cd "$(dir_of "$l")" && CGO_ENABLED=0 go test -c -tags netgo -o "$RUN/$l.$p.test" "./$p/" ) \
      >"$RUN/$l.build.log" 2>&1 || die "could not build $l's $p benchmarks; see $RUN/$l.build.log"
  done
done

if [ "$SPEED_COUNT" -gt 0 ]; then
  for round in $(seq 1 "$SPEED_COUNT"); do
    for l in "${LABELS[@]}"; do
      echo "benchmarks: round $round/$SPEED_COUNT, $l"
      for p in "${BENCH_PKGS[@]}"; do
        [ -x "$RUN/$l.$p.test" ] || continue
        re="${BENCH_RE[$p]}"; [ -n "$SPEED_BENCH" ] && re="$SPEED_BENCH"
        # Benchmarks log to stderr, which would otherwise split result lines.
        ( cd "$(dir_of "$l")/$p" && "$RUN/$l.$p.test" -test.run '^$' -test.bench "$re" \
            -test.benchmem -test.benchtime "$SPEED_BENCHTIME" -test.count 1 ) \
          >>"$RUN/$l.bench" 2>>"$RUN/$l.bench.stderr" \
          || die "$l's $p benchmarks failed; see $RUN/$l.bench and $RUN/$l.bench.stderr"
      done
    done
  done
fi

# --- scenarios -------------------------------------------------------------------
# Each scenario's wrdev.sh run is turned into benchmark-format lines so benchstat
# compares scenarios the same way as benchmarks. Units ending /s, and
# throughput-factor, are better higher; everything else is better lower.

# dur_ms <go duration> prints it in ms (eg. 35ms, 1.2s, 800µs, <=50ms).
dur_ms() {
  echo "$1" | sed 's/^<=//' | awk '{
    v = $0; u = v; sub(/[a-zµ]+$/, "", v); sub(/^[0-9.]+/, "", u)
    m = (u == "s") ? 1000 : (u == "ms") ? 1 : (u == "µs" || u == "us") ? 0.001 : (u == "m") ? 60000 : -1
    if (m < 0 || v == "") exit 1; printf "%.3f\n", v * m }'
}
kv() { printf '%s\n' "$1" | grep -aoE "(^| )$2=[0-9.]+" | head -1 | cut -d= -f2; }

scenario_args() {
  case "$1" in
    report-storm) echo "${SPEED_RS_ARGS:-20000 500 2000 300}" ;;
    dep-granularity-check) echo "${SPEED_DG_ARGS:-}" ;;
    add-storm) echo "${SPEED_AS_ARGS:-}" ;;
    archive-rate) echo "${SPEED_AR_ARGS:-}" ;;
    archive-ceiling) echo "${SPEED_AC_ARGS:-}" ;;
  esac
}

# scenario_line <scenario> <output file> prints one benchmark line, or nothing
# when the output holds no measurement (eg. a tree too old to have the scenario).
scenario_line() {
  local s="$1" f="$2" sum
  case "$s" in
    report-storm)
      local secs jobs max
      secs=$(grep -aoE -- '--- (PASS|FAIL): TestReliable4ReportStorm \([0-9.]+s\)' "$f" | grep -oE '[0-9.]+s' | tr -d s)
      jobs=$(grep -aoE 'VERDICT: completed=[0-9]+' "$f" | grep -oE '[0-9]+$' | tail -1)
      max=$(grep -aoE 'archive latency max=[^ ]+' "$f" | tail -1 | cut -d= -f2)
      [ -n "$secs" ] && [ -n "$jobs" ] || return 0
      printf 'BenchmarkScenarioReportStorm 1 %s jobs/s %s max-archive-ms %s wall-sec\n' \
        "$(awk -v j="$jobs" -v s="$secs" 'BEGIN{printf "%.1f", j/s}')" "$(dur_ms "$max" || echo 0)" "$secs" ;;
    dep-granularity-check)
      sum=$(grep -aoE 'DEPGRAN-SUMMARY .*' "$f" | tail -1); [ -n "$sum" ] || return 0
      [ -n "$(kv "$sum" peakRssMb)" ] || return 0
      printf 'BenchmarkScenarioDepGranularity 1 %s peak-rss-MB %s recovery-sec %s add-sec\n' \
        "$(kv "$sum" peakRssMb)" "$(kv "$sum" recoverySec)" "$(kv "$sum" addSec)" ;;
    add-storm)
      sum=$(grep -aoE 'ADDSTORM-SUMMARY .*' "$f" | tail -1); [ -n "$sum" ] || return 0
      printf 'BenchmarkScenarioAddStorm 1 %s adds/s %s p50-ms %s p99-ms %s max-ms %s txns/add\n' \
        "$(kv "$sum" highRate)" "$(kv "$sum" highP50Ms)" "$(kv "$sum" highP99Ms)" \
        "$(kv "$sum" highMaxMs)" "$(kv "$sum" txnsPerAdd)" ;;
    archive-rate)
      sum=$(grep -aoE 'ARCHRATE-SUMMARY .*' "$f" | tail -1); [ -n "$sum" ] || return 0
      printf 'BenchmarkScenarioArchiveRate 1 %s archives/s %s mean-ms %s p99-ms %s max-ms\n' \
        "$(kv "$sum" rate)" "$(kv "$sum" meanMs)" "$(kv "$sum" p99Ms)" "$(kv "$sum" maxMs)" ;;
    archive-ceiling)
      sum=$(grep -aoE 'ARCHCEIL-SUMMARY .*' "$f" | tail -1); [ -n "$sum" ] || return 0
      printf 'BenchmarkScenarioArchiveCeiling 1 %s archives/s %s throughput-factor %s p99-ms %s max-ms\n' \
        "$(kv "$sum" highRate)" "$(kv "$sum" throughputFactor)" "$(kv "$sum" highP99Ms)" "$(kv "$sum" highMaxMs)" ;;
  esac
}

[ -n "${SPEED_BIG_DB:-}" ] && export WRDEV_PRISTINE_DB="$SPEED_BIG_DB"

FAILED=()
if [ -n "$SPEED_SCENARIOS" ]; then
  for l in "${LABELS[@]}"; do
    case " $SPEED_SCENARIOS " in (*" dep-granularity-check "*)
      WRDEV_REPO="$(dir_of "$l")" WRDEV_ROOT="$RUN/wrdev-$l" "$WRDEV" build >"$RUN/$l.wrbuild.log" 2>&1 \
        || die "could not build $l's wr binary; see $RUN/$l.wrbuild.log" ;;
    esac
  done
  for round in $(seq 1 "$SPEED_SCENARIO_COUNT"); do
    for s in $SPEED_SCENARIOS; do
      for l in "${LABELS[@]}"; do
        out="$RUN/$l.$s.$round.out"
        echo "scenario: $s round $round/$SPEED_SCENARIO_COUNT, $l"
        rc=0
        # shellcheck disable=SC2046
        WRDEV_REPO="$(dir_of "$l")" WRDEV_ROOT="$RUN/wrdev-$l" "$WRDEV" "$s" $(scenario_args "$s") >"$out" 2>&1 || rc=$?
        line=$(scenario_line "$s" "$out")
        if [ -n "$line" ]; then
          echo "$line" >>"$RUN/$l.scenarios"
          echo "  $line"
        else
          echo "  not measured; see $out"
        fi
        if [ "$rc" -ne 0 ] || [ -z "$line" ]; then
          echo "  $l FAILED $s (exit $rc); see $out"
          [ "$l" = head ] && FAILED+=("scenario $s failed on head (exit $rc, see $out)")
        fi
      done
    done
  done
fi

# --- compare ------------------------------------------------------------------------
for l in "${LABELS[@]}"; do
  cat "$RUN/$l.bench" "$RUN/$l.scenarios" 2>/dev/null >"$RUN/$l.txt" || true
done

echo
echo "================ benchstat ================"
if [ -n "$BASE_DIR" ]; then
  ( cd "$RUN" && "$BENCHSTAT" base=base.txt head=head.txt ) | tee "$RUN/benchstat.txt"
  ( cd "$RUN" && "$BENCHSTAT" -format csv base=base.txt head=head.txt ) >"$RUN/benchstat.csv" 2>/dev/null
  # A table starts with a ",<unit>,CI,<unit>,CI,vs base,P" header; rows are
  # name,base,CI,head,CI,delta,P. benchstat writes "~" when not significant at
  # its default alpha of 0.05, so also check P against SPEED_ALPHA.
  awk -F, -v thr="$SPEED_THRESHOLD" -v alpha="$SPEED_ALPHA" '
    $1 == "" && $7 == "P" { unit = $2; next }
    $1 == "" || $1 == "geomean" || unit == "" { next }
    $6 ~ /^[+-][0-9.]+%$/ {
      d = $6; sub(/%/, "", d); d += 0
      p = $7; sub(/^p=/, "", p); sub(/ .*/, "", p); p += 0
      higher = (unit ~ /\/s$/ || unit == "throughput-factor")
      worse = higher ? -d : d
      if (worse > thr && p < alpha) printf "REGRESSION %s %s: %s (p=%s)\n", $1, unit, $6, p
    }' "$RUN/benchstat.csv" >"$RUN/regressions.txt"
  while read -r r; do FAILED+=("$r"); done <"$RUN/regressions.txt"
else
  ( cd "$RUN" && "$BENCHSTAT" head.txt ) | tee "$RUN/benchstat.txt"
fi

echo
echo "================ verdict ================"
echo "results: $RUN (benchstat.txt, *.txt raw, *.out scenario logs)"
if [ "${#FAILED[@]}" -gt 0 ]; then
  printf '  %s\n' "${FAILED[@]}"
  echo "FAIL: make speed found ${#FAILED[@]} problem(s) (threshold ${SPEED_THRESHOLD}% at p<${SPEED_ALPHA})"
  exit 1
fi
echo "PASS: no benchmark or scenario worsened by more than ${SPEED_THRESHOLD}% at p<${SPEED_ALPHA}, and every scenario met its thresholds"
