#!/usr/bin/env bash
# finding 3: does copying a symlinked fixture with <cp flags> give the run its
# own copy, or a link through which the run writes into the pristine fixture?
# usage: demo.sh <cp flags>
set -u
d=$(mktemp -d -p "$(cd "$(dirname "$0")" && pwd)")
for kind in dir file; do
  if [ $kind = dir ]; then mkdir "$d/pristine.$kind"; echo orig > "$d/pristine.$kind/wal"; tgt="$d/run.$kind/wal"; src="$d/pristine.$kind/wal"
  else echo orig > "$d/pristine.$kind"; tgt="$d/run.$kind"; src="$d/pristine.$kind"; fi
  ln -s "$d/pristine.$kind" "$d/link.$kind"
  cp $1 "$d/link.$kind" "$d/run.$kind"
  echo "manager write" >> "$tgt"
  printf '%-4s fixture: run copy is %s; pristine now: %s\n' "$kind" \
    "$( [ -L "$d/run.$kind" ] && echo 'a SYMLINK' || echo 'a real copy')" "$(tr '\n' '|' < "$src")"
done
