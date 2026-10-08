#!/bin/bash
# Runs soakgate.py on every case directory here with that case's args file
# (paths relative to the case directory) and diffs its output against the
# case's expected.txt. A case whose expected.txt starts with "ROTATED " must
# also exit 1, every other case 0. Exits non-zero if any case differs.
# usage: run.sh
#
# Every rule in soakgate.py is pinned by a case that fails under its mutant,
# except these mutants, which give the same output on all input the real
# producers write:
# - dropping ^ from T or ROTATED: re.match already anchors.
# - STARTED without its closing quote, TOTAL or PID without the leading
#   space, REPEATS without '(repeated)"', RUN= or runs= without \b: no other
#   field in those lines ends in pid/total/repeats/RUN/runs (D1 logs key and
#   total; only the repeated warning has repeats=; bjobs state names;
#   markers.py's first line).
# - RES/OK taking any key or path, or no space after the id, and KILLED taking
#   any key: keys are 32 hex (and KILLED's must also equal its reservation's),
#   and prodsim's psimjob.sh commands are absolute and take more arguments.
# - stall.log guards (field count, digit epoch, the STALL word, END with no
#   START): stall.sh, the only writer, prefixes date +%s and writes END only
#   after its own START.
# - no t= guard in manager or runner logs, or a start line falling through
#   to handout(): wr log lines all start t=, and a start line is no hand-out.
# - first_line_runs reading the whole file: markers.py prints runs= first.
# - a markers glob of '*' for '*.tsv': psimjob.sh writes only <host>.tsv.
# - runner logs without the host, target or name-length filters, or globbed
#   recursively: cmd/runner.go names them <dir>/<yy.mm.dd>/<time>.<host>.<pid>,
#   and segments are looked up by (host, pid) anyway.
# - the "command ran OK" and "killed by user request" substring pre-checks:
#   OK's and KILLED's regexes require them.
# - pid_of keeping runs with no S: their None start never equals an s1.
# - dbstart rows needing 5 fields: dbstart writes 11, or 1 (schemaVersion=).
# KILLED's prefix, with or without msg=", and its closing quote are not in that
# list: case 31 pins them with M.1 (a signal error leads the kill reason, as
# "sigErr; myerr") and H.1 (text after it). wr also wraps the reason when a kill
# lands before the command started, but that run has no S marker, so it is
# never a doubles row's first run.
set -u

here=$(cd "$(dirname "$0")" && pwd)
gate="$here/../../soakgate.py"
failed=0
ran=0

for case in "$here"/*/; do
    case=${case%/}
    name=$(basename "$case")
    read -r -a args < "$case/args"
    got=$(cd "$case" && python3 "$gate" "${args[@]}")
    status=$?
    want_status=0
    if head -n 1 "$case/expected.txt" | grep -q '^ROTATED '; then
        want_status=1
    fi
    ran=$((ran + 1))
    if ! difference=$(diff -u "$case/expected.txt" <(printf '%s\n' "$got")); then
        echo "FAIL $name: output differs from expected.txt"
        echo "$difference"
        failed=$((failed + 1))
    elif [ "$status" -ne "$want_status" ]; then
        echo "FAIL $name: exit status $status, want $want_status"
        failed=$((failed + 1))
    else
        echo "ok   $name"
    fi
done

if [ "$ran" -eq 0 ]; then
    echo "FAIL: no case directories under $here"
    exit 1
fi
if [ "$failed" -ne 0 ]; then
    echo "FAIL: $failed of $ran soakgate cases"
    exit 1
fi
echo "PASS: $ran soakgate cases"
