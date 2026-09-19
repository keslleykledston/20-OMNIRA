{{/*
Expand the name of the chart.
*/}}
{{- define "omnira.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Create a default fully qualified app name.
*/}}
{{- define "omnira.fullname" -}}
omnira
{{- end }}

{{/*
Create chart name and version as used by the chart label.
*/}}
{{- define "omnira.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.AppVersion | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Common labels
*/}}
{{- define "omnira.labels" -}}
helm.sh/chart: {{ include "omnira.chart" . }}
{{ include "omnira.selectorLabels" . }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}

{{/*
Selector labels
*/}}
{{- define "omnira.selectorLabels" -}}
app.kubernetes.io/name: {{ include "omnira.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{/*
API selector labels
*/}}
{{- define "omnira.api.selectorLabels" -}}
app.kubernetes.io/name: {{ include "omnira.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/component: api
{{- end }}

{{/*
Worker selector labels
*/}}
{{- define "omnira.worker.selectorLabels" -}}
app.kubernetes.io/name: {{ include "omnira.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/component: worker
{{- end }}

{{/*
Create the name of the service account to use
*/}}
{{- define "omnira.serviceAccountName" -}}
{{- if .Values.rbac.enabled }}
{{- .Values.rbac.serviceAccountName | default (include "omnira.fullname" .) }}
{{- else }}
default
{{- end }}
{{- end }}
