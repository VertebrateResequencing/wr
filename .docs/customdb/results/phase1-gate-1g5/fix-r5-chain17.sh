#!/bin/bash
# 1g.5 bugfix round 5, Monday 2026-11-16 (soaks wholly in 10:00-17:00 GMT):
# the gate soak G7 of the final code first, TestAddLatency1000 x3 (runs
# 19-21), then the higher-load soak H2 (prodsim scale 2) if it can start by
# 15:52. Everything comes from src-gate at commit WANT, which must equal the
# final working tree: the chain refuses to run otherwise, rebuilds the
# jobqueue test binary from it, and checks the soaks' wr buildinfo.
# Start (any time before 09:55 on 2026-11-16; it waits for 09:40):
#   setsid nohup /nfs/hgi/wr/sb10-bigdb/customdb/1g5fix/tools/chain17.sh \
#     > /nfs/hgi/wr/sb10-bigdb/customdb/1g5fix/chain17.out 2>&1 < /dev/null &
set -u
W=/nfs/hgi/wr/sb10-bigdb/customdb/1g5fix
G=$W/src-gate
R=$W/r5
WANT=8dab632aa844fad73528e4172ba257ac90e7ab3f
DAY=2026-11-16
die() { echo "chain17: $* ($(date -u))"; exit 1; }
at() { date -d "$DAY $1" +%s; }

[ $(date +%s) -lt $(at 09:55) ] || die "started after 09:55 on $DAY"
echo "chain17: waiting for 09:40 on $DAY, pid $$ ($(date -u))"
until [ $(date +%s) -ge $(at 09:40) ]; do sleep 30; done

head=$(git -C "$G" rev-parse HEAD)
[ "$head" = "$WANT" ] || die "src-gate HEAD is $head, not $WANT"
[ -z "$(git -C "$G" status --porcelain)" ] || die "src-gate has uncommitted changes"
echo "chain17: src-gate at $(git -C "$G" log --oneline -1)"

(
  unset $(compgen -v | grep '^OS_') 2>/dev/null
  export GOCACHE=$R/gocache GOPROXY=off GOTELEMETRY=off CGO_ENABLED=1 HOME=$W/home \
    GOPATH=/software/hgi/envs/sb10/go GOMODCACHE=/software/hgi/envs/sb10/go/pkg/mod
  cd "$G" && go test -c -tags netgo -o "$R/jq-gate.test" ./jobqueue
) || die "test binary build failed"
echo "chain17: built $R/jq-gate.test from $WANT, sha256 $(sha256sum < "$R/jq-gate.test" | cut -c1-16) ($(date -u))"

checkwr() {
  local info; info=$(go version -m "$W/soak/$1/wr" | grep -E 'vcs\.(revision|modified)')
  echo "chain17: $1 wr buildinfo: $(echo $info)"
  echo "$info" | grep -q "vcs.revision=$WANT" && echo "$info" | grep -q 'vcs.modified=false' \
    && echo "chain17: $1 buildinfo OK" || echo "chain17: $1 buildinfo MISMATCH"
}

[ $(date +%s) -lt $(at 10:00) ] || die "too late for G7"
until [ $(date +%s) -ge $(at 10:00) ]; do sleep 5; done
echo "chain17: G7 start $(date -u)"
$W/tools/soak-r5.sh G7-gate "$G" 1 > $W/G7-gate.out 2>&1 < /dev/null
echo "chain17: G7 done rc=$? $(date -u)"
checkwr G7-gate

for n in 19 20 21; do $W/tools/addbench-r5.sh $n; echo "chain17: addbench $n rc=$? $(date -u)"; done

[ $(date +%s) -le $(at 15:52) ] || die "too late for H2"
echo "chain17: H2 start $(date -u)"
$W/tools/soak-r5.sh H2-scale2 "$G" 1 SCALE=2 > $W/H2-scale2.out 2>&1 < /dev/null
echo "chain17: H2 done rc=$? $(date -u)"
checkwr H2-scale2
