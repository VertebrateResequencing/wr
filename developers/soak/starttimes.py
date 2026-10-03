#!/usr/bin/env python3
"""Recorded StartTime vs the real start. usage: starttimes.py <outdir> <dbstart.tsv> <unacked.tsv>

Joins each complete job in the DB (dbstart output) to its final run's marker S line by
(kind, id, host, pid) and prints the distribution of markerS - DB StartTime (a small
positive number when StartTime is the runner's start), for all jobs, for jobs whose
run started in the 10s before a crash, and for jobs whose start report was left unacked
(runnerlogs.py's unacked.tsv), with every unacked one listed.
"""
import collections, glob, os, sys

d, dbf, unf = sys.argv[1:4]
crashes = []
for l in open(os.path.join(d, 'restarts.tsv')):
    f = l.rstrip('\n').split('\t')
    if len(f) > 2 and f[1] == 'stop' and f[2] == 'rc=crash':
        crashes.append(int(f[0]) * 1000)
marks = {}
for p in glob.glob(os.path.join(d, 'markers', '*.tsv')):
    for l in open(p):
        f = l.rstrip('\n').split('\t')
        if len(f) < 8 or f[0] != 'S' or not f[1].isdigit():
            continue
        marks[(f[2], f[3], f[4].split('.')[0], f[5])] = int(f[1])
unacked = {}
try:
    for l in open(unf):
        f = l.rstrip('\n').split('\t')
        unacked[f[0]] = f
except FileNotFoundError:
    pass


def q(v):
    v = sorted(v)
    if not v:
        return 'n=0'
    return (f'n={len(v)} min={v[0]:.3f} p1={v[len(v) // 100]:.3f} p50={v[len(v) // 2]:.3f} '
            f'p99={v[len(v) * 99 // 100]:.3f} max={v[-1]:.3f}')


alld, precrash, und, nomark, zero = [], [], [], 0, 0
rows = []
for l in open(dbf):
    f = l.rstrip('\n').split('\t')
    if f[0] != 'jobscomplete':
        continue
    _, key, kind, jid, state, ec, att, host, pid, st, en = f
    st = int(st)
    if st == 0:
        zero += 1
        continue
    s = marks.get((kind, jid, host.split('.')[0], pid))
    if s is None:
        nomark += 1
        continue
    diff = (s - st) / 1000
    alld.append(diff)
    if any(0 <= c - s <= 10000 for c in crashes):
        precrash.append(diff)
    if key in unacked:
        und.append(diff)
        rows.append((key, kind, jid, host, pid, st, s, diff, unacked[key][2:]))
print(f'complete psimjob jobs with StartTime=0: {zero}; without a matching marker: {nomark}')
print('markerS - DB StartTime (s), all complete jobs:      ', q(alld))
print('  runs started <=10s before a crash:                ', q(precrash))
print('  runs whose start report was unacked (retried):    ', q(und))
print(f'  |diff| > 2s: all={sum(abs(x) > 2 for x in alld)} unacked={sum(abs(x) > 2 for x in und)}')
for r in rows[:40]:
    print('   ', *r)
