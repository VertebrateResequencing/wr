#!/usr/bin/env bash
#
# wr-check-adds.sh - report what a manager knows about the commands in a
# "wr add -f" file.
#
# It answers the 3 questions that no single wr command answers:
#
#   1. which of these commands does the manager not know at all (never added,
#      or added and since removed)
#   2. which are known but have never started (added, but never run)
#   3. did some of them run in a different time window from the rest - the
#      signature of an earlier partial run under another identifier, which is
#      what makes "wr add" call them duplicates
#
# A job's identity is its cmd + cwd (only when it was added with --cwd_matters)
# + mounts + container options, and never its identifier (report group), so
# give this script the same file and the same identity options you gave
# "wr add". Commands are joined to jobs by command line, so a file that adds
# one command in two different cwds is reported as one command.
#
# Read-only: the only thing it runs is "wr status". It exits non-zero if any of
# those queries failed, since the report would then understate what the manager
# knows.
#
# NOT part of the shipped binary or test suite.

set -uo pipefail

WR="${WR:-wr}"
GAP=3600
CHUNK=500
TOP=20
OUTDIR=""
SEL=()

usage() {
  cat >&2 <<EOF
Usage: $0 [options] <the file you gave to wr add -f>

Identity options - give the same ones you gave "wr add", or the lookups will
not match:
  -c, --cwd DIR             --cwd used at add time. This also finds jobs added
                            with --cwd_matters, so there is no --cwd_matters
                            option here; and a file whose own lines set
                            cwd_matters needs no -c at all.
      --mounts STR          --mounts value used at add time
  -j, --mount_json STR      --mount_json value used at add time
      --with_docker IMG     --with_docker value used at add time
      --with_singularity IMG
      --container_mounts STR
      --container_image_user

Report options:
  -g, --gap SECONDS    gap in start times that separates one run batch from the
                       next (default $GAP, ie. 1 hour)
  -n, --chunk N        commands per status query (default $CHUNK)
  -t, --top N          example commands to print per section (default $TOP)
  -o, --outdir DIR     where to write the per-command detail
                       (default: a wr-check-<pid> dir in the current directory)
      --wr PATH        wr binary to use (default: wr on your PATH)

Pass --deployment, --timeout etc. through to wr via \$WR_STATUS_ARGS.
EOF
  exit 2
}

FILE=""
while [ $# -gt 0 ]; do
  case "$1" in
    -c|--cwd)                SEL+=(--cwd "$2"); shift 2 ;;
    --mounts)                SEL+=(--mounts "$2"); shift 2 ;;
    -j|--mount_json)         SEL+=(--mount_json "$2"); shift 2 ;;
    --with_docker)           SEL+=(--with_docker "$2"); shift 2 ;;
    --with_singularity)      SEL+=(--with_singularity "$2"); shift 2 ;;
    --container_mounts)      SEL+=(--container_mounts "$2"); shift 2 ;;
    --container_image_user)  SEL+=(--container_image_user); shift ;;
    -g|--gap)                GAP="$2"; shift 2 ;;
    -n|--chunk)              CHUNK="$2"; shift 2 ;;
    -t|--top)                TOP="$2"; shift 2 ;;
    -o|--outdir)             OUTDIR="$2"; shift 2 ;;
    --wr)                    WR="$2"; shift 2 ;;
    -h|--help)               usage ;;
    -*)                      echo "unknown option: $1" >&2; usage ;;
    *)                       [ -z "$FILE" ] || usage; FILE="$1"; shift ;;
  esac
done

[ -n "$FILE" ] || usage
[ -r "$FILE" ] || { echo "cannot read $FILE" >&2; exit 2; }
command -v jq >/dev/null || { echo "this needs jq" >&2; exit 2; }
for n in "$GAP" "$CHUNK" "$TOP"; do
  case "$n" in
    ''|*[!0-9]*) echo "-g, -n and -t take a whole number: got $n" >&2; usage ;;
  esac
done

read -r -a WR_ARGS <<< "${WR_STATUS_ARGS:-}"

OUTDIR="${OUTDIR:-wr-check-$$}"
mkdir -p "$OUTDIR" || exit 2
: > "$OUTDIR/found.jsonl"
: > "$OUTDIR/wr.err"

# 1. ask the manager about the file's commands, in chunks so that a file of
#    tens of thousands of commands does not become one huge request. The
#    chunks are slices of the user's own file: every line of a commands file
#    stands alone, and "wr status -f" takes the same file and the same identity
#    flags as "wr add -f".
split -l "$CHUNK" -d -a 5 "$FILE" "$OUTDIR/chunk." || exit 2
nchunks=$(find "$OUTDIR" -name 'chunk.*' | wc -l)
[ "$nchunks" -gt 0 ] || { echo "$FILE has no lines" >&2; exit 2; }
i=0
failed=0
for chunk in "$OUTDIR"/chunk.*; do
  i=$((i + 1))
  printf '\rquerying the manager: chunk %d/%d' "$i" "$nchunks" >&2
  # captured rather than piped straight into jq, so that the failure counted
  # here is wr's own: a pipeline under pipefail merges the two exit statuses,
  # and a consumer that stops early (grep -q) makes wr die of SIGPIPE.
  if ! json=$("$WR" status ${SEL[@]+"${SEL[@]}"} ${WR_ARGS[@]+"${WR_ARGS[@]}"} \
    -f "$chunk" -o json --limit 0 2>>"$OUTDIR/wr.err"); then
    failed=$((failed + 1))
  fi
  printf '%s' "$json" | jq -c '.[]?' >> "$OUTDIR/found.jsonl"
done
printf '\r%*s\r' 40 '' >&2
rm -f "$OUTDIR"/chunk.*

# 2. join the file's commands to what came back, group the ones that ran into
#    batches of start times, and write it all as one machine-readable report
#    that the renderers below lay out. Times are kept as the Unix nanoseconds
#    wr reports, alongside the local-time string, so that this is the only
#    place that formats a time.
jq -n --rawfile addfile "$FILE" --slurpfile found "$OUTDIR/found.jsonl" \
  --arg file "$FILE" --argjson gap "$GAP" '
  def ts: if . == null then "-"
          else (. / 1000000000 | floor | strflocaltime("%Y-%m-%d %H:%M:%S")) end;
  def tally: group_by(.) | map({key: .[0], value: length}) | from_entries;
  def dur:
    (. / 1000000000 | floor) as $s
    | if $s >= 86400 then "\($s / 86400 | floor)d \(($s % 86400) / 3600 | floor)h"
      elif $s >= 3600 then "\($s / 3600 | floor)h \(($s % 3600) / 60 | floor)m"
      elif $s >= 60 then "\($s / 60 | floor)m \($s % 60)s"
      else "\($s)s" end;

  ($gap * 1000000000) as $gapns
  # the same line rules "wr add" uses: a trailing \r is dropped, an empty first
  # tab-separated column skips the line, and otherwise the command is that
  # column, or the "cmd" of a line that is a JSON object.
  | [$addfile | rtrimstr("\n") | split("\n")[]
     | sub("\r$"; "") | split("\t")
     | select((.[0] // "") != "")
     | if length > 1 then .[0]
       elif (.[0] | startswith("{")) then (.[0] | fromjson | .cmd)
       else .[0] end] as $lines
  | ($lines | to_entries | group_by(.value) | map(min_by(.key)) | sort_by(.key)
     | map(.value)) as $uniq
  | ($found | INDEX(.Cmd)) as $byCmd
  | ([$byCmd[]] | map(select(.Started != null)) | sort_by(.Started)) as $ran
  | [foreach $ran[] as $j ({b: 0, prev: null};
      if .prev == null or ($j.Started - .prev) > $gapns
      then {b: (.b + 1), prev: $j.Started}
      else {b: .b, prev: $j.Started} end;
      {batch: .b, job: $j})] as $assigned
  | ($assigned | map({key: .job.Cmd, value: .batch}) | from_entries) as $batchOf
  | [$uniq[]
     | . as $cmd
     | $byCmd[$cmd] as $j
     | {cmd: $cmd,
        known: ($j != null),
        batch: $batchOf[$cmd],
        state: $j.State,
        identifier: $j.RepGroup,
        started: $j.Started,
        startedAt: ($j.Started | ts),
        ended: $j.Ended,
        endedAt: ($j.Ended | ts),
        exitcode: $j.Exitcode,
        host: $j.Host,
        attempts: $j.Attempts,
        key: $j.Key}] as $cmds
  | ($cmds | map(select(.known))) as $known
  | ($known | map(select(.started == null))) as $notStarted
  | ($assigned | group_by(.batch)
     | map((map(.job.Started) | min) as $first
           | (map(.job.Ended) | map(select(. != null)) | max) as $last
           | {n: .[0].batch,
              count: length,
              first: $first,
              firstAt: ($first | ts),
              lastAt: ($last | ts),
              states: (map(.job.State) | tally),
              identifiers: (map(.job.RepGroup) | tally)})) as $batches
  | {file: $file,
     gap: $gap,
     lines: ($lines | length),
     unique: ($uniq | length),
     known: ($known | length),
     missing: (($uniq | length) - ($known | length)),
     notStarted: ($notStarted | length),
     started: (($known | length) - ($notStarted | length)),
     states: ($known | map(.state) | tally),
     notStartedStates: ($notStarted | map(.state) | tally),
     firstGap: (if ($batches | length) > 1
                then ($batches[1].first - $batches[0].first | dur) else null end),
     batches: $batches,
     commands: $cmds}
' > "$OUTDIR/report.json" || { echo "failed to build the report" >&2; exit 1; }

# 3. per-command detail, for the sections below to point at rather than print.
jq -r '.commands[] | select(.known | not) | .cmd' \
  "$OUTDIR/report.json" > "$OUTDIR/never-added.txt"
jq -r '["batch", "state", "started", "ended", "identifier", "exitcode", "host",
        "attempts", "key", "cmd"],
       (.commands[] | [(.batch // "-"), (.state // "not known"), .startedAt,
                       .endedAt, (.identifier // "-"), (.exitcode // "-"),
                       (.host // "-"), (.attempts // "-"), (.key // "-"),
                       .cmd])
       | @tsv' "$OUTDIR/report.json" > "$OUTDIR/commands.tsv"

# 4. the report itself, in the order the questions get asked.
jq -r --argjson top "$TOP" --arg dir "$OUTDIR" '
  def padr($n): tostring | . + (if ($n - length) > 0 then " " * ($n - length) else "" end);
  def padl($n): tostring | (if ($n - length) > 0 then " " * ($n - length) else "" end) + .;
  def tallied: if length == 0 then "none"
               else (to_entries | sort_by(-.value) | map("\(.key) \(.value)") | join(", ")) end;
  def named: to_entries | sort_by(-.value) | map("\(.key)(\(.value))") | join(" ");
  def examples(rows; line):
    (rows | length) as $n
    | (rows[0:$top][] | "     " + line),
      (if $n > $top then "     ... and \($n - $top) more" else empty end);

  (.commands | map(select(.known | not))) as $missing
  | (.commands | map(select(.known and .started == null))) as $notRun
  | "",
    "=== \(.file): \(.lines) line(s), \(.unique) unique command(s) ===",
    "",
    "  \(.missing) not known to the manager, \(.notStarted) known but never " +
      "started, \(.started) started at least once",
    "  states of the \(.known) known command(s): \(.states | tallied)",
    "",
    (if ($missing | length) == 0
     then "1. NOT KNOWN TO THE MANAGER: none, it knows every command in the file."
     else "1. NOT KNOWN TO THE MANAGER (\(.missing)) - never added, or added and since removed:",
          examples($missing; .cmd),
          "     full list: \($dir)/never-added.txt"
     end),
    (if (.missing > 0) and (.missing == .unique)
     then "     !! NOTHING at all matched, which usually means these are not the options the",
          "        commands were added with, or this is not the manager they were added to. The",
          "        identity of a job is its cmd + cwd (only if --cwd_matters) + mounts +",
          "        container options, so pass the same -c/--mounts/--with_* you gave to wr add."
     else empty end),
    "",
    (if ($notRun | length) == 0
     then "2. KNOWN BUT NEVER STARTED: none, every command the manager knows has started."
     else "2. KNOWN BUT NEVER STARTED (\(.notStarted)) - added, but never run: " +
            "\(.notStartedStates | tallied)",
          examples($notRun; (.state | padr(12)) + (.identifier | padr(24)) + .cmd),
          "     full list: \($dir)/commands.tsv, in its state column"
     end),
    "",
    (if (.batches | length) == 0
     then "3. RUN BATCHES: none of the known commands have ever run."
     else "3. RUN BATCHES (a gap of more than \(.gap)s in start times starts a new batch):",
          "",
          "     " + ("#" | padr(4)) + ("first started" | padr(21)) +
            ("last ended" | padr(21)) + ("count" | padl(5)) + "  " +
            ("states" | padr(20)) + "identifiers",
          (.batches[] | "     " + (.n | padr(4)) + (.firstAt | padr(21)) + (.lastAt | padr(21)) +
            (.count | padl(5)) + "  " + (.states | tallied | padr(20)) + (.identifiers | named)),
          "",
          (if (.batches | length) > 1
           then "     ** THEY RAN IN \(.batches | length) SEPARATE TIME WINDOWS: the " +
                  "\(.batches[0].count) command(s) of batch 1",
                "        started \(.firstGap) before batch 2, under identifier(s) " +
                  "\(.batches[0].identifiers | keys | join(", ")). That is an",
                "        earlier partial run: those are the commands wr add calls duplicates."
           else "     ** all \(.batches[0].count) command(s) that ran did so in one time " +
                  "window, so none of them ran in an earlier batch."
           end)
     end),
    "",
    "per-command detail: \($dir)/commands.tsv (batch, state, started, ended, identifier,",
    "exitcode, host, attempts, key, cmd), as JSON in \($dir)/report.json, as the manager",
    "sent it in \($dir)/found.jsonl; the wr stderr, which includes its N/M cmds were not",
    "found notes, in \($dir)/wr.err",
    ""
' "$OUTDIR/report.json"

if [ "$failed" -gt 0 ]; then
  echo "WARNING: $failed/$nchunks status queries failed, so this report understates" \
    "what the manager knows; see $OUTDIR/wr.err" >&2
  exit 1
fi
