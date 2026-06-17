"""Postgres data access via an asyncpg connection pool.

Mirrors the catalog/auth pattern: retry the initial connect (the DB pod may not be
ready when this service starts), run an inline migration, and expose small typed
helpers. A duplicate email surfaces as EmailConflict so the handler returns 409.
"""
from __future__ import annotations

import asyncio
import logging

import asyncpg

from .config import Settings
from .models import User

log = logging.getLogger("user.db")


class NotFound(Exception):
    """Raised when a requested user does not exist."""


class EmailConflict(Exception):
    """Raised when an email is already taken (unique violation)."""


_COLUMNS = "id, email, full_name, created_at, updated_at"


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
                CREATE TABLE IF NOT EXISTS users (
                    id         BIGSERIAL PRIMARY KEY,
                    email      TEXT NOT NULL UNIQUE,
                    full_name  TEXT NOT NULL DEFAULT '',
                    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
                    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
                );
                """
            )

    async def create(self, email: str, full_name: str) -> User:
        try:
            row = await self._pool.fetchrow(
                f"INSERT INTO users (email, full_name) VALUES ($1, $2) RETURNING {_COLUMNS}",
                email,
                full_name,
            )
        except asyncpg.UniqueViolationError as err:
            raise EmailConflict() from err
        return User(**dict(row))

    async def get(self, user_id: int) -> User:
        row = await self._pool.fetchrow(
            f"SELECT {_COLUMNS} FROM users WHERE id = $1", user_id
        )
        if row is None:
            raise NotFound()
        return User(**dict(row))

    async def get_by_email(self, email: str) -> User:
        row = await self._pool.fetchrow(
            f"SELECT {_COLUMNS} FROM users WHERE email = $1", email
        )
        if row is None:
            raise NotFound()
        return User(**dict(row))

    async def list(self, limit: int, offset: int) -> list[User]:
        rows = await self._pool.fetch(
            f"SELECT {_COLUMNS} FROM users ORDER BY id LIMIT $1 OFFSET $2",
            limit,
            offset,
        )
        return [User(**dict(r)) for r in rows]

    async def update(self, user_id: int, email: str, full_name: str) -> User:
        try:
            row = await self._pool.fetchrow(
                f"""
                UPDATE users SET email = $1, full_name = $2, updated_at = now()
                WHERE id = $3 RETURNING {_COLUMNS}
                """,
                email,
                full_name,
                user_id,
            )
        except asyncpg.UniqueViolationError as err:
            raise EmailConflict() from err
        if row is None:
            raise NotFound()
        return User(**dict(row))
