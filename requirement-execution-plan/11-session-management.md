# Phase 11 — Session management

## Goal
Make sessions work in any region: a signed token *is* the session, so any region can validate it locally
with nothing to replicate — and a user can move regions (or fail over) without getting logged out.

## Why this phase
For global routing and failover to pay off, a user has to be servable by any region — so sessions can't be
pinned to one. Here you make sessions region-agnostic: a request that lands on Mumbai today and London
tomorrow (or mid-session, on failover) authenticates and works the same, with no server-side session store
to replicate across regions. This is what makes Phase 09's failover clean.
- **Builds on:** Phase 09 (multiple regions behind global routing) and Phase 10 (cross-region data
  replication — the durable state lives in the replicated DB). `auth` already issues RS256 JWTs and
  exposes a JWKS endpoint (from Phases 01-08).
- **Unlocks:** clean cross-region failover for users, ready for DR (Phase 17) and a third region
  (Phase 21).

## Scope
**In scope:** stateless RS256 JWT verification at every region's edge via local-cached JWKS; short-TTL
access tokens + refresh tokens + JWKS key rotation (`kid`); region-local Redis cart keyed by user with a
defined failover behaviour; cross-region verification testing.
**Out of scope:** issuing the RS256 JWTs and the JWKS endpoint (already delivered in Phases 01-08);
cross-region data replication (Phase 10); the Singapore region (Phase 21); DR promotion (Phase 17).

## What it needs to do
- Each region's api-gateway validates the RS256 JWT against its local-cached JWKS and rejects on bad
  signature/expiry — with no session lookup anywhere.
- A token minted by `auth` is accepted by every region with no shared secret.
- Access tokens are short-lived and a refresh flow re-mints them seamlessly; JWKS key rotation uses `kid`
  so keys rotate without breaking live tokens.
- The cart is held in region-local Redis keyed by user.
- Cart behaviour on region failover is defined: accept a cold rebuild, or back the cart in the replicated
  DB (Phase 10) so it survives.
- A user can be served by, or fail over to, any region without losing their session.
- No server-side session state needs cross-region replication — the signed token carries the session.
- Revocation is bounded by a short TTL (e.g. 15 min); a revoked/expired token is rejected within one TTL.
- Token verification is local to each region; JWTs ride every request, so claims are kept small.

## Architecture

```
   auth ──(RS256-signs JWT)──► browser holds the token            (NEW: stateless JWT)
     └─ publishes JWKS (public keys) so any region can verify
                 users ─► Global Router ─► nearest region
          ┌──────────────────────────────┴─────────────────────────────┐
          ▼                                                             ▼
 ┌─ MUMBAI ─────────────────────────────┐       ┌─ LONDON ─────────────────────────────
 │  api-gateway                          │       │  api-gateway
 │   └─ verify JWT via JWKS  ────────────┤◄────► ├──── verify JWT via JWKS
 │      (local · no shared secret ·      │  any  │      (local · no shared secret ·
 │       no session store to replicate)  │ region│       no session store to replicate)
 │  cart ──► Redis (region-local)        │       │  cart ──► Redis (region-local)
 │  DB writes ──► Mumbai primary (Step 10)│       │  DB writes ──► Mumbai primary (Step 10)
 └────────────────────────────────────────┘       └─────────────────────────────────────
```
**What's new in this step:** stateless JWT sessions — any region validates a token locally via JWKS, so
a user can be served by (or fail over to) any region; the only server-side state, the cart, stays in
region-local Redis.

## The design
The token itself is the session, so every region verifies it locally via JWKS with nothing shared and
nothing to replicate.
```
        login ─► auth (RS256-signs JWT) ───────────────► browser holds token
                    │ publishes JWKS (public keys)
        ┌───────────┼───────────────────────────────────┐
        ▼           ▼                                    ▼
   Mumbai api-gw   UK api-gw                        SGP api-gw
   verify JWT via  verify JWT via                   verify JWT via
   JWKS (local)    JWKS (local)                     JWKS (local)   ← any region verifies, no shared secret
        │
        └─► cart state ─► Redis (per region, keyed by user)  ← region-local; cross-region only on failover
```

## How it works / why this approach
**Stateless JWT (chosen).** The token *is* the session: it carries `sub`, roles, and expiry, signed with
the auth service's private key. Every region validates it locally by fetching the public keys from the
JWKS endpoint — no shared secret, no per-region session lookup, nothing to replicate. It's the natural fit
because auth already issues RS256 JWTs with JWKS, so multi-region adds zero session plumbing.

**The one piece of server-side state — the cart — stays in Redis per region.** Three options:
- **Region-local Redis** *(chosen):* the cart is keyed by user, and geo-routing means a user is normally
  served by one region, so a region-local cart is right almost always. Simplest, fastest, no cross-region
  writes.
- **Replicate Redis cross-region:** active-active cart everywhere, but you take on replication lag and
  last-write-wins conflicts for little gain (a user is in one place at a time).
- **Rebuild on failover:** carts are short-lived and low-stakes; on the rare region failover, accept a cold
  cart, or back the cart in the replicated DB (Phase 10) so it survives. Pragmatic default.

**JWT trade-offs.** *Revocation:* you can't un-issue a signed token, so use short TTLs (e.g. 15 min) +
refresh tokens to re-mint — a banned user is locked out within one TTL. *Token size:* JWTs ride on every
request, so keep claims small. *Key rotation:* JWKS exposes multiple keys by `kid`, so auth rotates keys
without breaking live tokens.

**Why not the alternatives?**
- **Sticky sessions (LB affinity):** pins a user to one instance/region — exactly what breaks on region
  failover, and it defeats active-active. Wrong model for multi-region.
- **One global server-side session store:** every request from every region pays cross-region latency to
  read the session, and it's a global single-point-of-failure. Slow and fragile.
- **Replicated session DB across regions:** adds a whole replication system (lag, conflicts, cost) to store
  state the signed token already carries. Pure complexity.

## How to build it
1. **Confirm stateless verification at every edge**: api-gateway (each region) validates the RS256 JWT
   against the **local-cached JWKS**; reject on bad signature/expiry. No session lookup anywhere.
2. **Short-TTL access tokens + refresh tokens**: wire refresh flow so tokens stay short-lived but UX is
   seamless; document the JWKS **key-rotation** path (`kid`).
3. **Cart = region-local Redis, keyed by user**; decide failover behavior (cold rebuild, or back it in the
   replicated DB so it survives a region loss).
4. **Verify cross-region**: log in via one region, present the token to **another** region (exit check).

## Done when
- A token minted by `auth` is accepted by every region (Mumbai, UK, Singapore) with no shared secret —
  each verifies locally via JWKS.
- Forcing a user from one region to another (simulate failover) keeps them logged in; only the cart may be
  cold (or survives if backed in the replicated DB).
- A revoked/expired token is rejected within one TTL, and the refresh flow re-mints transparently.

---
> Interview one-liner: *"Auth is stateless RS256 JWTs verified via JWKS, so every region validates a token locally with no shared
> secret and no session store to replicate — a user can be served by any region or fail over freely. The
> only server-side state, the cart, lives in region-local Redis keyed by user, which is fine because
> geo-routing keeps a user in one region; I handle revocation with short TTLs plus refresh tokens, and avoid
> sticky sessions or a global session store because both break multi-region failover."*
