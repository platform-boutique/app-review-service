-- +goose Up
CREATE TABLE reviews (
    id          uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    product_id  text        NOT NULL CHECK (char_length(product_id) BETWEEN 1 AND 128),
    author_name text        NOT NULL CHECK (char_length(author_name) BETWEEN 1 AND 80),
    rating      smallint    NOT NULL CHECK (rating BETWEEN 1 AND 5),
    comment     text        NOT NULL CHECK (char_length(comment) BETWEEN 1 AND 2000),
    created_at  timestamptz NOT NULL DEFAULT now()
);

-- ListReviews: one product, newest first (id breaks ties so paging is stable)
CREATE INDEX reviews_product_created_idx ON reviews (product_id, created_at DESC, id DESC);

-- +goose Down
DROP TABLE reviews;
