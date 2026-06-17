"""Elasticsearch data access via the v8 AsyncElasticsearch client.

Mirrors the user/catalog DB pattern: retry the initial connect (the ES pod may not
be ready when this service starts), ensure the index exists with a simple mapping,
and expose small helpers for searching and bulk-indexing the catalog's products.
The index name is fixed at "products" per the service contract.
"""
from __future__ import annotations

import asyncio
import logging
from typing import Any, Iterable

from elasticsearch import AsyncElasticsearch
from elasticsearch.helpers import async_bulk

from .config import Settings

log = logging.getLogger("search.es")

INDEX = "products"

# A simple mapping: full-text on name/description, keep the rest numeric/date so we
# can sort/filter without analysis. Created on first connect if missing.
_MAPPINGS = {
    "properties": {
        "id": {"type": "long"},
        "name": {"type": "text"},
        "description": {"type": "text"},
        "price_cents": {"type": "long"},
        "stock": {"type": "integer"},
        "created_at": {"type": "date"},
        "updated_at": {"type": "date"},
    }
}


class Index:
    def __init__(self, client: AsyncElasticsearch) -> None:
        self._client = client

    @classmethod
    async def connect(cls, cfg: Settings) -> "Index":
        client = AsyncElasticsearch(hosts=[cfg.elasticsearch_url], request_timeout=10)
        last_err: Exception | None = None
        for attempt in range(1, 11):
            try:
                if await client.ping():
                    log.info("connected to elasticsearch")
                    self = cls(client)
                    await self.ensure_index()
                    return self
                raise RuntimeError("ping returned false")
            except Exception as err:  # noqa: BLE001 - retry on any connect error
                last_err = err
                log.warning(
                    "elasticsearch not ready, retrying", extra={"attempt": attempt}
                )
                await asyncio.sleep(3)
        await client.close()
        raise RuntimeError(
            f"could not connect to elasticsearch after retries: {last_err}"
        )

    async def close(self) -> None:
        await self._client.close()

    async def ping(self) -> None:
        if not await self._client.ping():
            raise RuntimeError("elasticsearch ping failed")

    async def ensure_index(self) -> None:
        if not await self._client.indices.exists(index=INDEX):
            await self._client.indices.create(index=INDEX, mappings=_MAPPINGS)
            log.info("created index", extra={"index": INDEX})

    async def search(self, q: str, limit: int) -> tuple[list[dict[str, Any]], int]:
        if q:
            query: dict[str, Any] = {
                "multi_match": {
                    "query": q,
                    "fields": ["name", "description"],
                }
            }
        else:
            # Empty query → return recent/all, capped by `limit`.
            query = {"match_all": {}}

        resp = await self._client.search(index=INDEX, query=query, size=limit)
        hits = [h["_source"] for h in resp["hits"]["hits"]]
        total = resp["hits"]["total"]["value"]
        return hits, total

    async def bulk_index(self, products: Iterable[dict[str, Any]]) -> int:
        """Bulk-index products into the `products` index, doc id = product id.

        Returns the number of successfully indexed documents.
        """
        actions = (
            {"_index": INDEX, "_id": str(p["id"]), "_source": p} for p in products
        )
        indexed, _ = await async_bulk(self._client, actions, refresh="wait_for")
        return indexed
