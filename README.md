# review-service

## Purpose and place in the shop

A gRPC service that stores product reviews and star ratings for the Everly shop: one page of a product's reviews (newest first), adding a review, and rating summaries (average, count and the per-star distribution) for many products at once, for the product grid. It owns its own Postgres database, creates its schema with embedded migrations at start-up and seeds it once from the store dataset. It calls no other service.

Context diagram and the main flow as sequence diagram: [`docs/architecture.md`](docs/architecture.md).

| | |
|---|---|
| Language / runtime | Go 1.26 (`go.mod`: `go 1.26.11`), static binary, `CGO_ENABLED=0` |
| Image | built locally only: `platform-demo/review-service:local` (`Dockerfile`, user `10001:10001`) |
| Ports | gRPC `8084` (plaintext), HTTP `9090` (`/metrics`, scraped by Alloy; pprof only on its own `PPROF_ADDR` listener) |
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
| `REVIEW_RATE_LIMIT` | `5` | no | `AddReview` limit per product + author per window; `0` switches the limit off. A value that is not a non-negative integer keeps the default (and logs a warning), it never disables the limit |
| `REVIEW_RATE_WINDOW` | `1m` | no | Window of the limit, a Go duration (`30s`, `2m`). Invalid or non-positive: default and a warning |
| `REQUEST_TIMEOUT` | `5s` | no | Server-side bound of every unary call (Go duration, at least `100ms`; `0` switches the bound off). A call without a deadline gets this one, a longer caller deadline is shortened to it; the context reaches every database query. A value that does not parse keeps `5s` with a warning |
| `HOSTNAME` | OS hostname | no | Used as `service.instance.id` (the pod name in Kubernetes) |
| `METRICS_PORT` | `9090` | no | Port of the Prometheus `/metrics` endpoint (scraped by Alloy). A value that is not a port falls back to `9090` with a warning |
| `PPROF_ADDR` | unset (no pprof) | no | `host:port` of a separate pprof listener, e.g. `127.0.0.1:6060`. Off unless set; never expose it. pprof is never served on the metrics port |
| `LOG_LEVEL` | `info` | no | `debug`, `info`, `warn` or `error`; anything else falls back to `info` with a warning |
| `SERVICE_VERSION` | build info, else `dev` | no | `service.version`, the `version` log field and the `build_info` label. Fallbacks in order: ldflags `-X <module>/internal/obs.Version=...` (and `.Commit=`, `.BuildTime=`), the VCS revision Go embeds in the binary (12 characters, `-dirty` if modified), `dev` |
| `DEPLOYMENT_ENVIRONMENT` | `local` | no | `deployment.environment` on every signal and the Pyroscope tag `environment` |
| `OTEL_EXPORTER_OTLP_ENDPOINT` | unset (telemetry off) | no | OTLP/**gRPC** endpoint for **traces, metrics and logs**, e.g. `http://alloy:4317`. Telemetry is on only if this is a valid absolute http(s) URL; empty or malformed means off (one warning, never a crash). The legacy `COLLECTOR_SERVICE_ADDR` and `ENABLE_TRACING` are no longer read |
| `OTEL_SDK_DISABLED` | unset | no | `true` (trimmed, case-insensitive) forces everything off: OTLP export and profiling, even if an endpoint or `PYROSCOPE_ADDR` is set |
| `OTEL_EXPORTER_OTLP_PROTOCOL` | `grpc` | no | Documented knob; only gRPC is implemented, the value is not read |
| `OTEL_TRACES_SAMPLER`, `OTEL_TRACES_SAMPLER_ARG` | SDK default (`parentbased_always_on`) | no | Standard OpenTelemetry sampler variables, read by the SDK (tested for `always_on` and `always_off`) |
| `OTEL_RESOURCE_ATTRIBUTES` | unset | no | Extra resource attributes (`k=v,...`); applied after the defaults, so they can override them |
| `OTEL_SERVICE_NAME` | `review-service` | no | `service.name`; keep the default, dashboards depend on it |
| `OTEL_METRIC_EXPORT_INTERVAL` | `15000` | no | Milliseconds between metric pushes (also accepts durations such as `15s`) |
| `PYROSCOPE_ADDR` | unset (profiling off) | no | Pyroscope URL (SDK push protocol, not OTLP). The only switch: no default address, `ENABLE_PROFILING=1` alone starts nothing |
| `ENABLE_PROFILING` | unset | no | `0` forces profiling off even with `PYROSCOPE_ADDR`; any other value changes nothing |

The OTLP exporters of the OpenTelemetry Go SDK also read their own standard `OTEL_EXPORTER_OTLP_*` variables (headers, TLS, timeout); they are not read by this service's code and were not tested. The sampler is the SDK's (`OTEL_TRACES_SAMPLER`, default `parentbased_always_on`).

With everything but `DATABASE_URL` unset the service starts without connecting to any collector. Start-up: the gRPC server listens at once (health `NOT_SERVING`); a background loop runs the goose migrations and the seed, retrying every 2 s until the database is reachable.

## Observability

| Signal | Implementation | Destination |
|---|---|---|
| **Traces** | OpenTelemetry (`otelgrpc` handlers), OTLP/gRPC exporter (batch processor, queue bounded at 2048), W3C TraceContext + Baggage propagated to every downstream call. Sampler from `OTEL_TRACES_SAMPLER` (SDK default `parentbased_always_on`) | Alloy `:4317` -> Tempo |
| **Metrics** | Prometheus default registry: `grpc_server_*` (go-grpc-prometheus with handling-time histogram), Go runtime and process metrics. Served as Prometheus text on `:9090/metrics` (`METRICS_PORT`) and, because the same registry is bridged into OpenTelemetry, also pushed over OTLP every 15 s (metric names identical). Plus `build_info{service,version,commit} 1` | Alloy scrapes `:9090` -> Mimir |
| **Logs** | `logrus`, one JSON object per line on stdout, plus an OTLP/gRPC hook (`otellogrus`) | stdout -> Alloy -> Loki; OTLP -> Alloy -> Loki |
| **Profiles** | Pyroscope Go SDK (CPU, alloc/in-use objects and space, goroutines, mutex, block), only with `PYROSCOPE_ADDR`; tags `service_name`, `version`, `environment` | Pyroscope |

**Telemetry contract** (the code is `internal/obs`, the same package in every Go service): telemetry is on only if `OTEL_EXPORTER_OTLP_ENDPOINT` is a valid absolute http(s) URL; `OTEL_SDK_DISABLED=true` forces everything off (profiling too); profiling only with `PYROSCOPE_ADDR`. Start never waits for the collector (the exporters connect in the background) and on SIGTERM the flush is cut off after 4 s, so a blackholed collector cannot hold the process (unit-tested with a listener that never answers: start and shutdown both return within the bound).

**Resource attributes** on every signal: `service.name` (`OTEL_SERVICE_NAME`, default `review-service`), `service.version` (`SERVICE_VERSION`, else build info, else `dev`), `service.namespace=everly`, `deployment.environment` (`DEPLOYMENT_ENVIRONMENT`, default `local`), `service.instance.id` (hostname). `OTEL_RESOURCE_ATTRIBUTES` is applied last.

**Log shape** (one shape for all services): `timestamp` (RFC 3339, UTC, milliseconds), `severity` (`debug`, `info`, `warning`, `error`), `message`, `service`, `version`, `trace_id` and `span_id` (hex, only while a valid span is active, never empty or all zeros), `request_id` (when the caller sent one), `error` (string, on errors), plus the call's own fields (for example `order_id`). Level from `LOG_LEVEL`. `trace_id`/`span_id` come from the context of the log call: handlers and clients log with `log.WithContext(ctx)`, the formatter reads the span from it. Calls without a context (startup, background work) have no ids. No card data, tokens or e-mail addresses are logged.

**Startup and version**: the first line is `message: "starting"` with `version`, `commit` and `environment`; `version`/`commit` come from `SERVICE_VERSION`, ldflags `-X <module>/internal/obs.Version=` / `.Commit=`, or the VCS data Go embeds (no Dockerfile change was made: without any of them a container image reports `version: "dev"`, `commit: "unknown"`). Panics are logged as one JSON line (`message: "panic"`, `error`) before the process dies; a startup error is one JSON error line, exit 1.

**pprof** is off. `PPROF_ADDR=127.0.0.1:6060` starts a separate listener; it is never on the metrics port.

**Graceful shutdown** (SIGTERM or SIGINT): health goes `NOT_SERVING`, `GracefulStop` with a 5 s cap (then `Stop`), then the telemetry flush runs, cut off after 4 s, the pool is closed: the process exits within 9 s even when the collector is blackholed. Run against a listener that never answers (OTLP and Pyroscope both pointed at it, database unreachable) the binary exited 0 about 4 s after SIGTERM (smoke run, not part of the unit tests; with a reachable database not re-measured).

**Not there yet** (standards section 6): spans for Postgres calls, DB pool stats and business counters (reviews added, rejected by reason); `build_info` is there. The handlers log product id, review id and rating only; author names and comments (free text from visitors) are not logged, and the new log-shape test checks that.

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

| Step | Command | Result on 2026-11-13 (rows marked 2026-11-21 were run again on `origin/main`) |
|---|---|---|
| Vet + unit tests | `go vet ./... && go test -race -count=1 ./...` | run, passed; run again 2026-11-21: passed |
| Format | `gofmt -l .` | run, no files listed; run again 2026-11-21: none |
| Lint | `golangci-lint run ./...` (config `.golangci.yaml`, v2 default linters) | run with golangci-lint v2.11.4, 0 issues; also with `--build-tags integration`, 0 issues. CI pins v2.14.0: not run |
| Integration (Postgres) | see below | run on 2026-11-21 against a throwaway `postgres:17-alpine`: passed (earlier runs by the author are recorded in `docs/development.md`) |
| Vulnerability / security scan | `govulncheck ./...`, `gosec -exclude-generated ./...` | not run today |
| Image | `docker build -t platform-demo/review-service:local .` | not run today |
| Docs | `mkdocs build --strict` (mkdocs 1.6.1 + mkdocs-techdocs-core 1.7.1, Python 3.12 venv made with uv) and the link check `python3 -I tools/check-md-links.py .` from `platform-demo-workflows` (relative links in `README.md` and `docs/`) | run, passed; run again 2026-11-21, both passed |
| Image scan | `trivy image --severity CRITICAL,HIGH --exit-code 1 platform-demo/review-service:local` | not run today |
| Secret scan | `pre-commit run --all-files` (`.pre-commit-config.yaml`, gitleaks) or `gitleaks git --redact` | run 2026-11-21 with the gitleaks binary of the pre-commit cache (`gitleaks git --redact`, full history, 31 commits): no leaks found |
| Workflow lint | `actionlint` | run 2026-11-21 (actionlint 1.7.12), no findings |

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
- **Shutdown:** on SIGTERM/SIGINT health goes `NOT_SERVING`, `GracefulStop` with a 5 s cap (then `Stop`), the pool is closed, telemetry is flushed (cut off after 4 s), the metrics listener stops, exit 0. At most 9 s in total (5 s drain + 4 s flush) even with a blackholed collector.
- **Scaling:** stateless except for Postgres, several replicas are possible (migrations and seed take advisory locks). The `AddReview` rate limit is **per process**: N replicas allow N times the limit. Multi-replica operation was not run.
- **Image:** Alpine runtime, user `10001:10001`, `EXPOSE 8084 9090`, about 12 MB when last built.
- Failure behaviour and runbook: [`docs/operations.md`](docs/operations.md).

## Known limits

- No authentication or moderation: anyone who can reach the port can add reviews. Duplicates and bursts per product+author are limited, but the rate limit is per process and keyed on a name the caller chooses; product ids are not checked against the catalog.
- The seed runs whenever the table is empty.
- `ListReviews` reads the totals and the page in two statements without a transaction; a concurrent `AddReview` can make `total` differ by one from the page.
- No spans for database calls, no DB pool stats or business counters. The version/commit is logged at start and in `build_info`, but the image build sets no ldflags, so a container reports `dev` unless `SERVICE_VERSION` is set.
- Offset paging (page limit 1,000,000); fine for a few reviews per product.
- Not run: arm64 build, more than one replica, the image after the latest code changes (the last image smoke run is recorded in `docs/development.md`).

## License

Apache License 2.0, see [LICENSE](LICENSE).
