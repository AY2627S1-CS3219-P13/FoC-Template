# Prediction serving

Planning only. Provide an internal HTTP prediction service whose public contract
is independent of the selected model implementation. See the [overall plan](../README.md)
and [training plan](../mlflow/README.md).

## Architecture and contract

Use a small Python HTTP service (FastAPI is the proposed starting point), with a
model adapter loaded once at startup. Bundle API code, preprocessing, the selected
MLflow model and pinned runtime dependencies in one image. Include the MLflow
runtime if loading through `mlflow.pyfunc`; no tracking server is needed at runtime.

- Define versioned request/response schemas and model input/output signatures.
  Exact endpoints and fields remain undecided; return model/release identity
  with predictions for traceability.
- Adding an optional field is compatible only with a valid missing-value policy
  and older instances that tolerate the field. Required fields or changed
  meanings/units require a coordinated contract migration, potentially `/v2`.
- The adapter validates and transforms API inputs into the model's expected
  features. Accepting a field does not make an older model use it.
- Provide liveness and readiness endpoints. Readiness succeeds only after model
  loading, contract checks and a warm-up prediction succeed.
- Validate configuration at startup; preserve the repository error envelope
  `{"error":{"code","message"}}` and internal bearer-token conventions.
- Run non-root with a read-only filesystem and dropped capabilities; keep the
  service private. Authentication details must follow repository boundaries.

## Release and deployment plan

1. Build locally in containers from an explicitly selected model package. Run
   contract, preprocessing and representative prediction checks inside the image.
2. Push a uniquely versioned image to ECR; record its immutable digest and model,
   data, code and API/schema identities in the release manifest.
3. From the local release script, register a new ECS task definition pinned to
   that digest and update the service. An ECR push alone does not trigger ECS.
4. Use ECS Fargate behind an internal ALB. Proposed starting configuration:
   two tasks, rolling deployment, minimum healthy 100%, maximum 200%, deployment
   circuit breaker and automatic rollback enabled. Provision overlap capacity.
5. Allow for model startup time, send traffic only after readiness passes, and
   drain in-flight requests before terminating old tasks. Both model versions
   may serve requests during rollout and must support the active API contract.
6. Wait for deployment stability and report success/failure. Retain previous task
   definitions and images for explicit rollback. Consider canary deployment later.

## Decisions for the next pass

- Finalize prediction endpoints, payloads, batch behaviour, feature ownership,
  supported schema versions and missing/invalid-input policies.
- Define the caller's timeout/fallback and whether a price quote needs to pin a
  model/release version across retries or a multi-prediction decision.
- Choose AWS region, CPU/memory/architecture, network access, IAM, credentials
  flow, infrastructure tooling and container registry retention.
- Define latency/error alarms, startup/drain timeouts and deployment checks.
  Model quality needs separate monitoring with delayed outcomes; HTTP health
  alone cannot establish prediction quality.
- Specify release serialization so concurrent local releases cannot overwrite
  each other's deployment, plus a tested rollback procedure.
