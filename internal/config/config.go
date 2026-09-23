// Package config loads the TOML file that defines what to cut and where to
// publish it. Everything has a working default, so `render` runs with no
// config file at all beyond the built-in rules.
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"
	"github.com/cwebley/shearcast/internal/detect"
	"github.com/cwebley/shearcast/internal/jev"
)

// Rule is one user-defined thing to cut, written in plain English. This is the
// reason to use a decision model rather than a fixed crowd-sourced vocabulary:
// the vocabulary is yours, not a list somebody else voted on.
//
// Threshold is currently unused: nothing in internal/detect reads it. The
// only threshold that gates anchor formation is the single global
// Jev.AnchorThreshold, applied identically to every rule. Per-rule confidence
// tuning would need Threshold actually wired into detect.clusters (or
// wherever else it'd matter) -- left as-is for now rather than either wiring
// it up or removing the field.
type Rule struct {
	ID        string  `toml:"id"`
	Prompt    string  `toml:"prompt"`
	Threshold float64 `toml:"threshold"`
}

// Jev holds provider settings and the four-pass tuning knobs.
type Jev struct {
	Provider string `toml:"provider"` // "openrouter" or "typesafe"
	BaseURL  string `toml:"base_url"` // overrides Provider when set
	Model    string `toml:"model"`
	Parallel int    `toml:"parallel"`

	ScanWindow       float64 `toml:"scan_window"`
	ScanMaxWindow    float64 `toml:"scan_max_window"`
	ScanBatchSize    int     `toml:"scan_batch_size"`
	AnchorThreshold  float64 `toml:"anchor_threshold"`
	LeadInSpan       float64 `toml:"lead_in_span"`
	TailSpan         float64 `toml:"tail_span"`
	MinSentence      int     `toml:"min_sentence"`
	MaxCandidates    int     `toml:"max_candidates"`
	ContextSpan      float64 `toml:"context_span"`
	Verify           *bool   `toml:"verify"`
	Weights          string  `toml:"weights"`
	MinRegion        float64 `toml:"min_region"`
	EndFitWindow     int     `toml:"end_fit_window"`
	ClusterBridgeGap float64 `toml:"cluster_bridge_gap"`
	SegmentMergeGap  float64 `toml:"segment_merge_gap"`
}

// Channel is one source, published as its own podcast feed.
type Channel struct {
	Name        string   `toml:"name"`
	URL         string   `toml:"url"`
	Slug        string   `toml:"slug"`        // storage key prefix and feed identifier
	Title       string   `toml:"title"`       // feed title; defaults to Name
	Description string   `toml:"description"` // feed description; defaults to Name
	Category    string   `toml:"category"`    // itunes:category; defaults to "Technology"
	Rules       []string `toml:"rules"`       // rule ids; empty means all
	WatchLimit  int      `toml:"watch_limit"` // how many latest uploads sync checks
	NoWeights   bool     `toml:"no_weights"`  // skip the fitted start-edge model
}

type Config struct {
	Rules    []Rule    `toml:"rules"`
	Jev      Jev       `toml:"jev"`
	Channels []Channel `toml:"channels"`
}

// DefaultRules cover the three things almost everyone wants gone. Add your own
// in config; they are just prose.
func DefaultRules() []Rule {
	return []Rule{
		{
			ID:        "sponsor",
			Prompt:    "A paid promotion: the lead-in, the pitch, and the offer for a product or service whose maker paid for the placement",
			Threshold: 0.80,
		},
		{
			ID:        "selfpromo",
			Prompt:    "Unpaid promotion of the creator's own Patreon, merch, membership, newsletter, or other channels",
			Threshold: 0.85,
		},
		{
			ID:        "interaction",
			Prompt:    "Asking viewers to like, subscribe, comment, join, or hit the bell, with no other content",
			Threshold: 0.85,
		},
	}
}

// Default returns a usable config with no file present.
func Default() *Config {
	return &Config{
		Rules: DefaultRules(),
		Jev: Jev{
			Provider: "openrouter",
			Model:    "typesafe/jev-1.13",
			Parallel: 4,
			Weights:  "data/weights.json",
		},
	}
}

// Load reads path, filling anything absent from the defaults. A missing file is
// not an error: it means run on defaults.
func Load(path string) (*Config, error) {
	cfg := Default()

	// Credentials load before anything else: running on built-in defaults with
	// no config.toml is a supported path, and it still needs an API key.
	dir := "."
	if path != "" {
		dir = filepath.Dir(path)
	}
	loadDotenv(filepath.Join(dir, ".env"))

	if path == "" {
		return cfg, nil
	}
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return cfg, nil
	}
	if err != nil {
		return nil, err
	}

	var file Config
	if _, err := toml.Decode(string(data), &file); err != nil {
		return nil, fmt.Errorf("parsing %s: %w", path, err)
	}
	if len(file.Rules) > 0 {
		cfg.Rules = file.Rules
	}
	if len(file.Channels) > 0 {
		cfg.Channels = file.Channels
	}
	mergeJev(&cfg.Jev, file.Jev)

	if err := cfg.validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

func (c *Config) validate() error {
	seen := map[string]bool{}
	for _, r := range c.Rules {
		if r.ID == "" || r.Prompt == "" {
			return fmt.Errorf("every rule needs an id and a prompt (got id=%q)", r.ID)
		}
		if r.ID == detect.KeepRule {
			return fmt.Errorf("%q is reserved for the keep category", detect.KeepRule)
		}
		if seen[r.ID] {
			return fmt.Errorf("duplicate rule id %q", r.ID)
		}
		seen[r.ID] = true
	}
	slugs := map[string]bool{}
	for _, ch := range c.Channels {
		if ch.Slug == "" {
			return fmt.Errorf("channel %q needs a slug", ch.Name)
		}
		if slugs[ch.Slug] {
			return fmt.Errorf("duplicate channel slug %q", ch.Slug)
		}
		slugs[ch.Slug] = true
		for _, id := range ch.Rules {
			if !seen[id] {
				return fmt.Errorf("channel %q references unknown rule %q", ch.Name, id)
			}
		}
	}
	return nil
}

// BaseURL resolves the provider name to an endpoint.
func (j Jev) ResolveBaseURL() string {
	if j.BaseURL != "" {
		return j.BaseURL
	}
	if strings.EqualFold(j.Provider, "typesafe") {
		return jev.TypeSafeURL
	}
	return jev.OpenRouterURL
}

// APIKey reads the key for the configured provider from the environment.
func (j Jev) APIKey() (string, error) {
	var names []string
	if strings.EqualFold(j.Provider, "typesafe") {
		names = []string{"TYPESAFE_API_KEY"}
	} else {
		names = []string{"OPENROUTER_API_KEY", "OPENROUTER_KEY"}
	}
	for _, n := range names {
		if v := os.Getenv(n); v != "" {
			return v, nil
		}
	}
	return "", fmt.Errorf("set %s (or put it in .env next to your config)", strings.Join(names, " or "))
}

// DetectOptions converts the config into detector options.
func (c *Config) DetectOptions(ruleIDs []string) detect.Options {
	var rules []detect.Rule
	for _, r := range c.Rules {
		if len(ruleIDs) > 0 && !contains(ruleIDs, r.ID) {
			continue
		}
		rules = append(rules, detect.Rule{ID: r.ID, Prompt: r.Prompt, Threshold: r.Threshold})
	}
	o := detect.Defaults(rules)
	j := c.Jev
	setF(&o.ScanWindow, j.ScanWindow)
	setF(&o.ScanMaxWindow, j.ScanMaxWindow)
	setF(&o.AnchorThreshold, j.AnchorThreshold)
	setF(&o.LeadInSpan, j.LeadInSpan)
	setF(&o.TailSpan, j.TailSpan)
	setF(&o.ContextSpan, j.ContextSpan)
	setF(&o.MinRegion, j.MinRegion)
	setF(&o.ClusterBridgeGap, j.ClusterBridgeGap)
	setF(&o.SegmentMergeGap, j.SegmentMergeGap)
	setI(&o.ScanBatchSize, j.ScanBatchSize)
	setI(&o.MinSentence, j.MinSentence)
	setI(&o.MaxCandidates, j.MaxCandidates)
	setI(&o.Parallel, j.Parallel)
	setI(&o.EndFitWindow, j.EndFitWindow)
	if j.Verify != nil {
		o.Verify = *j.Verify
	}
	return o
}

// RuleIDs lists every configured rule id.
func (c *Config) RuleIDs() []string {
	out := make([]string, 0, len(c.Rules))
	for _, r := range c.Rules {
		out = append(out, r.ID)
	}
	return out
}

// ChannelBySlug finds a configured channel, or reports false.
func (c *Config) ChannelBySlug(slug string) (Channel, bool) {
	for _, ch := range c.Channels {
		if ch.Slug == slug {
			return ch, true
		}
	}
	return Channel{}, false
}

// FeedTitle is the channel's title, falling back to its name.
func (ch Channel) FeedTitle() string {
	if ch.Title != "" {
		return ch.Title
	}
	return ch.Name
}

// FeedDescription is the channel's description, falling back to its name.
func (ch Channel) FeedDescription() string {
	if ch.Description != "" {
		return ch.Description
	}
	return ch.Name
}

// FeedCategory is the channel's itunes:category, defaulting to Technology.
func (ch Channel) FeedCategory() string {
	if ch.Category != "" {
		return ch.Category
	}
	return "Technology"
}

func mergeJev(dst *Jev, src Jev) {
	if src.Provider != "" {
		dst.Provider = src.Provider
	}
	if src.BaseURL != "" {
		dst.BaseURL = src.BaseURL
	}
	if src.Model != "" {
		dst.Model = src.Model
	}
	if src.Parallel > 0 {
		dst.Parallel = src.Parallel
	}
	dst.ScanWindow, dst.ScanMaxWindow = src.ScanWindow, src.ScanMaxWindow
	dst.ScanBatchSize, dst.AnchorThreshold = src.ScanBatchSize, src.AnchorThreshold
	dst.LeadInSpan, dst.TailSpan = src.LeadInSpan, src.TailSpan
	dst.MinSentence, dst.MaxCandidates = src.MinSentence, src.MaxCandidates
	dst.ContextSpan, dst.MinRegion = src.ContextSpan, src.MinRegion
	dst.EndFitWindow = src.EndFitWindow
	dst.ClusterBridgeGap, dst.SegmentMergeGap = src.ClusterBridgeGap, src.SegmentMergeGap
	dst.Verify = src.Verify
	if src.Weights != "" {
		dst.Weights = src.Weights
	}
}

func setF(dst *float64, v float64) {
	if v > 0 {
		*dst = v
	}
}

func setI(dst *int, v int) {
	if v > 0 {
		*dst = v
	}
}

func contains(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}

// loadDotenv fills the environment from a .env file without clobbering real vars.
func loadDotenv(path string) {
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		if os.Getenv(key) == "" {
			os.Setenv(key, strings.Trim(strings.TrimSpace(value), `"'`))
		}
	}
}
