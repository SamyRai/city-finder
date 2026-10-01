{{/* City-finder chart helpers. */}}

{{/* Chart name, overridable via nameOverride. */}}
{{- define "city-finder.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{/* Fully qualified app name: release-name-chart-name, deduplicated. */}}
{{- define "city-finder.fullname" -}}
{{- if .Values.fullnameOverride -}}
{{- .Values.fullnameOverride | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- $name := default .Chart.Name .Values.nameOverride -}}
{{- if contains $name .Release.Name -}}
{{- .Release.Name | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- printf "%s-%s" .Release.Name $name | trunc 63 | trimSuffix "-" -}}
{{- end -}}
{{- end -}}
{{- end -}}

{{/* Chart name and version as used by the chart label. */}}
{{- define "city-finder.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{/* Common labels. */}}
{{- define "city-finder.labels" -}}
helm.sh/chart: {{ include "city-finder.chart" . }}
{{ include "city-finder.selectorLabels" . }}
{{- with .Chart.AppVersion }}
app.kubernetes.io/version: {{ . | quote }}
{{- end }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end -}}

{{/* Selector labels. */}}
{{- define "city-finder.selectorLabels" -}}
app.kubernetes.io/name: {{ include "city-finder.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end -}}

{{/* Service account name to use. */}}
{{- define "city-finder.serviceAccountName" -}}
{{- if .Values.serviceAccount.create -}}
{{- default (include "city-finder.fullname" .) .Values.serviceAccount.name -}}
{{- else -}}
{{- default "default" .Values.serviceAccount.name -}}
{{- end -}}
{{- end -}}

{{/* Name of the datasets PVC the chart creates (empty when using an existing claim or emptyDir). */}}
{{- define "city-finder.datasetsPVCName" -}}
{{- default (printf "%s-datasets" (include "city-finder.fullname" .)) .Values.persistence.existingClaim -}}
{{- end -}}

{{/* ArgoCD sync-wave annotation; pass the wave number, e.g. include "city-finder.syncWave" (-2). */}}
{{- define "city-finder.syncWave" -}}
argocd.argoproj.io/sync-wave: {{ . | quote }}
{{- end -}}
