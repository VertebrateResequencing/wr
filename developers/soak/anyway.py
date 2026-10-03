#!/usr/bin/env python3
"""Which double runs had their first run handed out before its reservation was on disk.

usage: anyway.py <outdir> <runnerlogdir> <doubles.tsv> <managerlog>

The manager warns "reservation not yet recorded on disk, handing the job out anyway; a manager
crash before the job's start is recorded may run it twice" (key=, time) when a reservation's
durable write takes over its wait (a commit stall). A crash before that job's start is recorded
loses the run, and the job running again is the documented cost. This maps each warned key to
its psimjob (kind, id) through the runner logs' "reserved a job" lines, then splits doubles.tsv
(doubles.py's output) by whether the first run's reservation was warned about (within 60s of the
first run's start), and by the runner's outcome line for that first run.
"""
import collections, glob, os, re, sys, datetime

d, rl, dbl, mlog = sys.argv[1:5]
RES = re.compile(r'^t=(\S+) .*msg="reserved a job" key=([0-9a-f]{32}) cmd="\S*psimjob\.sh (\S+) (\S+) ')
def ts(s):
    return int(datetime.datetime.strptime(s, '%Y-%m-%dT%H:%M:%S%z').timestamp() * 1000)
key2job = {}
outcome = collections.defaultdict(list)  # key -> [(t, text)] of the runner's final-state lines
OUT = re.compile(r'^t=(\S+) .*msg="(command ran OK|failed to update server with cmd\'s final state|'
                 r'command \[.*will need to be rerun|server rejected the delayed[^"]*|'
                 r'jobqueue Execute\([0-9a-f]+\): recovered on a new server[^"]*)')
KEY = re.compile(r'(?:jobkey|key)=([0-9a-f]{32})')
for p in glob.glob(os.path.join(rl, '*', '*')):
    cur = None
    for l in open(p, errors='replace'):
        m = RES.match(l)
        if m:
            key2job[m.group(2)] = (m.group(3), m.group(4)); cur = m.group(2)
            continue
        m = OUT.match(l)
        if m:
            k = KEY.search(l); k = k.group(1) if k else cur
            txt = m.group(2)[:60]
            if 'will need to be rerun' in txt: txt = 'will need to be rerun'
            outcome[k].append((ts(m.group(1)), txt))
job2keys = collections.defaultdict(set)
for k, j in key2job.items():
    job2keys[j].add(k)
warned = collections.defaultdict(list)  # (kind,id) -> [ms]
W = re.compile(r'^t=(\S+) .*handing the job out anyway.* key=([0-9a-f]{32})')
nw = 0
for l in open(mlog, errors='replace'):
    m = W.match(l)
    if m:
        nw += 1
        j = key2job.get(m.group(2))
        if j: warned[j].append(ts(m.group(1)))
print(f'"handing the job out anyway" warnings: {nw}; mapped to a psimjob: {sum(len(v) for v in warned.values())}')
c = collections.Counter(); oc = collections.Counter()
for l in open(dbl):
    f = l.rstrip('\n').split('\t')
    kind, jid, where, s1, e1 = f[0], f[1], f[2], int(f[4]), int(f[5])
    w = any(abs(t - s1) <= 60000 for t in warned.get((kind, jid), []))
    c[(where.split(' ')[0], 'anyway' if w else 'NOT-anyway')] += 1
    if not w:
        outs = [o for k in job2keys.get((kind, jid), ()) for o in outcome[k] if s1 - 5000 <= o[0] <= e1 + 900000]
        oc[' > '.join(o[1] for o in sorted(outs)[:4]) or 'no runner log line'] += 1
for k, v in sorted(c.items()):
    print(f'{v:6d}  first run near {k[0]:28s} reservation {k[1]}')
if oc:
    print('runner outcome lines after the first run, for doubles NOT preceded by the warning:')
    for k, v in oc.most_common(20):
        print(f'{v:6d}  {k}')
