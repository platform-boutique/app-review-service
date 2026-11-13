# 3. Rating summaries come from one SQL statement

Status: accepted (reconstructed from the code and the earlier README).

## Context

The product grid needs stars for many products at once, and the product page needs the distribution per star.

## Decision

`GetRatingSummary` computes average, count and the five star buckets in one statement (`count(*) FILTER (WHERE rating = k)`), for up to 200 products per call. `star_counts` was added as an additive field (tag 4); older clients ignore it.

## Consequences

- One snapshot: `sum(star_counts) == count` even under concurrent writes (covered by an integration test with concurrent writers and readers).
- One round trip per catalogue page instead of one per product.
- `ListReviews` still reads the totals and the page in two statements; `total` can differ by one from the page under a concurrent write.
