#!/usr/bin/env python3
"""Run-marker analysis (developer tooling, not part of wr).

usage: markers.py <outdir>

Reads <outdir>/markers/*.tsv (psimjob.sh S/E/D lines), restarts.tsv and
stall.log, and prints: runs per kind; runs with no end marker; runs ended by a
signal; DOUBLE RUNS (a key run again after a run that exited 0: the defect);
true overlaps (two runs of one key at once); re-runs after a killed run, by the
nearest restart or stall; and the --cmd_deps ordering of ctrdep jobs.

E status is the script's real exit code or sig<NAME> (fixed psimjob.sh), so an
rc=0 end marker really is an exit 0.
"""
import collections
import glob
import os
import sys

d = sys.argv[1]
WINDOW_MS = 10 * 60 * 1000  # how near a restart/stall a run must be to be attributed to it

events = []  # (ms, label)
for line in open(os.path.join(d, 'restarts.tsv')):
    f = line.rstrip('\n').split('\t')
    if len(f) > 1 and f[1] == 'stop':
        kind = next((x.split('=', 1)[1] for x in f if x.startswith('kind=')), '?')
        took = next((int(x.split('=', 1)[1]) for x in f if x.startswith('ms=')), 0)
        events.append((int(f[0]) * 1000 - took, 'stop-' + kind))  # when the stop began
    elif len(f) > 1 and f[1] == 'start':
        events.append((int(f[0]) * 1000, 'start'))
try:
    for line in open(os.path.join(d, 'stall.log')):
        ts, rest = line.split(' ', 1)
        if rest.startswith('STALL START'):
            events.append((int(ts) * 1000, 'stall-start'))
        elif rest.startswith('STALL END'):
            events.append((int(ts) * 1000, 'stall-end'))
except FileNotFoundError:
    pass
events.sort()


def near(ms):
    best = None
    for t, lab in events:
        if lab == 'start':
            continue
        if abs(t - ms) <= WINDOW_MS and (best is None or abs(t - ms) < abs(best[0] - ms)):
            best = (t, lab)
    return 'none' if best is None else f'{best[1]}@{(ms - best[0]) / 1000:+.0f}s'


runs = {}  # (host, pid, kind, id) -> run
deps = []
for path in glob.glob(os.path.join(d, 'markers', '*.tsv')):
    for line in open(path):
        f = line.rstrip('\n').split('\t')
        if len(f) < 8:
            continue
        typ, ms, kind, jid, host, pid, lsf, val = f[:8]
        if not ms.isdigit():
            continue  # a line torn by a concurrent NFS append
        k = (host, pid, kind, jid)
        r = runs.setdefault(k, {'kind': kind, 'id': jid, 'host': host, 'pid': pid, 'lsf': lsf,
                                's': None, 'e': None, 'rc': None, 'd': None})
        if typ == 'S':
            r['s'] = int(ms)
        elif typ == 'E':
            r['e'] = int(ms)
            r['rc'] = val
        elif typ == 'D':
            r['d'] = val

bykey = collections.defaultdict(list)
for r in runs.values():
    if r['s'] is not None:
        bykey[(r['kind'], r['id'])].append(r)
for v in bykey.values():
    v.sort(key=lambda r: r['s'])

nruns = sum(len(v) for v in bykey.values())
kinds = collections.Counter(r['kind'] for v in bykey.values() for r in v)
print(f'runs={nruns} keys={len(bykey)} by kind: {dict(kinds)}')
noend = [r for v in bykey.values() for r in v if r['e'] is None]
sig = [r for v in bykey.values() for r in v if r['rc'] and r['rc'].startswith('sig')]
print(f'runs with no end marker (SIGKILLed, lost, or still running at the end): {len(noend)}')
print(f'  by nearest event: {dict(collections.Counter(near(r["s"]).split("@")[0] for r in noend))}')
print(f'runs ended by a signal: {len(sig)} {dict(collections.Counter(r["rc"] for r in sig))}')
print(f'  by nearest event: {dict(collections.Counter(near(r["s"]).split("@")[0] for r in sig))}')
rcs = collections.Counter(r['rc'] for v in bykey.values() for r in v if r['rc'] and not r['rc'].startswith('sig'))
print(f'exit codes: {dict(rcs)}')


def fmt(r):
    return (f"{r['host']}/{r['pid']} s={r['s']} e={r['e']} rc={r['rc']}")


# put jobs are re-added by the ibackup server after they complete, and stat jobs
# by a walk that runs again, so their re-runs are expected; everything else must
# never run again after an exit 0
reruns_ok_kinds = {'put'}
double, overlap, after_kill = [], [], []
for key, v in bykey.items():
    for i in range(1, len(v)):
        prev, cur = v[:i], v[i]
        for p in prev:
            if p['e'] is not None and p['e'] > cur['s']:
                overlap.append((key, p, cur))
        last = v[i - 1]
        if key[0] in reruns_ok_kinds:
            continue
        if any(p['rc'] == '0' for p in prev):
            double.append((key, [p for p in prev if p['rc'] == '0'][-1], cur))
        else:
            after_kill.append((key, last, cur))

print(f'\nDOUBLE RUNS (non-put key run again after a run that exited 0): {len(double)}')
for key, p, c in double[:40]:
    print(f'  {key[0]} {key[1]}: run1 {fmt(p)} | run2 {fmt(c)} | run1 near {near(p["e"])} run2 near {near(c["s"])}')
print(f'\ntrue overlaps (a second run began before an earlier one ended): {len(overlap)}')
for key, p, c in overlap[:20]:
    print(f'  {key[0]} {key[1]}: run1 {fmt(p)} | run2 {fmt(c)}')
print(f'\nre-runs after a run that did not exit 0 (killed, failed or lost; retries/wr retry are expected): '
      f'{len(after_kill)}')
cat = collections.Counter()
for key, p, c in after_kill:
    why = 'noend' if p['e'] is None else ('sig' if p['rc'].startswith('sig') else 'rc' + p['rc'])
    cat[(why, near(p['s']).split('@')[0])] += 1
for (why, ev), n in sorted(cat.items(), key=lambda x: -x[1]):
    print(f'  {n:6d}  previous run {why:6s} near {ev}')

ctr = [r for v in bykey.values() for r in v if r['kind'] == 'ctrdep']
missing = [r for r in ctr if r['d'] in (None, 'MISSING')]
early, lags = [], []
for r in ctr:
    try:
        dep = int(r['d'])
    except (TypeError, ValueError):
        continue
    lag = r['s'] / 1000 - dep
    lags.append(lag)
    if lag < -1:  # the dependency's own clock is second-granular
        early.append(r)
lags.sort()
print(f'\nctrdep: runs={len(ctr)} dependency-done-file missing at start={len(missing)} '
      f'started before dependency finished={len(early)}')
if lags:
    print(f'  start minus dependency end (s): min={lags[0]:.1f} p50={lags[len(lags) // 2]:.1f} max={lags[-1]:.1f}')
