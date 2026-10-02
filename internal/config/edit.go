package config

import (
	"bytes"
	"fmt"
	"os"

	"github.com/BurntSushi/toml"
	"github.com/cwebley/shearcast/internal/fileutil"
)

// EditChannel applies only supplied fields, preserving all other TOML values.
// Callers hold the same state lock as sync for the entire read/edit/write.
// TOML comments and formatting are not retained by the encoder.
func EditChannel(path, slug string, add bool, fields map[string]any) error {
	if !ValidSlug(slug) {
		return fmt.Errorf("invalid channel slug %q", slug)
	}
	path, err := fileutil.CanonicalPath(path)
	if err != nil {
		return err
	}
	data, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	if _, err := decode(data, Default()); err != nil {
		return err
	}
	doc := map[string]any{}
	if _, err := toml.Decode(string(data), &doc); err != nil {
		return err
	}
	var channels []map[string]any
	switch value := doc["channels"].(type) {
	case nil:
	case []map[string]any:
		channels = value
	case []any:
		for _, entry := range value {
			ch, ok := entry.(map[string]any)
			if !ok {
				return fmt.Errorf("channels must contain TOML tables")
			}
			channels = append(channels, ch)
		}
	default:
		return fmt.Errorf("channels must be an array of TOML tables")
	}
	index := -1
	for i, ch := range channels {
		if ch["slug"] == slug {
			index = i
			break
		}
	}
	if add {
		if index >= 0 {
			return fmt.Errorf("channel %q already exists; use channel update", slug)
		}
		channels = append(channels, map[string]any{"slug": slug})
		index = len(channels) - 1
	} else if index < 0 {
		return fmt.Errorf("unknown channel %q", slug)
	}
	for k, v := range fields {
		if k == "slug" {
			return fmt.Errorf("channel slugs cannot be changed")
		}
		channels[index][k] = v
	}
	if _, ok := fields["latest"]; ok {
		delete(channels[index], "watch_limit")
	}
	doc["channels"] = channels
	var buf bytes.Buffer
	if err := toml.NewEncoder(&buf).Encode(doc); err != nil {
		return err
	}
	if _, err := decode(buf.Bytes(), Default()); err != nil {
		return err
	}
	mode := os.FileMode(0o600)
	if info, err := os.Stat(path); err == nil {
		mode = info.Mode().Perm()
	}
	return fileutil.WriteAtomic(path, buf.Bytes(), mode)
}
