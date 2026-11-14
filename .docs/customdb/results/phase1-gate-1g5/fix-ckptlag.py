#!/usr/bin/env python3
"""1g.5 fix round 4: the fold lag bound each checkpoint shows: the time from
the creation of wal file N (the rotation) to the creation of N.ckpt.tmp,
which waits for the fold to reach the rotation. usage: ckptlag.py ckptwatch.tsv"""
import sys
wal, ckpt = {}, {}
for line in open(sys.argv[1]):
    f = line.rstrip("\n").split("\t")
    if len(f) < 3 or f[1] != "create":
        continue
    n = f[2].split(".")[0]
    if f[2].endswith(".wal"):
        wal[n] = float(f[0])
    elif f[2].endswith(".ckpt.tmp"):
        ckpt[n] = float(f[0])
gaps = [(n, ckpt[n] - wal[n]) for n in sorted(ckpt) if n in wal]
for n, g in gaps:
    print(f"{n}\t{g:.1f}")
if gaps:
    print(f"max\t{max(g for _, g in gaps):.1f}")
