#!/usr/bin/env bash
# finding 9: which paths beside WRDEV_PROD_BKFILE does each cleanup line delete?
# A backup dir of this run (db_bk_tokA, a directory with the log store), its
# temp (db_bk_tokA.tmp), and siblings of other runs whose token extends tokA.
set -u
d=$(mktemp -d -p "$(cd "$(dirname "$0")" && pwd)")
setup() { rm -rf "${d:?}"/bk; mkdir -p "$d/bk/db_bk_tokA/x" "$d/bk/db_bk_tokA2/x" "$d/bk/db_bk_tokAB"; touch "$d/bk/db_bk_tokA.tmp" "$d/bk/db_bk_tokA_old"; }
WRDEV_PROD_BKFILE="$d/bk/db_bk_tokA"
setup; echo "before:            $(ls "$d/bk" | tr '\n' ' ')"
rm -rf "${WRDEV_PROD_BKFILE:?}"* 2>/dev/null
echo "after old line:    $(ls "$d/bk" | tr '\n' ' ')"
setup
rm -rf "${WRDEV_PROD_BKFILE:?}" "${WRDEV_PROD_BKFILE:?}".* 2>/dev/null
echo "after fixed line:  $(ls "$d/bk" | tr '\n' ' ')"
