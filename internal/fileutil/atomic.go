// Package fileutil provides durable replacement of small local records.
package fileutil

import (
	"fmt"
	"os"
	"path/filepath"
)

// WriteAtomic syncs a new file before replacing path, then syncs its directory.
func WriteAtomic(path string, data []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	if err := MkdirAll(dir); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".record-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	defer f.Close()
	if err := f.Chmod(mode); err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(f.Name(), path); err != nil {
		return err
	}
	return SyncDir(dir)
}

// MkdirAll persists new directory entries in their parents, not just the leaf.
// Syncing only a file's immediate directory can lose a newly created ancestor
// on power loss even though the file and a checkpoint elsewhere were synced.
func MkdirAll(path string) error {
	path, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	var missing []string
	for current := path; ; current = filepath.Dir(current) {
		info, err := os.Stat(current)
		if err == nil {
			if !info.IsDir() {
				return fmt.Errorf("%s is not a directory", current)
			}
			break
		}
		if !os.IsNotExist(err) {
			return err
		}
		missing = append(missing, current)
		if filepath.Dir(current) == current {
			return err
		}
	}
	for i := len(missing) - 1; i >= 0; i-- {
		if err := os.Mkdir(missing[i], 0o755); err != nil && !os.IsExist(err) {
			return err
		}
		if err := SyncDir(missing[i]); err != nil {
			return err
		}
		if err := SyncDir(filepath.Dir(missing[i])); err != nil {
			return err
		}
	}
	return nil
}

// CanonicalPath resolves symlinks in existing ancestors even when the final
// file or directories have not been created yet.
func CanonicalPath(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	var suffix []string
	for current := abs; ; current = filepath.Dir(current) {
		resolved, err := filepath.EvalSymlinks(current)
		if err == nil {
			for i := len(suffix) - 1; i >= 0; i-- {
				resolved = filepath.Join(resolved, suffix[i])
			}
			return resolved, nil
		}
		if !os.IsNotExist(err) {
			return "", err
		}
		suffix = append(suffix, filepath.Base(current))
		if filepath.Dir(current) == current {
			return "", err
		}
	}
}

func SyncDir(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
}
