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
"""Write the synthetic speedjudge cases' base.txt and head.txt.

usage: synthetic.py <benchstat>

Each case is a directory here holding 6 rounds of benchmark lines per tree,
shaped like BenchmarkAddManyUnderReserveLoad/norc's in the 1g.4 runs, and the
benchstat.csv that <benchstat> (the version speed.sh pins) makes of them.
Round-to-round spread comes from fixed multipliers, so the files never change.
For each synthetic case, and each recorded case in RECORDED, it also writes
head-tol.txt and benchstat-tol.csv as speed.sh's tolerant_counts and second
benchstat run do.
"""

import math
import os
import re
import subprocess
import sys

HERE = os.path.dirname(os.path.abspath(__file__))
NAME = "BenchmarkAddManyUnderReserveLoad/norc-8"
HEADER = (
    "goos: linux\ngoarch: amd64\n"
    "pkg: github.com/VertebrateResequencing/wr/jobqueue\n"
    "cpu: Intel Xeon Processor (Skylake, IBRS)\n"
)

# Spread of every figure, and a different spread for slow-reserve counts, as
# the 1g.4 runs had (about 4% on time, 20% on counts).
SPREAD = [1.00, 1.04, 0.97, 1.02, 0.99, 1.03]
COUNT_SPREAD = [1.00, 1.20, 0.85, 1.10, 0.90, 1.05]

# The thresholds, in ms, of the counts of reserves over them; the harness
# also counts reserves over 1.1 times each (the -tol units).
TAIL_MS = ("1.25", "1.5", "2", "2.5", "3", "4", "5", "6", "8", "10", "12", "16", "20", "24", "32", "40", "48",
           "64")
TAIL_UNITS = tuple(f"reserves-over-{t}ms{tol}/op" for t in TAIL_MS for tol in ("", "-tol"))


def tails(over):
    """The counts of a tree with over(t) reserves per op waiting over t ms."""
    figures = {}
    for t in TAIL_MS:
        figures[f"reserves-over-{t}ms/op"] = round(over(float(t)), 4)
        figures[f"reserves-over-{t}ms-tol/op"] = round(over(float(t) * 1.1), 4)
    return figures


def survival(points):
    """over(t) through points {ms: reserves per op over it}, log-interpolated
    between them and 0 past the last."""
    ms = sorted(points)

    def over(t):
        if t in points:
            return points[t]
        if t > ms[-1]:
            return 0
        lo = max(k for k in ms if k < t)
        hi = min(k for k in ms if k > t)
        if points[hi] == 0:
            return 0
        f = (math.log(t) - math.log(lo)) / (math.log(hi) - math.log(lo))
        return points[lo] * (points[hi] / points[lo]) ** f

    return over


def stalls(n, ms):
    """over(t) of n slow reserves per op of ms each."""
    return lambda t: n if ms > t else 0


# base is d1a4b6d7's AddMany/norc, with window figures like the instrumented
# 1g.4 runs' (rl-addmany-tail.txt): 18 reserves over 1ms per add, of about
# 1.5ms each, a few of them over the tail thresholds, and a 17ms longest hold.
BASE_OVER = survival({1: 18, 2: 3, 3: 1.6, 4: 0.8, 6: 0.4, 8: 0.25, 12: 0.15, 16: 0.1, 24: 0})
BASE = {
    "ns/op": 560e6,
    "reserves/op": 15000,
    "reserve-p50-us": 6.5,
    "reserve-p99-us": 125,
    "reserve-max-us": 18000,
    "slow-reserves/op": 18,
    "max-qlock-us": 17000,
    "slow-reserve-p50-us": 1500,
    "slow-reserve-wait-us/op": 27000,
    **tails(BASE_OVER),
    "B/op": 166000000,
    "allocs/op": 800000,
}
WINDOW_UNITS = ("slow-reserves/op", "max-qlock-us", "slow-reserve-p50-us", "slow-reserve-wait-us/op") + TAIL_UNITS
# The slow reserves' total wait and tail counts follow their count from round
# to round.
COUNT_UNITS = ("slow-reserves/op", "slow-reserve-wait-us/op") + TAIL_UNITS


def lines(figures, spread):
    """figures is one benchmark's {unit: value}, named NAME, or a list of
    (name, figures) pairs. A value that is a list gives each round's figure
    as it is, unspread."""
    benches = figures if isinstance(figures, list) else [(NAME, figures)]
    out = []
    for name, figs in benches:
        for i, m in enumerate(spread):
            fields = [name, "2"]
            for unit, value in figs.items():
                if isinstance(value, list):
                    fields += [f"{value[i]:.6g}", unit]
                    continue
                s = COUNT_SPREAD[i] if unit in COUNT_UNITS else m
                fields += [f"{value * s:.6g}", unit]
            out.append(" ".join(fields))
    return HEADER + "\n".join(out) + "\nPASS\n"


def scaled(**factors):
    figures = dict(BASE)
    for key, f in factors.items():
        unit = key.replace("_", "-").replace("-per-", "/")
        figures[unit] *= f
    return figures


def without(figures, *units):
    return {u: v for u, v in figures.items() if u not in units}


# Item D's KickUnderReserveLoad on d1a4b6d7 (its A/B, rounded): 35 reserves
# over 1ms per kick, most of them about 1.2ms waits for the queue's lock.
KICK_NAME = "BenchmarkKickUnderReserveLoad-8"
KICK_OVER = survival({1: 35, 1.5: 10.4, 2: 7.4, 3: 3.3, 4: 2.1, 6: 0.55, 8: 0.1, 12: 0.03, 16: 0})
KICK_BASE = {
    "ns/op": 116e6,
    "reserves/op": 4700,
    "reserve-p50-us": 10,
    "reserve-p99-us": 900,
    "reserve-max-us": 7000,
    "slow-reserves/op": 35,
    "max-qlock-us": 1500,
    "slow-reserve-p50-us": 1180,
    "slow-reserve-wait-us/op": 45000,
    **tails(KICK_OVER),
}


def faster_kick(n, ms):
    """A kick twice as fast as KICK_BASE's whose slow reserves are n per op
    of ms each, in stalls off the queue's lock, so its longest hold is
    unchanged."""
    return [(KICK_NAME, dict(KICK_BASE, **{
        "ns/op": 58e6, "reserves/op": 2350, "reserve-p99-us": 270, "reserve-max-us": ms * 1000,
        "slow-reserves/op": n, "slow-reserve-p50-us": ms * 1000, "slow-reserve-wait-us/op": n * ms * 1000,
        **tails(stalls(n, ms))}))]


def addmany_over(x):
    """over(t) of an add whose hold of the queue's lock delays 2 reserves per
    add, spread over 15 to 17ms, so 1 per add waits past 16ms, and whose other
    slow reserves are a 5ms, a 3.5ms, a 2.5ms and 13 of 1.3ms, as the
    reviewer's AddMany-shaped base had; every wait x times longer."""
    lo, hi = 15 * (1 + x), 17 * (1 + x)
    others = [w * (1 + x) for w in (5, 3.5, 2.5)] + [1.3 * (1 + x)] * 13
    return lambda t: 2 * min(1, max(0, (hi - t) / (hi - lo))) + sum(w > t for w in others)


ADDMANY_NEAR16 = dict(BASE, **{
    "slow-reserves/op": 18, "slow-reserve-wait-us/op": 60000, "reserve-max-us": 17000, **tails(addmany_over(0))})


def longer_addmany_hold(x):
    """ADDMANY_NEAR16 with every wait, the add's hold included, x times
    longer."""
    return dict(ADDMANY_NEAR16, **{
        "max-qlock-us": 17000 * (1 + x), "reserve-max-us": 17000 * (1 + x),
        "slow-reserve-wait-us/op": 60000 * (1 + x), **tails(addmany_over(x))})


# head's rounds take the base's spread in reverse, so a figure head leaves
# alone compares equal (p=1) with base's.
SAME = list(reversed(SPREAD))

CASES = {
    # The add runs twice as fast, so its window holds 40% fewer reserves and
    # the same slow ones make a 20% higher p99 and, the longest of fewer, a
    # 15% higher max. Nothing the rule judges is worse: it must pass.
    "window-shrank": (BASE, scaled(
        ns_per_op=0.52, reserves_per_op=0.6, reserve_p99_us=1.2,
        reserve_max_us=1.15, slow_reserves_per_op=1.05, max_qlock_us=0.97, slow_reserve_p50_us=0.97,
        slow_reserve_wait_us_per_op=1.02, B_per_op=0.95, allocs_per_op=0.46)),
    # Item D's shape: a kick twice as fast as base's that no longer holds the
    # queue's lock per item, so the ~1.1ms lock waits that made up nearly all
    # of base's slow reserves are gone. The few left are the rarer stalls base
    # also has, so their median is 40% higher while their total wait per op
    # falls 93%, as do the counts over every threshold. Nothing is worse: it
    # must pass, with the median informational.
    "short-waits-vanish": (BASE, dict(scaled(
        ns_per_op=0.5, slow_reserves_per_op=0.05, slow_reserve_p50_us=1.4,
        slow_reserve_wait_us_per_op=0.07, max_qlock_us=0.3, reserve_p99_us=0.25),
        **tails(lambda t: 0.05 * BASE_OVER(t)))),
    # Each of these makes one judged figure worse and leaves the rest alone.
    "slower-op": (BASE, scaled(ns_per_op=1.25)),
    "more-slow-reserves": (BASE, scaled(slow_reserves_per_op=1.6)),
    "longer-hold": (BASE, scaled(max_qlock_us=1.4)),
    # Each slow reserve waits 1.5 times as long (stalls of the same class got
    # longer), so their total wait per op does too. The tail counts are left
    # unchanged, so this shows the total alone catching longer waits at an
    # equal count.
    "longer-slow-waits": (BASE, scaled(slow_reserve_p50_us=1.5, slow_reserve_wait_us_per_op=1.5)),
    # The op got genuinely slower: 30% longer, holding the queue's lock more
    # often and for longer, so 1.5 times as many reserves wait past 1ms, each
    # 10% longer. Every delay figure must fail with the duration.
    "slower-op-longer-delays": (BASE, dict(scaled(
        ns_per_op=1.3, slow_reserves_per_op=1.5, max_qlock_us=1.3, slow_reserve_p50_us=1.1,
        slow_reserve_wait_us_per_op=1.65), **tails(lambda t: 1.5 * BASE_OVER(t / 1.1)))),
    # Review round 1 of the total's rule (bugfix 261119 item D): a kick twice
    # as fast whose slow reserves fall from 35 to 3 per op, but each of those
    # now waits about 12ms, in a stall off the queue's lock (another lock, GC
    # or scheduling), so the longest queue-lock hold is unchanged. Their total
    # falls 20%: a count 12 times smaller leaves room for each to wait up to
    # 12 times as long before the total rises. Reserves really are delayed
    # longer, so the counts over 4 to 10ms must fail it (3 per op over 2.5ms
    # and 3ms is fewer than base's 4.7 and 3.3).
    "fewer-much-longer-offlock": (
        [(KICK_NAME, KICK_BASE)],
        [(KICK_NAME, dict(KICK_BASE, **{
            "ns/op": 58e6, "reserves/op": 2350, "reserve-p99-us": 270, "reserve-max-us": 14000,
            "slow-reserves/op": 3, "slow-reserve-p50-us": 12000, "slow-reserve-wait-us/op": 36000,
            **tails(stalls(3, 12))}))]),
    # Review rounds 2 and 3 (bugfix 261119 item D) found these passed with
    # sparser thresholds: each lengthens base's waits to just under the next
    # threshold up. 2 stalls per kick of 7.9ms are 3.6 times base's waits over
    # 6ms.
    "offlock-7.9ms-x2": ([(KICK_NAME, KICK_BASE)], faster_kick(2, 7.9)),
    # 8 stalls per kick of 3.9ms are 2.4 times base's waits over 3ms.
    "offlock-3.9ms-x8": ([(KICK_NAME, KICK_BASE)], faster_kick(8, 3.9)),
    # 20 stalls per kick of 1.95ms are nearly twice base's 10.4 waits over
    # 1.5ms, though fewer than its 35 slow reserves.
    "offlock-1.95ms-x20": ([(KICK_NAME, KICK_BASE)], faster_kick(20, 1.95)),
    # An add twice as fast whose only slow reserve is one stall per add of
    # 25ms, off the queue's lock, where base's longest waits were its hold
    # of 15 to 17ms: the count over 20ms must fail it.
    "addmany-stall-25ms": (ADDMANY_NEAR16, dict(ADDMANY_NEAR16, **{
        "ns/op": 280e6, "reserves/op": 8000, "max-qlock-us": 14000, "reserve-max-us": 25000,
        "slow-reserves/op": 1, "slow-reserve-p50-us": 25000, "slow-reserve-wait-us/op": 25000,
        **tails(stalls(1, 25))})),
    # The add holds the queue's lock 5% or 9% longer, so every reserve it
    # delays waits that much longer: the ones of 15.2 to 16ms now wait past
    # 16ms, nearly doubling the count over 16ms, while the hold, the total
    # and every other figure stay within 10%. No reserve waits more than 10%
    # longer, so they must pass.
    "addmany-hold-5pc-longer": (ADDMANY_NEAR16, longer_addmany_hold(0.05)),
    "addmany-hold-9pc-longer": (ADDMANY_NEAR16, longer_addmany_hold(0.09)),
    # 15% longer is a real slowdown: it must fail.
    "addmany-hold-15pc-longer": (ADDMANY_NEAR16, longer_addmany_hold(0.15)),
    # A few rounds of each tree have one reserve over 8 or 16ms in 10 ops, as
    # item D's runs had: head's mean is twice base's, but at p > 0.05 that is
    # noise, so it passes.
    "sparse-tail-noise": (
        dict(BASE, **{"reserves-over-8ms/op": [0.1, 0, 0, 0.2, 0, 0],
                      "reserves-over-16ms/op": [0, 0, 0.1, 0, 0, 0]}),
        dict(BASE, **{"reserves-over-8ms-tol/op": [0.1, 0.2, 0, 0.1, 0, 0.2],
                      "reserves-over-16ms-tol/op": [0, 0.1, 0, 0, 0.1, 0]})),
    # The median slow wait rises 50% with their total and tail counts
    # unchanged: informational only, so it passes.
    "slow-median-only": (BASE, scaled(slow_reserve_p50_us=1.5)),
    # Every reserve waits a little longer, none past the slow-reserve
    # threshold: only the median reserve wait shows it.
    "slower-median": (BASE, scaled(reserve_p50_us=1.3)),
    # The rule judges the window figures; it leaves allocations to the raw
    # rule.
    "more-allocs": (BASE, scaled(allocs_per_op=1.3)),
    # Head from before the window figures, against a base that has them.
    "head-without-window": (BASE, without(BASE, *WINDOW_UNITS)),
    # Both report slow reserves, but base does not say how long the lock was
    # held.
    "base-without-hold": (without(BASE, "max-qlock-us"), BASE),
    # A base from before the window figures, against a head that has them:
    # judged by the raw figures, so the window-shrunk p99 fails as before.
    "base-without-window": (without(BASE, *WINDOW_UNITS), scaled(
        ns_per_op=0.52, reserves_per_op=0.6, reserve_p99_us=1.2)),
    # Both report slow reserves, but head does not say how long they waited
    # in all (as a base whose harness predates that figure would not).
    "head-without-slow-wait": (BASE, without(scaled(ns_per_op=0.52), "slow-reserve-wait-us/op")),
    # No reserve over 1ms on base; on head, 2 per op, 0.5 of them 2.5ms long.
    "slow-reserves-from-none": (
        dict(BASE, **{"slow-reserves/op": 0, "slow-reserve-p50-us": 0, "slow-reserve-wait-us/op": 0,
                      **{u: 0 for u in TAIL_UNITS}}),
        dict(BASE, **{"slow-reserves/op": 2, "slow-reserve-wait-us/op": 3000, **tails(stalls(0.5, 2.5))})),
    # Benchmarks only one tree ran, in the same sec/op table as one both ran
    # that got slower: benchstat leaves the other tree's cells empty.
    "one-tree-benchmarks": (
        [("BenchmarkSame-8", {"ns/op": 500}), ("BenchmarkOld-8", {"ns/op": 500})],
        [("BenchmarkSame-8", {"ns/op": 625}), ("BenchmarkNew-8", {"ns/op": 200})]),
}


# Recorded cases whose trees report the counts over 1.1 times each threshold.
RECORDED = ("itemD-ab", "itemD-red-r4")


def tolerant_counts(text):
    """text, a tree's benchmark lines, with each count of reserves over a
    threshold replaced by its count over 1.1 times it, under the plain name,
    as speed.sh's tolerant_counts does."""
    out = []
    for line in text.split("\n"):
        if line.startswith("Benchmark"):
            f = line.split()
            kept = f[:2]
            for value, unit in zip(f[2::2], f[3::2]):
                if re.fullmatch(r"reserves-over-[0-9.]+ms/op", unit):
                    continue
                kept += [value, re.sub(r"^(reserves-over-[0-9.]+ms)-tol/op$", r"\1/op", unit)]
            line = " ".join(kept)
        out.append(line)
    return "\n".join(out)


def compare(benchstat, d, head, out):
    """Write d/out, benchstat's CSV comparing d/base.txt with d/head."""
    csv = subprocess.run(
        [benchstat, "-format", "csv", "base=base.txt", f"head={head}"],
        cwd=d, check=True, capture_output=True, text=True).stdout
    with open(os.path.join(d, out), "w") as f:
        f.write(csv)


def main():
    benchstat = sys.argv[1]
    for case, (base, head) in CASES.items():
        d = os.path.join(HERE, case)
        os.makedirs(d, exist_ok=True)
        with open(os.path.join(d, "base.txt"), "w") as f:
            f.write(lines(base, SPREAD))
        with open(os.path.join(d, "head.txt"), "w") as f:
            f.write(lines(head, SAME))
        compare(benchstat, d, "head.txt", "benchstat.csv")
    for case in (*CASES, *RECORDED):
        d = os.path.join(HERE, case)
        with open(os.path.join(d, "head.txt")) as f:
            tolerant = tolerant_counts(f.read())
        with open(os.path.join(d, "head-tol.txt"), "w") as f:
            f.write(tolerant)
        compare(benchstat, d, "head-tol.txt", "benchstat-tol.csv")


if __name__ == "__main__":
    main()
