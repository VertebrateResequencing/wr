"""Independent JSON Schema evidence: python3 + installed jsonschema, not a Go test prerequisite."""
import copy
import json
from pathlib import Path

from jsonschema import Draft202012Validator

ROOT = Path(__file__).resolve().parent
validators = {}
records = {}
for fixture in sorted((ROOT / "records").glob("*.json")):
    kind = fixture.stem
    schema = json.loads((ROOT.parent / "data" / "schema" / f"{kind}.v1.json").read_text())
    Draft202012Validator.check_schema(schema)
    validators[kind] = Draft202012Validator(schema)
    records[kind] = json.loads(fixture.read_text())
    validators[kind].validate(records[kind])

cases = json.loads((ROOT / "schema-cases.json").read_text())
for case in cases:
    record = copy.deepcopy(records[case["kind"]])
    if "fixture" in case:
        record = json.loads((ROOT / "schema-records" / case["fixture"]).read_text())
    target = record
    for part in case["path"][:-1]:
        target = target[part]
    target[case["path"][-1]] = case["value"]
    errors = list(validators[case["kind"]].iter_errors(record))
    assert (not errors) == case["valid"], (case["name"], [error.message for error in errors])
    print(case["name"], "accepted" if not errors else "rejected")
print(f"PASS: {len(records)} schemas and {len(cases)} independent mutations; relational rules require corpus validation.")
