import argparse
import json
import os
from pathlib import Path

from . import feed, pipeline


def main():
    parser = argparse.ArgumentParser(description="Experimental Pricing MLOps; no automatic production promotion")
    commands = parser.add_subparsers(dest="command", required=True)
    for name in ("train", "demo"):
        p = commands.add_parser(name)
        p.add_argument("--work-dir", required=True)
        p.add_argument("--formula-package", required=True)
        p.add_argument("--code-commit", required=True)
        p.add_argument("--tracking-uri")
        p.add_argument("--seed", type=int, default=42)
        p.add_argument("--count", type=int, default=1000)
        if name == "demo":
            p.add_argument("--output", required=True)
            p.add_argument("--release-id", required=True)
    p = commands.add_parser("select")
    p.add_argument("--work-dir", required=True)
    p.add_argument("--run-id", required=True)
    p.add_argument("--selection", required=True)
    p.add_argument("--maximum-mae", type=float, default=0.05)
    p.add_argument("--minimum-agreement", type=float, default=0.98)
    p = commands.add_parser("export")
    for name in ("work-dir", "selection", "formula-package", "output", "release-id"):
        p.add_argument("--" + name, required=True)
    p = commands.add_parser("consume")
    p.add_argument("--database", required=True)
    p.add_argument("--base-url", required=True)
    p.add_argument("--allow-insecure-local", action="store_true")
    p.add_argument("--page-size", type=int, default=1)
    p.add_argument("--max-response-bytes", type=int, default=64 * 1024 * 1024)
    args = parser.parse_args()
    if args.command in ("train", "demo"):
        result = pipeline.train(args.work_dir, args.formula_package, args.code_commit,
                                args.seed, args.count, args.tracking_uri)
        if args.command == "demo":
            selection = Path(args.work_dir) / (result["runId"] + ".selection.json")
            pipeline.select(args.work_dir, result["runId"], selection)
            manifest = pipeline.export(args.work_dir, selection, args.formula_package,
                                       args.output, args.release_id)
            result.update(selection=str(selection), output=str(args.output), releaseId=manifest["releaseId"])
    elif args.command == "select":
        result = pipeline.select(args.work_dir, args.run_id, args.selection,
                                 args.maximum_mae, args.minimum_agreement)
    elif args.command == "export":
        result = pipeline.export(args.work_dir, args.selection, args.formula_package,
                                 args.output, args.release_id)
    else:
        result = feed.poll(args.database, args.base_url, os.environ.get("PRICING_EXPORT_TOKEN", ""),
                           allow_insecure_local=args.allow_insecure_local, page_size=args.page_size,
                           max_response_bytes=args.max_response_bytes)
    print(json.dumps(result, sort_keys=True))


if __name__ == "__main__":
    main()
