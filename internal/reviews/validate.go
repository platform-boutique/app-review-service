package reviews

import (
	"errors"
	"fmt"
	"math"
	"strings"
	"unicode/utf8"
)

// Limits of the API contract (see proto/reviews.proto).
const (
	DefaultPageSize  = 10
	MaxPageSize      = 50
	MaxSummaryIDs    = 200
	MaxProductIDLen  = 128
	MaxAuthorNameLen = 80
	MaxCommentLen    = 2000
	MinRating        = 1
	MaxRating        = 5
	maxPage          = 1_000_000 // keeps the OFFSET far below any overflow
)

// ErrInvalid marks a request that breaks the contract; the gRPC layer maps it to INVALID_ARGUMENT.
var ErrInvalid = errors.New("invalid argument")

func invalid(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalid, fmt.Sprintf(format, args...))
}

// NewReview is a validated, normalised review that has not been stored yet.
type NewReview struct {
	ProductID  string
	AuthorName string
	Rating     int32
	Comment    string
}

// ValidateNewReview trims the text fields and checks every limit of AddReview.
// Lengths are counted in characters (runes), not bytes.
func ValidateNewReview(productID, author string, rating int32, comment string) (NewReview, error) {
	pid, err := ValidateProductID(productID)
	if err != nil {
		return NewReview{}, err
	}
	author = strings.TrimSpace(author)
	if n := utf8.RuneCountInString(author); n < 1 || n > MaxAuthorNameLen {
		return NewReview{}, invalid("author_name must be 1-%d characters, got %d", MaxAuthorNameLen, n)
	}
	if rating < MinRating || rating > MaxRating {
		return NewReview{}, invalid("rating must be %d-%d, got %d", MinRating, MaxRating, rating)
	}
	comment = strings.TrimSpace(comment)
	if n := utf8.RuneCountInString(comment); n < 1 || n > MaxCommentLen {
		return NewReview{}, invalid("comment must be 1-%d characters, got %d", MaxCommentLen, n)
	}
	if !utf8.ValidString(author) || !utf8.ValidString(comment) {
		return NewReview{}, invalid("text fields must be valid UTF-8")
	}
	if strings.ContainsRune(author, 0) || strings.ContainsRune(comment, 0) {
		// Postgres text cannot store NUL bytes
		return NewReview{}, invalid("text fields must not contain NUL characters")
	}
	return NewReview{ProductID: pid, AuthorName: author, Rating: rating, Comment: comment}, nil
}

// ValidateProductID trims the id and checks it is non-empty and at most MaxProductIDLen
// characters (counted after trimming). Apart from the trim the id is used exactly as given:
// ids are case sensitive, "Blue-Frock-162" and "blue-frock-162" are different products.
func ValidateProductID(id string) (string, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return "", invalid("product_id is required")
	}
	if utf8.RuneCountInString(id) > MaxProductIDLen || !utf8.ValidString(id) || strings.ContainsRune(id, 0) {
		return "", invalid("product_id must be valid text of at most %d characters", MaxProductIDLen)
	}
	return id, nil
}

// Page turns the 1-based page and page size of ListReviews into LIMIT/OFFSET.
// page 0 means 1, page_size 0 means DefaultPageSize, sizes above MaxPageSize are capped;
// negative values (and absurdly large pages) are invalid.
func Page(page, pageSize int32) (limit, offset int64, err error) {
	if page < 0 {
		return 0, 0, invalid("page must be >= 1, got %d", page)
	}
	if page > maxPage {
		return 0, 0, invalid("page must be <= %d, got %d", maxPage, page)
	}
	if pageSize < 0 {
		return 0, 0, invalid("page_size must be >= 0, got %d", pageSize)
	}
	if page == 0 {
		page = 1
	}
	switch {
	case pageSize == 0:
		pageSize = DefaultPageSize
	case pageSize > MaxPageSize:
		pageSize = MaxPageSize
	}
	return int64(pageSize), int64(page-1) * int64(pageSize), nil
}

// SummaryIDs validates the ids of GetRatingSummary and removes duplicates, keeping the request order.
func SummaryIDs(ids []string) ([]string, error) {
	if len(ids) > MaxSummaryIDs {
		return nil, invalid("at most %d product_ids per request, got %d", MaxSummaryIDs, len(ids))
	}
	seen := make(map[string]struct{}, len(ids))
	out := make([]string, 0, len(ids))
	for _, raw := range ids {
		id, err := ValidateProductID(raw)
		if err != nil {
			return nil, err
		}
		if _, dup := seen[id]; dup {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	return out, nil
}

// Round1 rounds to one decimal (half away from zero), the precision of every average in the API.
func Round1(v float64) float64 {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return 0
	}
	return math.Round(v*10) / 10
}
