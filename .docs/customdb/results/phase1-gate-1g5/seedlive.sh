#!/usr/bin/env bash
# seedlive.sh <outdir> <soakdir> <nJobs> <chunk> [delaySecs=300]:
# phase 1 gate item 1g.5's checkpoint measurement. Once restarts.tsv has its
# first start, waits delaySecs, then adds nJobs live jobs that never run
# (limit group ckptlive:0, as wrdev.sh's ready-but-blocked gates, so no LSF
# job is created), in adds of chunk jobs, so the log store's checkpoints hold
# a production-sized live set. Each command is about 1KB, half of it random
# hex from a fixed seed, so zstd cannot shrink the checkpoint to a test-scale
# one. Logs to <outdir>/seedlive.log.
# shellcheck source=/dev/null
. "$2/config.sh"
d=$(soak_outdir "$1") || exit 1
n=$3 chunk=$4 delay=${5:-300}
soak_enter "$d"
log() { echo "$(date +%s.%3N) $*" >> "$d/seedlive.log"; }
until grep -q '	start	' "$d/restarts.tsv"; do soak_alive "$d" || exit 0; sleep 5; done
sleep "$delay"
soak_isolated || { log "not our manager config"; exit 1; }
gen=$d/seedlive.gen.py
cat > "$gen" <<'EOF'
import random, sys
first, n = int(sys.argv[1]), int(sys.argv[2])
rnd = random.Random(1950 + first)
for i in range(first, first + n):
    pad = "%0512x" % rnd.getrandbits(2048)
    print(f"true ckptlive {i} /lustre/scratch126/hgi/projects/ckptlive/sample_{i:08d}/{pad} --out /lustre/scratch126/hgi/projects/ckptlive/out/{i:08d}.cram --ref /lustre/scratch126/hgi/ref/GRCh38_full_analysis_set_plus_decoy_hla.fa --threads 4 --memory 8G --tmpdir /tmp/ckptlive_{i}")
EOF
log "seeding $n jobs in adds of $chunk"
for (( first = 0; first < n; first += chunk )); do
  t0=$(date +%s%3N)
  out=$(python3 "$gen" "$first" "$chunk" \
    | timeout 900 "$SOAK_WR" add --deployment production -f - -i ckptlive --limit_grps ckptlive:0 -r 0 -m 100M -t 10m --queue "$QUEUE" 2>&1 | tail -1)
  log "add $first+$chunk took $(( $(date +%s%3N) - t0 ))ms: $out"
done
log "seeded"
