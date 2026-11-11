#!/usr/bin/env python3
"""Phase 1 gate item 1g.5: the "reservation wait" lines of a soak's manager
logs, each judged on its own (p99 <= 50ms and abandoned=0), and which of them
cover a checkpoint.

usage: waitlines.py <ckptwatch.tsv|-> <managerlog>...

Prints a TSV of every "reservation wait" line (time, n, p50, p99, max,
notDurable, abandoned, ok, ckpt), then a summary. A line covers a checkpoint
when its interval (the minute before it, its `interval` field being 1m) overlaps
a checkpoint's write window: from the create of its .ckpt.tmp to its rename
to .ckpt (ckptwatch.py's inotify events).
Also prints each checkpoint and the "db writer" writer=log maxSync of the lines
covering it.
"""
import re
import sys
from datetime import datetime

UNIT = {"ns": 1e-6, "µs": 1e-3, "us": 1e-3, "ms": 1.0, "s": 1e3, "m": 6e4}


def ms(v):
    if v == "0s" or v == "0":
        return 0.0
    total, rest = 0.0, v
    for m in re.finditer(r"([0-9.]+)(ns|µs|us|ms|s|m|h)", v):
        unit = m.group(2)
        total += float(m.group(1)) * (3.6e6 if unit == "h" else UNIT[unit])
    return total


def fields(line):
    return dict(re.findall(r'(\w+)=("[^"]*"|\S+)', line))


def epoch(f):
    return datetime.strptime(f["t"], "%Y-%m-%dT%H:%M:%S%z").timestamp()


def windows(path):
    """Each checkpoint's write window, from ckptwatch.py's inotify events: from
    the create of its .ckpt.tmp to the rename to .ckpt (write and fsync)."""
    if path == "-":
        return []
    out, tmp = [], {}
    for row in open(path):
        p = row.rstrip("\n").split("\t")
        if p[0] == "epoch":
            continue
        t, ev, name, size = float(p[0]), p[1], p[2], p[3]
        if ev == "create" and name.endswith(".ckpt.tmp"):
            tmp[name[: -len(".tmp")]] = t
        elif ev == "moved_to" and name.endswith(".ckpt"):
            out.append((tmp.pop(name, None), t, name, int(size)))
    return out


def main():
    ck = windows(sys.argv[1])
    waits, logs, seen = [], [], set()
    for path in sys.argv[2:]:
        for line in open(path, errors="replace"):
            if 'msg="reservation wait"' in line or ('msg="db writer"' in line and "writer=log" in line):
                if line in seen:
                    continue
                seen.add(line)
                f = fields(line)
                (waits if "reservation wait" in line else logs).append((epoch(f), f))
    waits.sort(key=lambda x: x[0])
    logs.sort(key=lambda x: x[0])

    def covering(t):
        names = []
        for start, end, name, size in ck:
            s = start if start is not None else end - 0.5
            if s <= t and end >= t - 60:
                names.append(f"{name}:{size}")
        return ",".join(names) or "-"

    print("time\tn\tp50\tp99\tmax\tnotDurable\tabandoned\tok\tckpt")
    bad = 0
    for t, f in waits:
        ok = ms(f["p99"]) <= 50 and f["abandoned"] == "0"
        bad += not ok
        print(f'{f["t"]}\t{f["n"]}\t{f["p50"]}\t{f["p99"]}\t{f["max"]}\t{f["notDurable"]}\t{f["abandoned"]}\t{"ok" if ok else "FAIL"}\t{covering(t)}')
    if waits:
        worst = max(waits, key=lambda x: ms(x[1]["p99"]))
        print(f'summary lines={len(waits)} fail={bad} worstP99={worst[1]["p99"]} at {worst[1]["t"]} '
              f'maxMax={max(waits, key=lambda x: ms(x[1]["max"]))[1]["max"]} '
              f'abandoned={sum(int(f["abandoned"]) for _, f in waits)} '
              f'notDurable={sum(int(f["notDurable"]) for _, f in waits)} '
              f'reservations={sum(int(f["n"]) for _, f in waits)}')
    else:
        print("summary lines=0")
    if ck:
        print("\ncheckpoint\tbytes\ttmpSeen\tdone\twindowS\tcoveringWaitP99/max\tcoveringLogMaxSync")
        for start, end, name, size in ck:
            s = start if start is not None else end - 0.5
            cw = [f'{f["p99"]}/{f["max"]}' for t, f in waits if s <= t and end >= t - 60]
            cl = [f["maxSync"] for t, f in logs if s <= t and end >= t - 60]
            span = f"{end - start:.3f}" if start is not None else "?"
            print(f'{name}\t{size}\t{start if start else "-"}\t{end}\t{span}\t{" ".join(cw) or "-"}\t{" ".join(cl) or "-"}')


main()
