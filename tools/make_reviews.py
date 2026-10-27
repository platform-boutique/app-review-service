#!/usr/bin/env python3
"""Turn the catalog seed (products.json) into review-service's seed (data/reviews.json).

Usage: python3 tools/make_reviews.py PATH/TO/products.json [-o data/reviews.json]

Input:  a JSON array of products (or {"products": [...]}), each with "id" and
        "reviews": [{"reviewer_name", "rating", "comment", "date"}]
        ("reviewerName" is accepted too, as in the DummyJSON original).
Output: a JSON array of {"product_id", "author_name", "rating", "comment", "created_at"},
        sorted by product id and date, so re-running it gives a byte-identical file.
Entries that break the AddReview limits are reported and skipped; the script fails if any are.
Standard library only.
"""

import argparse
import json
import sys
from datetime import datetime, timezone

MAX_AUTHOR = 80
MAX_COMMENT = 2000
MAX_PRODUCT_ID = 128


def parse_date(value):
    """ISO 8601 (e.g. 2024-05-23T08:56:21.618Z) -> RFC 3339 UTC with millisecond precision."""
    if not isinstance(value, str) or not value:
        raise ValueError("missing date")
    v = value.strip()
    if v.endswith("Z"):
        v = v[:-1] + "+00:00"
    dt = datetime.fromisoformat(v)
    if dt.tzinfo is None:
        dt = dt.replace(tzinfo=timezone.utc)
    dt = dt.astimezone(timezone.utc)
    return dt.strftime("%Y-%m-%dT%H:%M:%S.") + f"{dt.microsecond // 1000:03d}Z"


def convert(products):
    out, problems = [], []
    for p in products:
        pid = str(p.get("id", "")).strip()
        if not pid or len(pid) > MAX_PRODUCT_ID:
            problems.append(f"product without a usable id: {p.get('id')!r}")
            continue
        for i, r in enumerate(p.get("reviews") or []):
            where = f"{pid} review {i}"
            author = str(r.get("reviewer_name") or r.get("reviewerName") or "").strip()
            comment = str(r.get("comment") or "").strip()
            rating = r.get("rating")
            if not 1 <= len(author) <= MAX_AUTHOR:
                problems.append(f"{where}: author_name length {len(author)}")
                continue
            if not 1 <= len(comment) <= MAX_COMMENT:
                problems.append(f"{where}: comment length {len(comment)}")
                continue
            if not isinstance(rating, int) or isinstance(rating, bool) or not 1 <= rating <= 5:
                problems.append(f"{where}: rating {rating!r}")
                continue
            try:
                created = parse_date(r.get("date"))
            except ValueError as e:
                problems.append(f"{where}: date {r.get('date')!r} ({e})")
                continue
            out.append({
                "product_id": pid,
                "author_name": author,
                "rating": rating,
                "comment": comment,
                "created_at": created,
            })
    out.sort(key=lambda x: (x["product_id"], x["created_at"], x["author_name"]))
    return out, problems


def main():
    ap = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    ap.add_argument("products", help="path to products.json")
    ap.add_argument("-o", "--out", default="data/reviews.json", help="output file (default: data/reviews.json)")
    args = ap.parse_args()

    with open(args.products, encoding="utf-8") as f:
        doc = json.load(f)
    products = doc.get("products") if isinstance(doc, dict) else doc
    if not isinstance(products, list):
        sys.exit("input must be a JSON array of products or an object with a 'products' array")

    reviews, problems = convert(products)
    for msg in problems:
        print("skipped:", msg, file=sys.stderr)
    with open(args.out, "w", encoding="utf-8") as f:
        json.dump(reviews, f, ensure_ascii=False, indent=1)
        f.write("\n")
    n_products = len({r["product_id"] for r in reviews})
    print(f"wrote {len(reviews)} reviews for {n_products} products to {args.out}", file=sys.stderr)
    if problems:
        sys.exit(1)


if __name__ == "__main__":
    main()
