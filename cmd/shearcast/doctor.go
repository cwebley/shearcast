package main

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/cwebley/shearcast/internal/config"
	"github.com/cwebley/shearcast/internal/detect"
	"github.com/cwebley/shearcast/internal/render"
	"github.com/cwebley/shearcast/internal/state"
	"github.com/cwebley/shearcast/internal/storage"
	"github.com/cwebley/shearcast/internal/youtube"
)

type doctorReport struct {
	out      io.Writer
	failures int
}

func (r *doctorReport) line(result, name, detail string) {
	if result == "FAIL" {
		r.failures++
	}
	fmt.Fprintf(r.out, "  %s %s: %s\n", result, name, strings.ReplaceAll(detail, "\n", " "))
}

func (r *doctorReport) check(name, detail string, err error) bool {
	if err != nil {
		r.line("FAIL", name, err.Error())
		return false
	}
	r.line("PASS", name, detail)
	return true
}

func runDoctor(ctx context.Context, args []string) error {
	return doctorCommand(ctx, args, os.Stdout)
}

func doctorCommand(ctx context.Context, args []string, out io.Writer) error {
	fs := flagSet("doctor", "[-operation all|sync|publish|serve] [-network] [-write-probe] [flags]")
	cfgPath := fs.String("config", config.DefaultConfigPath(), "config file")
	statePath := fs.String("state", config.DefaultStatePath(), "shared episode state")
	cacheDir := cacheFlag(fs)
	slug := fs.String("channel", "", "only check this configured channel")
	operation := fs.String("operation", "all", "all, sync (new processing), publish (completed retries), or serve")
	network := fs.Bool("network", false, "check source access, OpenRouter authentication and published URLs as applicable")
	writeProbe := fs.Bool("write-probe", false, "write, read and delete a unique publishing probe; R2 also requires -network")
	timeout := fs.Duration("timeout", 15*time.Second, "timeout for each network check")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("doctor takes no positional arguments")
	}
	if *operation != "all" && *operation != "sync" && *operation != "publish" && *operation != "serve" {
		return fmt.Errorf("operation must be all, sync, publish or serve")
	}
	if *timeout <= 0 {
		return fmt.Errorf("timeout must be positive")
	}
	if *writeProbe && *operation == "serve" {
		return fmt.Errorf("serving is read-only; use -operation publish for a write probe")
	}
	r := &doctorReport{out: out}
	fmt.Fprintln(out, "Doctor checks apply to this invocation's user, PATH, environment and paths.")
	fmt.Fprintf(out, "  config: %s\n  state:  %s\n  cache:  %s\n", *cfgPath, *statePath, *cacheDir)
	cfg, err := loadCommandConfig(*cfgPath, *statePath, *cacheDir)
	if !r.check("configuration", "valid; private/public paths checked", err) {
		return fmt.Errorf("doctor found invalid configuration")
	}
	channels := cfg.Channels
	if *slug != "" {
		ch, err := requireChannel(cfg, *slug)
		if err != nil {
			return err
		}
		channels = []config.Channel{ch}
	}
	if *writeProbe && cfg.Publishing.Backend == "r2" && !*network {
		return fmt.Errorf("R2 write probes require both -network and -write-probe")
	}
	ops := []string{*operation}
	if *operation == "all" {
		ops = []string{"sync", "publish"}
		if cfg.Publishing.Backend == "filesystem" {
			ops = append(ops, "serve")
		}
	}
	client := &http.Client{Timeout: *timeout, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	for _, op := range ops {
		if err := ctx.Err(); err != nil {
			return err
		}
		fmt.Fprintf(out, "\n%s:\n", op)
		before := r.failures
		snapshot := &state.Snapshot{}
		if op != "serve" {
			read, err := state.ReadSnapshot(*statePath)
			if r.check("state", "readable snapshot; writer lock not acquired", err) {
				snapshot = read
				if !read.Exists {
					r.line("WARN", "history", "state absent; no recorded history")
				}
			}
			checkDirectory(r, "state directory", filepath.Dir(*statePath), true)
			checkDirectory(r, "cache directory", *cacheDir, true)
		} else if cfg.Publishing.Backend != "filesystem" {
			r.line("FAIL", "backend", "built-in serving requires filesystem publishing")
			continue
		}
		if op == "sync" {
			checkProcessing(ctx, r, cfg, channels, *network, client, *timeout)
		}
		if op == "publish" {
			checkExecutable(r, render.ProbeBinary)
			checkRetryFiles(r, snapshot, channels)
		}
		if op == "serve" {
			_, port, err := net.SplitHostPort(cfg.Serve.Listen)
			if err == nil {
				p, parseErr := strconv.Atoi(port)
				if parseErr != nil || p < 1 || p > 65535 {
					err = fmt.Errorf("serve.listen must use a numeric port from 1 through 65535")
				}
			}
			r.check("listen address", cfg.Serve.Listen+"; binding and port availability not tested", err)
		}
		// A failed state read cannot safely authorize a write probe.
		allowWrite := *writeProbe && op != "serve" && (*operation != "all" || op == "publish") && r.failures == before
		checkPublishing(ctx, r, cfg, channels, snapshot, op, *network, allowWrite, client, *timeout)
		if r.failures == before {
			fmt.Fprintln(out, "  Required checks passed at the requested depth; WARN checks remain unverified.")
		}
	}
	if r.failures > 0 {
		return fmt.Errorf("doctor found %d failed check(s)", r.failures)
	}
	return nil
}

func checkExecutable(r *doctorReport, binary string) {
	path, err := exec.LookPath(binary)
	r.check(binary, path+"; executable found", err)
}

// Read-only directory checks do not pretend that mode bits prove write access
// under ACLs, read-only mounts or quotas. Missing roots are normal before use.
func checkDirectory(r *doctorReport, label, path string, needsWrite bool) {
	original := path
	for {
		info, err := os.Stat(path)
		if os.IsNotExist(err) {
			parent := filepath.Dir(path)
			if parent != path {
				path = parent
				continue
			}
		}
		if err == nil && !info.IsDir() {
			err = fmt.Errorf("%s is not a directory", path)
		}
		if err == nil {
			var f *os.File
			f, err = os.Open(path)
			if err == nil {
				_, err = f.Readdirnames(1)
				f.Close()
				if err == io.EOF {
					err = nil
				}
			}
		}
		if err != nil {
			r.check(label, "", err)
			return
		}
		detail := original + "; readable"
		if path != original {
			detail = original + " absent; nearest existing directory " + path + " is readable"
		}
		if needsWrite {
			r.line("WARN", label, detail+"; write access and free workspace unverified")
		} else if path != original {
			r.line("WARN", label, detail+"; serving an empty library is supported")
		} else {
			r.line("PASS", label, detail)
		}
		return
	}
}

func checkProcessing(ctx context.Context, r *doctorReport, cfg *config.Config, channels []config.Channel, network bool, client *http.Client, timeout time.Duration) {
	if len(channels) == 0 {
		r.line("FAIL", "channels", "no channels configured")
		return
	}
	var active []config.Channel
	for _, ch := range channels {
		if !ch.Disabled {
			active = append(active, ch)
		}
	}
	if len(active) == 0 {
		r.line("SKIP", "new processing", "all selected channels are disabled")
		return
	}
	checkExecutable(r, youtube.Binary)
	checkExecutable(r, render.Binary)
	checkExecutable(r, render.ProbeBinary)
	key, err := cfg.Jev.APIKey()
	if r.check("OpenRouter key", "present; value hidden", err) {
		if network {
			r.check("OpenRouter authentication", "key accepted; model availability and inference unverified", checkOpenRouterKey(ctx, client, key))
		} else {
			r.line("WARN", "OpenRouter", "authentication and model availability unverified; use -network for authentication")
		}
	}
	for _, ch := range active {
		if !ch.NoWeights && cfg.Jev.Weights != "" {
			weights, err := detect.LoadWeights(cfg.Jev.Weights)
			if r.check(ch.Slug+" weights", cfg.Jev.Weights, err) && weights == nil {
				r.line("WARN", ch.Slug+" weights", "absent; processing uses the predicate fallback")
			}
		}
		u, err := url.Parse(ch.URL)
		if err == nil && (u.Hostname() == "" || u.Scheme != "https" && u.Scheme != "http" || u.User != nil) {
			err = fmt.Errorf("source must be an HTTP(S) URL without credentials")
		}
		if !r.check(ch.Slug+" source", "URL configured", err) {
			continue
		}
		if network {
			probeCtx, cancel := context.WithTimeout(ctx, timeout)
			_, err := youtube.InspectUploads(probeCtx, ch.URL, 1)
			cancel()
			r.check(ch.Slug+" source listing", "read at most one public upload; yt-dlp config and disk cache disabled", err)
		} else {
			r.line("WARN", ch.Slug+" source access", "unverified; use -network")
		}
	}
}

func checkRetryFiles(r *doctorReport, snapshot *state.Snapshot, channels []config.Channel) {
	count := 0
	for _, ch := range channels {
		for id, ep := range snapshot.Episodes[ch.Slug] {
			if ep.Removal != "" || !ep.PublishPending || ep.Stage != state.Rendered {
				continue
			}
			count++
			err := readableFile(ep.RenderPath)
			if err == nil && (ep.RecordPath != "" || ep.AudioSHA256 == "") {
				path := ep.RecordPath
				if path == "" {
					path = ep.RenderPath + ".json"
				}
				err = readableFile(path)
			}
			r.check(ch.Slug+"/"+id+" retry audio", "recorded files readable; full artifact verification occurs on publication", err)
		}
	}
	if count == 0 {
		r.line("SKIP", "retry artifacts", "no completed publication retries recorded in selected channels")
	}
}

func readableFile(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	info, err := f.Stat()
	if err == nil && !info.Mode().IsRegular() {
		err = fmt.Errorf("%s is not a regular file", path)
	}
	return err
}

func checkPublishing(ctx context.Context, r *doctorReport, cfg *config.Config, channels []config.Channel, snapshot *state.Snapshot, operation string, network, write bool, client *http.Client, timeout time.Duration) {
	before := r.failures
	destination, err := publishingDestination(cfg)
	if !r.check("publishing settings", "destination configured; credential values hidden", err) {
		return
	}
	var local *storage.Filesystem
	var remote *storage.Store
	if cfg.Publishing.Backend == "filesystem" {
		var err error
		local, err = storage.NewFilesystem(cfg.Publishing.Directory, cfg.Publishing.BaseURL)
		if !r.check("filesystem publishing", "configured", err) {
			return
		}
		checkDirectory(r, "publication directory", destination.Location, operation != "serve")
	} else {
		settings, err := storageConfigFromEnv()
		if !r.check("R2 settings", "required settings present; credential values hidden", err) {
			return
		}
		u, err := url.Parse(settings.PublicBaseURL)
		if err == nil && (u.Hostname() == "" || u.Scheme != "http" && u.Scheme != "https" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "") {
			err = fmt.Errorf("R2_PUBLIC_BASE_URL must be an HTTP(S) URL without credentials, query or fragment")
		}
		if !r.check("R2 public URL", "valid", err) {
			return
		}
		if network {
			settings.HTTPClient = client
			remote, err = storage.New(ctx, settings)
			if !r.check("R2 client", "configured", err) {
				return
			}
		} else {
			r.line("WARN", "R2 access", "credentials present; authentication and read/write access unverified")
		}
	}
	if operation != "serve" {
		if !r.check("publishing destination", "matches recorded history, or library is unbound", snapshot.CheckPublishing(destination)) {
			return
		}
	}
	for _, ch := range channels {
		key := ch.Slug + "/feed.xml"
		address := destination.BaseURL + "/" + key
		expected := feedExpected(snapshot, ch.Slug)
		if local != nil {
			data, err := local.Get(ctx, key)
			expected = checkStoredFeed(r, ch.Slug, snapshot, data, err) || expected
		} else if network {
			probeCtx, cancel := context.WithTimeout(ctx, timeout)
			data, err := remote.Get(probeCtx, key)
			cancel()
			expected = checkStoredFeed(r, ch.Slug, snapshot, data, err) || expected
		}
		if network {
			checkPublicFeed(ctx, r, client, ch.Slug, address, expected)
		} else {
			r.line("WARN", ch.Slug+" feed URL", address+"; reachability unchecked")
		}
	}
	if write && r.failures != before {
		r.line("SKIP", "publishing write probe", "publishing prerequisites failed")
	} else if write {
		probeCtx, cancel := context.WithTimeout(ctx, timeout)
		var err error
		if local != nil {
			err = local.ProbeWrite(probeCtx)
		} else {
			err = remote.ProbeWrite(probeCtx)
		}
		cancel()
		r.check("publishing write probe", "unique temporary object written, read back and deleted", err)
	} else if operation != "serve" {
		r.line("WARN", "publishing writes", "untested; use -write-probe, and -network for R2")
	}
}
