package storage

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// ProbeWrite uses a unique diagnostic object, never a feed or episode key.
// Cleanup gets its own bounded context, including after upload cancellation:
// the server may have accepted a write whose response never reached us.
func (s *Store) ProbeWrite(ctx context.Context) (err error) {
	key := ".shearcast-doctor/" + rand.Text()
	defer func() {
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancel()
		if deleteErr := s.Delete(cleanup, key); deleteErr != nil {
			err = errors.Join(err, fmt.Errorf("probe cleanup failed; remove object %s: %w", key, deleteErr))
		}
	}()
	const content = "shearcast publishing write probe\n"
	if _, err := s.Put(ctx, key, strings.NewReader(content), int64(len(content)), "text/plain"); err != nil {
		return err
	}
	data, err := s.Get(ctx, key)
	if err != nil {
		return err
	}
	if string(data) != content {
		return fmt.Errorf("publishing probe read-back mismatch")
	}
	return nil
}

// ProbeWrite requires an existing publication root. It creates one temporary
// file hidden from the HTTP handler and removes it before returning.
func (s *Filesystem) ProbeWrite(ctx context.Context) (err error) {
	if err := ctx.Err(); err != nil {
		return err
	}
	root, err := os.OpenRoot(s.directory)
	if err != nil {
		return fmt.Errorf("open publication directory for probe; create it first if absent: %w", err)
	}
	defer root.Close()
	name := ".doctor-" + rand.Text()
	f, err := root.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	defer func() {
		if removeErr := root.Remove(name); removeErr != nil {
			err = errors.Join(err, fmt.Errorf("probe cleanup failed; remove %s: %w", filepath.Join(s.directory, name), removeErr))
		}
	}()
	defer f.Close()
	content := []byte("shearcast publishing write probe\n")
	if _, err := f.Write(content); err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		return err
	}
	data, err := root.ReadFile(name)
	if err != nil {
		return err
	}
	if !bytes.Equal(content, data) {
		return fmt.Errorf("publishing probe read-back mismatch")
	}
	return ctx.Err()
}
