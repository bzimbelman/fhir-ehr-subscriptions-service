// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"log/slog"
	"os"
	"reflect"
	"regexp"
	"sort"
	"sync"
	"time"
)

// reloadTrigger names what initiated a reload.
type reloadTrigger string

const (
	reloadTriggerSIGHUP    reloadTrigger = "sighup"
	reloadTriggerFileMtime reloadTrigger = "file_mtime"
)

// reloadCoordinator owns the SIGHUP- and mtime-driven config reload
// path for the production binary (stories #151, #152). It re-reads the
// operator config file, validates, compares the post-merge view to the
// in-memory snapshot to reject changes to immutable fields (database
// URL, MLLP listeners, TLS), and on success applies the hot-reloadable
// subset to live components.
//
// One coordinator per process. Constructed in run.go after the logger
// and snapshot are in place. Nil-safe: if cfg.Source is nil (e.g.
// tests build a Config in code) the coordinator is not constructed and
// reload is a no-op.
type reloadCoordinator struct {
	mu       sync.Mutex
	source   ConfigSource
	prior    *Config
	logger   *slog.Logger
	logLevel *slog.LevelVar // live level swap target

	// hotApply hooks each component registers to be called when a
	// reload is accepted. They run under the coordinator mutex so
	// concurrent reloads serialize.
	hotApply []func(prior, next *Config)

	// secretFilesMu guards secretFiles.
	secretFilesMu sync.Mutex
	secretFiles   []string
	mtimes        map[string]time.Time
}

// newReloadCoordinator builds a coordinator from the boot-time
// snapshot. cfg.Source must be non-nil; callers gate construction on
// that.
func newReloadCoordinator(cfg *Config, logger *slog.Logger, logLevel *slog.LevelVar) *reloadCoordinator {
	c := &reloadCoordinator{
		source:   *cfg.Source,
		prior:    cloneConfigForReload(cfg),
		logger:   logger,
		logLevel: logLevel,
	}
	c.setSecretFiles(scanSecretFilePaths(cfg.Source.Path))
	c.mtimes = captureFileMtimes(c.snapshotSecretFiles())
	return c
}

// registerHotApply records a callback the coordinator invokes on every
// accepted reload. The callback runs under the coordinator's mutex —
// keep work bounded.
func (c *reloadCoordinator) registerHotApply(fn func(prior, next *Config)) {
	if fn == nil || c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.hotApply = append(c.hotApply, fn)
}

// reload re-runs loadConfig + Validate, rejects changes to immutable
// fields, and applies the hot-reloadable subset. Every outcome is
// logged with `trigger` so an operator can correlate metrics.
func (c *reloadCoordinator) reload(trigger reloadTrigger) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()

	next, err := loadConfig(c.source.Path)
	if err != nil {
		c.logger.Warn("config reload rejected: load failed",
			"trigger", string(trigger),
			"err", err.Error())
		return
	}
	if c.source.LogLevelOverride != "" {
		next.Deployment.LogLevel = c.source.LogLevelOverride
	}
	if applyErr := applySets(next, c.source.Sets); applyErr != nil {
		c.logger.Warn("config reload rejected: --set re-application failed",
			"trigger", string(trigger),
			"err", applyErr.Error())
		return
	}
	if vErr := next.Validate(); vErr != nil {
		c.logger.Warn("config reload rejected: validation failed",
			"trigger", string(trigger),
			"err", vErr.Error())
		return
	}

	if rejected := immutableFieldChanges(c.prior, next); len(rejected) > 0 {
		c.logger.Warn("config reload rejected: immutable fields changed",
			"trigger", string(trigger),
			"rejected_paths", rejected)
		// Refresh tracked secret files anyway so the watcher's
		// baseline doesn't keep firing on the same rotation.
		c.setSecretFiles(scanSecretFilePaths(c.source.Path))
		c.mtimes = captureFileMtimes(c.snapshotSecretFiles())
		return
	}

	// Hot-apply: dispatch to every registered component and to the
	// in-process logger level. The applied list is a static set today
	// (logging level is the only coordinator-owned field); component
	// hooks publish their own paths via logs.
	if c.logLevel != nil {
		c.logLevel.Set(slogLevel(next.Deployment.LogLevel))
	}
	for _, fn := range c.hotApply {
		fn(c.prior, next)
	}

	applied := []string{"deployment.log_level"}
	c.logger.Info("config reload applied",
		"trigger", string(trigger),
		"applied_paths", applied)

	c.prior = cloneConfigForReload(next)
	c.setSecretFiles(scanSecretFilePaths(c.source.Path))
	c.mtimes = captureFileMtimes(c.snapshotSecretFiles())
}

// startSecretFileWatcher polls the resolved ${file:...} paths every
// interval and triggers reload(file_mtime) when any path's mtime
// changes. Returns immediately; the goroutine exits when ctx fires.
//
// interval <= 0 defaults to 60s — production cadence per AC.
func (c *reloadCoordinator) startSecretFileWatcher(ctx context.Context, interval time.Duration) {
	if c == nil {
		return
	}
	if interval <= 0 {
		interval = 60 * time.Second
	}
	go c.runSecretFileWatcher(ctx, interval)
}

func (c *reloadCoordinator) runSecretFileWatcher(ctx context.Context, interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			paths := c.snapshotSecretFiles()
			next := captureFileMtimes(paths)
			c.secretFilesMu.Lock()
			prior := c.mtimes
			changed := mtimeMapsDiffer(prior, next)
			c.mtimes = next
			c.secretFilesMu.Unlock()
			if changed {
				c.reload(reloadTriggerFileMtime)
			}
		}
	}
}

func (c *reloadCoordinator) setSecretFiles(paths []string) {
	c.secretFilesMu.Lock()
	defer c.secretFilesMu.Unlock()
	c.secretFiles = paths
}

func (c *reloadCoordinator) snapshotSecretFiles() []string {
	c.secretFilesMu.Lock()
	defer c.secretFilesMu.Unlock()
	out := make([]string, len(c.secretFiles))
	copy(out, c.secretFiles)
	return out
}

// cloneConfigForReload shallow-copies cfg. We compare immutable scalars
// + a few struct-level fields (Database, MLLP, Server.HTTP.TLS) so a
// shallow copy is sufficient — mutations to the live cfg from
// hot-apply callbacks don't reach the cloned snapshot.
func cloneConfigForReload(cfg *Config) *Config {
	if cfg == nil {
		return nil
	}
	cp := *cfg
	cp.Source = nil
	return &cp
}

// immutableFieldChanges returns the dotted-path list of immutable
// fields that differ between prior and next. Empty result means "safe
// to apply".
//
// The set is intentionally conservative — fields where rotating the
// value at runtime requires re-establishing connections, re-binding
// listeners, or rebuilding TLS material should be rejected. Operators
// rotate those by rolling pods.
func immutableFieldChanges(prior, next *Config) []string {
	if prior == nil || next == nil {
		return nil
	}
	var rejected []string
	if prior.Database.URL != next.Database.URL {
		rejected = append(rejected, "database.url")
	}
	if !reflect.DeepEqual(prior.MLLP.Listeners, next.MLLP.Listeners) {
		rejected = append(rejected, "mllp.listeners")
	}
	if prior.Server.HTTP.Bind != next.Server.HTTP.Bind {
		rejected = append(rejected, "server.http.bind")
	}
	if prior.Server.HTTP.ProbeBind != next.Server.HTTP.ProbeBind {
		rejected = append(rejected, "server.http.probe_bind")
	}
	if prior.Server.HTTP.Insecure != next.Server.HTTP.Insecure {
		rejected = append(rejected, "server.http.insecure")
	}
	if prior.Server.HTTP.TLS != next.Server.HTTP.TLS {
		rejected = append(rejected, "server.http.tls")
	}
	if prior.Lifecycle.ShutdownGracePeriod != next.Lifecycle.ShutdownGracePeriod {
		rejected = append(rejected, "lifecycle.shutdown_grace_period")
	}
	sort.Strings(rejected)
	return rejected
}

// scanSecretFilePaths reads the operator config file at path and
// extracts every ${file:/abs/path} reference. The list is the watch
// set the mtime poller stats. Errors (file unreadable, malformed) are
// swallowed — the caller will see them at the next reload attempt
// when loadConfig fails loudly.
func scanSecretFilePaths(path string) []string {
	if path == "" {
		return nil
	}
	body, err := os.ReadFile(path) //nolint:gosec // operator-supplied config path is intended.
	if err != nil {
		return nil
	}
	matches := filePlaceholderRE.FindAllSubmatch(body, -1)
	if len(matches) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(matches))
	out := make([]string, 0, len(matches))
	for _, m := range matches {
		p := string(m[1])
		if p == "" {
			continue
		}
		if _, ok := seen[p]; ok {
			continue
		}
		seen[p] = struct{}{}
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

// filePlaceholderRE matches `${file:/abs/path}` in raw config bytes.
// Group 1 is the path. Mirrors cmd/fhir-subs/config.go's interpolation
// regex but is local to the reload subsystem so refactors there don't
// silently invalidate watcher coverage.
var filePlaceholderRE = regexp.MustCompile(`\$\{file:([^}]*)\}`)

// captureFileMtimes stats every path. Missing files (e.g. a rotation
// in progress) are recorded as zero-time so a subsequent rotation back
// to a real mtime registers as a change.
func captureFileMtimes(paths []string) map[string]time.Time {
	out := make(map[string]time.Time, len(paths))
	for _, p := range paths {
		fi, err := os.Stat(p)
		if err != nil {
			out[p] = time.Time{}
			continue
		}
		out[p] = fi.ModTime()
	}
	return out
}

// mtimeMapsDiffer reports whether the watcher should fire a reload.
// True when any path's mtime moved or when the path set itself
// changed.
func mtimeMapsDiffer(a, b map[string]time.Time) bool {
	if len(a) != len(b) {
		return true
	}
	for k, va := range a {
		vb, ok := b[k]
		if !ok || !va.Equal(vb) {
			return true
		}
	}
	return false
}
