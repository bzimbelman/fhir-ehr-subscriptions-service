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

{{- define "subscription-service.ipf.fullname" -}}
{{- printf "%s-ipf" .Release.Name | trunc 63 | trimSuffix "-" }}
{{- end }}

{{- define "subscription-service.postgres.fullname" -}}
{{- printf "%s-postgres" .Release.Name | trunc 63 | trimSuffix "-" }}
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
