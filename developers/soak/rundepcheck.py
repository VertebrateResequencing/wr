#!/usr/bin/env python3
"""Verdicts for rundep.sh's #649 instances. usage: rundepcheck.py <outdir>

Per instance i: A.i's, D.i's and B.i's runs (S/E marks), when B.i's add was accepted, and the
nearest manager stop/start in restarts.tsv. OK means D.i ran exactly twice, both runs exited 0,
its second run started after B.i's run ended, and B.i's add landed while D.i's first run was
going (or, labelled late-add, after it ended, which is the complete-dependent path).
"""
import collections, glob, os, re, sys

d = sys.argv[1]
R = os.path.join(d, 'rundep')
if not os.path.isdir(R):
    print(f'no rundep run in {d}')
    sys.exit(0)
runs = collections.defaultdict(list)  # name -> [ {s,e,rc,host,pid} ]
open_ = {}
rows = []
for p in glob.glob(os.path.join(R, '*.tsv')):
    for l in open(p):
        f = l.rstrip('\n').split('\t')
        if len(f) < 6 or not f[2].isdigit():
            continue
        rows.append(f)
rows.sort(key=lambda f: int(f[2]))
for f in rows:
    k, name, t, host, pid = f[0], f[1], int(f[2]), f[3], f[4]
    if k == 'S':
        r = {'s': t, 'e': None, 'rc': None, 'host': host, 'pid': pid}
        runs[name].append(r)
        open_[(name, host, pid)] = r
    elif k == 'E':
        r = open_.get((name, host, pid))
        if r:
            r['e'] = t
            r['rc'] = f[6] if len(f) > 6 else '?'
added, status = {}, {}
for l in open(os.path.join(R, 'driver.log')):
    m = re.match(r'(\d+) add B\.(\d+): .*(Added|duplicates)', l)
    if m and m.group(2) not in added:
        added[m.group(2)] = int(m.group(1))
    m = re.match(r'\d+ instance (\d+): D status: (.*)', l)
    if m:
        status[m.group(1)] = m.group(2).strip()
events = []
for l in open(os.path.join(d, 'restarts.tsv')):
    f = l.rstrip('\n').split('\t')
    kind = next((x[5:] for x in f if x.startswith('kind=')), f[1])
    events.append((int(f[0]) * 1000, f[1] + ('-' + kind if f[1] == 'stop' else '')))


def near(t0, t1):
    return [f'{k}@{(t - t0) / 1000:+.0f}s' for t, k in events if t0 - 5000 <= t <= t1 + 5000]


insts = sorted({n.split('.')[1] for n in runs} | set(added), key=int)
c = collections.Counter()
for i in insts:
    D, B, A = runs.get(f'D.{i}', []), runs.get(f'B.{i}', []), runs.get(f'A.{i}', [])
    ba = added.get(i)
    verdict = []
    if not ba:
        verdict.append('B-not-added')
    good = [r for r in D if r['rc'] == '0']
    bend = max((r['e'] for r in B if r['rc'] == '0'), default=None)
    if ba and D:
        late = D[0]['e'] is not None and D[0]['e'] < ba
        if len(D) == 2 and len(good) == 2 and bend and D[1]['s'] > bend and len(B) == 1 and len(A) == 1:
            verdict.append('OK-late-add' if late else 'OK')
        else:
            verdict.append(f'CHECK D runs={len(D)} ok={len(good)} B runs={len(B)} A runs={len(A)}'
                           + (' D2-before-B-end' if len(D) > 1 and bend and D[1]['s'] <= bend else ''))
    elif ba:
        verdict.append('CHECK D never ran')
    # a run with no end mark that a clean stop (which kills and buries running jobs) or the
    # final stop could have ended
    for n, rr in (('A', A), ('D', D), ('B', B)):
        for r in rr:
            if r['e'] is None:
                ks = [k for t, k in events if t >= r['s'] and k.startswith('stop')]
                verdict.append(f'{n}-noend-then-{ks[0] if ks else "nostop"}')
    v = ' '.join(verdict)
    c[v.split(' ')[0]] += 1
    span0 = D[0]['s'] if D else (ba or 0)
    span1 = max([r['e'] or r['s'] for r in D + B] + [span0])
    print(f'instance {i}: {v}')
    for n, rr in (('A', A), ('D', D), ('B', B)):
        for r in rr:
            print(f'   {n} s={r["s"]} e={r["e"]} rc={r["rc"]} {r["host"]}/{r["pid"]}')
    print(f'   B added at {ba}; events {near(span0, span1)}; status {status.get(i, "-")}')
print('summary:', dict(c))
