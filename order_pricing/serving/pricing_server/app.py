import asyncio
from contextlib import asynccontextmanager
from concurrent.futures import ThreadPoolExecutor
from datetime import timedelta
import hmac
from functools import partial
import time
from uuid import uuid4

from fastapi import FastAPI, Request
from fastapi.responses import JSONResponse
from starlette.exceptions import HTTPException

from pricing_contracts import PolicyUnavailable, UnsupportedRoute
from .config import Settings
from .context import Context, resolve_places
from .events import event_batch
from .package import load_package
from .storage import Storage
from .wire import APIError, decode_input, fingerprint, instant, json_loads, normalize_request, result_json, stamp, utcnow, uuid


def response_error(status, code):
    return JSONResponse({"error": {"code": code, "message": "Request could not be completed."}}, status_code=status,
                        headers={"Retry-After": "1"} if status == 429 else None)


async def read_json(request, maximum):
    if request.headers.get("content-type", "").split(";")[0].strip().lower() != "application/json":
        raise APIError(415, "unsupported_media_type")
    body = bytearray()
    async for chunk in request.stream():
        body.extend(chunk)
        if len(body) > maximum:
            raise APIError(413, "request_too_large")
    try:
        return json_loads(body)
    except (ValueError, UnicodeError):
        raise APIError(400, "invalid_request") from None


def authenticate(request, expected):
    authorization = request.headers.get("authorization", "")
    if not hmac.compare_digest(authorization.encode(), ("Bearer " + expected).encode()):
        raise APIError(401, "invalid_service_credential")


def no_query(request):
    if request.query_params:
        raise APIError(400, "invalid_request")


def create_app(settings=None, *, storage=None, package_loader=load_package):
    @asynccontextmanager
    async def lifespan(app):
        config = settings or Settings.from_env()
        manifest, policy, static = package_loader(config.package_dir)
        context = Context(config, static)
        store = storage or Storage(config.database_url)
        app.state.config, app.state.manifest, app.state.policy = config, manifest, policy
        app.state.context, app.state.storage = context, store
        app.state.database_ready = False
        app.state.stopping = False
        app.state.active = None
        app.state.active_key = None
        app.state.thread_count = 0
        app.state.http_count = 0
        executor = ThreadPoolExecutor(max_workers=8, thread_name_prefix="pricing")
        async def bounded_thread(function, *arguments):
            # Admission is tied to actual worker completion, not request lifetime.
            # A cancelled/timed-out request cannot free a still-running DB slot.
            if app.state.thread_count >= 8:
                raise APIError(429, "rate_limited")
            app.state.thread_count += 1
            future = asyncio.get_running_loop().run_in_executor(executor, partial(function, *arguments))
            def completed(done):
                app.state.thread_count -= 1
                if not done.cancelled():
                    done.exception()
            future.add_done_callback(completed)
            return await asyncio.shield(future)
        app.state.run_thread = bounded_thread
        async def check_storage():
            initialized = False
            while True:
                try:
                    if not initialized:
                        await bounded_thread(store.initialize)
                        initialized = True
                    app.state.database_ready = await bounded_thread(store.healthy)
                except Exception:
                    app.state.database_ready = False
                await asyncio.sleep(1)
        workers = [asyncio.create_task(context.poll(kind)) for kind in ("catalogue", "orders")]
        workers.append(asyncio.create_task(check_storage()))
        try:
            yield
        finally:
            app.state.stopping = True
            for task in workers:
                task.cancel()
            await asyncio.gather(*workers, return_exceptions=True)
            # Policies have a bounded inference contract; keep ownership until drain.
            if app.state.active:
                try:
                    await asyncio.wait_for(asyncio.shield(app.state.active), 2)
                except (Exception, asyncio.CancelledError):
                    pass
            await context.close()
            if app.state.active and not app.state.active.done():
                app.state.active.add_done_callback(lambda _: policy.close())
            else:
                policy.close()
            executor.shutdown(wait=False)

    app = FastAPI(lifespan=lifespan, docs_url=None, redoc_url=None, openapi_url=None)

    @app.middleware("http")
    async def admission(request, call_next):
        # Health remains available even when work admission is saturated.
        if request.url.path in ("/healthz", "/readyz"):
            return await call_next(request)
        if app.state.http_count >= 32:
            return response_error(429, "rate_limited")
        app.state.http_count += 1
        try:
            return await call_next(request)
        finally:
            app.state.http_count -= 1

    @app.exception_handler(APIError)
    async def domain_error(request, error):
        return response_error(error.status, error.code)

    @app.exception_handler(HTTPException)
    async def http_error(request, error):
        return response_error(error.status_code, "invalid_request")

    @app.exception_handler(Exception)
    async def unexpected_error(request, error):
        return response_error(503, "quote_unavailable")

    @app.get("/healthz")
    async def health(request: Request):
        no_query(request)
        return {"status": "ok"}

    @app.get("/readyz")
    async def ready(request: Request):
        no_query(request)
        if app.state.stopping or not app.state.database_ready or not app.state.context.ready(utcnow()):
            raise APIError(503, "context_unavailable")
        return {"status": "ok"}

    @app.post("/internal/v1/price-quotes")
    async def quote(request: Request):
        try:
            async with asyncio.timeout(1.5):
                return await calculate_quote(request)
        except TimeoutError:
            raise APIError(503, "quote_unavailable") from None

    async def calculate_quote(request):
        received = utcnow()
        authenticate(request, app.state.config.quote_token)
        no_query(request)
        value = await read_json(request, 16 * 1024)
        try:
            normalized = normalize_request(value)
            key = uuid(request.headers.get("idempotency-key", ""))
        except Exception:
            raise APIError(400, "invalid_request") from None
        payload_hash = fingerprint(normalized)
        store = app.state.storage
        try:
            existing = await app.state.run_thread(store.replay, "order", key, payload_hash)
        except APIError:
            raise
        except Exception:
            app.state.database_ready = False
            raise APIError(503, "quote_recording_unavailable") from None
        if existing is not None:
            return existing
        if instant(normalized["orderExpiresAt"]) <= received:
            raise APIError(400, "invalid_request")
        if app.state.stopping:
            raise APIError(503, "quote_unavailable")
        if app.state.active is not None:
            raise APIError(503 if key == app.state.active_key else 429, "quote_pending" if key == app.state.active_key else "rate_limited")
        now = utcnow()
        context = app.state.context.snapshot(now)
        resolved = resolve_places(normalized, context)
        policy_input = decode_input({"contractVersion": "1.0", "calculatedAt": stamp(now), "request": normalized, "resolvedPlaces": resolved, "context": context})
        def compute():
            started = time.monotonic()
            try:
                result = app.state.policy.calculate(policy_input)
                if time.monotonic() - started > 1:
                    raise PolicyUnavailable()
                result = result_json(result, app.state.manifest)
            except UnsupportedRoute:
                raise APIError(422, "unsupported_place") from None
            except Exception:
                raise APIError(503, "quote_unavailable") from None
            response = {
                "quoteId": str(uuid4()), "draft": normalized["draft"],
                "recommendedCredits": result["recommendedCredits"], "pricingMode": result["pricingMode"],
                "quotedAt": stamp(now), "validUntil": stamp(min(now + timedelta(seconds=120), instant(normalized["orderExpiresAt"]))),
                "releaseId": app.state.manifest["releaseId"], "contextSnapshotId": context["snapshotId"], "warnings": result["warnings"],
            }
            event = {
                "eventId": str(uuid4()), "source": "pricing", "type": "QuoteCalculated", "schemaVersion": 1, "occurredAt": stamp(now),
                "data": {"quoteId": response["quoteId"], "request": normalized, "response": response,
                         "releaseManifestId": app.state.manifest["releaseId"], "featureSchemaVersion": result["featureSchemaVersion"],
                         "featureSnapshot": result["featureSnapshot"], "contextSources": context["sources"], "contextSnapshot": context, "inferenceReused": False},
            }
            return response, event
        async def issue():
            try:
                return await app.state.run_thread(store.issue, "order", key, payload_hash, received, compute)
            except APIError:
                raise
            except Exception:
                app.state.database_ready = False
                raise APIError(503, "quote_recording_unavailable") from None
        task = asyncio.create_task(issue())
        app.state.active, app.state.active_key = task, key
        def finished(done):
            app.state.active, app.state.active_key = None, None
            if not done.cancelled():
                done.exception()  # Observe late failures without logging feature/token data.
        task.add_done_callback(finished)
        return await asyncio.shield(task)

    @app.post("/internal/v1/training-events")
    async def ingest(request: Request):
        try:
            async with asyncio.timeout(1.5):
                authenticate(request, app.state.config.write_token)
                no_query(request)
                events = event_batch(await read_json(request, 1024 * 1024))
                return await app.state.run_thread(app.state.storage.ingest, events)
        except APIError as error:
            if error.code == "invalid_request":
                raise APIError(400, "invalid_event") from None
            raise
        except Exception:
            raise APIError(503, "event_recording_unavailable") from None

    @app.get("/internal/v1/training-events")
    async def export(request: Request):
        authenticate(request, app.state.config.export_token)
        params = request.query_params
        if set(params) - {"after", "limit"} or len(params.multi_items()) != len(params):
            raise APIError(400, "invalid_request")
        try:
            raw = params.get("limit", "100")
            if not raw.isascii() or not raw.isdecimal() or not 1 <= int(raw) <= 100:
                raise ValueError()
            limit = int(raw)
        except ValueError:
            raise APIError(400, "invalid_request") from None
        try:
            async with asyncio.timeout(1.5):
                return await app.state.run_thread(app.state.storage.export, params.get("after"), limit)
        except APIError:
            raise
        except Exception:
            raise APIError(503, "event_recording_unavailable") from None

    return app


app = create_app()
