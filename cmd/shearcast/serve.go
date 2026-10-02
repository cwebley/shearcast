package main

import (
	"context"
	"errors"
	"fmt"
	"github.com/cwebley/shearcast/internal/config"
	"net"
	"net/http"
	"os"
	"time"

	"github.com/cwebley/shearcast/internal/storage"
)

func runServe(ctx context.Context, args []string) error {
	fs := flagSet("serve", "[flags]")
	configPath := fs.String("config", config.DefaultConfigPath(), "config file")
	listen := fs.String("listen", "", "listening address (default: serve.listen in config)")
	statePath := fs.String("state", config.DefaultStatePath(), "private state path, checked for separation; never opened")
	cacheDir := cacheFlag(fs)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("serve takes no positional arguments")
	}
	cfg, err := loadCommandConfig(*configPath, *statePath, *cacheDir)
	if err != nil {
		return err
	}
	if cfg.Publishing.Backend != "filesystem" {
		return fmt.Errorf("serve requires publishing.backend = 'filesystem'")
	}
	store, err := storage.NewFilesystem(cfg.Publishing.Directory, cfg.Publishing.BaseURL)
	if err != nil {
		return err
	}
	address := cfg.Serve.Listen
	if *listen != "" {
		address = *listen
	}
	listener, err := net.Listen("tcp", address)
	if err != nil {
		return err
	}
	server := &http.Server{Handler: store.Handler(), ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 60 * time.Second}
	done := make(chan struct{})
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		select {
		case <-ctx.Done():
			shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			if server.Shutdown(shutdown) != nil {
				server.Close()
			}
		case <-done:
		}
	}()
	fmt.Fprintf(os.Stderr, "serving %s on %s\n", cfg.Publishing.Directory, listener.Addr())
	for _, ch := range cfg.Channels {
		fmt.Fprintln(os.Stderr, store.PublicURL(ch.Slug+"/feed.xml"))
	}
	err = server.Serve(listener)
	close(done)
	<-stopped
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}
