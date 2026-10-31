package reviews

import (
	"context"
	"errors"
	"fmt"
	"hash/fnv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Review is a stored review.
type Review struct {
	ID         string
	ProductID  string
	AuthorName string
	Rating     int32
	Comment    string
	CreatedAt  time.Time
}

// Summary is the rating summary of one product.
type Summary struct {
	ProductID string
	Average   float64 // already rounded to one decimal
	Count     int64
	// Stars[i] is the number of reviews with rating i+1; it always sums to Count.
	Stars [5]int64
}

// Store is what the gRPC handlers need from the storage layer.
type Store interface {
	// List returns one page of a product's reviews (newest first) plus the totals over all of them.
	List(ctx context.Context, productID string, limit, offset int64) (page []Review, total int64, average float64, err error)
	// Add stores a review and returns it with id and created_at set, or ErrDuplicate
	// if an equal review (same product, author and text, compared case-insensitively) exists.
	Add(ctx context.Context, r NewReview) (Review, error)
	// Summaries returns the summaries (average, count and per-star counts, all from one statement and
	// therefore one snapshot) of the given products that have at least one review.
	Summaries(ctx context.Context, productIDs []string) (map[string]Summary, error)
	// Ping checks that the database answers.
	Ping(ctx context.Context) error
}

// ErrDuplicate is returned by Store.Add when a review with the same product,
// author name and text (case-insensitive) already exists; the gRPC layer maps
// it to ALREADY_EXISTS.
var ErrDuplicate = errors.New("duplicate review")

// ErrUnavailable marks storage errors caused by an unreachable database; the gRPC layer maps it to UNAVAILABLE.
var ErrUnavailable = errors.New("database unavailable")

// PGStore is the Postgres implementation of Store.
type PGStore struct {
	pool *pgxpool.Pool
}

// NewPGStore wraps an existing pool (the caller owns and closes it).
func NewPGStore(pool *pgxpool.Pool) *PGStore { return &PGStore{pool: pool} }

func classify(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		// the server answered: a real SQL error, except for "server shutting down / too many connections" classes
		if strings.HasPrefix(pgErr.Code, "08") || strings.HasPrefix(pgErr.Code, "57") || pgErr.Code == "53300" {
			return fmt.Errorf("%w: %v", ErrUnavailable, err)
		}
		return err
	}
	// anything else (dial errors, closed connections, timeouts) means the database is not reachable
	return fmt.Errorf("%w: %v", ErrUnavailable, err)
}

// List implements Store.
func (s *PGStore) List(ctx context.Context, productID string, limit, offset int64) ([]Review, int64, float64, error) {
	var total int64
	var avg float64
	err := s.pool.QueryRow(ctx,
		`SELECT count(*), COALESCE(round(avg(rating), 1), 0)::float8 FROM reviews WHERE product_id = $1`,
		productID).Scan(&total, &avg)
	if err != nil {
		return nil, 0, 0, classify(err)
	}
	if total == 0 || offset >= total {
		return []Review{}, total, avg, nil
	}
	rows, err := s.pool.Query(ctx,
		`SELECT id::text, product_id, author_name, rating, comment, created_at
		   FROM reviews
		  WHERE product_id = $1
		  ORDER BY created_at DESC, id DESC
		  LIMIT $2 OFFSET $3`,
		productID, limit, offset)
	if err != nil {
		return nil, 0, 0, classify(err)
	}
	page, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (Review, error) {
		var r Review
		err := row.Scan(&r.ID, &r.ProductID, &r.AuthorName, &r.Rating, &r.Comment, &r.CreatedAt)
		return r, err
	})
	if err != nil {
		return nil, 0, 0, classify(err)
	}
	return page, total, avg, nil
}

// dupLockClass is the first key of the two-int advisory lock Add takes; the
// migration/seed locks use the single-bigint form, which is a separate key space.
const dupLockClass = 0x52564457 // "RVDW"

// dupLockKey derives the second advisory-lock key from product and author, so
// two concurrent identical AddReview calls serialise (different reviews rarely
// share a key, and a collision only costs a short wait).
func dupLockKey(productID, author string) int32 {
	h := fnv.New32a()
	_, _ = h.Write([]byte(productID))
	_, _ = h.Write([]byte{0x1f})
	_, _ = h.Write([]byte(strings.ToLower(author)))
	return int32(h.Sum32()) //#nosec G115 -- wraps on purpose, any int32 is a valid lock key
}

// Add implements Store. The duplicate check and the insert run in one
// transaction under an advisory lock, so concurrent identical calls cannot
// both succeed. The comparison is done with strings.EqualFold on the product's
// reviews of the same author-name/comment length (Postgres lower() depends on
// the database locale and would miss non-ASCII case differences), so no
// schema change or unique index is needed and existing duplicate rows do not
// block anything.
func (s *PGStore) Add(ctx context.Context, n NewReview) (Review, error) {
	r := Review{ProductID: n.ProductID, AuthorName: n.AuthorName, Rating: n.Rating, Comment: n.Comment}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Review{}, classify(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1, $2)`, int32(dupLockClass), dupLockKey(n.ProductID, n.AuthorName)); err != nil {
		return Review{}, classify(err)
	}
	rows, err := tx.Query(ctx,
		`SELECT author_name, comment FROM reviews
		  WHERE product_id = $1 AND char_length(author_name) = $2 AND char_length(comment) = $3`,
		n.ProductID, utf8.RuneCountInString(n.AuthorName), utf8.RuneCountInString(n.Comment))
	if err != nil {
		return Review{}, classify(err)
	}
	dup := false
	for rows.Next() {
		var author, comment string
		if err := rows.Scan(&author, &comment); err != nil {
			rows.Close()
			return Review{}, classify(err)
		}
		if strings.EqualFold(author, n.AuthorName) && strings.EqualFold(comment, n.Comment) {
			dup = true
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return Review{}, classify(err)
	}
	if dup {
		return Review{}, ErrDuplicate
	}

	err = tx.QueryRow(ctx,
		`INSERT INTO reviews (product_id, author_name, rating, comment)
		 VALUES ($1, $2, $3, $4)
		 RETURNING id::text, created_at`,
		n.ProductID, n.AuthorName, n.Rating, n.Comment).Scan(&r.ID, &r.CreatedAt)
	if err != nil {
		return Review{}, classify(err)
	}
	if err := tx.Commit(ctx); err != nil {
		return Review{}, classify(err)
	}
	return r, nil
}

// Summaries implements Store.
func (s *PGStore) Summaries(ctx context.Context, productIDs []string) (map[string]Summary, error) {
	out := make(map[string]Summary, len(productIDs))
	if len(productIDs) == 0 {
		return out, nil
	}
	rows, err := s.pool.Query(ctx,
		`SELECT product_id, round(avg(rating), 1)::float8, count(*),
		        count(*) FILTER (WHERE rating = 1), count(*) FILTER (WHERE rating = 2),
		        count(*) FILTER (WHERE rating = 3), count(*) FILTER (WHERE rating = 4),
		        count(*) FILTER (WHERE rating = 5)
		   FROM reviews
		  WHERE product_id = ANY($1)
		  GROUP BY product_id`,
		productIDs)
	if err != nil {
		return nil, classify(err)
	}
	defer rows.Close()
	for rows.Next() {
		var sm Summary
		if err := rows.Scan(&sm.ProductID, &sm.Average, &sm.Count,
			&sm.Stars[0], &sm.Stars[1], &sm.Stars[2], &sm.Stars[3], &sm.Stars[4]); err != nil {
			return nil, classify(err)
		}
		out[sm.ProductID] = sm
	}
	return out, classify(rows.Err())
}

// Ping implements Store.
func (s *PGStore) Ping(ctx context.Context) error { return classify(s.pool.Ping(ctx)) }
