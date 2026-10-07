#!/usr/bin/env python3
# Copyright (c) 2026 Genome Research Ltd.
#
# Author: Sendu Bala <sb10@sanger.ac.uk>
#
# Permission is hereby granted, free of charge, to any person obtaining
# a copy of this software and associated documentation files (the
# "Software"), to deal in the Software without restriction, including
# without limitation the rights to use, copy, modify, merge, publish,
# distribute, sublicense, and/or sell copies of the Software, and to
# permit persons to whom the Software is furnished to do so, subject to
# the following conditions:
#
# The above copyright notice and this permission notice shall be included
# in all copies or substantial portions of the Software.
#
# THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND,
# EXPRESS OR IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF
# MERCHANTABILITY, FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT.
# IN NO EVENT SHALL THE AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY
# CLAIM, DAMAGES OR OTHER LIABILITY, WHETHER IN AN ACTION OF CONTRACT,
# TORT OR OTHERWISE, ARISING FROM, OUT OF OR IN CONNECTION WITH THE
# SOFTWARE OR THE USE OR OTHER DEALINGS IN THE SOFTWARE.
"""Count a crash soak's gate criteria: non-durable hand-outs, double runs and missing jobs.

usage: soakgate.py --source d1|warning <outdir> <runnerlogdir> <doubles.tsv> <dbstart.tsv>

Windows: an injected FUSE commit stall (stall.log's "<epoch> STALL START" to its "STALL END")
excuses what happens from its START to its END plus ReserveWriteWait (10s), or to START plus
STALL_SECS (180) plus 10s if it has no END, both ends inclusive, in whole seconds. A START
followed by another START ends as one with no END. With no stall.log there are no windows.

Non-durable hand-outs are read from <outdir>/manager.log only (manager.log.<epoch> files are
copies of it), split into one segment per manager process at each "wr manager ... started on
..., pid N" line; lines before the first start are ignored. With --source d1 each "reservation
handed out before it was recorded on disk" line counts 1, and each segment's total= values must
be exactly 1..max, each once; a D1 line with no total= still counts, and makes the totals GAP.
It never counts warnings, even in a log with no D1 line.
With --source warning (a tree without D1 lines) the rate-limited "reservation not yet recorded
on disk" warning counts 1, or its repeats= for a "(repeated)" line.
If the soak manager's directory (<outdir>/../.wr-prod_production) holds a rotated manager log,
manager.log is incomplete, so it prints ROTATED <files> and exits 1. If that directory does not
exist, it says so on stderr and cannot check.

Each doubles.tsv row (from doubles.py) is placed by its first run's "reserved a job" line, found
as anyway.py finds it (the run's marker host and pid, then the runner's "started executing ...
pid=" line); one that cannot be found counts as outside and is printed "unmapped". A double is
acknowledged if that runner log has a "command ran OK" line for its key between the first run's
start and the second run's start, both inclusive, in whole seconds (marker times truncated). When
the runner logs hold several reservations of the job by that pid, the one nearest the first run's
start is used.

Every psimjob (kind, id) with an S marker must be in dbstart.tsv, except a put or fofnput whose
last run's E status is not 0 or which has no E marker (prodsim may have removed it).

It prints:
  nondurable source=<d1|warning> inside=<n> outside=<n> runs=<runs> outsidePct=<x.xxxx|nan>
  totals <ok|GAP pid <pid> after <n>|n/a>
  doubles inside=<n> outside=<n> acknowledged=<n>
  missing ran=<n> absent=<n> excused=<n>
  peakRUN=<n>
then a "double" line per outside or acknowledged double, in doubles.tsv order, and an "absent"
line per absent job, sorted by kind then id. outsidePct is nan when runs is 0: such a soak
measured nothing, and nan compares false with any bound, so it fails a gate on outsidePct
instead of passing as 0.
"""
import argparse
import collections
import datetime
import glob
import os
import re
import sys

RESERVE_WRITE_WAIT_S = 10  # jobqueue's ReserveWriteWait with the default timeouts
STALL_SECS = 180  # stall.sh's default stall length
EXCUSABLE_KINDS = {'put', 'fofnput'}  # kinds prodsim may remove after a failed run

T = re.compile(r'^t=(\S+) ')
STARTED = re.compile(r'msg="wr manager \S* started on \S+, pid (\d+)"')
D1 = 'msg="reservation handed out before it was recorded on disk"'
TOTAL = re.compile(r' total=(\d+)')
WARN = 'msg="reservation not yet recorded on disk,'
REPEATS = re.compile(r' \(repeated\)" repeats=(\d+)')
ROTATED = re.compile(r'^log-(\d{4}-\d{2}-\d{2}T\d{2}-\d{2}-\d{2}\.\d{3})(?:\.gz)?$')
RES = re.compile(r'msg="reserved a job" key=([0-9a-f]{32}) cmd="\S*psimjob\.sh (\S+) (\S+) ')
PID = re.compile(r' pid=(\d+)')
OK = re.compile(r'msg="command ran OK" key=([0-9a-f]{32}) ')
RUN = re.compile(r'\bRUN=(\d+)')

Window = tuple[int, int]
Run = dict[str, object]


def epoch(iso: str) -> int:
    """Convert a log line's ISO 8601 t= value, with its zone offset, to whole epoch seconds."""
    return int(datetime.datetime.strptime(iso, '%Y-%m-%dT%H:%M:%S%z').timestamp())


def rotated_logs(outdir: str) -> list[str]:
    """Return the rotated manager logs (as cmd's rotatedManagerLogFiles finds them), sorted."""
    mgr = os.path.normpath(os.path.join(outdir, '..', '.wr-prod_production'))
    try:
        names = os.listdir(mgr)
    except FileNotFoundError:
        print(f'soakgate: no {mgr}, so rotated manager logs were not checked', file=sys.stderr)
        return []
    found = []
    for name in names:
        m = ROTATED.match(name)
        if not m or os.path.isdir(os.path.join(mgr, name)):
            continue
        try:
            datetime.datetime.strptime(m.group(1), '%Y-%m-%dT%H-%M-%S.%f')
        except ValueError:
            continue
        found.append(os.path.join(mgr, name))
    return sorted(found)


def stall_windows(outdir: str) -> list[Window]:
    """Return the injected stalls' windows from stall.log."""
    windows, start = [], None
    try:
        with open(os.path.join(outdir, 'stall.log')) as f:
            lines = f.read().splitlines()
    except FileNotFoundError:
        return []
    for line in lines:
        f = line.split()
        if len(f) < 3 or not f[0].isdigit() or f[1] != 'STALL':
            continue
        if f[2] == 'START':
            if start is not None:
                windows.append((start, start + STALL_SECS + RESERVE_WRITE_WAIT_S))
            start = int(f[0])
        elif f[2] == 'END' and start is not None:
            windows.append((start, int(f[0]) + RESERVE_WRITE_WAIT_S))
            start = None
    if start is not None:
        windows.append((start, start + STALL_SECS + RESERVE_WRITE_WAIT_S))
    return windows


def inside(t: int, windows: list[Window]) -> bool:
    """Report whether whole-second time t is in any window."""
    return any(lo <= t <= hi for lo, hi in windows)


def manager_segments(outdir: str, source: str) -> list[tuple[str, list[tuple[int, int]]]]:
    """Return per manager process (pid) its non-durable hand-outs as (time, total or count)."""
    segments: list[tuple[str, list[tuple[int, int]]]] = []
    with open(os.path.join(outdir, 'manager.log'), errors='replace') as log:
        for line in log:
            m = T.match(line)
            if not m:
                continue
            s = STARTED.search(line)
            if s:
                segments.append((s.group(1), []))
                continue
            if not segments:
                continue
            value = handout(line, source)
            if value is not None:
                segments[-1][1].append((epoch(m.group(1)), value))
    return segments


def handout(line: str, source: str) -> int | None:
    """Return a hand-out line's D1 total (0 if none), its warning count, or None for other lines."""
    if source == 'd1':
        if D1 not in line:
            return None
        m = TOTAL.search(line)
        return int(m.group(1)) if m else 0  # 0 is never a valid total: GAP
    if WARN not in line:
        return None
    r = REPEATS.search(line)
    return int(r.group(1)) if r else 1


def totals_verdict(segments: list[tuple[str, list[tuple[int, int]]]]) -> str:
    """Return "ok", or a GAP naming the first segment whose totals are not exactly 1..max."""
    for pid, handouts in segments:
        seen = collections.Counter(total for _, total in handouts)
        k = 0
        while seen[k + 1] == 1:
            k += 1
        if k != len(handouts):
            return f'GAP pid {pid} after {k}'
    return 'ok'


def first_line_runs(outdir: str) -> int:
    """Return the runs= value on the first line of markers-analysis.txt."""
    with open(os.path.join(outdir, 'markers-analysis.txt')) as f:
        m = re.search(r'\bruns=(\d+)', f.readline())
    if not m:
        sys.exit('soakgate: markers-analysis.txt has no runs= on its first line')
    return int(m.group(1))


def peak_run(outdir: str) -> int:
    """Return the largest RUN=<n> in lsf.tsv."""
    with open(os.path.join(outdir, 'lsf.tsv'), errors='replace') as f:
        return max((int(n) for line in f for n in RUN.findall(line)), default=0)


def marker_runs(outdir: str) -> dict[tuple[str, str, str, str], Run]:
    """Return every run in the markers, keyed by (host, pid, kind, id)."""
    runs: dict[tuple[str, str, str, str], Run] = {}
    for path in glob.glob(os.path.join(outdir, 'markers', '*.tsv')):
        with open(path, errors='replace') as markers:
            for line in markers:
                f = line.rstrip('\n').split('\t')
                if len(f) < 8 or not f[1].isdigit() or f[0] not in ('S', 'E'):
                    continue  # a line torn by a concurrent NFS append
                r = runs.setdefault((f[4], f[5], f[2], f[3]), {'s': None, 'e': None})
                if f[0] == 'S':
                    r['s'] = int(f[1])
                else:
                    r['e'] = f[7]
    return runs


def runner_segments(rl: str, targets: set[tuple[str, str]]) -> dict[tuple[str, str], list[Run]]:
    """Return each target (host, pid)'s reservations: job, key, time and that log's OK lines."""
    found: dict[tuple[str, str], list[Run]] = collections.defaultdict(list)
    hosts = {h for h, _ in targets}
    for path in glob.glob(os.path.join(rl, '*', '*')):
        parts = os.path.basename(path).split('.')
        if len(parts) < 3 or parts[1] not in hosts:
            continue
        segs, oks = scan_runner_log(path)
        for seg in segs:
            seg['oks'] = oks
            if (parts[1], seg['pid']) in targets:
                found[(parts[1], seg['pid'])].append(seg)
    return found


def scan_runner_log(path: str) -> tuple[list[Run], list[tuple[str, int]]]:
    """Return a runner log's reservations (each with its job's pid) and its "command ran OK" lines."""
    segs: list[Run] = []
    oks: list[tuple[str, int]] = []
    seg: Run | None = None
    with open(path, errors='replace') as log:
        for line in log:
            m = T.match(line)
            if not m:
                continue
            if 'msg="reserved a job"' in line:
                r = RES.search(line)
                seg = {'key': r.group(1), 'job': (r.group(2), r.group(3)), 'res': epoch(m.group(1)),
                       'pid': None} if r else None
                if seg is not None:
                    segs.append(seg)
            elif seg is not None and seg['pid'] is None and 'msg="started executing"' in line:
                p = PID.search(line)
                seg['pid'] = p.group(1) if p else None
            ok = OK.search(line) if 'msg="command ran OK"' in line else None
            if ok:
                oks.append((ok.group(1), epoch(m.group(1))))
    return segs, oks


def classify_doubles(doubles_tsv: str, runs: dict[tuple[str, str, str, str], Run], rl: str,
                     windows: list[Window]) -> list[tuple[str, str, str, int | None, bool]]:
    """Return per doubles.tsv row: kind, id, inside|outside|unmapped, reserved time, acknowledged."""
    with open(doubles_tsv) as f:
        rows = [line.rstrip('\n').split('\t') for line in f if line.strip()]
    pid_of = {(k[2], k[3], k[0], r['s']): k[1] for k, r in runs.items() if r['s'] is not None}
    firsts = [(f[0], f[1], f[3], int(f[4]), int(f[7])) for f in rows]
    targets = {(h, pid_of[(k, i, h, s1)]) for k, i, h, s1, _ in firsts if (k, i, h, s1) in pid_of}
    segs = runner_segments(rl, targets)
    out = []
    for kind, jid, host, s1, s2 in firsts:
        pid = pid_of.get((kind, jid, host, s1))
        cands = [s for s in segs.get((host, pid), ()) if s['job'] == (kind, jid)]
        if not cands:
            out.append((kind, jid, 'unmapped', None, False))
            continue
        seg = min(cands, key=lambda s: abs(s['res'] * 1000 - s1))
        acked = any(key == seg['key'] and s1 // 1000 <= t <= s2 // 1000 for key, t in seg['oks'])
        where = 'inside' if inside(seg['res'], windows) else 'outside'
        out.append((kind, jid, where, seg['res'], acked))
    return out


def missing_jobs(runs: dict[tuple[str, str, str, str], Run],
                 dbstart_tsv: str) -> tuple[int, int, list[tuple[str, str, str]]]:
    """Return how many jobs ran, how many absent ones are excused, and the other absent ones."""
    in_db = set()
    with open(dbstart_tsv, errors='replace') as db:
        for line in db:
            f = line.rstrip('\n').split('\t')
            if len(f) >= 4:
                in_db.add((f[2], f[3]))
    last: dict[tuple[str, str], Run] = {}
    for (_, _, kind, jid), r in runs.items():
        if r['s'] is not None and ((kind, jid) not in last or r['s'] > last[(kind, jid)]['s']):
            last[(kind, jid)] = r
    excused, absent = 0, []
    for (kind, jid), r in sorted(last.items()):
        if (kind, jid) in in_db:
            continue
        if kind in EXCUSABLE_KINDS and r['e'] != '0':
            excused += 1
        else:
            absent.append((kind, jid, r['e'] if r['e'] is not None else 'none'))
    return len(last), excused, absent


def main() -> None:
    """Print the gate's counts for one soak's output."""
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument('--source', required=True, choices=('d1', 'warning'))
    for name in ('outdir', 'runnerlogdir', 'doubles', 'dbstart'):
        ap.add_argument(name)
    a = ap.parse_args()

    rotated = rotated_logs(a.outdir)
    if rotated:
        print('ROTATED ' + ' '.join(rotated))
        sys.exit(1)

    windows = stall_windows(a.outdir)
    segments = manager_segments(a.outdir, a.source)
    handouts = [h for _, hs in segments for h in hs]
    n_in = sum(n if a.source == 'warning' else 1 for t, n in handouts if inside(t, windows))
    n_out = sum(n if a.source == 'warning' else 1 for t, n in handouts if not inside(t, windows))
    runs_total = first_line_runs(a.outdir)
    pct = f'{100 * n_out / runs_total:.4f}' if runs_total else 'nan'
    print(f'nondurable source={a.source} inside={n_in} outside={n_out} runs={runs_total} outsidePct={pct}')
    print('totals ' + (totals_verdict(segments) if a.source == 'd1' else 'n/a'))

    runs = marker_runs(a.outdir)
    doubles = classify_doubles(a.doubles, runs, a.runnerlogdir, windows)
    d_in = sum(1 for d in doubles if d[2] == 'inside')
    print(f'doubles inside={d_in} outside={len(doubles) - d_in} acknowledged={sum(1 for d in doubles if d[4])}')
    ran, excused, absent = missing_jobs(runs, a.dbstart)
    print(f'missing ran={ran} absent={len(absent)} excused={excused}')
    print(f'peakRUN={peak_run(a.outdir)}')
    for kind, jid, where, res, acked in doubles:
        if where != 'inside' or acked:
            print(f'double {kind} {jid} {where} reserved={"-" if res is None else res} '
                  f'acknowledged={"yes" if acked else "no"}')
    for kind, jid, status in absent:
        print(f'absent {kind} {jid} last={status}')


if __name__ == '__main__':
    main()
