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
            'what works and what doesn\'t.\n\n'
            'Each gap is a potential micro-task: read the Nextflow reference '
            '(linked per entry), understand the correct behaviour, then fix '
            'the Go code cited in "Current behaviour".'
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


# ── Fix strategy detection for PARSE_ERROR ───────────────────────────

# Each rule: (predicate, strategy_key, label, instruction)
# Evaluated in order; first match wins.
FIX_STRATEGIES = [
    (
        lambda f: f.id.startswith('BV-task-'),
        'task-placeholder',
        'Add placeholder value to `defaultDirectiveTask()`',
        'In `nextflowdsl/translate.go`, add the missing key to the map '
        'returned by `defaultDirectiveTask()` with a sensible zero/placeholder '
        'value (empty string for strings, 0 for ints). This prevents '
        '"unknown variable" errors when dynamic directives reference the property.',
    ),
    (
        lambda f: f.id.startswith(
            'CFG-enable-') or f.id.startswith('CFG-preview-'),
        'config-nextflow-scope',
        'Add `nextflow` to `skippedTopLevelConfigScopes`',
        'In `nextflowdsl/config.go`, add `"nextflow"` to the '
        '`skippedTopLevelConfigScopes` map. This lets the config parser skip '
        'the entire `nextflow { ... }` block (which contains `enable.*` and '
        '`preview.*` feature flags) instead of erroring on it.',
    ),
    (
        lambda f: (f.id.startswith('CFG-') and
                   'skippedTopLevelConfigScopes' in f.explanation),
        'config-skip-scope',
        'Add scope to `skippedTopLevelConfigScopes`',
        'In `nextflowdsl/config.go`, add the scope name to the '
        '`skippedTopLevelConfigScopes` map so the config parser silently '
        'skips it instead of returning "unsupported config section".',
    ),
    (
        lambda f: f.id.startswith(
            'CFG-') and 'does not dispatch' in f.explanation,
        'config-skip-scope',
        'Add scope to `skippedTopLevelConfigScopes`',
        'In `nextflowdsl/config.go`, add the scope name to the '
        '`skippedTopLevelConfigScopes` map so the config parser silently '
        'skips it instead of erroring.',
    ),
    (
        lambda f: f.id in ('CFG-executor-specific-configuration',
                           'CFG-executor-specific-defaults'),
        'executor-nested-blocks',
        'Skip nested blocks in `parseExecutorBlock`',
        'In `nextflowdsl/config.go`, modify `parseExecutorBlock` to detect '
        'when a value token is `{` (start of nested block) and skip it with '
        '`skipNamedBlock` or brace-counting, rather than only accepting flat '
        'assignments.',
    ),
    (
        lambda f: f.id == 'CFG-unscoped-options',
        'config-unscoped',
        'Handle bare assignments in config parser',
        'In `nextflowdsl/config.go`, modify `configParser.parse` to detect '
        'and skip unrecognised top-level assignments (e.g. `cleanup = true`) '
        'instead of erroring. These are bare config options outside any scope.',
    ),
    (
        lambda f: f.id == 'CFG-workflow',
        'config-skip-scope',
        'Add `workflow` to `skippedTopLevelConfigScopes`',
        'In `nextflowdsl/config.go`, add `"workflow"` to the '
        '`skippedTopLevelConfigScopes` map.',
    ),
    (
        lambda f: f.id in ('WF-recurse', 'WF-times'),
        'channel-operator-list',
        'Add to `supportedChannelOperators`',
        'In `nextflowdsl/parse.go`, add the operator name to the '
        '`supportedChannelOperators` map so it parses as a channel operator '
        'call instead of being rejected.',
    ),
    (
        lambda f: f.id in ('INP-path-name', 'INP-path-stageAs'),
        'input-qualifier',
        'Add qualifier to `applyDeclarationQualifier` / `applyTupleElementQualifier`',
        'In `nextflowdsl/parse.go`, add a case for the qualifier name in both '
        '`applyDeclarationQualifier` and `applyTupleElementQualifier` switch '
        'statements. The qualifier value is a string expression that should be '
        'stored on the Declaration/TupleElement (may need a new field).',
    ),
    (
        lambda f: f.id == 'STMT-multi-assignment',
        'multi-assignment-parse',
        'Parse `def (a, b) = expr` in statement parser',
        'In `nextflowdsl/parse.go`, modify `parseAssignmentStmt` to accept '
        '`def` followed by `(` as a multi-assignment. The evaluator '
        '`evalMultiAssignExpr` already exists — the parser just needs to '
        'produce the right AST node.',
    ),
    (
        lambda f: f.id == 'WF-and',
        'workflow-parallel-operator',
        'Handle `&` (parallel) in workflow statement desugaring',
        'In `nextflowdsl/parse.go`, modify `desugarWorkflowPipe` or '
        '`parseWorkflowStatement` to recognise `BitwiseAndExpr` as a parallel '
        'fork. In Nextflow, `A & B` means run A and B in parallel on the same '
        'input. This needs to produce two separate `Call` entries with the '
        'same input channel.',
    ),
    (
        lambda f: f.id == 'SYN-deprecated-addParams',
        'include-deprecated-clauses',
        'Skip deprecated `addParams`/`params` after include source',
        'In `nextflowdsl/parse.go`, after `parseImport` reads the `from` '
        'source string, check if the next token is an identifier matching '
        '`addParams` or `params`, and if so, skip it plus any following '
        'parenthesised/map argument. Emit a deprecation warning.',
    ),
    (
        lambda f: f.id in ('SYN-deprecated-for-loop',
                           'SYN-deprecated-while-loop'),
        'deprecated-loops',
        'Parse deprecated loop constructs with warning',
        'In `nextflowdsl/parse.go`, add support for `for`/`while` in '
        '`parseWorkflowStatement` or the statement parser. These are '
        'deprecated in Nextflow strict mode. Parse the loop structure '
        '(condition + body), emit a deprecation warning, and either evaluate '
        'the body or skip it gracefully.',
    ),
    (
        lambda f: f.id == 'PSEC-inputs-and-outputs-legacy',
        'legacy-dsl1-sections',
        'Skip legacy DSL1 combined input/output syntax',
        'This is DSL1 syntax which Nextflow itself has removed. Rather than '
        'implementing it, modify the error message in `parseDeclarationLine` '
        'to be more descriptive, or silently skip DSL1 blocks with a warning. '
        'The goal is to not crash when encountering legacy pipelines.',
    ),
]


def detect_fix_strategy(feature):
    """Return (strategy_key, label, instruction) for a PARSE_ERROR feature."""
    for pred, key, label, instruction in FIX_STRATEGIES:
        if pred(feature):
            return key, label, instruction
    return 'unknown', 'Manual investigation needed', feature.explanation


def group_by_fix_strategy(features_list):
    """Group PARSE_ERROR features by fix strategy.
    Returns list of (strategy_key, label, instruction, [features])."""
    groups = {}
    order = []
    for f in features_list:
        key, label, instruction = detect_fix_strategy(f)
        if key not in groups:
            groups[key] = (label, instruction, [])
            order.append(key)
        groups[key][2].append(f)
    return [(k, *groups[k]) for k in order]


# ── Rendering ────────────────────────────────────────────────────────

def render_file(classification, features_list):
    """Render a classification file as markdown."""
    if classification == 'PARSE_ERROR':
        return render_parse_errors(features_list)

    info = CLASSIFICATIONS[classification]
    lines = [
        f'# {info["title"]}',
        '',
        info['description'],
        '',
        f'**Total: {len(features_list)} features**',
        '',
    ]

    show_reference = classification in ('GAP', 'FUTURE', 'UNSUPPORTED')

    for page_title, group in group_features(features_list):
        source_url = group[0].source_url
        lines.append(f'## {page_title}')
        if source_url:
            lines.append(f'Source: {source_url}')
        lines.append('')

        for f in group:
            lines.append(f'### {f.id}')
            lines.append(f'`{f.display}`')
            if show_reference and f.source_url:
                lines.append(f'Reference: {f.source_url}')
            if f.explanation:
                if classification == 'SUPPORTED':
                    lines.append('')
                    lines.append(f.explanation)
                else:
                    lines.append('')
                    lines.append(f'**Current behaviour:** {f.explanation}')
            lines.append('')

    return '\n'.join(lines)


def render_parse_errors(features_list):
    """Render parse_errors.md grouped by fix strategy with actionable instructions."""
    info = CLASSIFICATIONS['PARSE_ERROR']
    lines = [
        f'# {info["title"]}',
        '',
        info['description'],
        '',
        f'**Total: {len(features_list)} features**',
        '',
    ]

    strategy_groups = group_by_fix_strategy(features_list)

    # Summary table
    lines.append('## Fix strategy summary')
    lines.append('')
    lines.append('| Strategy | Features | Fix |')
    lines.append('|----------|----------|-----|')
    for key, label, instruction, group in strategy_groups:
        ids = ', '.join(f.id for f in group)
        lines.append(f'| {label} | {len(group)} | {ids} |')
    lines.append('')

    # Detailed sections per strategy
    for key, label, instruction, group in strategy_groups:
        n = len(group)
        lines.append(f'## {label} ({n} {"feature" if n == 1 else "features"})')
        lines.append('')
        lines.append(f'**Fix:** {instruction}')
        lines.append('')

        for f in group:
            lines.append(f'### {f.id}')
            lines.append(f'`{f.display}`')
            if f.source_url:
                lines.append(f'Reference: {f.source_url}')
            if f.explanation:
                lines.append('')
                lines.append(f'**Current behaviour:** {f.explanation}')
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
