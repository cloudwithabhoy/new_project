"""Catalog client used during reindex.

Pulls the full product list from `GET {CATALOG_URL}/products`, paginating with
limit/offset until a short page is returned. The trace-propagation headers from the
incoming request are forwarded verbatim so the reindex shows up as one trace
spanning search → catalog.
"""
from __future__ import annotations

import logging
from typing import Any

import httpx

from .config import Settings

log = logging.getLogger("search.catalog")

# Page size for walking the catalog. Catalog's GET /products supports limit/offset;
# a page shorter than this means we've reached the end.
_PAGE = 100


class CatalogError(Exception):
    """Raised when the catalog call fails or returns a non-2xx status."""


async def fetch_all_products(
    cfg: Settings, trace_headers: dict[str, str]
) -> list[dict[str, Any]]:
    products: list[dict[str, Any]] = []
    offset = 0
    async with httpx.AsyncClient(timeout=15.0) as client:
        while True:
            try:
                resp = await client.get(
                    f"{cfg.catalog_url}/products",
                    params={"limit": _PAGE, "offset": offset},
                    headers=trace_headers,
                )
                resp.raise_for_status()
            except httpx.HTTPError as err:
                raise CatalogError(str(err)) from err

            page = resp.json()
            # Tolerate either a bare list or an envelope like {"products": [...]}.
            if isinstance(page, dict):
                page = page.get("products") or page.get("items") or []
            if not page:
                break

            products.extend(page)
            if len(page) < _PAGE:
                break
            offset += _PAGE

    log.info("fetched products from catalog", extra={"count": len(products)})
    return products
