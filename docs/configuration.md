# Configuration

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

Start-up log with everything but `DATABASE_URL` unset (from the author's container smoke run, not repeated on 2026-11-13):

```
{"message":"telemetry off — set OTEL_EXPORTER_OTLP_ENDPOINT to enable","severity":"info",...}
{"message":"profiling off — set PYROSCOPE_ADDR to enable","severity":"info",...}
{"message":"metrics endpoint on :9090 (pprof: false)","severity":"info",...}
{"message":"starting grpc server on [::]:8084","severity":"info",...}
{"message":"database ready: 1 migration(s) applied, 351 seed review(s) inserted","severity":"info",...}
{"message":"health: SERVING","severity":"info",...}
```
