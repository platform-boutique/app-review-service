# 1. Reviews live in a Postgres owned by this service

Status: accepted (reconstructed from the code and the earlier README; no dated decision record existed).

## Context

Reviews are written by shoppers and read on every catalogue page. The shop's other data services (product catalog, orders, users, inventory) each keep their own database.

## Decision

review-service owns one Postgres database (table `reviews`). No other service reads it. The schema is created by embedded goose migrations run by the service at start-up, forward-only, under goose's advisory lock. The initial data is an embedded seed (`data/reviews.json`) inserted once in one transaction when the table is empty.

## Consequences

- Other services need the gRPC API; there are no cross-database joins.
- The service is not ready (`NOT_SERVING`) until the database is migrated, seeded and reachable.
- The catalog's `rating` / `review_count` are derived from this service's seed by a script; they must be regenerated when the seed changes.
- Deleting every review re-seeds on the next start.
