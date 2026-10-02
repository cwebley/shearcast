package jev

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/cwebley/shearcast/internal/usage"
)

func TestUsageSeparatesReportedCostAndCalculatedCost(t *testing.T) {
	for _, tc := range []struct {
		name, wire                          string
		missingUsage, missingCost, reported int
		cost                                float64
	}{
		{"reported", `{"input_tokens":1000000,"output_tokens":55,"cost":0.05}`, 0, 0, 1, .05},
		{"zero", `{"input_tokens":1000000,"output_tokens":55,"cost":0}`, 0, 0, 1, 0},
		{"missing cost", `{"input_tokens":1000000,"output_tokens":55}`, 0, 1, 0, 0},
		{"missing usage", `null`, 1, 1, 0, 0},
		{"missing output", `{"input_tokens":1000000,"cost":0.05}`, 1, 0, 1, .05},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) { fmt.Fprintf(w, `{"answers":{},"usage":%s}`, tc.wire) })
			if _, err := c.Ask(context.Background(), "s", map[string]Question{"q": Noul("x")}); err != nil {
				t.Fatal(err)
			}
			s := c.Stats()
			if s.Attempts != 1 || s.Unresolved != 0 || s.MissingUsage != tc.missingUsage || s.MissingCost != tc.missingCost || s.ReportedCostCalls != tc.reported || s.ProviderCost != tc.cost {
				t.Fatalf("unexpected coverage: %+v", s)
			}
			if tc.missingUsage == 0 && (s.InputTokens != 1000000 || s.OutputTokens != 55 || s.Cost != .042 || len(s.Calculations) != 1 || s.Calculations[0].Rate.VerifiedAt != "2026-09-23") {
				t.Fatalf("unexpected calculation: %+v", s)
			}
			if tc.name == "missing output" && s.InputTokens != 1000000 {
				t.Fatal("lost valid input count when output was absent")
			}
		})
	}
}

func TestUsageSurvivesInvalidAnswersAndHTTPError(t *testing.T) {
	for _, status := range []int{200, 400} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(status)
				fmt.Fprint(w, `{"answers":"invalid","usage":{"input_tokens":200,"output_tokens":3,"cost":0.001}}`)
			})
			if _, err := c.Ask(context.Background(), "s", map[string]Question{"q": Noul("x")}); err == nil {
				t.Fatal("expected invalid response")
			}
			s := c.Stats()
			if s.InputTokens != 200 || s.OutputTokens != 3 || s.ProviderCost != .001 || s.Calls != 0 || s.MissingUsage != 0 {
				t.Fatalf("lost error usage: %+v", s)
			}
		})
	}
}

type usageTransport func(*http.Request) (*http.Response, error)

func (f usageTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestRetryRetainsLostResponseUncertainty(t *testing.T) {
	var calls atomic.Int32
	c := New(Config{Model: usage.JevModel, MaxRetries: 1, HTTPClient: &http.Client{Transport: usageTransport(func(r *http.Request) (*http.Response, error) {
		if calls.Add(1) == 1 {
			return nil, errors.New("response lost")
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"answers":{},"usage":{"input_tokens":100,"output_tokens":10,"cost":0.0000042}}`)), Header: http.Header{}}, nil
	})}})
	if _, err := c.Ask(context.Background(), "s", map[string]Question{"q": Noul("x")}); err != nil {
		t.Fatal(err)
	}
	s := c.Stats()
	if s.Attempts != 2 || s.Calls != 1 || s.MissingUsage != 1 || s.MissingCost != 1 || s.InputTokens != 100 || s.ReportedCostCalls != 1 || s.Unresolved != 0 {
		t.Fatalf("retry erased uncertainty: %+v", s)
	}
}

func TestParallelFailureRetainsSuccessfulBatchAndCheckpoints(t *testing.T) {
	// A transport deliberately completes the dispatched success after its sibling
	// fails. AskAll must join both before callers take the final snapshot.
	started, release := make(chan struct{}), make(chan struct{})
	c := New(Config{Model: usage.JevModel, HTTPClient: &http.Client{Transport: usageTransport(func(r *http.Request) (*http.Response, error) {
		body, _ := io.ReadAll(r.Body)
		status, response := 200, `{"answers":{},"usage":{"input_tokens":400,"output_tokens":4,"cost":0}}`
		if strings.Contains(string(body), `"state":"success"`) {
			close(started)
			<-release
		} else {
			<-started
			close(release)
			status, response = 400, `{"error":"bad question"}`
		}
		return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(response)), Header: http.Header{}}, nil
	})}})
	var snapshots []Stats
	c.RecordUsage(func(s Stats) error { snapshots = append(snapshots, s); return nil })
	_, err := c.AskAll(context.Background(), []Batch{{State: "success", Questions: map[string]Question{"a": Noul("x")}}, {State: "fail", Questions: map[string]Question{"b": Noul("x")}}}, 2)
	if err == nil {
		t.Fatal("expected sibling failure")
	}
	s := c.Stats()
	if s.Attempts != 2 || s.InputTokens != 400 || s.OutputTokens != 4 || s.MissingUsage != 1 || s.Unresolved != 0 || len(snapshots) != 4 {
		t.Fatalf("lost concurrent work: %+v snapshots=%d", s, len(snapshots))
	}
	if snapshots[0].Unresolved != 1 || snapshots[0].InputTokens != 0 {
		t.Fatal("start snapshot was not detached")
	}
}

func TestUnknownModelAndExplicitFreeRate(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"answers":{},"usage":{"input_tokens":300,"output_tokens":20}}`)
	}))
	defer srv.Close()
	for _, rate := range []*usage.Rate{nil, {Model: "free-test", Source: "test"}} {
		c := New(Config{BaseURL: srv.URL, Model: "free-test", Rate: rate})
		if _, err := c.Ask(context.Background(), "s", map[string]Question{"q": Noul("x")}); err != nil {
			t.Fatal(err)
		}
		s := c.Stats()
		if rate == nil && (s.UnpricedCalls != 1 || len(s.Calculations) != 0) {
			t.Fatalf("unknown model inherited Jev price: %+v", s)
		}
		if rate != nil && (s.Cost != 0 || s.UnpricedCalls != 0 || len(s.Calculations) != 1) {
			t.Fatalf("free rate became default: %+v", s)
		}
	}
}

func TestCheckpointFailureAndPrecanceledRequestsDoNotDispatch(t *testing.T) {
	var dispatched atomic.Int32
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) { dispatched.Add(1) })
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.Ask(ctx, "s", map[string]Question{"q": Noul("x")}); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if c.Stats().Attempts != 0 {
		t.Fatal("counted pre-canceled request")
	}
	c.RecordUsage(func(Stats) error { return errors.New("disk full") })
	if _, err := c.Ask(context.Background(), "s", map[string]Question{"q": Noul("x")}); err == nil || !strings.Contains(err.Error(), "disk full") {
		t.Fatal(err)
	}
	if dispatched.Load() != 0 || c.Stats().Attempts != 0 {
		t.Fatal("dispatched after failed start checkpoint")
	}
}

func TestAskAllDoesNotHideLateCheckpointFailure(t *testing.T) {
	started := make(chan struct{})
	c := New(Config{Model: usage.JevModel, HTTPClient: &http.Client{Transport: usageTransport(func(r *http.Request) (*http.Response, error) {
		body, _ := io.ReadAll(r.Body)
		status, response := 400, `{"error":"ordinary API failure"}`
		if strings.Contains(string(body), `"state":"late"`) {
			close(started)
			// Cancellation occurs after errgroup has selected the sibling's
			// ordinary API error. This dispatched response still arrives.
			<-r.Context().Done()
			status, response = 200, `{"answers":{},"usage":{"input_tokens":400,"output_tokens":4,"cost":0}}`
		} else {
			<-started
		}
		return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(response)), Header: http.Header{}}, nil
	})}})
	failed := false
	c.RecordUsage(func(s Stats) error {
		if s.InputTokens == 400 && !failed {
			failed = true
			return errors.New("transient checkpoint failure")
		}
		return nil
	})
	_, err := c.AskAll(context.Background(), []Batch{{State: "late", Questions: map[string]Question{"a": Noul("x")}}, {State: "first", Questions: map[string]Question{"b": Noul("x")}}}, 2)
	if !errors.Is(err, usage.ErrCheckpoint) {
		t.Fatalf("first API error hid checkpoint stop signal: %v", err)
	}
	if s := c.Stats(); s.InputTokens != 400 || s.Unresolved != 0 {
		t.Fatalf("lost late observations: %+v", s)
	}
}
