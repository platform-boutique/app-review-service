# Develop and test

Requirements: Go 1.26 (`GOTOOLCHAIN=auto` fetches the toolchain from `go.mod`), Docker for integration tests, `grpcurl`, `golangci-lint` v2.

Commands run on 2026-11-13 for the standards change (host: Go 1.22 with `GOTOOLCHAIN=auto` selecting 1.26.10):

| Command | Result |
|---|---|
| `go vet ./... && go test -race -count=1 ./...` | passed (2 packages with tests) |
| `golangci-lint run ./...` and `golangci-lint run --build-tags integration ./...` (golangci-lint v2.11.4, `.golangci.yaml`) | 0 issues. The shared pipeline pins v2.14.0: not run |
| `gofmt -l .` | no files listed |
| `mkdocs build --strict` (mkdocs 1.6.1, mkdocs-techdocs-core 1.7.1, Python 3.12 venv made with uv) | passed |

## What the earlier author ran (recorded, not re-run for this page)

Commands and results below come from the README before the standards change; they were not repeated on 2026-11-13. The image was not rebuilt after the `star_counts` change.

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

`star_counts` (slice 7) was verified with unit tests (in-memory store: bucket arithmetic, five zeros for unknown products) and integration tests against a throwaway `postgres:17-alpine`: zero reviews, unknown product, every seeded product's distribution equal to a direct SQL count, and 8 concurrent writers adding 200 reviews while 4 readers called `GetRatingSummary` (every answer had 5 entries summing to `count`, `count` never decreased, final counts exact). The container image was not rebuilt and the gRPC call was not made against a running container (not run).

Seed data: `python3 tools/make_reviews.py <product-catalog-service>/data/seed/products.json --spread-until 2026-10-01T00:00:00Z` (Python 3.11+, standard library). Ratings, comments and names come from the catalog dataset; because every source review has the same timestamp, `--spread-until` spreads `created_at` deterministically (SHA-256 of product id + index) over the year before that date. The output is sorted, so a re-run is byte-identical.

## Build the docs

```bash
uv venv docs-venv && uv pip install --python docs-venv/bin/python 'mkdocs==1.6.1' 'mkdocs-techdocs-core==1.7.1'
docs-venv/bin/mkdocs build --strict
```

Mermaid diagrams are plain fenced code blocks for `mkdocs`; rendering them in Backstage needs the TechDocs Mermaid add-on (not verified here).
