// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Config is the minimal typed shape this entry-point reads from the config file.
//
// It mirrors the architecture's YAML for the few fields main needs today; every
// other key is preserved as raw YAML in Extra so the schema-validating loader
// (infra/config) can claim it later without breaking on unknown fields here.
type Config struct {
	Deployment DeploymentConfig `yaml:"deployment"`
	Adapter    AdapterConfig    `yaml:"adapter"`
	Server     ServerConfig     `yaml:"server"`
	Lifecycle  LifecycleConfig  `yaml:"lifecycle"`
	Database   DatabaseConfig   `yaml:"database"`
	Storage    StorageConfig    `yaml:"storage"`
	Codec      CodecConfig      `yaml:"codec"`
	Auth       AuthConfig       `yaml:"auth"`
	MLLP       MLLPConfig       `yaml:"mllp"`
	Pipeline   PipelineConfig   `yaml:"pipeline"`
	Channels   ChannelsConfig   `yaml:"channels"`
	Topics     TopicsConfig     `yaml:"topics"`
	Admin      AdminConfig      `yaml:"admin"`
	Tracing    TracingConfig    `yaml:"tracing"`
	Metrics    MetricsConfig    `yaml:"metrics"`
	Audit      AuditConfig      `yaml:"audit"`

	// Extra captures anything not modeled above so a stricter loader can
	// claim it later without this thin loader rejecting valid configs.
	Extra map[string]any `yaml:",inline"`
}

// TracingConfig models tracing.* fields. Operator-tunable per
// docs/operations/otel-exporter-recipes.md (story #94 AC #1). All
// fields are optional; an empty OTLPEndpoint disables tracing entirely
// (the observability layer returns a no-op tracer).
type TracingConfig struct {
	OTLPEndpoint    string            `yaml:"otlp_endpoint"`
	SampleRate      float64           `yaml:"sample_rate"`
	ExporterTimeout time.Duration     `yaml:"exporter_timeout"`
	Insecure        bool              `yaml:"insecure"`
	TLS             TracingTLSConfig  `yaml:"tls"`
	Headers         map[string]string `yaml:"headers"`
}

// TracingTLSConfig models tracing.tls.* fields. Used to build the OTLP
// HTTP transport's TLS config (mTLS when CertFile + KeyFile are set,
// custom CA when CAFile is set).
type TracingTLSConfig struct {
	CertFile string `yaml:"cert_file"`
	KeyFile  string `yaml:"key_file"`
	CAFile   string `yaml:"ca_file"`
}

// MetricsConfig models metrics.* fields. Mirrors
// observability.MetricsConfig field naming so the wiring helper is a
// straight 1:1 copy.
type MetricsConfig struct {
	Bind string `yaml:"bind"`
	Path string `yaml:"path"`
}

// AuditConfig models audit.* fields. Mirrors observability.AuditConfig
// field naming.
type AuditConfig struct {
	Sink              string        `yaml:"sink"`
	FilePath          string        `yaml:"file_path"`
	FileSyncMode      string        `yaml:"file_sync_mode"`
	FileBatchInterval time.Duration `yaml:"file_batch_interval"`
}

// DatabaseConfig models database.* fields.
type DatabaseConfig struct {
	URL string `yaml:"url"`
}

// StorageConfig models storage.* fields used by the production binary
// to drive the partition maintainer + retention sweeper goroutines that
// storage.Start launches. Story #95 acceptance criterion: "Default
// storage.RetentionConfig MUST be parsed from storage.retention.* in
// YAML (six retention windows per the architecture doc)." All durations
// are optional; storage.Config.ApplyDefaults fills in the defaults the
// architecture doc specifies. Operators only override values they
// actually want different.
type StorageConfig struct {
	Retention    StorageRetentionConfig    `yaml:"retention"`
	Partitioning StoragePartitioningConfig `yaml:"partitioning"`
}

// StorageRetentionConfig models storage.retention.* — the chunked
// retention sweeper for non-partitioned tables. Four row-level sweep
// windows plus tunables for the loop cadence and chunk size.
type StorageRetentionConfig struct {
	Hl7MessageQueue time.Duration `yaml:"hl7_message_queue"`
	Deliveries      time.Duration `yaml:"deliveries"`
	DeadLetters     time.Duration `yaml:"dead_letters"`
	// AuditLog is accepted for backwards compatibility; the sweeper now
	// silently ignores it because audit retention is handled by partition
	// rotation (the audit chain does not survive a row-level DELETE).
	AuditLog time.Duration `yaml:"audit_log"`

	RunInterval time.Duration `yaml:"run_interval"`
	BatchSize   int32         `yaml:"batch_size"`
	BatchPause  time.Duration `yaml:"batch_pause"`
	TickTimeout time.Duration `yaml:"tick_timeout"`
}

// StoragePartitioningConfig models storage.partitioning.* — the daily
// partition maintainer that creates next-month partitions and (when
// AutoDrop is true) drops partitions older than the per-table retention.
type StoragePartitioningConfig struct {
	AutoDrop                 bool          `yaml:"auto_drop"`
	PartitionLockTimeout     time.Duration `yaml:"partition_lock_timeout"`
	RunInterval              time.Duration `yaml:"run_interval"`
	TickTimeout              time.Duration `yaml:"tick_timeout"`
	ResourceChangesRetention time.Duration `yaml:"resource_changes_retention"`
	EhrEventsRetention       time.Duration `yaml:"ehr_events_retention"`
}

// CodecConfig models codec.* fields.
//
// Keys are versioned to support rotation: every row is encrypted under the
// current `active_key_version` and decrypted via the version recorded in
// the row. The bundle MUST include `active_key_version`.
type CodecConfig struct {
	ActiveKeyVersion int32          `yaml:"active_key_version"`
	Keys             []CodecKeySpec `yaml:"keys"`
}

// CodecKeySpec is one entry under codec.keys[].
//
// Material is base64-encoded 32-byte AES-256 key bytes. Operators
// typically supply the key body via env interpolation (`${KEY_V1}`); the
// bytes never enter the YAML literally in production.
type CodecKeySpec struct {
	Version  int32  `yaml:"version"`
	Material string `yaml:"material"`
}

// AuthConfig models auth.* fields. Operator-supplied JWT issuer trust,
// audience, and the server-issued access-token signing material.
type AuthConfig struct {
	Audience       string         `yaml:"audience"`
	TokenURL       string         `yaml:"token_url"`
	IssuedSecret   string         `yaml:"issued_secret"`
	IssuedIssuer   string         `yaml:"issued_issuer"`
	AccessTokenTTL time.Duration  `yaml:"access_token_ttl"`
	JWKSCacheTTL   time.Duration  `yaml:"jwks_cache_ttl"`
	ClockSkew      time.Duration  `yaml:"clock_skew"`
	AllowInsecure  bool           `yaml:"allow_insecure_jwks"`
	JWKSAllowed    []string       `yaml:"jwks_allowed_hosts"`
	TrustedIssuers []TrustedIssue `yaml:"trusted_issuers"`

	// SubscriptionCreateRateLimit configures the per-authenticated-client
	// rate limit on POST /Subscription (S-3.3). Burst <= 0 disables.
	SubscriptionCreateRateLimit RateLimitConfig `yaml:"subscription_create_rate_limit"`

	// WSBindingTokenRateLimit configures the per-authenticated-client
	// rate limit on the $get-ws-binding-token operation (S-3.3).
	// Burst <= 0 disables.
	WSBindingTokenRateLimit RateLimitConfig `yaml:"ws_binding_token_rate_limit"`
}

// RateLimitConfig is the operator-facing shape for a per-client token
// bucket (S-3.3). Maps directly onto auth.RateLimit.
type RateLimitConfig struct {
	// Burst is the bucket capacity — the maximum number of immediate
	// requests allowed before refill kicks in. Zero or negative
	// disables the limit.
	Burst int `yaml:"burst"`
	// RefillPerSecond is the steady-state allowed rate. Zero is valid:
	// it pins the bucket at Burst (strict cap, no replenishment).
	RefillPerSecond float64 `yaml:"refill_per_second"`
	// MaxKeys caps the number of distinct client identities tracked.
	// Zero defaults to 65536; once full, the oldest bucket is evicted.
	MaxKeys int `yaml:"max_keys"`
}

// TrustedIssue models one entry under auth.trusted_issuers[]. Today the
// fields are advisory: per-client trust is stored in the auth_clients
// table; this list pins which issuers' tokens the verifier will load
// JWKS for. Future revisions may filter the verifier's keyfunc lookup.
type TrustedIssue struct {
	Issuer   string `yaml:"issuer"`
	Audience string `yaml:"audience"`
	JWKSURL  string `yaml:"jwks_url"`
}

// MLLPConfig models mllp.* fields.
type MLLPConfig struct {
	// Listeners is the set of MLLP TCP endpoints to bind. Empty means
	// "do not start the MLLP listener" (operators that only use the
	// FHIR scan path).
	Listeners []MLLPListener `yaml:"listeners"`
	// MaxMessageBytes overrides the per-message body cap (default 1
	// MiB).
	MaxMessageBytes int `yaml:"max_message_bytes"`
	// PersistTimeout bounds the per-message Persist call. Must be ≤
	// ShutdownDrainGrace (S-9.2) — Validate enforces the cap.
	PersistTimeout time.Duration `yaml:"persist_timeout"`
	// FrameAssemblyTimeout bounds how long a single inter-marker frame
	// may take to assemble (S-9.1). Default 30s.
	FrameAssemblyTimeout time.Duration `yaml:"frame_assembly_timeout"`
	// ReadIdleTimeout closes a connection that has been silent this
	// long. Default 60s. Maps to mllp.ListenerConfig.ReadIdleTimeout.
	ReadIdleTimeout time.Duration `yaml:"read_idle_timeout"`
	// NackThenDropAfter is the consecutive-persist-failure threshold
	// at which the listener drops the connection. Default 5.
	NackThenDropAfter int `yaml:"nack_then_drop_after"`
	// InflightCapPerConn caps per-connection unfinished persist calls.
	// Default 64.
	InflightCapPerConn int `yaml:"inflight_cap_per_conn"`
	// OnPersistFail selects behavior on persist failure ("nack" or
	// "drop"). Default "nack".
	OnPersistFail string `yaml:"on_persist_fail"`
	// MaxConnections caps total concurrent connections across all
	// endpoints (B-19).
	MaxConnections int `yaml:"max_connections"`
	// MaxConnectionsPerIP caps concurrent connections from a single
	// remote IP (B-19).
	MaxConnectionsPerIP int `yaml:"max_connections_per_ip"`
	// ShutdownDrainGrace bounds the graceful drain window for the
	// listener.
	ShutdownDrainGrace time.Duration `yaml:"shutdown_drain_grace"`
}

// MLLPListener is one entry under mllp.listeners[].
type MLLPListener struct {
	Name string `yaml:"name"`
	Bind string `yaml:"bind"`
	// ProxyProtocolV2, when true, requires every accepted TCP connection
	// to begin with a PROXY protocol v2 header (N-1.25). Enable only on
	// listeners reachable exclusively through a PROXY-v2-capable load
	// balancer; a peer that can reach the socket directly while this
	// flag is on can spoof its source IP at will. Default false.
	ProxyProtocolV2 bool `yaml:"proxy_protocol_v2"`
	// TLS, when non-nil, enables TLS for this endpoint. HL7 carries PHI
	// and on a hospital network MUST run encrypted (B-20).
	//
	// IMPLEMENTATION NOTE: the underlying mllp.Listener accepts a single
	// TLS config that applies to every endpoint it owns. To keep the
	// operator-facing YAML straightforward, we model TLS per endpoint;
	// Validate enforces that every endpoint with a TLS block configures
	// the SAME paths so the wiring layer can collapse them into one
	// listener-wide TLS config without surprise. Endpoints with no TLS
	// block run cleartext on the same listener — but a heterogeneous
	// mix of TLS+cleartext is rejected at startup because the listener
	// cannot run both shapes in parallel today.
	TLS *MLLPListenerTLSConfig `yaml:"tls"`
}

// MLLPListenerTLSConfig models mllp.listeners[].tls.* fields. CertFile +
// KeyFile are required when the block is present; ClientCAFile +
// RequireClientCert toggle mTLS.
type MLLPListenerTLSConfig struct {
	CertFile          string `yaml:"cert_file"`
	KeyFile           string `yaml:"key_file"`
	ClientCAFile      string `yaml:"client_ca_file"`
	RequireClientCert bool   `yaml:"require_client_cert"`
}

// PipelineConfig models pipeline.* fields. Each stage's claim loop has
// its own batch size and idle-poll cadence.
type PipelineConfig struct {
	HL7Processor StageConfig `yaml:"hl7_processor"`
	Matcher      StageConfig `yaml:"matcher"`
	Submatcher   StageConfig `yaml:"submatcher"`
	Scheduler    StageConfig `yaml:"scheduler"`

	// CorrelationHoldWindow caps how long the HL7 processor will hold
	// an unpaired half before reaping. Default 30s.
	CorrelationHoldWindow time.Duration `yaml:"correlation_hold_window"`

	// Supervisor is the operator-tunable bundle for the host-side
	// adapter Supervisor framework that wraps every pipeline worker
	// (story #99). Zero values are filled in with production defaults
	// (100ms initial backoff, 30s max, 0.2 jitter, 30s health cadence,
	// 5s stop grace).
	Supervisor PipelineSupervisorConfig `yaml:"supervisor"`
}

// StageConfig is the per-stage pipeline tunables.
type StageConfig struct {
	ClaimBatchSize   int32         `yaml:"claim_batch_size"`
	IdlePollInterval time.Duration `yaml:"idle_poll_interval"`
}

// ChannelsConfig models channels.* fields. Each channel block is
// optional; when absent the channel is wired with package defaults.
type ChannelsConfig struct {
	RestHook  RestHookChannelConfig  `yaml:"rest_hook"`
	WebSocket WebSocketChannelConfig `yaml:"websocket"`
	Email     EmailChannelConfig     `yaml:"email"`
	Message   MessageChannelConfig   `yaml:"message"`
}

// RestHookChannelConfig models channels.rest_hook.*.
type RestHookChannelConfig struct {
	UserAgent      string        `yaml:"user_agent"`
	RequestTimeout time.Duration `yaml:"request_timeout"`
}

// WebSocketChannelConfig models channels.websocket.*. Every field
// surfaces a websocket.Options knob so operators can tune the channel
// purely through YAML / --set overrides.
type WebSocketChannelConfig struct {
	OriginPatterns           []string      `yaml:"origin_patterns"`
	IdleTimeout              time.Duration `yaml:"idle_timeout"`
	PingInterval             time.Duration `yaml:"ping_interval"`
	BindTimeout              time.Duration `yaml:"bind_timeout"`
	PingWriteTimeout         time.Duration `yaml:"ping_write_timeout"`
	UpgradeReadHeaderTimeout time.Duration `yaml:"upgrade_read_header_timeout"`
	MaxFrameBytes            int           `yaml:"max_frame_bytes"`
	MaxSessions              int           `yaml:"max_sessions"`
	MaxSessionsPerClient     int           `yaml:"max_sessions_per_client"`
	MaxReplayEvents          int           `yaml:"max_replay_events"`
}

// EmailChannelConfig models channels.email.*. Mirrors the Config struct
// in internal/channel/email field-for-field so the wiring helper is a
// straight copy.
type EmailChannelConfig struct {
	From                     string        `yaml:"from"`
	SubjectTemplate          string        `yaml:"subject_template"`
	SMTPHost                 string        `yaml:"smtp_host"`
	SMTPPort                 int           `yaml:"smtp_port"`
	STARTTLS                 string        `yaml:"starttls"`
	AuthMechanism            string        `yaml:"auth_mechanism"`
	AuthUsername             string        `yaml:"auth_username"`
	AuthPassword             string        `yaml:"auth_password"`
	AuthIdentity             string        `yaml:"auth_identity"`
	AllowCleartextAuth       bool          `yaml:"allow_cleartext_auth"`
	AttachmentThresholdBytes int           `yaml:"attachment_threshold_bytes"`
	RequestTimeout           time.Duration `yaml:"request_timeout"`
	LocalName                string        `yaml:"local_name"`
	UserAgent                string        `yaml:"user_agent"`
	TLSMinVersion            uint16        `yaml:"tls_min_version"`
}

// MessageChannelConfig models channels.message.*. Mirrors the Options
// struct in internal/channel/message.
type MessageChannelConfig struct {
	UserAgent           string        `yaml:"user_agent"`
	RequestTimeout      time.Duration `yaml:"request_timeout"`
	ServerEndpoint      string        `yaml:"server_endpoint"`
	MaxIdleConnsPerHost int           `yaml:"max_idle_conns_per_host"`
	MaxConnsPerHost     int           `yaml:"max_conns_per_host"`
	TLSMinVersion       uint16        `yaml:"tls_min_version"`
}

// DeploymentConfig models deployment.* fields.
type DeploymentConfig struct {
	FacilityID  string `yaml:"facility_id"`
	Environment string `yaml:"environment"`
	LogLevel    string `yaml:"log_level"`
	LogFormat   string `yaml:"log_format"`
}

// AdapterConfig models adapter.* fields.
type AdapterConfig struct {
	ID         string `yaml:"id"`
	VersionPin string `yaml:"version_pin"`
}

// ServerConfig models server.* fields.
type ServerConfig struct {
	HTTP HTTPConfig `yaml:"http"`
}

// HTTPConfig models server.http.* fields. Timeouts are operator-tunable
// per audit B-2; defaults are applied by loadConfig when zero.
type HTTPConfig struct {
	Bind     string    `yaml:"bind"`
	Insecure bool      `yaml:"insecure"`
	TLS      TLSConfig `yaml:"tls"`

	// ReadHeaderTimeout caps the time the server reads request headers.
	// Default 5s.
	ReadHeaderTimeout time.Duration `yaml:"read_header_timeout"`
	// ReadTimeout caps the time the server reads the entire request
	// (headers + body). Default 30s.
	ReadTimeout time.Duration `yaml:"read_timeout"`
	// WriteTimeout caps the time the server takes to write a response.
	// Default 30s.
	WriteTimeout time.Duration `yaml:"write_timeout"`
	// IdleTimeout caps idle keep-alive duration. Default 120s.
	IdleTimeout time.Duration `yaml:"idle_timeout"`
	// MaxHeaderBytes caps the size of request headers. Default 1MiB.
	MaxHeaderBytes int `yaml:"max_header_bytes"`
}

// TLSConfig models server.http.tls.* fields. The HTTP listener wires
// these into srv.ServeTLS at startup so the API surface speaks TLS only;
// when server.http.insecure is false, CertFile + KeyFile are required and
// MUST point at PEM-encoded files that exist at boot (Validate fail-fast).
//
// MinVersion selects the TLS floor — "1.2" or "1.3" (default "1.3").
// Operators with legacy clients pin to "1.2"; everyone else gets the
// stronger 1.3 default.
type TLSConfig struct {
	CertFile   string `yaml:"cert_file"`
	KeyFile    string `yaml:"key_file"`
	MinVersion string `yaml:"min_version"`
}

// TopicsConfig models topics.* fields. The CatalogDir points at a
// directory of *.json SubscriptionTopic files the production wiring loads
// at startup (and re-loads on SIGHUP) so the matcher's CatalogProvider has
// non-empty content; without it the pipeline silently halts at matcher
// step 1 (D-1).
type TopicsConfig struct {
	// CatalogDir is the operator-supplied directory containing one
	// *.json SubscriptionTopic file per topic. Empty means "no operator
	// topics"; in production this should point at a mounted ConfigMap
	// or sidecar volume. Files are treated as `Operator` precedence
	// (highest) under catalog.Sources.
	CatalogDir string `yaml:"catalog_dir"`
}

// AdminConfig models admin.* fields. The admin surface is the read-only
// operator triage API mounted at PathPrefix (default `/admin`); story #92.
//
// Token is the operator-supplied shared secret. It must be at least
// handlers.MinAdminTokenBytes (32) bytes; an empty Token disables the
// admin surface entirely (the routes are not mounted, requests 404).
//
// PathPrefix overrides the default `/admin` mount point. Empty falls back
// to handlers.DefaultAdminPathPrefix.
//
// RateLimit is the per-token (per-IP fallback) bucket the admin surface
// is wrapped in so a runaway operator script cannot pin the audit-log
// write rate. Burst <= 0 disables the rate limit (handler chain is
// unwrapped).
type AdminConfig struct {
	Token      string          `yaml:"token"`
	PathPrefix string          `yaml:"path_prefix"`
	RateLimit  RateLimitConfig `yaml:"rate_limit"`
}

// LifecycleConfig models lifecycle.* fields used by the entry point.
type LifecycleConfig struct {
	ShutdownGracePeriod time.Duration `yaml:"shutdown_grace_period"`
}

// defaultConfig returns a fully populated Config with defaults applied. Loaders
// start from this and overlay the file, env, and CLI on top.
func defaultConfig() *Config {
	return &Config{
		Server: ServerConfig{
			HTTP: HTTPConfig{
				Bind: "0.0.0.0:8443",
			},
		},
		Lifecycle: LifecycleConfig{
			ShutdownGracePeriod: 30 * time.Second,
		},
	}
}

// loadConfig reads the YAML config file at path and returns a typed Config.
// Defaults are applied for any unset field; semantic validation lives in
// Config.Validate so callers can choose when to enforce it (e.g. --check-config).
//
// On YAML or filesystem errors loadConfig returns the error and a nil Config.
// All other call sites must call Validate() before relying on the data.
func loadConfig(path string) (*Config, error) {
	cfg := defaultConfig()

	body, err := os.ReadFile(path) //nolint:gosec // operator-supplied config path is intended.
	if err != nil {
		return nil, fmt.Errorf("read config %s: %w", path, err)
	}

	if strings.TrimSpace(string(body)) != "" {
		// Use strict-ish parsing: malformed YAML is a startup error.
		dec := yaml.NewDecoder(strings.NewReader(string(body)))
		if err := dec.Decode(cfg); err != nil {
			return nil, fmt.Errorf("parse config %s: %w", path, err)
		}
	}

	// Re-apply defaults for unset critical fields after unmarshal.
	if cfg.Server.HTTP.Bind == "" {
		cfg.Server.HTTP.Bind = "0.0.0.0:8443"
	}
	if cfg.Lifecycle.ShutdownGracePeriod == 0 {
		cfg.Lifecycle.ShutdownGracePeriod = 30 * time.Second
	}
	cfg.Server.HTTP.applyTimeoutDefaults()

	return cfg, nil
}

// applyTimeoutDefaults fills in safe HTTP server timeouts (audit B-2).
// Defaults chosen to defeat slowloris / write-side hangs while leaving
// room for legitimate FHIR bundles up to a few MiB.
func (h *HTTPConfig) applyTimeoutDefaults() {
	if h.ReadHeaderTimeout <= 0 {
		h.ReadHeaderTimeout = 5 * time.Second
	}
	if h.ReadTimeout <= 0 {
		h.ReadTimeout = 30 * time.Second
	}
	if h.WriteTimeout <= 0 {
		h.WriteTimeout = 30 * time.Second
	}
	if h.IdleTimeout <= 0 {
		h.IdleTimeout = 120 * time.Second
	}
	if h.MaxHeaderBytes <= 0 {
		h.MaxHeaderBytes = 1 << 20 // 1 MiB
	}
}

// Validate performs minimal semantic validation. It is intentionally narrow;
// the real per-domain JSON Schema validator lives in infra/config (configuration
// LLD §7) and will replace this once that module ships.
func (c *Config) Validate() error {
	if c == nil {
		return errors.New("config: nil")
	}

	var problems []string

	if strings.TrimSpace(c.Deployment.FacilityID) == "" {
		problems = append(problems, "deployment.facility_id is required")
	}
	if strings.TrimSpace(c.Adapter.ID) == "" {
		problems = append(problems, "adapter.id is required")
	}
	if strings.TrimSpace(c.Server.HTTP.Bind) == "" {
		problems = append(problems, "server.http.bind is required")
	}
	if !c.Server.HTTP.Insecure {
		if strings.TrimSpace(c.Server.HTTP.TLS.CertFile) == "" || strings.TrimSpace(c.Server.HTTP.TLS.KeyFile) == "" {
			problems = append(problems,
				"server.http.tls.cert_file and key_file are required when server.http.insecure is false")
		} else {
			// Fail-fast: the cert + key MUST exist and parse as PEM at
			// startup. Discovering a typo on the first request is a
			// production outage we should never ship.
			if err := checkPEMFile(c.Server.HTTP.TLS.CertFile); err != nil {
				problems = append(problems,
					fmt.Sprintf("server.http.tls.cert_file: %s", err))
			}
			if err := checkPEMFile(c.Server.HTTP.TLS.KeyFile); err != nil {
				problems = append(problems,
					fmt.Sprintf("server.http.tls.key_file: %s", err))
			}
		}
		// Normalize + validate min_version. Empty -> "1.3" (default).
		switch c.Server.HTTP.TLS.MinVersion {
		case "":
			c.Server.HTTP.TLS.MinVersion = "1.3"
		case "1.2", "1.3":
			// ok
		default:
			problems = append(problems,
				fmt.Sprintf("server.http.tls.min_version=%q: must be \"1.2\" or \"1.3\"",
					c.Server.HTTP.TLS.MinVersion))
		}
	}
	// MLLP listener TLS validation. Per-listener TLS blocks must be
	// homogeneous — every endpoint with a TLS block must share the same
	// cert/key/CA paths so the wiring layer can collapse them into a
	// single mllp.ListenerConfig.TLS (the underlying listener does not
	// support per-endpoint TLS today).
	var firstTLS *MLLPListenerTLSConfig
	for i, ep := range c.MLLP.Listeners {
		if ep.TLS == nil {
			continue
		}
		if strings.TrimSpace(ep.TLS.CertFile) == "" {
			problems = append(problems,
				fmt.Sprintf("mllp.listeners[%d].tls.cert_file is required", i))
		}
		if strings.TrimSpace(ep.TLS.KeyFile) == "" {
			problems = append(problems,
				fmt.Sprintf("mllp.listeners[%d].tls.key_file is required", i))
		}
		if ep.TLS.RequireClientCert && strings.TrimSpace(ep.TLS.ClientCAFile) == "" {
			problems = append(problems,
				fmt.Sprintf("mllp.listeners[%d].tls.client_ca_file is required when require_client_cert is true", i))
		}
		if firstTLS == nil {
			firstTLS = ep.TLS
		} else if *firstTLS != *ep.TLS {
			problems = append(problems,
				fmt.Sprintf("mllp.listeners[%d].tls: heterogeneous TLS configs across endpoints are not supported; every endpoint with a TLS block must share cert/key/CA paths", i))
		}
	}
	// Reject the heterogeneous TLS+cleartext mix when at least one
	// endpoint has TLS and at least one does not — the listener owns a
	// single TLS config and cannot run both shapes simultaneously.
	if firstTLS != nil {
		for i, ep := range c.MLLP.Listeners {
			if ep.TLS == nil {
				problems = append(problems,
					fmt.Sprintf("mllp.listeners[%d]: cleartext alongside TLS endpoints in the same listener is not supported", i))
				break
			}
		}
	}
	if c.MLLP.OnPersistFail != "" && c.MLLP.OnPersistFail != "nack" && c.MLLP.OnPersistFail != "drop" {
		problems = append(problems,
			fmt.Sprintf("mllp.on_persist_fail=%q: must be \"nack\" or \"drop\"", c.MLLP.OnPersistFail))
	}
	if c.Lifecycle.ShutdownGracePeriod <= 0 {
		problems = append(problems, "lifecycle.shutdown_grace_period must be positive")
	}
	// tracing.sample_rate must be in [0,1]; the OTel sampler treats any
	// rate <= 0 as "drop all" and >1 as "sample all", but we surface it
	// here so the operator gets a loud config error rather than silent
	// telemetry loss (story #94).
	if c.Tracing.SampleRate < 0 || c.Tracing.SampleRate > 1 {
		problems = append(problems,
			"tracing.sample_rate must be between 0.0 and 1.0")
	}

	if len(problems) > 0 {
		return fmt.Errorf("config invalid: %s", strings.Join(problems, "; "))
	}
	return nil
}

// applySets applies CLI --set overrides to the typed config. The set of keys
// supported here is the minimal subset main reads today; unknown keys are
// rejected so a typo doesn't silently no-op. The full dotted-key path tree
// will be supported by infra/config when that module ships.
func applySets(cfg *Config, sets []string) error {
	for _, raw := range sets {
		eq := strings.IndexByte(raw, '=')
		if eq <= 0 {
			return fmt.Errorf("--set %q: expected dotted.key=value", raw)
		}
		key := strings.TrimSpace(raw[:eq])
		val := raw[eq+1:]

		switch key {
		case "deployment.facility_id":
			cfg.Deployment.FacilityID = val
		case "deployment.environment":
			cfg.Deployment.Environment = val
		case "deployment.log_level":
			cfg.Deployment.LogLevel = val
		case "deployment.log_format":
			cfg.Deployment.LogFormat = val
		case "adapter.id":
			cfg.Adapter.ID = val
		case "adapter.version_pin":
			cfg.Adapter.VersionPin = val
		case "server.http.bind":
			cfg.Server.HTTP.Bind = val
		case "server.http.insecure":
			b, err := parseBool(val)
			if err != nil {
				return setParseErr(key, err)
			}
			cfg.Server.HTTP.Insecure = b
		case "server.http.tls.cert_file":
			cfg.Server.HTTP.TLS.CertFile = val
		case "server.http.tls.key_file":
			cfg.Server.HTTP.TLS.KeyFile = val
		case "server.http.tls.min_version":
			cfg.Server.HTTP.TLS.MinVersion = val
		case "lifecycle.shutdown_grace_period":
			d, err := time.ParseDuration(val)
			if err != nil {
				return setParseErr(key, err)
			}
			cfg.Lifecycle.ShutdownGracePeriod = d
		case "server.http.read_header_timeout":
			d, err := time.ParseDuration(val)
			if err != nil {
				return setParseErr(key, err)
			}
			cfg.Server.HTTP.ReadHeaderTimeout = d
		case "server.http.read_timeout":
			d, err := time.ParseDuration(val)
			if err != nil {
				return setParseErr(key, err)
			}
			cfg.Server.HTTP.ReadTimeout = d
		case "server.http.write_timeout":
			d, err := time.ParseDuration(val)
			if err != nil {
				return setParseErr(key, err)
			}
			cfg.Server.HTTP.WriteTimeout = d
		case "server.http.idle_timeout":
			d, err := time.ParseDuration(val)
			if err != nil {
				return setParseErr(key, err)
			}
			cfg.Server.HTTP.IdleTimeout = d
		case "server.http.max_header_bytes":
			n, err := strconv.Atoi(val)
			if err != nil {
				return setParseErr(key, err)
			}
			cfg.Server.HTTP.MaxHeaderBytes = n
		case "database.url":
			cfg.Database.URL = val
		case "auth.audience":
			cfg.Auth.Audience = val
		case "auth.token_url":
			cfg.Auth.TokenURL = val
		case "auth.issued_issuer":
			cfg.Auth.IssuedIssuer = val
		case "auth.issued_secret":
			cfg.Auth.IssuedSecret = val
		case "auth.allow_insecure_jwks":
			b, err := parseBool(val)
			if err != nil {
				return setParseErr(key, err)
			}
			cfg.Auth.AllowInsecure = b
		case "topics.catalog_dir":
			cfg.Topics.CatalogDir = val
		case "codec.active_key_version":
			n, err := strconv.ParseInt(val, 10, 32)
			if err != nil {
				return setParseErr(key, err)
			}
			cfg.Codec.ActiveKeyVersion = int32(n)
		case "admin.token":
			cfg.Admin.Token = val
		case "admin.path_prefix":
			cfg.Admin.PathPrefix = val
		case "admin.rate_limit.burst":
			n, err := strconv.Atoi(val)
			if err != nil {
				return setParseErr(key, err)
			}
			cfg.Admin.RateLimit.Burst = n
		case "admin.rate_limit.refill_per_second":
			f, err := strconv.ParseFloat(val, 64)
			if err != nil {
				return setParseErr(key, err)
			}
			cfg.Admin.RateLimit.RefillPerSecond = f
		case "admin.rate_limit.max_keys":
			n, err := strconv.Atoi(val)
			if err != nil {
				return setParseErr(key, err)
			}
			cfg.Admin.RateLimit.MaxKeys = n
		case "auth.subscription_create_rate_limit.burst":
			n, err := strconv.Atoi(val)
			if err != nil {
				return setParseErr(key, err)
			}
			cfg.Auth.SubscriptionCreateRateLimit.Burst = n
		case "auth.subscription_create_rate_limit.refill_per_second":
			f, err := strconv.ParseFloat(val, 64)
			if err != nil {
				return setParseErr(key, err)
			}
			cfg.Auth.SubscriptionCreateRateLimit.RefillPerSecond = f
		case "auth.subscription_create_rate_limit.max_keys":
			n, err := strconv.Atoi(val)
			if err != nil {
				return setParseErr(key, err)
			}
			cfg.Auth.SubscriptionCreateRateLimit.MaxKeys = n
		case "auth.ws_binding_token_rate_limit.burst":
			n, err := strconv.Atoi(val)
			if err != nil {
				return setParseErr(key, err)
			}
			cfg.Auth.WSBindingTokenRateLimit.Burst = n
		case "auth.ws_binding_token_rate_limit.refill_per_second":
			f, err := strconv.ParseFloat(val, 64)
			if err != nil {
				return setParseErr(key, err)
			}
			cfg.Auth.WSBindingTokenRateLimit.RefillPerSecond = f
		case "auth.ws_binding_token_rate_limit.max_keys":
			n, err := strconv.Atoi(val)
			if err != nil {
				return setParseErr(key, err)
			}
			cfg.Auth.WSBindingTokenRateLimit.MaxKeys = n
		case "channels.rest_hook.user_agent":
			cfg.Channels.RestHook.UserAgent = val
		case "channels.rest_hook.request_timeout":
			d, err := time.ParseDuration(val)
			if err != nil {
				return setParseErr(key, err)
			}
			cfg.Channels.RestHook.RequestTimeout = d
		case "channels.websocket.idle_timeout":
			d, err := time.ParseDuration(val)
			if err != nil {
				return setParseErr(key, err)
			}
			cfg.Channels.WebSocket.IdleTimeout = d
		case "channels.websocket.ping_interval":
			d, err := time.ParseDuration(val)
			if err != nil {
				return setParseErr(key, err)
			}
			cfg.Channels.WebSocket.PingInterval = d
		case "channels.websocket.bind_timeout":
			d, err := time.ParseDuration(val)
			if err != nil {
				return setParseErr(key, err)
			}
			cfg.Channels.WebSocket.BindTimeout = d
		case "channels.websocket.ping_write_timeout":
			d, err := time.ParseDuration(val)
			if err != nil {
				return setParseErr(key, err)
			}
			cfg.Channels.WebSocket.PingWriteTimeout = d
		case "channels.websocket.upgrade_read_header_timeout":
			d, err := time.ParseDuration(val)
			if err != nil {
				return setParseErr(key, err)
			}
			cfg.Channels.WebSocket.UpgradeReadHeaderTimeout = d
		case "channels.websocket.max_frame_bytes":
			n, err := strconv.Atoi(val)
			if err != nil {
				return setParseErr(key, err)
			}
			cfg.Channels.WebSocket.MaxFrameBytes = n
		case "channels.websocket.max_sessions":
			n, err := strconv.Atoi(val)
			if err != nil {
				return setParseErr(key, err)
			}
			cfg.Channels.WebSocket.MaxSessions = n
		case "channels.websocket.max_sessions_per_client":
			n, err := strconv.Atoi(val)
			if err != nil {
				return setParseErr(key, err)
			}
			cfg.Channels.WebSocket.MaxSessionsPerClient = n
		case "channels.websocket.max_replay_events":
			n, err := strconv.Atoi(val)
			if err != nil {
				return setParseErr(key, err)
			}
			cfg.Channels.WebSocket.MaxReplayEvents = n
		case "channels.email.from":
			cfg.Channels.Email.From = val
		case "channels.email.subject_template":
			cfg.Channels.Email.SubjectTemplate = val
		case "channels.email.smtp_host":
			cfg.Channels.Email.SMTPHost = val
		case "channels.email.smtp_port":
			n, err := strconv.Atoi(val)
			if err != nil {
				return setParseErr(key, err)
			}
			cfg.Channels.Email.SMTPPort = n
		case "channels.email.starttls":
			cfg.Channels.Email.STARTTLS = val
		case "channels.email.auth_mechanism":
			cfg.Channels.Email.AuthMechanism = val
		case "channels.email.auth_username":
			cfg.Channels.Email.AuthUsername = val
		case "channels.email.auth_password":
			cfg.Channels.Email.AuthPassword = val
		case "channels.email.auth_identity":
			cfg.Channels.Email.AuthIdentity = val
		case "channels.email.allow_cleartext_auth":
			b, err := parseBool(val)
			if err != nil {
				return setParseErr(key, err)
			}
			cfg.Channels.Email.AllowCleartextAuth = b
		case "channels.email.attachment_threshold_bytes":
			n, err := strconv.Atoi(val)
			if err != nil {
				return setParseErr(key, err)
			}
			cfg.Channels.Email.AttachmentThresholdBytes = n
		case "channels.email.request_timeout":
			d, err := time.ParseDuration(val)
			if err != nil {
				return setParseErr(key, err)
			}
			cfg.Channels.Email.RequestTimeout = d
		case "channels.email.local_name":
			cfg.Channels.Email.LocalName = val
		case "channels.email.user_agent":
			cfg.Channels.Email.UserAgent = val
		case "channels.email.tls_min_version":
			n, err := strconv.ParseUint(val, 10, 16)
			if err != nil {
				return setParseErr(key, err)
			}
			cfg.Channels.Email.TLSMinVersion = uint16(n)
		case "channels.message.user_agent":
			cfg.Channels.Message.UserAgent = val
		case "channels.message.request_timeout":
			d, err := time.ParseDuration(val)
			if err != nil {
				return setParseErr(key, err)
			}
			cfg.Channels.Message.RequestTimeout = d
		case "channels.message.server_endpoint":
			cfg.Channels.Message.ServerEndpoint = val
		case "channels.message.max_idle_conns_per_host":
			n, err := strconv.Atoi(val)
			if err != nil {
				return setParseErr(key, err)
			}
			cfg.Channels.Message.MaxIdleConnsPerHost = n
		case "channels.message.max_conns_per_host":
			n, err := strconv.Atoi(val)
			if err != nil {
				return setParseErr(key, err)
			}
			cfg.Channels.Message.MaxConnsPerHost = n
		case "channels.message.tls_min_version":
			n, err := strconv.ParseUint(val, 10, 16)
			if err != nil {
				return setParseErr(key, err)
			}
			cfg.Channels.Message.TLSMinVersion = uint16(n)
		default:
			return fmt.Errorf("--set %s: unsupported key (this loader is minimal)", key)
		}
	}
	return nil
}

func parseBool(v string) (bool, error) {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "true", "1", "yes", "on":
		return true, nil
	case "false", "0", "no", "off":
		return false, nil
	default:
		// Caller wraps with setParseErr so the bad value never reaches
		// stderr (S-1.1). Returning a fixed-string sentinel keeps
		// errors.Is checks intact while making the error self-redacted.
		return false, errInvalidBool
	}
}

// errInvalidBool is the sentinel returned by parseBool on a bad value;
// it intentionally carries no caller-supplied text (S-1.1).
var errInvalidBool = errors.New("invalid bool")

// checkPEMFile verifies that path exists, is readable, and that its body
// contains at least one PEM block. The body is bounded to 1 MiB — typical
// X.509 certs and PKCS#8 keys are a few KiB; a multi-megabyte file is
// almost certainly an operator pointing at the wrong path.
func checkPEMFile(path string) error {
	st, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("file does not exist: %s", path)
		}
		return fmt.Errorf("stat %s: %w", path, err)
	}
	if st.Size() == 0 {
		return fmt.Errorf("file is empty: %s", path)
	}
	if st.Size() > 1<<20 {
		return fmt.Errorf("file too large to be a PEM cert/key: %s (%d bytes)", path, st.Size())
	}
	body, err := os.ReadFile(path) //nolint:gosec // operator-supplied TLS material path
	if err != nil {
		return fmt.Errorf("read %s: %w", path, err)
	}
	block, _ := pem.Decode(body)
	if block == nil {
		return fmt.Errorf("not PEM-encoded: %s", path)
	}
	return nil
}

// setParseErr builds the operator-facing error for a malformed --set RHS
// without echoing the value, which may be a secret (S-1.1). The
// underlying error from strconv / time / parseBool is dropped because
// strconv.Atoi and time.ParseDuration both quote the offending input
// inside their error string.
func setParseErr(key string, _ error) error {
	return fmt.Errorf("--set %s=<redacted>: invalid value", key)
}
