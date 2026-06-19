// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

package handlers

import (
	"net/http"
	"time"

	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/adapter/supervisor"
)

// SupervisorStatusReader is the narrow read-only seam the
// /admin/supervisor/status endpoint needs from the production wiring.
// Implementations return one supervisor.Status per supervised adapter
// worker (HL7 processor, matcher, submatcher, scheduler) so operators
// can triage a stuck worker without ssh-ing into the pod (story #99).
type SupervisorStatusReader interface {
	Status() []supervisor.Status
}

const auditActionAdminSupervisorStatusList = "admin.supervisor.status.list"

// listSupervisorStatus serves GET /admin/supervisor/status. Mounted by
// RegisterAdminRoutes only when Deps.SupervisorStatus is non-nil so a
// probe cannot learn whether the supervised pipeline is wired (story
// #99).
//
// Each row carries adapterId / state / restartCount / lastError so the
// triage screen can spot a worker stuck in a backoff loop. lastErrorAt
// is rendered in RFC 3339; an empty/zero LastErrorAt is omitted so the
// JSON stays compact when nothing has gone wrong.
func (a *adminServer) listSupervisorStatus(w http.ResponseWriter, r *http.Request) {
	rows := a.deps.SupervisorStatus.Status()
	out := make([]map[string]any, 0, len(rows))
	for _, st := range rows {
		row := map[string]any{
			"adapterId":    st.AdapterID,
			"state":        st.State.String(),
			"restartCount": st.RestartCount,
			"stopped":      st.Stopped,
		}
		if st.LastError != nil {
			row["lastError"] = st.LastError.Error()
		}
		if !st.LastErrorAt.IsZero() {
			row["lastErrorAt"] = st.LastErrorAt.UTC().Format(time.RFC3339Nano)
		}
		out = append(out, row)
	}
	a.emitAdminAudit(r, auditActionAdminSupervisorStatusList, "supervisor.status", auditOutcomeSuccess)
	writeAdminJSON(w, http.StatusOK, map[string]any{
		"items": out,
		"total": len(out),
	})
}
