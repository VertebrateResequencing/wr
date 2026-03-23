#!/usr/bin/env python3
"""
Generate and synchronise the Nextflow verify file system.

Commands:
    scaffold  — Fetch NF reference pages, extract headings/DTs, create
                nf-*.md skeleton files with ### Feature-ID entries.
    sync      — Read existing nf-*.md files, regenerate 00-manifest.md and
                test-*.md files.
    all       — Run scaffold then sync.
    coverage  — Fetch NF docs and report headings not claimed by any file.

Usage:
    python3 00-generate.py scaffold   # fetch docs, create nf- skeletons
    python3 00-generate.py sync       # regenerate manifest + test files
    python3 00-generate.py all        # both in sequence
"""

import argparse
import glob
import os
import re
import sys
import textwrap
import urllib.request
from html.parser import HTMLParser

VERIFY_DIR = os.path.dirname(os.path.abspath(__file__))

# ═════════════════════════════════════════════════════════════════════
# Page → file mapping configuration
# ═════════════════════════════════════════════════════════════════════
#
# Each entry maps a Nextflow reference page to one or more nf- output files.
# For pages that produce multiple files (e.g. operator.html → 6 files),
# keyword-based heading_filter routes headings to the right file.
#
# Fields per file:
#   number:         nf- file number (e.g. "0100")
#   slug:           filename slug (e.g. "syntax-basics")
#   title:          H1 title in generated file
#   prefix:         default feature-ID prefix (e.g. "SYN")
#   heading_filter: optional keyword list — only keep headings matching
#   extract_mode:   "headings" (default), "dt", or "dt_sectioned"
#                   - "headings": extract from H2/H3/H4 headings
#                   - "dt": extract from <dt> definition terms (flat)
#                   - "dt_sectioned": extract <dt> items with parent H2
#                     section used to namespace the feature ID
#   section_prefix_map: for dt_sectioned, map H2 section → prefix override
#   manual_features: list of (id, description) to always include

PAGES_CONFIG = [
    {
        "url": "https://nextflow.io/docs/latest/reference/syntax.html",
        "files": [
            {
                "number": "0100", "slug": "syntax-basics",
                "title": "Syntax Basics",
                "prefix": "SYN",
                "heading_filter": [
                    "comment", "shebang", "feature flag",
                    "function", "closure", "enum", "record",
                    "variable declaration", "multi-assign", "scoping",
                    "string", "gstring", "slashy", "multiline",
                    "constructor", "named arg", "index expr",
                    "property expr",
                ],
            },
            {
                "number": "0200", "slug": "types-operators",
                "title": "Types, Literals and Operators",
                "prefix": "SYN",
                "heading_filter": [
                    "number", "integer", "float", "boolean", "null",
                    "list", "map", "range",
                    "arithmetic", "comparison", "logical", "bitwise",
                    "ternary", "elvis", "null-safe", "spread",
                    "regex", "membership", "instanceof", "as ",
                    "compound assign", "precedence", "parenthes",
                    "unary", "binary",
                ],
                "heading_exact": ["Variable"],
            },
            {
                "number": "0300", "slug": "statements",
                "title": "Statements",
                "prefix": "STMT",
                "heading_filter": [
                    "assign", "expression statement",
                    "assert", "if", "else", "return", "throw",
                    "try", "catch", "finally",
                    "for", "while", "switch", "case",
                    "break", "continue",
                ],
            },
        ],
    },
    {
        "url": "https://nextflow.io/docs/latest/reference/stdlib-types.html",
        "files": [
            {
                "number": "0500", "slug": "stdlib-types",
                "title": "Standard Library Types",
                "prefix": "TYPE",
                "extract_mode": "headings",
            },
            {
                "number": "0400", "slug": "groovy-methods",
                "title": "Groovy Methods & Imports",
                "prefix": "METH",
                "extract_mode": "dt_sectioned",
                "manual_features": [
                    ("METH-default-imports",
                     "Default import packages (groovy.lang.*, java.io.*, etc.)"),
                ],
            },
        ],
    },
    {
        "url": "https://nextflow.io/docs/latest/reference/stdlib-groovy.html",
        "files": [
            {
                "number": "0450", "slug": "groovy-imports",
                "title": "Groovy & Java Imports",
                "prefix": "METH",
                "extract_mode": "li",
                "li_filter": r'^(?:java|groovy)\.',
            },
        ],
    },
    {
        "url": "https://nextflow.io/docs/latest/reference/stdlib-namespaces.html",
        "files": [
            {
                "number": "4500", "slug": "builtins",
                "title": "Built-in Variables, Functions and Namespaces",
                "prefix": "BV",
                "extract_mode": "dt_sectioned",
                "section_prefix_map": {
                    "Global namespace": ("BV", ""),
                    "channel": ("BV", "channel"),
                    "log": ("BV", "log"),
                    "nextflow": ("BV", "nextflow"),
                    "workflow": ("BV", "workflow"),
                },
            },
        ],
    },
    {
        "url": "https://nextflow.io/docs/latest/reference/process.html",
        "files": [
            {
                "number": "1100", "slug": "process-sections",
                "title": "Process Sections",
                "prefix": "PSEC",
                "heading_exact": ["Inputs and outputs (legacy)",
                                  "Inputs", "Generic options",
                                  "Directives"],
                "manual_features": [
                    ("PSEC-script",
                     "script: section of a process definition"),
                    ("PSEC-exec",
                     "exec: section of a process definition"),
                    ("PSEC-stub",
                     "stub: section of a process definition"),
                ],
            },
            {
                "number": "1200", "slug": "input-qualifiers",
                "title": "Input Qualifiers & Options",
                "prefix": "INP",
                "extract_mode": "dt_sectioned",
                "section_prefix_map": {
                    "Inputs and outputs (legacy)": ("INP", ""),
                },
                "section_h3_filter": {"Inputs"},
            },
            {
                "number": "1300", "slug": "output-qualifiers",
                "title": "Output Qualifiers & Options",
                "prefix": "OUT",
                "extract_mode": "dt_sectioned",
                "section_prefix_map": {
                    "Inputs and outputs (legacy)": ("OUT", ""),
                },
                "section_h3_filter": {"Outputs"},
            },
            {
                "number": "1400", "slug": "dir-resources",
                "title": "Directives: Resources",
                "prefix": "DIR",
                "heading_exact": ["accelerator", "cpus", "disk",
                                  "memory", "time", "resourceLimits",
                                  "resourceLabels", "machineType"],
            },
            {
                "number": "1410", "slug": "dir-execution",
                "title": "Directives: Execution",
                "prefix": "DIR",
                "heading_exact": ["errorStrategy", "maxErrors",
                                  "maxRetries", "maxForks",
                                  "maxSubmitAwait", "cache", "fair",
                                  "storeDir", "scratch", "executor"],
            },
            {
                "number": "1420", "slug": "dir-environment",
                "title": "Directives: Environment",
                "prefix": "DIR",
                "heading_exact": ["container", "containerOptions",
                                  "conda", "spack", "module",
                                  "beforeScript", "afterScript",
                                  "env"],
            },
            {
                "number": "1430", "slug": "dir-publishing",
                "title": "Directives: Publishing",
                "prefix": "DIR",
                "heading_exact": ["publishDir", "stageInMode",
                                  "stageOutMode"],
            },
            {
                "number": "1440", "slug": "dir-scheduler",
                "title": "Directives: Scheduler",
                "prefix": "DIR",
                "heading_exact": ["clusterOptions", "queue", "penv",
                                  "pod", "arch"],
            },
            {
                "number": "1450", "slug": "dir-other",
                "title": "Directives: Other",
                "prefix": "DIR",
                "heading_exact": ["debug", "echo", "tag", "label",
                                  "ext", "shell", "array", "secret"],
                "heading_filter": ["dynamic"],
            },
            {
                "number": "1600", "slug": "typed-process",
                "title": "Typed Process (Preview)",
                "prefix": "PSEC",
                "heading_exact": ["Inputs and outputs (typed)",
                                  "Stage directives", "Outputs"],
            },
        ],
    },
    {
        "url": "https://nextflow.io/docs/latest/reference/channel.html",
        "files": [
            {
                "number": "2500", "slug": "channel-factories",
                "title": "Channel Factories",
                "prefix": "CF",
            },
        ],
    },
    {
        "url": "https://nextflow.io/docs/latest/reference/operator.html",
        "files": [
            {
                "number": "3000", "slug": "operators-filtering",
                "title": "Operators: Filtering",
                "prefix": "CO",
                "heading_exact": ["filter", "first", "last", "take",
                                  "until", "unique", "distinct",
                                  "randomSample"],
            },
            {
                "number": "3100", "slug": "operators-transforming",
                "title": "Operators: Transforming",
                "prefix": "CO",
                "heading_exact": ["map", "flatMap", "flatten",
                                  "collect", "groupTuple",
                                  "toList", "toSortedList",
                                  "transpose", "buffer", "collate",
                                  "reduce", "sum", "count",
                                  "min", "max", "toInteger",
                                  "toLong", "toFloat", "toDouble"],
            },
            {
                "number": "3200", "slug": "operators-combining",
                "title": "Operators: Combining",
                "prefix": "CO",
                "heading_exact": ["join", "combine", "cross",
                                  "merge", "mix", "concat",
                                  "collectFile"],
            },
            {
                "number": "3300", "slug": "operators-splitting",
                "title": "Operators: Splitting",
                "prefix": "CO",
                "heading_exact": ["splitCsv", "splitFasta",
                                  "splitFastq", "splitJson",
                                  "splitText",
                                  "countFasta", "countFastq",
                                  "countJson", "countLines"],
            },
            {
                "number": "3400", "slug": "operators-forking",
                "title": "Operators: Forking",
                "prefix": "CO",
                "heading_exact": ["branch", "multiMap", "tap",
                                  "set", "ifEmpty"],
            },
            {
                "number": "3500", "slug": "operators-other",
                "title": "Operators: Other",
                "prefix": "CO",
                "heading_exact": ["view", "dump", "subscribe"],
            },
        ],
    },
    {
        "url": "https://nextflow.io/docs/latest/reference/config.html",
        "files": [
            {
                "number": "4000", "slug": "config",
                "title": "Configuration",
                "prefix": "CFG",
            },
        ],
    },
    {
        "url": "https://nextflow.io/docs/latest/reference/feature-flags.html",
        "files": [
            {
                "number": "4100", "slug": "feature-flags",
                "title": "Feature Flags",
                "prefix": "CFG",
                "extract_mode": "dt",
            },
        ],
    },
]

# Files sourced from a page already in PAGES_CONFIG but routed differently
EXTRA_FILES = [
    {
        "number": "1000", "slug": "includes-params",
        "title": "Includes & Parameters",
        "prefix": "INCL",
        "source_url": "https://nextflow.io/docs/latest/reference/syntax.html",
        "heading_filter": ["include", "param", "addParams"],
    },
    {
        "number": "1500", "slug": "dynamic-directives",
        "title": "Dynamic Directives & Task Properties",
        "prefix": "BV",
        "source_url": "https://nextflow.io/docs/latest/reference/process.html",
        "extract_mode": "dt_sectioned",
        "section_prefix_map": {
            "Task properties": ("BV", "task"),
        },
    },
    {
        "number": "2000", "slug": "workflow",
        "title": "Workflow",
        "prefix": "WF",
        "source_url": "https://nextflow.io/docs/latest/reference/syntax.html",
        "heading_filter": ["workflow", "entry", "named", "take",
                           "main", "emit", "publish", "output"],
    },
    {
        "number": "0350", "slug": "deprecations",
        "title": "Deprecations",
        "prefix": "SYN",
        "source_url": "https://nextflow.io/docs/latest/reference/syntax.html",
        "heading_filter": ["deprecat"],
        "manual_features": [
            ("SYN-deprecated-addParams",
             "addParams and params clauses of include declarations"),
            ("SYN-deprecated-when-section",
             "when: section of a process definition"),
            ("SYN-deprecated-shell-section",
             "shell: section of a process definition"),
        ],
    },
]


# ═════════════════════════════════════════════════════════════════════
# HTML content extraction
# ═════════════════════════════════════════════════════════════════════

class PageContentExtractor(HTMLParser):
    """Extract headings, <dt>, and <li> items from HTML.

    Returns:
        headings: [(level, text)]
        dt_items: [(parent_h2_text, parent_h3_text, dt_text)]
        li_items: [(parent_h2_text, li_text)]
    """

    def __init__(self):
        super().__init__()
        self.headings = []       # [(level, text)]
        self.dt_items = []       # [(parent_h2, parent_h3, text)]
        self.li_items = []       # [(parent_h2, text)]
        self._in_tag = None      # current tag being captured
        self._tag_level = 0      # heading level if in h tag
        self._cur = []           # text accumulator
        self._parent_h2 = ""     # most recent H2 text
        self._parent_h3 = ""     # most recent H3 text
        self._skip = {"script", "style", "nav", "footer"}
        self._skip_depth = 0
        self._dl_depth = 0       # <dl> nesting depth
        self._dt_at_depth = {}   # depth → most recent DT name

    def handle_starttag(self, tag, attrs):
        if tag in self._skip:
            self._skip_depth += 1
        if self._skip_depth > 0:
            return
        if tag == "dl":
            self._dl_depth += 1
        m = re.match(r'^h([1-6])$', tag)
        if m:
            self._in_tag = "heading"
            self._tag_level = int(m.group(1))
            self._cur = []
        elif tag == "dt":
            self._in_tag = "dt"
            self._cur = []
        elif tag == "li":
            self._in_tag = "li"
            self._cur = []

    def handle_endtag(self, tag):
        if tag in self._skip:
            self._skip_depth -= 1
        if tag == "dl" and self._skip_depth == 0:
            for d in list(self._dt_at_depth):
                if d > self._dl_depth:
                    del self._dt_at_depth[d]
            self._dl_depth = max(0, self._dl_depth - 1)
        if self._in_tag == "heading" and re.match(r'^h[1-6]$', tag):
            text = "".join(self._cur).strip()
            if text:
                self.headings.append((self._tag_level, text))
                if self._tag_level == 2:
                    self._parent_h2 = text
                    self._parent_h3 = ""
                elif self._tag_level == 3:
                    self._parent_h3 = text
            self._in_tag = None
        elif self._in_tag == "dt" and tag == "dt":
            text = "".join(self._cur).strip()
            if text:
                # Track this DT's name at current depth for nesting
                name_m = re.match(r'([a-zA-Z_]\w*)', text)
                if name_m:
                    self._dt_at_depth[self._dl_depth] = name_m.group(1)
                # Prefix nested DTs with parent name
                if self._dl_depth > 1:
                    parent = self._dt_at_depth.get(
                        self._dl_depth - 1, '')
                    if parent:
                        text = f"{parent}.{text}"
                self.dt_items.append(
                    (self._parent_h2, self._parent_h3, text))
            self._in_tag = None
        elif self._in_tag == "li" and tag == "li":
            text = "".join(self._cur).strip()
            if text:
                self.li_items.append((self._parent_h2, text))
            self._in_tag = None

    def handle_data(self, data):
        if self._in_tag:
            # Strip anchor-link icons (U+F0C1) and other non-ASCII cruft
            self._cur.append(re.sub(r'[^\x00-\x7f]', '', data))


def fetch_page_content(url):
    """Fetch a page and return (headings, dt_items, li_items)."""
    req = urllib.request.Request(
        url, headers={"User-Agent": "Mozilla/5.0 audit"})
    with urllib.request.urlopen(req, timeout=30) as r:
        raw = r.read().decode("utf-8", errors="replace")
    parser = PageContentExtractor()
    parser.feed(raw)
    return parser.headings, parser.dt_items, parser.li_items


# ═════════════════════════════════════════════════════════════════════
# Feature ID generation
# ═════════════════════════════════════════════════════════════════════

def heading_to_feature_id(heading_text, prefix):
    """Convert a heading to PREFIX-kebab-case feature ID.

    Examples:
        'splitCsv'       → PREFIX-splitCsv  (camelCase preserved)
        'Channel.fromPath' → PREFIX-fromPath
        'Error strategy' → PREFIX-error-strategy
        'stageInMode'    → PREFIX-stageInMode
    """
    # Strip generic type params as a unit first: Bag<E> → Bag
    clean = re.sub(r'<[^>]*>', '', heading_text)
    # Then strip remaining special chars
    clean = re.sub(r'[`()\[\]]', '', clean).strip()

    # If it's already camelCase or a single known word, use as-is
    if re.match(r'^[a-z][a-zA-Z0-9]*$', clean):
        return f"{prefix}-{clean}"

    # If it's a dotted name like 'Channel.fromPath', use last part
    if '.' in clean:
        parts = clean.split('.')
        last = parts[-1]
        if re.match(r'^[a-zA-Z][a-zA-Z0-9]*$', last):
            return f"{prefix}-{last}"

    # Convert multi-word to kebab-case: 'Error strategy' → 'error-strategy'
    words = re.split(r'[\s_]+', clean)
    if len(words) > 1:
        slug = '-'.join(w.lower() for w in words if w)
        slug = re.sub(r'[^a-zA-Z0-9-]', '', slug)
        return f"{prefix}-{slug}"

    # Single word — lowercase
    slug = re.sub(r'[^a-zA-Z0-9-]', '', clean).lower()
    if slug:
        return f"{prefix}-{slug}"

    return None


# Operator symbols to slug names
_OP_MAP = {
    "+": "plus", "-": "minus", "*": "multiply", "/": "divide",
    "%": "mod", "[]": "getAt", "in": "in", "!in": "notIn",
    "in, !in": "membership", "<<": "leftShift", ">>": "rightShift",
    "**": "power", "<=>": "compareTo", "==": "equals",
    "!=": "notEquals", "~": "bitwiseNegate",
    "=~": "find", "==~": "match",
}


def li_to_feature_id(li_text, prefix):
    """Convert a <li> import package name to a feature ID.

    'java.io.*'           → PREFIX-java-io
    'java.math.BigDecimal' → PREFIX-java-math-BigDecimal
    """
    clean = li_text.strip()
    clean = re.sub(r'\.\*$', '', clean)  # Strip wildcard
    name = clean.replace('.', '-')
    name = re.sub(r'[^a-zA-Z0-9-]', '', name)
    if name:
        return f"{prefix}-{name}"
    return None


def dt_to_feature_id(dt_text, prefix, section_slug=""):
    """Convert a <dt> definition term to a feature ID.

    dt_text is a method signature like:
        'size() -> Integer'
        '+ : (Bag<E>, Bag<E>) -> Bag<E>'
        'baseDir: Path'
        'nextflow.enable.strict'

    section_slug provides namespace, e.g. 'list', 'workflow'.
    """
    clean = dt_text.strip()

    # Feature flag style: 'nextflow.enable.strict'
    if clean.startswith("nextflow."):
        parts = clean.split(".")
        # Take last 2 parts: 'enable.strict' → 'enable-strict'
        slug = "-".join(parts[-2:]) if len(parts) >= 3 else parts[-1]
        slug = re.sub(r'[^a-zA-Z0-9-]', '', slug)
        return f"{prefix}-{slug}"

    # Dotted property: 'task.attempt', 'fusion.enabled: boolean'
    dotted_match = re.match(r'^([a-zA-Z_]\w+(?:\.[a-zA-Z_]\w+)+)', clean)
    if dotted_match:
        dotted = dotted_match.group(1)
        name = dotted.replace('.', '-')
        if section_slug and name.startswith(section_slug + '-'):
            return f"{prefix}-{name}"
        elif section_slug:
            return f"{prefix}-{section_slug}-{name}"
        return f"{prefix}-{name}"

    # Operator style: '+ : (...) -> ...' or '~ : ...'
    op_match = re.match(r'^([+\-*/\[\]<>=!%&|~,\s]+)\s*:', clean)
    # Also match word-based operators: 'in, !in : ...'
    if not op_match:
        op_match = re.match(r'^((?:!?in)(?:,\s*!?in)*)\s*:', clean)
    if op_match:
        op = op_match.group(1).strip()
        slug = _OP_MAP.get(op, op.replace(" ", ""))
        slug = re.sub(r'[^a-zA-Z0-9]', '', slug)
        if slug:
            if section_slug:
                return f"{prefix}-{section_slug}-{slug}"
            return f"{prefix}-{slug}"

    # Method style: 'name(...)' or 'name( ... ) -> ...'
    method_match = re.match(r'^([a-zA-Z_][a-zA-Z0-9_]*)\s*\(', clean)
    if method_match:
        name = method_match.group(1)
        if section_slug:
            return f"{prefix}-{section_slug}-{name}"
        return f"{prefix}-{name}"

    # Property style: 'name: Type' or 'name'
    prop_match = re.match(r'^([a-zA-Z_][a-zA-Z0-9_]*)\s*(?::|$)', clean)
    if prop_match:
        name = prop_match.group(1)
        if section_slug:
            return f"{prefix}-{section_slug}-{name}"
        return f"{prefix}-{name}"

    return None


def clean_section_slug(h2_text):
    """Convert an H2 section name to a lowercase slug for namespacing.

    'Iterable<E>' → 'iterable'
    'List<E>'     → 'list'
    'Global namespace' → ''  (special case)
    'workflow'    → 'workflow'
    """
    if not h2_text or h2_text.lower() == "global namespace":
        return ""
    # Remove generic type params
    clean = re.sub(r'<[^>]*>', '', h2_text).strip()
    # Take first word, lowercase
    words = clean.split()
    if words:
        return words[0].lower()
    return ""


GENERIC_HEADINGS = {
    'overview', 'description', 'options', 'example', 'examples',
    'syntax', 'usage', 'see also', 'parameters',
    'notes', 'note', 'contents', 'table of contents',
    'top', 'navigation', 'search', 'next', 'previous', 'reference',
    # Structural parent headings (contain features, not features themselves)
    'script declarations', 'statements', 'expressions',
}


def is_generic_heading(text):
    """Return True if heading is too generic to be a feature."""
    return text.strip().lower() in GENERIC_HEADINGS or len(text.strip()) < 2


def heading_matches_filter(heading_text, heading_filter):
    """Check if heading matches any keyword in the filter list (substring)."""
    lower = heading_text.lower()
    clean = re.sub(r'[`<>()\[\].]', ' ', lower)
    for kw in heading_filter:
        if kw.lower() in clean:
            return True
    return False


def heading_matches_exact(heading_text, exact_list):
    """Check if heading text exactly matches any item in the list."""
    lower = heading_text.lower().strip()
    for name in exact_list:
        if name.lower().strip() == lower:
            return True
    return False


# ═════════════════════════════════════════════════════════════════════
# Scaffold command
# ═════════════════════════════════════════════════════════════════════

def cmd_scaffold(args):
    """Fetch NF docs and create nf-*.md skeleton files."""
    # Collect all page URLs we need to fetch (deduplicate)
    urls_needed = set()
    for pc in PAGES_CONFIG:
        urls_needed.add(pc["url"])
    for ef in EXTRA_FILES:
        urls_needed.add(ef["source_url"])

    # Fetch all pages
    page_data = {}  # url → (headings, dt_items, li_items)
    for url in sorted(urls_needed):
        page_name = url.split('/')[-1].replace('.html', '')
        print(f"Fetching {page_name}...", file=sys.stderr)
        try:
            page_data[url] = fetch_page_content(url)
        except Exception as e:
            print(f"  ERROR fetching {url}: {e}", file=sys.stderr)
            page_data[url] = ([], [], [])

    created = 0

    # Process main page configs
    for pc in PAGES_CONFIG:
        url = pc["url"]
        headings, dt_items, li_items = page_data.get(url, ([], [], []))

        for file_conf in pc["files"]:
            features = extract_features(
                headings, dt_items, li_items, file_conf)
            write_nf_file(file_conf, pc["url"], features, args.force)
            created += 1

    # Process extra files (cross-page references)
    for ef in EXTRA_FILES:
        url = ef["source_url"]
        headings, dt_items, li_items = page_data.get(url, ([], [], []))
        features = extract_features(headings, dt_items, li_items, ef)
        write_nf_file(ef, ef["source_url"], features, args.force)
        created += 1

    print(f"\nScaffolded {created} nf-*.md files.", file=sys.stderr)


def cmd_coverage(_args):
    """Report headings on each page not claimed by any file config."""
    # Build url → list-of-file-configs from both PAGES_CONFIG and EXTRA_FILES
    url_configs = {}  # url → [file_conf, ...]
    for pc in PAGES_CONFIG:
        url_configs.setdefault(pc["url"], []).extend(pc["files"])
    for ef in EXTRA_FILES:
        url_configs.setdefault(ef["source_url"], []).append(ef)

    # Headings covered by a config sourcing a different page (cross-page).
    # Key = page URL, value = set of heading texts (case-insensitive).
    cross_page = {
        "https://nextflow.io/docs/latest/reference/syntax.html": {
            "process", "process (typed)", "output block",
            "script declarations",
        },
    }

    total_unclaimed = 0

    for url in sorted(url_configs):
        page_name = url.split('/')[-1]
        print(f"Fetching {page_name}...", file=sys.stderr)
        try:
            headings, dt_items, _li_items = fetch_page_content(url)
        except Exception as e:
            print(f"  ERROR fetching {url}: {e}", file=sys.stderr)
            continue

        configs = url_configs[url]

        # Collect all section names claimed by heading_exact configs
        # (DTs under those sections are implicitly covered)
        exact_claimed_sections = set()
        for fc in configs:
            for name in fc.get("heading_exact", []):
                exact_claimed_sections.add(name.lower().strip())

        # Collect section names used as dt_sectioned section_prefix_map keys
        # (H2 headings naming those sections are implicitly covered)
        dt_section_names = set()
        for fc in configs:
            for key in fc.get("section_prefix_map", {}):
                dt_section_names.add(key.lower().strip())

        # Known cross-page headings for this URL
        xpage = cross_page.get(url, set())

        # Check heading-mode files: find headings not claimed
        heading_configs = [c for c in configs
                           if c.get("extract_mode", "headings") == "headings"]
        if heading_configs:
            unclaimed = []
            for level, text in headings:
                if level == 1 or is_generic_heading(text):
                    continue
                # H2 headings used as dt_sectioned section names are covered
                if text.lower().strip() in dt_section_names:
                    continue
                # Headings covered by configs on a different page
                if text.lower().strip() in xpage:
                    continue
                claimed = False
                for fc in heading_configs:
                    h_filter = fc.get("heading_filter")
                    h_exact = fc.get("heading_exact")
                    if h_exact and h_filter:
                        if (heading_matches_exact(text, h_exact) or
                                heading_matches_filter(text, h_filter)):
                            claimed = True
                            break
                    elif h_exact:
                        if heading_matches_exact(text, h_exact):
                            claimed = True
                            break
                    elif h_filter:
                        if heading_matches_filter(text, h_filter):
                            claimed = True
                            break
                    else:
                        # No filter at all → claims everything
                        claimed = True
                        break
                if not claimed:
                    unclaimed.append((level, text))
            if unclaimed:
                print(f"\n{page_name}: {len(unclaimed)} unclaimed headings:")
                for lvl, txt in unclaimed:
                    print(f"  H{lvl}: {txt}")
                total_unclaimed += len(unclaimed)

        # Check DT-mode files: find DTs not claimed by any config
        # DTs under sections claimed by heading_exact are considered covered
        dt_configs = [c for c in configs
                      if c.get("extract_mode") in ("dt", "dt_sectioned")]
        if dt_configs or exact_claimed_sections:
            unclaimed_dt = []
            for section_h2, _h3, text in dt_items:
                # Implicitly covered if parent section is heading_exact claimed
                if section_h2.lower().strip() in exact_claimed_sections:
                    continue
                # Covered by cross-page config
                if section_h2.lower().strip() in xpage:
                    continue
                claimed = False
                for fc in dt_configs:
                    mode = fc.get("extract_mode")
                    if mode == "dt":
                        claimed = True
                        break
                    elif mode == "dt_sectioned":
                        smap = fc.get("section_prefix_map", {})
                        if not smap or section_h2 in smap:
                            claimed = True
                            break
                if not claimed:
                    unclaimed_dt.append((section_h2, text))
            if unclaimed_dt:
                sections = sorted(set(s for s, _ in unclaimed_dt))
                print(f"\n{page_name}: {len(unclaimed_dt)} unclaimed <dt> "
                      f"items in sections: {', '.join(sections)}")
                total_unclaimed += len(unclaimed_dt)

    if total_unclaimed == 0:
        print("\n*** ALL CLEAR — every heading/DT is claimed by a file "
              "config ***")
    else:
        print(f"\n*** {total_unclaimed} unclaimed items found ***")
    sys.exit(1 if total_unclaimed > 0 else 0)


def extract_features(headings, dt_items, li_items, file_conf):
    """Extract feature IDs from page content for a specific file config.

    Dispatches based on extract_mode: headings, dt, dt_sectioned, li.
    """
    mode = file_conf.get("extract_mode", "headings")
    features = []
    seen_ids = set()

    # Always add manual features first
    for fid, desc in file_conf.get("manual_features", []):
        if fid not in seen_ids:
            seen_ids.add(fid)
            features.append((fid, desc, 3))

    if mode == "headings":
        features.extend(
            _extract_from_headings(headings, file_conf, seen_ids))
    elif mode == "dt":
        features.extend(
            _extract_from_dt_flat(dt_items, file_conf, seen_ids))
    elif mode == "dt_sectioned":
        features.extend(
            _extract_from_dt_sectioned(dt_items, file_conf, seen_ids))
    elif mode == "li":
        features.extend(
            _extract_from_li(li_items, file_conf, seen_ids))

    return features


def _disambiguate_overload(fid, dt_text):
    """Append parameter count to disambiguate method overloads."""
    paren_idx = dt_text.find('(')
    if paren_idx < 0:
        return None
    close_idx = dt_text.find(')', paren_idx)
    if close_idx <= paren_idx:
        return None
    params = dt_text[paren_idx + 1:close_idx].strip()
    nparams = len([p for p in params.split(',')
                   if p.strip()]) if params else 0
    return f"{fid}-{nparams}"


def _extract_from_headings(headings, file_conf, seen_ids):
    """Extract features from heading elements."""
    prefix = file_conf["prefix"]
    heading_filter = file_conf.get("heading_filter")
    heading_exact = file_conf.get("heading_exact")
    features = []

    for level, text in headings:
        if is_generic_heading(text):
            continue

        # Check filter/exact matching
        if heading_exact and heading_filter:
            # Both: match either
            if not (heading_matches_exact(text, heading_exact) or
                    heading_matches_filter(text, heading_filter)):
                continue
        elif heading_exact:
            if not heading_matches_exact(text, heading_exact):
                continue
        elif heading_filter:
            if not heading_matches_filter(text, heading_filter):
                continue

        # Skip only H1 page titles (not H2 — H2 can be features)
        if level == 1:
            continue

        fid = heading_to_feature_id(text, prefix)
        if fid and fid not in seen_ids:
            seen_ids.add(fid)
            features.append((fid, text, level))

    return features


def _extract_from_dt_flat(dt_items, file_conf, seen_ids):
    """Extract features from <dt> items without section namespacing."""
    prefix = file_conf["prefix"]
    features = []

    for _section, _h3, text in dt_items:
        fid = dt_to_feature_id(text, prefix)
        if fid and fid in seen_ids:
            fid = _disambiguate_overload(fid, text)
        if fid and fid not in seen_ids:
            seen_ids.add(fid)
            features.append((fid, text, 3))

    return features


def _extract_from_dt_sectioned(dt_items, file_conf, seen_ids):
    """Extract features from <dt> items, namespaced by parent H2 section.

    Uses section_prefix_map if provided, otherwise derives section slug
    from the H2 heading text.  When section_prefix_map is present, only
    DTs from listed sections are included (implicit filter).

    section_h3_filter: optional set of H3 names to restrict DTs to.
    """
    prefix = file_conf["prefix"]
    section_map = file_conf.get("section_prefix_map", {})
    h3_filter = file_conf.get("section_h3_filter")
    features = []

    for section_h2, section_h3, text in dt_items:
        # When section_map is set, skip sections not in the map
        if section_map and section_h2 not in section_map:
            continue

        # When h3_filter is set, skip DTs not under a matching H3
        if h3_filter and section_h3 not in h3_filter:
            continue

        # Determine prefix and section slug for this DT
        if section_h2 in section_map:
            dt_prefix, section_slug = section_map[section_h2]
        else:
            dt_prefix = prefix
            section_slug = clean_section_slug(section_h2)

        fid = dt_to_feature_id(text, dt_prefix, section_slug)
        if fid and fid in seen_ids:
            fid = _disambiguate_overload(fid, text)
        if fid and fid not in seen_ids:
            seen_ids.add(fid)
            features.append((fid, text, 3))

    return features


def _extract_from_li(li_items, file_conf, seen_ids):
    """Extract features from <li> items, optionally filtered by regex."""
    prefix = file_conf["prefix"]
    li_filter = file_conf.get("li_filter")
    features = []

    for _section, text in li_items:
        if li_filter and not re.search(li_filter, text):
            continue
        fid = li_to_feature_id(text, prefix)
        if fid and fid not in seen_ids:
            seen_ids.add(fid)
            features.append((fid, text, 3))

    return features


def write_nf_file(file_conf, source_url, features, force=False):
    """Write an nf-*.md skeleton file."""
    number = file_conf["number"]
    slug = file_conf["slug"]
    title = file_conf["title"]
    fname = f"nf-{number}-{slug}.md"
    fpath = os.path.join(VERIFY_DIR, fname)

    if os.path.exists(fpath) and not force:
        print(f"  SKIP {fname} (exists, use --force to overwrite)",
              file=sys.stderr)
        return

    lines = [
        f"# {title}\n",
        f"\n",
        f"**Source:** {source_url}\n",
        f"\n",
        f"## Features\n",
    ]

    for fid, heading_text, level in features:
        lines.append(f"\n### {fid}\n")
        lines.append(
            f"`{heading_text}` — TODO: describe expected behaviour.\n")

    content = "".join(lines)
    with open(fpath, 'w') as f:
        f.write(content)
    print(f"  WROTE {fname} ({len(features)} features)", file=sys.stderr)


# ═════════════════════════════════════════════════════════════════════
# Sync command
# ═════════════════════════════════════════════════════════════════════

# nf-number → impl files mapping
NF_TO_IMPL = {
    "0100": ["impl-01-parse.md", "impl-08-groovy.md"],
    "0200": ["impl-01-parse.md", "impl-08-groovy.md"],
    "0300": ["impl-08-groovy.md"],
    "0400": ["impl-08-groovy.md"],
    "0450": ["impl-08-groovy.md"],
    "0500": ["impl-08-groovy.md"],
    "1000": ["impl-10-modules-params.md", "impl-01-parse.md"],
    "1100": ["impl-01-parse.md", "impl-04-translate-jobs.md"],
    "1200": ["impl-04-translate-jobs.md"],
    "1300": ["impl-04-translate-jobs.md"],
    "1400": ["impl-05-translate-directives.md"],
    "1410": ["impl-05-translate-directives.md"],
    "1420": ["impl-05-translate-directives.md"],
    "1430": ["impl-05-translate-directives.md"],
    "1440": ["impl-05-translate-directives.md"],
    "1450": ["impl-05-translate-directives.md"],
    "1500": ["impl-05-translate-directives.md"],
    "1600": ["impl-04-translate-jobs.md"],
    "2000": ["impl-04-translate-jobs.md", "impl-01-parse.md"],
    "2500": ["impl-03-channel-factories.md"],
    "3000": ["impl-02-channel-operators.md"],
    "3100": ["impl-02-channel-operators.md"],
    "3200": ["impl-02-channel-operators.md"],
    "3300": ["impl-02-channel-operators.md"],
    "3400": ["impl-02-channel-operators.md"],
    "3500": ["impl-02-channel-operators.md"],
    "4000": ["impl-07-config.md"],
    "4100": ["impl-07-config.md"],
    "4500": ["impl-08-groovy.md"],
    "0350": ["impl-01-parse.md"],
}


def cmd_sync(args):
    """Regenerate 00-manifest.md and test-*.md from existing nf-*.md files."""
    # Load all nf- files
    nf_files = {}
    for path in sorted(glob.glob(os.path.join(VERIFY_DIR, "nf-*.md"))):
        fname = os.path.basename(path)
        with open(path) as f:
            nf_files[fname] = f.read()

    if not nf_files:
        print("ERROR: No nf-*.md files found. Run 'scaffold' first.",
              file=sys.stderr)
        sys.exit(1)

    # Generate manifest
    generate_manifest(nf_files)

    # Generate test files
    test_count = 0
    for nf_fname, content in sorted(nf_files.items()):
        generate_test_file(nf_fname, content, args.force)
        test_count += 1

    print(f"\nSynced: 1 manifest + {test_count} test files.",
          file=sys.stderr)


def extract_feature_ids_from_headings(text):
    """Extract (feature_id, heading_line) pairs from ### headings."""
    results = []
    for m in re.finditer(r'^### (\S+)\s*$', text, re.MULTILINE):
        results.append(m.group(1))
    return results


def generate_manifest(nf_files):
    """Generate 00-manifest.md from nf-*.md files."""
    fpath = os.path.join(VERIFY_DIR, "00-manifest.md")

    lines = [
        "# Reference Manifest\n",
        "\n",
        "Auto-generated by `00-generate.py sync`. Do not edit manually.\n",
        "\n",
        "This file lists every feature ID defined in nf-*.md files.\n",
        "Use `00-audit.py` to validate consistency.\n",
        "\n",
    ]

    total = 0
    for nf_fname in sorted(nf_files):
        content = nf_files[nf_fname]
        fids = extract_feature_ids_from_headings(content)

        # Extract title from first # heading
        title_match = re.search(r'^# (.+)$', content, re.MULTILINE)
        title = title_match.group(1) if title_match else nf_fname

        lines.append(f"## {nf_fname} — {title}\n")
        lines.append("\n")
        lines.append("| Feature ID | nf- file |\n")
        lines.append("|------------|----------|\n")

        for fid in fids:
            lines.append(f"| {fid} | {nf_fname} |\n")
            total += 1

        lines.append("\n")

    lines.append(f"**Total: {total} feature IDs across "
                 f"{len(nf_files)} files.**\n")

    with open(fpath, 'w') as f:
        f.writelines(lines)
    print(f"  WROTE 00-manifest.md ({total} feature IDs)", file=sys.stderr)


def nf_number(filename):
    """Extract the numeric prefix from an nf- or test- filename."""
    m = re.match(r'(?:nf|test)-(\d+)', filename)
    return m.group(1) if m else None


def generate_test_file(nf_fname, content, force=False):
    """Generate a test-*.md file from an nf-*.md file."""
    num = nf_number(nf_fname)
    if not num:
        return

    # Build test filename matching the nf- slug
    slug = nf_fname.replace(f"nf-{num}-", "").replace(".md", "")
    test_fname = f"test-{num}-{slug}.md"
    test_path = os.path.join(VERIFY_DIR, test_fname)

    if os.path.exists(test_path) and not force:
        print(f"  SKIP {test_fname} (exists, use --force to overwrite)",
              file=sys.stderr)
        return

    # Extract title
    title_match = re.search(r'^# (.+)$', content, re.MULTILINE)
    title = title_match.group(1) if title_match else slug

    # Extract feature IDs
    fids = extract_feature_ids_from_headings(content)

    # Get impl files
    impl_files = NF_TO_IMPL.get(num, ["(unknown — update NF_TO_IMPL mapping)"])

    lines = [
        f"# Test: {title}\n",
        f"\n",
        f"**Spec files:** {nf_fname}\n",
        f"**Impl files:** {', '.join(impl_files)}\n",
        f"\n",
        f"## Task\n",
        f"\n",
        f"For each feature ID in {nf_fname}, determine its classification.\n",
        f"\n",
        f"### Checklist\n",
        f"\n",
        f"1. Read {nf_fname} to understand what Nextflow expects\n",
        f"2. Read the impl files to find where to look in Go source\n",
        f"3. Read the actual Go source code at the cited locations\n",
        f"4. Classify each feature per 00-instructions.md criteria\n",
        f"\n",
        f"### Features to classify\n",
        f"\n",
    ]

    # Write feature IDs in groups of ~5 per line
    for i in range(0, len(fids), 5):
        chunk = fids[i:i+5]
        prefix = "- " if i == 0 else "  "
        lines.append(f"{prefix}{', '.join(chunk)}\n")

    lines.extend([
        f"\n",
        f"### Output format\n",
        f"\n",
        f"```\n",
    ])
    if fids:
        lines.append(f"{fids[0]}: SUPPORTED | reason\n")
        lines.append(f"...\n")
    lines.append(f"```\n")

    with open(test_path, 'w') as f:
        f.writelines(lines)
    print(f"  WROTE {test_fname} ({len(fids)} features)", file=sys.stderr)


# ═════════════════════════════════════════════════════════════════════
# Main
# ═════════════════════════════════════════════════════════════════════

def main():
    parser = argparse.ArgumentParser(
        description="Generate Nextflow verify file system")
    sub = parser.add_subparsers(dest="command")

    p_scaffold = sub.add_parser("scaffold",
                                help="Fetch NF docs, create nf-*.md skeletons")
    p_scaffold.add_argument("--force", action="store_true",
                            help="Overwrite existing files")

    p_sync = sub.add_parser("sync",
                            help="Regenerate manifest + test files from nf-*.md")
    p_sync.add_argument("--force", action="store_true",
                        help="Overwrite existing files")

    p_all = sub.add_parser("all", help="Run scaffold then sync")
    p_all.add_argument("--force", action="store_true",
                       help="Overwrite existing files")

    sub.add_parser("coverage",
                   help="Report unclaimed headings per page")

    args = parser.parse_args()

    if args.command == "scaffold":
        cmd_scaffold(args)
    elif args.command == "sync":
        cmd_sync(args)
    elif args.command == "all":
        cmd_scaffold(args)
        cmd_sync(args)
    elif args.command == "coverage":
        cmd_coverage(args)
    else:
        parser.print_help()
        sys.exit(1)


if __name__ == "__main__":
    main()
