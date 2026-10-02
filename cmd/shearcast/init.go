package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/cwebley/shearcast"
	"github.com/cwebley/shearcast/internal/config"
	"github.com/cwebley/shearcast/internal/fileutil"
)

// runInit writes the starter config.toml and .env. Existing files are never
// touched; it fails only when there was nothing left to write.
func runInit(_ context.Context, args []string) error {
	fs := flagSet("init", "[flags]")
	cfgPath := fs.String("config", config.DefaultConfigPath(), "config file to create")
	if _, err := parseArgs(fs, args); err == flag.ErrHelp {
		return nil
	} else if err != nil {
		return err
	}
	if err := fileutil.MkdirAll(filepath.Dir(*cfgPath)); err != nil {
		return err
	}
	envPath := filepath.Join(filepath.Dir(*cfgPath), ".env")
	wrote := 0
	for _, f := range []struct {
		path string
		data []byte
	}{{*cfgPath, shearcast.ConfigExample}, {envPath, shearcast.EnvExample}} {
		created, err := createOnce(f.path, f.data)
		if err != nil {
			return err
		}
		if created {
			fmt.Println("wrote", f.path)
			wrote++
		} else {
			fmt.Println("kept existing", f.path)
		}
	}
	if wrote == 0 {
		return fmt.Errorf("%s already exists; edit it instead", *cfgPath)
	}
	fmt.Printf(`
next:
  1. put your OpenRouter key (and R2 settings, if publishing to R2) in %s
  2. replace the example channels in %s, or use: shearcast channel add
  3. check the setup: shearcast doctor
`, envPath, *cfgPath)
	return nil
}

// createOnce writes data to a new private file, reporting false if the path
// already exists.
func createOnce(path string, data []byte) (bool, error) {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(err, os.ErrExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return false, err
	}
	return true, f.Close()
}
