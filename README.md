# review-service

A gRPC service that stores product reviews and star ratings for the platform-demo store: one page of a product's reviews (newest first), adding a review, and rating summaries (average + count) for many products at once, for the product grid. It owns its own Postgres database, creates its schema with embedded migrations at start-up and seeds it once from the store dataset.

## At a glance

| | |
|---|---|
| **Language / runtime** | Go 1.26 (static binary, `CGO_ENABLED=0`) |
| **Image** | built locally only: `review-service:local` |
| **Protocol / port** | gRPC (HTTP/2, plaintext) on `8084` |
| **Metrics** | Prometheus `/metrics` on `9090`; pushed over OTLP/gRPC only when an endpoint is set |
| **Health** | Standard `grpc.health.v1.Health/Check` on `8084`: `NOT_SERVING` until migrations + seed are done and whenever the database does not answer a ping (checked every 5 s) |
| **State** | Postgres (`reviews` table), via `DATABASE_URL` |
| **Depends on** | Its own Postgres (optional: OTLP collector, Pyroscope) |
| **Called by** | nobody yet (frontend: planned in slice 2) |
| **gRPC reflection** | Enabled: `grpcurl` works without `-proto` |

## API

Package `hipstershop`, service `ReviewService` ([`proto/reviews.proto`](proto/reviews.proto)).

| Method | Request | Response | Behaviour |
|---|---|---|---|
| `ListReviews` | `{ product_id, int32 page, int32 page_size }` | `{ repeated Review reviews, int32 total, double average_rating }` | Newest first (`created_at DESC`, id as tie-break). `page` is 1-based, `0` means 1; `page_size` `0` means 10, above 50 is capped at 50. `total` and `average_rating` (1 decimal, 0 if none) cover all reviews of the product. A page past the end returns no reviews. Empty `product_id` or negative values: `INVALID_ARGUMENT` |
| `AddReview` | `{ product_id, author_name, int32 rating, comment }` | `Review` | Text fields are trimmed; `product_id` 1-128, `author_name` 1-80, `comment` 1-2000 characters (runes), `rating` 1-5, no NUL characters, otherwise `INVALID_ARGUMENT`. The server sets `id` (UUID) and `created_at` |
| `GetRatingSummary` | `{ repeated string product_ids }` (max 200) | `{ repeated RatingSummary summaries }` | One entry per distinct id, in request order; `average` has 1 decimal; products without reviews come back with `count` 0 (and `average` 0). More than 200 ids or an empty id: `INVALID_ARGUMENT` |

`Review` = `{ id, product_id, author_name, int32 rating, comment, google.protobuf.Timestamp created_at }`.
Errors: `UNAVAILABLE` while the database is not ready/reachable, `INTERNAL` (without SQL details, which are logged) for other storage errors. A recovery interceptor turns a handler panic into `INTERNAL`. Product ids are not checked against the catalog.

Examples (run against the container, see below):

```bash
grpcurl -plaintext -d '{"product_id":"blue-frock-162","page_size":2}' localhost:8084 hipstershop.ReviewService/ListReviews
grpcurl -plaintext -d '{"product_id":"blue-frock-162","author_name":"Smoke Test","rating":5,"comment":"Works in the container."}' \
  localhost:8084 hipstershop.ReviewService/AddReview
grpcurl -plaintext -d '{"product_ids":["blue-frock-162","no-such-product"]}' localhost:8084 hipstershop.ReviewService/GetRatingSummary
grpcurl -plaintext localhost:8084 grpc.health.v1.Health/Check
```

## Configuration

| Variable | Default | Description |
|---|---|---|
| `DATABASE_URL` | **required** | Postgres URL, e.g. `postgres://review:review@review-db:5432/reviews?sslmode=disable`. The process exits if it is unset. A 5 s connect timeout is added unless the URL sets one |
| `PORT` | `8084` | gRPC listen port |
| `METRICS_PORT` | `9090` | Prometheus (and, with profiling, pprof) HTTP port |
| `OTEL_EXPORTER_OTLP_ENDPOINT` | unset (telemetry off) | OTLP/**gRPC** endpoint for traces, metrics and logs (scheme required). Legacy fallback: `COLLECTOR_SERVICE_ADDR` (`host:port`). Telemetry is on only if one of them is set and non-empty |
| `OTEL_SDK_DISABLED` | — | `true` forces telemetry off even with an endpoint (the legacy `ENABLE_TRACING=0` does the same) |
| `OTEL_METRIC_EXPORT_INTERVAL` | `15000` | Milliseconds between metric pushes |
| `OTEL_SERVICE_NAME` | `review-service` | `service.name` |
| `PYROSCOPE_ADDR` | unset (profiling off) | Profiling (Pyroscope push **and** the pprof endpoints on `METRICS_PORT`) is on only if this is set and `ENABLE_PROFILING` is not `0` |
| `ENABLE_PROFILING` | — | `0` forces profiling off |

With everything but `DATABASE_URL` unset the service starts without connecting to any collector (log of the container smoke run):

```
{"message":"telemetry off — set OTEL_EXPORTER_OTLP_ENDPOINT to enable","severity":"info",...}
{"message":"profiling off — set PYROSCOPE_ADDR to enable","severity":"info",...}
{"message":"metrics endpoint on :9090 (pprof: false)","severity":"info",...}
{"message":"starting grpc server on [::]:8084","severity":"info",...}
{"message":"database ready: 1 migration(s) applied, 351 seed review(s) inserted","severity":"info",...}
{"message":"health: SERVING","severity":"info",...}
```

Start-up: the gRPC server listens at once (health `NOT_SERVING`); a background loop runs the goose migrations (under goose's Postgres session advisory lock) and the seed, retrying every 2 s until the database is reachable. The seed (`data/reviews.json`, embedded) is inserted in one transaction under `pg_advisory_xact_lock` only if the `reviews` table is empty. SIGTERM/SIGINT: health goes `NOT_SERVING`, `GracefulStop` (10 s cap), then the pool is closed and the process exits 0.

## Container image

Multi-stage [`Dockerfile`](Dockerfile): `golang:1.26-alpine` builder (`--platform=$BUILDPLATFORM`, `GOARCH=$TARGETARCH`, so no hard-coded architecture), `alpine:3.24` runtime with the single binary at `/app/review-service`.

- **User:** `10001:10001` (numeric, non-root).
- **HEALTHCHECK:** `/app/review-service healthcheck` calls the gRPC health service on `127.0.0.1:$PORT` and exits 0 only on `SERVING`, so the image needs no extra tool.
- **EXPOSE:** `8084`, `9090`. `.dockerignore` keeps `.git`, tests and tools out of the context.
- Built once locally on amd64 (`docker build -t review-service:local .`, about 12 MB); arm64 was not built.

## Not deployed anywhere yet

No Helm chart, pipeline, Backstage entry or GitHub repository exists yet; the service is only built and run locally (docker compose in `local/` is the first target).

## Running locally

Requirements: Go 1.26 (`GOTOOLCHAIN=auto` fetches it), Docker, [`grpcurl`](https://github.com/fullstorydev/grpcurl).

What was actually run:

```bash
# unit tests (validation, pagination, summaries, error mapping, telemetry switches) + vet
go vet ./... && go test ./...

# integration tests against a throwaway Postgres (each test creates and drops its own database)
docker run -d --rm --name review-it-pg -e POSTGRES_USER=review -e POSTGRES_PASSWORD=review \
  -p 127.0.0.1:55432:5432 postgres:17-alpine
TEST_DATABASE_URL='postgres://review:review@127.0.0.1:55432/postgres?sslmode=disable' \
  go test -race -tags integration ./...

# container smoke run: image + that Postgres on one docker network
docker build -t review-service:local .
docker network create review-smoke && docker network connect --alias review-db review-smoke review-it-pg
docker run -d --name review-smoke-svc --network review-smoke -p 127.0.0.1:58084:8084 \
  -e DATABASE_URL='postgres://review:review@review-db:5432/postgres?sslmode=disable' review-service:local
```

The smoke run checked: health `SERVING` and Docker health `healthy`, the three RPCs above, `INVALID_ARGUMENT` for rating 9, health `NOT_SERVING` + `UNAVAILABLE` after disconnecting the database from the network and `SERVING` again after reconnecting it, and exit code 0 on `docker stop`.
`golangci-lint run` (default linters) and `gosec -exclude-generated ./...` report no issues.

Seed data: `python3 tools/make_reviews.py <product-catalog-service>/data/seed/products.json --spread-until 2026-10-01T00:00:00Z` (Python 3.11+, standard library). Ratings, comments and names come from the catalog dataset; because every source review has the same timestamp, `--spread-until` spreads `created_at` deterministically (SHA-256 of product id + index) over the year before that date. The output is sorted, so a re-run is byte-identical.

## Project structure

```
├── cmd/server/
│   ├── main.go            # entrypoint: telemetry, profiling, gRPC server, DB watcher, graceful shutdown
│   ├── otlp.go            # OTLP traces/metrics/logs (same as shipping-service, plus OTEL_SDK_DISABLED)
│   └── healthcheck.go     # `review-service healthcheck` for the container HEALTHCHECK
├── internal/reviews/
│   ├── service.go         # gRPC handlers, error mapping
│   ├── validate.go        # request limits, paging, rounding
│   ├── store.go           # Postgres store (pgx/v5)
│   ├── migrate.go         # goose migrations, seed, Setup
│   └── migrations/        # embedded SQL migrations
├── data/                  # reviews.json (embedded seed) + data.go
├── proto/                 # reviews.proto + generated Go
├── tests/integration/     # tests against a real Postgres (build tag `integration`)
├── testdata/              # small fixture products.json and the reviews made from it
├── tools/make_reviews.py  # products.json -> data/reviews.json
└── Dockerfile
```

## Known limitations

- No authentication, rate limiting or moderation: anyone who can reach the port can add reviews; product ids are not checked against the catalog.
- The seed runs whenever the table is empty, so deleting every review re-seeds on the next start.
- `ListReviews` reads the totals and the page in two statements without a transaction; a concurrent `AddReview` can make `total` differ by one from the page.
- Database calls do not produce trace spans (no pgx tracer).
- Offset paging (page limit 1,000,000); fine for a few reviews per product.
- The smoke-run image was built before the last change of the "database not ready" error message (code-only change, unit tests re-run).
