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
"""

import os
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

# base is d1a4b6d7's AddMany/norc, with window figures like the instrumented
# 1g.4 runs' (rl-addmany-tail.txt): 18 reserves over 1ms per add, a 17ms
# longest hold.
BASE = {
    "ns/op": 560e6,
    "reserves/op": 15000,
    "reserve-p50-us": 6.5,
    "reserve-p99-us": 125,
    "reserve-max-us": 18000,
    "slow-reserves/op": 18,
    "max-qlock-us": 17000,
    "slow-reserve-p50-us": 1500,
    "B/op": 166000000,
    "allocs/op": 800000,
}
WINDOW_UNITS = ("slow-reserves/op", "max-qlock-us", "slow-reserve-p50-us")


def lines(figures, spread):
    """figures is one benchmark's {unit: value}, named NAME, or a list of
    (name, figures) pairs."""
    benches = figures if isinstance(figures, list) else [(NAME, figures)]
    out = []
    for name, figs in benches:
        for i, m in enumerate(spread):
            fields = [name, "2"]
            for unit, value in figs.items():
                s = COUNT_SPREAD[i] if unit == "slow-reserves/op" else m
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
        B_per_op=0.95, allocs_per_op=0.46)),
    # Each of these makes one judged figure worse and leaves the rest alone.
    "slower-op": (BASE, scaled(ns_per_op=1.25)),
    "more-slow-reserves": (BASE, scaled(slow_reserves_per_op=1.6)),
    "longer-hold": (BASE, scaled(max_qlock_us=1.4)),
    "longer-slow-waits": (BASE, scaled(slow_reserve_p50_us=1.5)),
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
    # Both report slow reserves, but head does not say how long they waited.
    "head-without-slow-wait": (BASE, without(scaled(ns_per_op=0.52), "slow-reserve-p50-us")),
    # No reserve over 1ms on base; some on head.
    "slow-reserves-from-none": (
        dict(BASE, **{"slow-reserves/op": 0, "slow-reserve-p50-us": 0}),
        dict(BASE, **{"slow-reserves/op": 2})),
    # Benchmarks only one tree ran, in the same sec/op table as one both ran
    # that got slower: benchstat leaves the other tree's cells empty.
    "one-tree-benchmarks": (
        [("BenchmarkSame-8", {"ns/op": 500}), ("BenchmarkOld-8", {"ns/op": 500})],
        [("BenchmarkSame-8", {"ns/op": 625}), ("BenchmarkNew-8", {"ns/op": 200})]),
}


def main():
    benchstat = sys.argv[1]
    for case, (base, head) in CASES.items():
        d = os.path.join(HERE, case)
        os.makedirs(d, exist_ok=True)
        with open(os.path.join(d, "base.txt"), "w") as f:
            f.write(lines(base, SPREAD))
        with open(os.path.join(d, "head.txt"), "w") as f:
            f.write(lines(head, SAME))
        csv = subprocess.run(
            [benchstat, "-format", "csv", "base=base.txt", "head=head.txt"],
            cwd=d, check=True, capture_output=True, text=True).stdout
        with open(os.path.join(d, "benchstat.csv"), "w") as f:
            f.write(csv)


if __name__ == "__main__":
    main()
