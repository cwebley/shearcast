// Package jev talks to Jev through OpenRouter's decisions endpoint.
package jev

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"sync"
	"time"

	"github.com/cwebley/shearcast/internal/usage"
	"github.com/cwebley/shearcast/internal/worklimit"
)

const (
	OpenRouterURL = "https://openrouter.ai/api/alpha/decisions"

	// DefaultInputCostPerMTok is Jev's input rate verified on 2026-09-23.
	// New calculations use usage.ModelRate to retain model and source provenance.
	DefaultInputCostPerMTok = 0.042
)

type QuestionType string

const (
	TypeChoice QuestionType = "choice"
	TypeScore  QuestionType = "score"
	TypeNoul   QuestionType = "noul"
)

// Question is one bounded thing to decide about the state.
type Question struct {
	Type         QuestionType `json:"type"`
	Instructions string       `json:"instructions"`
	// Criteria is map[string]string for a choice and []string for a score.
	// A noul has none.
	Criteria any `json:"criteria,omitempty"`
}

// Choice picks one key from criteria, which maps key to its description.
func Choice(instructions string, criteria map[string]string) Question {
	return Question{Type: TypeChoice, Instructions: instructions, Criteria: criteria}
}

// Score places the state on an ordered scale described by levels.
func Score(instructions string, levels []string) Question {
	return Question{Type: TypeScore, Instructions: instructions, Criteria: levels}
}

// Noul evaluates a proposition and returns its probability.
func Noul(instructions string) Question {
	return Question{Type: TypeNoul, Instructions: instructions}
}

type Request struct {
	Model     string              `json:"model"`
	State     string              `json:"state"`
	Questions map[string]Question `json:"questions"`
}

// Answer carries whichever fields match its type.
type Answer struct {
	Type          string             `json:"type"`
	Choice        string             `json:"choice,omitempty"`
	Score         float64            `json:"score,omitempty"`
	Noul          float64            `json:"noul,omitempty"`
	Confidence    float64            `json:"confidence,omitempty"`
	Probabilities map[string]float64 `json:"probabilities,omitempty"`
	Legend        map[string]string  `json:"legend,omitempty"`
}

// P returns the probability assigned to one choice key, or zero if the model
// did not mention it. For a noul, P ignores its argument and returns the noul.
func (a Answer) P(key string) float64 {
	if a.Type == string(TypeNoul) {
		return a.Noul
	}
	return a.Probabilities[key]
}

type Usage struct {
	InputTokens  int      `json:"input_tokens"`
	OutputTokens int      `json:"output_tokens"`
	Cost         *float64 `json:"cost,omitempty"`
}

type Response struct {
	Model   string            `json:"model"`
	Answers map[string]Answer `json:"answers"`
	Usage   Usage             `json:"usage"`
}

// Batch is one request: a slice of state plus the questions asked against it.
type Batch struct {
	State     string
	Questions map[string]Question
}

// Stats accumulates what a run cost, for reporting.
type Stats = usage.Summary

type Config struct {
	BaseURL    string // internal override for HTTP tests
	APIKey     string
	Model      string
	MaxRetries int
	Timeout    time.Duration
	Rate       *usage.Rate // optional explicit rate, including a genuine zero rate
	HTTPClient *http.Client
}

type Client struct {
	cfg  Config
	http *http.Client

	mu        sync.Mutex
	stats     Stats
	recorder  func(Stats) error
	recordErr error
}

func New(cfg Config) *Client {
	if cfg.BaseURL == "" {
		cfg.BaseURL = OpenRouterURL
	}
	if cfg.MaxRetries == 0 {
		cfg.MaxRetries = 3
	}
	if cfg.Timeout == 0 {
		cfg.Timeout = 60 * time.Second
	}
	if cfg.Rate == nil {
		cfg.Rate = usage.ModelRate(cfg.Model)
	}
	if cfg.Rate != nil {
		rate := *cfg.Rate
		cfg.Rate = &rate
	}
	httpClient := cfg.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: cfg.Timeout}
	}
	return &Client{cfg: cfg, http: httpClient}
}

// Stats returns a copy of what has been spent so far.
func (c *Client) Stats() Stats {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.stats.Clone()
}

// RecordUsage installs a synchronous checkpoint callback before processing.
// Calls are serialized with statistics updates. A failed checkpoint stops new
// requests, while already-dispatched requests still report their outcomes.
func (c *Client) RecordUsage(record func(Stats) error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.recorder, c.recordErr = record, nil
}

func (c *Client) checkpoint() error {
	if c.recorder != nil {
		if err := c.recorder(c.stats.Clone()); err != nil {
			c.recordErr = errors.Join(c.recordErr, usage.ErrCheckpoint, err)
		}
	}
	return c.recordErr
}

// Ask sends one batch of questions against one state.
func (c *Client) Ask(ctx context.Context, state string, questions map[string]Question) (*Response, error) {
	if len(questions) == 0 {
		return &Response{Answers: map[string]Answer{}}, nil
	}
	body, err := json.Marshal(Request{Model: c.cfg.Model, State: state, Questions: questions})
	if err != nil {
		return nil, fmt.Errorf("encoding request: %w", err)
	}

	var lastErr error
	for attempt := 0; attempt <= c.cfg.MaxRetries; attempt++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(backoff(attempt)):
			}
		}
		resp, err := c.attempt(ctx, body, len(questions))
		if err == nil {
			return resp, nil
		}
		lastErr = err
		if errors.Is(err, usage.ErrCheckpoint) || !retryable(err) {
			return nil, err
		}
	}
	return nil, fmt.Errorf("after %d attempts: %w", c.cfg.MaxRetries+1, lastErr)
}

func (c *Client) attempt(ctx context.Context, body []byte, questions int) (result *Response, resultErr error) {
	release, err := worklimit.Acquire(ctx, worklimit.Model)
	if err != nil {
		return nil, err
	}
	defer release()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.cfg.BaseURL, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.cfg.APIKey)
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	c.mu.Lock()
	if c.recordErr != nil {
		err := c.recordErr
		c.mu.Unlock()
		return nil, err
	}
	c.stats.Attempts++
	c.stats.Unresolved++
	err = c.checkpoint()
	if err != nil {
		c.stats.Attempts--
		c.stats.Unresolved--
	}
	c.mu.Unlock()
	if err != nil {
		return nil, fmt.Errorf("checkpointing model request: %w", err)
	}
	started := time.Now()
	var payload []byte
	defer func() {
		// Decode usage separately so malformed answers cannot discard billing
		// observations. Missing fields are not decoded as zero.
		var envelope struct {
			Usage json.RawMessage `json:"usage"`
		}
		var fields map[string]json.RawMessage
		if json.Unmarshal(payload, &envelope) == nil {
			_ = json.Unmarshal(envelope.Usage, &fields)
		}
		var input, output *int
		var cost *float64
		if err := json.Unmarshal(fields["input_tokens"], &input); err != nil {
			input = nil
		}
		if err := json.Unmarshal(fields["output_tokens"], &output); err != nil {
			output = nil
		}
		if err := json.Unmarshal(fields["cost"], &cost); err != nil {
			cost = nil
		}
		c.mu.Lock()
		defer c.mu.Unlock()
		c.stats.Unresolved--
		c.stats.Wall += time.Since(started)
		if resultErr == nil {
			c.stats.Calls++
			c.stats.Questions += questions
		}
		c.stats.Observe(input, output, cost, c.cfg.Rate)
		if err := c.checkpoint(); err != nil {
			resultErr = errors.Join(resultErr, fmt.Errorf("checkpointing model usage: %w", err))
		}
	}()

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, &apiError{Status: 0, Body: err.Error()}
	}
	defer resp.Body.Close()

	payload, err = io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, &apiError{Status: resp.StatusCode, Body: err.Error()}
	}
	if resp.StatusCode != http.StatusOK {
		return nil, &apiError{Status: resp.StatusCode, Body: string(payload)}
	}

	var out Response
	if err := json.Unmarshal(payload, &out); err != nil {
		return nil, fmt.Errorf("decoding response: %w (body: %.400s)", err, payload)
	}
	return &out, nil
}

type apiError struct {
	Status int
	Body   string
}

func (e *apiError) Error() string {
	if e.Status == 0 {
		return fmt.Sprintf("transport error: %s", e.Body)
	}
	return fmt.Sprintf("http %d: %.400s", e.Status, e.Body)
}

func retryable(err error) bool {
	var ae *apiError
	if !errors.As(err, &ae) {
		return false
	}
	return ae.Status == 0 || ae.Status == 408 || ae.Status == 429 || ae.Status >= 500
}

func backoff(attempt int) time.Duration {
	base := time.Duration(1<<uint(attempt-1)) * 500 * time.Millisecond
	jitter := time.Duration(rand.Int63n(int64(250 * time.Millisecond)))
	return base + jitter
}
