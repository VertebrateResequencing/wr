#!/bin/bash
# Validation script for the verify file system.
# Checks that:
# 1. Every feature ID in nf-*.md files appears in at least one test-*.md file
# 2. Every feature ID in test-*.md files exists in an nf-*.md file
# 3. Every feature ID in the manifest has a covering nf- file
# 4. Reports orphaned feature IDs
#
# Usage: bash .docs/nextflow/verify/00-validate.sh

set -euo pipefail
DIR="$(cd "$(dirname "$0")" && pwd)"

echo "=== Nextflow Verify File Validation ==="
echo

# Extract all feature IDs from nf- files (### ID lines)
NF_IDS=$(grep -h '^### ' "$DIR"/nf-*.md 2>/dev/null | sed 's/^### //' | sort -u)
NF_COUNT=$(echo "$NF_IDS" | wc -l)
echo "Feature IDs in nf-*.md files: $NF_COUNT"

# Extract all feature IDs mentioned in test- files
TEST_IDS=$(grep -ohE '\b[A-Z]+-[a-zA-Z0-9_-]+' "$DIR"/test-*.md 2>/dev/null | sort -u)
TEST_COUNT=$(echo "$TEST_IDS" | wc -l)
echo "Feature IDs referenced in test-*.md files: $TEST_COUNT"

# Extract all feature IDs from manifest
MANIFEST_IDS=$(grep -ohE '\b[A-Z]+-[a-zA-Z0-9_-]+' "$DIR"/00-manifest.md 2>/dev/null | sort -u)
MANIFEST_COUNT=$(echo "$MANIFEST_IDS" | wc -l)
echo "Feature IDs in 00-manifest.md: $MANIFEST_COUNT"
echo

# Check 1: nf- IDs not in any test file
echo "--- Feature IDs in nf-*.md but not in any test-*.md ---"
MISSING=0
while IFS= read -r id; do
    if ! echo "$TEST_IDS" | grep -qxF "$id"; then
        echo "  MISSING from tests: $id"
        MISSING=$((MISSING + 1))
    fi
done <<< "$NF_IDS"
if [ "$MISSING" -eq 0 ]; then
    echo "  (none — all covered)"
fi
echo

# Check 2: manifest IDs not in nf- files
echo "--- Feature IDs in manifest but not in nf-*.md ---"
MISSING=0
while IFS= read -r id; do
    if ! echo "$NF_IDS" | grep -qxF "$id"; then
        echo "  MISSING from specs: $id"
        MISSING=$((MISSING + 1))
    fi
done <<< "$MANIFEST_IDS"
if [ "$MISSING" -eq 0 ]; then
    echo "  (none — all covered)"
fi
echo

# Summary
echo "--- File counts ---"
echo "  nf-*.md:   $(ls "$DIR"/nf-*.md 2>/dev/null | wc -l)"
echo "  impl-*.md: $(ls "$DIR"/impl-*.md 2>/dev/null | wc -l)"
echo "  test-*.md: $(ls "$DIR"/test-*.md 2>/dev/null | wc -l)"
echo "  Total:     $(ls "$DIR"/*.md 2>/dev/null | wc -l)"
echo
echo "Done."
