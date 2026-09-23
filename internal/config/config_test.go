package config

import (
	"os"
	"path/filepath"
	"testing"
)

func writeConfig(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoadMissingFileReturnsDefaults(t *testing.T) {
	cfg, err := Load(filepath.Join(t.TempDir(), "absent.toml"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(cfg.Rules) != len(DefaultRules()) {
		t.Errorf("got %d rules, want the %d built-in defaults", len(cfg.Rules), len(DefaultRules()))
	}
}

func TestLoadRejectsDuplicateRuleIDs(t *testing.T) {
	path := writeConfig(t, `
[[rules]]
id = "sponsor"
prompt = "a"

[[rules]]
id = "sponsor"
prompt = "b"
`)
	if _, err := Load(path); err == nil {
		t.Error("expected an error for a duplicate rule id")
	}
}

func TestLoadRejectsRuleUsingReservedKeepID(t *testing.T) {
	path := writeConfig(t, `
[[rules]]
id = "content"
prompt = "a"
`)
	if _, err := Load(path); err == nil {
		t.Error("expected an error: \"content\" is reserved for the keep category")
	}
}

func TestLoadRejectsChannelWithoutSlug(t *testing.T) {
	path := writeConfig(t, `
[[channels]]
name = "Some Channel"
url = "https://www.youtube.com/@somechannel"
`)
	if _, err := Load(path); err == nil {
		t.Error("expected an error for a channel with no slug")
	}
}

func TestLoadRejectsDuplicateChannelSlugs(t *testing.T) {
	path := writeConfig(t, `
[[channels]]
name = "A"
url = "https://example.com/a"
slug = "dup"

[[channels]]
name = "B"
url = "https://example.com/b"
slug = "dup"
`)
	if _, err := Load(path); err == nil {
		t.Error("expected an error for two channels sharing a slug")
	}
}

func TestLoadRejectsChannelReferencingUnknownRule(t *testing.T) {
	path := writeConfig(t, `
[[channels]]
name = "A"
url = "https://example.com/a"
slug = "a"
rules = ["nonexistent"]
`)
	if _, err := Load(path); err == nil {
		t.Error("expected an error for a channel referencing an unconfigured rule")
	}
}

func TestLoadAcceptsAWellFormedConfig(t *testing.T) {
	path := writeConfig(t, `
[[rules]]
id = "sponsor"
prompt = "a paid promotion"
threshold = 0.8

[jev]
provider = "openrouter"
model = "typesafe/jev-1.13"

[[channels]]
name = "History of the Universe"
url = "https://www.youtube.com/@historyoftheuniverse"
slug = "hotu"
title = "HotU (de-sponsored)"
rules = ["sponsor"]
watch_limit = 5
`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	ch, ok := cfg.ChannelBySlug("hotu")
	if !ok {
		t.Fatal("channel hotu not found")
	}
	if ch.FeedTitle() != "HotU (de-sponsored)" {
		t.Errorf("FeedTitle: got %q", ch.FeedTitle())
	}
	if ch.FeedCategory() != "Technology" {
		t.Errorf("FeedCategory default: got %q, want Technology", ch.FeedCategory())
	}
}

func TestChannelFeedMetadataDefaultsToName(t *testing.T) {
	ch := Channel{Name: "Some Show"}
	if ch.FeedTitle() != "Some Show" {
		t.Errorf("FeedTitle: got %q, want the channel name", ch.FeedTitle())
	}
	if ch.FeedDescription() != "Some Show" {
		t.Errorf("FeedDescription: got %q, want the channel name", ch.FeedDescription())
	}
}

func TestChannelBySlugReportsMissing(t *testing.T) {
	cfg := Default()
	if _, ok := cfg.ChannelBySlug("nope"); ok {
		t.Error("expected ChannelBySlug to report false for an unconfigured slug")
	}
}
