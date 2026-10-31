package reviews

import (
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"review-service/data"
	pb "review-service/proto"

	"github.com/sirupsen/logrus"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// memStore is an in-memory Store with the same semantics as PGStore.
type memStore struct {
	mu   sync.Mutex
	rows []Review
	next int
	err  error // returned by every call when set
}

func (m *memStore) List(_ context.Context, pid string, limit, offset int64) ([]Review, int64, float64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.err != nil {
		return nil, 0, 0, m.err
	}
	var all []Review
	sum := 0
	for _, r := range m.rows {
		if r.ProductID == pid {
			all = append(all, r)
			sum += int(r.Rating)
		}
	}
	sort.Slice(all, func(i, j int) bool {
		if !all[i].CreatedAt.Equal(all[j].CreatedAt) {
			return all[i].CreatedAt.After(all[j].CreatedAt)
		}
		return all[i].ID > all[j].ID
	})
	total := int64(len(all))
	avg := 0.0
	if total > 0 {
		avg = Round1(float64(sum) / float64(total))
	}
	if offset >= total {
		return []Review{}, total, avg, nil
	}
	end := min(offset+limit, total)
	return all[offset:end], total, avg, nil
}

func (m *memStore) Add(_ context.Context, n NewReview) (Review, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.err != nil {
		return Review{}, m.err
	}
	for _, r := range m.rows {
		if r.ProductID == n.ProductID && strings.EqualFold(r.AuthorName, n.AuthorName) && strings.EqualFold(r.Comment, n.Comment) {
			return Review{}, ErrDuplicate
		}
	}
	m.next++
	r := Review{ID: string(rune('a' + m.next)), ProductID: n.ProductID, AuthorName: n.AuthorName, Rating: n.Rating,
		Comment: n.Comment, CreatedAt: time.Unix(1_700_000_000+int64(m.next), 0)}
	m.rows = append(m.rows, r)
	return r, nil
}

func (m *memStore) Summaries(_ context.Context, ids []string) (map[string]Summary, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.err != nil {
		return nil, m.err
	}
	sums := map[string][2]int64{}
	stars := map[string][5]int64{}
	for _, r := range m.rows {
		s := sums[r.ProductID]
		sums[r.ProductID] = [2]int64{s[0] + int64(r.Rating), s[1] + 1}
		st := stars[r.ProductID]
		st[r.Rating-1]++
		stars[r.ProductID] = st
	}
	out := map[string]Summary{}
	for _, id := range ids {
		if s, ok := sums[id]; ok {
			out[id] = Summary{ProductID: id, Average: Round1(float64(s[0]) / float64(s[1])), Count: s[1], Stars: stars[id]}
		}
	}
	return out, nil
}

func (m *memStore) Ping(context.Context) error { return m.err }

func newTestService(t *testing.T) (*Service, *memStore) {
	t.Helper()
	l := logrus.New()
	l.Out = io.Discard
	st := &memStore{}
	s := NewService(st, l, WithRateLimit(0, 0)) // rate limit off: tests add many reviews; see ratelimit_test.go
	s.SetReady(true)
	return s, st
}

func code(err error) codes.Code { return status.Code(err) }

func TestAddReviewValidation(t *testing.T) {
	s, _ := newTestService(t)
	ctx := context.Background()
	for _, req := range []*pb.AddReviewRequest{
		{},
		{ProductId: "p", AuthorName: "A", Rating: 0, Comment: "c"},
		{ProductId: "p", AuthorName: "A", Rating: 6, Comment: "c"},
		{ProductId: "", AuthorName: "A", Rating: 5, Comment: "c"},
		{ProductId: "p", AuthorName: "", Rating: 5, Comment: "c"},
		{ProductId: "p", AuthorName: "A", Rating: 5, Comment: ""},
	} {
		if _, err := s.AddReview(ctx, req); code(err) != codes.InvalidArgument {
			t.Errorf("AddReview(%v) code = %v, want InvalidArgument", req, code(err))
		}
	}
	r, err := s.AddReview(ctx, &pb.AddReviewRequest{ProductId: "p", AuthorName: " Ada ", Rating: 4, Comment: "nice"})
	if err != nil {
		t.Fatal(err)
	}
	if r.Id == "" || r.CreatedAt == nil || r.AuthorName != "Ada" || r.Rating != 4 {
		t.Errorf("unexpected review %v", r)
	}
}

func TestNilRequestsDoNotPanic(t *testing.T) {
	s, _ := newTestService(t)
	ctx := context.Background()
	if _, err := s.AddReview(ctx, nil); code(err) != codes.InvalidArgument {
		t.Errorf("AddReview(nil) = %v", err)
	}
	if _, err := s.ListReviews(ctx, nil); code(err) != codes.InvalidArgument {
		t.Errorf("ListReviews(nil) = %v", err)
	}
	if resp, err := s.GetRatingSummary(ctx, nil); err != nil || len(resp.Summaries) != 0 {
		t.Errorf("GetRatingSummary(nil) = %v, %v", resp, err)
	}
}

func TestListReviewsPagination(t *testing.T) {
	s, _ := newTestService(t)
	ctx := context.Background()
	for i := 1; i <= 23; i++ {
		if _, err := s.AddReview(ctx, &pb.AddReviewRequest{ProductId: "p", AuthorName: "A", Rating: int32(i%5 + 1), Comment: fmt.Sprintf("comment %d", i)}); err != nil {
			t.Fatal(err)
		}
	}
	_, _ = s.AddReview(ctx, &pb.AddReviewRequest{ProductId: "other", AuthorName: "B", Rating: 1, Comment: "c"})

	first, err := s.ListReviews(ctx, &pb.ListReviewsRequest{ProductId: "p"})
	if err != nil {
		t.Fatal(err)
	}
	if first.Total != 23 || len(first.Reviews) != 10 {
		t.Fatalf("default page: total %d, len %d", first.Total, len(first.Reviews))
	}
	for i := 1; i < len(first.Reviews); i++ {
		if first.Reviews[i].CreatedAt.AsTime().After(first.Reviews[i-1].CreatedAt.AsTime()) {
			t.Fatal("reviews are not newest first")
		}
	}
	last, _ := s.ListReviews(ctx, &pb.ListReviewsRequest{ProductId: "p", Page: 3, PageSize: 10})
	if len(last.Reviews) != 3 {
		t.Errorf("page 3: %d reviews, want 3", len(last.Reviews))
	}
	beyond, err := s.ListReviews(ctx, &pb.ListReviewsRequest{ProductId: "p", Page: 9})
	if err != nil || len(beyond.Reviews) != 0 || beyond.Total != 23 {
		t.Errorf("page beyond the end: %v, %v", beyond, err)
	}
	capped, _ := s.ListReviews(ctx, &pb.ListReviewsRequest{ProductId: "p", PageSize: 500})
	if len(capped.Reviews) != 23 {
		t.Errorf("page_size 500 (capped at 50): %d reviews", len(capped.Reviews))
	}
	// ratings 2,3,4,5,1 repeating: 23 values, sum = 4*15 + (2+3+4) = 69 -> 3.0
	if first.AverageRating != 3.0 {
		t.Errorf("average %v, want 3.0", first.AverageRating)
	}
	none, err := s.ListReviews(ctx, &pb.ListReviewsRequest{ProductId: "nothing"})
	if err != nil || none.Total != 0 || none.AverageRating != 0 || len(none.Reviews) != 0 {
		t.Errorf("unknown product: %v, %v", none, err)
	}
	if _, err := s.ListReviews(ctx, &pb.ListReviewsRequest{ProductId: "p", Page: -1}); code(err) != codes.InvalidArgument {
		t.Errorf("negative page: %v", err)
	}
}

func TestGetRatingSummary(t *testing.T) {
	s, _ := newTestService(t)
	ctx := context.Background()
	for i, r := range []int32{5, 4, 4} {
		_, _ = s.AddReview(ctx, &pb.AddReviewRequest{ProductId: "a", AuthorName: "A", Rating: r, Comment: fmt.Sprintf("c%d", i)})
	}
	_, _ = s.AddReview(ctx, &pb.AddReviewRequest{ProductId: "b", AuthorName: "B", Rating: 1, Comment: "c"})

	resp, err := s.GetRatingSummary(ctx, &pb.GetRatingSummaryRequest{ProductIds: []string{"none", "a", "b", "a"}})
	if err != nil {
		t.Fatal(err)
	}
	want := []struct {
		id    string
		avg   float64
		count int32
		stars []int32
	}{
		{"none", 0, 0, []int32{0, 0, 0, 0, 0}},
		{"a", 4.3, 3, []int32{0, 0, 0, 2, 1}},
		{"b", 1, 1, []int32{1, 0, 0, 0, 0}},
	}
	if len(resp.Summaries) != len(want) {
		t.Fatalf("got %d summaries, want %d", len(resp.Summaries), len(want))
	}
	for i, w := range want {
		g := resp.Summaries[i]
		if g.ProductId != w.id || g.Average != w.avg || g.Count != w.count || !slices.Equal(g.StarCounts, w.stars) {
			t.Errorf("summary %d = %v, want %+v", i, g, w)
		}
	}
	too := make([]string, MaxSummaryIDs+1)
	for i := range too {
		too[i] = "x"
	}
	if _, err := s.GetRatingSummary(ctx, &pb.GetRatingSummaryRequest{ProductIds: too}); code(err) != codes.InvalidArgument {
		t.Errorf("201 ids: %v", err)
	}
}

func TestErrorMapping(t *testing.T) {
	s, st := newTestService(t)
	ctx := context.Background()
	list := &pb.ListReviewsRequest{ProductId: "p"}

	st.err = errors.Join(ErrUnavailable, errors.New("dial tcp: connection refused"))
	if _, err := s.ListReviews(ctx, list); code(err) != codes.Unavailable {
		t.Errorf("unreachable DB: %v", err)
	}
	st.err = errors.New("syntax error at or near SELECT")
	_, err := s.ListReviews(ctx, list)
	if code(err) != codes.Internal || status.Convert(err).Message() != "internal error" {
		t.Errorf("SQL error must be INTERNAL without details: %v", err)
	}
	st.err = nil
	s.SetReady(false)
	if _, err := s.ListReviews(ctx, list); code(err) != codes.Unavailable {
		t.Errorf("not ready: %v", err)
	}
	// validation still wins over readiness
	if _, err := s.ListReviews(ctx, &pb.ListReviewsRequest{}); code(err) != codes.InvalidArgument {
		t.Errorf("not ready + invalid: %v", err)
	}
}

func TestEmbeddedSeedIsValid(t *testing.T) {
	items, err := ParseSeed(data.Reviews)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) == 0 {
		t.Fatal("embedded seed is empty")
	}
}

func TestParseSeedRejectsBadEntries(t *testing.T) {
	for _, raw := range []string{
		`{}`,
		`[{"product_id":"p","author_name":"A","rating":9,"comment":"c","created_at":"2025-01-01T00:00:00Z"}]`,
		`[{"product_id":"p","author_name":"A","rating":5,"comment":"c"}]`,
	} {
		if _, err := ParseSeed([]byte(raw)); err == nil {
			t.Errorf("ParseSeed(%s) accepted bad input", raw)
		}
	}
}

// Every summary has exactly five star counts that sum to count, for products with and without reviews.
func TestSummaryStarCountsInvariant(t *testing.T) {
	s, _ := newTestService(t)
	ctx := context.Background()
	ratings := []int32{1, 1, 2, 3, 3, 3, 5, 5, 5, 5, 4}
	for i, r := range ratings {
		if _, err := s.AddReview(ctx, &pb.AddReviewRequest{ProductId: "p", AuthorName: "A", Rating: r, Comment: fmt.Sprintf("c%d", i)}); err != nil {
			t.Fatal(err)
		}
	}
	resp, err := s.GetRatingSummary(ctx, &pb.GetRatingSummaryRequest{ProductIds: []string{"p", "unknown"}})
	if err != nil {
		t.Fatal(err)
	}
	if got := resp.Summaries[0].StarCounts; !slices.Equal(got, []int32{2, 1, 3, 1, 4}) {
		t.Errorf("star_counts = %v, want [2 1 3 1 4]", got)
	}
	for _, sm := range resp.Summaries {
		if len(sm.StarCounts) != 5 {
			t.Fatalf("%s: %d star_counts, want 5", sm.ProductId, len(sm.StarCounts))
		}
		var sum int32
		for _, c := range sm.StarCounts {
			sum += c
		}
		if sum != sm.Count {
			t.Errorf("%s: sum(star_counts) %d != count %d", sm.ProductId, sum, sm.Count)
		}
	}
	if resp.Summaries[1].Count != 0 || !slices.Equal(resp.Summaries[1].StarCounts, make([]int32, 5)) {
		t.Errorf("unknown product = %v, want count 0 and five zeros", resp.Summaries[1])
	}
}
