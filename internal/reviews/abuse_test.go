package reviews

import (
	"context"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	pb "review-service/proto"

	"github.com/sirupsen/logrus"
	"google.golang.org/grpc/codes"
)

func add(s *Service, product, author, comment string) error {
	_, err := s.AddReview(context.Background(), &pb.AddReviewRequest{ProductId: product, AuthorName: author, Rating: 4, Comment: comment})
	return err
}

// ---------------------------------------------------------------------------
// Duplicates
// ---------------------------------------------------------------------------

func TestAddReview_DuplicateIsAlreadyExists(t *testing.T) {
	s, st := newTestService(t)
	if err := add(s, "p", "Ada", "Great shirt"); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ product, author, comment string }{
		{"p", "Ada", "Great shirt"},
		{"p", "  ada  ", "great SHIRT"},
		{"p", "ADA", "  Great shirt\n"},
	} {
		err := add(s, c.product, c.author, c.comment)
		if code(err) != codes.AlreadyExists {
			t.Errorf("%+v: code = %v, want AlreadyExists (%v)", c, code(err), err)
		}
	}
	if len(st.rows) != 1 {
		t.Errorf("%d rows stored, want 1", len(st.rows))
	}
}

func TestAddReview_NotADuplicate(t *testing.T) {
	s, st := newTestService(t)
	for i, c := range []struct{ product, author, comment string }{
		{"p", "Ada", "Great shirt"},
		{"q", "Ada", "Great shirt"},         // other product
		{"p", "Bob", "Great shirt"},         // other author
		{"p", "Ada", "Great shirt, really"}, // other text
		{"P", "Ada", "Great shirt"},         // product ids are case sensitive
	} {
		if err := add(s, c.product, c.author, c.comment); err != nil {
			t.Errorf("case %d %+v: %v", i, c, err)
		}
	}
	if len(st.rows) != 5 {
		t.Errorf("%d rows stored, want 5", len(st.rows))
	}
}

func TestAddReview_DuplicateUnicodeCase(t *testing.T) {
	s, _ := newTestService(t)
	if err := add(s, "p", "Zoë Ärger", "Schön und günstig"); err != nil {
		t.Fatal(err)
	}
	if err := add(s, "p", "ZOË ÄRGER", "SCHÖN UND GÜNSTIG"); code(err) != codes.AlreadyExists {
		t.Errorf("unicode case difference: %v", err)
	}
}

func TestAddReview_DuplicateMessageIsReadable(t *testing.T) {
	s, _ := newTestService(t)
	_ = add(s, "p", "Ada", "x")
	err := add(s, "p", "Ada", "x")
	if err == nil || !strings.Contains(err.Error(), "already posted") {
		t.Errorf("message: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Rate limit
// ---------------------------------------------------------------------------

func rateService(t *testing.T, limit int, window time.Duration, now func() time.Time) *Service {
	t.Helper()
	l := logrus.New()
	l.Out = io.Discard
	s := NewService(&memStore{}, l)
	s.limiter = NewRateLimiter(limit, window, now)
	s.SetReady(true)
	return s
}

func TestAddReview_RateLimitPerProductAndAuthor(t *testing.T) {
	clock := time.Unix(1_700_000_000, 0)
	s := rateService(t, 5, time.Minute, func() time.Time { return clock })
	for i := 0; i < 5; i++ {
		if err := add(s, "p", "Ada", string(rune('a'+i))); err != nil {
			t.Fatalf("review %d: %v", i, err)
		}
	}
	err := add(s, "p", "Ada", "sixth")
	if code(err) != codes.ResourceExhausted {
		t.Fatalf("sixth review: code = %v, want ResourceExhausted (%v)", code(err), err)
	}
	if !strings.Contains(err.Error(), "try again in") {
		t.Errorf("message should say when to retry: %v", err)
	}
	// author name case and blanks do not dodge the limit
	if code(add(s, "p", "  ADA ", "seventh")) != codes.ResourceExhausted {
		t.Error("a different spelling of the author must share the bucket")
	}
	// other author / other product are separate buckets
	if err := add(s, "p", "Bob", "x"); err != nil {
		t.Errorf("other author: %v", err)
	}
	if err := add(s, "q", "Ada", "x"); err != nil {
		t.Errorf("other product: %v", err)
	}
	// still limited 59 s later, free again after the window
	clock = clock.Add(59 * time.Second)
	if code(add(s, "p", "Ada", "later")) != codes.ResourceExhausted {
		t.Error("still inside the window")
	}
	clock = clock.Add(2 * time.Second)
	if err := add(s, "p", "Ada", "after the window"); err != nil {
		t.Errorf("after the window: %v", err)
	}
}

func TestAddReview_InvalidRequestsDoNotUseTheBudget(t *testing.T) {
	s := rateService(t, 1, time.Minute, nil)
	for i := 0; i < 3; i++ {
		if code(add(s, "p", "Ada", "")) != codes.InvalidArgument {
			t.Fatal("empty comment must be invalid")
		}
	}
	if err := add(s, "p", "Ada", "valid"); err != nil {
		t.Errorf("valid review after invalid ones: %v", err)
	}
}

func TestAddReview_RateLimitDisabled(t *testing.T) {
	s := rateService(t, 0, time.Minute, nil)
	for i := 0; i < 50; i++ {
		if err := add(s, "p", "Ada", strings.Repeat("x", i+1)); err != nil {
			t.Fatalf("review %d: %v", i, err)
		}
	}
}

func TestNewService_DefaultsToFivePerMinute(t *testing.T) {
	l := logrus.New()
	l.Out = io.Discard
	s := NewService(&memStore{}, l)
	s.SetReady(true)
	for i := 0; i < DefaultRateLimit; i++ {
		if err := add(s, "p", "Ada", strings.Repeat("y", i+1)); err != nil {
			t.Fatal(err)
		}
	}
	if code(add(s, "p", "Ada", "one too many")) != codes.ResourceExhausted {
		t.Error("default limit is not applied")
	}
}

func TestRateLimiter_ConcurrentUse(t *testing.T) {
	l := NewRateLimiter(10, time.Minute, nil)
	var wg sync.WaitGroup
	var mu sync.Mutex
	allowed := 0
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if ok, _ := l.Allow("k"); ok {
				mu.Lock()
				allowed++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if allowed != 10 {
		t.Errorf("allowed %d, want exactly 10", allowed)
	}
}

func TestRateLimiter_KeyTableIsBounded(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	l := NewRateLimiter(1, time.Minute, func() time.Time { return now })
	for i := 0; i < maxLimiterKeys; i++ {
		l.events[string(rune(i))+"k"] = []time.Time{now}
	}
	if ok, _ := l.Allow("new"); ok {
		t.Error("a new key must be refused while the table is full of live keys")
	}
	now = now.Add(2 * time.Minute)
	if ok, _ := l.Allow("new"); !ok {
		t.Error("expired keys must be swept")
	}
	if len(l.events) != 1 {
		t.Errorf("%d keys left, want 1", len(l.events))
	}
}

func TestRateLimitFromEnv(t *testing.T) {
	env := func(m map[string]string) func(string) string { return func(k string) string { return m[k] } }
	cases := []struct {
		name    string
		env     map[string]string
		limit   int
		window  time.Duration
		wantErr bool
	}{
		{"defaults", nil, 5, time.Minute, false},
		{"custom", map[string]string{"REVIEW_RATE_LIMIT": "20", "REVIEW_RATE_WINDOW": "30s"}, 20, 30 * time.Second, false},
		{"off", map[string]string{"REVIEW_RATE_LIMIT": "0"}, 0, time.Minute, false},
		{"blank is default", map[string]string{"REVIEW_RATE_LIMIT": "  "}, 5, time.Minute, false},
		{"typo keeps protection on", map[string]string{"REVIEW_RATE_LIMIT": "five"}, 5, time.Minute, true},
		{"negative", map[string]string{"REVIEW_RATE_LIMIT": "-1"}, 5, time.Minute, true},
		{"bad window", map[string]string{"REVIEW_RATE_WINDOW": "soon"}, 5, time.Minute, true},
		{"zero window", map[string]string{"REVIEW_RATE_WINDOW": "0s"}, 5, time.Minute, true},
	}
	for _, c := range cases {
		limit, window, err := RateLimitFromEnv(env(c.env))
		if limit != c.limit || window != c.window || (err != nil) != c.wantErr {
			t.Errorf("%s: got %d %s %v; want %d %s err=%v", c.name, limit, window, err, c.limit, c.window, c.wantErr)
		}
	}
}

// ---------------------------------------------------------------------------
// Product ids: trimmed, otherwise as given (case sensitive), capped
// ---------------------------------------------------------------------------

func TestProductIDNormalisation(t *testing.T) {
	got, err := ValidateProductID("  Blue-Frock-162 \n")
	if err != nil || got != "Blue-Frock-162" {
		t.Errorf("trim: %q, %v", got, err)
	}
	a, _ := ValidateProductID("Blue-Frock-162")
	b, _ := ValidateProductID("blue-frock-162")
	if a == b {
		t.Error("ids are case sensitive: the two spellings are different products")
	}
	if got, err := ValidateProductID(strings.Repeat("é", MaxProductIDLen)); err != nil || len([]rune(got)) != MaxProductIDLen {
		t.Errorf("an id of exactly %d characters is valid: %v", MaxProductIDLen, err)
	}
	if _, err := ValidateProductID(strings.Repeat("a", MaxProductIDLen+1)); err == nil {
		t.Error("an id above the cap must be rejected")
	}
	// the cap is applied after trimming, not before
	if _, err := ValidateProductID("  " + strings.Repeat("a", MaxProductIDLen) + "  "); err != nil {
		t.Errorf("padding must not count towards the cap: %v", err)
	}
}

func TestAddReview_ProductIDTrimmedSoPaddedIDsShareADuplicateCheck(t *testing.T) {
	s, st := newTestService(t)
	if err := add(s, "p", "Ada", "x"); err != nil {
		t.Fatal(err)
	}
	if code(add(s, "  p ", "Ada", "x")) != codes.AlreadyExists {
		t.Error("'  p ' and 'p' are the same product")
	}
	if len(st.rows) != 1 || st.rows[0].ProductID != "p" {
		t.Errorf("stored rows: %+v", st.rows)
	}
}
