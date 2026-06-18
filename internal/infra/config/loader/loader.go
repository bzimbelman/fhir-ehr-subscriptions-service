// Copyright the fhir-subscriptions-foss authors.
// SPDX-License-Identifier: Apache-2.0

// Package loader reads each configuration source — CLI flags, environment
// variables, the config file (YAML or TOML), and built-in defaults — into a
// generic tree representation. The merger reduces those layers to one effective
// config; the validator and resolver run downstream.
//
// Per docs/low-level-design/configuration.md S4 and S5: each layer becomes a
// generic tree (map[string]interface{}); the merger applies precedence later.
package loader

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/BurntSushi/toml"
	"gopkg.in/yaml.v3"
)

// CLIArgs is the parsed command-line surface as documented in
// docs/low-level-design/configuration.md S4.1.
type CLIArgs struct {
	// ConfigPath overrides the default /etc/fhir-subs/config.yaml.
	ConfigPath string
	// LogLevel is shorthand for --set deployment.log_level=<value>.
	LogLevel string
	// CheckOnly: validate-and-exit (used by deployment pipelines).
	CheckOnly bool
	// Sets is the list of --set dotted.key=value overrides.
	Sets []string
}

// ParseCLI returns the CLI layer as a sparse generic tree. Highest precedence;
// applied last by the merger.
func ParseCLI(args CLIArgs) (map[string]interface{}, error) {
	out := map[string]interface{}{}
	apply := func(path, value string) error {
		segs := strings.Split(path, ".")
		for _, s := range segs {
			if s == "" {
				return fmt.Errorf("invalid --set key %q: empty segment", path)
			}
		}
		return setNested(out, segs, value)
	}
	if args.LogLevel != "" {
		if err := apply("deployment.log_level", args.LogLevel); err != nil {
			return nil, err
		}
	}
	for _, s := range args.Sets {
		eq := strings.IndexByte(s, '=')
		if eq <= 0 {
			return nil, fmt.Errorf("invalid --set %q: must be dotted.key=value", s)
		}
		key := s[:eq]
		val := s[eq+1:]
		if err := apply(key, val); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// setNested writes value at the dotted path inside tree, creating intermediate
// maps as needed. Existing leaves are overwritten (CLI is highest precedence).
func setNested(tree map[string]interface{}, segs []string, value interface{}) error {
	cur := tree
	for i, s := range segs {
		if i == len(segs)-1 {
			cur[s] = value
			return nil
		}
		next, ok := cur[s]
		if !ok {
			m := map[string]interface{}{}
			cur[s] = m
			cur = m
			continue
		}
		m, ok := next.(map[string]interface{})
		if !ok {
			return fmt.Errorf("path %q crosses non-map node at %q",
				strings.Join(segs, "."), s)
		}
		cur = m
	}
	return nil
}

// EnvVarFromPath maps a dotted config path to its environment-variable name:
// uppercase + underscores, with array indices kept positional.
//
// Example: auth.trusted_issuers.0.jwks_url -> AUTH_TRUSTED_ISSUERS_0_JWKS_URL.
func EnvVarFromPath(path string) string {
	return strings.ToUpper(strings.ReplaceAll(path, ".", "_"))
}

// ReadEnvForKnownKeys reads only the env vars derived from the known config
// paths and assembles them into a generic tree. Unknown env vars are ignored
// — no silent shadowing.
//
// The known list is generated once at startup by walking the registered schema
// (see schemas/). It is keyed by *config path* (e.g., "auth.trusted_issuers.0.jwks_url"),
// not by env-var name, so multi-word config keys like "trusted_issuers" survive
// the round-trip cleanly.
func ReadEnvForKnownKeys(known []string) map[string]interface{} {
	out := map[string]interface{}{}
	for _, path := range known {
		envName := EnvVarFromPath(path)
		raw, ok := os.LookupEnv(envName)
		if !ok {
			continue
		}
		segs := strings.Split(path, ".")
		_ = setMixedNested(out, segs, raw)
	}
	return out
}

// setMixedNested writes value at segs in tree, where any numeric segment marks
// its parent as a slice. Slices grow to fit the maximum index seen.
func setMixedNested(tree map[string]interface{}, segs []string, value interface{}) error {
	if len(segs) == 0 {
		return errors.New("empty path")
	}
	// Collect a "container ref" idea: at any step we either write into a map
	// key or into a slice index. We model the parent's slot as a setter.

	type slot struct {
		// Either set (parent map + key) OR (parent slice + index).
		mp  map[string]interface{}
		key string
		sl  []interface{}
		idx int
	}
	getSlot := func(s slot) interface{} {
		if s.mp != nil {
			return s.mp[s.key]
		}
		return s.sl[s.idx]
	}
	putSlot := func(s slot, v interface{}) {
		if s.mp != nil {
			s.mp[s.key] = v
			return
		}
		s.sl[s.idx] = v
	}

	// Bootstrap the first slot. The root must be a map; the first segment is
	// either a string key (most common) or numeric (rejected).
	if _, isIdx := numericSeg(segs[0]); isIdx {
		return errors.New("array index at root not supported")
	}
	cur := slot{mp: tree, key: segs[0]}

	for i := 1; i < len(segs); i++ {
		s := segs[i]
		if idx, isIdx := numericSeg(s); isIdx {
			// Materialize cur as a slice of length >= idx+1.
			existing := getSlot(cur)
			sl, ok := existing.([]interface{})
			if !ok {
				sl = []interface{}{}
			}
			for len(sl) <= idx {
				sl = append(sl, map[string]interface{}{})
			}
			putSlot(cur, sl)
			cur = slot{sl: sl, idx: idx}
			continue
		}
		// String descent. Materialize cur as a map.
		existing := getSlot(cur)
		mp, ok := existing.(map[string]interface{})
		if !ok {
			mp = map[string]interface{}{}
		}
		putSlot(cur, mp)
		cur = slot{mp: mp, key: s}
	}
	// Write leaf.
	putSlot(cur, value)
	return nil
}

func numericSeg(s string) (int, bool) {
	n, err := strconv.Atoi(s)
	if err != nil || n < 0 {
		return 0, false
	}
	return n, true
}

// ReadFile reads the config file at path and returns it as a generic tree.
// An empty path or a non-existent path returns an empty tree (the operator
// may rely entirely on env / CLI / defaults). A present-but-unreadable file
// or a parse error is returned as an error.
func ReadFile(path string) (map[string]interface{}, error) {
	if path == "" {
		return map[string]interface{}{}, nil
	}
	data, err := os.ReadFile(path) //nolint:gosec // operator-supplied path
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return map[string]interface{}{}, nil
		}
		return nil, fmt.Errorf("read config file %q: %w", path, err)
	}
	ext := strings.ToLower(filepath.Ext(path))
	switch ext {
	case ".yaml", ".yml":
		var raw map[string]interface{}
		if err := yaml.Unmarshal(data, &raw); err != nil {
			return nil, fmt.Errorf("parse yaml %q: %w", path, err)
		}
		return normalizeTree(raw), nil
	case ".toml":
		var raw map[string]interface{}
		if err := toml.Unmarshal(data, &raw); err != nil {
			return nil, fmt.Errorf("parse toml %q: %w", path, err)
		}
		return normalizeTree(raw), nil
	default:
		return nil, fmt.Errorf("unsupported config file extension %q (want .yaml/.yml/.toml)", ext)
	}
}

// normalizeTree converts yaml.v3's map[interface{}]interface{} sub-trees into
// map[string]interface{}, recursively, so downstream code only deals with one
// shape.
func normalizeTree(in map[string]interface{}) map[string]interface{} {
	out := make(map[string]interface{}, len(in))
	for k, v := range in {
		out[k] = normalizeValue(v)
	}
	return out
}

func normalizeValue(v interface{}) interface{} {
	switch x := v.(type) {
	case map[string]interface{}:
		return normalizeTree(x)
	case map[interface{}]interface{}:
		out := make(map[string]interface{}, len(x))
		for k, vv := range x {
			out[fmt.Sprintf("%v", k)] = normalizeValue(vv)
		}
		return out
	case []interface{}:
		out := make([]interface{}, len(x))
		for i, item := range x {
			out[i] = normalizeValue(item)
		}
		return out
	default:
		return v
	}
}
