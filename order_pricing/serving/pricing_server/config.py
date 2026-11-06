import os
from dataclasses import dataclass
from pathlib import Path
from urllib.parse import urlparse


@dataclass(frozen=True, repr=False)
class Settings:
    database_url: str
    package_dir: Path
    quote_token: str
    write_token: str
    export_token: str
    supplier_url: str
    supplier_token: str
    order_url: str
    order_token: str
    supplier_refresh: float = 60
    order_refresh: float = 15
    source_provenance: str = "observed"

    @classmethod
    def from_env(cls):
        def required(name):
            value = os.environ.get(name, "")
            if not value or value.strip() != value:
                raise ValueError(f"Invalid configuration: {name}")
            return value
        def source_url(name):
            value = required(name).rstrip("/")
            parsed = urlparse(value)
            if parsed.scheme not in ("http", "https") or not parsed.hostname or parsed.username or parsed.password or parsed.query or parsed.fragment:
                raise ValueError(f"Invalid configuration: {name}")
            return value
        tokens = [required(name) for name in ("PRICING_QUOTE_TOKEN", "PRICING_ORDER_WRITE_TOKEN", "PRICING_EXPORT_TOKEN", "PRICING_SUPPLIER_TOKEN", "PRICING_ORDER_TOKEN")]
        if any(any(ord(character) <= 32 or ord(character) >= 127 for character in token) for token in tokens):
            raise ValueError("Service credentials must be printable bearer tokens")
        if len(set(tokens[:3])) != 3 or set(tokens[:3]) & set(tokens[3:]):
            raise ValueError("Pricing read/write credentials must be separate")
        refresh = [float(os.environ.get(name, default)) for name, default in (("PRICING_CATALOGUE_REFRESH_SECONDS", "60"), ("PRICING_ORDER_REFRESH_SECONDS", "15"))]
        if any(not 0.1 <= value <= 3600 for value in refresh):
            raise ValueError("Invalid refresh interval")
        provenance = os.environ.get("PRICING_SOURCE_PROVENANCE", "observed")
        if provenance not in ("observed", "synthetic"):
            raise ValueError("Invalid source provenance")
        database = required("PRICING_DATABASE_URL")
        if urlparse(database).scheme not in ("postgres", "postgresql"):
            raise ValueError("Invalid database URL")
        return cls(database, Path(os.environ.get("PRICING_PACKAGE_DIR", "/opt/policy")), *tokens[:3], source_url("PRICING_SUPPLIER_URL"), tokens[3], source_url("PRICING_ORDER_URL"), tokens[4], *refresh, provenance)
