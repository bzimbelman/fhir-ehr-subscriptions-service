// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

// Command demo-publisher emits HL7 v2 messages over MLLP from a YAML
// catalog. It is the operator-facing companion to cmd/fhir-subs for the
// subscription-sidecar demo described in docs/subscription-sidecar-demo.md.
//
// Example:
//
//	demo-publisher --addr 127.0.0.1:6000 --catalog demo/scenarios/labs.yaml
//
// The catalog format is documented in demo/scenarios/labs.yaml and the
// supportedTemplates set in catalog.go.
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
	if err := mainE(os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "demo-publisher:", err)
		os.Exit(1)
	}
}

// mainE is the testable entry point. Tests can call it with a fake argv
// and assert on the writer's contents instead of forking the binary.
func mainE(argv []string, stdout, stderr *os.File) error {
	fs := flag.NewFlagSet("demo-publisher", flag.ContinueOnError)
	fs.SetOutput(stderr)
	addr := fs.String("addr", "127.0.0.1:6000", "MLLP listener address (host:port)")
	catalogPath := fs.String("catalog", "", "path to YAML catalog (required)")
	noColor := fs.Bool("no-color", false, "disable ANSI color output")
	if err := fs.Parse(argv); err != nil {
		return err
	}
	if *catalogPath == "" {
		return fmt.Errorf("--catalog is required")
	}

	f, err := os.Open(*catalogPath)
	if err != nil {
		return fmt.Errorf("open catalog: %w", err)
	}
	defer f.Close()
	cat, err := loadCatalog(f)
	if err != nil {
		return fmt.Errorf("catalog %s: %w", *catalogPath, err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	pub := &publisher{
		addr:    *addr,
		out:     stdout,
		noColor: *noColor,
	}
	fmt.Fprintf(stdout, "demo-publisher: %d messages → %s\n", len(cat.Messages), *addr)
	return pub.run(ctx, cat)
}
