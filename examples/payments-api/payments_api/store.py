"""Storage for charges, refunds and idempotency records.

Week 1 keeps everything in memory behind small async interfaces. Week 2
swaps in PostgreSQL for charges and refunds and Redis for idempotency
records, without touching the routes.
"""

import asyncio
from collections.abc import Callable
from dataclasses import dataclass
from datetime import datetime, timedelta
from typing import Protocol

from .models import Charge, ChargeStatus, Refund, RefundStatus


class UnknownCursor(Exception):
    """starting_after named an object that isn't in the list."""


@dataclass(frozen=True)
class Page[T]:
    data: list[T]
    has_more: bool


def paginate[T](newest_first: list[T], limit: int, starting_after: str | None) -> Page[T]:
    items = newest_first
    if starting_after is not None:
        ids = [item.id for item in items]  # type: ignore[attr-defined]
        if starting_after not in ids:
            raise UnknownCursor(starting_after)
        items = items[ids.index(starting_after) + 1 :]
    return Page(data=items[:limit], has_more=len(items) > limit)


class PaymentStore(Protocol):
    async def add_charge(self, charge: Charge) -> None: ...
    async def get_charge(self, charge_id: str) -> Charge | None: ...
    async def list_charges(
        self,
        *,
        customer_id: str | None,
        status: ChargeStatus | None,
        idempotency_key: str | None,
        limit: int,
        starting_after: str | None,
    ) -> Page[Charge]: ...
    async def add_refund(self, refund: Refund) -> None: ...
    async def get_refund(self, refund_id: str) -> Refund | None: ...
    async def list_refunds(
        self,
        charge_id: str,
        *,
        idempotency_key: str | None,
        limit: int,
        starting_after: str | None,
    ) -> Page[Refund]: ...
    async def set_refund_status(self, refund_id: str, status: RefundStatus) -> Refund: ...


class MemoryStore:
    """An in-memory PaymentStore. Refunds settle lazily, when read."""

    def __init__(self, clock: Callable[[], datetime]) -> None:
        self._clock = clock
        self._charges: dict[str, Charge] = {}
        self._refunds: dict[str, Refund] = {}

    def _settle(self, refund: Refund) -> Refund:
        if refund.status is RefundStatus.pending and self._clock() >= refund.settles_at:
            refund = refund.model_copy(update={"status": RefundStatus.succeeded})
            self._refunds[refund.id] = refund
        return refund

    def _with_refunded(self, charge: Charge) -> Charge:
        refunded = sum(
            r.amount
            for r in self._refunds.values()
            if r.charge_id == charge.id and r.status is not RefundStatus.cancelled
        )
        return charge.model_copy(update={"amount_refunded": refunded})

    async def add_charge(self, charge: Charge) -> None:
        self._charges[charge.id] = charge

    async def get_charge(self, charge_id: str) -> Charge | None:
        charge = self._charges.get(charge_id)
        return self._with_refunded(charge) if charge else None

    async def list_charges(
        self,
        *,
        customer_id: str | None,
        status: ChargeStatus | None,
        idempotency_key: str | None,
        limit: int,
        starting_after: str | None,
    ) -> Page[Charge]:
        items = [
            self._with_refunded(c)
            for c in reversed(self._charges.values())
            if (customer_id is None or c.customer_id == customer_id)
            and (status is None or c.status is status)
            and (idempotency_key is None or c.idempotency_key == idempotency_key)
        ]
        return paginate(items, limit, starting_after)

    async def add_refund(self, refund: Refund) -> None:
        self._refunds[refund.id] = refund

    async def get_refund(self, refund_id: str) -> Refund | None:
        refund = self._refunds.get(refund_id)
        return self._settle(refund) if refund else None

    async def list_refunds(
        self,
        charge_id: str,
        *,
        idempotency_key: str | None,
        limit: int,
        starting_after: str | None,
    ) -> Page[Refund]:
        items = [
            self._settle(r)
            for r in reversed(self._refunds.values())
            if r.charge_id == charge_id
            and (idempotency_key is None or r.idempotency_key == idempotency_key)
        ]
        return paginate(items, limit, starting_after)

    async def set_refund_status(self, refund_id: str, status: RefundStatus) -> Refund:
        refund = self._refunds[refund_id].model_copy(update={"status": status})
        self._refunds[refund_id] = refund
        return refund


@dataclass(frozen=True)
class IdempotencyRecord:
    fingerprint: str
    status_code: int
    body: object
    expires_at: datetime


class IdempotencyStore(Protocol):
    ttl: timedelta

    def lock(self, key: str) -> asyncio.Lock: ...
    async def get(self, key: str) -> IdempotencyRecord | None: ...
    async def put(self, key: str, record: IdempotencyRecord) -> None: ...


class MemoryIdempotencyStore:
    """Keeps replayable responses for `ttl`. Redis takes over in week 2."""

    def __init__(self, clock: Callable[[], datetime], ttl: timedelta) -> None:
        self._clock = clock
        self.ttl = ttl
        self._records: dict[str, IdempotencyRecord] = {}
        self._locks: dict[str, asyncio.Lock] = {}

    def lock(self, key: str) -> asyncio.Lock:
        return self._locks.setdefault(key, asyncio.Lock())

    async def get(self, key: str) -> IdempotencyRecord | None:
        record = self._records.get(key)
        if record and self._clock() >= record.expires_at:
            del self._records[key]
            return None
        return record

    async def put(self, key: str, record: IdempotencyRecord) -> None:
        self._records[key] = record
