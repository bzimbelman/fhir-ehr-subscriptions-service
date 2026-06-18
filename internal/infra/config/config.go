// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

// Package config is the layered configuration loader. It owns the boot path
// (CLI > env > file > defaults), structural validation, secret-placeholder
// resolution, per-domain JSON Schema validation, the SIGHUP hot-reload path,
// and the redaction map. See docs/low-level-design/configuration.md for the
// full design.
//
// Public surface:
//
//	type Module struct{...}
//	func Start(ctx, args, cctx) (*Module, Handle, error)
//	func (m *Module) Reload(ctx, trigger) ReloadReport
//	func (m *Module) Shutdown(ctx) error
//	type Handle interface { Read() Effective; Subscribe(domain, cb) }
//
// CliArgs / Context / Effective / ReloadReport / ReloadTrigger live in this
// package as the externally-stable types every consumer reads through.
package config

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"sync"
	"time"

	configtypes "github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/infra/config/config_types"
	effectivestore "github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/infra/config/effective_store"
	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/infra/config/loader"
	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/infra/config/merger"
	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/infra/config/redaction"
	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/infra/config/reload"
	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/infra/config/schemas"
	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/infra/config/secrets"
	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/infra/config/validator"
)

// CliArgs is the parsed command-line surface. See loader.CLIArgs for source
// of truth on field semantics.
type CliArgs = loader.CLIArgs

// Context is the host-provided dependency bundle the configuration module
// consumes at startup. Per LLD §3 the module does not need the DB; it loads
// entirely from CLI / env / file / defaults.
type Context struct {
	Clock  func() time.Time
	Logger *slog.Logger
}

// Effective is the immutable, post-resolution snapshot every consumer reads
// through. It exposes both typed views (one per domain) and the generic
// post-resolution Tree the redaction walker consumes for serialization.
type Effective struct {
	Deployment    configtypes.DeploymentConfig
	Server        configtypes.ServerConfig
	Lifecycle     configtypes.LifecycleConfig
	Storage       configtypes.StorageConfig
	Auth          configtypes.AuthConfig
	Topics        configtypes.TopicsConfig
	MLLPListener  configtypes.MLLPListenerConfig
	Adapter       configtypes.AdapterConfig
	Channels      configtypes.ChannelsConfig
	Delivery      configtypes.DeliveryConfig
	Observability configtypes.ObservabilityConfig

	// Tree is the post-resolution generic config tree, kept so the redaction
	// walker can serialize it for $status, error reports, and audit log.
	Tree map[string]interface{}
	// Redaction is the path-keyed sensitivity map. Travels with the snapshot.
	Redaction *redaction.Map
}

// Handle is the consumer-facing read interface every other module wires to.
type Handle interface {
	Read() Effective
	Subscribe(domain string, cb func(Effective)) effectivestore.SubscriptionID
}

// ReloadTrigger identifies what initiated a reload. SIGHUP is the
// operator-driven path (ADR 0008 #9). file_mtime is the polling path
// added in B-35 — when secret-files mtime moves the module reloads
// without an explicit signal so vault-agent / cert-manager rotation
// does not require signaling rights.
type ReloadTrigger int

// ReloadTrigger constants.
const (
	TriggerSIGHUP ReloadTrigger = iota
	TriggerFileMtime
)

// String returns the lower-snake-case label used for metrics.
func (t ReloadTrigger) String() string {
	switch t {
	case TriggerSIGHUP:
		return "sighup"
	case TriggerFileMtime:
		return "file_mtime"
	default:
		return "unknown"
	}
}

// ReloadReport is the structured outcome of a reload.
type ReloadReport struct {
	Outcome       string
	AppliedPaths  []string
	RejectedPaths []string
	Error         error
}

// Module owns the boot path, the live snapshot, and the reload coordinator.
// Constructed once at startup via Start.
type Module struct {
	mu        sync.RWMutex
	cliArgs   CliArgs
	cctx      Context
	registry  *schemas.Registry
	store     *effectivestore.Store
	priorTree map[string]interface{} // post-resolution tree from the last successful boot/reload
	logger    *slog.Logger
	closed    bool

	// Reload trigger observers (B-35). Hooks fire after every reload —
	// the host wires this to the fhir_subs_config_reload_total{trigger}
	// metric counter.
	reloadHooksMu sync.Mutex
	reloadHooks   []func(trigger string)

	// secretFilesMu guards secretFiles. Updated on every successful
	// reload so the mtime watcher tracks the current set of resolved
	// ${file:...} placeholders, even when the config gains/drops them.
	secretFilesMu sync.Mutex
	secretFiles   []string
}

// Start runs the boot path and publishes the first effective snapshot.
//
// Per LLD §5: parse CLI/env/file/defaults; merge by precedence;
// structural-validate; resolve secrets; domain-validate; semantic-validate;
// publish.
func Start(_ context.Context, args CliArgs, cctx Context) (*Module, Handle, error) {
	if cctx.Clock == nil {
		cctx.Clock = time.Now
	}
	if cctx.Logger == nil {
		cctx.Logger = slog.New(slog.NewTextHandler(os.Stderr, nil))
	}

	registry := schemas.NewRegistry()

	// Tag every schema-declared sensitive path up front. This is the safety
	// net for fields the operator embedded directly without using a placeholder.
	rmap := redaction.NewMap()
	for _, p := range registry.SensitivePaths() {
		rmap.TagSensitive(p)
	}

	tree, err := loadAndMerge(args, registry)
	if err != nil {
		return nil, nil, err
	}

	// Structural validation BEFORE secret resolution per LLD §3 (a).
	if r := validator.ValidateStructural(tree, registry); !r.OK() {
		return nil, nil, fmt.Errorf("config validation failed:\n%s", r.Error())
	}

	resolved, rmap, secretFiles, err := secrets.ResolveWithFilePaths(tree, rmap)
	if err != nil {
		return nil, nil, fmt.Errorf("secret resolution failed: %w", err)
	}

	// Domain (manifest) schemas.
	if r := validator.ValidateDomainSchemas(resolved, registry); !r.OK() {
		return nil, nil, fmt.Errorf("domain schema validation failed:\n%s", r.Error())
	}

	// Cross-field semantic checks.
	if r := validator.ValidateSemantic(resolved, registry); !r.OK() {
		return nil, nil, fmt.Errorf("semantic validation failed:\n%s", r.Error())
	}

	if args.CheckOnly {
		return &Module{
			cliArgs:  args,
			cctx:     cctx,
			registry: registry,
			store:    effectivestore.New(),
			logger:   cctx.Logger,
		}, nil, nil
	}

	store := effectivestore.New()
	store.Publish(&effectivestore.Effective{Tree: resolved, Redaction: rmap})

	mod := &Module{
		cliArgs:     args,
		cctx:        cctx,
		registry:    registry,
		store:       store,
		priorTree:   resolved,
		logger:      cctx.Logger,
		secretFiles: secretFiles,
	}
	cctx.Logger.Info("config loaded",
		slog.String("config_path", args.ConfigPath),
		slog.Int("redacted_fields", rmap.Len()))
	return mod, &handle{m: mod}, nil
}

// loadAndMerge runs the per-layer parsing and the precedence merge.
func loadAndMerge(args CliArgs, registry *schemas.Registry) (map[string]interface{}, error) {
	cliLayer, err := loader.ParseCLI(args)
	if err != nil {
		return nil, fmt.Errorf("CLI parse: %w", err)
	}
	envLayer := loader.ReadEnvForKnownKeys(registry.KnownPaths())
	fileLayer, err := loader.ReadFile(args.ConfigPath)
	if err != nil {
		return nil, fmt.Errorf("file parse: %w", err)
	}
	defLayer := defaults()
	merged := merger.Merge(defLayer, fileLayer, envLayer, cliLayer)
	return merged, nil
}

// RegisterDomainSchema lets adapter/channel manifests register their config
// schemas at runtime. Should be called BEFORE Start (so the boot path
// validates against them) when possible. Calling after Start is allowed but
// only takes effect on the next reload.
func (m *Module) RegisterDomainSchema(domain string, schemaJSON []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.registry.Register(domain, schemaJSON)
}

// Reload re-reads the file and applies only the reloadable subset.
// SIGHUP and file_mtime polling are the recognized triggers (B-35).
//
// Every reload — accepted or rejected — fans out to registered OnReload
// hooks with the trigger label. Hosts use this to bump the
// fhir_subs_config_reload_total{trigger} metric.
func (m *Module) Reload(_ context.Context, trigger ReloadTrigger) ReloadReport {
	if trigger != TriggerSIGHUP && trigger != TriggerFileMtime {
		return ReloadReport{
			Outcome: "rejected_validation",
			Error:   fmt.Errorf("reload trigger %d not recognized", trigger),
		}
	}
	report := m.doReload()
	m.notifyReloadHooks(trigger.String())
	return report
}

func (m *Module) doReload() ReloadReport {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return ReloadReport{Outcome: "rejected_validation", Error: errors.New("module shut down")}
	}

	tree, err := loadAndMerge(m.cliArgs, m.registry)
	if err != nil {
		return ReloadReport{Outcome: "rejected_validation", Error: err}
	}
	if r := validator.ValidateStructural(tree, m.registry); !r.OK() {
		return ReloadReport{Outcome: "rejected_validation", Error: errors.New(r.Error())}
	}

	// Build a fresh redaction map from the schema walk (so dropped placeholders
	// don't leak stale tags).
	rmap := redaction.NewMap()
	for _, p := range m.registry.SensitivePaths() {
		rmap.TagSensitive(p)
	}
	resolved, rmap, secretFiles, err := secrets.ResolveWithFilePaths(tree, rmap)
	if err != nil {
		return ReloadReport{Outcome: "rejected_validation", Error: err}
	}
	if r := validator.ValidateDomainSchemas(resolved, m.registry); !r.OK() {
		return ReloadReport{Outcome: "rejected_validation", Error: errors.New(r.Error())}
	}
	if r := validator.ValidateSemantic(resolved, m.registry); !r.OK() {
		return ReloadReport{Outcome: "rejected_validation", Error: errors.New(r.Error())}
	}

	plan := reload.Plan(m.priorTree, resolved, m.registry)
	// Refresh tracked secret-file paths so the mtime watcher follows
	// any add/remove on the next tick.
	m.setSecretFiles(secretFiles)
	switch plan.Outcome {
	case reload.OutcomeRejectedImmutable:
		m.logger.Warn("config reload rejected: immutable fields changed",
			slog.Any("rejected_paths", plan.RejectedPaths))
		return ReloadReport{
			Outcome:       string(plan.Outcome),
			RejectedPaths: plan.RejectedPaths,
		}
	case reload.OutcomeApplied:
		next := reload.ApplyOverrides(m.priorTree, plan.AppliedDiffs)
		m.priorTree = next
		m.store.Publish(&effectivestore.Effective{Tree: next, Redaction: rmap})
		m.logger.Info("config reload applied",
			slog.Any("applied_paths", plan.AppliedPaths))
		return ReloadReport{
			Outcome:      string(plan.Outcome),
			AppliedPaths: plan.AppliedPaths,
		}
	}
	return ReloadReport{Outcome: "rejected_validation", Error: errors.New("unknown plan outcome")}
}

// OnReload registers a hook that fires after every reload attempt
// (applied or rejected). Trigger label values: "sighup" | "file_mtime".
// The host wires this to the fhir_subs_config_reload_total{trigger}
// metric counter (B-35). Safe under concurrent calls.
func (m *Module) OnReload(fn func(trigger string)) {
	if fn == nil {
		return
	}
	m.reloadHooksMu.Lock()
	m.reloadHooks = append(m.reloadHooks, fn)
	m.reloadHooksMu.Unlock()
}

func (m *Module) notifyReloadHooks(trigger string) {
	m.reloadHooksMu.Lock()
	hooks := make([]func(string), len(m.reloadHooks))
	copy(hooks, m.reloadHooks)
	m.reloadHooksMu.Unlock()
	for _, fn := range hooks {
		fn(trigger)
	}
}

func (m *Module) setSecretFiles(paths []string) {
	m.secretFilesMu.Lock()
	defer m.secretFilesMu.Unlock()
	m.secretFiles = paths
}

func (m *Module) snapshotSecretFiles() []string {
	m.secretFilesMu.Lock()
	defer m.secretFilesMu.Unlock()
	out := make([]string, len(m.secretFiles))
	copy(out, m.secretFiles)
	return out
}

// WatchSecretFiles starts a background goroutine that polls the mtime
// of every resolved ${file:...} placeholder and triggers a
// TriggerFileMtime reload when any file changes (B-35). Returns
// immediately; the goroutine exits when ctx fires.
//
// interval <= 0 defaults to 60s. interval == 0 with ctx that never
// fires is fine — the host can also disable polling by simply never
// calling WatchSecretFiles.
//
// Safe to call multiple times — each call starts an independent
// watcher. The host should call once at startup.
func (m *Module) WatchSecretFiles(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = 60 * time.Second
	}
	go m.runSecretFileWatcher(ctx, interval)
}

func (m *Module) runSecretFileWatcher(ctx context.Context, interval time.Duration) {
	mtimes := captureMtimes(m.snapshotSecretFiles())
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			paths := m.snapshotSecretFiles()
			next := captureMtimes(paths)
			if mtimesChanged(mtimes, next) {
				m.Reload(ctx, TriggerFileMtime)
				// Refresh the tracked mtimes from the post-reload set
				// (Reload may have rotated which paths are tracked).
				mtimes = captureMtimes(m.snapshotSecretFiles())
			} else {
				mtimes = next
			}
		}
	}
}

func captureMtimes(paths []string) map[string]time.Time {
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

func mtimesChanged(a, b map[string]time.Time) bool {
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

// Shutdown releases module-held resources. The store remains accessible for
// reads briefly after shutdown — callers should drain before calling.
func (m *Module) Shutdown(_ context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.closed = true
	return nil
}

// handle adapts effectivestore.Store to the public Handle interface, including
// the typed projection of Effective.
type handle struct{ m *Module }

func (h *handle) Read() Effective {
	eff := h.m.store.Read()
	if eff == nil {
		return Effective{Tree: map[string]interface{}{}, Redaction: redaction.NewMap()}
	}
	return buildEffective(eff.Tree, eff.Redaction)
}

func (h *handle) Subscribe(domain string, cb func(Effective)) effectivestore.SubscriptionID {
	return h.m.store.Subscribe(domain, func(e *effectivestore.Effective) {
		if e == nil {
			return
		}
		cb(buildEffective(e.Tree, e.Redaction))
	})
}
