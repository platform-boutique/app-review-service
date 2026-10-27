package reviews

import (
	"context"
	"errors"
	"math"
	"sync/atomic"

	pb "review-service/proto"

	"github.com/sirupsen/logrus"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Service implements the gRPC ReviewService.
type Service struct {
	pb.UnimplementedReviewServiceServer
	store Store
	log   *logrus.Logger
	ready atomic.Bool
}

// NewService creates the service. It answers UNAVAILABLE until SetReady(true)
// (after migrations and seeding have run).
func NewService(store Store, log *logrus.Logger) *Service {
	return &Service{store: store, log: log}
}

// SetReady switches the handlers on or off.
func (s *Service) SetReady(ok bool) { s.ready.Store(ok) }

func (s *Service) checkReady() error {
	if !s.ready.Load() {
		return status.Error(codes.Unavailable, "review-service is starting: database not ready")
	}
	return nil
}

// toStatus maps internal errors to gRPC status codes without leaking SQL details to the caller.
func (s *Service) toStatus(method string, err error) error {
	switch {
	case errors.Is(err, ErrInvalid):
		return status.Error(codes.InvalidArgument, err.Error())
	case errors.Is(err, context.Canceled):
		return status.Error(codes.Canceled, "request canceled")
	case errors.Is(err, context.DeadlineExceeded):
		return status.Error(codes.DeadlineExceeded, "deadline exceeded")
	case errors.Is(err, ErrUnavailable):
		s.log.Warnf("[%s] database unavailable: %v", method, err)
		return status.Error(codes.Unavailable, "database unavailable")
	default:
		s.log.Errorf("[%s] storage error: %v", method, err)
		return status.Error(codes.Internal, "internal error")
	}
}

func toPB(r Review) *pb.Review {
	return &pb.Review{
		Id:         r.ID,
		ProductId:  r.ProductID,
		AuthorName: r.AuthorName,
		Rating:     r.Rating,
		Comment:    r.Comment,
		CreatedAt:  timestamppb.New(r.CreatedAt),
	}
}

func clampInt32(v int64) int32 {
	if v > math.MaxInt32 {
		return math.MaxInt32
	}
	return int32(v) //#nosec G115 -- bounded above
}

// ListReviews returns one page of a product's reviews, newest first.
func (s *Service) ListReviews(ctx context.Context, req *pb.ListReviewsRequest) (*pb.ListReviewsResponse, error) {
	pid, err := ValidateProductID(req.GetProductId())
	if err != nil {
		return nil, s.toStatus("ListReviews", err)
	}
	limit, offset, err := Page(req.GetPage(), req.GetPageSize())
	if err != nil {
		return nil, s.toStatus("ListReviews", err)
	}
	if err := s.checkReady(); err != nil {
		return nil, err
	}
	page, total, avg, err := s.store.List(ctx, pid, limit, offset)
	if err != nil {
		return nil, s.toStatus("ListReviews", err)
	}
	resp := &pb.ListReviewsResponse{
		Reviews:       make([]*pb.Review, 0, len(page)),
		Total:         clampInt32(total),
		AverageRating: Round1(avg),
	}
	for _, r := range page {
		resp.Reviews = append(resp.Reviews, toPB(r))
	}
	return resp, nil
}

// AddReview validates and stores a review.
func (s *Service) AddReview(ctx context.Context, req *pb.AddReviewRequest) (*pb.Review, error) {
	n, err := ValidateNewReview(req.GetProductId(), req.GetAuthorName(), req.GetRating(), req.GetComment())
	if err != nil {
		return nil, s.toStatus("AddReview", err)
	}
	if err := s.checkReady(); err != nil {
		return nil, err
	}
	r, err := s.store.Add(ctx, n)
	if err != nil {
		return nil, s.toStatus("AddReview", err)
	}
	s.log.WithFields(logrus.Fields{"product_id": r.ProductID, "review_id": r.ID, "rating": r.Rating}).Info("[AddReview] review stored")
	return toPB(r), nil
}

// GetRatingSummary returns average and count per requested product; products without reviews get count 0.
func (s *Service) GetRatingSummary(ctx context.Context, req *pb.GetRatingSummaryRequest) (*pb.GetRatingSummaryResponse, error) {
	ids, err := SummaryIDs(req.GetProductIds())
	if err != nil {
		return nil, s.toStatus("GetRatingSummary", err)
	}
	if err := s.checkReady(); err != nil {
		return nil, err
	}
	found, err := s.store.Summaries(ctx, ids)
	if err != nil {
		return nil, s.toStatus("GetRatingSummary", err)
	}
	return &pb.GetRatingSummaryResponse{Summaries: BuildSummaries(ids, found)}, nil
}

// BuildSummaries returns one summary per id in the given order, with count 0 for ids missing from found.
func BuildSummaries(ids []string, found map[string]Summary) []*pb.RatingSummary {
	out := make([]*pb.RatingSummary, 0, len(ids))
	for _, id := range ids {
		sm := found[id]
		out = append(out, &pb.RatingSummary{ProductId: id, Average: Round1(sm.Average), Count: clampInt32(sm.Count)})
	}
	return out
}
