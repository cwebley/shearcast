package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
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

// A missing file is an error, not a silent run on defaults with no channels.
func TestLoadMissingFileSaysToRunInit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "absent.toml")
	_, err := Load(path)
	if !errors.Is(err, ErrNoConfig) {
		t.Fatalf("Load: got %v, want ErrNoConfig", err)
	}
	if !strings.Contains(err.Error(), path) || !strings.Contains(err.Error(), "shearcast init") {
		t.Errorf("error %q should name %s and suggest shearcast init", err, path)
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

func TestLoadRejectsSubscriptionPageSlug(t *testing.T) {
	path := writeConfig(t, `
[[channels]]
name = "A"
url = "https://example.com/a"
slug = "Subscribe"
`)
	if _, err := Load(path); err == nil || !strings.Contains(err.Error(), "reserved") {
		t.Errorf("err = %v, want the reserved subscription slug rejected", err)
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

func TestOutputBitrateDefaultsAndOverrides(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		want       int
	}{
		{"default", "", 128},
		{"global", "[audio]\nbitrate_kbps = 96", 96},
		{"channel", "[audio]\nbitrate_kbps = 96\n[[channels]]\nslug = 'test'\nbitrate_kbps = 64", 64},
		{"inherit", "[audio]\nbitrate_kbps = 96\n[[channels]]\nslug = 'test'\nbitrate_kbps = 0", 96},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg, err := Load(writeConfig(t, tc.body))
			if err != nil {
				t.Fatal(err)
			}
			ch, _ := cfg.ChannelBySlug("test")
			if got := cfg.OutputBitrate(ch); got != tc.want {
				t.Fatalf("bitrate = %d, want %d", got, tc.want)
			}
		})
	}
}

func TestLoadRejectsInvalidBitrates(t *testing.T) {
	for _, body := range []string{
		"[audio]\nbitrate_kbps = 0", "[audio]\nbitrate_kbps = 31",
		"[audio]\nbitrate_kbps = 321", "[audio]\nbitrate_kbps = -1",
		"[[channels]]\nslug = 'test'\nbitrate_kbps = -1",
		"[[channels]]\nslug = 'test'\nbitrate_kbps = 321",
	} {
		if _, err := Load(writeConfig(t, body)); err == nil || !strings.Contains(err.Error(), "bitrate") {
			t.Errorf("Load(%q) = %v, want bitrate error", body, err)
		}
	}
}

func TestLoadRejectsRemovedProviderSettings(t *testing.T) {
	for _, setting := range []string{"provider = 'openrouter'", "provider = 'typesafe'", "base_url = 'https://example.com'"} {
		if _, err := Load(writeConfig(t, "[jev]\n"+setting)); err == nil || !strings.Contains(err.Error(), "no longer supported") {
			t.Errorf("Load(%q) = %v, want migration error", setting, err)
		}
	}
}

func TestAPIKeyUsesOpenRouterWithLegacyAlias(t *testing.T) {
	t.Setenv("OPENROUTER_API_KEY", "canonical")
	t.Setenv("OPENROUTER_KEY", "legacy")
	t.Setenv("TYPESAFE_API_KEY", "ignored")
	if got, err := (Jev{}).APIKey(); err != nil || got != "canonical" {
		t.Fatalf("key = %q, %v", got, err)
	}
	t.Setenv("OPENROUTER_API_KEY", "")
	if got, err := (Jev{}).APIKey(); err != nil || got != "legacy" {
		t.Fatalf("alias = %q, %v", got, err)
	}
	t.Setenv("OPENROUTER_KEY", "")
	if _, err := (Jev{}).APIKey(); err == nil || !strings.Contains(err.Error(), "OPENROUTER_API_KEY") {
		t.Fatalf("missing key error = %v", err)
	}
}

func TestSlugsMustDifferByMoreThanCase(t *testing.T) {
	// A case-insensitive filesystem would give both channels one directory.
	if _, err := Load(writeConfig(t, "[[channels]]\nslug='Show'\n[[channels]]\nslug='show'\n")); err == nil {
		t.Fatal("accepted Show and show")
	}
}
