// Package config loads the TOML file that defines what to cut and where to
// publish it. Everything has a working default, so `render` runs with no
// config file at all beyond the built-in rules.
package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"
	"github.com/cwebley/shearcast/internal/detect"
	"github.com/cwebley/shearcast/internal/render"
	"github.com/cwebley/shearcast/internal/subscribe"
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

// Jev holds the OpenRouter model and the four-pass tuning knobs.
type Jev struct {
	Model    string `toml:"model"`
	Parallel int    `toml:"parallel"`

	ScanWindow       float64 `toml:"scan_window"`
	ScanMaxWindow    float64 `toml:"scan_max_window"`
	ScanBatchSize    int     `toml:"scan_batch_size"`
	AnchorThreshold  float64 `toml:"anchor_threshold"`
	WeakThreshold    float64 `toml:"weak_anchor_threshold"`
	LocalThreshold   float64 `toml:"localized_threshold"`
	OutroTrimFloor   float64 `toml:"outro_trim_floor"`
	LeadInSpan       float64 `toml:"lead_in_span"`
	TailSpan         float64 `toml:"tail_span"`
	MinSentence      int     `toml:"min_sentence"`
	MaxCandidates    int     `toml:"max_candidates"`
	ContextSpan      float64 `toml:"context_span"`
	Weights          string  `toml:"weights"`
	EndWeights       string  `toml:"end_weights"`
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
	WatchLimit  int      `toml:"watch_limit"` // legacy alias for latest
	Latest      int      `toml:"latest"`
	Keep        int      `toml:"keep"`
	Disabled    bool     `toml:"disabled"`
	NoWeights   bool     `toml:"no_weights"`   // skip the fitted start-edge model
	KeepTail    bool     `toml:"keep_tail"`    // keep audio after the last spoken caption, such as closing music
	BitrateKbps int      `toml:"bitrate_kbps"` // zero inherits the global audio bitrate
}

type Audio struct {
	BitrateKbps int `toml:"bitrate_kbps"`
}

type Publishing struct {
	Backend   string `toml:"backend"` // r2 or filesystem; omitted preserves R2 setups
	Directory string `toml:"directory"`
	BaseURL   string `toml:"base_url"`
}

type Serve struct {
	Listen string `toml:"listen"`
}

type Config struct {
	Publishing Publishing `toml:"publishing"`
	Serve      Serve      `toml:"serve"`
	Audio      Audio      `toml:"audio"`
	Rules      []Rule     `toml:"rules"`
	Jev        Jev        `toml:"jev"`
	Channels   []Channel  `toml:"channels"`
}

// DefaultRules cover the things almost everyone wants gone. Add your own
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
			Prompt:    "Unpaid promotion of the creator's own channel: asking viewers to like, subscribe, comment or hit the bell, or promoting their Patreon, merch, membership, newsletter or other channels",
			Threshold: 0.85,
		},
		{
			ID:        "credits",
			Prompt:    "Housekeeping about the show itself rather than its subject: production credits (who produced, edited, mixed or scored it), network identification, how to contact the show by email or hotline, and an announcement of a break (\"we'll be right back\") with any music around it. A teaser for an upcoming episode, a sign-on or sign-off, and a thank-you to a guest are not part of it",
			Threshold: 0.85,
		},
	}
}

// Default returns a usable config with no file present.
func Default() *Config {
	return &Config{
		Publishing: Publishing{Backend: "r2"},
		Serve:      Serve{Listen: "127.0.0.1:8080"},
		Rules:      DefaultRules(),
		Audio:      Audio{BitrateKbps: render.DefaultBitrateKbps},
		Jev: Jev{
			Model:    "typesafe/jev-1.13",
			Parallel: 4,
		},
	}
}

// ErrNoConfig means the config file does not exist. Commands other than init
// and channel add need one, because it is where channels are defined.
var ErrNoConfig = errors.New("no config")

// Load reads path, filling anything absent from the defaults.
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
		return nil, fmt.Errorf("%w at %s; run `shearcast init` to create one", ErrNoConfig, path)
	}
	if err != nil {
		return nil, err
	}

	cfg, err = decode(data, cfg)
	if err != nil {
		return nil, err
	}
	if cfg.Publishing.Directory != "" && !filepath.IsAbs(cfg.Publishing.Directory) {
		cfg.Publishing.Directory = filepath.Join(dir, cfg.Publishing.Directory)
	}
	// Fitted weights are optional: without any, the start edge uses the plain
	// predicate. A configured path must exist; LoadWeights reports it if not.
	switch {
	case cfg.Jev.Weights == "":
		if beside := filepath.Join(dir, "weights.json"); fileExists(beside) {
			cfg.Jev.Weights = beside
		}
	case !filepath.IsAbs(cfg.Jev.Weights):
		cfg.Jev.Weights = filepath.Join(dir, cfg.Jev.Weights)
	}
	// End weights are fitted alongside the start ones, so by default they are
	// looked for beside them.
	switch {
	case cfg.Jev.EndWeights == "" && cfg.Jev.Weights != "":
		if beside := filepath.Join(filepath.Dir(cfg.Jev.Weights), "end-weights.json"); fileExists(beside) {
			cfg.Jev.EndWeights = beside
		}
	case cfg.Jev.EndWeights != "" && !filepath.IsAbs(cfg.Jev.EndWeights):
		cfg.Jev.EndWeights = filepath.Join(dir, cfg.Jev.EndWeights)
	}
	return cfg, nil
}

func decode(data []byte, cfg *Config) (*Config, error) {
	var file Config
	md, err := toml.Decode(string(data), &file)
	if err != nil {
		return nil, fmt.Errorf("parsing config: %w", err)
	}
	for _, key := range []string{"provider", "base_url"} {
		if md.IsDefined("jev", key) {
			return nil, fmt.Errorf("jev.%s is no longer supported; remove it and use OPENROUTER_API_KEY", key)
		}
	}
	// Decode presence separately: explicit zero must not mean unlimited.
	var raw struct {
		Channels []map[string]any `toml:"channels"`
	}
	if _, err := toml.Decode(string(data), &raw); err != nil {
		return nil, err
	}
	for _, ch := range raw.Channels {
		for _, key := range []string{"latest", "keep", "watch_limit"} {
			if value, ok := ch[key]; ok {
				n, ok := value.(int64)
				if !ok || n <= 0 {
					return nil, fmt.Errorf("channel %v: %s must be a positive finite integer", ch["slug"], key)
				}
			}
		}
		if _, a := ch["latest"]; a {
			if _, b := ch["watch_limit"]; b {
				return nil, fmt.Errorf("channel %v: use latest instead of watch_limit, not both", ch["slug"])
			}
		}
	}
	if md.IsDefined("audio", "bitrate_kbps") {
		cfg.Audio = file.Audio
	}
	if md.IsDefined("publishing") {
		cfg.Publishing = file.Publishing
		if cfg.Publishing.Backend == "" {
			cfg.Publishing.Backend = "r2"
		}
	}
	if md.IsDefined("serve", "listen") {
		cfg.Serve = file.Serve
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
	if c.Publishing.Backend != "r2" && c.Publishing.Backend != "filesystem" {
		return fmt.Errorf("publishing.backend must be r2 or filesystem")
	}
	if c.Publishing.Backend == "filesystem" && (c.Publishing.Directory == "" || c.Publishing.BaseURL == "") {
		return fmt.Errorf("filesystem publishing requires publishing.directory and publishing.base_url")
	}
	if err := render.ValidateBitrate(c.Audio.BitrateKbps); err != nil {
		return fmt.Errorf("audio.bitrate_kbps: %w", err)
	}
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
		if ch.Latest < 0 || ch.WatchLimit < 0 || ch.Keep < 0 || ch.RetentionLimit() < ch.SelectionLimit() {
			return fmt.Errorf("channel %q: latest must be positive and keep must be at least latest", ch.Slug)
		}
		if ch.BitrateKbps != 0 {
			if err := render.ValidateBitrate(ch.BitrateKbps); err != nil {
				return fmt.Errorf("channel %q bitrate_kbps: %w", ch.Name, err)
			}
		}
		if !ValidSlug(ch.Slug) {
			return fmt.Errorf("channel %q needs a slug containing only letters, digits, hyphens or underscores", ch.Name)
		}
		// Case-insensitive because a macOS publishing directory is.
		if strings.EqualFold(ch.Slug, subscribe.Dir) {
			return fmt.Errorf("channel %q: slug %q is reserved for the subscription page", ch.Name, ch.Slug)
		}
		// Also case-insensitive: Show and show would share a directory there
		// while state kept them as separate libraries.
		if slugs[strings.ToLower(ch.Slug)] {
			return fmt.Errorf("duplicate channel slug %q (slugs must differ by more than letter case)", ch.Slug)
		}
		slugs[strings.ToLower(ch.Slug)] = true
		for _, id := range ch.Rules {
			if !seen[id] {
				return fmt.Errorf("channel %q references unknown rule %q", ch.Name, id)
			}
		}
	}
	return nil
}

func ValidSlug(slug string) bool {
	if slug == "" {
		return false
	}
	for _, r := range slug {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_') {
			return false
		}
	}
	return true
}

func (ch Channel) SelectionLimit() int {
	if ch.Latest != 0 {
		return ch.Latest
	}
	if ch.WatchLimit != 0 {
		return ch.WatchLimit
	}
	return 5
}

func (ch Channel) RetentionLimit() int {
	if ch.Keep != 0 {
		return ch.Keep
	}
	return 10
}

// OutputBitrate resolves a channel override against the global setting.
func (c *Config) OutputBitrate(ch Channel) int {
	if ch.BitrateKbps != 0 {
		return ch.BitrateKbps
	}
	return c.Audio.BitrateKbps
}

// APIKey reads the OpenRouter key. OPENROUTER_KEY remains a legacy alias.
func (j Jev) APIKey() (string, error) {
	for _, n := range []string{"OPENROUTER_API_KEY", "OPENROUTER_KEY"} {
		if v := os.Getenv(n); v != "" {
			return v, nil
		}
	}
	return "", fmt.Errorf("set OPENROUTER_API_KEY (or put it in .env next to your config)")
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
	setF(&o.WeakAnchorThreshold, j.WeakThreshold)
	setF(&o.LocalizedThreshold, j.LocalThreshold)
	setF(&o.OutroTrimFloor, j.OutroTrimFloor)
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
	if src.Model != "" {
		dst.Model = src.Model
	}
	if src.Parallel > 0 {
		dst.Parallel = src.Parallel
	}
	dst.ScanWindow, dst.ScanMaxWindow = src.ScanWindow, src.ScanMaxWindow
	dst.ScanBatchSize, dst.AnchorThreshold = src.ScanBatchSize, src.AnchorThreshold
	dst.WeakThreshold = src.WeakThreshold
	dst.LocalThreshold, dst.OutroTrimFloor = src.LocalThreshold, src.OutroTrimFloor
	dst.LeadInSpan, dst.TailSpan = src.LeadInSpan, src.TailSpan
	dst.MinSentence, dst.MaxCandidates = src.MinSentence, src.MaxCandidates
	dst.ContextSpan, dst.MinRegion = src.ContextSpan, src.MinRegion
	dst.EndFitWindow = src.EndFitWindow
	dst.ClusterBridgeGap, dst.SegmentMergeGap = src.ClusterBridgeGap, src.SegmentMergeGap
	if src.EndWeights != "" {
		dst.EndWeights = src.EndWeights
	}
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

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}
