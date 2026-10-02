#!/usr/bin/env python3
"""Runner-log analysis. usage: runnerlogs.py <runnerlogdir> [unacked.tsv]

Per runner log (jobs run one at a time in a runner, so unkeyed lines belong to the
job most recently started):
 (b) jobs whose start report was left unacked, and whether it was settled
     ("reported ... after retrying" / "server rejected the delayed ...") BEFORE the
     job's outcome line ("command ran OK" / "will need to be rerun"); final-state
     failures and their errors (bad request = archive rejected for a missing start).
 (d) logs whose last line is "kill requested externally"; kill-requested jobs with
     no following kill line; "gave up waiting for the resource checking goroutine".
"""
import collections, os, re, sys

root = sys.argv[1]
KEY = re.compile(r'(?:jobkey|key)=([0-9a-f]{32})')
TS = re.compile(r'^t=(\S+)')
MSG = re.compile(r'msg="((?:[^"\\]|\\.)*)"')
ERR = re.compile(r'err="((?:[^"\\]|\\.)*)"')

c = collections.Counter()
errs = collections.Counter()
bad = []           # problems, (file, text)
unacked_rows = []  # key, file, started ts, first-warn ts, settled ts, settled how, outcome ts, outcome
logs = 0
ends_on_kre = []
kre_nokill = []
for dp, _, fs in os.walk(root):
    for fn in fs:
        p = os.path.join(dp, fn)
        logs += 1
        try:
            lines = open(p, errors='replace').read().splitlines()
        except OSError:
            continue
        cur = None
        st = {}
        kre_open = None
        for ln in lines:
            m = MSG.search(ln)
            if not m:
                continue
            msg = m.group(1)
            ts = (TS.search(ln) or [None, ''])[1] if TS.search(ln) else ''
            km = KEY.search(ln)
            if msg == 'started executing':
                cur = km.group(1) if km else None
                st = {'key': cur, 'start': ts}
                if kre_open:
                    kre_nokill.append((p, kre_open))
                kre_open = None
            elif msg.startswith('could not report command start to server; keeping'):
                c['unacked_start'] += 1
                st['warn'] = ts
                errs['start-warn: ' + (ERR.search(ln).group(1) if ERR.search(ln) else '?')[:60]] += 1
            elif msg == 'reported command start to server after retrying':
                c['start_settled_ok'] += 1
                st.setdefault('settled', ts); st.setdefault('how', 'accepted')
            elif msg.startswith('server rejected the delayed command-start report'):
                c['start_settled_rejected'] += 1
                st.setdefault('settled', ts); st.setdefault('how', 'rejected')
                errs['start-reject: ' + re.sub(r'\([0-9a-f]{32}\)', '(K)', ERR.search(ln).group(1) if ERR.search(ln) else '?')[:80]] += 1
            elif msg.startswith('could not report command start to server; will keep retrying'):
                c['start_retry_failed_attempt'] += 1
            elif msg == "failed to update server with cmd's final state":
                c['final_state_fail'] += 1
                e = ERR.search(ln).group(1) if ERR.search(ln) else '?'
                errs['final: ' + re.sub(r'\([0-9a-f]{32}\)', '(K)', e)[:90]] += 1
                if 'bad request' in e and 'jarchive' in e:
                    c['archive_bad_request'] += 1
                    bad.append((p, 'archive bad request: ' + ln[:300]))
                if 'warn' in st and 'settled' not in st:
                    c['final_fail_before_settle'] += 1
            elif msg == 'command ran OK' or 'will need to be rerun' in msg or 'recovered on a new server' in msg:
                out = 'ok' if msg == 'command ran OK' else ('rerun' if 'rerun' in msg else 'ok-newserver')
                c['outcome_' + out] += 1
                if out == 'rerun':
                    bad.append((p, 'rerun: ' + ln[:400]))
                if 'warn' in st:
                    if 'settled' not in st:
                        c['UNSETTLED_AT_OUTCOME'] += 1
                        bad.append((p, f"outcome {out} with start unsettled, key {st.get('key')}"))
                    unacked_rows.append((st.get('key'), p, st.get('start'), st.get('warn'), st.get('settled', ''),
                                         st.get('how', ''), ts, out))
                st = {}
                if kre_open:
                    kre_nokill.append((p, kre_open))
                    kre_open = None
            elif msg == 'kill requested externally':
                c['kill_requested'] += 1
                kre_open = ts
            elif msg in ('killed cmd', 'failed to kill cmd') or msg.startswith('killed child of cmd'):
                c[msg.split(' of')[0].replace(' ', '_')] += 1
                kre_open = None
            elif msg.startswith('gave up waiting for the resource checking goroutine'):
                c['gave_up_resource_goroutine'] += 1
            elif 'giving up trying' in msg:
                c['giving_up'] += 1
                bad.append((p, ln[:300]))
        if lines:
            lm = MSG.search(lines[-1])
            if lm and lm.group(1) == 'kill requested externally':
                ends_on_kre.append(p)

print(f'runner logs: {logs}')
for k, v in sorted(c.items()):
    print(f'  {k}: {v}')
print('error texts:')
for k, v in errs.most_common(30):
    print(f'  {v:6d} {k}')
print(f'logs whose last line is "kill requested externally": {len(ends_on_kre)}')
for p in ends_on_kre[:20]:
    print('   ', p)
print(f'kill requested with no kill line before the job ended or the log ended (incl. above): '
      f'{len(kre_nokill) + len(ends_on_kre)}')
for p, t in kre_nokill[:20]:
    print('   ', t, p)
print(f'problems: {len(bad)}')
for p, t in bad[:60]:
    print('   ', p, t)
if len(sys.argv) > 2:
    with open(sys.argv[2], 'w') as o:
        for r in unacked_rows:
            o.write('\t'.join(map(str, r)) + '\n')
