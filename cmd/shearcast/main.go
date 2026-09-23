// Command shearcast turns YouTube channels into private podcast feeds with
// configured segments cut out: detect regions with a System One model against
// your own rules (ad reads by default), cut them, publish the result to
// object storage as a per-channel RSS feed.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}

	var err error
	switch os.Args[1] {
	case "render":
		err = runRender(ctx, os.Args[2:])
	case "publish":
		err = runPublish(ctx, os.Args[2:])
	case "sync":
		err = runSync(ctx, os.Args[2:])
	case "transcript":
		err = runTranscript(ctx, os.Args[2:])
	case "help", "-h", "--help":
		usage()
		return
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n", os.Args[1])
		usage()
		os.Exit(2)
	}

	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `shearcast - de-sponsored audio, published as private podcast feeds

usage:
  shearcast render <video-url-or-id> -channel SLUG [flags]   detect, snap to silence, and cut an episode
  shearcast publish <video-url-or-id> -channel SLUG [flags]  upload a rendered episode and update its feed
  shearcast sync [-channel SLUG] [flags]                     check configured channels for new episodes, render+publish each
  shearcast transcript <video-url-or-id> [flags]             print cached captions, optionally windowed

run "shearcast render -h" for flags
`)
}

// flagSet builds a flag set that prints its own usage line.
func flagSet(name, args string) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ExitOnError)
	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, "usage: shearcast %s %s\n\nflags:\n", name, args)
		fs.PrintDefaults()
	}
	return fs
}

// parseArgs parses flags that appear before or after positional arguments.
// Go's flag package stops at the first non-flag token, which makes
// "render <id> -no-snap" fail in a way nobody expects.
func parseArgs(fs *flag.FlagSet, args []string) ([]string, error) {
	var positional []string
	for len(args) > 0 {
		if err := fs.Parse(args); err != nil {
			return nil, err
		}
		rest := fs.Args()
		if len(rest) == 0 {
			break
		}
		positional = append(positional, rest[0])
		args = rest[1:]
	}
	return positional, nil
}

func hms(seconds float64) string {
	if seconds <= 0 {
		return "0:00"
	}
	total := int(seconds + 0.5)
	h, m, s := total/3600, (total%3600)/60, total%60
	if h > 0 {
		return fmt.Sprintf("%d:%02d:%02d", h, m, s)
	}
	return fmt.Sprintf("%d:%02d", m, s)
}
