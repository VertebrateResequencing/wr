# The 1g.6 review mutants against fix round 3's code: round 2's M1-M8 and
# round 3's N1-N10, re-anchored where countJobs changed. Baseline first.
# Usage: python3 mutants.py <scratch copy of the repo> [label ...]
import os
import shutil
import subprocess
import sys

W = sys.argv[1]
SC = "jobqueue/store_convert.go"
DB = "jobqueue/db.go"
JC = ("\tif b.upgrade.progressDue(n) {\n\t\tb.upgrade.writeProgress(internal.DBUpgradeConvertState,\n"
      "\t\t\tfmt.Sprintf(\"counting the jobs to convert (%d counted)\", n), n)\n\t}")
WP = ("b.upgrade.writeProgress(internal.DBUpgradeConvertState,\n"
      "\t\t\tfmt.Sprintf(\"counting the jobs to convert (%d counted)\", n), n)")
RESET = "\tr.phaseProcessed = 0\n\n\tr.writeStatus(r.phaseState, detail, r.phaseProcessed)"
GUARD = "\t\t\tif !counted || v == nil {\n\t\t\t\treturn nil\n\t\t\t}\n"

muts = [
    ("baseline", SC, "func (b *storeBuild) jobCounted(n int) {", "func (b *storeBuild) jobCounted(n int) {"),
    # round 2
    ("M1-no-count-progress", SC, "\t\t\tb.jobCounted(n)\n", ""),
    ("M2-no-reset", DB, RESET, "\n\tr.writeStatus(r.phaseState, detail, r.phaseProcessed)"),
    ("M3-detail-no-count", SC, 'fmt.Sprintf("counting the jobs to convert (%d counted)", n), n)',
     'fmt.Sprint("counting the jobs to convert", n*0), n)'),
    ("M4-writeStatus-not-log", SC, WP,
     "b.upgrade.phaseProcessed = n\n\t\tb.upgrade.writeStatus(internal.DBUpgradeConvertState,\n"
     "\t\t\tfmt.Sprintf(\"counting the jobs to convert (%d counted)\", n), n)"),
    ("M5-progress-total-in-count", SC, '(%d counted)", n), n)', '(%d counted)", n), n/2)'),
    ("M6-reset-after-write", DB, RESET,
     "\n\tr.writeStatus(r.phaseState, detail, r.phaseProcessed)\n\tr.phaseProcessed = 0"),
    ("M7-count-runstate-progress-only", SC, GUARD,
     "\t\t\tif v != nil {\n\t\t\t\tb.upgrade.progress(internal.DBUpgradeConvertState, "
     "fmt.Sprintf(\"counting the jobs to convert (%d counted)\", n), n)\n\t\t\t}\n\n" + GUARD),
    ("M8-convert-no-log", SC,
     "\t\tb.upgrade.writeProgress(internal.DBUpgradeConvertState, b.progressDetail(), b.processed)",
     "\t\tb.upgrade.phaseProcessed = b.processed\n"
     "\t\tb.upgrade.writeStatus(internal.DBUpgradeConvertState, b.progressDetail(), b.processed)"),
    # round 3
    ("N1-log-not-sidecar", SC, JC, JC.replace("writeProgress", "logProgress")),
    ("N2-sidecar-processed-0", SC, JC,
     JC[:-3] + "\n\t\tb.upgrade.writeStatus(internal.DBUpgradeConvertState, \"counting the jobs to convert\", 0)\n\t}"),
    ("N3-count-nested", SC, "\t\t\tif !counted || v == nil {", "\t\t\tif !counted {"),
    ("N4-count-runstate", SC, "\t\tcounted := !bytes.Equal(name, bucketJobRunState)", "\t\tcounted := true"),
    ("N5-report-before-inc", SC, "\t\t\tn++\n\n\t\t\tb.jobCounted(n)", "\t\t\tb.jobCounted(n)\n\n\t\t\tn++"),
    ("N6-wrong-state", SC, "\t\tb.upgrade.writeProgress(internal.DBUpgradeConvertState,\n\t\t\tfmt.Sprintf(\"counting",
     "\t\tb.upgrade.writeProgress(internal.DBUpgradeJobLookupState,\n\t\t\tfmt.Sprintf(\"counting"),
    ("N7-no-total", DB, "\tr.phaseTotal = total\n\tr.phaseDetail = detail\n", "\tr.phaseDetail = detail\n"),
    ("N8-no-time-progress", SC, "\tif b.upgrade.progressDue(n) {", "\tif n%dbUpgradeProgressEntries == 0 {"),
    ("N9-swallow-err", SC, "\t\tif err != nil {\n\t\t\treturn 0, err\n\t\t}\n\t}\n\n\treturn n, nil",
     "\t\t_ = err\n\t}\n\n\treturn n, nil"),
    ("N10-keep-detail", DB, "\tr.phaseTotal = total\n\tr.phaseDetail = detail\n", "\tr.phaseTotal = total\n"),
]

sel = sys.argv[2:]
env = dict(os.environ, CGO_ENABLED="1")

for label, f, old, new in muts:
    if sel and label not in sel:
        continue

    p = f"{W}/{f}"
    src = open(p).read()

    if src.count(old) != 1:
        print("INVALID(match)", label, src.count(old), flush=True)
        continue

    shutil.copy(p, p + ".bak")
    open(p, "w").write(src.replace(old, new))

    try:
        r = subprocess.run(["go", "vet", "-tags", "netgo", "./jobqueue/"], cwd=W, capture_output=True, text=True,
                           env=env)
        if r.returncode:
            print("INVALID", label, r.stderr[-400:], flush=True)
            continue

        r = subprocess.run(["go", "test", "-tags", "netgo", "./jobqueue/", "-run", "Convert|Upgrade", "-count=1",
                            "-timeout", "1500s"], cwd=W, capture_output=True, text=True, env=env)
        fails = [line for line in r.stdout.splitlines() if line.startswith("--- FAIL")]
        print("KILLED" if r.returncode else "SURVIVED", label, fails[:5], flush=True)
    finally:
        shutil.move(p + ".bak", p)
