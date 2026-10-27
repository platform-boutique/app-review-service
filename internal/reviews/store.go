package reviews

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

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
}

// Store is what the gRPC handlers need from the storage layer.
type Store interface {
	// List returns one page of a product's reviews (newest first) plus the totals over all of them.
	List(ctx context.Context, productID string, limit, offset int64) (page []Review, total int64, average float64, err error)
	// Add stores a review and returns it with id and created_at set.
	Add(ctx context.Context, r NewReview) (Review, error)
	// Summaries returns the summaries of the given products that have at least one review.
	Summaries(ctx context.Context, productIDs []string) (map[string]Summary, error)
	// Ping checks that the database answers.
	Ping(ctx context.Context) error
}

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

// Add implements Store.
func (s *PGStore) Add(ctx context.Context, n NewReview) (Review, error) {
	r := Review{ProductID: n.ProductID, AuthorName: n.AuthorName, Rating: n.Rating, Comment: n.Comment}
	err := s.pool.QueryRow(ctx,
		`INSERT INTO reviews (product_id, author_name, rating, comment)
		 VALUES ($1, $2, $3, $4)
		 RETURNING id::text, created_at`,
		n.ProductID, n.AuthorName, n.Rating, n.Comment).Scan(&r.ID, &r.CreatedAt)
	if err != nil {
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
		`SELECT product_id, round(avg(rating), 1)::float8, count(*)
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
		if err := rows.Scan(&sm.ProductID, &sm.Average, &sm.Count); err != nil {
			return nil, classify(err)
		}
		out[sm.ProductID] = sm
	}
	return out, classify(rows.Err())
}

// Ping implements Store.
func (s *PGStore) Ping(ctx context.Context) error { return classify(s.pool.Ping(ctx)) }
