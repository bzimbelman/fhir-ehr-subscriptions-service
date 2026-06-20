// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"flag"
	"fmt"
	"io"
	"os"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("docs-lint", flag.ContinueOnError)
	fs.SetOutput(stderr)
	root := fs.String("root", ".", "repo root (containing go.mod, docs/, deploy/, internal/)")
	fs.Usage = func() {
		fmt.Fprintf(stderr, "Usage of docs-lint:\n")
		fmt.Fprintf(stderr, "  docs-lint [--root PATH]\n\n")
		fmt.Fprintf(stderr, "Walks docs/ and asserts every CLI subcommand, metric name, port reference,\n")
		fmt.Fprintf(stderr, "and mkdocs nav entry resolves to a real binary symbol. Exits non-zero on any\n")
		fmt.Fprintf(stderr, "drift.\n")
	}
	if err := fs.Parse(args); err != nil {
		return 2
	}

	findings, err := Lint(*root)
	if err != nil {
		fmt.Fprintln(stderr, "error:", err)
		return 2
	}
	if len(findings) == 0 {
		fmt.Fprintln(stdout, "docs-lint: no findings — docs and code agree")
		return 0
	}
	for _, f := range findings {
		fmt.Fprintln(stdout, f.String())
	}
	fmt.Fprintf(stderr, "docs-lint: %d findings\n", len(findings))
	return 1
}
