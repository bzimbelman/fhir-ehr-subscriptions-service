// Copyright the fhir-subscriptions-foss authors.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"errors"
	"fmt"
	"os"
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

	// Extra captures anything not modeled above so a stricter loader can
	// claim it later without this thin loader rejecting valid configs.
	Extra map[string]any `yaml:",inline"`
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

// HTTPConfig models server.http.* fields.
type HTTPConfig struct {
	Bind     string    `yaml:"bind"`
	Insecure bool      `yaml:"insecure"`
	TLS      TLSConfig `yaml:"tls"`
}

// TLSConfig models server.http.tls.* fields. Real TLS wiring lands later.
type TLSConfig struct {
	CertFile string `yaml:"cert_file"`
	KeyFile  string `yaml:"key_file"`
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

	if len(strings.TrimSpace(string(body))) > 0 {
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

	return cfg, nil
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
		}
	}
	if c.Lifecycle.ShutdownGracePeriod <= 0 {
		problems = append(problems, "lifecycle.shutdown_grace_period must be positive")
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
				return fmt.Errorf("--set %s=%q: %w", key, val, err)
			}
			cfg.Server.HTTP.Insecure = b
		case "server.http.tls.cert_file":
			cfg.Server.HTTP.TLS.CertFile = val
		case "server.http.tls.key_file":
			cfg.Server.HTTP.TLS.KeyFile = val
		case "lifecycle.shutdown_grace_period":
			d, err := time.ParseDuration(val)
			if err != nil {
				return fmt.Errorf("--set %s=%q: %w", key, val, err)
			}
			cfg.Lifecycle.ShutdownGracePeriod = d
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
		return false, fmt.Errorf("invalid bool %q", v)
	}
}
