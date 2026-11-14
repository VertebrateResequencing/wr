#!/usr/bin/env python3
"""1g.5 bugfix: split a soak's "reservation wait" lines into those whose minute
(or the 30s before it) holds a portal burst start, and the rest.
usage: linesplit.py <prodsim events.tsv> <manager log>"""
import re, sys, datetime
UNIT = {"ns": 1e-6, "µs": 1e-3, "us": 1e-3, "ms": 1.0, "s": 1e3}
def ms(v):
    return sum(float(a) * UNIT[u] for a, u in re.findall(r"([0-9.]+)(ns|µs|us|ms|s)", v))
bursts = []
for line in open(sys.argv[1]):
    m = re.search(r"burst (\d{8}T\d{6})", line)
    if m:
        bursts.append(datetime.datetime.strptime(m.group(1), "%Y%m%dT%H%M%S").replace(tzinfo=datetime.timezone.utc).timestamp())
rows = []
for line in open(sys.argv[2], errors="replace"):
    if 'msg="reservation wait"' not in line:
        continue
    f = dict(re.findall(r'(\w+)=("[^"]*"|\S+)', line))
    t = datetime.datetime.strptime(f["t"], "%Y-%m-%dT%H:%M:%S%z").timestamp()
    b = any(t - 90 < s <= t for s in bursts)
    rows.append((f["t"][11:19], int(f["n"]), ms(f["p99"]), ms(f["max"]), int(f["abandoned"]), b))
for r in rows:
    print("%s\tn=%d\tp99=%.1f\tmax=%.1f\tabandoned=%d\t%s\t%s" % (r[0], r[1], r[2], r[3], r[4], "burst" if r[5] else "-", "FAIL" if r[2] > 50 or r[4] else "pass"))
for label, sel in (("burst", True), ("other", False)):
    for lo, hi in ((0, 2000), (2000, 4000), (4000, 99999)):
        xs = [r[2] for r in rows if r[5] == sel and lo <= r[1] < hi]
        if xs:
            print("summary %s n%d-%d: lines=%d p99 %.1f-%.1fms median %.1f fail=%d" % (label, lo, hi, len(xs), min(xs), max(xs), sorted(xs)[len(xs)//2], sum(1 for x in xs if x > 50)))
print("total lines=%d fail=%d abandoned=%d" % (len(rows), sum(1 for r in rows if r[2] > 50 or r[4]), sum(r[4] for r in rows)))
