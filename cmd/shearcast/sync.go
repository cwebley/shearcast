package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/cwebley/shearcast/internal/config"
	"github.com/cwebley/shearcast/internal/jev"
	"github.com/cwebley/shearcast/internal/pipeline"
	"github.com/cwebley/shearcast/internal/state"
	modelusage "github.com/cwebley/shearcast/internal/usage"
	"github.com/cwebley/shearcast/internal/youtube"
)

// runSync checks each configured channel's uploads for videos not already
// published, and renders + publishes each one it finds. It is meant to be
// invoked periodically by the OS scheduler (launchd, systemd), not run as a
// long-lived process: each invocation does one pass and exits.
func runSync(ctx context.Context, args []string) (runErr error) {
	fs := flagSet("sync", "[-channel SLUG] [flags]")
	cfgPath := fs.String("config", config.DefaultConfigPath(), "config file (optional)")
	channelSlug := fs.String("channel", "", "only sync this channel slug (default: every configured channel)")
	statePath := fs.String("state", config.DefaultStatePath(), "path to the processed-episode record")
	dryRun := fs.Bool("dry-run", false, "plan selection, model cost and storage without processing or publishing")
	cacheDir := cacheFlag(fs)
	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return nil
		}
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("sync takes no positional arguments")
	}

	cfg, err := loadCommandConfig(*cfgPath, *statePath, *cacheDir)
	if err != nil {
		return err
	}
	st, err := state.Open(*statePath)
	if err != nil {
		return fmt.Errorf("opening state: %w", err)
	}
	defer st.Close()
	if len(cfg.Channels) == 0 {
		return fmt.Errorf("no channels configured in %s", *cfgPath)
	}
	channels := cfg.Channels
	if *channelSlug != "" {
		ch, err := requireChannel(cfg, *channelSlug)
		if err != nil {
			return err
		}
		channels = []config.Channel{ch}
	}

	cache := youtube.Cache{Dir: *cacheDir}
	runner := pipeline.Runner{Config: cfg, Cache: cache, State: st,
		NewClient: func() (*jev.Client, error) { return newJevClient(cfg) },
	}
	if !*dryRun {
		runner.Progress = func(msg string) {
			fmt.Fprintf(os.Stderr, "%s %s\n", time.Now().Format("15:04:05"), msg)
		}
	}
	if !*dryRun {
		var slugs []string
		for _, channel := range channels {
			slugs = append(slugs, channel.Slug)
		}
		if err := st.StartSync(slugs); err != nil {
			return err
		}
		defer func() {
			runErr = errors.Join(runErr, st.FinishSync(runErr))
			printUsage(os.Stderr, "model usage this run", &runner.Usage)
		}()
	}
	store, err := newPublisher(ctx, cfg, st, *dryRun)
	if err != nil {
		return err
	}
	runner.Publisher = store

	var failures int
	for _, channel := range channels {
		if err := ctx.Err(); err != nil {
			return err
		}
		if *dryRun {
			plan, err := runner.Plan(ctx, channel)
			if err != nil {
				fmt.Fprintf(os.Stderr, "%s: planning: %v\n", channel.Slug, err)
				failures++
				continue
			}
			printSyncPlan(channel, plan)
			continue
		}
		if st.PurgePending(channel.Slug) && !channel.Disabled {
			if err := stopChannel(*cfgPath, channel.Slug, st, true); err != nil {
				if recordErr := st.StartChannelSync(channel.Slug); recordErr != nil {
					return errors.Join(err, recordErr)
				}
				if recordErr := st.FinishChannelSync(channel.Slug, false, err); recordErr != nil {
					return errors.Join(err, recordErr)
				}
				fmt.Fprintf(os.Stderr, "%s: recovering channel stop: %v\n", channel.Slug, err)
				failures++
				continue
			}
			channel.Disabled = true
		}
		outcomes, err := runner.SyncChannel(ctx, channel)
		for _, outcome := range outcomes {
			if outcome.Skipped != "" {
				fmt.Fprintf(os.Stderr, "%s: %s: skipped (%s)\n", channel.Slug, outcome.ID, outcome.Skipped)
				continue
			}
			printUsage(os.Stderr, channel.Slug+": "+outcome.ID+": model usage", &outcome.Usage)
			if outcome.Error != nil {
				continue
			}
			status := string(outcome.Episode.Stage)
			if outcome.Episode.Removal != "" {
				status = outcome.Episode.Removal
			}
			fmt.Fprintf(os.Stderr, "%s: %s: %s\n", channel.Slug, outcome.ID, status)
		}
		if err != nil {
			fmt.Fprintf(os.Stderr, "%s: %v\n", channel.Slug, err)
			if errors.Is(err, modelusage.ErrCheckpoint) {
				return err
			}
			failures++
		}
	}
	if !*dryRun {
		// Always from every configured channel, even on a scoped run: the page
		// is one list, and a -channel sync must not shrink it to one show.
		if pageURL, err := pipeline.PublishSubscriptions(ctx, store, cfg.Channels); err != nil {
			fmt.Fprintf(os.Stderr, "subscription page: %v\n", err)
			failures++
		} else {
			fmt.Fprintf(os.Stderr, "subscription page: %s\n", pageURL)
		}
	}
	if failures > 0 {
		return fmt.Errorf("%d channel(s) failed; see above", failures)
	}
	return nil
}

func printSyncPlan(ch config.Channel, p *pipeline.SyncPlan) {
	fmt.Fprintf(os.Stdout, "%s: latest=%d keep=%d\n", ch.Slug, ch.SelectionLimit(), ch.RetentionLimit())
	if ch.Disabled {
		fmt.Fprintln(os.Stdout, "  synchronization disabled")
	}
	selected := 0
	for _, row := range p.Episodes {
		window := "publication retry"
		if row.Skipped != "" {
			fmt.Fprintf(os.Stdout, "  %s\tskipped\t%s\n", row.ID, row.Skipped)
			continue
		}
		if row.Selected {
			window = "selected"
			selected++
		}
		fmt.Fprintf(os.Stdout, "  %s\t%s\t%s\t%s\n", row.ID, window, row.Status, hms(row.SourceSeconds))
	}
	fmt.Fprintf(os.Stdout, "  selected: %d; source duration: %s; unknown durations: %d; processing duration: %s\n", selected, hms(p.SourceSeconds), p.UnknownDurations, hms(p.ProcessingSeconds))
	fmt.Fprintf(os.Stdout, "  storage MB: current %.2f; uploads ~%.2f; retained ~%.2f per copy\n", float64(p.CurrentBytes)/1e6, float64(p.UploadBytes)/1e6, float64(p.RetainedBytes)/1e6)
	fmt.Fprintln(os.Stdout, "  AAC target + 3% container allowance. Filesystem retains one managed copy; R2 also retains a private local copy. Temporary source/replacement workspace is additional and unknown.")
	if p.CostKnown {
		fmt.Fprintf(os.Stdout, "  estimated model cost: $%.6f to $%.6f; %d matching historical samples\n", p.ModelCostLow, p.ModelCostHigh, p.HistorySamples)
	} else {
		fmt.Fprintln(os.Stdout, "  estimated model cost: insufficient history, duration data or verified model pricing")
	}
	if p.Pricing != nil {
		fmt.Fprintf(os.Stdout, "  %s: $%g/million input tokens; output free. Rate verified %s.\n", p.Pricing.Model, p.Pricing.InputPerMillion, p.Pricing.VerifiedAt)
	}
	fmt.Fprintln(os.Stdout, "  Estimates use successful processing history, not provider billing; retries, credit fees, taxes and hosting/storage/bandwidth are additional.")
	if len(p.PruneIDs) > 0 {
		fmt.Fprintf(os.Stdout, "  outside retention after sync: %v\n", p.PruneIDs)
	}
	if p.RecoveryPending {
		fmt.Fprintln(os.Stdout, "  interrupted changes need recovery; this read-only plan may change after recovery")
	}
	if p.ChronologyPending {
		fmt.Fprintln(os.Stdout, "  same-day retention order is unknown; increase latest to cover the cutoff or increase keep. Retained bytes remain above the requested limit until resolved.")
	}
}
