"""Combine the causal-graph and agent-based generators into one versioned dataset."""

import hashlib
import json
from pathlib import Path
import random
import shutil
import tempfile

from . import agents, graph
from .records import shared_context
from .world import World, WORLD_PATH, FOOT_TRAFFIC_PATH

SCHEMA_VERSION = 1


def canonical(value):
    return json.dumps(value, sort_keys=True, separators=(",", ":"), allow_nan=False)


def digest(value):
    return hashlib.sha256(canonical(value).encode()).hexdigest()


def batch_split(batch):
    # Whole parameter batches are held out, so no theta is in both splits.
    return "validation" if batch % 5 == 0 else "train"


def build(seed, baseline, baseline_package, *, graph_rows=12000, graph_batches=20,
          agent_days=60, agent_batches=10):
    if type(seed) is not int:
        raise ValueError("seed must be an integer")
    if graph_batches < 5 or agent_batches < 5 or graph_rows < graph_batches or agent_days < agent_batches:
        raise ValueError("each generator needs at least five batches with data")
    world = World()
    shared = shared_context(world, baseline_package)
    records, thetas = [], {}
    for batch in range(graph_batches):
        rng = random.Random(f"{seed}:{graph.NAME}:{batch}")
        theta, scenario = world.draw_theta(rng), f"g{seed}-{batch}"
        thetas[scenario] = theta
        records += graph.generate(rng, world, theta, baseline, shared, rows=graph_rows // graph_batches,
                                  scenario=scenario, split=batch_split(batch))
    days = agent_days // agent_batches
    for batch in range(agent_batches):
        rng = random.Random(f"{seed}:{agents.NAME}:{batch}")
        theta, scenario = world.draw_theta(rng), f"a{seed}-{batch}"
        thetas[scenario] = theta
        records += agents.generate(rng, world, theta, baseline, shared, days=days, first_day=batch * days,
                                   scenario=scenario, split=batch_split(batch))
    counts = {}
    for record in records:
        key = f"{record['generator']}/{record['split']}/{record['outcome']}"
        counts[key] = counts.get(key, 0) + 1
    manifest = {
        "schemaVersion": SCHEMA_VERSION, "provenance": "synthetic", "seed": seed,
        "generators": {graph.NAME: {"version": graph.VERSION, "rows": graph_rows, "batches": graph_batches},
                       agents.NAME: {"version": agents.VERSION, "days": agent_days, "batches": agent_batches}},
        "worldSha256": hashlib.sha256(WORLD_PATH.read_bytes()).hexdigest(),
        "footTrafficSha256": hashlib.sha256(FOOT_TRAFFIC_PATH.read_bytes()).hexdigest(),
        "sharedContextSha256": digest(shared), "thetas": thetas, "counts": counts,
        "rowsSha256": digest(records), "split": "parameter batch index modulo 5 is validation",
        "labels": "accepted before expiry vs expired; cancelled before acceptance is censored",
        "limitation": "Encodes assumed behaviour; not evidence of real acceptance or price response.",
    }
    manifest["datasetId"] = "synthetic-acceptance-" + digest(manifest)[:32]
    return records, shared, manifest


def write(output, records, shared, manifest):
    output = Path(output).resolve()
    if output.exists():
        raise FileExistsError("dataset output already exists; datasets are never overwritten")
    output.parent.mkdir(parents=True, exist_ok=True)
    with tempfile.TemporaryDirectory(dir=output.parent) as temporary:
        staging = Path(temporary) / "dataset"
        staging.mkdir()
        (staging / "records.json").write_text(canonical(records) + "\n")
        (staging / "context.json").write_text(canonical(shared) + "\n")
        (staging / "manifest.json").write_text(canonical(manifest) + "\n")
        staging.rename(output)
    return output


def read(directory):
    directory = Path(directory)
    manifest = json.loads((directory / "manifest.json").read_text())
    records = json.loads((directory / "records.json").read_text())
    shared = json.loads((directory / "context.json").read_text())
    if digest(records) != manifest["rowsSha256"] or digest(shared) != manifest["sharedContextSha256"]:
        raise ValueError("dataset integrity mismatch")
    identity = dict(manifest)
    del identity["datasetId"]
    if "synthetic-acceptance-" + digest(identity)[:32] != manifest["datasetId"]:
        raise ValueError("dataset identity mismatch")
    return records, shared, manifest


def copy(source, destination):
    shutil.copytree(source, destination)
