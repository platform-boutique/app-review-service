# Develop and test

Requirements: Go 1.26 (`GOTOOLCHAIN=auto` fetches the toolchain from `go.mod`), Docker for integration tests, `grpcurl`, `golangci-lint` v2.

Final checks of 2026-11-22 (branch `final/review-service`, host: Go 1.22 with `GOTOOLCHAIN=auto` selecting 1.26.11). The same results are in the `## Develop` table of the README.

| Command | Result |
|---|---|
| `gofmt -l .`, `go vet ./...`, `go vet -tags integration ./...` | clean |
| `go test -race -count=1 ./...` | passed (3 packages with tests; coverage `cmd/server` 0%, `internal/reviews` 57.0%, `internal/obs` 89.6%); `TestGRPCRequestPathLogsCarryTraceIDs` with `-count=50`: passed |
| `golangci-lint run ./...` and `golangci-lint run --build-tags integration ./...` (golangci-lint v2.14.0 in `golangci/golangci-lint:v2.14.0`, `.golangci.yaml`) | 0 issues both times |
| Integration tests | 10 integration tests (`TestMigrationsTwice`, `TestConcurrentStarts`, `TestAddListSummary`, `TestUnreachableDatabase`, `TestAddReviewDuplicates`, `TestSummaryZeroAndUnknownProduct`, `TestSeededStarCountsMatchSQL`, `TestSummaryConsistentUnderConcurrentAdd`, `TestDatabaseSpansAndPoolStats`, `TestRequestTimeoutEndsASlowDatabaseCall`) and all unit tests, `go test -race -count=1 -tags integration ./...`, throwaway `postgres:17-alpine` (`--memory 256m`): passed |
| `govulncheck ./...`, `gosec -exclude-generated ./...` | `No vulnerabilities found`; gosec 0 issues |
| `gitleaks git --redact` over the full history (38 commits scanned) | no leaks found |
| `docker build --pull --platform linux/amd64 ...`, `trivy image --severity CRITICAL,HIGH --exit-code 1` (0.76.1) | OK, 12.1 MB content (44 MB disk usage); 0 vulnerabilities, exit 0 |
| whole shop: `local/up.sh` (`SKIP_BUILD=1`), `local/smoke.sh` three times | `SMOKE PASSED`, 87 PASS lines each time; no error log lines of this service afterwards |
| `mkdocs build --strict` (mkdocs 1.6.1, mkdocs-techdocs-core 1.7.1) and the link check | passed |

The container run (image built with `--build-arg SERVICE_VERSION=final-test`, throwaway `postgres:17-alpine` on a private docker network, `grpcurl` with reflection): Docker health `healthy`, uid:gid `10001:10001`; `grpc.health.v1.Health/Check` `SERVING`; `ListReviews` for a seeded product (3 reviews, average 4), `GetRatingSummary` (`star_counts` `[0,0,0,3,0]`, an unknown product comes back as zeros), `AddReview` OK, the same review again `ALREADY_EXISTS`, rating 9 `INVALID_ARGUMENT`; the `starting` log line carries `version: final-test` and the commit; `/metrics` (read inside the container) has `build_info{commit,service,version}`, `db_pool_connections_max 12` and `grpc_server_handled_total` per method and code; with the database disconnected from the network health went `NOT_SERVING` and `GetRatingSummary` answered `UNAVAILABLE` (`database not ready`), after reconnecting `SERVING` again; 0 error log lines; `docker stop` exit code 0 after about 0.4 s.

Seed: `python3 tools/make_reviews.py <catalog>/data/seed/products.json --spread-until 2026-10-01T00:00:00Z` (the products file of the workspace's `apps/product-catalog-service`) wrote 351 reviews for 117 products and the file is identical to `data/reviews.json` (`cmp`).

Not run: OTLP exporters against a real collector and Pyroscope, an arm64 build, several replicas against one database, `buf` locally (the pipeline's Proto check runs it), `verify-signals.py` (needs a posted review first), any load or timing measurement.

## What the earlier author ran (recorded, not re-run for this page)

Commands and results below come from the README before the standards change; they were not repeated for this page; the current results are in the table above. A smoke run of the binary with OTLP and Pyroscope pointed at a listener that never answers (database unreachable) exited 0 about 4 s after SIGTERM. The image was rebuilt and run again on 2026-11-22 (see above).

Requirements: Go 1.26 (`GOTOOLCHAIN=auto` fetches it), Docker, [`grpcurl`](https://github.com/fullstorydev/grpcurl).

What was actually run:

```bash
# regenerate the Go code after editing proto/reviews.proto (protoc 3.21.12, protoc-gen-go v1.36.11, protoc-gen-go-grpc 1.6.1)
protoc --go_out=. --go_opt=paths=source_relative --go-grpc_out=. --go-grpc_opt=paths=source_relative proto/reviews.proto

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
`golangci-lint run` (default linters) and `gosec -exclude-generated ./...` report no issues (both re-run after the `star_counts` change).

`star_counts` (slice 7) was verified with unit tests (in-memory store: bucket arithmetic, five zeros for unknown products) and integration tests against a throwaway `postgres:17-alpine`: zero reviews, unknown product, every seeded product's distribution equal to a direct SQL count, and 8 concurrent writers adding 200 reviews while 4 readers called `GetRatingSummary` (every answer had 5 entries summing to `count`, `count` never decreased, final counts exact). The image was rebuilt and called through `grpcurl` on 2026-11-22 (see above).

Seed data: `python3 tools/make_reviews.py <product-catalog-service>/data/seed/products.json --spread-until 2026-10-01T00:00:00Z` (Python 3.11+, standard library). Ratings, comments and names come from the catalog dataset; because every source review has the same timestamp, `--spread-until` spreads `created_at` deterministically (SHA-256 of product id + index) over the year before that date. The output is sorted, so a re-run is byte-identical.

## Build the docs

```bash
uv venv docs-venv && uv pip install --python docs-venv/bin/python 'mkdocs==1.6.1' 'mkdocs-techdocs-core==1.7.1'
docs-venv/bin/mkdocs build --strict
```

Mermaid diagrams are plain fenced code blocks for `mkdocs`; rendering them in Backstage needs the TechDocs Mermaid add-on (not verified here).
