"""Idempotency-Key handling for POST endpoints.

The same key with the same request replays the first response, including a
deterministic error such as a declined charge. The same key with a different
request is a 409. Validation errors (422) happen before this runs, so they
are never stored.
"""

import hashlib
import json
from collections.abc import Awaitable, Callable
from datetime import datetime

from fastapi import HTTPException
from fastapi.encoders import jsonable_encoder
from fastapi.responses import JSONResponse
from pydantic import BaseModel

from .store import IdempotencyRecord, IdempotencyStore

REPLAYED_HEADER = "Idempotent-Replayed"


def fingerprint(method: str, path: str, payload: BaseModel) -> str:
    body = json.dumps(payload.model_dump(mode="json"), sort_keys=True, separators=(",", ":"))
    return hashlib.sha256(f"{method} {path}\n{body}".encode()).hexdigest()


async def run_once(
    store: IdempotencyStore,
    clock: Callable[[], datetime],
    key: str,
    fp: str,
    handler: Callable[[], Awaitable[tuple[int, BaseModel]]],
) -> JSONResponse:
    async with store.lock(key):
        existing = await store.get(key)
        if existing is not None:
            if existing.fingerprint != fp:
                raise HTTPException(
                    status_code=409,
                    detail="Idempotency-Key was already used with a different request. "
                    "Use a new key for a new request.",
                )
            return JSONResponse(
                existing.body,
                status_code=existing.status_code,
                headers={REPLAYED_HEADER: "true"},
            )

        try:
            status_code, model = await handler()
            body = jsonable_encoder(model)
        except HTTPException as exc:
            if exc.status_code >= 500:
                raise
            status_code, body = exc.status_code, {"detail": exc.detail}

        await store.put(
            key,
            IdempotencyRecord(
                fingerprint=fp,
                status_code=status_code,
                body=body,
                expires_at=clock() + store.ttl,
            ),
        )
        return JSONResponse(body, status_code=status_code)
