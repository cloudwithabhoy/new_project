"""Request/response schemas. Pydantic validates input before it reaches the DB."""
from __future__ import annotations

from pydantic import BaseModel, Field


class StockInput(BaseModel):
    """Client-supplied payload for PUT /inventory/{product_id} — set/seed stock."""

    available: int = Field(ge=0)


class Inventory(BaseModel):
    """An inventory row as stored and returned by the API."""

    product_id: int
    available: int
    reserved: int


class ReserveItem(BaseModel):
    """One line of a reservation request."""

    product_id: int
    quantity: int = Field(gt=0)


class ReserveInput(BaseModel):
    """Client-supplied payload for POST /inventory/reserve. All-or-none."""

    order_id: int
    items: list[ReserveItem] = Field(min_length=1)
