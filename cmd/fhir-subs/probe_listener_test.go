// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

func unmarshalYAMLForTest(cfg *Config, body []byte) error {
	dec := yaml.NewDecoder(strings.NewReader(string(body)))
	return dec.Decode(cfg)
}

// TestRun_ProbeListener_ServesHealthzOnSeparatePort is the binary
// contract test for OP #118: the binary MUST open a SECOND HTTP
// listener on cfg.Server.HTTP.ProbeBind that serves /healthz and
// /readyz unauthenticated, separate from the main listener on
// cfg.Server.HTTP.Bind. Today probes are mounted on the same mux as
// the auth-protected production router and there is only one listener,
// so the helm chart's `port: probes -> 8081` never receives traffic
// and pods never go Ready.
//
// The test wires the smallest possible Config (insecure HTTP main, no
// DB), runs `run` in a goroutine, curls /healthz on the probe address,
// and asserts 200. It then asserts the main address does NOT serve
// /healthz at all (probes must not leak into the auth-protected mux —
// today they share a mux and a buggy auth wrap could 401 a kubelet
// probe).
func TestRun_ProbeListener_ServesHealthzOnSeparatePort(t *testing.T) {
	mainBind := "127.0.0.1:" + freeProbePort(t)
	probeBind := "127.0.0.1:" + freeProbePort(t)

	cfg := &Config{
		Deployment: DeploymentConfig{
			FacilityID: "probe-test",
			LogLevel:   "error",
			LogFormat:  "json",
			Mode:       DeploymentModeProbeOnly,
		},
		Adapter: AdapterConfig{ID: "default"},
		Server: ServerConfig{
			HTTP: HTTPConfig{
				Bind:      mainBind,
				ProbeBind: probeBind,
				Insecure:  true,
			},
		},
		Lifecycle: LifecycleConfig{ShutdownGracePeriod: 2 * time.Second},
	}
	cfg.Server.HTTP.applyTimeoutDefaults()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan error, 1)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		done <- run(ctx, cfg, io.Discard)
	}()

	probeURL := "http://" + probeBind + "/healthz"
	if err := waitForHTTP200(probeURL, 5*time.Second); err != nil {
		cancel()
		<-done
		t.Fatalf("probe listener did not serve /healthz on %s: %v", probeBind, err)
	}

	// Probes must not leak into the main mux. We don't assert auth
	// here (the test starts insecure), but we assert /healthz is NOT
	// mounted on the main listener so future auth wraps cannot 401 a
	// kubelet probe by accident.
	if status, _ := httpGetStatus("http://"+mainBind+"/healthz", 1*time.Second); status == 200 {
		cancel()
		<-done
		t.Fatalf("main listener at %s also served /healthz; probes must live on a separate listener", mainBind)
	}

	cancel()
	wg.Wait()
	if err := <-done; err != nil && err != context.Canceled {
		t.Logf("run exited with: %v", err)
	}
}

// TestHTTPConfig_ProbeBind_ParsesFromYAML asserts the typed Config has
// a ProbeBind field on HTTPConfig that captures server.http.probe_bind.
// Today the field doesn't exist, so probe_bind from helm-rendered YAML
// silently lands in Config.Extra and the binary never opens a probe
// listener. RED until #118's struct change lands.
func TestHTTPConfig_ProbeBind_ParsesFromYAML(t *testing.T) {
	cfg := defaultConfig()
	yaml := []byte(`server:
  http:
    bind: ":8443"
    probe_bind: ":8081"
`)
	if err := unmarshalYAMLForTest(cfg, yaml); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if cfg.Server.HTTP.ProbeBind != ":8081" {
		t.Fatalf("HTTPConfig.ProbeBind=%q, want %q", cfg.Server.HTTP.ProbeBind, ":8081")
	}
	// OP #205: Config.Extra removed. With KnownFields(true), a
	// successfully-unmarshalled cfg implies every top-level key
	// resolved to a modeled field — there is no inline-catch-all to
	// inspect.
}

func freeProbePort(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("freePort: %v", err)
	}
	defer l.Close()
	_, port, _ := net.SplitHostPort(l.Addr().String())
	return port
}

func waitForHTTP200(url string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	var lastErr error
	for time.Now().Before(deadline) {
		status, err := httpGetStatus(url, 500*time.Millisecond)
		if err == nil && status == 200 {
			return nil
		}
		lastErr = err
		time.Sleep(50 * time.Millisecond)
	}
	if lastErr == nil {
		return fmt.Errorf("never returned 200 within %s", timeout)
	}
	return lastErr
}

func httpGetStatus(url string, timeout time.Duration) (int, error) {
	c := &http.Client{Timeout: timeout}
	resp, err := c.Get(url)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	return resp.StatusCode, nil
}
