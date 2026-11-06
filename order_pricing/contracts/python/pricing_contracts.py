"""Framework-independent adapter v1. Serialization rules live in schemas/v1.json."""

from dataclasses import dataclass
from pathlib import Path
from typing import Callable, Literal, Mapping, Protocol, TypeAlias

JSONValue: TypeAlias = (
    str | int | float | bool | None | Mapping[str, "JSONValue"] | tuple["JSONValue", ...]
)
JSONObject: TypeAlias = Mapping[str, JSONValue]


@dataclass(frozen=True)
class PolicyInput:
    contract_version: str
    calculated_at: str
    request: JSONObject
    resolved_places: JSONObject
    context: JSONObject


@dataclass(frozen=True)
class PolicyResult:
    recommended_credits: int
    pricing_mode: Literal["baseline", "model"]
    warnings: tuple[str, ...]
    feature_schema_version: str
    feature_snapshot: JSONObject


class PolicyUnavailable(Exception):
    """Expected failure; framework emits a sanitized quote_unavailable response."""


class UnsupportedRoute(Exception):
    """Framework emits unsupported_place; never authorizes Order cancellation."""


class PricingPolicy(Protocol):
    def warm_up(self, example: PolicyInput) -> None: ...

    def calculate(self, value: PolicyInput) -> PolicyResult: ...

    def close(self) -> None: ...


PolicyLoader: TypeAlias = Callable[[Path], PricingPolicy]
