//go:build integration

// Integration tests against a real Postgres. Run with:
//
//	TEST_DATABASE_URL=postgres://review:review@localhost:55432/postgres?sslmode=disable \
//	  go test -tags integration ./tests/integration/
//
// Every test creates (and drops) its own database, so they need a user that may CREATE DATABASE.
package integration

import (
	"context"
	"fmt"
	"io"
	"net/url"
	"os"
	"sync"
	"testing"
	"time"

	"review-service/data"
	"review-service/internal/reviews"
	pb "review-service/proto"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/sirupsen/logrus"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// freshDB creates an empty database and returns its DSN.
func freshDB(t *testing.T) string {
	t.Helper()
	admin := os.Getenv("TEST_DATABASE_URL")
	if admin == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, admin)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer func() { _ = conn.Close(ctx) }()
	name := fmt.Sprintf("reviews_test_%d", time.Now().UnixNano())
	if _, err := conn.Exec(ctx, "CREATE DATABASE "+name); err != nil {
		t.Fatalf("create database: %v", err)
	}
	t.Cleanup(func() {
		c, err := pgx.Connect(context.Background(), admin)
		if err != nil {
			return
		}
		defer func() { _ = c.Close(context.Background()) }()
		_, _ = c.Exec(context.Background(), "DROP DATABASE IF EXISTS "+name+" WITH (FORCE)")
	})
	u, err := url.Parse(admin)
	if err != nil {
		t.Fatal(err)
	}
	u.Path = "/" + name
	return u.String()
}

func newPool(t *testing.T, dsn string) *pgxpool.Pool {
	t.Helper()
	p, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(p.Close)
	return p
}

// seed returns the embedded production seed (data/reviews.json).
func seed(t *testing.T) []reviews.SeedReview {
	t.Helper()
	items, err := reviews.ParseSeed(data.Reviews)
	if err != nil {
		t.Fatal(err)
	}
	return items
}

// fixtureSeed returns the small fixture (testdata/reviews.fixture.json, made by tools/make_reviews.py
// from testdata/products.json) whose ratings the assertions below rely on.
func fixtureSeed(t *testing.T) []reviews.SeedReview {
	t.Helper()
	raw, err := os.ReadFile("../../testdata/reviews.fixture.json")
	if err != nil {
		t.Fatal(err)
	}
	items, err := reviews.ParseSeed(raw)
	if err != nil {
		t.Fatal(err)
	}
	return items
}

func count(t *testing.T, p *pgxpool.Pool) int64 {
	t.Helper()
	var n int64
	if err := p.QueryRow(context.Background(), "SELECT count(*) FROM reviews").Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestMigrationsTwice(t *testing.T) {
	p := newPool(t, freshDB(t))
	ctx := context.Background()
	items := seed(t)

	applied, seeded, err := reviews.Setup(ctx, p, items)
	if err != nil {
		t.Fatal(err)
	}
	if applied != 1 || seeded != int64(len(items)) {
		t.Errorf("first run: applied %d, seeded %d (want 1, %d)", applied, seeded, len(items))
	}
	applied, seeded, err = reviews.Setup(ctx, p, items)
	if err != nil {
		t.Fatal(err)
	}
	if applied != 0 || seeded != 0 {
		t.Errorf("second run: applied %d, seeded %d (want 0, 0)", applied, seeded)
	}
	if n := count(t, p); n != int64(len(items)) {
		t.Errorf("rows %d, want %d", n, len(items))
	}
	var idx int
	if err := p.QueryRow(ctx, `SELECT count(*) FROM pg_indexes WHERE tablename = 'reviews' AND indexname = 'reviews_product_created_idx'`).Scan(&idx); err != nil || idx != 1 {
		t.Errorf("index missing: %d, %v", idx, err)
	}
}

func TestConcurrentStarts(t *testing.T) {
	dsn := freshDB(t)
	items := seed(t)
	const replicas = 6
	var wg sync.WaitGroup
	errs := make(chan error, replicas)
	results := make(chan [2]int64, replicas)
	for i := 0; i < replicas; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			p, err := pgxpool.New(context.Background(), dsn) // one pool per "replica"
			if err != nil {
				errs <- err
				return
			}
			defer p.Close()
			applied, seeded, err := reviews.Setup(context.Background(), p, items)
			if err != nil {
				errs <- err
				return
			}
			results <- [2]int64{int64(applied), seeded}
		}()
	}
	wg.Wait()
	close(errs)
	close(results)
	for err := range errs {
		t.Errorf("replica failed: %v", err)
	}
	var applied, seeded int64
	for r := range results {
		applied += r[0]
		seeded += r[1]
	}
	if applied != 1 || seeded != int64(len(items)) {
		t.Errorf("over %d replicas: applied %d, seeded %d (want 1, %d)", replicas, applied, seeded, len(items))
	}
	if n := count(t, newPool(t, dsn)); n != int64(len(items)) {
		t.Errorf("rows %d, want %d", n, len(items))
	}
}

func TestAddListSummary(t *testing.T) {
	p := newPool(t, freshDB(t))
	ctx := context.Background()
	if _, _, err := reviews.Setup(ctx, p, fixtureSeed(t)); err != nil {
		t.Fatal(err)
	}
	l := logrus.New()
	l.Out = io.Discard
	svc := reviews.NewService(reviews.NewPGStore(p), l)
	svc.SetReady(true)

	before, err := svc.ListReviews(ctx, &pb.ListReviewsRequest{ProductId: "fixture-tee"})
	if err != nil {
		t.Fatal(err)
	}
	if before.Total != 3 || before.AverageRating != 3.7 { // 5, 4, 2
		t.Fatalf("seeded fixture-tee: total %d avg %v", before.Total, before.AverageRating)
	}

	added, err := svc.AddReview(ctx, &pb.AddReviewRequest{ProductId: "fixture-tee", AuthorName: "  Edsger  ", Rating: 1, Comment: "Considered harmful. ☕"})
	if err != nil {
		t.Fatal(err)
	}
	if added.Id == "" || added.AuthorName != "Edsger" || time.Since(added.CreatedAt.AsTime()) > time.Minute {
		t.Errorf("unexpected stored review %v", added)
	}

	after, err := svc.ListReviews(ctx, &pb.ListReviewsRequest{ProductId: "fixture-tee", Page: 1, PageSize: 2})
	if err != nil {
		t.Fatal(err)
	}
	if after.Total != 4 || len(after.Reviews) != 2 || after.Reviews[0].Id != added.Id {
		t.Fatalf("after add: total %d, len %d, first %v", after.Total, len(after.Reviews), after.Reviews)
	}
	if after.AverageRating != 3.0 { // (5+4+2+1)/4
		t.Errorf("average %v, want 3.0", after.AverageRating)
	}
	page2, err := svc.ListReviews(ctx, &pb.ListReviewsRequest{ProductId: "fixture-tee", Page: 2, PageSize: 2})
	if err != nil || len(page2.Reviews) != 2 {
		t.Fatalf("page 2: %v, %v", page2, err)
	}
	seen := map[string]bool{}
	for _, r := range append(after.Reviews, page2.Reviews...) {
		if seen[r.Id] {
			t.Errorf("review %s on two pages", r.Id)
		}
		seen[r.Id] = true
	}
	if page2.Reviews[1].AuthorName != "Ada Lovelace" { // the oldest seed entry comes last
		t.Errorf("oldest review last: got %s", page2.Reviews[1].AuthorName)
	}

	sum, err := svc.GetRatingSummary(ctx, &pb.GetRatingSummaryRequest{ProductIds: []string{"fixture-tee", "fixture-mug", "fixture-empty"}})
	if err != nil {
		t.Fatal(err)
	}
	want := []*pb.RatingSummary{
		{ProductId: "fixture-tee", Average: 3.0, Count: 4},
		{ProductId: "fixture-mug", Average: 3.0, Count: 1},
		{ProductId: "fixture-empty", Average: 0, Count: 0},
	}
	for i, w := range want {
		g := sum.Summaries[i]
		if g.ProductId != w.ProductId || g.Average != w.Average || g.Count != w.Count {
			t.Errorf("summary %d = %v, want %v", i, g, w)
		}
	}

	// the database constraints back up the validation
	if _, err := p.Exec(ctx, `INSERT INTO reviews (product_id, author_name, rating, comment) VALUES ('p', 'a', 9, 'c')`); err == nil {
		t.Error("rating 9 accepted by the database")
	}
	// invalid input never reaches the database
	if _, err := svc.AddReview(ctx, &pb.AddReviewRequest{ProductId: "p", AuthorName: "a", Rating: 9, Comment: "c"}); status.Code(err) != codes.InvalidArgument {
		t.Errorf("rating 9: %v", err)
	}
}

func TestUnreachableDatabase(t *testing.T) {
	p := newPool(t, "postgres://review:review@127.0.0.1:1/none?sslmode=disable&connect_timeout=2")
	l := logrus.New()
	l.Out = io.Discard
	svc := reviews.NewService(reviews.NewPGStore(p), l)
	svc.SetReady(true)
	_, err := svc.ListReviews(context.Background(), &pb.ListReviewsRequest{ProductId: "p"})
	if status.Code(err) != codes.Unavailable {
		t.Errorf("unreachable DB: %v, want Unavailable", err)
	}
	if _, _, err := reviews.Setup(context.Background(), p, nil); err == nil {
		t.Error("Setup succeeded without a database")
	}
}
