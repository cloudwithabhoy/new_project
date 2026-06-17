"""Response schemas. Pydantic shapes what the API returns."""
from __future__ import annotations

from typing import Any

from pydantic import BaseModel


class SearchResponse(BaseModel):
    """The result of a `GET /search` query."""

    query: str
    # Hits are catalog product documents as stored in the index; kept loose so the
    # search service doesn't have to track every catalog field change.
    hits: list[dict[str, Any]]
    total: int


class ReindexResponse(BaseModel):
    """The result of a `POST /reindex` run."""

    indexed: int
