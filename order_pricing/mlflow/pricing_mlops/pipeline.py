"""Synthetic effort distillation proves the pipeline, not real price effectiveness."""

import hashlib
import importlib.util
import json
import math
import os
from pathlib import Path, PurePosixPath
import random
import re
import shutil
import tempfile

FEATURE_NAMES = ["travelMinutes", "handlingCredits"]


def canonical(value):
    return json.dumps(value, sort_keys=True, separators=(",", ":"), allow_nan=False)


def digest(value):
    return hashlib.sha256(canonical(value).encode()).hexdigest()


def write_json(path, value):
    Path(path).write_text(canonical(value) + "\n")


def use_tracking(work, tracking_uri):
    """Point MLflow at this run's store without leaking it to later runs.

    mlflow.set_tracking_uri also exports MLFLOW_TRACKING_URI, which a later train
    call in the same process would otherwise read as an explicit choice.
    """
    import mlflow

    configured = os.environ.get("MLFLOW_TRACKING_URI")
    mlflow.set_tracking_uri(tracking_uri or configured or (Path(work) / "mlruns").as_uri())
    if configured is None:
        os.environ.pop("MLFLOW_TRACKING_URI", None)
    else:
        os.environ["MLFLOW_TRACKING_URI"] = configured


def baseline_module(package):
    verify_baseline(package)
    spec = importlib.util.spec_from_file_location("_training_formula", Path(package) / "policy/formula.py")
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


def verify_baseline(package):
    package = Path(package).resolve()
    if (package / "manifest.json").is_symlink():
        raise ValueError("baseline symlinks forbidden")
    manifest = json.loads((package / "manifest.json").read_text())
    if manifest["adapterMajor"] != 1 or manifest["contextMajor"] != 1:
        raise ValueError("incompatible baseline")
    for name, checksum in manifest["files"].items():
        relative = PurePosixPath(name)
        if relative.is_absolute() or ".." in relative.parts or "\\" in name:
            raise ValueError("unsafe baseline artifact path")
        path = package / name
        if any(parent.is_symlink() for parent in (path, *path.parents) if parent != package.parent):
            raise ValueError("baseline symlinks forbidden")
        if hashlib.sha256(path.read_bytes()).hexdigest() != checksum:
            raise ValueError("baseline checksum mismatch")
    return manifest


def generate_dataset(seed, count, config):
    if type(seed) is not int or type(count) is not int or count < 100:
        raise ValueError("seed must be integer and dataset must contain at least 100 rows")
    rng = random.Random(seed)
    rows = []
    for index in range(count):
        distance = rng.uniform(100, 4500)
        quantity = rng.choice([None, 1, 2, 3, 4, 6, 10])
        travel = distance / config["walkingMetresPerMinute"]
        handling = 0.0 if quantity is None else (
            min(max(quantity - 1, 0), config["maxExtraItems"]) * config["extraItemCredits"]
        )
        raw = config["baseCredits"] + config["creditsPerHour"] * (
            travel + config["approachMinutes"] + config["pickupMinutes"]
        ) / 60 + handling
        rows.append({
            "scenarioId": f"synthetic-{seed}-{index}", "provenance": "synthetic",
            "walkableMetres": distance, "quantity": quantity,
            "features": [travel, handling], "targetUnroundedCredits": raw,
            "split": "validation" if index % 5 == 0 else "train",
        })
    manifest = {
        "schemaVersion": 1, "generator": "synthetic-effort-1", "seed": seed,
        "count": count, "provenance": "synthetic", "config": dict(config),
        "split": "scenario-index-modulo-5; validation indices never fit",
        "rowsSha256": digest(rows),
        "limitation": "Distils assumed effort policy; no observed price-response labels.",
    }
    manifest["datasetId"] = "synthetic-" + digest(manifest)
    return rows, manifest


def fit(rows):
    # Solve ordinary least squares normal equations for three coefficients.
    training = [r for r in rows if r["split"] == "train"]
    matrix = [[0.0] * 4 for _ in range(3)]
    for row in training:
        x = [1.0, *row["features"]]
        for i in range(3):
            for j in range(3):
                matrix[i][j] += x[i] * x[j]
            matrix[i][3] += x[i] * row["targetUnroundedCredits"]
    for pivot in range(3):
        largest = max(range(pivot, 3), key=lambda i: abs(matrix[i][pivot]))
        matrix[pivot], matrix[largest] = matrix[largest], matrix[pivot]
        divisor = matrix[pivot][pivot]
        if abs(divisor) < 1e-10:
            raise ValueError("rank-deficient synthetic training features")
        matrix[pivot] = [v / divisor for v in matrix[pivot]]
        for i in range(3):
            if i != pivot:
                factor = matrix[i][pivot]
                matrix[i] = [a - factor * b for a, b in zip(matrix[i], matrix[pivot])]
    return [matrix[i][3] for i in range(3)]


def evaluate(model, rows):
    validation = [r for r in rows if r["split"] == "validation"]
    if not validation:
        raise ValueError("validation set is empty")
    errors, price_matches = [], []
    for row in validation:
        predicted = model["intercept"] + sum(a * b for a, b in zip(model["coefficients"], row["features"]))
        target = row["targetUnroundedCredits"]
        errors.append(abs(predicted - target))
        discretize = lambda p: min(30, max(5, math.ceil(round(p, 8))))
        price_matches.append(discretize(predicted) == discretize(target))
    return {"synthetic_mae": sum(errors) / len(errors),
            "synthetic_price_agreement": sum(price_matches) / len(price_matches),
            "validation_rows": len(validation)}


def train(work_dir, formula_package, code_commit, seed=42, count=1000, tracking_uri=None):
    import mlflow

    if not re.fullmatch(r"[a-f0-9]{40}", code_commit):
        raise ValueError("code commit must be a real 40-character SHA")
    work = Path(work_dir).resolve()
    work.mkdir(parents=True, exist_ok=True)
    baseline_manifest = verify_baseline(Path(formula_package))
    formula = baseline_module(formula_package)
    formula.load_config(Path(formula_package))
    config = json.loads((Path(formula_package) / "artifacts/config.json").read_text())
    rows, dataset = generate_dataset(seed, count, config)
    learned = fit(rows)
    model = {"schemaVersion": 1, "features": FEATURE_NAMES, "intercept": learned[0],
             "coefficients": learned[1:], "datasetId": dataset["datasetId"],
             "provenance": "synthetic", "experimental": True}
    metrics = evaluate(model, rows)
    use_tracking(work, tracking_uri)
    mlflow.set_experiment("foc-synthetic-effort")
    with mlflow.start_run(tags={"provenance": "synthetic", "experimental": "true", "code_commit": code_commit}) as run:
        run_id = run.info.run_id
        mlflow.log_params({"seed": seed, "rows": count, "dataset_id": dataset["datasetId"],
                           "algorithm": "ordinary-least-squares", "features": ",".join(FEATURE_NAMES)})
        mlflow.log_metrics(metrics)
        mlflow.log_dict(dataset, "dataset-manifest.json")
        mlflow.log_dict(model, "model.json")
        mlflow.log_dict({"features": FEATURE_NAMES, "example": rows[0]["features"]}, "signature.json")
        candidate = {"schemaVersion": 1, "runId": run_id, "codeCommit": code_commit,
                     "dataset": dataset, "model": model, "metrics": metrics,
                     "modelSha256": digest(model), "provenance": "synthetic",
                     "inferenceSourceSha256": hashlib.sha256(Path(__file__).with_name("model.py").read_bytes()).hexdigest(),
                     "baselineSourceSha256": hashlib.sha256((Path(formula_package) / "policy/formula.py").read_bytes()).hexdigest()}
        candidate["baselineManifestSha256"] = digest(baseline_manifest)
        run_dir = work / "candidates" / run_id
        run_dir.mkdir(parents=True, exist_ok=False)
        write_json(run_dir / "candidate.json", candidate)
        write_json(run_dir / "dataset.json", rows)
        mlflow.log_artifact(str(run_dir / "candidate.json"))
    return {"runId": run_id, "candidate": str(run_dir / "candidate.json"), "metrics": metrics}


def read_candidate(work_dir, run_id):
    if not re.fullmatch(r"[a-f0-9]{32}", run_id):
        raise ValueError("exact MLflow run ID required")
    candidate = json.loads((Path(work_dir) / "candidates" / run_id / "candidate.json").read_text())
    if candidate["runId"] != run_id or digest(candidate["model"]) != candidate["modelSha256"]:
        raise ValueError("candidate identity or model integrity mismatch")
    rows = json.loads((Path(work_dir) / "candidates" / run_id / "dataset.json").read_text())
    if digest(rows) != candidate["dataset"]["rowsSha256"]:
        raise ValueError("dataset integrity mismatch")
    if evaluate(candidate["model"], rows) != candidate["metrics"]:
        raise ValueError("candidate metrics are inconsistent")
    return candidate


def select(work_dir, run_id, selection_path, maximum_mae=0.05, minimum_agreement=0.98):
    candidate = read_candidate(work_dir, run_id)
    metrics = candidate["metrics"]
    if not math.isfinite(maximum_mae) or maximum_mae < 0 or not 0 <= minimum_agreement <= 1:
        raise ValueError("invalid promotion thresholds")
    if metrics["synthetic_mae"] > maximum_mae or metrics["synthetic_price_agreement"] < minimum_agreement:
        raise ValueError("candidate failed explicit synthetic promotion gates")
    selection = {"schemaVersion": 1, "runId": run_id, "candidateSha256": digest(candidate),
                 "maximumSyntheticMae": maximum_mae, "minimumSyntheticPriceAgreement": minimum_agreement,
                 "scope": "experimental-synthetic-only", "productionApproved": False}
    with Path(selection_path).open("x") as stream:
        stream.write(canonical(selection) + "\n")
    return selection


def export(work_dir, selection_path, formula_package, output, release_id):
    selection = json.loads(Path(selection_path).read_text())
    candidate = read_candidate(work_dir, selection["runId"])
    if digest(candidate) != selection["candidateSha256"] or selection.get("scope") != "experimental-synthetic-only":
        raise ValueError("selection no longer matches candidate")
    if (candidate["metrics"]["synthetic_mae"] > selection["maximumSyntheticMae"] or
        candidate["metrics"]["synthetic_price_agreement"] < selection["minimumSyntheticPriceAgreement"]):
        raise ValueError("selection gates were not met")
    if not re.fullmatch(r"[A-Za-z0-9_.-]{1,128}", release_id):
        raise ValueError("invalid release ID")
    baseline = Path(formula_package)
    baseline_manifest = verify_baseline(baseline)
    if digest(baseline_manifest) != candidate.get("baselineManifestSha256"):
        raise ValueError("baseline release assets changed since training")
    if (baseline_manifest["files"]["policy/formula.py"] != candidate["baselineSourceSha256"] or
        hashlib.sha256(Path(__file__).with_name("model.py").read_bytes()).hexdigest() != candidate["inferenceSourceSha256"]):
        raise ValueError("inference/preprocessing implementation changed since training")
    if json.loads((baseline / "artifacts/config.json").read_text()) != candidate["dataset"]["config"]:
        raise ValueError("baseline configuration changed since training")
    output = Path(output).resolve()
    if output.exists():
        raise FileExistsError("release output already exists; export never overwrites releases")
    output.parent.mkdir(parents=True, exist_ok=True)
    with tempfile.TemporaryDirectory(dir=output.parent) as temporary:
        package = Path(temporary) / "package"
        (package / "policy").mkdir(parents=True)
        (package / "artifacts").mkdir()
        for relative in ("policy/__init__.py", "policy/formula.py", "artifacts/config.json",
                         "artifacts/context.json", "artifacts/warmup.json"):
            shutil.copyfile(baseline / relative, package / relative)
        traffic_asset = baseline / "artifacts/foot_traffic.json"
        if traffic_asset.exists():
            shutil.copyfile(traffic_asset, package / "artifacts/foot_traffic.json")
        shutil.copyfile(Path(__file__).with_name("model.py"), package / "policy/model.py")
        (package / "requirements.lock").write_text("")
        write_json(package / "artifacts/model.json", candidate["model"])
        write_json(package / "artifacts/selection.json", selection)
        manifest = {
            "releaseId": release_id, "entryPoint": "policy.model:load_policy",
            "adapterMajor": 1, "contextMajor": 1,
            "requiredCapabilities": baseline_manifest["requiredCapabilities"],
            "featureSchemaVersion": "synthetic-effort-model-2", "bounds": {"minimum": 5, "maximum": 30},
            "provenance": {"codeCommit": candidate["codeCommit"],
                           "datasetIds": [candidate["dataset"]["datasetId"]], "modelRuns": [candidate["runId"]]},
            "experimental": True, "productionApproved": False,
            "files": {p.relative_to(package).as_posix(): hashlib.sha256(p.read_bytes()).hexdigest()
                      for p in sorted(package.rglob("*")) if p.is_file()},
        }
        write_json(package / "manifest.json", manifest)
        package.rename(output)
    return manifest
