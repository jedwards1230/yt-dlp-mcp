{{/*
Chart name, overridable with nameOverride.
*/}}
{{- define "yt-dlp-mcp.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Fully qualified app name. fullnameOverride wins; otherwise the release name,
suffixed with the chart name unless it already contains it (release
"yt-dlp-mcp" -> "yt-dlp-mcp").
*/}}
{{- define "yt-dlp-mcp.fullname" -}}
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

{{- define "yt-dlp-mcp.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{- end }}

{{- define "yt-dlp-mcp.selectorLabels" -}}
app.kubernetes.io/name: {{ include "yt-dlp-mcp.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{- define "yt-dlp-mcp.labels" -}}
helm.sh/chart: {{ include "yt-dlp-mcp.chart" . }}
{{ include "yt-dlp-mcp.selectorLabels" . }}
app.kubernetes.io/version: {{ (.Values.image.tag | default .Chart.AppVersion) | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}

{{/*
Secret holding the bearer token: the existing one, or the chart-created one
when auth.token is set. Empty when auth is off.
*/}}
{{- define "yt-dlp-mcp.authSecretName" -}}
{{- if .Values.auth.existingSecret }}
{{- .Values.auth.existingSecret }}
{{- else if .Values.auth.token }}
{{- printf "%s-auth" (include "yt-dlp-mcp.fullname" .) }}
{{- end }}
{{- end }}
