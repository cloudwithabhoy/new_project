"""Postgres data access via an asyncpg connection pool.

Mirrors the user/catalog/auth pattern: retry the initial connect (the DB pod may not
be ready when this service starts), run an inline migration, and expose small typed
helpers.

The interesting bits here are the two transactional operations that make inventory
correct under concurrency and at-least-once Kafka delivery:

- `reserve(...)` — all-or-none across every line of an order, idempotent by order_id.
  It locks the involved rows (`SELECT ... FOR UPDATE`), checks every line has stock,
  then decrements `available` / increments `reserved` in a single transaction. Any
  shortfall aborts the whole thing (nothing changes). The first reserve for an
  order_id inserts a `reservations` row; a repeat is a no-op success.
- `commit_reservation(...)` — invoked from the Kafka consumer on `order.created`;
  turns reserved stock into sold (`reserved -= qty`), idempotent by order_id via the
  `committed` flag so redelivered events don't double-decrement.
"""
from __future__ import annotations

import asyncio
import logging

import asyncpg

from .config import Settings
from .models import Inventory, ReserveItem

log = logging.getLogger("inventory.db")


class NotFound(Exception):
    """Raised when a requested inventory row does not exist."""


class InsufficientStock(Exception):
    """Raised when a reservation cannot be fully satisfied (all-or-none)."""


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
                CREATE TABLE IF NOT EXISTS inventory (
                    product_id BIGINT PRIMARY KEY,
                    available  BIGINT NOT NULL DEFAULT 0,
                    reserved   BIGINT NOT NULL DEFAULT 0
                );
                """
            )
            # Idempotency ledger: one row per order_id. `committed` flips true once the
            # order.created event has been processed (reservation turned into a sale).
            await conn.execute(
                """
                CREATE TABLE IF NOT EXISTS reservations (
                    order_id   BIGINT PRIMARY KEY,
                    committed  BOOLEAN NOT NULL DEFAULT FALSE,
                    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
                );
                """
            )

    async def get(self, product_id: int) -> Inventory:
        row = await self._pool.fetchrow(
            "SELECT product_id, available, reserved FROM inventory WHERE product_id = $1",
            product_id,
        )
        if row is None:
            raise NotFound()
        return Inventory(**dict(row))

    async def upsert(self, product_id: int, available: int) -> Inventory:
        """Set/seed stock for a product (insert or update available)."""
        row = await self._pool.fetchrow(
            """
            INSERT INTO inventory (product_id, available, reserved)
            VALUES ($1, $2, 0)
            ON CONFLICT (product_id)
            DO UPDATE SET available = EXCLUDED.available
            RETURNING product_id, available, reserved
            """,
            product_id,
            available,
        )
        return Inventory(**dict(row))

    async def reserve(self, order_id: int, items: list[ReserveItem]) -> None:
        """Atomically reserve stock for every line, or change nothing.

        Idempotent by order_id: a second reserve for the same order is a no-op
        success. Raises InsufficientStock (→ 409) if any line can't be met.
        """
        async with self._pool.acquire() as conn:
            async with conn.transaction():
                # Idempotency: claim the order_id. If it already exists, this reserve
                # already happened (or was committed) — treat as success, do nothing.
                claimed = await conn.fetchval(
                    """
                    INSERT INTO reservations (order_id) VALUES ($1)
                    ON CONFLICT (order_id) DO NOTHING
                    RETURNING order_id
                    """,
                    order_id,
                )
                if claimed is None:
                    return

                # Collapse duplicate product lines so a product appearing twice is
                # checked/decremented by its total quantity.
                wanted: dict[int, int] = {}
                for item in items:
                    wanted[item.product_id] = wanted.get(item.product_id, 0) + item.quantity

                # Lock the rows we're about to touch, in a stable order to avoid
                # deadlocks between concurrent reservations.
                product_ids = sorted(wanted.keys())
                rows = await conn.fetch(
                    """
                    SELECT product_id, available FROM inventory
                    WHERE product_id = ANY($1::bigint[])
                    FOR UPDATE
                    """,
                    product_ids,
                )
                available = {r["product_id"]: r["available"] for r in rows}

                # Every line must have a row AND enough stock — else abort the whole
                # transaction (the `raise` rolls back, including the claim above).
                for pid in product_ids:
                    if pid not in available or available[pid] < wanted[pid]:
                        raise InsufficientStock()

                for pid in product_ids:
                    await conn.execute(
                        """
                        UPDATE inventory
                        SET available = available - $2, reserved = reserved + $2
                        WHERE product_id = $1
                        """,
                        pid,
                        wanted[pid],
                    )

    async def commit_reservation(self, order_id: int, items: list[ReserveItem]) -> None:
        """Turn reserved stock into sold for an order (reserved -= qty).

        Driven by the Kafka consumer on `order.created`. Idempotent by order_id via
        the `committed` flag, so redelivered events don't double-decrement.
        """
        async with self._pool.acquire() as conn:
            async with conn.transaction():
                # Lock the ledger row and only proceed if not already committed.
                rec = await conn.fetchrow(
                    "SELECT committed FROM reservations WHERE order_id = $1 FOR UPDATE",
                    order_id,
                )
                if rec is not None and rec["committed"]:
                    return  # already processed — idempotent no-op

                wanted: dict[int, int] = {}
                for item in items:
                    wanted[item.product_id] = wanted.get(item.product_id, 0) + item.quantity

                for pid in sorted(wanted.keys()):
                    # Never let reserved go negative if the event arrives without a
                    # prior reserve (e.g. out-of-order). GREATEST clamps at 0.
                    await conn.execute(
                        """
                        UPDATE inventory
                        SET reserved = GREATEST(reserved - $2, 0)
                        WHERE product_id = $1
                        """,
                        pid,
                        wanted[pid],
                    )

                # Record the order as committed (insert if the reserve never ran).
                await conn.execute(
                    """
                    INSERT INTO reservations (order_id, committed) VALUES ($1, TRUE)
                    ON CONFLICT (order_id) DO UPDATE SET committed = TRUE
                    """,
                    order_id,
                )
