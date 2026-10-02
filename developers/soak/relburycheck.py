#!/usr/bin/env python3
"""Verdicts for relbury.sh's #654 batches. usage: relburycheck.py <outdir> <runnerlogdir> [final dbstart.tsv]

Per batch <tag> (relb.<tag>.rel: --retries 1; relb.<tag>.bur: --retries 0; every run exits 3 at a
shared deadline D, and the manager is killed -9 soon after D):
 - each job's runs (S/E marks) and its runner's outcome line per run ("exited with code 3", with
   "so it will be tried again" for a release, or a failed final-state update);
 - ACKED-BEFORE-CRASH: an outcome line whose whole second is before the kill's second, so the
   runner had the release/bury acknowledged before the kill. After the restart ("up" snapshot)
   such a rel job must not be running without a later S mark, and such a bur job must be buried
   with no second run;
 - STUCK: a job that is running in a snapshot at least 3 min after the restart (or in the final
   DB) while every run of it has an E mark;
 - runs per job: rel jobs should run exactly twice and bur jobs once, all ending buried.
"""
import collections
import datetime
import glob
import json
import os
import re
import sys

d, rl = sys.argv[1:3]
dbf = sys.argv[3] if len(sys.argv) > 3 else None
R = os.path.join(d, 'relbury')
TS = re.compile(r'^t=(\S+) ')
NAME = re.compile(r': relb (relb\.[^;]+);')


def ts(s):
    return datetime.datetime.strptime(s, '%Y-%m-%dT%H:%M:%S%z').timestamp()


runs = collections.defaultdict(list)  # name -> [[s, e, host, pid]]
for p in glob.glob(os.path.join(R, '*.tsv')):
    for l in open(p):
        f = l.rstrip('\n').split('\t')
        if len(f) < 5 or not f[2].isdigit():
            continue
        if f[0] == 'S':
            runs[f[1]].append([int(f[2]), None, f[3], f[4]])
        elif f[0] == 'E':
            for r in runs[f[1]]:
                if r[3] == f[4] and r[2] == f[3] and r[1] is None:
                    r[1] = int(f[2])
for v in runs.values():
    v.sort()

outcome = collections.defaultdict(list)  # name -> [(ts, kind)]
badjob = collections.Counter()
for p in glob.glob(os.path.join(rl, '*', '*')):
    cur = None
    for l in open(p, errors='replace'):
        if 'relb.' not in l and cur is None:
            continue
        t = TS.match(l)
        if 'msg="started executing"' in l:
            m = NAME.search(l)
            cur = m.group(1) if m else None
            continue
        if cur is None or not t:
            continue
        if 'exited with code 3' in l:
            outcome[cur].append((ts(t.group(1)), 'release' if 'tried again' in l else 'bury'))
            cur = None
        elif "failed to update server with cmd's final state" in l:
            e = re.search(r'err="([^"]*)"', l)
            outcome[cur].append((ts(t.group(1)), 'final-fail: ' + (e.group(1)[:70] if e else '?')))
            if e and 'bad job' in e.group(1):
                badjob[cur] += 1

kills = {}  # tag -> kill ms
for l in open(os.path.join(R, 'driver.log')):
    m = re.match(r'(\d+) (\S+) killed pid \d+ at (\d+)', l)
    if m:
        kills[m.group(2)] = int(m.group(3))
    m = re.match(r'(\d+) (\S+) deadline D=(\d+)', l)
    if m:
        kills.setdefault(m.group(2) + ':D', int(m.group(3)))

snaps = collections.defaultdict(dict)  # tag -> label -> name -> (state, attempts)
for p in glob.glob(os.path.join(R, '*.json')):
    tag, label, _g, _ = os.path.basename(p).rsplit('.', 3) if os.path.basename(p).count('.') >= 3 else (None,) * 4
    if tag is None:
        continue
    try:
        js = json.load(open(p))
    except (ValueError, OSError):
        continue
    for j in js or []:
        m = NAME.search(j.get('Cmd', ''))
        if m:
            snaps[tag].setdefault(label, {})[m.group(1)] = (j.get('State'), j.get('Attempts'))

final = {}
if dbf:
    for l in open(dbf):
        f = l.rstrip('\n').split('\t')
        if len(f) > 6 and f[2] == 'relb':
            final[f[3]] = (f[0], f[4], f[6])

tags = sorted({n.split('.')[1] for n in runs} | {k for k in kills if ':' not in k})
bad = 0
for tag in tags:
    km = kills.get(tag)
    dms = kills.get(tag + ':D')
    names = sorted(n for n in set(runs) | set(outcome) if n.split('.')[1] == tag)
    print(f'== batch {tag}: D={dms} kill={km} (D+{(km - dms) if km and dms else "?"}ms) jobs with marks={len(names)}')
    c = collections.Counter()
    problems = []
    lab = snaps.get(tag, {})
    later = [x for x in lab if x.startswith('+')]
    for n in names:
        g = n.split('.')[2]
        rs = runs.get(n, [])
        oc = outcome.get(n, [])
        pre = [o for o in oc if km and int(o[0]) * 1000 + 999 < km]
        c[(g, 'runs=%d' % len(rs))] += 1
        c[(g, 'acked-before-kill' if pre else 'not-acked-before-kill')] += 1
        up = lab.get('up', {}).get(n)
        if pre and up:
            later_s = [r for r in rs if km and r[0] > km]
            if g == 'bur' and up[0] != 'buried':
                problems.append(f'ACKED-BURY-LOST {n}: acked {pre} but state at up={up}')
            if g == 'rel' and up[0] == 'running' and not later_s:
                problems.append(f'ACKED-RELEASE-LOST {n}: acked {pre} but running at up with no later run')
        for x in later:
            st = lab[x].get(n)
            if st and st[0] == 'running' and rs and all(r[1] for r in rs):
                problems.append(f'STUCK {n} at {x}: {st}, runs {rs}')
        fin = final.get(n)
        if fin:
            c[(g, 'final=' + fin[1])] += 1
            if fin[1] == 'running':
                problems.append(f'STUCK-FINAL {n}: {fin}, runs {rs}')
        want = 2 if g == 'rel' else 1
        if len(rs) > want:
            problems.append(f'EXTRA-RUN {n}: {len(rs)} runs (want {want}): {rs} outcomes {oc}')
        if badjob.get(n):
            problems.append(f'BAD-JOB {n}: {oc}')
        lat = [o[0] - r[1] / 1000 for r, o in zip(rs, oc) if r[1]]
        for x in lat:
            c[(g, 'ack<1s' if x < 1 else ('ack<5s' if x < 5 else 'ack>=5s'))] += 1
    for x, v in sorted(lab.items()):
        print(f'   snapshot {x}: ' + ' '.join(f'{k}={v2}' for k, v2 in sorted(collections.Counter(s[0] for s in v.values()).items())))
    for k, v in sorted(c.items()):
        print(f'   {k[0]} {k[1]}: {v}')
    for p in problems:
        print('   ' + p)
    bad += len(problems)
print('problems:', bad)
