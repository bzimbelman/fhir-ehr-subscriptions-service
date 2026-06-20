// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"time"
)

// defaultHealthcheckURL is the URL the bridge's distroless image hits
// when docker invokes its in-container healthcheck. 127.0.0.1:8081
// matches cmd/fhir-subs/config.go's ProbeBind default; /readyz is the
// kubelet-style readiness endpoint mounted on that listener.
const defaultHealthcheckURL = "http://127.0.0.1:8081/readyz"

// runHealthcheckSubcommand performs an HTTP GET against the configured
// probe URL. It exists so the demo bridge image (gcr.io/distroless/...)
// has an in-container healthcheck without shipping curl/wget. Docker
// invokes it as `["CMD", "/fhir-subs", "healthcheck"]`; non-zero exit
// flips the container to "unhealthy" and unblocks `depends_on:
// condition: service_healthy` for any chained service.
//
// OP #230 — the demo bridge previously had no healthcheck because the
// distroless runtime has no shell. This subcommand is the
// distroless-friendly equivalent.
func runHealthcheckSubcommand(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("fhir-subs healthcheck", flag.ContinueOnError)
	fs.SetOutput(stderr)

	var (
		url     string
		timeout time.Duration
	)
	fs.StringVar(&url, "url", defaultHealthcheckURL, "probe URL to GET (default: bridge's :8081/readyz)")
	fs.DurationVar(&timeout, "timeout", 5*time.Second, "request timeout")

	fs.Usage = func() {
		fmt.Fprintf(stderr, "Usage: fhir-subs healthcheck [--url URL] [--timeout DURATION]\n\n")
		fmt.Fprintf(stderr, "Performs an HTTP GET against the bridge's probe listener and exits\n")
		fmt.Fprintf(stderr, "0 on a 2xx response, 1 otherwise. Designed for use as a docker\n")
		fmt.Fprintf(stderr, "HEALTHCHECK on the bridge's distroless image, which has no shell.\n\n")
		fs.PrintDefaults()
	}

	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, http.NoBody)
	if err != nil {
		fmt.Fprintf(stderr, "healthcheck: build request %s: %v\n", url, err)
		return 1
	}

	client := &http.Client{Timeout: timeout}
	resp, err := client.Do(req)
	if err != nil {
		fmt.Fprintf(stderr, "healthcheck: GET %s: %v\n", url, err)
		return 1
	}
	defer resp.Body.Close()
	// Drain so the connection can be reused on a retry; bounded so a
	// misbehaving server cannot make the healthcheck hang.
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64*1024))

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		fmt.Fprintf(stderr, "healthcheck: GET %s: status %d\n", url, resp.StatusCode)
		return 1
	}
	return 0
}
