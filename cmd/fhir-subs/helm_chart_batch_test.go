// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"os/exec"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// Tests for the helm-chart hygiene batch (#121-#125). All tests use the
// real `helm template` / `helm lint` binaries via the helpers in
// helm_chart_test.go (chartDir, helmTemplate, requireHelm).

// TestHelmChart_Service_MetricsPort_Conditioned (OP #121) — the rendered
// Service MUST omit the `metrics` port when metrics.enabled=false. The
// chart can't honestly expose a metrics endpoint until the binary opens
// the listener, so the operator MUST be able to disable it.
func TestHelmChart_Service_MetricsPort_Conditioned(t *testing.T) {
	requireHelm(t)
	chartPath := chartDir(t)

	// metrics disabled: Service must NOT contain a metrics port.
	rendered := helmTemplate(t, chartPath, []string{
		"--set", "tls.enabled=false",
		"--set", "metrics.enabled=false",
		"--set", "networkPolicy.ingress.api.from[0].podSelector.matchLabels.app=test",
		"--set", "networkPolicy.ingress.mllp.from[0].podSelector.matchLabels.app=test",
		"--set", "image.repository=ghcr.io/example/fhir-ehr-subscriptions-service",
	})
	svc := extractServiceManifest(t, rendered)
	if portsContain(svc, "metrics") {
		t.Fatalf("metrics port must NOT render when metrics.enabled=false; got service:\n%s", svc)
	}

	// metrics enabled (default): Service must contain a metrics port.
	rendered2 := helmTemplate(t, chartPath, []string{
		"--set", "tls.enabled=false",
		"--set", "metrics.enabled=true",
		"--set", "networkPolicy.ingress.api.from[0].podSelector.matchLabels.app=test",
		"--set", "networkPolicy.ingress.mllp.from[0].podSelector.matchLabels.app=test",
		"--set", "image.repository=ghcr.io/example/fhir-ehr-subscriptions-service",
	})
	svc2 := extractServiceManifest(t, rendered2)
	if !portsContain(svc2, "metrics") {
		t.Fatalf("metrics port MUST render when metrics.enabled=true; got service:\n%s", svc2)
	}
}

// TestHelmChart_ServiceMonitor_RequiresMetrics (OP #121) — the
// ServiceMonitor MUST render only when BOTH serviceMonitor.enabled and
// metrics.enabled are true. A ServiceMonitor with no listener silently
// fails scraping.
func TestHelmChart_ServiceMonitor_RequiresMetrics(t *testing.T) {
	requireHelm(t)
	chartPath := chartDir(t)

	// serviceMonitor on, metrics off: ServiceMonitor must NOT render.
	rendered := helmTemplate(t, chartPath, []string{
		"--set", "tls.enabled=false",
		"--set", "metrics.enabled=false",
		"--set", "serviceMonitor.enabled=true",
		"--set", "networkPolicy.ingress.api.from[0].podSelector.matchLabels.app=test",
		"--set", "networkPolicy.ingress.mllp.from[0].podSelector.matchLabels.app=test",
		"--set", "image.repository=ghcr.io/example/fhir-ehr-subscriptions-service",
	})
	if containsKind(rendered, "ServiceMonitor") {
		t.Fatalf("ServiceMonitor MUST NOT render when metrics.enabled=false; rendered:\n%s", rendered)
	}

	// both on: ServiceMonitor renders.
	rendered2 := helmTemplate(t, chartPath, []string{
		"--set", "tls.enabled=false",
		"--set", "metrics.enabled=true",
		"--set", "serviceMonitor.enabled=true",
		"--set", "networkPolicy.ingress.api.from[0].podSelector.matchLabels.app=test",
		"--set", "networkPolicy.ingress.mllp.from[0].podSelector.matchLabels.app=test",
		"--set", "image.repository=ghcr.io/example/fhir-ehr-subscriptions-service",
	})
	if !containsKind(rendered2, "ServiceMonitor") {
		t.Fatalf("ServiceMonitor MUST render when both serviceMonitor.enabled and metrics.enabled are true; rendered:\n%s", rendered2)
	}
}

// TestHelmChart_NetworkPolicy_RequiresIngressFrom (OP #122) — the chart
// MUST refuse to render with empty `from` lists for the API and MLLP
// ingress rules. Empty `from` allows ingress namespace-wide, which
// silently undoes the NetworkPolicy hardening promise.
func TestHelmChart_NetworkPolicy_RequiresIngressFrom(t *testing.T) {
	requireHelm(t)
	chartPath := chartDir(t)

	// Empty from defaults: chart MUST fail.
	if err := helmTemplateExpectFail(t, chartPath, []string{
		"--set", "tls.enabled=false",
		"--set", "image.repository=ghcr.io/example/fhir-ehr-subscriptions-service",
	}); err != nil {
		t.Fatalf("helm template MUST fail when networkPolicy.ingress.{api,mllp}.from default to empty; %v", err)
	}

	// Operator-supplied podSelectors: chart renders, NetworkPolicy has
	// non-empty `from` for both api and mllp ingress rules.
	rendered := helmTemplate(t, chartPath, []string{
		"--set", "tls.enabled=false",
		"--set", "networkPolicy.ingress.api.from[0].podSelector.matchLabels.app=allowed",
		"--set", "networkPolicy.ingress.mllp.from[0].podSelector.matchLabels.app=allowed",
		"--set", "image.repository=ghcr.io/example/fhir-ehr-subscriptions-service",
	})
	np := extractKindManifest(t, rendered, "NetworkPolicy")
	if np == "" {
		t.Fatalf("NetworkPolicy did not render with operator-supplied selectors:\n%s", rendered)
	}
	apiCount, mllpCount := countIngressFrom(t, np)
	if apiCount == 0 {
		t.Fatalf("NetworkPolicy ingress for api MUST have non-empty from; got 0 entries:\n%s", np)
	}
	if mllpCount == 0 {
		t.Fatalf("NetworkPolicy ingress for mllp MUST have non-empty from; got 0 entries:\n%s", np)
	}
}

// TestHelmChart_Image_PlaceholderFailFast (OP #123) — when the operator
// leaves image.repository at the placeholder value, helm template MUST
// fail with a clear error. We refuse to silently install a chart that
// points at an unknown personal repo.
func TestHelmChart_Image_PlaceholderFailFast(t *testing.T) {
	requireHelm(t)
	chartPath := chartDir(t)

	if err := helmTemplateExpectFail(t, chartPath, []string{
		"--set", "tls.enabled=false",
		"--set", "networkPolicy.ingress.api.from[0].podSelector.matchLabels.app=test",
		"--set", "networkPolicy.ingress.mllp.from[0].podSelector.matchLabels.app=test",
		// Default values.yaml repository is the placeholder; do not override.
	}); err != nil {
		t.Fatalf("helm template MUST fail when image.repository is left at the placeholder; %v", err)
	}

	// Operator-supplied repo: chart renders.
	rendered := helmTemplate(t, chartPath, []string{
		"--set", "tls.enabled=false",
		"--set", "networkPolicy.ingress.api.from[0].podSelector.matchLabels.app=test",
		"--set", "networkPolicy.ingress.mllp.from[0].podSelector.matchLabels.app=test",
		"--set", "image.repository=ghcr.io/myorg/fhir-ehr-subscriptions-service",
	})
	if !strings.Contains(rendered, "ghcr.io/myorg/fhir-ehr-subscriptions-service") {
		t.Fatalf("operator-supplied image.repository did not appear in rendered chart:\n%s", rendered)
	}
}

// TestHelmChart_ReplicaCount_HPACollision (OP #124) — when
// autoscaling.enabled is true, the rendered Deployment MUST omit the
// .spec.replicas field, AND helm install NOTES MUST surface a warning
// when both replicaCount and autoscaling are set.
func TestHelmChart_ReplicaCount_HPACollision(t *testing.T) {
	requireHelm(t)
	chartPath := chartDir(t)

	// HPA on (default): Deployment.spec.replicas absent.
	rendered := helmTemplate(t, chartPath, []string{
		"--set", "tls.enabled=false",
		"--set", "autoscaling.enabled=true",
		"--set", "replicaCount=2",
		"--set", "networkPolicy.ingress.api.from[0].podSelector.matchLabels.app=test",
		"--set", "networkPolicy.ingress.mllp.from[0].podSelector.matchLabels.app=test",
		"--set", "image.repository=ghcr.io/example/fhir-ehr-subscriptions-service",
	})
	dep := extractKindManifest(t, rendered, "Deployment")
	if deploymentHasReplicas(t, dep) {
		t.Fatalf("Deployment.spec.replicas MUST be absent when autoscaling.enabled=true; got:\n%s", dep)
	}

	// HPA off: Deployment.spec.replicas present.
	rendered2 := helmTemplate(t, chartPath, []string{
		"--set", "tls.enabled=false",
		"--set", "autoscaling.enabled=false",
		"--set", "replicaCount=3",
		"--set", "networkPolicy.ingress.api.from[0].podSelector.matchLabels.app=test",
		"--set", "networkPolicy.ingress.mllp.from[0].podSelector.matchLabels.app=test",
		"--set", "image.repository=ghcr.io/example/fhir-ehr-subscriptions-service",
	})
	dep2 := extractKindManifest(t, rendered2, "Deployment")
	if !deploymentHasReplicas(t, dep2) {
		t.Fatalf("Deployment.spec.replicas MUST be present when autoscaling.enabled=false; got:\n%s", dep2)
	}

	// NOTES.txt MUST warn on the collision when both are set. Use
	// `helm template --show-only` to grab the NOTES output via stderr is
	// not reliable; instead we render and let the `helm install --dry-run`
	// fall back to checking for the warning string in the rendered chart's
	// NOTES.txt path. Helm exposes NOTES via the `notes` field of a
	// release — rendered manifests don't include NOTES, so we drive the
	// check via `helm install --dry-run` which prints NOTES.
	out := helmInstallDryRun(t, chartPath, []string{
		"--set", "tls.enabled=false",
		"--set", "autoscaling.enabled=true",
		"--set", "replicaCount=2",
		"--set", "networkPolicy.ingress.api.from[0].podSelector.matchLabels.app=test",
		"--set", "networkPolicy.ingress.mllp.from[0].podSelector.matchLabels.app=test",
		"--set", "image.repository=ghcr.io/example/fhir-ehr-subscriptions-service",
	})
	if !strings.Contains(out, "replicaCount") || !strings.Contains(strings.ToLower(out), "autoscaling") {
		t.Fatalf("helm install NOTES MUST warn about replicaCount/autoscaling collision; got:\n%s", out)
	}
}

// TestHelmChart_ConfigCheckInit_RenamedFromMigrationInit (OP #125) — the
// init container that runs --check-config is named `config-check` (it
// ALWAYS was), and the values.yaml key MUST follow suit. The old
// `migrationInit` key is misleading: it doesn't run migrations.
func TestHelmChart_ConfigCheckInit_RenamedFromMigrationInit(t *testing.T) {
	requireHelm(t)
	chartPath := chartDir(t)

	// Render with new key set to enabled.
	rendered := helmTemplate(t, chartPath, []string{
		"--set", "tls.enabled=false",
		"--set", "configCheck.enabled=true",
		"--set", "networkPolicy.ingress.api.from[0].podSelector.matchLabels.app=test",
		"--set", "networkPolicy.ingress.mllp.from[0].podSelector.matchLabels.app=test",
		"--set", "image.repository=ghcr.io/example/fhir-ehr-subscriptions-service",
	})
	dep := extractKindManifest(t, rendered, "Deployment")
	if !strings.Contains(dep, "name: config-check") {
		t.Fatalf("Deployment MUST contain init container named `config-check`; got:\n%s", dep)
	}

	// Render with new key set to disabled: init container must vanish.
	rendered2 := helmTemplate(t, chartPath, []string{
		"--set", "tls.enabled=false",
		"--set", "configCheck.enabled=false",
		"--set", "networkPolicy.ingress.api.from[0].podSelector.matchLabels.app=test",
		"--set", "networkPolicy.ingress.mllp.from[0].podSelector.matchLabels.app=test",
		"--set", "image.repository=ghcr.io/example/fhir-ehr-subscriptions-service",
	})
	dep2 := extractKindManifest(t, rendered2, "Deployment")
	if strings.Contains(dep2, "name: config-check") {
		t.Fatalf("Deployment MUST NOT contain init container `config-check` when configCheck.enabled=false; got:\n%s", dep2)
	}
}

// helmTemplateExpectFail runs `helm template` and returns nil if the
// command failed (the desired RED for fail-fast tests). It returns a
// non-nil error if helm template succeeded — meaning the fail-fast
// guard is not in place.
func helmTemplateExpectFail(t *testing.T, chartPath string, extra []string) error {
	t.Helper()
	args := append([]string{"template", "testrel", chartPath}, extra...)
	cmd := exec.Command("helm", args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil // good: chart refused to render
	}
	return &unexpectedSuccessError{rendered: stdout.String()}
}

type unexpectedSuccessError struct{ rendered string }

func (e *unexpectedSuccessError) Error() string {
	return "helm template succeeded but a fail-fast guard was expected"
}

// helmInstallDryRun runs `helm install --dry-run` and returns combined
// stdout, which includes the NOTES.txt block.
func helmInstallDryRun(t *testing.T, chartPath string, extra []string) string {
	t.Helper()
	args := append([]string{"install", "testrel", chartPath, "--dry-run=client", "--namespace", "default"}, extra...)
	cmd := exec.Command("helm", args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("helm install --dry-run failed: %v\nstderr:\n%s", err, stderr.String())
	}
	return stdout.String()
}

// extractServiceManifest returns the rendered Service manifest body.
func extractServiceManifest(t *testing.T, rendered string) string {
	t.Helper()
	return extractKindManifest(t, rendered, "Service")
}

// extractKindManifest scans the rendered multi-doc YAML and returns the
// first document of the requested kind, re-encoded as YAML.
func extractKindManifest(t *testing.T, rendered string, kind string) string {
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
		if doc["kind"] != kind {
			continue
		}
		// Skip the ServiceMonitor when looking for Service (kind is
		// distinct, but be explicit).
		out, err := yaml.Marshal(doc)
		if err != nil {
			t.Fatalf("re-marshal %s: %v", kind, err)
		}
		return string(out)
	}
	return ""
}

// containsKind reports whether the rendered chart includes a manifest of
// the given kind.
func containsKind(rendered string, kind string) bool {
	dec := yaml.NewDecoder(strings.NewReader(rendered))
	for {
		var doc map[string]any
		if err := dec.Decode(&doc); err != nil {
			break
		}
		if doc == nil {
			continue
		}
		if doc["kind"] == kind {
			return true
		}
	}
	return false
}

// portsContain reports whether the Service manifest text contains a port
// with `name: <wanted>`.
func portsContain(svc string, wanted string) bool {
	var doc map[string]any
	if err := yaml.Unmarshal([]byte(svc), &doc); err != nil {
		return false
	}
	spec, _ := doc["spec"].(map[string]any)
	ports, _ := spec["ports"].([]any)
	for _, p := range ports {
		pm, _ := p.(map[string]any)
		if pm["name"] == wanted {
			return true
		}
	}
	return false
}

// countIngressFrom returns (apiFromLen, mllpFromLen) by walking the
// NetworkPolicy.spec.ingress rules and matching the rule that targets
// the api / mllp port.
func countIngressFrom(t *testing.T, np string) (int, int) {
	t.Helper()
	var doc map[string]any
	if err := yaml.Unmarshal([]byte(np), &doc); err != nil {
		t.Fatalf("yaml.Unmarshal NetworkPolicy: %v", err)
	}
	spec, _ := doc["spec"].(map[string]any)
	rules, _ := spec["ingress"].([]any)
	apiLen, mllpLen := 0, 0
	for _, r := range rules {
		rm, _ := r.(map[string]any)
		ports, _ := rm["ports"].([]any)
		var portName string
		for _, p := range ports {
			pm, _ := p.(map[string]any)
			if v, ok := pm["port"]; ok {
				if s, ok := v.(string); ok {
					portName = s
					break
				}
			}
		}
		from, _ := rm["from"].([]any)
		switch portName {
		case "api":
			apiLen = len(from)
		case "mllp":
			mllpLen = len(from)
		}
	}
	return apiLen, mllpLen
}

// deploymentHasReplicas reports whether the Deployment has spec.replicas set.
func deploymentHasReplicas(t *testing.T, dep string) bool {
	t.Helper()
	if dep == "" {
		t.Fatalf("empty Deployment manifest")
	}
	var doc map[string]any
	if err := yaml.Unmarshal([]byte(dep), &doc); err != nil {
		t.Fatalf("yaml.Unmarshal Deployment: %v", err)
	}
	spec, _ := doc["spec"].(map[string]any)
	_, ok := spec["replicas"]
	return ok
}
