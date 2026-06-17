"""Postgres data access via an asyncpg connection pool.

Mirrors the user/catalog pattern: retry the initial connect (the DB pod may not be
ready when this service starts), run an inline migration, and expose small typed
helpers.

Three tables back the rankings:
  - product_popularity(product_id PK, count) — how often a product is ordered.
  - co_purchase(product_id, other_product_id, count, PK(product_id, other_product_id))
    — how often two products appear in the SAME order (directional pairs).
  - processed_orders(order_id PK) — idempotency ledger so a re-delivered
    `order.created` event is counted at most once.

The consumer's per-event work runs in ONE transaction: it claims the order_id, and
if the claim is new, updates popularity + co-purchase counts. Re-delivery is a no-op.
"""
from __future__ import annotations

import asyncio
import logging
from itertools import permutations

import asyncpg

from .config import Settings

log = logging.getLogger("recommendation.db")


class Store:
    def __init__(self, pool: asyncpg.Pool) -> None:
        self._pool = pool

    @classmethod
    async def connect(cls, cfg: Settings) -> "Store":
        ssl = "require" if cfg.db_sslmode not in ("disable", "") else None
        last_err: Exception | None = None
        for attempt in range(1, 11):
            try:
                pool = await asyncpg.create_pool(
                    dsn=cfg.dsn, ssl=ssl, min_size=1, max_size=10, command_timeout=10
                )
                async with pool.acquire() as conn:
                    await conn.execute("SELECT 1")
                log.info("connected to database")
                return cls(pool)
            except Exception as err:  # noqa: BLE001 - retry on any connect error
                last_err = err
                log.warning("database not ready, retrying", extra={"attempt": attempt})
                await asyncio.sleep(3)
        raise RuntimeError(f"could not connect to database after retries: {last_err}")

    async def close(self) -> None:
        await self._pool.close()

    async def ping(self) -> None:
        async with self._pool.acquire() as conn:
            await conn.execute("SELECT 1")

    async def migrate(self) -> None:
        async with self._pool.acquire() as conn:
            await conn.execute(
                """
                CREATE TABLE IF NOT EXISTS product_popularity (
                    product_id BIGINT PRIMARY KEY,
                    count      BIGINT NOT NULL DEFAULT 0
                );

                CREATE TABLE IF NOT EXISTS co_purchase (
                    product_id       BIGINT NOT NULL,
                    other_product_id BIGINT NOT NULL,
                    count            BIGINT NOT NULL DEFAULT 0,
                    PRIMARY KEY (product_id, other_product_id)
                );

                CREATE TABLE IF NOT EXISTS processed_orders (
                    order_id   BIGINT PRIMARY KEY,
                    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
                );
                """
            )

    # --- read paths used by the two A/B ranking strategies ---

    async def top_popular(self, limit: int) -> list[int]:
        """Variant A: most popular products overall."""
        rows = await self._pool.fetch(
            """
            SELECT product_id FROM product_popularity
            ORDER BY count DESC, product_id ASC
            LIMIT $1
            """,
            limit,
        )
        return [r["product_id"] for r in rows]

    async def co_purchased(self, product_id: int, limit: int) -> list[int]:
        """Variant B: products most frequently bought together with `product_id`."""
        rows = await self._pool.fetch(
            """
            SELECT other_product_id FROM co_purchase
            WHERE product_id = $1
            ORDER BY count DESC, other_product_id ASC
            LIMIT $2
            """,
            product_id,
            limit,
        )
        return [r["other_product_id"] for r in rows]

    # --- write path used by the Kafka consumer ---

    async def record_order(self, order_id: int, product_ids: list[int]) -> bool:
        """Apply one order's signals, idempotently by order_id.

        Returns True if the order was newly processed, False if it was a duplicate
        (already in processed_orders). All work happens in a single transaction so a
        crash mid-apply can't leave counts half-updated relative to the ledger.
        """
        # Distinct, stable order of products in this order.
        unique_ids = sorted(set(product_ids))
        async with self._pool.acquire() as conn:
            async with conn.transaction():
                claimed = await conn.fetchval(
                    """
                    INSERT INTO processed_orders (order_id) VALUES ($1)
                    ON CONFLICT (order_id) DO NOTHING
                    RETURNING order_id
                    """,
                    order_id,
                )
                if claimed is None:
                    return False  # duplicate event — already counted

                # Popularity: +1 per product in the order.
                if unique_ids:
                    await conn.executemany(
                        """
                        INSERT INTO product_popularity (product_id, count)
                        VALUES ($1, 1)
                        ON CONFLICT (product_id)
                        DO UPDATE SET count = product_popularity.count + 1
                        """,
                        [(pid,) for pid in unique_ids],
                    )

                # Co-purchase: +1 for every ordered (directional) pair so that a
                # lookup by either product surfaces the other.
                pairs = [
                    (a, b) for a, b in permutations(unique_ids, 2)
                ]
                if pairs:
                    await conn.executemany(
                        """
                        INSERT INTO co_purchase (product_id, other_product_id, count)
                        VALUES ($1, $2, 1)
                        ON CONFLICT (product_id, other_product_id)
                        DO UPDATE SET count = co_purchase.count + 1
                        """,
                        pairs,
                    )
                return True
