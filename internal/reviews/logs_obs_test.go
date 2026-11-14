package reviews

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"review-service/internal/obs"
	pb "review-service/proto"

	"github.com/sirupsen/logrus"
	"go.opentelemetry.io/otel/trace"
)

func spanCtx() context.Context {
	tid, _ := trace.TraceIDFromHex("4bf92f3577b34da6a3ce929d0e0e4736")
	sid, _ := trace.SpanIDFromHex("00f067aa0ba902b7")
	c := trace.ContextWithSpanContext(context.Background(), trace.NewSpanContext(trace.SpanContextConfig{TraceID: tid, SpanID: sid, TraceFlags: trace.FlagsSampled}))
	return obs.WithRequestID(c, "req-4")
}

func jsonLines(t *testing.T, buf *bytes.Buffer) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, l := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		if l == "" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(l), &m); err != nil {
			t.Fatalf("not JSON: %q", l)
		}
		out = append(out, m)
	}
	return out
}

func logService(buf *bytes.Buffer, opts ...Option) (*Service, *memStore) {
	l := logrus.New()
	l.SetOutput(buf)
	l.SetFormatter(obs.NewFormatter("review-service", "test"))
	st := &memStore{}
	s := NewService(st, l, opts...)
	s.SetReady(true)
	return s, st
}

// OBS-30: the request path logs in the contract's shape with the request's trace ids; the author
// name and the comment (free text from the shop's visitors) never reach a log line.
func TestRequestPathLogsCarryTraceIDsAndNoFreeText(t *testing.T) {
	var buf bytes.Buffer
	s, st := logService(&buf, WithRateLimit(1, 3600e9))
	c := spanCtx()
	req := &pb.AddReviewRequest{ProductId: "p1", AuthorName: "Jane Marker", Rating: 5, Comment: "comment-MARKER"}
	if _, err := s.AddReview(c, req); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddReview(c, &pb.AddReviewRequest{ProductId: "p1", AuthorName: "Jane Marker", Rating: 4, Comment: "other-MARKER"}); err == nil {
		t.Fatal("expected the rate limit")
	}
	st.err = errors.Join(ErrUnavailable, errors.New("connection refused"))
	if _, err := s.GetRatingSummary(c, &pb.GetRatingSummaryRequest{ProductIds: []string{"p1"}}); err == nil {
		t.Fatal("expected UNAVAILABLE")
	}

	lines := jsonLines(t, &buf)
	if len(lines) != 3 {
		t.Fatalf("expected 3 lines, got:\n%s", buf.String())
	}
	for _, m := range lines {
		if m["trace_id"] != "4bf92f3577b34da6a3ce929d0e0e4736" || m["span_id"] != "00f067aa0ba902b7" || m["request_id"] != "req-4" {
			t.Errorf("line without the request's ids: %v", m)
		}
		if m["service"] != "review-service" || m["severity"] == nil || m["timestamp"] == nil || m["message"] == nil {
			t.Errorf("shape: %v", m)
		}
	}
	if e, _ := lines[2]["error"].(string); !strings.Contains(e, "connection refused") || lines[2]["severity"] != "warning" {
		t.Errorf("outage line: %v", lines[2])
	}
	if strings.Contains(buf.String(), "Marker") || strings.Contains(buf.String(), "MARKER") {
		t.Errorf("free text in the logs:\n%s", buf.String())
	}
}

// Outside a span the ids are absent.
func TestLogsWithoutSpanHaveNoIDs(t *testing.T) {
	var buf bytes.Buffer
	s, _ := logService(&buf, WithRateLimit(0, 0))
	if _, err := s.AddReview(context.Background(), &pb.AddReviewRequest{ProductId: "p1", AuthorName: "A", Rating: 5, Comment: "c"}); err != nil {
		t.Fatal(err)
	}
	for _, m := range jsonLines(t, &buf) {
		for _, k := range []string{"trace_id", "span_id", "request_id"} {
			if _, ok := m[k]; ok {
				t.Errorf("%s present outside a span: %v", k, m)
			}
		}
	}
}
