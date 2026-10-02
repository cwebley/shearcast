// Package usage describes reported model usage and reproducible token-cost
// calculations. A summary is a subtotal of observations, never an invoice.
package usage

import (
	"errors"
	"time"
)

// ErrCheckpoint stops further model work in the invocation when its durable
// accounting cannot be saved. Already-dispatched requests must still settle.
var ErrCheckpoint = errors.New("model usage checkpoint failed")

const JevModel = "typesafe/jev-1.13"

// Rate is copied into each calculation so future prices cannot rewrite history.
// A nil rate means unknown; a rate whose prices are zero means free.
type Rate struct {
	Model            string  `json:"model"`
	InputPerMillion  float64 `json:"input_usd_per_million"`
	OutputPerMillion float64 `json:"output_usd_per_million"`
	VerifiedAt       string  `json:"verified_at,omitempty"`
	Source           string  `json:"source"`
}

func ModelRate(model string) *Rate {
	if model != JevModel {
		return nil
	}
	return &Rate{Model: model, InputPerMillion: 0.042, OutputPerMillion: 0,
		VerifiedAt: "2026-09-23", Source: "https://openrouter.ai/api/v1/model/typesafe/jev-1.13"}
}

type Calculation struct {
	Rate         Rate    `json:"rate"`
	Observations int     `json:"observations"`
	InputTokens  int     `json:"input_tokens"`
	OutputTokens int     `json:"output_tokens"`
	USD          float64 `json:"usd"`
}

// The original Stats field names remain readable in legacy render records.
// Coverage counters distinguish missing observations from genuine zero usage.
type Summary struct {
	Calls             int
	Questions         int
	InputTokens       int
	OutputTokens      int
	Wall              time.Duration
	Cost              float64 // calculated subtotal; see Calculations and UnpricedCalls
	Attempts          int
	Unresolved        int // dispatched requests without a recorded outcome
	MissingUsage      int
	MissingCost       int
	UnpricedCalls     int
	ReportedCostCalls int
	ProviderCost      float64
	Calculations      []Calculation `json:",omitempty"`
}

func (s Summary) Clone() Summary {
	s.Calculations = append([]Calculation(nil), s.Calculations...)
	return s
}

// Add and Since operate on detached values. Rates are never averaged together.
func (s Summary) Add(other Summary) Summary    { return s.combine(other, 1) }
func (s Summary) Since(before Summary) Summary { return s.combine(before, -1) }

func (s Summary) combine(o Summary, sign int) Summary {
	s = s.Clone()
	s.Calls += sign * o.Calls
	s.Questions += sign * o.Questions
	s.InputTokens += sign * o.InputTokens
	s.OutputTokens += sign * o.OutputTokens
	s.Wall += time.Duration(sign) * o.Wall
	s.Cost += float64(sign) * o.Cost
	s.Attempts += sign * o.Attempts
	s.Unresolved += sign * o.Unresolved
	s.MissingUsage += sign * o.MissingUsage
	s.MissingCost += sign * o.MissingCost
	s.UnpricedCalls += sign * o.UnpricedCalls
	s.ReportedCostCalls += sign * o.ReportedCostCalls
	s.ProviderCost += float64(sign) * o.ProviderCost
	for _, c := range o.Calculations {
		found := false
		for i := range s.Calculations {
			if s.Calculations[i].Rate == c.Rate {
				s.Calculations[i].Observations += sign * c.Observations
				s.Calculations[i].InputTokens += sign * c.InputTokens
				s.Calculations[i].OutputTokens += sign * c.OutputTokens
				s.Calculations[i].USD += float64(sign) * c.USD
				found = true
				break
			}
		}
		if !found {
			c.Observations *= sign
			c.InputTokens *= sign
			c.OutputTokens *= sign
			c.USD *= float64(sign)
			s.Calculations = append(s.Calculations, c)
		}
	}
	kept := s.Calculations[:0]
	for _, c := range s.Calculations {
		if c.Observations != 0 {
			kept = append(kept, c)
		}
	}
	s.Calculations = kept
	return s
}

// Observe records an HTTP outcome. Tokens and returned cost are independent:
// a usable cost may be present even when token counts are missing or invalid.
func (s *Summary) Observe(input, output *int, cost *float64, rate *Rate) {
	if input != nil && *input >= 0 {
		s.InputTokens += *input
	}
	if output != nil && *output >= 0 {
		s.OutputTokens += *output
	}
	if input == nil || output == nil || *input < 0 || *output < 0 {
		s.MissingUsage++
	} else {
		if rate == nil {
			s.UnpricedCalls++
		} else {
			usd := (float64(*input)*rate.InputPerMillion + float64(*output)*rate.OutputPerMillion) / 1e6
			*s = s.Add(Summary{Cost: usd, Calculations: []Calculation{{Rate: *rate, Observations: 1, InputTokens: *input, OutputTokens: *output, USD: usd}}})
		}
	}
	if cost == nil || *cost < 0 {
		s.MissingCost++
	} else {
		s.ReportedCostCalls++
		s.ProviderCost += *cost
	}
}

type Attempt struct {
	ID               string    `json:"id"`
	Model            string    `json:"model"`
	StartedAt        time.Time `json:"started_at"`
	FinishedAt       time.Time `json:"finished_at,omitempty"`
	SyncStartedAt    time.Time `json:"sync_started_at,omitempty"`
	ChannelStartedAt time.Time `json:"channel_started_at,omitempty"`
	Usage            Summary   `json:"usage"`
}

type History struct {
	Since               time.Time `json:"since"`
	Latest              Attempt   `json:"latest"`
	Total               Summary   `json:"total"`
	InterruptedAttempts int       `json:"interrupted_attempts,omitempty"`
}

func (h *History) Clone() *History {
	if h == nil {
		return nil
	}
	c := *h
	c.Latest.Usage = c.Latest.Usage.Clone()
	c.Total = c.Total.Clone()
	return &c
}
