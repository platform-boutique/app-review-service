# Operations

## Health

- `grpc.health.v1.Health/Check` on the gRPC port (`8084`), for the empty service name and for `hipstershop.ReviewService`.
- `NOT_SERVING` from start until migrations and seed are done, and again whenever the Postgres ping fails (checked every 5 s). While not ready the handlers answer `UNAVAILABLE`.
- Container probe: `/app/review-service healthcheck` calls the local health service (`127.0.0.1:$PORT`) and exits 0 only on `SERVING` (Docker `HEALTHCHECK`, interval 5 s, start period 10 s).

```bash
grpcurl -plaintext localhost:8084 grpc.health.v1.Health/Check
```

## Shutdown

SIGTERM or SIGINT: health goes `NOT_SERVING` so probes fail first; `GracefulStop` with a 10 s cap, then `Stop`; the metrics server is shut down (5 s); telemetry is flushed (5 s); the pool is closed; exit 0.

Measured earlier by the author: `docker stop` exit code 0 (image smoke run). Not re-measured with a blackholed OTLP endpoint.

## Scaling and replicas

- Postgres is the only state. Several replicas are possible in principle: migrations and seed take advisory locks, identical `AddReview` calls are serialised by an advisory lock in the database.
- The `AddReview` rate limit is **per process**: a restart resets it and N replicas allow N times the limit.
- Running more than one replica was not tested.
- Resource use in the local stack: `mem_limit: 128m` (workspace `local/compose.yaml`).

## Failure behaviour

| Failure | Behaviour |
|---|---|
| Postgres down at start | server listens, health `NOT_SERVING`, migration and seed retried every 2 s |
| Postgres goes away while running | health `NOT_SERVING` within about 5 s, handlers answer `UNAVAILABLE`, `SERVING` again after it returns |
| Handler panic | turned into `INTERNAL` by a recovery interceptor; the process keeps running |
| Collector unreachable | telemetry export fails in the background; start-up does not depend on it |

## Runbook

| Symptom | Check |
|---|---|
| Health stays `NOT_SERVING` | logs for `database not ready (attempt N)`: wrong `DATABASE_URL`, network, credentials |
| Users get `RESOURCE_EXHAUSTED` on posting | the per product + author limit (`REVIEW_RATE_LIMIT` per `REVIEW_RATE_WINDOW`); `REVIEW_RATE_LIMIT=0` switches it off |
| Stars on the grid disagree with the catalog | the catalog's `rating` and `review_count` are derived from this service's seed; re-run `tools/sync_reviews.py` of the catalog after changing `data/reviews.json` |
| Process exits at start | `DATABASE_URL is not set` |

There is no dashboard or alert for this service yet.
