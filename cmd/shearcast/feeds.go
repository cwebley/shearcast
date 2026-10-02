package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	"rsc.io/qr"

	"github.com/cwebley/shearcast/internal/config"
	"github.com/cwebley/shearcast/internal/pipeline"
	"github.com/cwebley/shearcast/internal/state"
	"github.com/cwebley/shearcast/internal/subscribe"
)

// runFeeds prints where to subscribe. Sync keeps the subscription page current;
// -publish rewrites it now, after a channel edit, without waiting for a sync.
func runFeeds(ctx context.Context, args []string) error {
	fs := flagSet("feeds", "[flags]")
	cfgPath := fs.String("config", config.DefaultConfigPath(), "config file")
	statePath := fs.String("state", config.DefaultStatePath(), "episode state and command lock (used with -publish)")
	publish := fs.Bool("publish", false, "rewrite the subscription page and OPML list now")
	noQR := fs.Bool("no-qr", false, "omit the QR code")
	cacheDir := cacheFlag(fs)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("feeds takes no positional arguments")
	}
	cfg, err := loadCommandConfig(*cfgPath, *statePath, *cacheDir)
	if err != nil {
		return err
	}
	destination, err := publishingDestination(cfg)
	if err != nil {
		return err
	}
	pageURL := destination.BaseURL + "/" + subscribe.PageKey
	if *publish {
		st, err := state.Open(*statePath)
		if err != nil {
			return fmt.Errorf("opening state: %w", err)
		}
		defer st.Close()
		store, err := newPublisher(ctx, cfg, st, false)
		if err != nil {
			return err
		}
		if pageURL, err = pipeline.PublishSubscriptions(ctx, store, cfg.Channels); err != nil {
			return fmt.Errorf("publishing subscription page: %w", err)
		}
	}

	fmt.Println("Subscription page (open on your phone, tap to subscribe):")
	fmt.Println("  " + pageURL)
	if !*noQR {
		if err := printQR(os.Stdout, pageURL); err != nil {
			return err
		}
	}
	fmt.Println("Feeds:")
	for _, ch := range cfg.Channels {
		if ch.Disabled {
			continue
		}
		fmt.Printf("  %-12s %s\n", ch.Slug, destination.BaseURL+"/"+ch.Slug+"/feed.xml")
	}
	if !*publish {
		fmt.Println("The page lists channels with a published feed as of the last sync; run with -publish to refresh it now.")
	}
	return nil
}

// printQR draws two modules per character cell with explicit colors, so the
// code scans on light and dark terminal themes alike.
func printQR(w io.Writer, text string) error {
	code, err := qr.Encode(text, qr.M)
	if err != nil {
		return err
	}
	const quiet = 2
	dark := func(x, y int) bool {
		return x >= 0 && y >= 0 && x < code.Size && y < code.Size && code.Black(x, y)
	}
	var b strings.Builder
	for y := -quiet; y < code.Size+quiet; y += 2 {
		b.WriteString("  ")
		for x := -quiet; x < code.Size+quiet; x++ {
			fg, bg := "97", "107" // white
			if dark(x, y) {
				fg = "30"
			}
			if dark(x, y+1) {
				bg = "40"
			}
			fmt.Fprintf(&b, "\x1b[%s;%sm▀", fg, bg)
		}
		b.WriteString("\x1b[0m\n")
	}
	_, err = io.WriteString(w, b.String())
	return err
}
