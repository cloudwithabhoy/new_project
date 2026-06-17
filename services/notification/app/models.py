"""Response schemas. Pydantic shapes the inspection API's output."""
from __future__ import annotations

from typing import Optional

from pydantic import BaseModel


class Notification(BaseModel):
    """A notification record as kept in the in-memory ring buffer and returned by
    `GET /notifications` (newest first)."""

    channel: str
    order_id: int
    user_id: Optional[int] = None
    sent_at: str
