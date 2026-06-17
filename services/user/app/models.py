"""Request/response schemas. Pydantic validates input before it reaches the DB."""
from __future__ import annotations

from datetime import datetime

from pydantic import BaseModel, EmailStr, Field


class UserInput(BaseModel):
    """Client-supplied payload for create/update. Server-managed fields are omitted."""

    email: EmailStr
    full_name: str = Field(min_length=1, max_length=200)


class User(BaseModel):
    """A user profile as stored and returned by the API."""

    id: int
    email: EmailStr
    full_name: str
    created_at: datetime
    updated_at: datetime
