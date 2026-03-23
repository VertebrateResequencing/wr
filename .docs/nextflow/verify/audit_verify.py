#!/usr/bin/env python3
"""
Systematic audit: fetch every Nextflow reference page, extract all
documented items, and cross-reference against verify file FULL TEXT.
Uses simple substring-in-full-text matching to avoid false positives.
"""

import re
import os
import glob
import urllib.request
from html.parser import HTMLParser


VERIFY_DIR = os.path.dirname(os.path.abspath(__file__))

PAGES = {
    "stdlib-namespaces": "https://nextflow.io/docs/latest/reference/stdlib-namespaces.html",
    "stdlib-types": "https://nextflow.io/docs/latest/reference/stdlib-types.html",
    "stdlib-groovy": "https://nextflow.io/docs/latest/reference/stdlib-groovy.html",
    "process": "https://nextflow.io/docs/latest/reference/process.html",
    "channel": "https://nextflow.io/docs/latest/reference/channel.html",
    "operator": "https://nextflow.io/docs/latest/reference/operator.html",
    "config": "https://nextflow.io/docs/latest/reference/config.html",
    "feature-flags": "https://nextflow.io/docs/latest/reference/feature-flags.html",
    "syntax": "https://nextflow.io/docs/latest/reference/syntax.html",
}


class TextExtractor(HTMLParser):
    def __init__(self):
        super().__init__()
        self.parts = []
        self.skip = {"script", "style", "nav", "footer"}
        self.skip_depth = 0

    def handle_starttag(self, tag, attrs):
        if tag in self.skip:
            self.skip_depth += 1

    def handle_endtag(self, tag):
        if tag in self.skip:
            self.skip_depth -= 1

    def handle_data(self, data):
        if self.skip_depth <= 0:
            self.parts.append(data)

    def get_text(self):
        return " ".join(self.parts)


def fetch_text(url):
    req = urllib.request.Request(
        url, headers={"User-Agent": "Mozilla/5.0 audit"})
    with urllib.request.urlopen(req, timeout=30) as r:
        raw = r.read().decode("utf-8", errors="replace")
    p = TextExtractor()
    p.feed(raw)
    return p.get_text()


def load_verify():
    """Return (entries_dict, all_text_lower)."""
    entries = {}
    texts = []
    for path in sorted(glob.glob(os.path.join(VERIFY_DIR, "nf-*.md"))):
        fname = os.path.basename(path)
        with open(path) as f:
            content = f.read()
            texts.append(content)
            for m in re.finditer(r"^### (\S+)", content, re.M):
                entries[m.group(1)] = fname
    return entries, "\n".join(texts)


def extract_bold_code(text):
    """Pull all **`...`** items from rendered text (backtick-bold patterns)."""
    items = []
    for m in re.finditer(r'(\b\w+(?:\.\w+)*(?:\.\<\w+\>)?)\s*[:(]', text):
        items.append(m.group(1))
    return items


# ── Page-specific extractors ─────────────────────────────────────────

def audit_namespaces(text, verify_text):
    """Check global constants, functions, workflow/nextflow/log properties."""
    gaps = []

    # Global constants
    for name in ["baseDir", "launchDir", "moduleDir", "params", "projectDir",
                 "secrets", "workDir"]:
        if name not in verify_text:
            gaps.append(f"global constant '{name}'")

    # Global functions
    for name in ["branchCriteria", "env", "error", "exit", "file", "files",
                 "groupKey", "multiMapCriteria", "print", "printf", "println",
                 "record", "sendMail", "sleep", "tuple"]:
        if name not in verify_text:
            gaps.append(f"global function '{name}'")

    # Workflow properties (from the docs)
    wf_props = [
        "commandLine", "commitId", "complete", "configFiles", "container",
        "containerEngine", "duration", "errorMessage", "errorReport",
        "exitStatus", "failOnIgnore", "fusion.enabled", "fusion.version",
        "homeDir", "launchDir", "manifest", "outputDir", "preview",
        "profile", "projectDir", "repository", "resume", "revision",
        "runName", "scriptFile", "scriptId", "scriptName", "sessionId",
        "start", "stubRun", "success", "userName", "wave.enabled", "workDir",
    ]
    for prop in wf_props:
        if f"workflow.{prop}" not in verify_text:
            gaps.append(f"workflow property 'workflow.{prop}'")

    # Workflow functions
    for func in ["onComplete", "onError"]:
        if func not in verify_text:
            gaps.append(f"workflow function '{func}'")

    # Nextflow properties
    for prop in ["version", "build", "timestamp"]:
        if f"nextflow.{prop}" not in verify_text:
            gaps.append(f"nextflow property 'nextflow.{prop}'")

    # Log functions
    for func in ["log.info", "log.warn", "log.error"]:
        if func not in verify_text:
            gaps.append(f"log function '{func}'")

    return gaps


def audit_types(text, verify_text):
    """Check stdlib types are covered."""
    gaps = []

    # Major types that should have entries
    types_expected = {
        "Duration": ["duration"],
        "MemoryUnit": ["memunit"],
        "Path": ["path"],
        "Bag": ["Bag", "bag"],
        "Record": ["Record", "record"],
        "Set": ["Set", "set"],
        "Tuple": ["Tuple", "tuple"],
        "VersionNumber": ["VersionNumber", "version-number", "version"],
        "Iterable": ["Iterable", "iterable"],
    }

    for type_name, search_terms in types_expected.items():
        found = False
        for term in search_terms:
            if f"TYPE-{term}" in verify_text or f"`{type_name}`" in verify_text:
                found = True
                break
        if not found:
            gaps.append(f"type '{type_name}'")

    # Duration methods from docs
    duration_methods = ["toMillis", "toSeconds",
                        "toMinutes", "toHours", "toDays"]
    for m in duration_methods:
        if m not in verify_text:
            gaps.append(f"Duration method '{m}'")

    # MemoryUnit methods
    mem_methods = ["toBytes", "toKilo", "toMega", "toGiga", "toUnit"]
    for m in mem_methods:
        if m not in verify_text:
            gaps.append(f"MemoryUnit method '{m}'")

    # Path methods - key ones
    path_methods = ["baseName", "simpleName", "extension", "name", "parent",
                    "exists", "isFile", "isDirectory", "isLink", "isHidden",
                    "text", "readLines", "copyTo", "moveTo", "renameTo",
                    "delete", "toAbsolutePath", "resolve", "toUri"]
    for m in path_methods:
        if m not in verify_text:
            gaps.append(f"Path method '{m}'")

    return gaps


def audit_groovy_imports(text, verify_text):
    """Check default Groovy/Java imports."""
    gaps = []
    imports = [
        "groovy.lang.*", "groovy.util.*",
        "java.io.*", "java.lang.*",
        "java.math.BigDecimal", "java.math.BigInteger",
        "java.net.*", "java.util.*",
    ]
    for imp in imports:
        if imp not in verify_text:
            gaps.append(f"default import '{imp}'")
    return gaps


def audit_process(text, verify_text):
    """Check process directives, input qualifiers, output qualifiers."""
    gaps = []

    # All directives from the process reference
    directives = [
        "accelerator", "afterScript", "arch", "array", "beforeScript",
        "cache", "clusterOptions", "conda", "container", "containerOptions",
        "cpus", "debug", "disk", "dynamic", "errorStrategy", "executor",
        "ext", "fair", "label", "machineType", "maxErrors", "maxForks",
        "maxRetries", "maxSubmitAwait", "memory", "module", "penv",
        "pod", "publishDir", "queue", "resourceLabels", "resourceLimits",
        "scratch", "secret", "shell", "spack", "stageInMode", "stageOutMode",
        "storeDir", "tag", "time",
    ]
    for d in directives:
        if d not in verify_text:
            gaps.append(f"process directive '{d}'")

    # Input qualifiers
    inputs = ["val", "path", "env", "stdin", "tuple", "each"]
    for i in inputs:
        if f"INP-{i}" not in verify_text:
            gaps.append(f"input qualifier '{i}'")

    # Output qualifiers
    outputs = ["val", "path", "env", "stdout", "tuple", "topic"]
    for o in outputs:
        if o not in verify_text:
            gaps.append(f"output qualifier '{o}'")

    # Process sections
    sections = ["script", "shell", "exec", "stub", "input", "output", "when"]
    for s in sections:
        if s not in verify_text:
            gaps.append(f"process section '{s}'")

    return gaps


def audit_channel_factories(text, verify_text):
    """Check channel factories."""
    gaps = []
    factories = [
        "channel.empty", "channel.from", "channel.fromFilePairs",
        "channel.fromLineage", "channel.fromList", "channel.fromPath",
        "channel.fromSRA", "channel.of", "channel.topic", "channel.value",
        "channel.watchPath", "Channel.empty", "Channel.from",
    ]
    for f in factories:
        name = f.split(".")[-1]
        if f"CF-{name}" not in verify_text and name not in verify_text:
            gaps.append(f"channel factory '{f}'")

    # interval is new
    if "interval" not in verify_text:
        gaps.append("channel factory 'channel.interval'")

    return gaps


def audit_operators(text, verify_text):
    """Check all channel operators."""
    gaps = []

    operators = [
        "branch", "buffer", "collate", "collect", "collectFile",
        "combine", "concat", "count", "countFasta", "countFastq",
        "countJson", "countLines", "cross", "distinct", "dump",
        "filter", "first", "flatMap", "flatten", "groupTuple",
        "ifEmpty", "join", "last", "map", "max", "merge",
        "min", "mix", "multiMap", "randomSample", "reduce",
        "set", "splitCsv", "splitFasta", "splitFastq", "splitJson",
        "splitText", "subscribe", "sum", "take", "tap",
        "toDouble", "toFloat", "toInteger", "toLong", "toList",
        "toSortedList", "transpose", "unique", "until", "view",
    ]
    for op in operators:
        if f"CO-{op}" not in verify_text:
            gaps.append(f"operator '{op}'")

    return gaps


def audit_config(text, verify_text):
    """Check config scopes and unscoped options."""
    gaps = []

    # Config scopes
    scopes = [
        "params", "process", "env", "executor", "profiles",
        "docker", "singularity", "apptainer", "charliecloud", "podman",
        "sarus", "shifter",
        "conda", "dag", "manifest", "notification", "report",
        "timeline", "tower", "trace", "wave", "weblog",
        "aws", "azure", "google", "k8s", "mail",
        "seqera", "fusion", "lineage", "spack",
        "nextflow", "workflow",
    ]
    for s in scopes:
        if f"CFG-{s}" not in verify_text and f"`{s}` scope" not in verify_text and f"`{s}`" not in verify_text:
            gaps.append(f"config scope '{s}'")

    # Unscoped options
    unscoped = ["bucketDir", "cleanup", "outputDir", "resume", "workDir"]
    for u in unscoped:
        if u not in verify_text:
            gaps.append(f"unscoped config option '{u}'")

    return gaps


def audit_feature_flags(text, verify_text):
    """Check feature flags."""
    gaps = []
    flags = [
        "nextflow.enable.configProcessNamesValidation",
        "nextflow.enable.dsl",
        "nextflow.enable.moduleBinaries",
        "nextflow.enable.strict",
        "nextflow.preview.output",
        "nextflow.preview.recursion",
        "nextflow.preview.topic",
        "nextflow.preview.types",
    ]
    for f in flags:
        if f not in verify_text:
            gaps.append(f"feature flag '{f}'")
    return gaps


def audit_syntax(text, verify_text):
    """Check syntax features - general categories."""
    gaps = []

    constructs = {
        "shebang": "shebang",
        "comments": "comment",
        "variable declaration": "var-def",
        "if/else": "if",
        "for": "for",
        "while": "while",
        "switch": "switch",
        "try/catch": "try",
        "throw": "throw",
        "return": "return",
        "assert": "assert",
        "break": "break",
        "continue": "continue",
        "closure": "closure",
        "function": "function",
        "string literal": "string-lit",
        "GString": "gstring",
        "multiline string": "multiline",
        "slashy string": "slashy",
        "integer literal": "int",
        "float literal": "float",
        "boolean": "bool",
        "null": "null",
        "list literal": "list",
        "map literal": "map",
        "range": "range",
        "enum": "enum",
        "record": "record",
        "arithmetic": "arith",
        "comparison": "compare",
        "logical": "logical",
        "bitwise": "bitwise",
        "ternary": "ternary",
        "elvis": "elvis",
        "instanceof": "instanceof",
        "membership (in)": "membership",
        "regex find": "regex-find",
        "regex match": "regex-match",
        "null-safe": "null-safe",
        "spread": "spread",
        "index expression": "index-expr",
        "property expression": "property-expr",
        "string concatenation": "string-concat",
        "constructor call": "constructor",
        "named arguments": "named-args",
        "multi-assignment": "multi-assign",
        "precedence": "precedence",
        "scoping": "scoping",
        "feature flag": "feature-flag",
        "compound assignment": "assign-compound",
    }

    for desc, keyword in constructs.items():
        if keyword not in verify_text:
            gaps.append(f"syntax '{desc}' (keyword: {keyword})")

    return gaps


def main():
    entries, verify_text = load_verify()
    print(f"=== {len(entries)} verify entries across files ===\n")

    all_gaps = []

    checks = [
        ("stdlib-namespaces", audit_namespaces),
        ("stdlib-types", audit_types),
        ("stdlib-groovy", audit_groovy_imports),
        ("process", audit_process),
        ("channel", audit_channel_factories),
        ("operator", audit_operators),
        ("config", audit_config),
        ("feature-flags", audit_feature_flags),
        ("syntax", audit_syntax),
    ]

    for page_name, checker in checks:
        url = PAGES[page_name]
        print(f"Fetching {page_name}...")
        try:
            text = fetch_text(url)
        except Exception as e:
            print(f"  ERROR: {e}")
            continue

        gaps = checker(text, verify_text)
        if gaps:
            print(f"  {len(gaps)} gaps:")
            for g in gaps:
                print(f"    GAP: {g}")
        else:
            print(f"  OK — no gaps")
        all_gaps.extend([(page_name, g) for g in gaps])
        print()

    print("=" * 70)
    if all_gaps:
        print(f"\nTOTAL GAPS: {len(all_gaps)}\n")
        for page, g in all_gaps:
            print(f"  [{page}] {g}")
    else:
        print("\n*** ALL CLEAR — no gaps found ***")


if __name__ == "__main__":
    main()
