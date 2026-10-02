package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/cwebley/shearcast/internal/config"
	"github.com/cwebley/shearcast/internal/jev"
	"github.com/cwebley/shearcast/internal/pipeline"
	"github.com/cwebley/shearcast/internal/state"
	"github.com/cwebley/shearcast/internal/youtube"
)

func runChannel(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: shearcast channel add|list|update|remove [slug] [flags]")
	}
	action := args[0]
	if action != "add" && action != "list" && action != "update" && action != "remove" {
		return fmt.Errorf("unknown channel action %q", action)
	}
	fs := flagSet("channel "+action, "[slug] [flags]")
	cfgPath := fs.String("config", config.DefaultConfigPath(), "config file to edit")
	statePath := fs.String("state", config.DefaultStatePath(), "shared episode state and lock")
	cacheDir := cacheFlag(fs)
	purge := fs.Bool("purge", false, "also delete the feed and managed audio when removing a channel")
	name := fs.String("name", "", "channel name")
	url := fs.String("url", "", "YouTube channel URL")
	title := fs.String("title", "", "feed title")
	description := fs.String("description", "", "feed description")
	category := fs.String("category", "", "feed category")
	latest := fs.Int("latest", 5, "newest uploads to inspect")
	keep := fs.Int("keep", 10, "published episodes to retain, at least latest")
	bitrate := fs.Int("bitrate-kbps", 0, "AAC bitrate, zero inherits the global setting")
	rules := fs.String("rules", "", "comma-separated rule IDs; empty selects all")
	noWeights := fs.Bool("no-weights", false, "skip the fitted start-edge model")
	keepTail := fs.Bool("keep-tail", false, "keep audio after the last spoken caption instead of trimming it")
	disabled := fs.Bool("disabled", false, "stop syncing this channel; false resumes it")
	pos, err := parseArgs(fs, args[1:])
	if err == flag.ErrHelp {
		return nil
	}
	if err != nil {
		return err
	}
	if *purge && action != "remove" {
		return fmt.Errorf("-purge requires channel remove")
	}
	if action == "list" && len(pos) != 0 || action != "list" && len(pos) != 1 {
		return fmt.Errorf("channel %s requires %s", action, map[bool]string{true: "no slug", false: "one slug"}[action == "list"])
	}
	cfg, err := loadCommandConfig(*cfgPath, *statePath, *cacheDir)
	// The first channel add is how a config comes into being.
	if action == "add" && errors.Is(err, config.ErrNoConfig) {
		cfg, err = config.Default(), nil
	}
	if err != nil {
		return err
	}
	st, err := state.Open(*statePath)
	if err != nil {
		return err
	}
	defer st.Close()
	if action == "list" {
		for _, ch := range cfg.Channels {
			status := "active"
			if ch.Disabled {
				status = "disabled"
			}
			if st.PurgePending(ch.Slug) {
				status = "purge_pending"
			}
			fmt.Fprintf(os.Stdout, "%s\t%s\tlatest=%d\tkeep=%d\t%d kbps\t%s\n", ch.Slug, status, ch.SelectionLimit(), ch.RetentionLimit(), cfg.OutputBitrate(ch), ch.URL)
		}
		return nil
	}
	slug := pos[0]
	if action == "remove" {
		ch, err := requireChannel(cfg, slug)
		if err != nil {
			return err
		}
		var publisher pipeline.Publisher
		if *purge {
			publisher, err = newPublisher(ctx, cfg, st, false)
			if err != nil {
				return err
			}
		}
		if err := stopChannel(*cfgPath, slug, st, *purge); err != nil {
			return err
		}
		if *purge {
			r := pipeline.Runner{Config: cfg, State: st, Publisher: publisher, Cache: youtube.Cache{Dir: *cacheDir}}
			if err := r.PurgeChannel(ctx, ch); err != nil {
				return err
			}
		}
		fmt.Fprintf(os.Stdout, "%s: synchronization stopped", slug)
		if *purge {
			fmt.Fprint(os.Stdout, "; feed and managed audio purged")
		}
		fmt.Fprintln(os.Stdout)
		return nil
	}
	fields := map[string]any{}
	fs.Visit(func(f *flag.Flag) {
		switch f.Name {
		case "name":
			fields["name"] = *name
		case "url":
			fields["url"] = *url
		case "title":
			fields["title"] = *title
		case "description":
			fields["description"] = *description
		case "category":
			fields["category"] = *category
		case "latest":
			fields["latest"] = *latest
		case "keep":
			fields["keep"] = *keep
		case "bitrate-kbps":
			fields["bitrate_kbps"] = *bitrate
		case "no-weights":
			fields["no_weights"] = *noWeights
		case "keep-tail":
			fields["keep_tail"] = *keepTail
		case "disabled":
			fields["disabled"] = *disabled
		case "rules":
			ids := []string{}
			if *rules != "" {
				for _, id := range strings.Split(*rules, ",") {
					ids = append(ids, strings.TrimSpace(id))
				}
			}
			fields["rules"] = ids
		}
	})
	if action == "add" {
		if *url == "" {
			return fmt.Errorf("channel add requires -url")
		}
		if *name == "" {
			fields["name"] = slug
		}
		fields["latest"], fields["keep"] = *latest, *keep
	}
	if st.PurgePending(slug) {
		return fmt.Errorf("channel purge is pending; finish channel remove -purge before updating")
	}
	if err := config.EditChannel(*cfgPath, slug, action == "add", fields); err != nil {
		return err
	}
	fmt.Fprintf(os.Stdout, "%s: configuration saved\n", slug)
	return nil
}

// The state intent must precede the separate config write. Sync uses the same
// operation to finish a stop interrupted between those two durable files.
func stopChannel(configPath, slug string, st *state.Store, purge bool) error {
	if purge {
		if err := st.SetPurgePending(slug, true); err != nil {
			return err
		}
	}
	return config.EditChannel(configPath, slug, false, map[string]any{"disabled": true})
}

func runEpisode(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: shearcast episode list|remove|restore|reprocess [video-id] -channel SLUG [flags]")
	}
	action := args[0]
	if action != "list" && action != "remove" && action != "restore" && action != "reprocess" {
		return fmt.Errorf("unknown episode action %q", action)
	}
	fs := flagSet("episode "+action, "[video-id] -channel SLUG [flags]")
	cfgPath := fs.String("config", config.DefaultConfigPath(), "config file")
	statePath := fs.String("state", config.DefaultStatePath(), "shared episode state and lock")
	slug := fs.String("channel", "", "channel slug")
	cacheDir := cacheFlag(fs)
	pos, err := parseArgs(fs, args[1:])
	if err == flag.ErrHelp {
		return nil
	}
	if err != nil {
		return err
	}
	if action == "list" && len(pos) != 0 || action != "list" && len(pos) != 1 {
		return fmt.Errorf("episode %s: expected %s", action, map[bool]string{true: "no video id", false: "one video id"}[action == "list"])
	}
	cfg, err := loadCommandConfig(*cfgPath, *statePath, *cacheDir)
	if err != nil {
		return err
	}
	st, err := state.Open(*statePath)
	if err != nil {
		return err
	}
	defer st.Close()
	ch, err := requireChannel(cfg, *slug)
	if err != nil {
		return err
	}
	if action == "list" {
		entries := st.Episodes(ch.Slug)
		ids := make([]string, 0, len(entries))
		for id := range entries {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		for _, id := range ids {
			ep := entries[id]
			status := string(ep.Stage)
			if ep.Removal != "" {
				status = ep.Removal
			}
			if ep.DeletePending {
				status += "/deleting"
			}
			title := ""
			if ep.Video != nil {
				title = ep.Video.Title
			}
			fmt.Fprintf(os.Stdout, "%s\t%s\t%s", id, status, title)
			if ep.LastError != "" {
				fmt.Fprintf(os.Stdout, "\terror=%s", ep.LastError)
			}
			fmt.Fprintln(os.Stdout)
			printEpisodeUsage(os.Stdout, ch, id, ep, youtube.Cache{Dir: *cacheDir})
		}
		return nil
	}
	store, err := newPublisher(ctx, cfg, st, false)
	if err != nil {
		return err
	}
	r := pipeline.Runner{Config: cfg, State: st, Cache: youtube.Cache{Dir: *cacheDir}, Publisher: store,
		NewClient: func() (*jev.Client, error) { return newJevClient(cfg) },
		Progress:  func(message string) { fmt.Fprintln(os.Stderr, message) },
	}
	if st.PurgePending(ch.Slug) {
		return fmt.Errorf("channel purge is pending; finish channel remove -purge first")
	}
	if action == "remove" {
		return r.RemoveEpisode(ctx, ch, pos[0])
	}
	defer func() { printUsage(os.Stderr, "model usage this run", &r.Usage) }()
	ep, err := r.Run(ctx, pipeline.EpisodeRequest{Action: pipeline.Action(action), Channel: ch, Target: pos[0]})
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stdout, "%s: %s\n", youtube.VideoID(pos[0]), ep.Stage)
	return nil
}
