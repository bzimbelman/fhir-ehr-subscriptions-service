// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// TestHelmChart_ConfigMapParsesIntoConfig is the binary contract test
// for OP #120: every key the chart emits in its default config.contents
// MUST be modeled by Config (no Extra fallthrough). It runs the real
// `helm template` against deploy/helm/fhir-subs, extracts the rendered
// ConfigMap's config.yaml, and parses it with the same loader the
// binary uses. After parse, Config.Extra MUST be empty — any orphan key
// is a chart/binary contract drift the operator would never notice
// because yaml.v3 silently routes it to the inline catch-all.
func TestHelmChart_ConfigMapParsesIntoConfig(t *testing.T) {
	requireHelm(t)

	chartPath := chartDir(t)
	rendered := helmTemplate(t, chartPath, []string{
		// Disable probes/TLS/externalSecrets just to keep the rendered
		// manifest small; none of those affect config.contents.
		"--set", "tls.enabled=false",
		"--set", "probes.liveness.enabled=false",
		"--set", "probes.readiness.enabled=false",
		"--set", "probes.startup.enabled=false",
	})

	configYAML := extractConfigMapConfigYAML(t, rendered)
	if strings.TrimSpace(configYAML) == "" {
		t.Fatalf("rendered ConfigMap has empty config.yaml; got:\n%s", rendered)
	}

	tmp := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(tmp, []byte(configYAML), 0o600); err != nil {
		t.Fatalf("write tmp config: %v", err)
	}
	cfg, err := loadConfig(tmp)
	if err != nil {
		t.Fatalf("loadConfig of helm-rendered config: %v\n--- config.yaml ---\n%s", err, configYAML)
	}
	if len(cfg.Extra) != 0 {
		// Render orphan keys for a precise diagnostic.
		var keys []string
		for k := range cfg.Extra {
			keys = append(keys, k)
		}
		t.Fatalf("helm-rendered config.yaml contains keys not modeled in Config (orphans): %v\n--- config.yaml ---\n%s", keys, configYAML)
	}
}

// TestHelmChart_TopicCatalogConfigMap_Renders is the binary contract
// test for OP #115: when topicCatalog is supplied via values, the chart
// MUST project it into a ConfigMap (or equivalent volume source) and
// the deployment MUST mount it at /etc/fhir-subs/topics — the same path
// Config.Topics.CatalogDir defaults to. Today the chart has no
// topicCatalog block at all, so this test fails until #115 wires it.
func TestHelmChart_TopicCatalogConfigMap_Renders(t *testing.T) {
	requireHelm(t)

	chartPath := chartDir(t)
	// Write a values file so we can pass real JSON containing commas
	// (helm --set treats `,` as a list separator, which mangles JSON).
	valuesFile := filepath.Join(t.TempDir(), "topic-values.yaml")
	valuesBody := `tls:
  enabled: false
probes:
  liveness:
    enabled: false
  readiness:
    enabled: false
  startup:
    enabled: false
topicCatalog:
  files:
    demo.json: |
      {"resourceType":"SubscriptionTopic","status":"active","url":"http://example.org/topic/demo"}
`
	if err := os.WriteFile(valuesFile, []byte(valuesBody), 0o600); err != nil {
		t.Fatalf("write values: %v", err)
	}
	rendered := helmTemplate(t, chartPath, []string{"--values", valuesFile})

	if !strings.Contains(rendered, "fhir-subs-topics") {
		t.Fatalf("expected a topics ConfigMap (suffix 'fhir-subs-topics') in rendered chart; not found:\n%s", rendered)
	}
	if !strings.Contains(rendered, "/etc/fhir-subs/topics") {
		t.Fatalf("expected deployment to mount topics at /etc/fhir-subs/topics; not found in rendered chart:\n%s", rendered)
	}
	// The mounted file must be the demo.json key the operator supplied.
	if !strings.Contains(rendered, "demo.json") {
		t.Fatalf("expected the demo.json topic key from --set to land in the rendered ConfigMap; not found:\n%s", rendered)
	}
}

// TestHelmChart_ProbePort_MatchesBinaryListener is the binary contract
// test for OP #118: the helm chart's probe port MUST match the binary's
// listener config. Today values.yaml emits server.http.probe_bind: ":8081"
// but Config has no ProbeBind field — it's silently swallowed by Extra,
// so the binary never opens :8081 and pods never go Ready. This test
// asserts the rendered config.yaml contains a probe_bind that maps to
// the helm probe targetPort and that Config.Server.HTTP.ProbeBind reads it.
func TestHelmChart_ProbePort_MatchesBinaryListener(t *testing.T) {
	requireHelm(t)

	chartPath := chartDir(t)
	rendered := helmTemplate(t, chartPath, []string{
		"--set", "tls.enabled=false",
	})
	configYAML := extractConfigMapConfigYAML(t, rendered)

	tmp := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(tmp, []byte(configYAML), 0o600); err != nil {
		t.Fatalf("write tmp config: %v", err)
	}
	cfg, err := loadConfig(tmp)
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	// ProbeBind must be parsed into the typed Config (no Extra).
	if cfg.Server.HTTP.ProbeBind == "" {
		t.Fatalf("Config.Server.HTTP.ProbeBind is empty after loading helm-rendered config; chart says probe_bind but binary doesn't model it. config.yaml:\n%s", configYAML)
	}
	if !strings.Contains(cfg.Server.HTTP.ProbeBind, "8081") {
		t.Fatalf("Config.Server.HTTP.ProbeBind=%q does not match the helm probe containerPort 8081", cfg.Server.HTTP.ProbeBind)
	}
}

// TestHelmChart_HelmLint runs `helm lint` so any chart YAML defect
// (template errors, undefined values references, malformed manifests)
// is caught at build time. Helm's own static checker.
func TestHelmChart_HelmLint(t *testing.T) {
	requireHelm(t)

	chartPath := chartDir(t)
	cmd := exec.Command("helm", "lint", chartPath, "--strict")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("helm lint failed: %v\n%s", err, out)
	}
}

// helmTemplate runs `helm template testrel <chartPath> <extraArgs...>`
// and returns the combined stdout. Fails the test on non-zero exit.
func helmTemplate(t *testing.T, chartPath string, extra []string) string {
	t.Helper()
	args := append([]string{"template", "testrel", chartPath}, extra...)
	cmd := exec.Command("helm", args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("helm template failed: %v\nstderr:\n%s", err, stderr.String())
	}
	return stdout.String()
}

// extractConfigMapConfigYAML walks the rendered manifest looking for
// the ConfigMap that owns config.yaml and returns its body.
func extractConfigMapConfigYAML(t *testing.T, rendered string) string {
	t.Helper()
	dec := yaml.NewDecoder(strings.NewReader(rendered))
	for {
		var doc map[string]any
		if err := dec.Decode(&doc); err != nil {
			break
		}
		if doc == nil {
			continue
		}
		if doc["kind"] != "ConfigMap" {
			continue
		}
		md, _ := doc["metadata"].(map[string]any)
		name, _ := md["name"].(string)
		if !strings.HasSuffix(name, "-fhir-subs-config") && !strings.HasSuffix(name, "-config") {
			continue
		}
		data, _ := doc["data"].(map[string]any)
		if data == nil {
			continue
		}
		body, _ := data["config.yaml"].(string)
		if body != "" {
			return body
		}
	}
	t.Fatalf("no ConfigMap with config.yaml found in rendered chart")
	return ""
}

// chartDir returns the absolute path to deploy/helm/fhir-subs by walking
// up from the test's working directory until the chart manifest is found.
func chartDir(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	dir := wd
	for i := 0; i < 6; i++ {
		candidate := filepath.Join(dir, "deploy", "helm", "fhir-subs", "Chart.yaml")
		if _, err := os.Stat(candidate); err == nil {
			return filepath.Dir(candidate)
		}
		dir = filepath.Dir(dir)
	}
	t.Fatalf("could not locate deploy/helm/fhir-subs from %s", wd)
	return ""
}

// requireHelm skips the test when the helm binary is not on PATH; the
// chart contract tests cannot run without it.
func requireHelm(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("helm"); err != nil {
		t.Skip("helm not on PATH; skipping chart contract test")
	}
}
