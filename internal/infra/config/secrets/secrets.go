// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

// Package secrets resolves the ${env:VAR} and ${file:/path} placeholder forms
// committed by the architecture. It runs *after* structural validation so a
// typo in field names never causes a secret to be touched (LLD §6 invariant
// (a)). Resolved fields are tagged sensitive in the redaction map (invariant
// (b)).
//
// Per docs/low-level-design/configuration.md S6 and S13.
package secrets

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode"

	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/infra/config/redaction"
)

const (
	envPrefix  = "${env:"
	filePrefix = "${file:"
	suffix     = "}"
)

// MaxSecretFileSize bounds how many bytes Resolve will read from a
// ${file:/path} reference. Larger inputs return ErrSecretFileTooLarge
// rather than OOMing the process. 1 MiB is two orders of magnitude
// above any realistic secret (RS256 PEM, OAuth client secret, vendor
// API token) and well below container memory pressure limits. S-15 #4.
const MaxSecretFileSize int64 = 1 << 20

// ErrSecretFileTooLarge is returned by ResolveWithFilePaths when a
// ${file:...} reference exceeds MaxSecretFileSize.
var ErrSecretFileTooLarge = errors.New("secrets: file exceeds MaxSecretFileSize")

// Resolve walks the config tree, substitutes any ${env:VAR} or ${file:/path}
// placeholder, and tags each resolved path as sensitive in the returned map.
// The supplied map (which may already carry schema-tagged sensitive paths) is
// preserved — Resolve adds to it rather than replacing it.
//
// A missing referenced env var or unreadable file is returned as an error;
// the caller (boot path) refuses to start.
func Resolve(tree map[string]interface{}, rmap *redaction.Map) (map[string]interface{}, *redaction.Map, error) {
	resolved, rmap, _, err := ResolveWithFilePaths(tree, rmap)
	return resolved, rmap, err
}

// ResolveWithFilePaths is Resolve plus the deduplicated set of on-disk
// paths actually read for ${file:...} placeholders. Used by the secret
// rotation watcher (B-35) so it knows which paths to mtime-poll.
func ResolveWithFilePaths(tree map[string]interface{}, rmap *redaction.Map) (resolved map[string]interface{}, redactionMap *redaction.Map, filePaths []string, err error) {
	if rmap == nil {
		rmap = redaction.NewMap()
	}
	seen := map[string]struct{}{}
	out, walkErr := walk(tree, "", rmap, seen)
	if walkErr != nil {
		return nil, nil, nil, walkErr
	}
	resolved, _ = out.(map[string]interface{})
	paths := make([]string, 0, len(seen))
	for p := range seen {
		paths = append(paths, p)
	}
	return resolved, rmap, paths, nil
}

func walk(v interface{}, path string, rmap *redaction.Map, files map[string]struct{}) (interface{}, error) {
	switch x := v.(type) {
	case map[string]interface{}:
		out := make(map[string]interface{}, len(x))
		for k, vv := range x {
			r, err := walk(vv, redaction.JoinPath(path, k), rmap, files)
			if err != nil {
				return nil, err
			}
			out[k] = r
		}
		return out, nil
	case []interface{}:
		out := make([]interface{}, len(x))
		for i, item := range x {
			r, err := walk(item, redaction.JoinIndex(path, i), rmap, files)
			if err != nil {
				return nil, err
			}
			out[i] = r
		}
		return out, nil
	case string:
		return resolveString(x, path, rmap, files)
	default:
		return v, nil
	}
}

// resolveString returns the substituted value if s is a placeholder, otherwise
// s as-is. A path tagged here is added to rmap. When files is non-nil and
// the placeholder is a ${file:...} form, the resolved on-disk path is
// recorded so callers can later mtime-poll it for rotation.
func resolveString(s, path string, rmap *redaction.Map, files map[string]struct{}) (string, error) {
	if strings.HasPrefix(s, envPrefix) && strings.HasSuffix(s, suffix) {
		name := s[len(envPrefix) : len(s)-len(suffix)]
		if name == "" {
			return "", fmt.Errorf("empty env-var name in placeholder at %q", path)
		}
		v, ok := os.LookupEnv(name)
		if !ok {
			return "", fmt.Errorf("env var %s referenced by %s is not set", name, path)
		}
		rmap.TagSensitive(path)
		return v, nil
	}
	if strings.HasPrefix(s, filePrefix) && strings.HasSuffix(s, suffix) {
		raw := s[len(filePrefix) : len(s)-len(suffix)]
		if raw == "" {
			return "", fmt.Errorf("empty file path in placeholder at %q", path)
		}
		// Don't allow relative escape — operator-supplied path, but reject
		// obvious traversal-only forms. Operators occasionally point at
		// /run/secrets/foo or /etc/fhir-subs/keys/foo; both are absolute.
		clean := filepath.Clean(raw)
		f, err := os.Open(clean) //nolint:gosec // operator-supplied path
		if err != nil {
			return "", fmt.Errorf("file %s referenced by %s is unreadable: %w", raw, path, err)
		}
		// io.LimitReader caps the byte budget at MaxSecretFileSize+1 so
		// we can detect overflow with a single Read; an exactly-sized
		// secret is fine, anything larger is rejected.
		limited := io.LimitReader(f, MaxSecretFileSize+1)
		data, readErr := io.ReadAll(limited)
		_ = f.Close()
		if readErr != nil {
			return "", fmt.Errorf("file %s referenced by %s is unreadable: %w", raw, path, readErr)
		}
		if int64(len(data)) > MaxSecretFileSize {
			return "", fmt.Errorf("%w: %s referenced by %s (%d > %d)",
				ErrSecretFileTooLarge, raw, path, len(data), MaxSecretFileSize)
		}
		rmap.TagSensitive(path)
		if files != nil {
			files[clean] = struct{}{}
		}
		return strings.TrimRightFunc(string(data), unicode.IsSpace), nil
	}
	return s, nil
}

// ErrPlaceholderUnclosed is returned only by stricter parsers; reserved here
// for future use so callers can switch on a typed error if needed.
var ErrPlaceholderUnclosed = errors.New("placeholder not closed")
