#!/usr/bin/env bash
#
# speedlayout.sh - writes the build overlay for one of speed.sh's code layouts.
# See ../DEVELOPERS.md ("Code layouts").
#
#   developers/speedlayout.sh <tree> <pkg> <layout> <outdir>
#
# A benchmark's speed can depend by up to about 10% on where the linker places
# the test binary's code, so one test-only commit can move a benchmark with no
# product change (bugfix 261119 item I). speed.sh therefore builds each
# package's test binary in several layouts and pools their samples. Layout 0 is
# the tree as it is: this prints nothing. Layout k > 0 adds k empty Test
# functions to <tree>/<pkg> in a generated aaa_speed_layout_test.go, which sorts
# before the package's own test files so it moves all of their code; it writes
# the file and a `go build -overlay` file mapping it into the package under
# <outdir>, and prints the overlay's path. The tree itself is never written to.

set -euo pipefail

[ "$#" -eq 4 ] || { echo "usage: $0 <tree> <pkg> <layout> <outdir>" >&2; exit 2; }
tree=$(cd "$1" && pwd)
pkg="$2" layout="$3" outdir="$4"
case "$layout" in (''|*[!0-9]*) echo "speedlayout: layout '$layout' is not a whole number" >&2; exit 2 ;; esac
[ "$layout" -eq 0 ] && exit 0

target="$tree/$pkg/aaa_speed_layout_test.go"
[ -e "$target" ] && { echo "speedlayout: $target already exists" >&2; exit 1; }
name=$(cd "$tree" && go list -f '{{.Name}}' "./$pkg") || { echo "speedlayout: no Go package at $tree/$pkg" >&2; exit 1; }

mkdir -p "$outdir"
src="$(cd "$outdir" && pwd)/${pkg//\//_}.layout$layout.go"
{
  printf 'package %s\n\nimport "testing"\n' "$name"
  for i in $(seq 1 "$layout"); do printf '\nfunc TestSpeedLayoutPad%d(t *testing.T) { t.Log(%d) }\n' "$i" "$i"; done
} >"$src"
overlay="${src%.go}.json"
printf '{"Replace":{"%s":"%s"}}\n' "$target" "$src" >"$overlay"
echo "$overlay"
