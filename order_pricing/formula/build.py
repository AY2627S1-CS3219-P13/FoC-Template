"""Build an immutable, checksum-inventoried baseline policy release."""

import argparse
import hashlib
import json
from pathlib import Path
import re
import shutil

from formula.policy.formula import DEFAULT_CONFIG, FEATURE_SCHEMA_VERSION, validate_config, validate_foot_traffic, validate_static_context


def write_json(path, value):
    path.write_text(json.dumps(value, sort_keys=True, indent=2, allow_nan=False) + "\n")


def build(output, code_commit, release_id, *, fixture=None, config=None, foot_traffic=None):
    output = Path(output)
    if output.exists():
        raise ValueError("release output already exists; choose a new directory")
    if not re.fullmatch(r"[a-f0-9]{40}", code_commit):
        raise ValueError("code commit must be a full lowercase Git commit")
    if not release_id or not isinstance(release_id, str):
        raise ValueError("release identity required")
    config = dict(config or DEFAULT_CONFIG)
    validate_config(config)
    root = Path(__file__).resolve().parents[1]
    traffic_path = Path(foot_traffic or Path(__file__).parent / "data/foot_traffic.json")
    traffic = json.loads(traffic_path.read_text())
    validate_foot_traffic(traffic)
    fixture = json.loads(Path(fixture or root / "tests" / "fixtures" / "policy-input.json").read_text())
    context = fixture["context"]
    static = {
        "routes": context["routes"], "footfall": context["footfall"],
        "sources": [s for s in context["sources"] if s["source"] in ("routes", "footfall")],
    }
    validate_static_context(static)
    if any(s["provenance"] != "synthetic" for s in context["sources"]):
        raise ValueError("example builder accepts explicitly synthetic fixtures only")
    output.parent.mkdir(parents=True, exist_ok=True)
    temporary = output.parent / ("." + output.name + ".building")
    if temporary.exists():
        raise ValueError("build staging directory already exists")
    temporary.mkdir()
    try:
        (temporary / "policy").mkdir()
        (temporary / "artifacts").mkdir()
        source = Path(__file__).resolve().parent / "policy"
        for name in ("__init__.py", "formula.py"):
            shutil.copyfile(source / name, temporary / "policy" / name)
        (temporary / "requirements.lock").write_text("")
        write_json(temporary / "artifacts" / "config.json", config)
        write_json(temporary / "artifacts" / "foot_traffic.json", traffic)
        write_json(temporary / "artifacts" / "context.json", static)
        write_json(temporary / "artifacts" / "warmup.json", fixture)
        files = {
            p.relative_to(temporary).as_posix(): hashlib.sha256(p.read_bytes()).hexdigest()
            for p in sorted(temporary.rglob("*")) if p.is_file()
        }
        manifest = {
            "releaseId": release_id, "entryPoint": "policy.formula:load_policy",
            "adapterMajor": 1, "contextMajor": 1,
            "requiredCapabilities": ["catalogue.v1", "orders.v1", "routes.v1", "footfall.v1"],
            "featureSchemaVersion": FEATURE_SCHEMA_VERSION,
            "bounds": {"minimum": 5, "maximum": 30},
            "provenance": {
                "codeCommit": code_commit,
                "datasetIds": ["synthetic-fixture-001", traffic["datasetId"]], "modelRuns": [],
                "sourceCodeSha256": files["policy/formula.py"],
                "calibration": "uncalibrated-policy-assumptions",
            },
            "files": files,
        }
        write_json(temporary / "manifest.json", manifest)
        temporary.rename(output)
    except BaseException:
        shutil.rmtree(temporary)
        raise
    return manifest


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--output", required=True)
    parser.add_argument("--code-commit", required=True)
    parser.add_argument("--release-id", required=True)
    parser.add_argument("--fixture", type=Path)
    parser.add_argument("--config", type=Path)
    parser.add_argument("--foot-traffic", type=Path)
    args = parser.parse_args()
    config = json.loads(args.config.read_text()) if args.config else None
    manifest = build(args.output, args.code_commit, args.release_id, fixture=args.fixture, config=config, foot_traffic=args.foot_traffic)
    print(json.dumps({"releaseId": manifest["releaseId"], "output": str(args.output)}))


if __name__ == "__main__":
    main()
