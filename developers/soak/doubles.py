#!/usr/bin/env python3
"""Classify every double run (a non-put key run again after a run that exited 0) by where its
first run sat relative to the manager outages in restarts.tsv. usage: doubles.py <outdir> [tsvout]"""
import collections, glob, os, sys
d = sys.argv[1]
outs = []  # (down_ms, up_ms, kind)
lines = [l.rstrip('\n').split('\t') for l in open(os.path.join(d, 'restarts.tsv'))]
for i, f in enumerate(lines):
    if f[1] == 'stop':
        took = next(int(x[3:]) for x in f if x.startswith('ms='))
        kind = next(x[5:] for x in f if x.startswith('kind='))
        nxt = next((g for g in lines[i + 1:] if g[1] == 'start'), None)
        up = int(nxt[0]) * 1000 if nxt else 10**15
        outs.append((int(f[0]) * 1000 - took, up, kind))
runs = collections.defaultdict(dict)
for p in glob.glob(os.path.join(d, 'markers', '*.tsv')):
    for l in open(p):
        f = l.rstrip('\n').split('\t')
        if len(f) < 8 or not f[1].isdigit():
            continue
        r = runs[(f[4], f[5], f[2], f[3])]
        r['host'] = f[4]
        if f[0] == 'S': r['s'] = int(f[1])
        elif f[0] == 'E': r['e'] = int(f[1]); r['rc'] = f[7]
bykey = collections.defaultdict(list)
for k, r in runs.items():
    if 's' in r: bykey[(k[2], k[3])].append(r)
def where(r):
    for dn, up, kind in outs:
        if r['s'] < dn and r.get('e', 0) >= dn - 5000 and r['e'] <= up + 600000:
            ph = 'ended-while-down' if r['e'] < up else 'ended-after-restart'
            return f'{kind}@{dn // 1000} {ph}'
    return 'no-outage'
cat = collections.Counter(); rows = []
for key, v in bykey.items():
    if key[0] == 'put': continue
    v.sort(key=lambda r: r['s'])
    for i in range(1, len(v)):
        ok = [p for p in v[:i] if p.get('rc') == '0']
        if ok:
            p, c = ok[-1], v[i]
            w = where(p); cat[w] += 1
            rows.append((key[0], key[1], w, p['host'], p['s'], p['e'], c['host'], c['s'], c.get('e'), c.get('rc')))
# denominator: exit-0 runs straddling each outage
den = collections.Counter()
for key, v in bykey.items():
    for r in v:
        if r.get('rc') == '0' and key[0] != 'put':
            w = where(r)
            if w != 'no-outage': den[w] += 1
for w in sorted(set(cat) | set(den)):
    print(f'{cat[w]:6d} double runs of {den[w]:6d} exit-0 runs  {w}')
if len(sys.argv) > 2:
    with open(sys.argv[2], 'w') as o:
        for r in rows: o.write('\t'.join(map(str, r)) + '\n')
