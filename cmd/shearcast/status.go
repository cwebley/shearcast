package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/cwebley/shearcast/internal/config"
	"github.com/cwebley/shearcast/internal/state"
)

func runStatus(ctx context.Context, args []string) error {
	return statusCommand(args, os.Stdout)
}

func statusCommand(args []string, out io.Writer) error {
	fs := flagSet("status", "[-channel SLUG] [flags]")
	cfgPath := fs.String("config", config.DefaultConfigPath(), "config file")
	statePath := fs.String("state", config.DefaultStatePath(), "episode and sync history")
	slug := fs.String("channel", "", "only show this channel")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("status takes no positional arguments")
	}
	// Status does not construct a publisher or require its credentials. A changed
	// destination remains visible as configuration alongside recorded history.
	cfg, err := config.Load(*cfgPath)
	if err != nil {
		return err
	}
	snapshot, err := state.ReadSnapshot(*statePath)
	if err != nil {
		return fmt.Errorf("reading state: %w", err)
	}
	channels := diagnosticChannels(cfg, snapshot)
	if *slug != "" {
		ch, ok := channels[*slug]
		if !ok {
			return fmt.Errorf("unknown channel %q", *slug)
		}
		channels = map[string]config.Channel{*slug: ch}
	}
	fmt.Fprintln(out, "Last saved state; offline snapshot, not a live health or process check.")
	if !snapshot.Exists {
		fmt.Fprintln(out, "State file absent; no recorded history.")
	}
	if snapshot.LastRun == nil {
		fmt.Fprintln(out, "Latest sync invocation: unknown; no recorded attempt")
	} else {
		fmt.Fprintf(out, "Latest sync invocation, scope [%s]: %s\n", strings.Join(snapshot.LastRun.Channels, ", "), attemptText(snapshot.LastRun.SyncAttempt))
		printUsage(out, "  recorded model usage", snapshot.LastRun.Usage)
	}
	if len(channels) == 0 {
		fmt.Fprintln(out, "No configured channels or recorded channel history.")
	}
	for _, name := range sortedChannelNames(channels) {
		ch := channels[name]
		mode := "active"
		if ch.Disabled {
			mode = "disabled"
		}
		if _, ok := cfg.ChannelBySlug(name); !ok {
			mode = "not configured"
		}
		fmt.Fprintf(out, "\n%s: %s\n", name, mode)
		history, ok := snapshot.Sync[name]
		if !ok {
			fmt.Fprintln(out, "  latest attempt: unknown; no recorded attempt")
		} else {
			fmt.Fprintf(out, "  latest attempt: %s\n", attemptText(history.Latest))
			printUsage(out, "  recorded model usage", history.Latest.Usage)
		}
		fmt.Fprintf(out, "  last successful sync: %s\n", recordedTime(history.LastSuccess))
		printFeedAddresses(out, cfg, snapshot, name)
		if snapshot.Purges[name] {
			fmt.Fprintln(out, "  channel purge pending")
		}
		entries := snapshot.Episodes[name]
		var ids []string
		published, waiting, pending, rendered, deleting, failures := 0, 0, 0, 0, 0, 0
		for id, ep := range entries {
			if ep.HasPublished && ep.Removal == "" {
				published++
			}
			if ep.DeletePending {
				deleting++
			}
			if ep.LastError != "" {
				failures++
			}
			if ep.Removal == "" {
				if ep.Stage == state.Waiting {
					waiting++
				}
				if ep.PublishPending && ep.Stage == state.Rendered {
					pending++
				}
				if ep.Stage == state.Rendered && !ep.PublishPending {
					rendered++
				}
			}
			if ep.LastError != "" || ep.DeletePending || ep.Removal == "" && ep.Stage != state.Published {
				ids = append(ids, id)
			}
		}
		fmt.Fprintf(out, "  recorded library: %d published, %d waiting captions, %d pending publication, %d local renders, %d pending deletions, %d episode errors\n", published, waiting, pending, rendered, deleting, failures)
		sort.Strings(ids)
		for _, id := range ids {
			ep := entries[id]
			label := string(ep.Stage)
			switch {
			case ep.DeletePending:
				label = ep.Removal + "; deletion pending"
			case ep.Removal != "":
				label = ep.Removal
			case ep.Stage == state.Rendered && ep.PublishPending:
				label = "rendered; publication pending"
			case ep.Stage == state.Rendered:
				label = "local render; not queued for publication"
			case ep.Stage == state.Pending:
				label = "processing incomplete"
			case ep.Stage == state.Waiting:
				label = "waiting captions"
			}
			if ep.HasPublished && ep.Removal == "" && ep.Stage != state.Published {
				label += "; previous publication recorded"
			}
			if ep.ReprocessPending {
				label += "; reprocessing pending"
			}
			fmt.Fprintf(out, "  %s: %s", id, label)
			if ep.Video != nil {
				fmt.Fprintf(out, " %q", ep.Video.Title)
			}
			if ep.LastError != "" {
				fmt.Fprintf(out, " error=%q", ep.LastError)
			}
			fmt.Fprintln(out)
		}
	}
	fmt.Fprintln(out, "\nWaiting and unfinished processing retry only when selected. Use sync -dry-run for current selection; doctor -network for live checks.")
	return nil
}

func recordedTime(t time.Time) string {
	if t.IsZero() {
		return "unknown; no recorded successful sync"
	}
	return t.UTC().Format(time.RFC3339)
}

func attemptText(a state.SyncAttempt) string {
	text := a.Result
	if a.Result == "unfinished" {
		text = "completion not recorded"
	}
	text += "; started " + a.StartedAt.UTC().Format(time.RFC3339)
	if !a.FinishedAt.IsZero() {
		text += "; finished " + a.FinishedAt.UTC().Format(time.RFC3339)
	}
	if a.Error != "" {
		text += fmt.Sprintf("; error=%q", a.Error)
	}
	return text
}

func diagnosticChannels(cfg *config.Config, s *state.Snapshot) map[string]config.Channel {
	channels := map[string]config.Channel{}
	for slug := range s.Episodes {
		channels[slug] = config.Channel{Slug: slug}
	}
	for slug := range s.Sync {
		channels[slug] = config.Channel{Slug: slug}
	}
	for slug := range s.Purges {
		channels[slug] = config.Channel{Slug: slug}
	}
	for _, ch := range cfg.Channels {
		channels[ch.Slug] = ch
	}
	return channels
}

func sortedChannelNames(channels map[string]config.Channel) []string {
	names := make([]string, 0, len(channels))
	for name := range channels {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func configuredFeedURL(cfg *config.Config, slug string) string {
	base := cfg.Publishing.BaseURL
	if cfg.Publishing.Backend == "r2" {
		base = os.Getenv("R2_PUBLIC_BASE_URL")
	}
	if base == "" {
		return ""
	}
	return strings.TrimRight(base, "/") + "/" + slug + "/feed.xml"
}

func printFeedAddresses(out io.Writer, cfg *config.Config, s *state.Snapshot, slug string) {
	urls := map[string]bool{}
	if s.Publishing != nil {
		urls[s.Publishing.BaseURL+"/"+slug+"/feed.xml"] = true
	}
	for _, ep := range s.Episodes[slug] {
		if ep.FeedURL != "" {
			urls[ep.FeedURL] = true
		}
	}
	var recorded []string
	for url := range urls {
		recorded = append(recorded, url)
	}
	sort.Strings(recorded)
	for _, url := range recorded {
		fmt.Fprintf(out, "  recorded feed address: %s [availability unchecked]\n", url)
	}
	configured := configuredFeedURL(cfg, slug)
	if configured != "" && !urls[configured] {
		fmt.Fprintf(out, "  configured feed address: %s [publication and availability unchecked]\n", configured)
	}
	if configured == "" && len(recorded) == 0 {
		fmt.Fprintln(out, "  feed address: unknown")
	}
}
