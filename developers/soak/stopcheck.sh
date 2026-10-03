#!/usr/bin/env bash
# stopcheck.sh <outdir> <stopBeginEpoch> <stopEndEpoch> [<previousStartEpoch>]:
# joins the run markers of every portal run in flight at a clean stop to the
# manager's view of its job (psinspect, run against our manager, so it must be
# up), so that killed runs must be buried and runs that exited 0 must be
# complete ("stop means buried"; no lost exit 0). Writes
# <outdir>/stopcheck.<stopBeginEpoch>.{runs,inspect} and prints the verdict.
# shellcheck source=config.sh
. "$(dirname "$0")/config.sh"
[ $# -ge 3 ] || die "usage: stopcheck.sh <outdir> <stopBeginEpoch> <stopEndEpoch> [<previousStartEpoch>]"
d=$(soak_outdir "$1") || exit 1
t0=$2 t1=$3 tprev=${4:-0} o=$d/stopcheck.$2
[ -x "$SOAK_ROOT/psinspect" ] || die "no $SOAK_ROOT/psinspect; run.sh builds it"
python3 - "$d" "$t0" "$t1" "$tprev" > "$o.runs" <<'PY'
import glob,sys
d,t0,t1,tp=sys.argv[1],int(sys.argv[2])*1000,int(sys.argv[3])*1000,int(sys.argv[4])*1000
runs={}
for p in glob.glob(d+'/markers/*.tsv'):
    for l in open(p):
        f=l.rstrip('\n').split('\t')
        if len(f)<8 or not f[1].isdigit(): continue
        r=runs.setdefault((f[4],f[5],f[2],f[3]),{})
        if f[0]=='S': r['s']=int(f[1])
        if f[0]=='E': r['e']=int(f[1]); r['rc']=f[7]
for k,r in runs.items():
    if 's' not in r or r['s']>t0 or r['s']<tp: continue
    e=r.get('e')
    if e is None or e>=t0-1000:
        cls='noend' if e is None else ('sig' if r['rc'].startswith('sig') else 'rc'+r['rc']+('-during' if e<=t1 else '-after'))
        print(cls,k[2],k[3],r['s'],e,k[0],sep='\t')
PY
rgs=$(awk -F'\t' '$2~/^portal_/{split($3,a,"."); sub("portal_","",$2); print "inc:portal_"a[1]"_"$2}' "$o.runs" | sort -u)
soak_enter "$d"
soak_isolated || die "wr does not resolve --deployment production to our manager on :$PROD_PORT"
# shellcheck disable=SC2086 # one rep group per word
timeout 900 "$SOAK_ROOT/psinspect" $rgs > "$o.inspect"
python3 - "$o.runs" "$o.inspect" <<'PY'
import sys,collections
st={}
for l in open(sys.argv[2]):
    f=l.rstrip('\n').split('\t'); st[(f[1],f[2])]=f
c=collections.Counter(); bad=[]
for l in open(sys.argv[1]):
    f=l.rstrip('\n').split('\t')
    if not f[1].startswith('portal'): continue
    s=st.get((f[1],f[2]))
    c[(f[0], s[3] if s else 'complete(not incomplete)')]+=1
    if f[0].startswith('rc0') and s: bad.append((f,s))
for k,v in sorted(c.items()): print(v,*k)
for f,s in bad: print('EXIT0-NOT-COMPLETE', f, s and s[3:])
PY
