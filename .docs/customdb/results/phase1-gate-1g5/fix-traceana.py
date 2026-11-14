#!/usr/bin/env python3
"""1g.5 bugfix: analyse a diag trace (sync/fold/resv/add lines).
usage: traceana.py trace.tsv [bigbytes]
Per minute: reservation n, p99, max; fraction of wall time a fold tx was
running; wal syncs: count, p99 sync; slow-sync (>50ms) share that overlapped a
fold tx vs the fold duty cycle; slow reservations (>50ms) whose wait overlapped
a fold tx, a big wal batch (>= bigbytes, default 1MB)."""
import sys, bisect, collections
path = sys.argv[1]; big = int(sys.argv[2]) if len(sys.argv) > 2 else 1 << 20
folds, syncs, resv, hists = [], [], [], []
for line in open(path):
    p = line.rstrip('\n').split('\t')
    if p[0] == 'fold':
        a, b, e = int(p[1]), int(p[2]), int(p[3])
        folds.append((b, e, int(p[4]), int(p[5]), int(p[6]), int(p[8]), int(p[9]), int(p[11]), int(p[12])))
    elif p[0] == 'sync':
        s, w, y, n = int(p[1]), int(p[2]), int(p[3]), int(p[4])
        syncs.append((s, s + w + y, w, y, n))
    elif p[0] == 'hist':
        hists.append((int(p[1]), int(p[2]), int(p[3])))
    elif p[0] == 'resv':
        e, d = int(p[1]), int(p[2])
        resv.append((e - d, e, d))
folds.sort(); syncs.sort(); resv.sort(key=lambda r: r[1])
fstarts = [f[0] for f in folds]
def overlaps_fold(a, b):
    i = bisect.bisect_left(fstarts, b)
    j = i - 1
    while j >= 0:
        fs, fe = folds[j][0], folds[j][1]
        if fe > a: return True
        if fs < a - 30e9: break
        j -= 1
    return False
hists.sort()
def overlaps_hist(a, b):
    return any(hs < b and he > a for hs, he, _ in hists)
def hist_ms(a, b):
    return sum(max(0, min(b, he) - max(a, hs)) for hs, he, _ in hists)
sstarts = [s[0] for s in syncs]
def big_batch(a, b):
    i = bisect.bisect_left(sstarts, b); j = i - 1
    while j >= 0 and syncs[j][0] > a - 30e9:
        if syncs[j][1] > a and syncs[j][4] >= big: return True
        j -= 1
    return False
def q(xs, p):
    if not xs: return 0
    xs = sorted(xs); return xs[min(len(xs) - 1, int(len(xs) * p / 100))]
if not resv: sys.exit("no resv")
t0 = resv[0][1] // 60_000_000_000 * 60_000_000_000
mins = collections.defaultdict(lambda: {'r': [], 's': [], 'sy': [], 'f': 0, 'fn': 0, 'fw': 0, 'fsz': 0, 'free': 0, 'bigb': 0})
for a, e, d in resv: mins[(e - t0) // 60_000_000_000]['r'].append((a, e, d))
for s in syncs: mins[(s[0] - t0) // 60_000_000_000]['s'].append(s)
for f in folds:
    m = mins[(f[0] - t0) // 60_000_000_000]; m['f'] += f[1] - f[0]; m['fn'] += 1; m['fw'] += f[3]; m['fsz'] = f[8]; m['free'] = f[6]
print("min\tresN\tresP99ms\tresMaxms\tsyncN\tsyncP99ms\tsyncMaxms\tbigBatches\tbigMB\tfoldDuty\tfoldN\tfoldPagesW\tfreePages\tbolMB\tslowSync_inFold\tslowSync\tslowRes_inFold\tslowRes_bigBatch\tslowRes\thistDuty\tslowSync_inHist\tslowRes_inHist\thistGB")
import time
for k in sorted(mins):
    m = mins[k]
    rd = [r[2] / 1e6 for r in m['r']]
    sy = [s[3] / 1e6 for s in m['s']]
    bigs = [s for s in m['s'] if s[4] >= big]
    slow = [s for s in m['s'] if s[3] > 50e6]
    slowin = sum(1 for s in slow if overlaps_fold(s[0] + s[2], s[1]))
    sr = [r for r in m['r'] if r[2] > 50e6]
    srf = sum(1 for r in sr if overlaps_fold(r[0], r[1]))
    srb = sum(1 for r in sr if big_batch(r[0], r[1]))
    ms0 = t0 + k * 60_000_000_000
    hd = hist_ms(ms0, ms0 + 60_000_000_000) / 60e9
    shi = sum(1 for x in slow if overlaps_hist(x[0], x[1]))
    srh = sum(1 for r in sr if overlaps_hist(r[0], r[1]))
    hsz = max([h[2] for h in hists if h[0] < ms0 + 60e9] or [0]) / 1e9
    ts = time.strftime('%H:%M', time.gmtime((t0 + k * 60_000_000_000) / 1e9))
    print(f"{ts}\t{len(rd)}\t{q(rd,99):.1f}\t{max(rd or [0]):.1f}\t{len(sy)}\t{q(sy,99):.1f}\t{max(sy or [0]):.1f}\t{len(bigs)}\t{sum(s[4] for s in bigs)/1e6:.1f}\t{m['f']/60e9:.2f}\t{m['fn']}\t{m['fw']}\t{m['free']}\t{m['fsz']/1e6:.0f}\t{slowin}\t{len(slow)}\t{srf}\t{srb}\t{len(sr)}\t{hd:.2f}\t{shi}\t{srh}\t{hsz:.2f}")
