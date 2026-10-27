package reviews

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
	"github.com/pressly/goose/v3/lock"
)

//go:embed migrations/*.sql
var migrationFS embed.FS

// seedLockKey is the pg_advisory_xact_lock key that serialises seeding between replicas
// (goose uses its own key for the migration lock).
const seedLockKey int64 = 0x7265766965777321 // "reviews!"

// Migrate applies all embedded migrations. goose holds a Postgres session advisory lock while it runs,
// so replicas that start at the same time apply each migration exactly once.
func Migrate(ctx context.Context, pool *pgxpool.Pool) ([]*goose.MigrationResult, error) {
	sub, err := fs.Sub(migrationFS, "migrations")
	if err != nil {
		return nil, err
	}
	locker, err := lock.NewPostgresSessionLocker(
		lock.WithLockTimeout(1, 120), // retry every second for up to two minutes
	)
	if err != nil {
		return nil, err
	}
	db := stdlib.OpenDBFromPool(pool)
	defer db.Close()
	provider, err := goose.NewProvider(goose.DialectPostgres, db, sub, goose.WithSessionLocker(locker))
	if err != nil {
		return nil, err
	}
	res, err := provider.Up(ctx)
	if err != nil {
		return res, classify(err)
	}
	return res, nil
}

// SeedReview is one entry of data/reviews.json (written by tools/make_reviews.py).
type SeedReview struct {
	ProductID  string    `json:"product_id"`
	AuthorName string    `json:"author_name"`
	Rating     int32     `json:"rating"`
	Comment    string    `json:"comment"`
	CreatedAt  time.Time `json:"created_at"`
}

// ParseSeed decodes and validates seed data; any invalid entry fails the whole file.
func ParseSeed(raw []byte) ([]SeedReview, error) {
	var items []SeedReview
	if err := json.Unmarshal(raw, &items); err != nil {
		return nil, fmt.Errorf("seed: %w", err)
	}
	for i := range items {
		n, err := ValidateNewReview(items[i].ProductID, items[i].AuthorName, items[i].Rating, items[i].Comment)
		if err != nil {
			return nil, fmt.Errorf("seed entry %d: %w", i, err)
		}
		if items[i].CreatedAt.IsZero() {
			return nil, fmt.Errorf("seed entry %d: created_at is missing", i)
		}
		items[i] = SeedReview{ProductID: n.ProductID, AuthorName: n.AuthorName, Rating: n.Rating, Comment: n.Comment, CreatedAt: items[i].CreatedAt}
	}
	return items, nil
}

// Seed inserts the seed reviews if the reviews table is empty and returns how many rows it inserted.
// It runs in one transaction under an advisory lock, so concurrent starts seed exactly once.
func Seed(ctx context.Context, pool *pgxpool.Pool, items []SeedReview) (int64, error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return 0, classify(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, seedLockKey); err != nil {
		return 0, classify(err)
	}
	var exists bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM reviews)`).Scan(&exists); err != nil {
		return 0, classify(err)
	}
	if exists || len(items) == 0 {
		return 0, nil
	}
	n, err := tx.CopyFrom(ctx,
		pgx.Identifier{"reviews"},
		[]string{"product_id", "author_name", "rating", "comment", "created_at"},
		pgx.CopyFromSlice(len(items), func(i int) ([]any, error) {
			it := items[i]
			return []any{it.ProductID, it.AuthorName, it.Rating, it.Comment, it.CreatedAt}, nil
		}))
	if err != nil {
		return 0, classify(err)
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, classify(err)
	}
	return n, nil
}

// Setup is the start-up path: migrations first, then the one-time seed.
func Setup(ctx context.Context, pool *pgxpool.Pool, seed []SeedReview) (applied int, seeded int64, err error) {
	res, err := Migrate(ctx, pool)
	if err != nil {
		return 0, 0, fmt.Errorf("migrate: %w", err)
	}
	seeded, err = Seed(ctx, pool, seed)
	if err != nil {
		return len(res), 0, fmt.Errorf("seed: %w", err)
	}
	return len(res), seeded, nil
}
