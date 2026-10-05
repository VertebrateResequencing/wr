#!/usr/bin/env python3
"""How long a job's end takes to be acknowledged, and how many ends are handled per minute.

usage: latency.py <outdir> <runnerlogdir>

For every psimjob.sh run with an E marker (its real exit code, written just before the exit), the
runner's outcome line for that run ("command ran OK": the archive was acknowledged; "command [..]
exited with code N": the release or bury was acknowledged, "so it will be tried again" telling a
release from a bury) is found in the runner logs by (host, command pid, kind, id): the first
outcome line for that job and pid at or after one second before the end. The job's identity is needed
because hosts reuse pids, so (host, pid) alone paired some runs with a much later run's outcome.
Latency is the outcome line's time (whole seconds, rounded down) minus the E marker's (ms), so a
figure can read up to 1s low, and a negative one means under a second; compare rounds with this
same script. Runs whose end falls in a manager outage (from a stop's start to the next start in
restarts.tsv) or up to 60s before a stop are left out of the "steady" figures. Throughput: E
markers per minute by outcome, and the manager log's "archive fold" archives per minute.
"""
import collections
import datetime
import glob
import os
import re
import sys

d, rl = sys.argv[1:3]
TS = re.compile(r'^t=(\S+) ')
START = re.compile(r'msg="started executing" jobkey=\S+ cmd="\S*psimjob\.sh (\S+) (\S+) .*pid=(\d+)')
OK = re.compile(r'msg="command ran OK"')
EXIT = re.compile(r'msg="command \[\S*psimjob\.sh (\S+) (\S+) .*?\] exited with code (\d+)(, which may be a temporary issue, so it will be tried again)?')


def ts(s):
    return datetime.datetime.strptime(s, '%Y-%m-%dT%H:%M:%S%z').timestamp()


outs = []
lines = [l.rstrip('\n').split('\t') for l in open(os.path.join(d, 'restarts.tsv'))]
for i, f in enumerate(lines):
    if f[1] == 'stop':
        took = next((int(x[3:]) for x in f if x.startswith('ms=')), 0)
        nxt = next((g for g in lines[i + 1:] if g[1] == 'start' and 'rc=0' in g), None)
        outs.append((int(f[0]) - took / 1000 - 60, int(nxt[0]) if nxt else 1e12))


def steady(t):
    return not any(a <= t <= b for a, b in outs)


ack = collections.defaultdict(list)  # (host, pid, kind, id) -> [(ts, outcome)]
for p in glob.glob(os.path.join(rl, '*', '*')):
    host = os.path.basename(p).split('.')[1]
    cur = None
    for l in open(p, errors='replace'):
        m = START.search(l)
        if m:
            cur = (host, m.group(3), m.group(1), m.group(2))
            continue
        if cur is None:
            continue
        t = TS.match(l)
        if not t:
            continue
        if OK.search(l):
            ack[cur].append((ts(t.group(1)), 'archive')); cur = None
        else:
            m = EXIT.search(l)
            if m:
                ack[cur].append((ts(t.group(1)), 'release' if m.group(4) else 'bury')); cur = None

lat = collections.defaultdict(list)
bucket = collections.defaultdict(list)  # (outcome, 10-min slot) -> latencies
per_min = collections.defaultdict(collections.Counter)
unmatched = collections.Counter()
for p in glob.glob(os.path.join(d, 'markers', '*.tsv')):
    for l in open(p):
        f = l.rstrip('\n').split('\t')
        if len(f) < 8 or f[0] != 'E' or not f[1].isdigit():
            continue
        e = int(f[1]) / 1000
        rc = f[7]
        per_min[int(e // 60)]['rc0' if rc == '0' else ('rc' + rc)] += 1
        # outcome lines are whole seconds, so one can read up to 1s before its E marker
        a = min((x for x in ack.get((f[4], f[5], f[2], f[3]), ()) if x[0] >= e - 1), default=None)
        if a is None:
            unmatched[rc] += 1
            continue
        lat[(a[1], 'all')].append(a[0] - e)
        bucket[(a[1] if a[1] == 'archive' else 'rel/bury', int(e // 600))].append(a[0] - e)
        if steady(e):
            lat[(a[1], 'steady')].append(a[0] - e)


def q(v, p):
    return v[min(len(v) - 1, int(p * len(v)))]


print("end-to-acknowledgement latency (s; outcome line's whole second minus E marker ms: up to 1s low)")
print(f'{"outcome":10} {"set":7} {"n":>8} {"p50":>6} {"p90":>6} {"p99":>6} {"max":>8} {">5s":>6} {">30s":>6}')
for k in sorted(lat):
    v = sorted(lat[k])
    print(f'{k[0]:10} {k[1]:7} {len(v):8} {q(v, .5):6.2f} {q(v, .9):6.2f} {q(v, .99):6.2f} {v[-1]:8.1f} '
          f'{sum(x > 5 for x in v):6} {sum(x > 30 for x in v):6}')
print('E markers without an outcome line:', dict(unmatched))
print('per 10 minutes from the first end (outcome: n p50 p99 max)')
t0 = min(k[1] for k in bucket) if bucket else 0
for sl in sorted({k[1] for k in bucket}):
    row = []
    for o in ('archive', 'rel/bury'):
        v = sorted(bucket.get((o, sl), []))
        row.append(f'{o} n={len(v)} p50={q(v, .5):.1f} p99={q(v, .99):.1f} max={v[-1]:.0f}' if v else f'{o} n=0')
    print(f'  +{(sl - t0) * 10:3d}m  ' + '   '.join(row))
mins = sorted(per_min)
if mins:
    span = mins[-1] - mins[0] + 1
    for k in ('rc0', 'rc3'):
        v = sorted(per_min[m][k] for m in mins)
        print(f'{k} ends per minute over {span} min: mean={sum(v) / span:.1f} p50={q(v, .5)} p90={q(v, .9)} max={v[-1]}')
arch = []
for p in [os.path.join(d, 'manager.log')]:
    for l in open(p, errors='replace'):
        m = re.search(r'msg="archive fold" txs=(\d+) archives=(\d+)', l)
        if m:
            arch.append(int(m.group(2)))
if arch:
    a = sorted(arch)
    print(f'archive fold archives/min over {len(a)} min: mean={sum(a) / len(a):.0f} p50={q(a, .5)} p90={q(a, .9)} max={a[-1]} '
          f'(={a[-1] / 60:.1f}/s peak)')
