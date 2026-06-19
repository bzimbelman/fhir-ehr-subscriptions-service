// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

// Command demo-subscriber is the subscriber half of the
// subscription-sidecar demo (docs/subscription-sidecar-demo.md, gap 4).
//
// It POSTs a rest-hook Subscription to the bridge, hosts a local HTTP
// listener that accepts the bridge's notification deliveries, and
// pretty-prints each Bundle to stdout, color-coded by topic.
//
// Two auth paths:
//
//	./demo-subscriber --bridge https://bridge --token <jwt> ...
//	./demo-subscriber --bridge https://bridge --client-id X --private-key key.pem --kid k --token-url ...
package main

import (
	"context"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
)

func main() {
	if err := run(os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "demo-subscriber:", err)
		os.Exit(1)
	}
}

// waitForReadyz polls url every 500ms until a 2xx response is observed
// or deadline elapses. The polling honours ctx so SIGTERM /
// docker-stop unblocks the wait. Used by the --wait-for-readyz flag
// to ride out a slow bridge boot in compose.
func waitForReadyz(ctx context.Context, url string, deadline time.Duration) error {
	pollCtx, cancel := context.WithTimeout(ctx, deadline)
	defer cancel()
	client := &http.Client{Timeout: 2 * time.Second}
	for {
		req, _ := http.NewRequestWithContext(pollCtx, http.MethodGet, url, http.NoBody)
		resp, err := client.Do(req)
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode >= 200 && resp.StatusCode < 300 {
				return nil
			}
		}
		select {
		case <-pollCtx.Done():
			return fmt.Errorf("timed out waiting for %s: last err=%v", url, err)
		case <-time.After(500 * time.Millisecond):
		}
	}
}

type cliFlags struct {
	bridge      string
	listen      string
	advertise   string
	topic       string
	filter      string
	channelType string

	token       string
	tokenURL    string
	clientID    string
	scope       string
	privateKey  string
	kid         string
	noColor     bool
	pretty      bool
	subscribeTO time.Duration

	// waitForReadyz, when non-zero, polls a readyz URL before
	// subscribing. Lets the demo subscriber survive a slow bridge
	// boot (e.g. running under docker compose where the bridge needs
	// to migrate the demo Postgres before /readyz flips to 200).
	waitForReadyz time.Duration
	// readyzURL is the URL to poll when waitForReadyz > 0. Defaults
	// to <bridge>/readyz, but the bridge serves probes on a separate
	// port from the FHIR API in production
	// (cmd/fhir-subs/config.go ProbeBind defaults to :8081), so a
	// caller using compose must override this.
	readyzURL string
}

func parseFlags(args []string) (cliFlags, error) {
	fs := flag.NewFlagSet("demo-subscriber", flag.ContinueOnError)
	var f cliFlags
	fs.StringVar(&f.bridge, "bridge", "", "bridge base URL (e.g. http://localhost:8080)")
	fs.StringVar(&f.listen, "listen", "127.0.0.1:0", "host:port to bind the local rest-hook listener")
	fs.StringVar(&f.advertise, "advertise", "", "URL the bridge should POST to (defaults to http://<listen>); useful when listening on 0.0.0.0")
	fs.StringVar(&f.topic, "topic", "", "Subscription topic URL")
	fs.StringVar(&f.filter, "filter", "", "filter in name=value form (e.g. patient=ABC123)")
	fs.StringVar(&f.channelType, "channel-type", "rest-hook", "Subscription channel type")
	fs.StringVar(&f.token, "token", "", "static bearer token to use; if set, JWT mint flags are ignored")
	fs.StringVar(&f.tokenURL, "token-url", "", "bridge token endpoint URL (default: <bridge>/token)")
	fs.StringVar(&f.clientID, "client-id", "", "SMART Backend Services client_id (used when --token is empty)")
	fs.StringVar(&f.scope, "scope", "system/Subscription.cruds", "OAuth scope to request when minting a token")
	fs.StringVar(&f.privateKey, "private-key", "", "path to a PEM-encoded RSA private key for client_assertion signing")
	fs.StringVar(&f.kid, "kid", "", "JWS `kid` header for the client_assertion")
	fs.BoolVar(&f.noColor, "no-color", false, "disable ANSI color (kept for backward compat; prefer NO_COLOR env)")
	fs.BoolVar(&f.pretty, "pretty", true, "pretty-print colored, emoji-tagged transcript; --pretty=false emits JSON Lines")
	fs.DurationVar(&f.subscribeTO, "subscribe-timeout", 10*time.Second, "timeout for the POST /Subscription request")
	fs.DurationVar(&f.waitForReadyz, "wait-for-readyz", 0, "if >0, poll the readyz URL until 2xx or this duration elapses before subscribing (default 0 = no wait)")
	fs.StringVar(&f.readyzURL, "readyz-url", "", "URL to poll for readiness (default <bridge>/readyz). Override when probes are served on a different port than the FHIR API.")

	if err := fs.Parse(args); err != nil {
		return f, err
	}
	if f.bridge == "" {
		return f, errors.New("--bridge is required")
	}
	if f.topic == "" {
		return f, errors.New("--topic is required")
	}
	if f.token == "" {
		// JWT-mint path requires client-id + private-key.
		if f.clientID == "" || f.privateKey == "" {
			return f, errors.New("either --token or both --client-id and --private-key are required")
		}
	}
	return f, nil
}

// loadPrivateKey reads an RSA private key from a PEM file (PKCS1 or PKCS8).
func loadPrivateKey(path string) (*rsa.PrivateKey, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	block, _ := pem.Decode(data)
	if block == nil {
		return nil, errors.New("no PEM block found")
	}
	if key, kErr := x509.ParsePKCS1PrivateKey(block.Bytes); kErr == nil {
		return key, nil
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("parse PKCS8: %w", err)
	}
	rsaKey, ok := parsed.(*rsa.PrivateKey)
	if !ok {
		return nil, errors.New("private key is not RSA")
	}
	return rsaKey, nil
}

func run(args []string, stdout *os.File) error {
	f, err := parseFlags(args)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	rcv := newReceiver(stdout, f.pretty, f.noColor)
	logger := rcv.printer

	// 1. Bring up the listener first so the activation handshake the
	//    bridge fires after POST /Subscription has somewhere to land.
	ln, err := net.Listen("tcp", f.listen)
	if err != nil {
		return fmt.Errorf("listen %s: %w", f.listen, err)
	}
	srv := &http.Server{
		Handler:           rcv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}
	listenerErr := make(chan error, 1)
	go func() {
		serveErr := srv.Serve(ln)
		if serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
			listenerErr <- serveErr
			return
		}
		listenerErr <- nil
	}()
	logger.printInfo("listening on http://%s", ln.Addr().String())

	// 2. Resolve the URL the bridge will POST to. CLI override wins;
	//    otherwise advertise <listen>/hook/<sub-id>.
	subID := fmt.Sprintf("demo-%d", time.Now().UnixNano())
	advertise := f.advertise
	if advertise == "" {
		advertise = "http://" + ln.Addr().String()
	}
	endpoint := strings.TrimRight(advertise, "/") + "/hook/" + subID

	// 3. Mint or load token.
	bearer := f.token
	if bearer == "" {
		key, kErr := loadPrivateKey(f.privateKey)
		if kErr != nil {
			return fmt.Errorf("load private key %s: %w", f.privateKey, kErr)
		}
		tokenURL := f.tokenURL
		if tokenURL == "" {
			tokenURL = strings.TrimRight(f.bridge, "/") + "/token"
		}
		mintCtx, cancel := context.WithTimeout(ctx, f.subscribeTO)
		var mErr error
		bearer, mErr = mintToken(mintCtx, MintConfig{
			TokenURL:   tokenURL,
			ClientID:   f.clientID,
			Scope:      f.scope,
			PrivateKey: key,
			Kid:        f.kid,
		})
		cancel()
		if mErr != nil {
			return fmt.Errorf("mint token: %w", mErr)
		}
		logger.printInfo("minted access token (%d-byte JWT)", len(bearer))
	}

	// 3.5. Optionally wait for the bridge's /readyz to flip to 2xx so
	//      a slow boot (compose: bridge migrates Postgres before
	//      Subscription API serves) does not race the subscribe POST.
	if f.waitForReadyz > 0 {
		readyzURL := f.readyzURL
		if readyzURL == "" {
			readyzURL = strings.TrimRight(f.bridge, "/") + "/readyz"
		}
		if rerr := waitForReadyz(ctx, readyzURL, f.waitForReadyz); rerr != nil {
			return fmt.Errorf("wait for bridge readyz: %w", rerr)
		}
		logger.printInfo("bridge readyz observed at %s", readyzURL)
	}

	// 4. POST the Subscription.
	subCtx, cancel := context.WithTimeout(ctx, f.subscribeTO)
	id, err := postSubscription(subCtx, SubscribeConfig{
		BridgeBaseURL: f.bridge,
		Token:         bearer,
		Topic:         f.topic,
		Filter:        f.filter,
		ChannelType:   f.channelType,
		Endpoint:      endpoint,
	})
	cancel()
	if err != nil {
		return fmt.Errorf("subscribe: %w", err)
	}
	logger.printInfo("subscribed: id=%s topic=%s endpoint=%s", id, f.topic, endpoint)

	// 5. Block on signals; shut the listener cleanly so in-flight
	//    deliveries finish writing their lines to stdout.
	select {
	case <-ctx.Done():
	case lErr := <-listenerErr:
		if lErr != nil {
			return fmt.Errorf("listener: %w", lErr)
		}
	}
	shutdownCtx, sCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer sCancel()
	_ = srv.Shutdown(shutdownCtx)
	logger.printInfo("shut down")
	return nil
}
