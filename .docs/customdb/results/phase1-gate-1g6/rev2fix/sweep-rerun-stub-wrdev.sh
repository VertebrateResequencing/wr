#!/usr/bin/env bash
# stub of wrdev.sh's add-storm-fixture output contract for a sweep.sh rerun test
case "$1" in
  job-token) echo stub${2}tok ;;
  add-storm-fixture)
    out=${WRDEV_ASL_FIXTURE:?}
    [ -e "$out" ] && { echo "wrdev: $out exists (an earlier fixture?); remove it, and its .aslmanifest, first" >&2; exit 1; }
    mkdir -p "$out" "$out.jobcwd" && echo data > "$out/log.0" && echo "size 5" > "$out.aslmanifest" && echo "fixture written" ;;
  *) exit 3 ;;
esac
