# review-service

## Purpose and place in the shop

A gRPC service that stores product reviews and star ratings for the Everly shop: one page of a product's reviews (newest first), adding a review, and rating summaries (average, count and the per-star distribution) for many products at once, for the product grid. It owns its own Postgres database, creates its schema with embedded migrations at start-up and seeds it once from the store dataset. It calls no other service.

Context diagram and the main flow as sequence diagram: [`docs/architecture.md`](docs/architecture.md).

| | |
|---|---|
| Language / runtime | Go 1.26 (`go.mod`: `go 1.26.0`, `toolchain go1.26.10`), static binary, `CGO_ENABLED=0` |
| Image | built locally only: `platform-demo/review-service:local` (`Dockerfile`, user `10001:10001`) |
| Ports | gRPC `8084` (plaintext), HTTP `9090` (`/metrics`, pprof only with profiling) |
| Health | `grpc.health.v1.Health/Check` on `8084`, `NOT_SERVING` until migrations + seed are done and whenever the database does not answer a ping (every 5 s) |
| State | Postgres (`reviews` table) via `DATABASE_URL` |
| Depends on | its own Postgres (required); OTLP collector and Pyroscope (optional) |
| Called by | frontend (`GetRatingSummary`, `ListReviews`, `AddReview`; see `local/compose.yaml` and the frontend repo) |
| gRPC reflection | enabled |

## Status

No CI pipeline, GitHub repository, Helm chart or Backstage registration exists yet; the service is built and run locally only. Status badges are added when the repo has a pipeline.

## Quick start

In the platform-demo workspace (this repo is built from `apps/review-service` by `local/compose.yaml`; the compose commands were not run for this README):

```bash
local/up.sh                      # builds all images and starts the shop, http://localhost:8080
SKIP_BUILD=1 local/up.sh         # reuse images
local/rebuild.sh review-service  # rebuild and recreate only this service
```

Run the tests (run on 2026-11-13, see Develop):

```bash
GOTOOLCHAIN=auto go vet ./... && GOTOOLCHAIN=auto go test -race -count=1 ./...
```

## API

Package `hipstershop`, service `ReviewService` ([`proto/reviews.proto`](proto/reviews.proto)). Full rules, abuse limits and `grpcurl` examples: [`docs/api.md`](docs/api.md).

| Method | Request | Response | Behaviour |
|---|---|---|---|
| `ListReviews` | `product_id`, `page`, `page_size` | `reviews[]`, `total`, `average_rating` | Newest first. `page` is 1-based (`0` means 1), `page_size` `0` means 10, above 50 is capped. Empty `product_id` or negative values: `INVALID_ARGUMENT` |
| `AddReview` | `product_id`, `author_name`, `rating`, `comment` | `Review` | Validated and trimmed (`rating` 1-5, name 1-80, comment 1-2000 characters). An equal review is `ALREADY_EXISTS`; too many from one author on one product is `RESOURCE_EXHAUSTED` |
| `GetRatingSummary` | `product_ids[]` (max 200) | `summaries[]` | One entry per distinct id in request order: `average`, `count`, `star_counts` (5 entries, 1-star first). Unknown products return zeros |

Errors: `UNAVAILABLE` while the database is not ready or reachable; `INTERNAL` (no SQL details, they are logged) for other storage errors; a recovery interceptor turns a handler panic into `INTERNAL`. Product ids are case sensitive and not checked against the catalog.

## Configuration

Every variable read by the code (`grep -rn 'Getenv\|getenv'` over `cmd/` and `internal/`, checked 2026-11-13). All configuration is by environment; there is no config file.

| Variable | Default | Required | Meaning |
|---|---|---|---|
| `DATABASE_URL` | none | **yes** | Postgres URL, e.g. `postgres://review:review@review-db:5432/reviews?sslmode=disable`. The process logs an error and exits 1 if it is unset. A 5 s connect timeout is added unless the URL sets one |
| `PORT` | `8084` | no | gRPC listen port. The `healthcheck` subcommand reads it too |
| `METRICS_PORT` | `9090` | no | HTTP port for `/metrics` (and pprof when profiling is on) |
| `REVIEW_RATE_LIMIT` | `5` | no | `AddReview` limit per product + author per window; `0` switches the limit off. A value that is not a non-negative integer keeps the default (and logs a warning), it never disables the limit |
| `REVIEW_RATE_WINDOW` | `1m` | no | Window of the limit, a Go duration (`30s`, `2m`). Invalid or non-positive: default and a warning |
| `OTEL_EXPORTER_OTLP_ENDPOINT` | unset (telemetry off) | no | OTLP/**gRPC** endpoint URL with scheme for traces, metrics and logs |
| `OTEL_SDK_DISABLED` | unset | no | `true` forces telemetry off even with an endpoint |
| `OTEL_SERVICE_NAME` | `review-service` | no | `service.name` resource attribute |
| `OTEL_METRIC_EXPORT_INTERVAL` | `15000` | no | Metric push interval: a number of milliseconds, or a Go duration (`15s`). Invalid values fall back to 15 s |
| `PYROSCOPE_ADDR` | unset (profiling off) | no | Pyroscope server URL. Profiling (push and pprof endpoints) is on only if this is set |
| `ENABLE_PROFILING` | unset | no | `0` forces profiling off even with `PYROSCOPE_ADDR` |
| `ENABLE_TRACING` | unset | no | Legacy: `0` forces telemetry off |
| `COLLECTOR_SERVICE_ADDR` | unset | no | **Legacy, to be removed** (owner decision 2026-11-13): `host:port` of a collector; turns telemetry on without `OTEL_EXPORTER_OTLP_ENDPOINT` |
| `HOSTNAME` | OS hostname | no | Used as `service.instance.id` (the pod name in Kubernetes) |
| `OTEL_RESOURCE_ATTRIBUTES` | unset | no | Read by the OTel SDK resource detector (`resource.WithFromEnv`), not by this code directly |

The OTLP exporters of the OpenTelemetry Go SDK also read their own standard `OTEL_EXPORTER_OTLP_*` variables (headers, TLS, timeout); they are not read by this service's code and were not tested. `OTEL_TRACES_SAMPLER` is **not** honoured: the sampler is fixed to always-on (`otlp.go`). `LOG_LEVEL` does not exist: the log level is logrus' default (info).

With everything but `DATABASE_URL` unset the service starts without connecting to any collector. Start-up: the gRPC server listens at once (health `NOT_SERVING`); a background loop runs the goose migrations and the seed, retrying every 2 s until the database is reachable.

## Observability

What the service emits today (code read 2026-11-13; the compose observability profile was not run for this README):

| Signal | Today | Not there yet (standards section 6) |
|---|---|---|
| Traces | OTLP/gRPC, only when `OTEL_EXPORTER_OTLP_ENDPOINT` is set. A server span per inbound gRPC call (`otelgrpc`), W3C `traceparent` + `baggage` propagation, sampler always-on | spans for Postgres calls |
| Metrics | Prometheus on `:9090/metrics`: `grpc_server_*` (go-grpc-prometheus with handling-time histogram), Go runtime and process metrics. The same registry is pushed over OTLP when the endpoint is set | `build_info`, DB pool stats, business counters (reviews added, rejected by reason) |
| Logs | JSON on stdout, one object per line: `timestamp` (RFC3339Nano), `severity`, `message` plus fields. Mirrored over OTLP when the endpoint is set | `trace_id` / `span_id` on the stdout line, `service`, `version`, `LOG_LEVEL` |
| Profiles | Pyroscope push (CPU, alloc objects/space, inuse objects/space) and pprof on the metrics port, only with `PYROSCOPE_ADDR` | goroutine / mutex / block profiles, tags `version`, `environment` |

Resource attributes today: `service.name`, `service.instance.id` and the SDK's telemetry attributes (plus whatever `OTEL_RESOURCE_ATTRIBUTES` adds). `service.version`, `service.namespace` and `deployment.environment` are not set. Review texts and author names are not logged by the handlers' normal path (not audited line by line).

Example queries (not run against a live backend): `sum by (grpc_method, grpc_code) (rate(grpc_server_handled_total{grpc_service="hipstershop.ReviewService"}[5m]))`.

## Data

Postgres, database owned by this service. One table `reviews` (`id uuid`, `product_id`, `author_name`, `rating smallint 1-5`, `comment`, `created_at timestamptz`) with check constraints that repeat the validation limits, and one index `(product_id, created_at DESC, id DESC)` for the newest-first listing.

Migrations: [goose](https://github.com/pressly/goose) SQL files in `internal/reviews/migrations/`, embedded in the binary and applied at start-up under goose's advisory lock, forward-only (`00001_create_reviews.sql`).

Seed: `data/reviews.json` (embedded, 351 reviews in the last smoke run) is inserted in one transaction under `pg_advisory_xact_lock` only if the `reviews` table is empty. Deleting every review therefore re-seeds on the next start. The seed is generated from the catalog dataset:

```bash
python3 tools/make_reviews.py <product-catalog-service>/data/seed/products.json --spread-until 2026-10-01T00:00:00Z   # not run today
```

Ratings, comments and names come from the catalog dataset; `--spread-until` spreads `created_at` deterministically over the year before that date, so a re-run is byte-identical. The catalog's own `rating` and `review_count` are derived from this seed (`apps/product-catalog-service/tools/sync_reviews.py`): re-run it whenever `data/reviews.json` changes, otherwise the "Top rated" sort and the displayed stars disagree.

## Develop

Requirements: Go 1.26 (`GOTOOLCHAIN=auto` fetches it), Docker for integration tests, [`grpcurl`](https://github.com/fullstorydev/grpcurl) for manual calls. Layout: `cmd/server/` (entry point, telemetry, health), `internal/reviews/` (service, validation, rate limit, store, migrations, seed), `data/` (embedded seed), `proto/`, `tests/integration/`, `testdata/`, `tools/`.

| Step | Command | Result on 2026-11-13 |
|---|---|---|
| Vet + unit tests | `go vet ./... && go test -race -count=1 ./...` | run, passed |
| Format | `gofmt -l .` | run, no files listed |
| Lint | `golangci-lint run ./...` (config `.golangci.yaml`, v2 default linters) | run with golangci-lint v2.11.4, 0 issues; also with `--build-tags integration`, 0 issues. CI pins v2.14.0: not run |
| Integration (Postgres) | see below | not run today (earlier runs by the author are in `docs/development.md`) |
| Vulnerability / security scan | `govulncheck ./...`, `gosec -exclude-generated ./...` | not run today |
| Image | `docker build -t platform-demo/review-service:local .` | not run today |
| Docs | `mkdocs build --strict` (mkdocs 1.6.1 + mkdocs-techdocs-core 1.7.1, Python 3.12 venv made with uv) | run, passed |

Integration tests (build tag `integration`, each test creates and drops its own database):

```bash
docker run -d --rm --name review-it-pg --memory 256m -e POSTGRES_USER=review -e POSTGRES_PASSWORD=review \
  -p 127.0.0.1:55432:5432 postgres:17-alpine
TEST_DATABASE_URL='postgres://review:review@127.0.0.1:55432/postgres?sslmode=disable' \
  go test -race -tags integration ./...
docker rm -f review-it-pg
```

Regenerate the Go code after editing the proto: `protoc --go_out=. --go_opt=paths=source_relative --go-grpc_out=. --go-grpc_opt=paths=source_relative proto/reviews.proto` (protoc 3.21.12, protoc-gen-go v1.36.11, protoc-gen-go-grpc 1.6.1; not run today). Proto changes are additive only, server first.

## Operate

- **Health:** `grpc.health.v1` on the service port, for the empty service name and `hipstershop.ReviewService`. `NOT_SERVING` until migrations and seed are done, and again whenever the Postgres ping fails (every 5 s); handlers answer `UNAVAILABLE` meanwhile. The image `HEALTHCHECK` runs `/app/review-service healthcheck` (exit 0 only on `SERVING`).
- **Shutdown:** on SIGTERM/SIGINT health goes `NOT_SERVING`, `GracefulStop` with a 10 s cap (then `Stop`), the metrics server stops (5 s), telemetry is flushed (5 s), the pool is closed, exit 0.
- **Scaling:** stateless except for Postgres, several replicas are possible (migrations and seed take advisory locks). The `AddReview` rate limit is **per process**: N replicas allow N times the limit. Multi-replica operation was not run.
- **Image:** Alpine runtime, user `10001:10001`, `EXPOSE 8084 9090`, about 12 MB when last built.
- Failure behaviour and runbook: [`docs/operations.md`](docs/operations.md).

## Known limits

- No authentication or moderation: anyone who can reach the port can add reviews. Duplicates and bursts per product+author are limited, but the rate limit is per process and keyed on a name the caller chooses; product ids are not checked against the catalog.
- The seed runs whenever the table is empty.
- `ListReviews` reads the totals and the page in two statements without a transaction; a concurrent `AddReview` can make `total` differ by one from the page.
- No spans for database calls; no `trace_id` in logs; no version/commit exposed.
- Offset paging (page limit 1,000,000); fine for a few reviews per product.
- Not run: arm64 build, more than one replica, the image after the latest code changes (the last image smoke run is recorded in `docs/development.md`). The legacy `COLLECTOR_SERVICE_ADDR` is still read (to be removed).

## License

Apache License 2.0, see [LICENSE](LICENSE).
