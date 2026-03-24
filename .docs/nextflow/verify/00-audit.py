#!/usr/bin/env python3
"""
Unified audit & validation for the Nextflow verify file system.

Combines and improves upon 00-validate.sh and audit_verify.py.

Checks performed:
  1. STRUCTURAL: Every feature ID in nf-*.md is in at least one test-*.md
  2. STRUCTURAL: Every feature ID in test-*.md exists in an nf-*.md
  3. MANIFEST:   Every feature ID in 00-manifest.md exists in an nf-*.md
  4. MANIFEST:   Every feature ID in nf-*.md exists in 00-manifest.md
  5. TEST-SPEC:  Every ### ID in nf-*.md is mentioned in the matching test-*.md
  6. RESULTS:    Every ### ID in nf-*.md has a result in the matching test-*.md
  7. CONTENT:    (optional, --fetch) Fetch live Nextflow docs and check for
                 keywords/headings not mentioned in verify files

Usage:
    python3 00-audit.py             # structural + manifest checks only
    python3 00-audit.py --fetch     # also fetch live docs for content audit
    python3 00-audit.py --verbose   # show passing checks too
"""

import argparse
import glob
import os
import re
import sys
import urllib.request
from html.parser import HTMLParser
from collections import defaultdict

VERIFY_DIR = os.path.dirname(os.path.abspath(__file__))

# ── Feature ID extraction ────────────────────────────────────────────

FEATURE_ID_RE = re.compile(r'\b([A-Z]+-[a-zA-Z0-9_][-a-zA-Z0-9_]*)')


def extract_feature_ids_from_headings(text):
    """Extract feature IDs from ### heading lines only."""
    ids = set()
    for m in re.finditer(r'^### (\S+)', text, re.MULTILINE):
        ids.add(m.group(1))
    return ids


def extract_all_feature_ids(text):
    """Extract all feature IDs mentioned anywhere in text."""
    return set(FEATURE_ID_RE.findall(text))


# ── File loading ─────────────────────────────────────────────────────

def load_files(pattern):
    """Load files matching glob pattern, return dict of basename -> content."""
    files = {}
    for path in sorted(glob.glob(os.path.join(VERIFY_DIR, pattern))):
        with open(path) as f:
            files[os.path.basename(path)] = f.read()
    return files


def nf_number(filename):
    """Extract the numeric prefix from an nf- or test- filename."""
    m = re.match(r'(?:nf|test)-(\d+)', filename)
    return m.group(1) if m else None


# ── Structural checks ───────────────────────────────────────────────

def check_structural(nf_files, test_files, manifest_content):
    errors = []
    warnings = []

    # 1. Build map: feature_id -> nf_file
    nf_id_map = {}  # id -> [files]
    for fname, content in nf_files.items():
        for fid in extract_feature_ids_from_headings(content):
            nf_id_map.setdefault(fid, []).append(fname)

    # 2. Build map: feature_id -> test_files that mention it
    test_id_map = defaultdict(set)
    for fname, content in test_files.items():
        for fid in extract_all_feature_ids(content):
            test_id_map[fid].add(fname)

    # 3. Build manifest IDs (explicit) plus "Covered by nf-XXXX" expansions
    manifest_ids = extract_all_feature_ids(
        manifest_content) if manifest_content else set()
    # Expand "Covered by nf-XXXX" lines: treat all IDs in the referenced nf-
    # file as if they appeared in the manifest
    if manifest_content:
        for m in re.finditer(
            r'[Cc]overed by (nf-\d+(?:\s+through\s+nf-\d+)?)', manifest_content
        ):
            ref = m.group(1)
            # Match all nf- files whose number falls in the range
            if 'through' in ref:
                parts = re.findall(r'nf-(\d+)', ref)
                lo, hi = int(parts[0]), int(parts[1])
                for fname in nf_files:
                    num = nf_number(fname)
                    if num and lo <= int(num) <= hi:
                        manifest_ids |= extract_feature_ids_from_headings(
                            nf_files[fname])
            else:
                num = re.search(r'nf-(\d+)', ref).group(1)
                for fname in nf_files:
                    if nf_number(fname) == num:
                        manifest_ids |= extract_feature_ids_from_headings(
                            nf_files[fname])

    # Check 1: nf- IDs not in any test file
    for fid, nf_fnames in sorted(nf_id_map.items()):
        if fid not in test_id_map:
            errors.append(
                f"STRUCTURAL: {fid} (in {', '.join(nf_fnames)}) has no matching test-*.md entry")

    # Check 2: test- IDs not in any nf- file
    all_nf_ids = set(nf_id_map.keys())
    for fid, test_fnames in sorted(test_id_map.items()):
        if fid not in all_nf_ids:
            # Only warn — test files may reference IDs informally
            warnings.append(
                f"STRUCTURAL: {fid} (in {', '.join(test_fnames)}) not defined as ### heading in any nf-*.md")

    # Check 3: manifest IDs not in nf- files
    for fid in sorted(manifest_ids):
        if fid not in all_nf_ids:
            errors.append(
                f"MANIFEST: {fid} in 00-manifest.md but not defined as ### heading in any nf-*.md")

    # Check 4: nf- IDs not in manifest
    for fid in sorted(all_nf_ids):
        if fid not in manifest_ids:
            errors.append(
                f"MANIFEST: {fid} defined in nf-*.md but not mentioned in 00-manifest.md")

    # Check 5: nf- heading IDs should appear in the matching test- file.
    # Test files may use broader/aggregated IDs or prefix-based references.
    # Match by exact ID or by prefix (SYN-int covers SYN-int in nf-).
    for nf_fname, content in nf_files.items():
        num = nf_number(nf_fname)
        if not num:
            continue
        matching_tests = [f for f in test_files if nf_number(f) == num]
        if not matching_tests:
            errors.append(
                f"TEST-SPEC: {nf_fname} has no matching test-{num}*.md file")
            continue
        test_content = "\n".join(test_files[f] for f in matching_tests)
        test_ids = extract_all_feature_ids(test_content)
        for fid in extract_feature_ids_from_headings(content):
            # Exact match
            if fid in test_content:
                continue
            # Prefix match: SYN-list in test covers SYN-list-foo in nf-
            prefix = fid.rsplit('-', 1)[0] if '-' in fid else fid
            if any(tid == prefix or fid.startswith(tid + '-') for tid in test_ids):
                continue
            errors.append(
                f"TEST-SPEC: {fid} (in {nf_fname}) not listed in {', '.join(matching_tests)}")

    # Check: duplicate feature IDs across nf- files
    for fid, fnames in sorted(nf_id_map.items()):
        if len(fnames) > 1:
            warnings.append(
                f"DUPLICATE: {fid} defined as ### heading in multiple files: {', '.join(fnames)}")

    # Check 6: every nf- feature ID has a result line in the matching test file
    valid_classes = {'SUPPORTED', 'GAP', 'UNSUPPORTED', 'FUTURE',
                     'PARSE_ERROR', 'UNKNOWN'}
    result_re = re.compile(r'^- ([A-Z]+-[a-zA-Z0-9_][-a-zA-Z0-9_]*):\s+(\S+)')
    for nf_fname, content in nf_files.items():
        num = nf_number(nf_fname)
        if not num:
            continue
        matching_tests = [f for f in test_files if nf_number(f) == num]
        if not matching_tests:
            continue
        # Extract result IDs from the ## Results section(s)
        result_ids = {}  # id -> classification
        for tf in matching_tests:
            tc = test_files[tf]
            in_results = False
            for line in tc.splitlines():
                if line.strip() == '## Results':
                    in_results = True
                    continue
                if in_results:
                    m = result_re.match(line)
                    if m:
                        result_ids[m.group(1)] = m.group(2)
        nf_ids = extract_feature_ids_from_headings(content)
        # Check each nf- feature has a result
        for fid in sorted(nf_ids):
            if fid not in result_ids:
                errors.append(
                    f"RESULTS: {fid} (in {nf_fname}) has no result in "
                    f"{', '.join(matching_tests)}")
            elif result_ids[fid] not in valid_classes:
                warnings.append(
                    f"RESULTS: {fid} has non-standard classification "
                    f"'{result_ids[fid]}' in {', '.join(matching_tests)}")
        # Check for result IDs not in nf- file
        for fid in sorted(result_ids):
            if fid not in nf_ids:
                warnings.append(
                    f"RESULTS: {fid} has a result in "
                    f"{', '.join(matching_tests)} but no ### heading in {nf_fname}")

    return errors, warnings


# ── Content audit (live fetch) ───────────────────────────────────────

class HeadingExtractor(HTMLParser):
    """Extract heading text from HTML."""

    def __init__(self):
        super().__init__()
        self.headings = []
        self.in_heading = False
        self.current = []
        self.skip = {"script", "style", "nav", "footer"}
        self.skip_depth = 0

    def handle_starttag(self, tag, attrs):
        if tag in self.skip:
            self.skip_depth += 1
        if self.skip_depth <= 0 and tag in ('h1', 'h2', 'h3', 'h4'):
            self.in_heading = True
            self.current = []

    def handle_endtag(self, tag):
        if tag in self.skip:
            self.skip_depth -= 1
        if tag in ('h1', 'h2', 'h3', 'h4') and self.in_heading:
            self.in_heading = False
            text = "".join(self.current).strip()
            if text:
                self.headings.append(text)

    def handle_data(self, data):
        if self.in_heading:
            self.current.append(data)


PAGES = {
    "syntax": "https://nextflow.io/docs/latest/reference/syntax.html",
    "process": "https://nextflow.io/docs/latest/reference/process.html",
    "channel": "https://nextflow.io/docs/latest/reference/channel.html",
    "operator": "https://nextflow.io/docs/latest/reference/operator.html",
    "config": "https://nextflow.io/docs/latest/reference/config.html",
    "stdlib-types": "https://nextflow.io/docs/latest/reference/stdlib-types.html",
    "stdlib-namespaces": "https://nextflow.io/docs/latest/reference/stdlib-namespaces.html",
    "feature-flags": "https://nextflow.io/docs/latest/reference/feature-flags.html",
}


def fetch_headings(url):
    """Fetch a page and extract all headings."""
    req = urllib.request.Request(
        url, headers={"User-Agent": "Mozilla/5.0 audit"})
    with urllib.request.urlopen(req, timeout=30) as r:
        raw = r.read().decode("utf-8", errors="replace")
    parser = HeadingExtractor()
    parser.feed(raw)
    return parser.headings


def check_content(verify_text, manifest_text):
    """Fetch live pages and report headings not referenced in verify files."""
    errors = []
    combined = (verify_text + "\n" + manifest_text).lower()

    for page_name, url in sorted(PAGES.items()):
        print(f"  Fetching {page_name}...", file=sys.stderr)
        try:
            headings = fetch_headings(url)
        except Exception as e:
            errors.append(f"CONTENT: Could not fetch {page_name}: {e}")
            continue

        for heading in headings:
            # Normalise: strip backticks, parens, angle brackets
            clean = re.sub(r'[`<>()]', '', heading).strip().lower()
            # Skip very generic headings
            if len(clean) < 3 or clean in (
                'overview', 'description', 'options', 'example',
                'examples', 'syntax', 'usage', 'see also', 'parameters',
                'deprecated', 'deprecations', 'notes', 'note',
            ):
                continue
            # Check if heading or key words appear in verify text
            words = clean.split()
            key = words[-1] if words else clean
            if key not in combined and clean not in combined:
                errors.append(
                    f"CONTENT[{page_name}]: heading '{heading}' — keyword '{key}' not found in verify files")

    return errors


# ── Main ─────────────────────────────────────────────────────────────

def main():
    parser = argparse.ArgumentParser(description="Audit verify file system")
    parser.add_argument("--fetch", action="store_true",
                        help="Also fetch live Nextflow docs for content audit")
    parser.add_argument("--verbose", action="store_true",
                        help="Show counts and passing checks")
    args = parser.parse_args()

    nf_files = load_files("nf-*.md")
    test_files = load_files("test-*.md")
    impl_files = load_files("impl-*.md")
    manifest_path = os.path.join(VERIFY_DIR, "00-manifest.md")
    manifest_content = ""
    if os.path.exists(manifest_path):
        with open(manifest_path) as f:
            manifest_content = f.read()

    if args.verbose:
        print(
            f"Files: {len(nf_files)} nf-, {len(test_files)} test-, {len(impl_files)} impl-")
        nf_id_count = sum(
            len(extract_feature_ids_from_headings(c)) for c in nf_files.values()
        )
        print(f"Total feature IDs (### headings in nf-): {nf_id_count}")

        # Count results by classification
        result_re = re.compile(
            r'^- [A-Z]+-[a-zA-Z0-9_][-a-zA-Z0-9_]*:\s+(\S+)')
        class_counts = defaultdict(int)
        for content in test_files.values():
            in_results = False
            for line in content.splitlines():
                if line.strip() == '## Results':
                    in_results = True
                    continue
                if in_results:
                    m = result_re.match(line)
                    if m:
                        class_counts[m.group(1)] += 1
        total_results = sum(class_counts.values())
        if total_results:
            print(f"Total results: {total_results}")
            for cls in ['SUPPORTED', 'GAP', 'UNSUPPORTED', 'FUTURE',
                        'PARSE_ERROR', 'UNKNOWN']:
                if class_counts.get(cls):
                    print(f"  {cls}: {class_counts[cls]}")
            others = {k: v for k, v in class_counts.items()
                      if k not in {'SUPPORTED', 'GAP', 'UNSUPPORTED',
                                   'FUTURE', 'PARSE_ERROR', 'UNKNOWN'}}
            for cls, count in sorted(others.items()):
                print(f"  {cls} (non-standard): {count}")
        print()

    # Run structural checks
    errors, warnings = check_structural(nf_files, test_files, manifest_content)

    # Run content checks if requested
    if args.fetch:
        all_verify_text = "\n".join(
            nf_files.values()) + "\n" + "\n".join(test_files.values())
        content_errors = check_content(all_verify_text, manifest_content)
        # Content issues are warnings (may be false positives from fuzzy matching)
        warnings.extend(content_errors)

    # Report
    if errors:
        print(f"\n=== ERRORS ({len(errors)}) ===")
        for e in errors:
            print(f"  ✗ {e}")

    if warnings:
        print(f"\n=== WARNINGS ({len(warnings)}) ===")
        for w in warnings:
            print(f"  ⚠ {w}")

    if not errors and not warnings:
        print("\n*** ALL CLEAR — no issues found ***")
    elif not errors:
        print(f"\n--- {len(warnings)} warnings, 0 errors ---")
    else:
        print(f"\n--- {len(errors)} errors, {len(warnings)} warnings ---")
        sys.exit(1)


if __name__ == "__main__":
    main()
