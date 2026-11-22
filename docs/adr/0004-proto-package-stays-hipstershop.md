# 4. The proto package stays `hipstershop`

Status: accepted exception (owner informed 2026-11-22; application standards section 3, rule CODE-PROTO).

## Context

The standard for a service's own contract is the package `hipstershop.<name>` (here `hipstershop.review`). `proto/reviews.proto` declares `package hipstershop`, the package the shop's original services use. The frontend, the only caller, keeps its own copy of the file with the same package, so both sides use the gRPC service name `/hipstershop.ReviewService/...`.

The package name is part of that name on the wire. Renaming it changes the name of every method, so an old client and a new server (or the reverse) would not find each other (`UNIMPLEMENTED`), and the enforced `buf breaking` check of the pipeline (rule TEST-03) reports the rename as a breaking change and fails.

review-service is not deployed in any environment yet, so no running client would break today. A rename would still have to be made in this repository and in the frontend's copy in one coordinated change, with the frontend deployed together with the service.

## Decision

Keep `package hipstershop` for `reviews.proto`. Changes stay additive (new fields or RPCs, no renumbering, no removals), server first; `star_counts` in `RatingSummary` was added this way. If the package is ever renamed, review-service changes first and the frontend's copy in the same release, with a new package version and a deprecation window.

## Consequences

- The package name does not follow the pattern of the newer services (inventory, order, user). This is a recorded exception, not an oversight; the metric label `grpc_service="hipstershop.ReviewService"` and the health service name carry it.
- `buf breaking` stays green and no change in the frontend is needed.
