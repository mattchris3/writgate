"""The mock payments API: writgate's demo target.

Charges succeed at once, except for a few test customers. Refunds start
pending and settle after REFUND_SETTLE_DELAY, so they can be cancelled in
between. Every POST needs an Idempotency-Key.
"""

import os
import secrets
import string
from collections.abc import Callable
from datetime import UTC, datetime, timedelta
from typing import Annotated

from fastapi import (
    APIRouter,
    Depends,
    FastAPI,
    Header,
    HTTPException,
    Path,
    Query,
    Request,
    Response,
)
from fastapi.routing import APIRoute
from fastapi.security import HTTPAuthorizationCredentials, HTTPBearer

from . import idempotency
from .models import (
    Charge,
    ChargeCreate,
    ChargeList,
    ChargeStatus,
    Refund,
    RefundCreate,
    RefundList,
    RefundStatus,
)
from .store import MemoryIdempotencyStore, MemoryStore, PaymentStore, UnknownCursor

REFUND_SETTLE_DELAY = timedelta(seconds=30)
IDEMPOTENCY_TTL = timedelta(hours=24)

# Test customers whose charges fail, and why.
FAILING_CUSTOMERS = {
    "cus_test_declined": "card_declined",
    "cus_test_insufficient_funds": "insufficient_funds",
}

_ID_ALPHABET = string.ascii_letters + string.digits


def new_id(prefix: str) -> str:
    return prefix + "".join(secrets.choice(_ID_ALPHABET) for _ in range(20))


def utc_now() -> datetime:
    return datetime.now(UTC)


# ---- dependencies -------------------------------------------------------

_bearer = HTTPBearer(auto_error=False, description="The API key, sent as a bearer token.")


def require_api_key(
    request: Request,
    creds: Annotated[HTTPAuthorizationCredentials | None, Depends(_bearer)],
) -> None:
    expected: str = request.app.state.api_key
    if creds is None or not secrets.compare_digest(creds.credentials.encode(), expected.encode()):
        raise HTTPException(
            status_code=401,
            detail="Missing or invalid API key.",
            headers={"WWW-Authenticate": "Bearer"},
        )


def get_store(request: Request) -> PaymentStore:
    return request.app.state.store


def get_clock(request: Request) -> Callable[[], datetime]:
    return request.app.state.clock


Store = Annotated[PaymentStore, Depends(get_store)]
Clock = Annotated[Callable[[], datetime], Depends(get_clock)]
IdempotencyKey = Annotated[
    str,
    Header(
        alias="Idempotency-Key",
        min_length=1,
        max_length=255,
        description="A unique key per action. Retrying with the same key and body "
        "returns the first response instead of acting twice.",
    ),
]
Limit = Annotated[int, Query(ge=1, le=100, description="Page size.")]
StartingAfter = Annotated[
    str | None,
    Query(description="Cursor: the id of the last object on the previous page."),
]
LookupKey = Annotated[
    str | None,
    Query(description="Only return the object created with this Idempotency-Key."),
]
ChargeID = Annotated[str, Path(description="The charge's id (ch_...).")]


NOT_FOUND = {404: {"description": "No object with that id."}}


def not_found(kind: str, object_id: str) -> HTTPException:
    return HTTPException(status_code=404, detail=f"No {kind} with id {object_id}.")


def bad_cursor(cursor: str) -> HTTPException:
    return HTTPException(status_code=400, detail=f"starting_after {cursor} is not in this list.")


# ---- charges ------------------------------------------------------------

charges = APIRouter(tags=["charges"])


@charges.post(
    "/charges",
    operation_id="create_charge",
    summary="Create a charge",
    status_code=201,
    response_model=Charge,
    responses={402: {"description": "The charge failed (test customers only)."}},
)
async def create_charge(
    body: ChargeCreate,
    request: Request,
    store: Store,
    clock: Clock,
    key: IdempotencyKey,
):
    """Charge a customer. Charges succeed at once, except for the test
    customers cus_test_declined and cus_test_insufficient_funds."""

    async def handler() -> tuple[int, Charge]:
        failure = FAILING_CUSTOMERS.get(body.customer_id)
        charge = Charge(
            id=new_id("ch_"),
            amount=body.amount,
            currency=body.currency,
            customer_id=body.customer_id,
            description=body.description,
            status=ChargeStatus.failed if failure else ChargeStatus.succeeded,
            failure_reason=failure,
            amount_refunded=0,
            idempotency_key=key,
            created_at=clock(),
        )
        await store.add_charge(charge)
        if failure:
            raise HTTPException(status_code=402, detail=f"Charge {charge.id} failed: {failure}.")
        return 201, charge

    fp = idempotency.fingerprint("POST", "/charges", body)
    return await idempotency.run_once(request.app.state.idempotency, clock, key, fp, handler)


@charges.get(
    "/charges",
    operation_id="list_charges",
    summary="List charges",
    response_model=ChargeList,
)
async def list_charges(
    store: Store,
    customer_id: Annotated[str | None, Query(description="Only this customer's charges.")] = None,
    status: Annotated[
        ChargeStatus | None, Query(description="Only charges in this status.")
    ] = None,
    idempotency_key: LookupKey = None,
    limit: Limit = 20,
    starting_after: StartingAfter = None,
) -> ChargeList:
    """List charges, newest first."""
    try:
        page = await store.list_charges(
            customer_id=customer_id,
            status=status,
            idempotency_key=idempotency_key,
            limit=limit,
            starting_after=starting_after,
        )
    except UnknownCursor as exc:
        raise bad_cursor(str(exc)) from None
    return ChargeList(data=page.data, has_more=page.has_more)


@charges.get(
    "/charges/{charge_id}",
    operation_id="get_charge",
    summary="Get a charge",
    response_model=Charge,
    responses=NOT_FOUND,
)
async def get_charge(charge_id: ChargeID, store: Store) -> Charge:
    """Get one charge, including how much of it has been refunded."""
    charge = await store.get_charge(charge_id)
    if charge is None:
        raise not_found("charge", charge_id)
    return charge


# ---- refunds ------------------------------------------------------------

refunds = APIRouter(tags=["refunds"])


@refunds.post(
    "/charges/{charge_id}/refunds",
    operation_id="create_refund",
    summary="Refund a charge",
    status_code=201,
    response_model=Refund,
    responses={
        **NOT_FOUND,
        409: {"description": "The charge can't be refunded by that amount."},
    },
)
async def create_refund(
    charge_id: ChargeID,
    body: RefundCreate,
    request: Request,
    store: Store,
    clock: Clock,
    key: IdempotencyKey,
):
    """Refund all or part of a charge. Omit amount to refund what's left.
    The refund is pending for about 30 seconds, then succeeds."""

    async def handler() -> tuple[int, Refund]:
        charge = await store.get_charge(charge_id)
        if charge is None:
            raise not_found("charge", charge_id)
        if charge.status is not ChargeStatus.succeeded:
            raise HTTPException(status_code=409, detail=f"Charge {charge_id} did not succeed.")
        remaining = charge.amount - charge.amount_refunded
        amount = body.amount if body.amount is not None else remaining
        if remaining == 0:
            raise HTTPException(status_code=409, detail=f"Charge {charge_id} is fully refunded.")
        if amount > remaining:
            raise HTTPException(
                status_code=409,
                detail=f"Refund of {amount} is more than the {remaining} left to refund "
                f"on charge {charge_id}.",
            )
        now = clock()
        refund = Refund(
            id=new_id("re_"),
            charge_id=charge_id,
            amount=amount,
            currency=charge.currency,
            reason=body.reason,
            status=RefundStatus.pending,
            idempotency_key=key,
            created_at=now,
            settles_at=now + REFUND_SETTLE_DELAY,
        )
        await store.add_refund(refund)
        return 201, refund

    fp = idempotency.fingerprint("POST", f"/charges/{charge_id}/refunds", body)
    return await idempotency.run_once(request.app.state.idempotency, clock, key, fp, handler)


@refunds.get(
    "/charges/{charge_id}/refunds",
    operation_id="list_refunds",
    summary="List a charge's refunds",
    response_model=RefundList,
    responses=NOT_FOUND,
)
async def list_refunds(
    charge_id: ChargeID,
    store: Store,
    idempotency_key: LookupKey = None,
    limit: Limit = 20,
    starting_after: StartingAfter = None,
) -> RefundList:
    """List one charge's refunds, newest first."""
    if await store.get_charge(charge_id) is None:
        raise not_found("charge", charge_id)
    try:
        page = await store.list_refunds(
            charge_id,
            idempotency_key=idempotency_key,
            limit=limit,
            starting_after=starting_after,
        )
    except UnknownCursor as exc:
        raise bad_cursor(str(exc)) from None
    return RefundList(data=page.data, has_more=page.has_more)


@refunds.delete(
    "/refunds/{refund_id}",
    operation_id="cancel_refund",
    summary="Cancel a pending refund",
    status_code=204,
    response_class=Response,
    responses={**NOT_FOUND, 409: {"description": "The refund has already succeeded."}},
)
async def cancel_refund(
    refund_id: Annotated[str, Path(description="The refund's id (re_...).")],
    store: Store,
) -> Response:
    """Cancel a refund that is still pending. Cancelling a cancelled refund
    does nothing, so retries are safe."""
    refund = await store.get_refund(refund_id)
    if refund is None:
        raise not_found("refund", refund_id)
    if refund.status is RefundStatus.succeeded:
        raise HTTPException(status_code=409, detail=f"Refund {refund_id} has already succeeded.")
    if refund.status is RefundStatus.pending:
        await store.set_refund_status(refund_id, RefundStatus.cancelled)
    return Response(status_code=204)


# ---- app ----------------------------------------------------------------


def create_app(
    *,
    api_key: str,
    clock: Callable[[], datetime] = utc_now,
) -> FastAPI:
    if not api_key:
        raise ValueError("api_key must not be empty")
    app = FastAPI(
        title="Mock Payments API",
        version="0.1.0",
        summary="Charges and refunds in minor units, for writgate's demo.",
        dependencies=[Depends(require_api_key)],
    )
    app.state.api_key = api_key
    app.state.clock = clock
    app.state.store = MemoryStore(clock)
    app.state.idempotency = MemoryIdempotencyStore(clock, IDEMPOTENCY_TTL)
    for router in (charges, refunds):
        for route in router.routes:
            # Docstrings are wrapped at 100 columns. Unwrap them so each tool
            # description reads as one line.
            if isinstance(route, APIRoute):
                route.description = " ".join(route.description.split())
        app.include_router(router)
    return app


def app_from_env() -> FastAPI:
    """The app for `uvicorn --factory payments_api.app:app_from_env`."""
    key = os.environ.get("PAYMENTS_API_KEY")
    if not key:
        raise RuntimeError("Set PAYMENTS_API_KEY to the API key clients must send.")
    return create_app(api_key=key)
