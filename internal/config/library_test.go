package config

import (
	"os"
	"reflect"
	"testing"

	"github.com/BurntSushi/toml"
)

func TestLibraryLimitsAndMigration(t *testing.T) {
	for _, tc := range []struct {
		body         string
		latest, keep int
		invalid      bool
	}{
		{"", 5, 10, false}, {"latest=30\nkeep=30", 30, 30, false},
		{"watch_limit=7\nkeep=12", 7, 12, false},
		{"latest=0", 0, 0, true}, {"latest=-1", 0, 0, true}, {"keep=0", 0, 0, true},
		{"latest=6\nkeep=5", 0, 0, true}, {"watch_limit=-1", 0, 0, true},
		{"watch_limit=5\nlatest=5", 0, 0, true}, {"latest=30", 0, 0, true},
	} {
		t.Run(tc.body, func(t *testing.T) {
			cfg, err := Load(writeConfig(t, "[[channels]]\nslug='show'\n"+tc.body))
			if tc.invalid {
				if err == nil {
					t.Fatal("accepted invalid limits")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			ch := cfg.Channels[0]
			if ch.SelectionLimit() != tc.latest || ch.RetentionLimit() != tc.keep {
				t.Fatalf("wrong limits: %+v", ch)
			}
		})
	}
}

func TestChannelEditsPreserveRulesTuningAndUnknownValues(t *testing.T) {
	path := writeConfig(t, `
custom = "preserve me"
[[rules]]
id = 'quiet'
prompt = '''Skip the loud parts.
Keep everything else.'''
threshold = 0.72
[jev]
verify = false
parallel = 7
scan_window = 19.5
[audio]
bitrate_kbps = 64
[[channels]]
slug = 'show'
name = 'Original'
watch_limit = 5
rules = ['quiet']
no_weights = true
future_setting = 'keep this too'
[[channels]]
slug = 'other'
title = 'Untouched & title'
`)
	var before map[string]any
	if _, err := toml.DecodeFile(path, &before); err != nil {
		t.Fatal(err)
	}
	if err := EditChannel(path, "show", false, map[string]any{"latest": int64(8), "keep": int64(12), "title": "Changed"}); err != nil {
		t.Fatal(err)
	}
	var after map[string]any
	if _, err := toml.DecodeFile(path, &after); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"rules", "jev", "audio", "custom"} {
		if !reflect.DeepEqual(before[key], after[key]) {
			t.Fatalf("%s changed", key)
		}
	}
	oldChannels, newChannels := before["channels"].([]map[string]any), after["channels"].([]map[string]any)
	if !reflect.DeepEqual(oldChannels[1], newChannels[1]) {
		t.Fatal("other channel changed")
	}
	for _, key := range []string{"rules", "no_weights", "future_setting", "name"} {
		if !reflect.DeepEqual(oldChannels[0][key], newChannels[0][key]) {
			t.Fatalf("channel %s changed", key)
		}
	}
	if _, ok := newChannels[0]["watch_limit"]; ok {
		t.Fatal("alias was not migrated")
	}
	data, _ := os.ReadFile(path)
	if err := EditChannel(path, "show", false, map[string]any{"keep": 1}); err == nil {
		t.Fatal("accepted conflicting edit")
	}
	got, _ := os.ReadFile(path)
	if string(got) != string(data) {
		t.Fatal("invalid edit changed config")
	}
	if err := EditChannel(path, "new", true, map[string]any{"url": "https://youtube.com/@new"}); err != nil {
		t.Fatal(err)
	}
	if err := EditChannel(path, "new", true, nil); err == nil {
		t.Fatal("duplicate add succeeded")
	}
}

func TestChannelEditsAcceptInlineTablesWithoutLosingChannels(t *testing.T) {
	path := writeConfig(t, `channels = [{slug="old", name="Keep me", latest=3, keep=4, extra="preserved"}]`)
	if err := EditChannel(path, "old", false, map[string]any{"title": "Updated"}); err != nil {
		t.Fatal(err)
	}
	if err := EditChannel(path, "new", true, map[string]any{"url": "https://youtube.com/@new"}); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Channels) != 2 || cfg.Channels[0].Name != "Keep me" || cfg.Channels[0].Title != "Updated" {
		t.Fatalf("lost channel: %+v", cfg.Channels)
	}
	var raw map[string]any
	if _, err := toml.DecodeFile(path, &raw); err != nil {
		t.Fatal(err)
	}
	if raw["channels"].([]map[string]any)[0]["extra"] != "preserved" {
		t.Fatal("lost unknown value")
	}
}
