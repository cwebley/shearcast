package jev

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

func newTestClient(t *testing.T, handler http.HandlerFunc) *Client {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return New(Config{BaseURL: srv.URL, APIKey: "test-key", Model: "typesafe/jev-1.13", MaxRetries: 3})
}

func TestAskSendsDocumentedWireFormat(t *testing.T) {
	var got Request
	var authHeader string

	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		authHeader = r.Header.Get("Authorization")
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Errorf("decoding request: %v", err)
		}
		json.NewEncoder(w).Encode(Response{
			Model: "jev-1.13",
			Answers: map[string]Answer{
				"W001": {Type: "choice", Choice: "sponsor", Confidence: 0.91,
					Probabilities: map[string]float64{"sponsor": 0.93, "content": 0.07}},
			},
			Usage: Usage{InputTokens: 512},
		})
	})

	resp, err := c.Ask(context.Background(), "some transcript", map[string]Question{
		"W001": Choice("Window [W001] is best described as", map[string]string{
			"content": "The video's subject matter",
			"sponsor": "A paid promotion",
		}),
	})
	if err != nil {
		t.Fatalf("Ask: %v", err)
	}

	if authHeader != "Bearer test-key" {
		t.Errorf("Authorization: got %q, want %q", authHeader, "Bearer test-key")
	}
	if got.Model != "typesafe/jev-1.13" {
		t.Errorf("model: got %q", got.Model)
	}
	if got.State != "some transcript" {
		t.Errorf("state: got %q", got.State)
	}
	q := got.Questions["W001"]
	if q.Type != TypeChoice {
		t.Errorf("question type: got %q, want choice", q.Type)
	}
	crit, ok := q.Criteria.(map[string]any)
	if !ok || crit["sponsor"] != "A paid promotion" {
		t.Errorf("criteria did not round-trip as an object: %#v", q.Criteria)
	}
	if p := resp.Answers["W001"].P("sponsor"); p != 0.93 {
		t.Errorf("P(sponsor): got %v, want 0.93", p)
	}
}

func TestNoulOmitsCriteria(t *testing.T) {
	var raw map[string]any
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&raw)
		json.NewEncoder(w).Encode(Response{Answers: map[string]Answer{
			"C01": {Type: "noul", Noul: 0.88},
		}})
	})

	resp, err := c.Ask(context.Background(), "state", map[string]Question{
		"C01": Noul("Chunk [C01] leads into the sponsored segment"),
	})
	if err != nil {
		t.Fatalf("Ask: %v", err)
	}

	questions := raw["questions"].(map[string]any)
	c01 := questions["C01"].(map[string]any)
	if _, present := c01["criteria"]; present {
		t.Error("a noul must not send a criteria field")
	}
	// P ignores its argument for a noul and returns the probability.
	if p := resp.Answers["C01"].P("anything"); p != 0.88 {
		t.Errorf("P: got %v, want 0.88", p)
	}
}

func TestAskRetriesTransientFailures(t *testing.T) {
	var calls int32
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if n := atomic.AddInt32(&calls, 1); n < 3 {
			w.WriteHeader(http.StatusTooManyRequests)
			fmt.Fprint(w, `{"error":"slow down"}`)
			return
		}
		json.NewEncoder(w).Encode(Response{Answers: map[string]Answer{"W001": {Type: "noul", Noul: 1}}})
	})

	if _, err := c.Ask(context.Background(), "s", map[string]Question{"W001": Noul("x")}); err != nil {
		t.Fatalf("Ask: %v", err)
	}
	if calls != 3 {
		t.Errorf("got %d calls, want 3 (two retries then success)", calls)
	}
}

func TestAskDoesNotRetryBadRequest(t *testing.T) {
	var calls int32
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprint(w, `{"error":"malformed criteria"}`)
	})

	if _, err := c.Ask(context.Background(), "s", map[string]Question{"W001": Noul("x")}); err == nil {
		t.Fatal("expected an error")
	}
	if calls != 1 {
		t.Errorf("got %d calls, want 1: a 400 is not retryable", calls)
	}
}

func TestStatsAccumulateCost(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(Response{
			Answers: map[string]Answer{"W001": {Type: "noul", Noul: 1}},
			Usage:   Usage{InputTokens: 1_000_000},
		})
	})
	for i := 0; i < 2; i++ {
		if _, err := c.Ask(context.Background(), "s", map[string]Question{"W001": Noul("x")}); err != nil {
			t.Fatalf("Ask: %v", err)
		}
	}
	s := c.Stats()
	if s.Calls != 2 || s.Questions != 2 {
		t.Errorf("got calls=%d questions=%d, want 2 and 2", s.Calls, s.Questions)
	}
	if s.InputTokens != 2_000_000 {
		t.Errorf("InputTokens: got %d, want 2000000", s.InputTokens)
	}
	// Two million input tokens at $0.042 per million.
	if want := 0.084; s.Cost < want-1e-9 || s.Cost > want+1e-9 {
		t.Errorf("Cost: got %v, want %v", s.Cost, want)
	}
}

func TestAskAllMergesBatches(t *testing.T) {
	var calls int32
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		var req Request
		json.NewDecoder(r.Body).Decode(&req)
		answers := map[string]Answer{}
		for id := range req.Questions {
			answers[id] = Answer{Type: "noul", Noul: 0.5}
		}
		json.NewEncoder(w).Encode(Response{Answers: answers})
	})

	batches := []Batch{
		{State: "a", Questions: map[string]Question{"W001": Noul("x"), "W002": Noul("x")}},
		{State: "b", Questions: map[string]Question{"W003": Noul("x")}},
	}
	got, err := c.AskAll(context.Background(), batches, 2)
	if err != nil {
		t.Fatalf("AskAll: %v", err)
	}
	if len(got) != 3 {
		t.Errorf("got %d answers %v, want 3", len(got), got)
	}
	if calls != 2 {
		t.Errorf("got %d calls, want 2", calls)
	}
}

func TestAskAllPropagatesFailure(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprint(w, `{"error":"nope"}`)
	})
	_, err := c.AskAll(context.Background(), []Batch{
		{State: "a", Questions: map[string]Question{"W001": Noul("x")}},
	}, 2)
	if err == nil {
		t.Fatal("expected the batch error to surface")
	}
}

func TestChunk(t *testing.T) {
	questions := map[string]Question{}
	var ids []string
	for i := 1; i <= 10; i++ {
		id := fmt.Sprintf("W%03d", i)
		ids = append(ids, id)
		questions[id] = Noul("x")
	}
	batches := Chunk("shared state", questions, ids, 4)
	if len(batches) != 3 {
		t.Fatalf("got %d batches, want 3", len(batches))
	}
	total := 0
	for _, b := range batches {
		if b.State != "shared state" {
			t.Errorf("batch state: got %q", b.State)
		}
		total += len(b.Questions)
	}
	if total != 10 {
		t.Errorf("got %d questions across batches, want 10", total)
	}
}
