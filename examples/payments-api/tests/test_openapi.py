"""The spec is what writgate reads, so pin the parts it depends on."""

import json
from pathlib import Path

from payments_api import create_app

SPEC = create_app(api_key="unused").openapi()

EXPECTED = {
    "create_charge": ("post", "/charges"),
    "list_charges": ("get", "/charges"),
    "get_charge": ("get", "/charges/{charge_id}"),
    "create_refund": ("post", "/charges/{charge_id}/refunds"),
    "list_refunds": ("get", "/charges/{charge_id}/refunds"),
    "cancel_refund": ("delete", "/refunds/{refund_id}"),
}


def operations():
    for path, item in SPEC["paths"].items():
        for method, op in item.items():
            yield method, path, op


def test_operation_ids() -> None:
    got = {op["operationId"]: (method, path) for method, path, op in operations()}
    assert got == EXPECTED


def test_every_operation_has_summary_and_success_schema() -> None:
    for method, path, op in operations():
        assert op.get("summary"), f"{method} {path} has no summary"
        if method != "delete":
            ok = next(code for code in op["responses"] if code.startswith("2"))
            assert "schema" in op["responses"][ok]["content"]["application/json"], path


def test_posts_require_idempotency_key() -> None:
    for method, path, op in operations():
        if method == "post":
            params = {p["name"]: p for p in op["parameters"]}
            assert params["Idempotency-Key"]["required"] is True, path


def test_bearer_auth_declared() -> None:
    assert SPEC["components"]["securitySchemes"]["HTTPBearer"]["scheme"] == "bearer"


def test_committed_spec_is_current() -> None:
    committed = json.loads((Path(__file__).parent.parent / "openapi.json").read_text())
    assert committed == SPEC, "openapi.json is stale: run `uv run python scripts/export_openapi.py`"
