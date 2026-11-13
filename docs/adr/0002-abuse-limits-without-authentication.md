# 2. AddReview abuse limits without authentication

Status: accepted (reconstructed from the code and the earlier README).

## Context

The shop has no way to authenticate a reviewer at this service; the author name is text the caller sends.

## Decision

Two cheap limits: an equal review (same product, author, text; case-insensitive) is `ALREADY_EXISTS`, checked in the insert transaction under a Postgres advisory lock, with no unique index so existing duplicate rows block nothing. A per product + author rate limit (`REVIEW_RATE_LIMIT` per `REVIEW_RATE_WINDOW`, default 5 per minute) is held in memory, bounded to 50,000 buckets.

## Consequences

- It slows accidental double posts and naive scripts; it does not stop a determined attacker who changes the author name.
- The rate limit is per process: a restart resets it, N replicas allow N times the limit. A shared limiter would need a store (not planned).
- Authentication and moderation are out of scope until accounts exist at this service.
