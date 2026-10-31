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
	"slices"
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
		{ProductId: "fixture-tee", Average: 3.0, Count: 4, StarCounts: []int32{1, 1, 0, 1, 1}}, // 5, 4, 2, 1
		{ProductId: "fixture-mug", Average: 3.0, Count: 1, StarCounts: []int32{0, 0, 1, 0, 0}},
		{ProductId: "fixture-empty", Average: 0, Count: 0, StarCounts: []int32{0, 0, 0, 0, 0}},
	}
	for i, w := range want {
		g := sum.Summaries[i]
		if g.ProductId != w.ProductId || g.Average != w.Average || g.Count != w.Count || !slices.Equal(g.StarCounts, w.StarCounts) {
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

// Duplicate reviews are rejected by the real store, also when identical calls race.
func TestAddReviewDuplicates(t *testing.T) {
	p := newPool(t, freshDB(t))
	ctx := context.Background()
	if _, _, err := reviews.Setup(ctx, p, nil); err != nil {
		t.Fatal(err)
	}
	l := logrus.New()
	l.Out = io.Discard
	svc := reviews.NewService(reviews.NewPGStore(p), l, reviews.WithRateLimit(0, 0))
	svc.SetReady(true)
	add := func(product, author, comment string) error {
		_, err := svc.AddReview(ctx, &pb.AddReviewRequest{ProductId: product, AuthorName: author, Rating: 5, Comment: comment})
		return err
	}

	if err := add("p", "Zoë Ärger", "Schön und günstig"); err != nil {
		t.Fatal(err)
	}
	for _, c := range [][3]string{
		{"p", "Zoë Ärger", "Schön und günstig"},
		{"p", "  ZOË ÄRGER ", "SCHÖN UND GÜNSTIG"},
		{" p ", "zoë ärger", "schön und günstig\n"},
	} {
		if err := add(c[0], c[1], c[2]); status.Code(err) != codes.AlreadyExists {
			t.Errorf("%q: %v, want AlreadyExists", c, err)
		}
	}
	for _, c := range [][3]string{{"q", "Zoë Ärger", "Schön und günstig"}, {"p", "Bob", "Schön und günstig"}, {"p", "Zoë Ärger", "Schön"}, {"P", "Zoë Ärger", "Schön und günstig"}} {
		if err := add(c[0], c[1], c[2]); err != nil {
			t.Errorf("%q should be accepted: %v", c, err)
		}
	}

	// 20 identical concurrent calls: exactly one is stored.
	var wg sync.WaitGroup
	var mu sync.Mutex
	ok, dup := 0, 0
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			err := add("race", "Racer", "same text")
			mu.Lock()
			defer mu.Unlock()
			switch status.Code(err) {
			case codes.OK:
				ok++
			case codes.AlreadyExists:
				dup++
			default:
				t.Errorf("unexpected: %v", err)
			}
		}()
	}
	wg.Wait()
	if ok != 1 || dup != 19 {
		t.Errorf("concurrent identical adds: %d stored, %d duplicates; want 1 and 19", ok, dup)
	}
	var n int
	if err := p.QueryRow(ctx, `SELECT count(*) FROM reviews WHERE product_id = 'race'`).Scan(&n); err != nil || n != 1 {
		t.Errorf("rows for 'race': %d, %v", n, err)
	}
}

func newService(t *testing.T, p *pgxpool.Pool) *reviews.Service {
	t.Helper()
	l := logrus.New()
	l.Out = io.Discard
	svc := reviews.NewService(reviews.NewPGStore(p), l, reviews.WithRateLimit(0, 0))
	svc.SetReady(true)
	return svc
}

func checkStars(t *testing.T, sm *pb.RatingSummary) {
	t.Helper()
	if len(sm.StarCounts) != 5 {
		t.Errorf("%s: %d star_counts, want 5", sm.ProductId, len(sm.StarCounts))
		return
	}
	var sum int32
	for _, c := range sm.StarCounts {
		sum += c
	}
	if sum != sm.Count {
		t.Errorf("%s: sum(star_counts) %d != count %d (%v)", sm.ProductId, sum, sm.Count, sm.StarCounts)
	}
}

// Zero reviews and an unknown product both give count 0 and five zeros.
func TestSummaryZeroAndUnknownProduct(t *testing.T) {
	p := newPool(t, freshDB(t))
	ctx := context.Background()
	if _, _, err := reviews.Setup(ctx, p, fixtureSeed(t)); err != nil {
		t.Fatal(err)
	}
	svc := newService(t, p)
	resp, err := svc.GetRatingSummary(ctx, &pb.GetRatingSummaryRequest{ProductIds: []string{"fixture-empty", "no-such-product"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Summaries) != 2 {
		t.Fatalf("got %d summaries", len(resp.Summaries))
	}
	for _, sm := range resp.Summaries {
		if sm.Count != 0 || sm.Average != 0 || !slices.Equal(sm.StarCounts, make([]int32, 5)) {
			t.Errorf("%s = %v, want count 0, average 0, five zeros", sm.ProductId, sm)
		}
	}
}

// For every product of the production seed the star counts equal a direct SQL count, in chunks of 200 ids.
func TestSeededStarCountsMatchSQL(t *testing.T) {
	p := newPool(t, freshDB(t))
	ctx := context.Background()
	items := seed(t)
	if _, _, err := reviews.Setup(ctx, p, items); err != nil {
		t.Fatal(err)
	}
	svc := newService(t, p)

	rows, err := p.Query(ctx, `SELECT product_id, count(*), sum((rating = 1)::int), sum((rating = 2)::int), sum((rating = 3)::int),
		sum((rating = 4)::int), sum((rating = 5)::int) FROM reviews GROUP BY product_id ORDER BY product_id`)
	if err != nil {
		t.Fatal(err)
	}
	type want struct {
		count int64
		stars [5]int64
	}
	direct := map[string]want{}
	var ids []string
	for rows.Next() {
		var id string
		var w want
		if err := rows.Scan(&id, &w.count, &w.stars[0], &w.stars[1], &w.stars[2], &w.stars[3], &w.stars[4]); err != nil {
			t.Fatal(err)
		}
		direct[id] = w
		ids = append(ids, id)
	}
	rows.Close()
	if len(ids) < 10 {
		t.Fatalf("only %d seeded products", len(ids))
	}
	checked := 0
	for start := 0; start < len(ids); start += reviews.MaxSummaryIDs {
		chunk := ids[start:min(start+reviews.MaxSummaryIDs, len(ids))]
		resp, err := svc.GetRatingSummary(ctx, &pb.GetRatingSummaryRequest{ProductIds: chunk})
		if err != nil {
			t.Fatal(err)
		}
		for _, sm := range resp.Summaries {
			w := direct[sm.ProductId]
			checkStars(t, sm)
			if int64(sm.Count) != w.count {
				t.Errorf("%s: count %d, SQL %d", sm.ProductId, sm.Count, w.count)
			}
			for i := range 5 {
				if int64(sm.StarCounts[i]) != w.stars[i] {
					t.Errorf("%s: star_counts %v, SQL %v", sm.ProductId, sm.StarCounts, w.stars)
					break
				}
			}
			checked++
		}
	}
	if checked != len(ids) {
		t.Errorf("checked %d of %d products", checked, len(ids))
	}
}

// While reviews are added concurrently, every GetRatingSummary answer is internally consistent:
// five entries, sum(star_counts) == count, count never goes backwards, and at the end the numbers
// equal what was added.
func TestSummaryConsistentUnderConcurrentAdd(t *testing.T) {
	p := newPool(t, freshDB(t))
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute) // generous: the host clock jumps
	defer cancel()
	if _, _, err := reviews.Setup(ctx, p, fixtureSeed(t)); err != nil {
		t.Fatal(err)
	}
	svc := newService(t, p)
	const product = "fixture-empty"
	const writers, perWriter = 8, 25

	var wg sync.WaitGroup
	stop := make(chan struct{})
	var wwg sync.WaitGroup
	for w := 0; w < writers; w++ {
		wwg.Add(1)
		go func() {
			defer wwg.Done()
			for i := 0; i < perWriter; i++ {
				_, err := svc.AddReview(ctx, &pb.AddReviewRequest{ProductId: product, AuthorName: fmt.Sprintf("writer-%d", w),
					Rating: int32(i%5) + 1, Comment: fmt.Sprintf("review %d of writer %d", i, w)})
				if err != nil {
					t.Errorf("AddReview: %v", err)
					return
				}
			}
		}()
	}
	const readers = 4
	reads := make([]int, readers)
	for r := 0; r < readers; r++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			var last int32
			for {
				resp, err := svc.GetRatingSummary(ctx, &pb.GetRatingSummaryRequest{ProductIds: []string{product}})
				if err != nil {
					t.Errorf("GetRatingSummary: %v", err)
					return
				}
				sm := resp.Summaries[0]
				checkStars(t, sm)
				if sm.Count < last {
					t.Errorf("count went backwards: %d after %d", sm.Count, last)
				}
				last = sm.Count
				reads[r]++
				select {
				case <-stop:
					return
				default:
				}
			}
		}()
	}
	wwg.Wait()
	close(stop)
	wg.Wait()
	total := 0
	for _, n := range reads {
		total += n
	}
	if total < readers {
		t.Errorf("only %d reads", total)
	}
	t.Logf("%d consistent summary reads while %d reviews were added", total, writers*perWriter)

	resp, err := svc.GetRatingSummary(ctx, &pb.GetRatingSummaryRequest{ProductIds: []string{product}})
	if err != nil {
		t.Fatal(err)
	}
	sm := resp.Summaries[0]
	checkStars(t, sm)
	if sm.Count != writers*perWriter || !slices.Equal(sm.StarCounts, []int32{40, 40, 40, 40, 40}) || sm.Average != 3.0 {
		t.Errorf("final = %v, want count %d, 40 per star, average 3.0", sm, writers*perWriter)
	}
}
