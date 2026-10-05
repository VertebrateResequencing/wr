#!/usr/bin/env python3
"""Which double runs had their first run handed out before its reservation was on disk.

usage: anyway.py <outdir> <runnerlogdir> <doubles.tsv> <managerlog> [managerlog...]

When a reservation's durable write takes longer than ReserveWriteWait (10s by default), the
manager hands the job out anyway, and a crash before that job's start is recorded loses the
reservation, so the job runs again: the documented cost (.docs/bugfixes/260928-reserve-durability.md).

The manager's warning about it ("reservation not yet recorded on disk, handing the job out
anyway ...") is rate-limited: one full line (key=) per minute, then one "(repeated) repeats=N"
summary naming only the latest key (sample_key=), and a crash loses the pending summary. So most
such reservations are not named in the log, and this classifies each double run (a doubles.tsv
row, from doubles.py) from the runner log of its first run instead, found through that run's
marker (host, pid) and the runner's "started executing ... pid=" line. The first run's
reservation counts as handed out anyway on any of:
  slow    the runner's "reserved a job" line is at least ReserveWriteWait after the line before
          it (the runner's start, or the end of its previous job), which is when it asked;
  lost    after a manager stop (restarts.tsv) that followed the reservation, the manager
          rejected that run's own start, touch or final report as "bad job" or "you must
          Reserve()";
  warned  the manager's warning names the key (key= or sample_key=) within 60s of it.
slow on its own can also be a runner that waited for a job to become ready; lost is what a
reservation that was on disk does not show, since recovery returns such a job to its runner.
The outcome lines listed are the first run's own: those of its runner log between its
reservation and the runner's next one.
"""
import collections, datetime, glob, os, re, sys

if len(sys.argv) < 5:
    sys.exit(__doc__)
d, rl, dbl, mlogs = sys.argv[1], sys.argv[2], sys.argv[3], sys.argv[4:]
RESERVE_WRITE_WAIT_MS = 10000  # jobqueue's serverReserveWriteWait() with the default timeouts
WARN_NEAR_MS = 60000

def ts(s):
    return int(datetime.datetime.strptime(s, '%Y-%m-%dT%H:%M:%S%z').timestamp() * 1000)

# the first run of each double, and its pid from its S marker
first = []  # (kind, id, where, host, s1)
for l in open(dbl):
    f = l.rstrip('\n').split('\t')
    first.append((f[0], f[1], f[2], f[3], int(f[4])))
want = {(k, i, h, s) for k, i, _, h, s in first}
pid_of = {}
for p in glob.glob(os.path.join(d, 'markers', '*.tsv')):
    for l in open(p, errors='replace'):
        f = l.rstrip('\n').split('\t')
        if len(f) >= 6 and f[0] == 'S' and f[1].isdigit() and (f[2], f[3], f[4], int(f[1])) in want:
            pid_of[(f[2], f[3], f[4], int(f[1]))] = f[5]
targets = {(h, pid_of[(k, i, h, s)]) for k, i, _, h, s in first if (k, i, h, s) in pid_of}

# a stop, not the next start, since restarts.tsv records a start once the manager is ready, which
# can be after the restarted manager's first replies
stops = sorted(int(l.split('\t')[0]) * 1000 for l in open(os.path.join(d, 'restarts.tsv'))
               if l.split('\t')[1:2] == ['stop'])

T = re.compile(r'^t=(\S+) ')
RES = re.compile(r'msg="reserved a job" key=([0-9a-f]{32}) cmd="\S*psimjob\.sh (\S+) (\S+) ')
PID = re.compile(r' pid=(\d+)')
REJ = re.compile(r'j(?:start|touch|archive|release|bury)\([0-9a-f]{32}\): (?:bad job|you must Reserve\(\))')
ERR = re.compile(r' err="((?:[^"\\]|\\.)*)"')
OUTS = [  # (pattern, label): the outcome lines worth listing, in a run's own segment
    (re.compile(r'msg="command ran OK"'), 'ran OK'),
    (re.compile(r'msg="failed to update server with cmd\'s final state"'), 'final report failed'),
    (re.compile(r'msg="server rejected the delayed command-start report'), 'delayed start report rejected'),
    (re.compile(r'msg="command \[.*\] started running, but I killed it due to a jobqueue server error: ([^"]*)'),
     'killed for server error'),
    (re.compile(r'msg="command \[.*will need to be rerun'), 'will need to be rerun'),
    (re.compile(r'msg="could not touch"'), 'touch failed'),
    (re.compile(r'msg="jobqueue Execute\([0-9a-f]+\): recovered on a new server'), 'recovered on a new server'),
]

def norm(e):
    return re.sub(r'\([0-9a-f]{32}\)', '(K)', e)[:60]

def outcome(l):
    for pat, label in OUTS:
        m = pat.search(l)
        if m:
            e = m.group(1) if m.groups() else (ERR.search(l).group(1) if ERR.search(l) else '')
            return f'{label}: {norm(e)}' if e else label
    return None

# (host, pid) -> [segment]: a runner's reservation of one job, and its own lines up to the
# runner's next reservation (a runner runs one job at a time)
runs = collections.defaultdict(list)
for p in glob.glob(os.path.join(rl, '*', '*')):
    host = os.path.basename(p).split('.')[1]
    prev, seg, segs = None, None, []
    for l in open(p, errors='replace'):
        m = T.match(l)
        if not m:
            continue
        t = ts(m.group(1))
        if 'msg="reserved a job"' in l:
            seg = None
            m = RES.search(l)
            if m:
                seg = {'key': m.group(1), 'job': (m.group(2), m.group(3)), 'res': t, 'req': prev, 'pid': None,
                       'lost': False, 'outs': []}
                segs.append(seg)
        elif seg is not None:
            if seg['pid'] is None and 'msg="started executing"' in l:
                m = PID.search(l)
                seg['pid'] = m.group(1) if m else None
            if REJ.search(l) and any(seg['res'] < s <= t for s in stops):
                seg['lost'] = True
            o = outcome(l)
            if o and (not seg['outs'] or seg['outs'][-1] != o):
                seg['outs'].append(o)
        prev = t
    for seg in segs:
        if (host, seg['pid']) in targets:
            runs[(host, seg['pid'])].append(seg)

W = re.compile(r'^t=(\S+) .*msg="reservation not yet recorded on disk, handing the job out anyway.* (?:sample_)?key=([0-9a-f]{32})')
REPEATS = re.compile(r' \(repeated\)" repeats=(\d+) ')
warned = collections.defaultdict(list)  # key -> [ms]
full = summaries = repeats = 0
seen = set()
for path in mlogs:  # copies of one log may overlap, so a line is counted once
    for l in open(path, errors='replace'):
        m = W.match(l)
        if not m or l in seen:
            continue
        seen.add(l)
        r = REPEATS.search(l)
        if r:
            summaries += 1
            repeats += int(r.group(1))
        else:
            full += 1
        warned[m.group(2)].append(ts(m.group(1)))
print(f'"handing the job out anyway" warnings: {full} full lines + {summaries} summaries of {repeats} repeats '
      f'= {full + repeats} reservations handed out before they were on disk (less any summary a crash lost)')

c = collections.Counter(); oc = collections.Counter()
for kind, jid, where, host, s1 in first:
    segs = [s for s in runs.get((host, pid_of.get((kind, jid, host, s1))), ()) if s['job'] == (kind, jid)]
    seg = min(segs, key=lambda s: abs(s['res'] - s1)) if segs else None
    if seg is None:
        verdict, sig = 'NOT-anyway', 'no runner log'
    else:
        sig = '+'.join(n for n, on in (
            ('slow', seg['req'] is not None and seg['res'] - seg['req'] >= RESERVE_WRITE_WAIT_MS),
            ('lost', seg['lost']),
            ('warned', any(abs(t - seg['res']) <= WARN_NEAR_MS for t in warned.get(seg['key'], ()))),
        ) if on)
        verdict, sig = ('anyway', sig) if sig else ('NOT-anyway', '-')
    c[(where.split(' ')[0], verdict, sig)] += 1
    oc[(verdict, ' > '.join(seg['outs'][:4]) if seg and seg['outs'] else 'no outcome line')] += 1
for k, v in sorted(c.items()):
    print(f'{v:6d}  first run near {k[0]:28s} reservation {k[1]:10s} ({k[2]})')
print("the first run's own runner outcome lines:")
for k, v in oc.most_common(20):
    print(f'{v:6d}  {k[0]:10s} {k[1]}')
