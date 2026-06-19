// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

//go:build e2e_realstack

package realstack

import (
	"context"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// ListProjectResources returns every container, network, and volume
// the docker engine reports as belonging to the given compose project.
// Used by TestRealStack_TeardownIsClean to assert Close left no
// residue. Each returned string is "<kind>:<name>".
//
// Implementation: shells out to `docker ps -a`, `docker network ls`,
// and `docker volume ls` with `--filter label=com.docker.compose.project=<project>`,
// which docker-compose stamps on every resource it creates.
func ListProjectResources(t *testing.T, project string) []string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	var out []string
	for _, q := range []struct {
		kind string
		args []string
	}{
		{"container", []string{"ps", "-a", "--filter", "label=com.docker.compose.project=" + project, "--format", "{{.Names}}"}},
		{"network", []string{"network", "ls", "--filter", "label=com.docker.compose.project=" + project, "--format", "{{.Name}}"}},
		{"volume", []string{"volume", "ls", "--filter", "label=com.docker.compose.project=" + project, "--format", "{{.Name}}"}},
	} {
		raw, err := exec.CommandContext(ctx, "docker", q.args...).Output()
		if err != nil {
			t.Logf("[realstack] docker %s: %v", strings.Join(q.args, " "), err)
			continue
		}
		for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			out = append(out, q.kind+":"+line)
		}
	}
	return out
}
