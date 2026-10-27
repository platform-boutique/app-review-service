package reviews

import (
	"errors"
	"strings"
	"testing"
)

func TestValidateNewReview(t *testing.T) {
	ok := func(pid, author string, rating int32, comment string) NewReview {
		t.Helper()
		n, err := ValidateNewReview(pid, author, rating, comment)
		if err != nil {
			t.Fatalf("ValidateNewReview(%q, %q, %d, %q) = %v, want ok", pid, author, rating, comment, err)
		}
		return n
	}
	bad := func(name, pid, author string, rating int32, comment string) {
		t.Helper()
		_, err := ValidateNewReview(pid, author, rating, comment)
		if !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: got %v, want ErrInvalid", name, err)
		}
	}

	n := ok("  p1 ", "  Ada  ", 5, "  great  ")
	if n.ProductID != "p1" || n.AuthorName != "Ada" || n.Comment != "great" || n.Rating != 5 {
		t.Errorf("not trimmed: %+v", n)
	}
	ok("p1", strings.Repeat("a", MaxAuthorNameLen), 1, strings.Repeat("c", MaxCommentLen))
	// limits are characters, not bytes: 80 two-byte runes are fine
	ok("p1", strings.Repeat("ü", MaxAuthorNameLen), 3, strings.Repeat("☕", MaxCommentLen))

	bad("empty product", "", "Ada", 5, "x")
	bad("blank product", "   ", "Ada", 5, "x")
	bad("long product", strings.Repeat("p", MaxProductIDLen+1), "Ada", 5, "x")
	bad("empty author", "p1", "", 5, "x")
	bad("blank author", "p1", " \t ", 5, "x")
	bad("long author", "p1", strings.Repeat("a", MaxAuthorNameLen+1), 5, "x")
	bad("rating 0", "p1", "Ada", 0, "x")
	bad("rating 6", "p1", "Ada", 6, "x")
	bad("rating negative", "p1", "Ada", -1, "x")
	bad("empty comment", "p1", "Ada", 5, "")
	bad("long comment", "p1", "Ada", 5, strings.Repeat("c", MaxCommentLen+1))
	bad("NUL in comment", "p1", "Ada", 5, "a\x00b")
	bad("invalid utf8", "p1", "Ada\xff", 5, "x")
}

func TestPage(t *testing.T) {
	cases := []struct {
		page, size    int32
		limit, offset int64
	}{
		{0, 0, 10, 0},
		{1, 0, 10, 0},
		{2, 0, 10, 10},
		{3, 5, 5, 10},
		{1, 50, 50, 0},
		{2, 51, 50, 50},
		{1, 1 << 30, 50, 0},
		{1_000_000, 50, 50, 49_999_950},
	}
	for _, c := range cases {
		limit, offset, err := Page(c.page, c.size)
		if err != nil || limit != c.limit || offset != c.offset {
			t.Errorf("Page(%d, %d) = %d, %d, %v; want %d, %d", c.page, c.size, limit, offset, err, c.limit, c.offset)
		}
	}
	for _, c := range [][2]int32{{-1, 10}, {1, -1}, {1_000_001, 10}, {-2147483648, -2147483648}} {
		if _, _, err := Page(c[0], c[1]); !errors.Is(err, ErrInvalid) {
			t.Errorf("Page(%d, %d) err = %v, want ErrInvalid", c[0], c[1], err)
		}
	}
}

func TestSummaryIDs(t *testing.T) {
	got, err := SummaryIDs([]string{"b", " a ", "b", "c", "a"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(got, ",") != "b,a,c" {
		t.Errorf("got %v, want deduplicated b,a,c in request order", got)
	}
	if got, err := SummaryIDs(nil); err != nil || len(got) != 0 {
		t.Errorf("nil ids: %v, %v", got, err)
	}
	ids := make([]string, MaxSummaryIDs)
	for i := range ids {
		ids[i] = "p"
	}
	if _, err := SummaryIDs(ids); err != nil {
		t.Errorf("%d ids should be allowed: %v", MaxSummaryIDs, err)
	}
	if _, err := SummaryIDs(append(ids, "p")); !errors.Is(err, ErrInvalid) {
		t.Errorf("%d ids: err = %v, want ErrInvalid", MaxSummaryIDs+1, err)
	}
	if _, err := SummaryIDs([]string{"a", ""}); !errors.Is(err, ErrInvalid) {
		t.Errorf("empty id: err = %v, want ErrInvalid", err)
	}
}

func TestRound1(t *testing.T) {
	cases := map[float64]float64{0: 0, 4.25: 4.3, 4.24: 4.2, 3.3333333: 3.3, 5: 5, 1.05: 1.1}
	for in, want := range cases {
		if got := Round1(in); got != want {
			t.Errorf("Round1(%v) = %v, want %v", in, got, want)
		}
	}
}
