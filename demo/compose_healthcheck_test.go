// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

package demo

import (
	"os"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// OP #230: the demo bridge service MUST declare a healthcheck. The
// production-readiness honesty audit (supplement-2 finding 153) called
// out the missing healthcheck — without one, dependent services in the
// compose chain cannot gate on `condition: service_healthy`. The
// distroless image has no shell, so the healthcheck shells out to the
// bridge binary's built-in `healthcheck` subcommand.
func TestDemoCompose_BridgeDeclaresHealthcheck(t *testing.T) {
	t.Parallel()
	body, err := os.ReadFile("docker-compose.yml")
	if err != nil {
		t.Fatalf("read docker-compose.yml: %v", err)
	}

	var doc struct {
		Services map[string]struct {
			Healthcheck *struct {
				Test []string `yaml:"test"`
			} `yaml:"healthcheck"`
		} `yaml:"services"`
	}
	if err := yaml.Unmarshal(body, &doc); err != nil {
		t.Fatalf("parse: %v", err)
	}

	bridge, ok := doc.Services["bridge"]
	if !ok {
		t.Fatalf("docker-compose.yml has no `bridge` service")
	}
	if bridge.Healthcheck == nil {
		t.Fatalf("OP #230: services.bridge must declare a healthcheck (distroless-friendly: invoke the bridge binary's `healthcheck` subcommand)")
	}
	if len(bridge.Healthcheck.Test) == 0 {
		t.Fatalf("services.bridge.healthcheck.test is empty")
	}
	joined := strings.Join(bridge.Healthcheck.Test, " ")
	if !strings.Contains(joined, "/fhir-subs") {
		t.Errorf("services.bridge.healthcheck.test must invoke the bridge binary; got %v", bridge.Healthcheck.Test)
	}
	if !strings.Contains(joined, "healthcheck") {
		t.Errorf("services.bridge.healthcheck.test must invoke the `healthcheck` subcommand; got %v", bridge.Healthcheck.Test)
	}
}

// OP #230: now that the bridge has a healthcheck, demo-subscriber must
// gate on service_healthy (not service_started) so the subscribe POST
// does not fire before the bridge's /readyz returns 200. Without this
// gate the healthcheck addition is half a fix.
func TestDemoCompose_DemoSubscriberWaitsOnBridgeHealthy(t *testing.T) {
	t.Parallel()
	body, err := os.ReadFile("docker-compose.yml")
	if err != nil {
		t.Fatalf("read docker-compose.yml: %v", err)
	}

	var doc struct {
		Services map[string]struct {
			DependsOn map[string]struct {
				Condition string `yaml:"condition"`
			} `yaml:"depends_on"`
		} `yaml:"services"`
	}
	if err := yaml.Unmarshal(body, &doc); err != nil {
		t.Fatalf("parse: %v", err)
	}

	sub, ok := doc.Services["demo-subscriber"]
	if !ok {
		t.Fatalf("no `demo-subscriber` service")
	}
	if sub.DependsOn == nil {
		t.Fatalf("demo-subscriber has no depends_on block")
	}
	bridge, ok := sub.DependsOn["bridge"]
	if !ok {
		t.Fatalf("demo-subscriber.depends_on must reference `bridge`")
	}
	if bridge.Condition != "service_healthy" {
		t.Errorf("demo-subscriber.depends_on.bridge.condition: want service_healthy, got %q", bridge.Condition)
	}
}
