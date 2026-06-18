// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

// Package configtypes holds the typed structs that mirror the architecture's
// canonical YAML shape, one per domain. Mirrors `architecture.md` "Configuration"
// and the per-domain LLDs.
//
// Duration-typed fields are kept as strings here so the generic JSON/YAML
// round-trip through map[string]interface{} works without bespoke
// unmarshalers; consumers convert via time.ParseDuration.
package configtypes

// DeploymentConfig — `deployment.*` per architecture.md.
type DeploymentConfig struct {
	FacilityID  string `yaml:"facility_id"  toml:"facility_id"  json:"facility_id"`
	Environment string `yaml:"environment"  toml:"environment"  json:"environment"`
	LogLevel    string `yaml:"log_level"    toml:"log_level"    json:"log_level"`
	LogFormat   string `yaml:"log_format"   toml:"log_format"   json:"log_format"`
}

// ServerHTTPTLSConfig — `server.http.tls.*`.
type ServerHTTPTLSConfig struct {
	CertFile string `yaml:"cert_file" toml:"cert_file" json:"cert_file"`
	KeyFile  string `yaml:"key_file"  toml:"key_file"  json:"key_file"`
}

// ServerHTTPConfig — `server.http.*`.
type ServerHTTPConfig struct {
	Bind      string              `yaml:"bind"       toml:"bind"       json:"bind"`
	TLS       ServerHTTPTLSConfig `yaml:"tls"        toml:"tls"        json:"tls"`
	ProbeBind string              `yaml:"probe_bind" toml:"probe_bind" json:"probe_bind"`
}

// ServerWebSocketConfig — `server.websocket.*`.
type ServerWebSocketConfig struct {
	Enabled        bool `yaml:"enabled"         toml:"enabled"         json:"enabled"`
	MaxConnections int  `yaml:"max_connections" toml:"max_connections" json:"max_connections"`
}

// ServerConfig — `server.*`.
type ServerConfig struct {
	HTTP      ServerHTTPConfig      `yaml:"http"      toml:"http"      json:"http"`
	WebSocket ServerWebSocketConfig `yaml:"websocket" toml:"websocket" json:"websocket"`
}

// LifecycleConfig — `lifecycle.*`. Durations are operator-supplied Go
// duration strings (e.g., "30s", "2s"). Consumers parse via time.ParseDuration.
type LifecycleConfig struct {
	ShutdownGracePeriod  string `yaml:"shutdown_grace_period"   toml:"shutdown_grace_period"   json:"shutdown_grace_period"`
	PostgresProbeTimeout string `yaml:"postgres_probe_timeout"  toml:"postgres_probe_timeout"  json:"postgres_probe_timeout"`
}

// StoragePostgresConfig — `storage.postgres.*`.
type StoragePostgresConfig struct {
	URL              string `yaml:"url"               toml:"url"               json:"url"`
	PoolSize         int    `yaml:"pool_size"         toml:"pool_size"         json:"pool_size"`
	StatementTimeout string `yaml:"statement_timeout" toml:"statement_timeout" json:"statement_timeout"`
}

// StorageEncryptionConfig — `storage.encryption.*`.
type StorageEncryptionConfig struct {
	AtRestKey string `yaml:"at_rest_key" toml:"at_rest_key" json:"at_rest_key"`
}

// StorageRetentionConfig — `storage.retention.*`. Values are durations.
type StorageRetentionConfig struct {
	HL7MessageQueue string `yaml:"hl7_message_queue" toml:"hl7_message_queue" json:"hl7_message_queue"`
	ResourceChanges string `yaml:"resource_changes"  toml:"resource_changes"  json:"resource_changes"`
	EHREvents       string `yaml:"ehr_events"        toml:"ehr_events"        json:"ehr_events"`
	Deliveries      string `yaml:"deliveries"        toml:"deliveries"        json:"deliveries"`
	DeadLetters     string `yaml:"dead_letters"      toml:"dead_letters"      json:"dead_letters"`
	AuditLog        string `yaml:"audit_log"         toml:"audit_log"         json:"audit_log"`
}

// StorageConfig — `storage.*`.
type StorageConfig struct {
	Postgres   StoragePostgresConfig   `yaml:"postgres"   toml:"postgres"   json:"postgres"`
	Encryption StorageEncryptionConfig `yaml:"encryption" toml:"encryption" json:"encryption"`
	Retention  StorageRetentionConfig  `yaml:"retention"  toml:"retention"  json:"retention"`
}

// AuthJWKSConfig — `auth.jwks.*`.
type AuthJWKSConfig struct {
	CacheTTL string `yaml:"cache_ttl" toml:"cache_ttl" json:"cache_ttl"`
}

// AuthTrustedIssuer — `auth.trusted_issuers[]`.
type AuthTrustedIssuer struct {
	Issuer   string `yaml:"issuer"   toml:"issuer"   json:"issuer"`
	JWKSURL  string `yaml:"jwks_url" toml:"jwks_url" json:"jwks_url"`
	Audience string `yaml:"audience" toml:"audience" json:"audience"`
}

// AuthClient — `auth.client_registry[]`.
type AuthClient struct {
	ID      string   `yaml:"id"       toml:"id"       json:"id"`
	JWKSURL string   `yaml:"jwks_url" toml:"jwks_url" json:"jwks_url"`
	Scopes  []string `yaml:"scopes"   toml:"scopes"   json:"scopes"`
}

// AuthConfig — `auth.*`.
type AuthConfig struct {
	Schemes        []string            `yaml:"schemes"         toml:"schemes"         json:"schemes"`
	JWKS           AuthJWKSConfig      `yaml:"jwks"            toml:"jwks"            json:"jwks"`
	TrustedIssuers []AuthTrustedIssuer `yaml:"trusted_issuers" toml:"trusted_issuers" json:"trusted_issuers"`
	ClientRegistry []AuthClient        `yaml:"client_registry" toml:"client_registry" json:"client_registry"`
}

// TopicsConfig — `topics.*`.
type TopicsConfig struct {
	CatalogDir   string `yaml:"catalog_dir"    toml:"catalog_dir"    json:"catalog_dir"`
	ValueSetsDir string `yaml:"value_sets_dir" toml:"value_sets_dir" json:"value_sets_dir"`
}

// MLLPListenerEndpointConfig — `mllp_listener.endpoints[]`.
type MLLPListenerEndpointConfig struct {
	Name                string   `yaml:"name"                  toml:"name"                  json:"name"`
	Bind                string   `yaml:"bind"                  toml:"bind"                  json:"bind"`
	TLS                 bool     `yaml:"tls"                   toml:"tls"                   json:"tls"`
	AllowedMessageTypes []string `yaml:"allowed_message_types" toml:"allowed_message_types" json:"allowed_message_types"`
}

// MLLPListenerConfig — `mllp_listener.*`.
type MLLPListenerConfig struct {
	Endpoints []MLLPListenerEndpointConfig `yaml:"endpoints" toml:"endpoints" json:"endpoints"`
}

// AdapterConfig — `adapter.*`.
type AdapterConfig struct {
	ID         string                 `yaml:"id"           toml:"id"           json:"id"`
	VersionPin string                 `yaml:"version_pin"  toml:"version_pin"  json:"version_pin"`
	Config     map[string]interface{} `yaml:"config"       toml:"config"       json:"config"`
}

// CustomChannelConfig — `channels.custom[]`.
type CustomChannelConfig struct {
	ID     string                 `yaml:"id"     toml:"id"     json:"id"`
	Module string                 `yaml:"module" toml:"module" json:"module"`
	Config map[string]interface{} `yaml:"config" toml:"config" json:"config"`
}

// ChannelsConfig — `channels.*`. Built-in channel sub-tree is left as a generic
// map so the validator can walk it against the per-channel manifest schema.
type ChannelsConfig struct {
	RestHook  map[string]interface{} `yaml:"rest_hook" toml:"rest_hook" json:"rest_hook"`
	WebSocket map[string]interface{} `yaml:"websocket" toml:"websocket" json:"websocket"`
	Email     map[string]interface{} `yaml:"email"     toml:"email"     json:"email"`
	Message   map[string]interface{} `yaml:"message"   toml:"message"   json:"message"`
	Custom    []CustomChannelConfig  `yaml:"custom"    toml:"custom"    json:"custom"`
}

// DeliveryRetryBackoffConfig — `delivery.retry.backoff.*`. Initial/Max are
// duration strings.
type DeliveryRetryBackoffConfig struct {
	Kind    string  `yaml:"kind"    toml:"kind"    json:"kind"`
	Initial string  `yaml:"initial" toml:"initial" json:"initial"`
	Max     string  `yaml:"max"     toml:"max"     json:"max"`
	Jitter  float64 `yaml:"jitter"  toml:"jitter"  json:"jitter"`
}

// DeliveryRetryConfig — `delivery.retry.*`.
type DeliveryRetryConfig struct {
	MaxAttempts int                        `yaml:"max_attempts" toml:"max_attempts" json:"max_attempts"`
	Backoff     DeliveryRetryBackoffConfig `yaml:"backoff"      toml:"backoff"      json:"backoff"`
}

// DeliveryHeartbeatConfig — `delivery.heartbeat.*`. All three fields are
// duration strings.
type DeliveryHeartbeatConfig struct {
	DefaultPeriod string `yaml:"default_period" toml:"default_period" json:"default_period"`
	MinPeriod     string `yaml:"min_period"     toml:"min_period"     json:"min_period"`
	MaxPeriod     string `yaml:"max_period"     toml:"max_period"     json:"max_period"`
}

// DeliveryConfig — `delivery.*`.
type DeliveryConfig struct {
	DefaultMaxCount int                     `yaml:"default_max_count" toml:"default_max_count" json:"default_max_count"`
	MaxBatchWait    string                  `yaml:"max_batch_wait"    toml:"max_batch_wait"    json:"max_batch_wait"`
	Retry           DeliveryRetryConfig     `yaml:"retry"             toml:"retry"             json:"retry"`
	Heartbeat       DeliveryHeartbeatConfig `yaml:"heartbeat"         toml:"heartbeat"         json:"heartbeat"`
}

// ObservabilityMetricsConfig — `observability.metrics.*`.
type ObservabilityMetricsConfig struct {
	Bind string `yaml:"bind" toml:"bind" json:"bind"`
}

// ObservabilityTracingConfig — `observability.tracing.*`.
type ObservabilityTracingConfig struct {
	OTLPEndpoint string  `yaml:"otlp_endpoint" toml:"otlp_endpoint" json:"otlp_endpoint"`
	SampleRate   float64 `yaml:"sample_rate"   toml:"sample_rate"   json:"sample_rate"`
}

// ObservabilityAuditLogConfig — `observability.audit_log.*`.
type ObservabilityAuditLogConfig struct {
	Sink     string `yaml:"sink"      toml:"sink"      json:"sink"`
	FilePath string `yaml:"file_path" toml:"file_path" json:"file_path"`
}

// ObservabilityConfig — `observability.*`.
type ObservabilityConfig struct {
	Metrics  ObservabilityMetricsConfig  `yaml:"metrics"   toml:"metrics"   json:"metrics"`
	Tracing  ObservabilityTracingConfig  `yaml:"tracing"   toml:"tracing"   json:"tracing"`
	AuditLog ObservabilityAuditLogConfig `yaml:"audit_log" toml:"audit_log" json:"audit_log"`
}
