# API

Package `hipstershop`, service `ReviewService` ([`proto/reviews.proto`](https://github.com/platform-boutique/app-review-service/blob/main/proto/reviews.proto)) (legacy package kept on purpose, see [ADR 4](adr/0004-proto-package-stays-hipstershop.md)).

| Method | Request | Response | Behaviour |
|---|---|---|---|
| `ListReviews` | `{ product_id, int32 page, int32 page_size }` | `{ repeated Review reviews, int32 total, double average_rating }` | Newest first (`created_at DESC`, id as tie-break). `page` is 1-based, `0` means 1; `page_size` `0` means 10, above 50 is capped at 50. `total` and `average_rating` (1 decimal, 0 if none) cover all reviews of the product. A page past the end returns no reviews. Empty `product_id` or negative values: `INVALID_ARGUMENT` |
| `AddReview` | `{ product_id, author_name, int32 rating, comment }` | `Review` | Text fields are trimmed; `product_id` 1-128, `author_name` 1-80, `comment` 1-2000 characters (runes), `rating` 1-5, no NUL characters, otherwise `INVALID_ARGUMENT`. The server sets `id` (UUID) and `created_at`. **Abuse limits** (below): an equal review → `ALREADY_EXISTS`; too many from one author on one product → `RESOURCE_EXHAUSTED` |
| `GetRatingSummary` | `{ repeated string product_ids }` (max 200) | `{ repeated RatingSummary summaries }` | One entry per distinct id, in request order; `average` has 1 decimal; products without reviews (and unknown ids) come back with `count` 0, `average` 0 and `star_counts` all zeros. More than 200 ids or an empty id: `INVALID_ARGUMENT` |

`Review` = `{ id, product_id, author_name, int32 rating, comment, google.protobuf.Timestamp created_at }`.
`RatingSummary` = `{ product_id, double average, int32 count, repeated int32 star_counts }`. `star_counts` always has exactly 5 entries: index 0 = number of 1-star reviews ... index 4 = number of 5-star reviews, so `sum(star_counts) == count`. Average, count and the five buckets come out of one SQL statement (`count(*) FILTER (WHERE rating = k)`), i.e. one snapshot, so a concurrent `AddReview` can never make them disagree. The field is additive (tag 4); older clients ignore it.
Errors: `UNAVAILABLE` while the database is not ready/reachable, `INTERNAL` (without SQL details, which are logged) for other storage errors. A recovery interceptor turns a handler panic into `INTERNAL`. Product ids are not checked against the catalog.

**Product ids** are trimmed and otherwise used exactly as given: they are **case sensitive** (`Blue-Frock-162` and `blue-frock-162` are two different products), at most 128 characters after trimming (more → `INVALID_ARGUMENT`), no NUL. Callers must send the catalog's id spelling.

**AddReview abuse limits** (no authentication exists, so the author name is just text the caller sends):

- *Duplicates:* a review is rejected with `ALREADY_EXISTS` ("this review was already posted for this product") if the table already has one with the same product id, the same author name and the same text, compared case-insensitively (Unicode simple case folding) with surrounding blanks ignored. The check and the insert run in one transaction under a Postgres advisory lock, so concurrent identical calls store one. No schema change or unique index: rows that are already duplicates (e.g. from before this check) are left alone and block nothing.
- *Rate limit:* at most `REVIEW_RATE_LIMIT` (default 5) reviews per `REVIEW_RATE_WINDOW` (default `1m`) per product + author name (case-insensitive); the next one → `RESOURCE_EXHAUSTED` with the seconds to wait. Invalid requests do not use the budget, rejected ones are not counted. It is **in memory and per process**: a restart resets it and N replicas allow N times the limit; an author who changes the name gets a new bucket, so it slows down accidental double posts and naive scripts, not a determined attacker. At most 50,000 live buckets are kept; beyond that new buckets are refused until old ones expire.
- *Not done:* checking the product id against the catalog (an unknown id is accepted; out of scope until the catalog client exists), authentication, moderation.

Examples (run against a started container or the compose stack):

```bash
grpcurl -plaintext -d '{"product_id":"blue-frock-162","page_size":2}' localhost:8084 hipstershop.ReviewService/ListReviews
grpcurl -plaintext -d '{"product_id":"blue-frock-162","author_name":"Smoke Test","rating":5,"comment":"Works in the container."}' \
  localhost:8084 hipstershop.ReviewService/AddReview
grpcurl -plaintext -d '{"product_ids":["blue-frock-162","no-such-product"]}' localhost:8084 hipstershop.ReviewService/GetRatingSummary
grpcurl -plaintext localhost:8084 grpc.health.v1.Health/Check
```
