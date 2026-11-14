# The 1g.6 review round 2 mutants (scratchpad/1g6rev2/muts.py), with M1, M4, M5
# and M7 re-anchored on countJobs' jobCounted form. Baseline first.
import subprocess, sys, shutil, os
W=sys.argv[1]
WP="b.upgrade.writeProgress(internal.DBUpgradeConvertState,\n\t\t\tfmt.Sprintf(\"counting the jobs to convert (%d counted)\", n), n)"
muts = [
 ("M0-baseline","jobqueue/store_convert.go","func (b *storeBuild) jobCounted(n int) {","func (b *storeBuild) jobCounted(n int) {"),
 ("M1-no-count-progress","jobqueue/store_convert.go","\t\t\t\tb.jobCounted(n)\n",""),
 ("M2-no-reset","jobqueue/db.go","\tr.phaseProcessed = 0\n\n\tr.writeStatus(r.phaseState, detail, r.phaseProcessed)","\n\tr.writeStatus(r.phaseState, detail, r.phaseProcessed)"),
 ("M3-detail-no-count","jobqueue/store_convert.go",'fmt.Sprintf("counting the jobs to convert (%d counted)", n), n)','fmt.Sprint("counting the jobs to convert", n*0), n)'),
 ("M4-writeStatus-not-log","jobqueue/store_convert.go",WP,"b.upgrade.phaseProcessed = n\n\t\tb.upgrade.writeStatus(internal.DBUpgradeConvertState,\n\t\t\tfmt.Sprintf(\"counting the jobs to convert (%d counted)\", n), n)"),
 ("M5-progress-total-in-count","jobqueue/store_convert.go",'(%d counted)", n), n)','(%d counted)", n), n/2)'),
 ("M6-reset-after-write","jobqueue/db.go","\tr.phaseProcessed = 0\n\n\tr.writeStatus(r.phaseState, detail, r.phaseProcessed)","\n\tr.writeStatus(r.phaseState, detail, r.phaseProcessed)\n\tr.phaseProcessed = 0"),
 ("M7-count-runstate-progress-only","jobqueue/store_convert.go","\t\t\tif counted && v != nil {\n\t\t\t\tn++\n","\t\t\tif v != nil {\n\t\t\t\tb.upgrade.progress(internal.DBUpgradeConvertState, fmt.Sprintf(\"counting the jobs to convert (%d counted)\", n), n)\n\t\t\t}\n\t\t\tif counted && v != nil {\n\t\t\t\tn++\n"),
 ("M8-convert-no-log","jobqueue/store_convert.go","\t\tb.upgrade.writeProgress(internal.DBUpgradeConvertState, b.progressDetail(), b.processed)","\t\tb.upgrade.phaseProcessed = b.processed\n\t\tb.upgrade.writeStatus(internal.DBUpgradeConvertState, b.progressDetail(), b.processed)"),
]
sel = sys.argv[2:]
for label,f,old,new in muts:
    if sel and label not in sel: continue
    p=f"{W}/{f}"; src=open(p).read()
    if src.count(old)!=1: print("INVALID(match)",label, flush=True); continue
    shutil.copy(p,p+".bak"); open(p,"w").write(src.replace(old,new))
    try:
        env=dict(os.environ,CGO_ENABLED="1")
        r=subprocess.run(["go","test","-tags","netgo","./jobqueue/","-run","^$","-count=1"],cwd=W,capture_output=True,text=True,env=env)
        if r.returncode: print("INVALID",label,r.stderr[-400:], flush=True); continue
        r=subprocess.run(["go","test","-tags","netgo","./jobqueue/","-run","Convert|Upgrade","-count=1","-timeout","900s"],cwd=W,capture_output=True,text=True,env=env)
        fails=[l for l in r.stdout.splitlines() if l.startswith("--- FAIL")]
        print("KILLED" if r.returncode else "SURVIVED", label, fails[:5], flush=True)
    finally:
        shutil.move(p+".bak",p)
