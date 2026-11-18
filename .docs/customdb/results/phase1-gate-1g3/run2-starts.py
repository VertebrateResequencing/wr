import sys,re
# usage: starts.py soakdir [storelisting]
S=sys.argv[1]
def dur(s):
    tot=0.0
    for v,u in re.findall(r'([\d.]+)(ms|µs|h|m|s)',s):
        tot+=float(v)*{'h':3600,'m':60,'s':1,'ms':1e-3,'µs':1e-6}[u]
    return tot
sizes={}
if len(sys.argv)>2:
    for l in open(sys.argv[2]):
        m=re.search(r'\s(\d+)\s+\S+\s+([0-9a-f]{8})\.wal$',l)
        if m: sizes[int(m.group(2),16)]=int(m.group(1))
starts=[l.split('\t') for l in open(S+'/restarts.tsv') if '\tstart\t' in l]
st={}
import os
for l in (open(S+'/statustime.tsv') if os.path.exists(S+'/statustime.tsv') else []):
    f=l.rstrip('\n').split('\t')
    if len(f)>1 and f[1]=='start':
        g=dict(re.findall(r'(\w+)=(\S+)',l)); st[int(f[0])//1000]=g
phases=[]; cur=None
for l in open(S+'/manager.log',errors='replace'):
    if 'msg="recovering: opened database"' in l:
        import datetime
        ts=datetime.datetime.fromisoformat(re.match(r't=(\S+)',l).group(1)).timestamp()
        cur={'open':dur(re.search(r'elapsed=(\S+)',l).group(1)),'t':ts}; phases.append(cur)
    elif cur is not None and 'msg="recovering: decoded live jobs"' in l:
        cur['jobs']=int(re.search(r'jobs=(\d+)',l).group(1)); cur['decode']=dur(re.search(r'elapsed=(\S+)',l).group(1))
    elif cur is not None and 'built dependency-group state' in l:
        cur['dep']=dur(re.search(r'elapsed=(\S+)',l).group(1))
    elif cur is not None and 'enqueued prior jobs' in l:
        cur['enq']=dur(re.search(r'elapsed=(\S+)',l).group(1))
print("start\ttotal_s\topen_s\tdecode_s\tdepgrp_s\tenqueue_s\tliveJobs\tus/job(decode)\twalFiles\tbacklogMB\twalFromCkptMB\tload")
for i,s in enumerate(starts):
    t=int(s[0]); g=dict(re.findall(r'(\w+)=(\S+)','\t'.join(s)))
    cand=[x for x in phases if x['t']<=t+1]
    p=cand[-1] if cand else {}
    if p.get('used'): p={}
    elif p: p['used']=True
    k=min(st, key=lambda x: abs(x-t)) if st else None
    sg=st.get(k,{}) if k is not None and abs(k-t)<60 else {}
    wal='-'
    if sizes and 'end' in sg:
        ef,eo=sg['end'].split(':'); ef=int(ef)
        if ef>=0xa: wal=f"{(sum(sizes.get(n,0) for n in range(0xa,ef))+int(eo))/1e6:.0f}"
    bl=f"{int(sg['backlogBytes'])/1e6:.0f}" if 'backlogBytes' in sg else '-'
    jobs=p.get('jobs',0)
    print(f"{t}\t{int(g['ms'])/1000:.1f}\t{p.get('open',0):.1f}\t{p.get('decode',0):.1f}\t{p.get('dep',0):.1f}\t{p.get('enq',0):.1f}\t{jobs}\t{(p.get('decode',0)/jobs*1e6) if jobs else 0:.0f}\t{sg.get('walFiles','-')}\t{bl}\t{wal}\t{g.get('load','-')}")
