"""Train, select and export the LightGBM acceptance model.

Training uses simulated orders, optionally mixed with observed ones (see
observed.py). Synthetic metrics measure fit to the simulators' assumptions, not
real acceptance or price effectiveness; observed holdout metrics are reported
alongside them but do not gate selection.
"""

import hashlib
import json
import math
from pathlib import Path
import re
import shutil
import tempfile

from . import acceptance_policy as policy
from .pipeline import baseline_module, canonical, digest, use_tracking, verify_baseline, write_json
from .observed import NAME as OBSERVED
from .sim import agents, dataset, graph
from .sim.records import policy_input

KIND = "acceptance-gbm"
TARGET_ACCEPTANCE = 0.8
TARGET_TOLERANCE = 0.03
ROUNDS = 300
INFERENCE_SOURCE = Path(policy.__file__)
RELEASE_LOCK = Path(__file__).resolve().parents[1] / "release-requirements.lock"
GENERATORS = (graph.NAME, agents.NAME)
SCOPES = {"synthetic": "synthetic-acceptance-only", "mixed": "synthetic-and-observed-acceptance"}
CALIBRATION = {"synthetic": "synthetic-only", "mixed": "synthetic-and-observed"}


def params(seed):
    return {
        "objective": "binary", "learning_rate": 0.05, "num_leaves": 15, "max_depth": 5,
        "min_data_in_leaf": 40, "lambda_l2": 1.0, "monotone_constraints": list(policy.MONOTONE),
        "monotone_constraints_method": "advanced", "deterministic": True, "force_row_wise": True,
        "num_threads": 1, "seed": seed, "verbosity": -1,
    }


def prepare(records, shared, baseline):
    """Feature rows for decided (not censored) orders, built by the exported code."""
    rows = []
    for record in records:
        if record["outcome"] == "cancelled":
            continue  # Censored: nobody had accepted yet, which is not a rejection.
        value = policy_input(record, shared)
        result = baseline.calculate(value)
        context = policy.context_features(value, result.feature_snapshot)
        rows.append({"context": context, "features": policy.feature_row(context, record["offeredCredits"]),
                     "label": 1 if record["outcome"] == "accepted" else 0,
                     "generator": record["generator"], "split": record["split"],
                     "baselineCredits": result.recommended_credits})
    return rows


def balanced_weights(rows, observed_weight=0.0):
    """Observed rows share observed_weight of the total and each present simulator
    an equal part of the rest; mean weight stays 1."""
    counts = {}
    for r in rows:
        counts[r["generator"]] = counts.get(r["generator"], 0) + 1
    share = observed_weight if counts.get(OBSERVED) else 0.0
    present = [name for name in GENERATORS if counts.get(name)]
    total = {name: (1 - share) / len(present) for name in present}
    if share:
        total[OBSERVED] = share
    return [len(rows) * total[r["generator"]] / counts[r["generator"]] for r in rows]


def fit(rows, seed, observed_weight=0.0):
    import lightgbm
    import numpy

    data = lightgbm.Dataset(numpy.array([r["features"] for r in rows], dtype=float),
                            label=[r["label"] for r in rows], weight=balanced_weights(rows, observed_weight),
                            feature_name=list(policy.FEATURES), free_raw_data=True)
    return lightgbm.train(params(seed), data, num_boost_round=ROUNDS)


def predict(booster, matrix):
    import numpy

    return booster.predict(numpy.array(matrix, dtype=float), num_threads=1)


def scores(probabilities, labels, base_rate):
    import numpy

    p = numpy.clip(numpy.asarray(probabilities, dtype=float), 1e-12, 1 - 1e-12)
    y = numpy.asarray(labels, dtype=float)
    log_loss = float(-numpy.mean(y * numpy.log(p) + (1 - y) * numpy.log(1 - p)))
    base = min(max(base_rate, 1e-12), 1 - 1e-12)
    base_loss = float(-numpy.mean(y * math.log(base) + (1 - y) * math.log(1 - base)))
    order = numpy.argsort(p, kind="stable")
    ranks = numpy.empty(len(p))
    ranks[order] = numpy.arange(1, len(p) + 1)
    positives = y.sum()
    negatives = len(y) - positives
    auc = (float((ranks[y == 1].sum() - positives * (positives + 1) / 2) / (positives * negatives))
           if positives and negatives else None)  # Undefined when one class is absent.
    return {"log_loss": log_loss, "base_rate_log_loss": base_loss,
            "brier": float(numpy.mean((p - y) ** 2)), "auc": auc, "rows": len(y)}


def evaluate(booster, train_rows, rows, observed_weight=0.0):
    """Generator-balanced holdout metrics: the mean of per-simulator scores, plus
    separate observed_* scores when the dataset has observed orders."""
    weights = balanced_weights(train_rows, observed_weight)
    base_rate = sum(w * r["label"] for w, r in zip(weights, train_rows)) / sum(weights)
    per = {name: scores(predict(booster, [r["features"] for r in rows if r["generator"] == name]),
                        [r["label"] for r in rows if r["generator"] == name], base_rate)
           for name in GENERATORS}
    metrics = {key: sum(per[name][key] for name in GENERATORS) / len(GENERATORS)
               for key in ("log_loss", "base_rate_log_loss", "brier")}
    defined = [per[name]["auc"] for name in GENERATORS if per[name]["auc"] is not None]
    metrics["auc"] = sum(defined) / len(defined) if defined else None
    for name in GENERATORS:
        for key, value in per[name].items():
            metrics[f"{name}_{key}"] = value
    observed = [r for r in rows if r["generator"] == OBSERVED]
    if observed:
        for key, value in scores(predict(booster, [r["features"] for r in observed]),
                                 [r["label"] for r in observed], base_rate).items():
            metrics[f"{OBSERVED}_{key}"] = value
        metrics[f"{OBSERVED}_actual_acceptance"] = sum(r["label"] for r in observed) / len(observed)
    return metrics


def price_matrix(booster, rows):
    prices = list(range(5, 31))
    matrix = [policy.feature_row(r["context"], price) for r in rows for price in prices]
    flat = predict(booster, matrix)
    return [list(flat[i * len(prices):(i + 1) * len(prices)]) for i in range(len(rows))], prices


def chosen(matrix, prices, penalty, weights):
    total = probability = credits = 0.0
    picks = []
    for row, weight in zip(matrix, weights):
        best = policy.choose(row, prices, penalty)
        picks.append(best)
        total += weight
        probability += weight * row[best]
        credits += weight * prices[best]
    return probability / total, credits / total, picks


def calibrate(matrix, prices, weights, target=TARGET_ACCEPTANCE):
    """Largest-penalty search keeping mean P(accept | chosen price) at or above target."""
    mean = lambda penalty: chosen(matrix, prices, penalty, weights)[0]
    if mean(0.0) < target:
        return 0.0
    low, high = 0.0, 1.0
    while mean(high) >= target and high < 64:
        low, high = high, high * 2
    if mean(high) >= target:
        return high
    for _ in range(40):
        middle = (low + high) / 2
        low, high = (middle, high) if mean(middle) >= target else (low, middle)
    return round(low, 9)


def policy_metrics(booster, rows, penalty=None, observed_weight=0.0):
    matrix, prices = price_matrix(booster, rows)
    weights = balanced_weights(rows, observed_weight)
    violations = sum(1 for row in matrix for a, b in zip(row, row[1:]) if b < a - 1e-12)
    if penalty is None:
        penalty = calibrate(matrix, prices, weights)
    sweep = {f"{p:g}": chosen(matrix, prices, p, weights)[0] for p in (0, 0.1, 0.25, 0.5, 1, 2, 4)}
    probability, credits, picks = chosen(matrix, prices, penalty, weights)
    baseline = sum(w * r["baselineCredits"] for w, r in zip(weights, rows)) / sum(weights)
    return penalty, sweep, {
        "monotonicity_violations": violations, "mean_acceptance_at_chosen": probability,
        "mean_chosen_credits": credits, "mean_baseline_credits": baseline,
        "share_at_minimum": sum(1 for i in picks if i == 0) / len(picks),
        "share_at_maximum": sum(1 for i in picks if i == len(prices) - 1) / len(picks),
    }


def train(work_dir, formula_package, code_commit, dataset_dir, seed=42, tracking_uri=None):
    import lightgbm
    import mlflow

    if not re.fullmatch(r"[a-f0-9]{40}", code_commit):
        raise ValueError("code commit must be a real 40-character SHA")
    work = Path(work_dir).resolve()
    work.mkdir(parents=True, exist_ok=True)
    baseline_manifest = verify_baseline(formula_package)
    baseline = baseline_module(formula_package).load_policy(Path(formula_package))
    records, shared, manifest = dataset.read(dataset_dir)
    provenance = manifest["provenance"]
    observed_weight = manifest.get("observedWeight", 0.0)
    rows = prepare(records, shared, baseline)
    train_rows = [r for r in rows if r["split"] == "train"]
    holdout = [r for r in rows if r["split"] == "validation"]
    required = GENERATORS + ((OBSERVED,) if provenance == "mixed" else ())
    if any(not [r for r in part if r["generator"] == name] for part in (train_rows, holdout) for name in required):
        raise ValueError("every generator needs training and validation rows")
    booster = fit(train_rows, seed, observed_weight)
    metrics = evaluate(booster, train_rows, holdout, observed_weight)
    # Leave-one-generator-out: a model that only fits its own simulator fails here.
    for source, target in ((graph.NAME, agents.NAME), (agents.NAME, graph.NAME)):
        only = [r for r in train_rows if r["generator"] == source]
        cross = scores(predict(fit(only, seed), [r["features"] for r in holdout if r["generator"] == target]),
                       [r["label"] for r in holdout if r["generator"] == target],
                       sum(r["label"] for r in only) / len(only))
        metrics[f"cross_{source}_to_{target}_log_loss"] = cross["log_loss"]
        metrics[f"cross_{source}_to_{target}_auc"] = cross["auc"]
    penalty, sweep, decision = policy_metrics(booster, holdout, observed_weight=observed_weight)
    metrics.update(decision)
    model_text = booster.model_to_string()
    spec = {"schemaVersion": 1, "features": list(policy.FEATURES), "monotone": list(policy.MONOTONE),
            "prices": list(range(5, 31)), "penalty": penalty, "targetAcceptance": TARGET_ACCEPTANCE,
            "datasetId": manifest["datasetId"], "lightgbmVersion": lightgbm.__version__,
            "params": params(seed), "rounds": ROUNDS, "provenance": provenance, "experimental": True}
    use_tracking(work, tracking_uri)
    mlflow.set_experiment("foc-acceptance-gbm")
    tags = {"provenance": provenance, "experimental": "true", "code_commit": code_commit, "kind": KIND}
    with mlflow.start_run(tags=tags) as run:
        run_id = run.info.run_id
        mlflow.log_params({"seed": seed, "dataset_id": manifest["datasetId"], "algorithm": "lightgbm-binary",
                           "rounds": ROUNDS, "features": ",".join(policy.FEATURES), "penalty": penalty,
                           "target_acceptance": TARGET_ACCEPTANCE, "train_rows": len(train_rows),
                           "validation_rows": len(holdout), "lightgbm": lightgbm.__version__,
                           "observed_weight": observed_weight,
                           "generators": ",".join(f"{n}:{v['version']}" for n, v in manifest["generators"].items())})
        mlflow.log_metrics({k: v for k, v in metrics.items() if isinstance(v, (int, float)) and math.isfinite(v)})
        mlflow.log_dict(manifest, "dataset-manifest.json")
        mlflow.log_dict(spec, "acceptance.json")
        mlflow.log_dict(sweep, "penalty-sweep.json")
        run_dir = work / "candidates" / run_id
        run_dir.mkdir(parents=True, exist_ok=False)
        (run_dir / "model.txt").write_text(model_text)
        dataset.copy(dataset_dir, run_dir / "dataset")
        candidate = {
            "schemaVersion": 1, "kind": KIND, "runId": run_id, "codeCommit": code_commit,
            "datasetId": manifest["datasetId"], "spec": spec, "metrics": metrics, "penaltySweep": sweep,
            "modelSha256": hashlib.sha256(model_text.encode()).hexdigest(), "provenance": provenance,
            "inferenceSourceSha256": hashlib.sha256(INFERENCE_SOURCE.read_bytes()).hexdigest(),
            "baselineManifestSha256": digest(baseline_manifest),
        }
        write_json(run_dir / "candidate.json", candidate)
        mlflow.log_artifact(str(run_dir / "model.txt"))
        mlflow.log_artifact(str(run_dir / "candidate.json"))
    return {"runId": run_id, "candidate": str(run_dir / "candidate.json"), "metrics": metrics}


def read_candidate(work_dir, run_id):
    if not re.fullmatch(r"[a-f0-9]{32}", run_id):
        raise ValueError("exact MLflow run ID required")
    run_dir = Path(work_dir) / "candidates" / run_id
    candidate = json.loads((run_dir / "candidate.json").read_text())
    if candidate.get("kind") != KIND or candidate["runId"] != run_id:
        raise ValueError("candidate identity mismatch")
    if hashlib.sha256((run_dir / "model.txt").read_bytes()).hexdigest() != candidate["modelSha256"]:
        raise ValueError("model integrity mismatch")
    if dataset.read(run_dir / "dataset")[2]["datasetId"] != candidate["datasetId"]:
        raise ValueError("dataset integrity mismatch")
    return candidate


def gate_failures(metrics, tolerance=TARGET_TOLERANCE):
    failures = []
    if metrics["monotonicity_violations"] != 0:
        failures.append("acceptance decreases with price")
    if not metrics["log_loss"] < metrics["base_rate_log_loss"]:
        failures.append("holdout log loss is no better than the base rate")
    if abs(metrics["mean_acceptance_at_chosen"] - TARGET_ACCEPTANCE) > tolerance:
        failures.append("chosen prices miss the target acceptance")
    return failures


def select(work_dir, run_id, selection_path, approved_by=None, approval_reason=None):
    """Select an exact run. Production approval is an explicit, attributed decision."""
    candidate = read_candidate(work_dir, run_id)
    failures = gate_failures(candidate["metrics"])
    if failures:
        raise ValueError("candidate failed synthetic gates: " + "; ".join(failures))
    if bool(approved_by) != bool(approval_reason):
        raise ValueError("production approval needs both an approver and a reason")
    selection = {"schemaVersion": 1, "kind": KIND, "runId": run_id, "candidateSha256": digest(candidate),
                 "targetAcceptance": TARGET_ACCEPTANCE, "targetTolerance": TARGET_TOLERANCE,
                 "scope": SCOPES[candidate.get("provenance", "synthetic")],
                 "productionApproved": bool(approved_by)}
    if approved_by:
        selection["approval"] = {"approvedBy": approved_by, "reason": approval_reason}
    with Path(selection_path).open("x") as stream:
        stream.write(canonical(selection) + "\n")
    return selection


def export(work_dir, selection_path, formula_package, output, release_id):
    selection = json.loads(Path(selection_path).read_text())
    candidate = read_candidate(work_dir, selection["runId"])
    if selection.get("kind") != KIND or digest(candidate) != selection["candidateSha256"]:
        raise ValueError("selection no longer matches candidate")
    if gate_failures(candidate["metrics"], selection["targetTolerance"]):
        raise ValueError("selection gates were not met")
    if not re.fullmatch(r"[A-Za-z0-9_.-]{1,128}", release_id):
        raise ValueError("invalid release ID")
    baseline = Path(formula_package)
    baseline_manifest = verify_baseline(baseline)
    if digest(baseline_manifest) != candidate["baselineManifestSha256"]:
        raise ValueError("baseline release changed since training")
    if hashlib.sha256(INFERENCE_SOURCE.read_bytes()).hexdigest() != candidate["inferenceSourceSha256"]:
        raise ValueError("inference/preprocessing implementation changed since training")
    output = Path(output).resolve()
    if output.exists():
        raise FileExistsError("release output already exists; export never overwrites releases")
    output.parent.mkdir(parents=True, exist_ok=True)
    run_dir = Path(work_dir) / "candidates" / candidate["runId"]
    with tempfile.TemporaryDirectory(dir=output.parent) as temporary:
        package = Path(temporary) / "package"
        (package / "policy").mkdir(parents=True)
        (package / "artifacts").mkdir()
        for relative in ("policy/__init__.py", "policy/formula.py", "artifacts/config.json",
                         "artifacts/context.json", "artifacts/warmup.json", "artifacts/foot_traffic.json"):
            shutil.copyfile(baseline / relative, package / relative)
        shutil.copyfile(INFERENCE_SOURCE, package / "policy/acceptance.py")
        shutil.copyfile(run_dir / "model.txt", package / "artifacts/model.txt")
        shutil.copyfile(RELEASE_LOCK, package / "requirements.lock")
        write_json(package / "artifacts/acceptance.json", candidate["spec"])
        write_json(package / "artifacts/selection.json", selection)
        manifest = {
            "releaseId": release_id, "entryPoint": "policy.acceptance:load_policy",
            "adapterMajor": 1, "contextMajor": 1,
            "requiredCapabilities": baseline_manifest["requiredCapabilities"],
            "featureSchemaVersion": policy.FEATURE_SCHEMA_VERSION, "bounds": {"minimum": 5, "maximum": 30},
            "provenance": {"codeCommit": candidate["codeCommit"],
                           "datasetIds": [candidate["datasetId"], *baseline_manifest["provenance"]["datasetIds"]],
                           "modelRuns": [candidate["runId"]], "baselineReleaseId": baseline_manifest["releaseId"],
                           "calibration": CALIBRATION[candidate.get("provenance", "synthetic")]},
            "experimental": True, "productionApproved": selection.get("productionApproved") is True,
            "files": {p.relative_to(package).as_posix(): hashlib.sha256(p.read_bytes()).hexdigest()
                      for p in sorted(package.rglob("*")) if p.is_file()},
        }
        write_json(package / "manifest.json", manifest)
        package.rename(output)
    return manifest


def simulate(output, formula_package, seed, **sizes):
    verify_baseline(formula_package)
    baseline = baseline_module(formula_package).load_policy(Path(formula_package))
    records, shared, manifest = dataset.build(seed, baseline, formula_package, **sizes)
    dataset.write(output, records, shared, manifest)
    return {"datasetId": manifest["datasetId"], "output": str(output), "counts": manifest["counts"]}
