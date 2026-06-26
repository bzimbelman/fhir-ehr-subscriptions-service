{{/*
Expand the name of the chart.
*/}}
{{- define "subscription-service.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Create a default fully qualified app name. We truncate at 63 chars because
some Kubernetes name fields are limited to this.
*/}}
{{- define "subscription-service.fullname" -}}
{{- if .Values.fullnameOverride }}
{{- .Values.fullnameOverride | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- $name := default .Chart.Name .Values.nameOverride }}
{{- if contains $name .Release.Name }}
{{- .Release.Name | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- printf "%s-%s" .Release.Name $name | trunc 63 | trimSuffix "-" }}
{{- end }}
{{- end }}
{{- end }}

{{/*
Chart label.
*/}}
{{- define "subscription-service.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Common labels applied to every object.
*/}}
{{- define "subscription-service.labels" -}}
helm.sh/chart: {{ include "subscription-service.chart" . }}
{{ include "subscription-service.selectorLabels" . }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
app.kubernetes.io/part-of: subscription-service
{{- end }}

{{/*
Selector labels (release-scoped). Per-component labels add app.kubernetes.io/component.
*/}}
{{- define "subscription-service.selectorLabels" -}}
app.kubernetes.io/name: {{ include "subscription-service.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{/*
Per-component fullname helpers: <release>-<component>. We don't share the
chart fullname because templates need stable, predictable names (e.g.
SPRING_DATASOURCE_URL points at the postgres service by DNS name).
*/}}
{{- define "subscription-service.hapi.fullname" -}}
{{- printf "%s-hapi" .Release.Name | trunc 63 | trimSuffix "-" }}
{{- end }}

{{- define "subscription-service.matchbox.fullname" -}}
{{- printf "%s-matchbox" .Release.Name | trunc 63 | trimSuffix "-" }}
{{- end }}

{{- define "subscription-service.interfaceEngine.fullname" -}}
{{- printf "%s-interface-engine" .Release.Name | trunc 63 | trimSuffix "-" }}
{{- end }}

{{- define "subscription-service.postgres.fullname" -}}
{{- printf "%s-postgres" .Release.Name | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
External Postgres validation (ticket #416). When externalPostgres.enabled
is true, both host and passwordSecret must be set — otherwise rendering
would produce a Deployment that immediately CrashLoops on JDBC connect.
Invoked from a standalone validation template so it fires at every render.
*/}}
{{- define "subscription-service.externalPostgres.validate" -}}
{{- if .Values.externalPostgres.enabled -}}
  {{- if not .Values.externalPostgres.host -}}
    {{- fail "externalPostgres.enabled is true but externalPostgres.host is empty. Set it to your managed Postgres hostname." -}}
  {{- end -}}
  {{- if not .Values.externalPostgres.passwordSecret -}}
    {{- fail "externalPostgres.enabled is true but externalPostgres.passwordSecret is empty. Pre-create a Secret in the release namespace and reference it here." -}}
  {{- end -}}
{{- end -}}
{{- end }}

{{/*
Per-workload pod-level securityContext (ticket #420). Deep-merges the chart-level
`podSecurityContext` with any per-workload override under `<workload>.podSecurityContext`.
Usage:
  securityContext:
    {{- include "subscription-service.podSecurityContext" (dict "ctx" . "workload" "hapi") | nindent 8 }}

The chart-level value is the BASE; the workload override (if any) wins for
the keys it sets. Mergeoverwrite mutates the second arg, so we copy the
chart-level map into a fresh dict first.
*/}}
{{- define "subscription-service.podSecurityContext" -}}
{{- $base := deepCopy (default (dict) .ctx.Values.podSecurityContext) -}}
{{- $workload := index .ctx.Values .workload -}}
{{- $override := default (dict) (and $workload (index $workload "podSecurityContext")) -}}
{{- toYaml (mergeOverwrite $base $override) -}}
{{- end }}

{{/*
Per-workload container-level securityContext (ticket #420). Same deep-merge as
the pod-level helper above, sourced from `securityContext` (chart-level) and
`<workload>.securityContext` (per-workload override).
*/}}
{{- define "subscription-service.securityContext" -}}
{{- $base := deepCopy (default (dict) .ctx.Values.securityContext) -}}
{{- $workload := index .ctx.Values .workload -}}
{{- $override := default (dict) (and $workload (index $workload "securityContext")) -}}
{{- toYaml (mergeOverwrite $base $override) -}}
{{- end }}

{{/*
The fetch-igs initContainer runs `curlimages/curl:8.10.1`, which has
USER 100:101 (`curl_user:curl_group`). Override the chart-level UID/GID so
the kubelet doesn't refuse the spec under PSA `restricted` (which requires
the container-level UID to match the pod-level when both are set).
*/}}
{{- define "subscription-service.igFetcherSecurityContext" -}}
allowPrivilegeEscalation: false
readOnlyRootFilesystem: false
capabilities:
  drop: ["ALL"]
runAsNonRoot: true
runAsUser: 100
runAsGroup: 101
seccompProfile:
  type: RuntimeDefault
{{- end }}

{{/*
Resolved JWKS URL: explicit override wins; otherwise derive from the issuer.
Returns empty string when auth is off.
*/}}
{{- define "subscription-service.auth.jwksUrl" -}}
{{- if .Values.featureToggles.auth.enabled -}}
{{- if .Values.featureToggles.auth.jwksUrl -}}
{{- .Values.featureToggles.auth.jwksUrl -}}
{{- else if .Values.featureToggles.auth.issuer -}}
{{- printf "%s/protocol/openid-connect/certs" (trimSuffix "/" .Values.featureToggles.auth.issuer) -}}
{{- end -}}
{{- end -}}
{{- end }}
