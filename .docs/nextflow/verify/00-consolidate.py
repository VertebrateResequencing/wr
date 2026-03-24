#!/usr/bin/env python3
"""
Consolidate verification results into per-classification markdown files.

Reads all nf-*.md and test-*.md files, extracts feature IDs, display text,
classifications, and explanations, then writes:

  supported.md    — features confirmed working
  gaps.md         — features with incomplete/incorrect behaviour
  unsupported.md  — features that cannot be implemented
  future.md       — features not yet implemented but feasible
  parse_errors.md — features that cause parse errors

Each output file groups features by source page and includes the agent's
explanation, making them directly useful for generating implementation tasks.

Usage:
    python3 00-consolidate.py                  # write to ../ (parent dir)
    python3 00-consolidate.py --outdir ./out   # write to custom dir
    python3 00-consolidate.py --dry-run        # preview counts only
"""

import argparse
import glob
import os
import re
import sys
from collections import defaultdict

VERIFY_DIR = os.path.dirname(os.path.abspath(__file__))

# ── Data structures ──────────────────────────────────────────────────


class Feature:
    __slots__ = ('id', 'display', 'classification', 'explanation',
                 'nf_file', 'source_url', 'page_title')

    def __init__(self, fid, display, classification, explanation,
                 nf_file, source_url, page_title):
        self.id = fid
        self.display = display
        self.classification = classification
        self.explanation = explanation
        self.nf_file = nf_file
        self.source_url = source_url
        self.page_title = page_title

# ── Parsing ──────────────────────────────────────────────────────────


def parse_nf_files():
    """Parse all nf-*.md files. Returns dict: feature_id -> (display, nf_file, source_url, page_title)."""
    features = {}
    for path in sorted(glob.glob(os.path.join(VERIFY_DIR, 'nf-*.md'))):
        fname = os.path.basename(path)
        with open(path) as f:
            content = f.read()

        # Extract page title from first # heading
        title_m = re.search(r'^# (.+)', content, re.MULTILINE)
        page_title = title_m.group(1).strip() if title_m else fname

        # Extract source URL
        url_m = re.search(r'\*\*Source:\*\*\s*(https?://\S+)', content)
        source_url = url_m.group(1) if url_m else ''

        # Extract features: ### ID followed by display text
        for m in re.finditer(
            r'^### (\S+)\n(.+?)(?=\n###|\n## |\Z)',
            content, re.MULTILINE | re.DOTALL
        ):
            fid = m.group(1)
            # Display text: first non-blank line after heading
            body = m.group(2).strip()
            # Format is typically: `Display Text` — description
            display_m = re.match(r'`([^`]+)`', body)
            display = display_m.group(1) if display_m else body.split('\n')[0]
            features[fid] = (display, fname, source_url, page_title)

    return features


def parse_results():
    """Parse ## Results sections from all test-*.md files.
    Returns dict: feature_id -> (classification, explanation)."""
    results = {}
    result_re = re.compile(
        r'^- ([A-Z]+-[a-zA-Z0-9_][-a-zA-Z0-9_]*):\s+(\S+)\s*(?:—\s*(.*))?'
    )

    for path in sorted(glob.glob(os.path.join(VERIFY_DIR, 'test-*.md'))):
        with open(path) as f:
            content = f.read()

        in_results = False
        current_id = None
        current_class = None
        current_explanation = []

        def flush():
            if current_id and current_class:
                expl = ' '.join(current_explanation).strip()
                # Don't overwrite if already seen (first wins)
                if current_id not in results:
                    results[current_id] = (current_class, expl)

        for line in content.splitlines():
            if line.strip() == '## Results':
                in_results = True
                continue
            if not in_results:
                continue
            # New heading ends results section
            if line.startswith('## ') and line.strip() != '## Results':
                flush()
                in_results = False
                break

            m = result_re.match(line)
            if m:
                flush()
                current_id = m.group(1)
                current_class = m.group(2)
                current_explanation = [m.group(3) or '']
            elif current_id and line.startswith('  '):
                # Continuation line
                current_explanation.append(line.strip())

        if in_results:
            flush()

    return results


# ── Output generation ────────────────────────────────────────────────

CLASSIFICATIONS = {
    'SUPPORTED': {
        'filename': 'supported.md',
        'title': 'Supported Features',
        'description': (
            'Nextflow features that are fully implemented in wr\'s '
            'nextflowdsl package. These parse correctly and produce '
            'correct behaviour.'
        ),
    },
    'GAP': {
        'filename': 'gaps.md',
        'title': 'Implementation Gaps',
        'description': (
            'Nextflow features that are partially implemented — they parse '
            'but have incorrect or incomplete behaviour. Each entry describes '
            'what works and what doesn\'t, and can serve as a micro-task spec '
            'for fixing the gap.'
        ),
    },
    'UNSUPPORTED': {
        'filename': 'unsupported.md',
        'title': 'Unsupported Features',
        'description': (
            'Nextflow features that cannot or will not be implemented in wr '
            'due to fundamental architectural differences (e.g. wr runs shell '
            'commands, not JVM code).'
        ),
    },
    'FUTURE': {
        'filename': 'future.md',
        'title': 'Future Features',
        'description': (
            'Nextflow features not currently implemented but feasible to add. '
            'Each entry describes what would need to change. These are '
            'candidates for new implementation tasks.'
        ),
    },
    'PARSE_ERROR': {
        'filename': 'parse_errors.md',
        'title': 'Parse Errors',
        'description': (
            'Nextflow features that cause parse errors when encountered. '
            'These need parser fixes before behaviour can be implemented. '
            'Each entry describes where the parser fails.'
        ),
    },
}


def group_features(features_list):
    """Group features by page_title, preserving order within groups."""
    groups = defaultdict(list)
    seen_titles = []
    for f in features_list:
        key = f.page_title
        if key not in groups:
            seen_titles.append(key)
        groups[key].append(f)
    return [(t, groups[t]) for t in seen_titles]


def render_file(classification, features_list):
    """Render a classification file as markdown."""
    info = CLASSIFICATIONS[classification]
    lines = [
        f'# {info["title"]}',
        '',
        info['description'],
        '',
        f'**Total: {len(features_list)} features**',
        '',
    ]

    for page_title, group in group_features(features_list):
        source_url = group[0].source_url
        lines.append(f'## {page_title}')
        if source_url:
            lines.append(f'Source: {source_url}')
        lines.append('')

        for f in group:
            lines.append(f'### {f.id}')
            lines.append(f'`{f.display}`')
            if f.explanation:
                lines.append('')
                lines.append(f.explanation)
            lines.append('')

    return '\n'.join(lines)


# ── Main ─────────────────────────────────────────────────────────────

def main():
    parser = argparse.ArgumentParser(
        description='Consolidate verification results into per-classification files')
    parser.add_argument('--outdir', default=os.path.join(VERIFY_DIR, '..'),
                        help='Output directory (default: parent of verify/)')
    parser.add_argument('--dry-run', action='store_true',
                        help='Show counts without writing files')
    args = parser.parse_args()

    # Parse source data
    nf_features = parse_nf_files()
    results = parse_results()

    # Merge into Feature objects
    classified = defaultdict(list)
    missing = []

    for fid, (display, nf_file, source_url, page_title) in sorted(nf_features.items()):
        if fid in results:
            cls, explanation = results[fid]
            if cls not in CLASSIFICATIONS:
                print(f"  Warning: {fid} has unknown classification '{cls}', skipping",
                      file=sys.stderr)
                continue
            classified[cls].append(Feature(
                fid, display, cls, explanation, nf_file, source_url, page_title
            ))
        else:
            missing.append(fid)

    # Report
    total = sum(len(v) for v in classified.values())
    print(f'Features: {len(nf_features)} total, {total} classified, '
          f'{len(missing)} missing results')
    print()
    for cls in ['SUPPORTED', 'GAP', 'FUTURE', 'UNSUPPORTED', 'PARSE_ERROR']:
        count = len(classified.get(cls, []))
        if count:
            print(
                f'  {cls:12s}  {count:4d}  -> {CLASSIFICATIONS[cls]["filename"]}')
    print()

    if missing:
        print(f'Missing results ({len(missing)}):')
        for fid in missing[:10]:
            print(f'  {fid}')
        if len(missing) > 10:
            print(f'  ... and {len(missing) - 10} more')
        print()

    if args.dry_run:
        print('(dry run — no files written)')
        return

    # Write output files
    outdir = os.path.abspath(args.outdir)
    os.makedirs(outdir, exist_ok=True)

    for cls, features_list in classified.items():
        if not features_list:
            continue
        info = CLASSIFICATIONS[cls]
        outpath = os.path.join(outdir, info['filename'])
        content = render_file(cls, features_list)
        with open(outpath, 'w') as f:
            f.write(content)
        print(f'  Wrote {outpath} ({len(features_list)} features)')

    print('\nDone.')


if __name__ == '__main__':
    main()
