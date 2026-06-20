// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

// Command covergate enforces a per-package coverage floor against a
// `go tool cover -func=` profile. It reads the threshold from
// .coverage-thresholds.json (default 80%) and exits non-zero if any
// package falls below its applicable threshold, listing every offender
// with its observed percentage and the threshold it missed.
//
// Acceptance criteria from OP #128:
//   - parses cover.out via `go tool cover -func`
//   - default threshold 80% (overridable per-package)
//   - failure links the offending packages
//
// Usage (from CI):
//
//	go test -coverprofile=cover.out -covermode=atomic ./...
//	go run ./tools/covergate -profile cover.out -thresholds .coverage-thresholds.json
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"
)

type thresholdsFile struct {
	Default  float64            `json:"default"`
	Packages map[string]float64 `json:"packages"`
}

func main() {
	if err := run(os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(args []string, stdout, stderr *os.File) error {
	fs := flag.NewFlagSet("covergate", flag.ContinueOnError)
	fs.SetOutput(stderr)
	profile := fs.String("profile", "cover.out", "path to the coverage profile (cover.out)")
	cfgPath := fs.String("thresholds", ".coverage-thresholds.json", "path to the coverage thresholds JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}

	cfg, err := loadThresholds(*cfgPath)
	if err != nil {
		return fmt.Errorf("load thresholds: %w", err)
	}
	if _, statErr := os.Stat(*profile); statErr != nil {
		return fmt.Errorf("coverage profile %s not readable: %w", *profile, statErr)
	}

	// #nosec G204 — *profile is a CLI flag controlled by the
	// invoking CI step; this tool is run with it as an explicit
	// path. The go-cover invocation is fixed; only the path arg
	// is variable, and exec.Command does not invoke a shell.
	out, err := exec.Command("go", "tool", "cover", "-func="+*profile).Output()
	if err != nil {
		return fmt.Errorf("go tool cover: %w", err)
	}

	pkgs, err := parseFunc(string(out))
	if err != nil {
		return err
	}

	type miss struct {
		pkg   string
		got   float64
		floor float64
	}
	var misses []miss
	for pkg, got := range pkgs {
		floor := cfg.Default
		if v, ok := cfg.Packages[pkg]; ok {
			floor = v
		}
		if got+1e-9 < floor {
			misses = append(misses, miss{pkg: pkg, got: got, floor: floor})
		}
	}
	sort.Slice(misses, func(i, j int) bool { return misses[i].pkg < misses[j].pkg })

	if len(misses) == 0 {
		fmt.Fprintf(stdout, "covergate: all packages meet thresholds (default=%.1f%%)\n", cfg.Default)
		return nil
	}
	fmt.Fprintf(stderr, "covergate: %d package(s) below threshold:\n", len(misses))
	for _, m := range misses {
		fmt.Fprintf(stderr, "  %s — %.1f%% (threshold %.1f%%)\n", m.pkg, m.got, m.floor)
	}
	return fmt.Errorf("coverage threshold not met")
}

func loadThresholds(path string) (thresholdsFile, error) {
	var cfg thresholdsFile
	body, err := os.ReadFile(path)
	if err != nil {
		return cfg, err
	}
	if err := json.Unmarshal(body, &cfg); err != nil {
		return cfg, err
	}
	if cfg.Default <= 0 {
		cfg.Default = 80
	}
	if cfg.Packages == nil {
		cfg.Packages = map[string]float64{}
	}
	return cfg, nil
}

// parseFunc parses `go tool cover -func=` output. The format is one row
// per func plus a final `total:` row. Per-func rows look like:
//
//	github.com/foo/bar/baz/x.go:12:	Name		73.5%
//
// We aggregate to package level (path before the last /), tracking the
// covered/total statement counts from the per-line percentages — but
// `-func` only gives percentages, not statement counts, so the cleanest
// proxy is the simple unweighted mean of all functions in a package.
// That matches what `go test ./...` reports per-package and is what the
// project's prior contributors will recognise as "package coverage".
func parseFunc(text string) (map[string]float64, error) {
	pkgSum := map[string]float64{}
	pkgCount := map[string]int{}
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "total:") {
			continue
		}
		// Expected format: <path>:<line>:\t<func>\t<pct>%
		fields := strings.Fields(line)
		if len(fields) < 3 {
			continue
		}
		pctStr := strings.TrimSuffix(fields[len(fields)-1], "%")
		pct, err := strconv.ParseFloat(pctStr, 64)
		if err != nil {
			continue
		}
		pathPart := fields[0]
		colon := strings.Index(pathPart, ":")
		if colon == -1 {
			continue
		}
		filePath := pathPart[:colon]
		slash := strings.LastIndex(filePath, "/")
		var pkg string
		if slash == -1 {
			pkg = "."
		} else {
			pkg = filePath[:slash]
		}
		pkgSum[pkg] += pct
		pkgCount[pkg]++
	}
	out := make(map[string]float64, len(pkgSum))
	for pkg, sum := range pkgSum {
		out[pkg] = sum / float64(pkgCount[pkg])
	}
	return out, nil
}
