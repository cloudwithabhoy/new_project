"""Request/response schemas. Pydantic validates input before it reaches the DB."""
from __future__ import annotations

from pydantic import BaseModel


class Recommendations(BaseModel):
    """The recommendation response: the active A/B variant plus ranked product ids."""

    variant: str
    product_ids: list[int]
