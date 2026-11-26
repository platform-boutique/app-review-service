# review-service

A gRPC service that stores product reviews and star ratings for the Everly shop.

- `ListReviews`: one page of a product's reviews, newest first, with total and average.
- `AddReview`: validated, de-duplicated and rate limited per product and author.
- `GetRatingSummary`: average, count and per-star distribution for up to 200 products at once, for the product grid.
- It owns its own Postgres database, creates the schema with embedded migrations at start-up and seeds it once from the store dataset (351 reviews).

| | |
|---|---|
| Runtime | Go 1.26, static binary, image `platform-demo/review-service:local` (built locally only) |
| Ports | gRPC `8084`, HTTP `9090` (`/metrics`, scraped by Alloy) |
| State | its own Postgres (`reviews`) |
| Called by | frontend |
| Status | shared CI pipeline with Publish; Helm chart in platform-demo-helm; not deployed in any environment yet |

Pages: [architecture](architecture.md), [API](api.md), [configuration](configuration.md), [observability](observability.md), [operations](operations.md), [development](development.md), [decisions](adr/0001-one-postgres-per-service.md), [proto package exception](adr/0004-proto-package-stays-hipstershop.md).
