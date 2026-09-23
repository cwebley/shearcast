package main

import (
	"context"
	"fmt"

	"github.com/cwebley/shearcast/internal/transcript"
	"github.com/cwebley/shearcast/internal/youtube"
)

// runTranscript prints the cached transcript for a video, optionally limited
// to a time window and with an ad region marked, so a cut can be checked by
// eye against the exact words it removes without paying for another run.
func runTranscript(ctx context.Context, args []string) error {
	fs := flagSet("transcript", "<video-url-or-id> [-from S] [-to S] [-ad-start S] [-ad-end S]")
	from := fs.Float64("from", 0, "start of the window, seconds (default: whole video)")
	to := fs.Float64("to", 0, "end of the window, seconds (default: whole video)")
	adStart := fs.Float64("ad-start", -1, "mark the ad region's start, seconds")
	adEnd := fs.Float64("ad-end", -1, "mark the ad region's end, seconds")
	cacheDir := cacheFlag(fs)
	positional, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	if len(positional) != 1 {
		fs.Usage()
		return fmt.Errorf("need exactly one video url or id")
	}

	video, cues, err := loadVideo(ctx, youtube.Cache{Dir: *cacheDir}, positional[0])
	if err != nil {
		return err
	}

	to2 := *to
	if to2 == 0 {
		to2 = video.Duration
	}
	window := transcript.Slice(cues, *from, to2)
	// Same unit the detector itself reasons about, not raw 2-3s caption
	// cues -- 25 matches the production default (config.go's MinSentence).
	sentences := transcript.Sentences(window, "S", 25)

	fmt.Printf("%s  (%s)\n", video.Title, video.ID)
	fmt.Printf("window %s -> %s\n", hms(*from), hms(to2))
	if *adStart >= 0 && *adEnd >= 0 {
		fmt.Printf("ad marked %s -> %s (%.0fs)\n", hms(*adStart), hms(*adEnd), *adEnd-*adStart)
	}
	fmt.Println()

	adOpen := false
	for _, s := range sentences {
		marker := "   "
		if *adStart >= 0 && *adEnd >= 0 {
			if !adOpen && s.Start >= *adStart && s.Start < *adEnd {
				fmt.Printf("--- AD BEGINS (%s) %s\n", hms(*adStart), dashes(50))
				adOpen = true
			}
			if adOpen && s.Start >= *adEnd {
				fmt.Printf("--- AD ENDS (%s) %s\n", hms(*adEnd), dashes(52))
				adOpen = false
			}
			if adOpen {
				marker = "AD "
			}
		}
		fmt.Printf("%s %7s  %s\n", marker, hms(s.Start), s.Text)
	}
	return nil
}

func dashes(n int) string {
	b := make([]byte, n)
	for i := range b {
		b[i] = '-'
	}
	return string(b)
}
