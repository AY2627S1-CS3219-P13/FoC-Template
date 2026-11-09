import hashlib
import importlib
import re
import sys
from pathlib import Path, PurePosixPath

from .wire import decode_input, json_loads, normalize, validate

CAPABILITIES = {"catalogue.v1", "orders.v1", "routes.v1", "footfall.v1", "coordinates.v1"}


def load_package(directory):
    directory = directory.resolve()
    manifest_path = directory / "manifest.json"
    if manifest_path.is_symlink():
        raise ValueError("manifest symlink")
    manifest = json_loads(manifest_path.read_bytes())
    validate("ReleaseManifest", manifest)
    if not set(manifest["requiredCapabilities"]) <= CAPABILITIES:
        raise ValueError("unsupported capability")
    if manifest["bounds"]["minimum"] > manifest["bounds"]["maximum"]:
        raise ValueError("invalid release bounds")
    names = set()
    for path in directory.rglob("*"):
        if path.is_symlink():
            raise ValueError("package symlink")
        if path.is_file() and path != manifest_path:
            names.add(path.relative_to(directory).as_posix())
    if names != set(manifest["files"]):
        raise ValueError("incomplete manifest")
    for name, expected in manifest["files"].items():
        path = PurePosixPath(name)
        if path.is_absolute() or ".." in path.parts or "\\" in name or str(path) != name:
            raise ValueError("unsafe artifact path")
        if hashlib.sha256((directory / name).read_bytes()).hexdigest() != expected:
            raise ValueError("artifact checksum mismatch")
    for line in (directory / "requirements.lock").read_text().splitlines():
        if line.strip() and not line.startswith("#") and not re.fullmatch(r"[A-Za-z0-9][A-Za-z0-9_.-]*==[A-Za-z0-9][A-Za-z0-9.+!-]*", line.strip()):
            raise ValueError("unlocked dependency")
    static = json_loads((directory / "artifacts/context.json").read_bytes())
    for name, item in (("route", "routes"), ("footfall", "footfall"), ("source", "sources")):
        if not isinstance(static.get(item), list):
            raise ValueError("invalid static context")
        static[item] = [normalize(name, value) for value in static[item]]
    if {value["source"] for value in static["sources"]} != {"routes", "footfall"}:
        raise ValueError("static provenance is required")
    example = decode_input(json_loads((directory / "artifacts/warmup.json").read_bytes()))
    sys.path.insert(0, str(directory))
    module_name, loader_name = manifest["entryPoint"].split(":")
    module = importlib.import_module(module_name)
    # Only trusted code inside the verified package is a valid entry point.
    if not module.__file__ or not Path(module.__file__).resolve().is_relative_to(directory):
        raise ValueError("entry point outside package")
    policy = getattr(module, loader_name)(directory)
    policy.warm_up(example)
    return manifest, policy, static
