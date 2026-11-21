"""Request and response models. Money is an integer in minor units."""

from datetime import datetime
from enum import StrEnum
from typing import Annotated

from pydantic import BaseModel, ConfigDict, Field

Currency = Annotated[
    str,
    Field(pattern=r"^[A-Z]{3}$", description="ISO 4217 currency code, e.g. GBP."),
]
MinorUnits = Annotated[
    int,
    Field(gt=0, description="Amount in minor units, e.g. 1250 = £12.50."),
]


class ChargeStatus(StrEnum):
    succeeded = "succeeded"
    failed = "failed"


class RefundStatus(StrEnum):
    pending = "pending"
    succeeded = "succeeded"
    cancelled = "cancelled"


class RefundReason(StrEnum):
    duplicate = "duplicate"
    fraudulent = "fraudulent"
    requested_by_customer = "requested_by_customer"


class ChargeCreate(BaseModel):
    model_config = ConfigDict(extra="forbid")

    amount: MinorUnits
    currency: Currency = "GBP"
    customer_id: str = Field(pattern=r"^cus_\w+$", description="The customer to charge.")
    description: str | None = Field(default=None, max_length=500)


class Charge(BaseModel):
    id: str = Field(description="Starts with ch_.")
    amount: int
    currency: str
    customer_id: str
    description: str | None
    status: ChargeStatus
    failure_reason: str | None = Field(description="Why the charge failed, if it did.")
    amount_refunded: int = Field(
        description="Total of refunds that are pending or succeeded, in minor units."
    )
    idempotency_key: str
    created_at: datetime


class ChargeList(BaseModel):
    data: list[Charge]
    has_more: bool


class RefundCreate(BaseModel):
    model_config = ConfigDict(extra="forbid")

    amount: MinorUnits | None = Field(
        default=None, description="Omit to refund everything not yet refunded."
    )
    reason: RefundReason | None = None


class Refund(BaseModel):
    id: str = Field(description="Starts with re_.")
    charge_id: str
    amount: int
    currency: str
    reason: RefundReason | None
    status: RefundStatus
    idempotency_key: str
    created_at: datetime
    settles_at: datetime = Field(description="When a pending refund becomes succeeded.")


class RefundList(BaseModel):
    data: list[Refund]
    has_more: bool
