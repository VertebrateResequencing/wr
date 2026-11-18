import sys,re,collections
# per 10-minute bin: fold items/min (mean), fold txs, mean of meanHold, max maxLag (s), log records/min; reservation-wait p99 max, add-wait p99 max
def dur(s):
    tot=0.0
    for v,u in re.findall(r'([\d.]+)(ms|µs|us|ns|h|m|s)',s):
        v=float(v); tot+=v*{'h':3600,'m':60,'s':1,'ms':1e-3,'µs':1e-6,'us':1e-6,'ns':1e-9}[u]
    return tot
B=collections.defaultdict(lambda: collections.defaultdict(list))
for l in open(sys.argv[1], errors='replace'):
    m=re.match(r't=(\S+?)T(\d\d):(\d\d)',l)
    if not m: continue
    k=f"{m.group(2)}:{int(m.group(3))//10}0"
    if 'msg="db writer" writer=fold' in l:
        g=dict(re.findall(r'(\w+)=(\S+)',l))
        B[k]['fitems'].append(int(g['items'])); B[k]['lag'].append(dur(g.get('maxLag','0s'))); B[k]['hold'].append(dur(g['meanHold'])); B[k]['txs'].append(int(g['txs']))
    elif 'msg="db writer" writer=log' in l:
        g=dict(re.findall(r'(\w+)=(\S+)',l)); B[k]['lrec'].append(int(g['records']))
    elif 'msg="reservation wait"' in l:
        g=dict(re.findall(r'(\w+)=(\S+)',l)); B[k]['rp99'].append(dur(g['p99']))
    elif 'msg="add wait"' in l:
        g=dict(re.findall(r'(\w+)=(\S+)',l)); B[k]['ap99'].append(dur(g['p99']))
print("bin    foldItems/min  foldTx/min meanHold(s) maxLag(s) logRecs/min resvP99max(s) addP99max(s) minutes")
for k in sorted(B):
    d=B[k]; mean=lambda x: sum(x)/len(x) if x else float('nan')
    print(f"{k}  {mean(d['fitems']):10.0f} {mean(d['txs']):8.1f} {mean(d['hold']):8.2f} {max(d['lag'] or [0]):9.0f} {mean(d['lrec']):10.0f} {max(d['rp99'] or [0]):8.2f} {max(d['ap99'] or [0]):8.2f} {len(d['lrec'])}")
