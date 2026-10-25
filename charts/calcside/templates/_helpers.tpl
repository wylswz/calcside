{{/*
Expand the name of the chart.
*/}}
{{- define "calcside.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{/*
Create a default fully qualified app name.
*/}}
{{- define "calcside.fullname" -}}
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

{{- define "calcside.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "calcside.selectorLabels" -}}
app.kubernetes.io/name: {{ include "calcside.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end -}}

{{- define "calcside.labels" -}}
helm.sh/chart: {{ include "calcside.chart" . }}
{{ include "calcside.selectorLabels" . }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end -}}

{{- define "calcside.api.selectorLabels" -}}
{{ include "calcside.selectorLabels" . }}
app.kubernetes.io/component: api
{{- end -}}

{{- define "calcside.api.labels" -}}
{{ include "calcside.labels" . }}
app.kubernetes.io/component: api
{{- end -}}

{{- define "calcside.worker.selectorLabels" -}}
{{ include "calcside.selectorLabels" . }}
app.kubernetes.io/component: worker
{{- end -}}

{{- define "calcside.worker.labels" -}}
{{ include "calcside.labels" . }}
app.kubernetes.io/component: worker
{{- end -}}

{{- define "calcside.serviceAccountName" -}}
{{- if .Values.serviceAccount.create -}}
{{- default (include "calcside.fullname" .) .Values.serviceAccount.name -}}
{{- else -}}
{{- default "default" .Values.serviceAccount.name -}}
{{- end -}}
{{- end -}}

{{- define "calcside.image" -}}
{{- printf "%s:%s" .Values.image.repository (default .Chart.AppVersion .Values.image.tag) -}}
{{- end -}}

{{- define "calcside.migrationImage" -}}
{{- printf "%s:%s" .Values.migrations.image.repository (default .Chart.AppVersion .Values.migrations.image.tag) -}}
{{- end -}}

{{- define "calcside.secretName" -}}
{{- default (printf "%s-secrets" (include "calcside.fullname" .)) .Values.secrets.existingSecret -}}
{{- end -}}

{{- define "calcside.postgresSecretName" -}}
{{- if .Values.database.postgres.existingSecret -}}
{{- .Values.database.postgres.existingSecret -}}
{{- else -}}
{{- printf "%s-postgres" (include "calcside.fullname" .) -}}
{{- end -}}
{{- end -}}

{{- define "calcside.postgresSecretKey" -}}
{{- default "dsn" .Values.database.postgres.existingSecretKey -}}
{{- end -}}

{{/*
API -> worker address list. Worker node IDs are StatefulSet pod names.
*/}}
{{- define "calcside.workerNodes" -}}
{{- $fullname := include "calcside.fullname" . -}}
{{- $service := printf "%s-worker" $fullname -}}
{{- $nodes := list -}}
{{- range $i := until (int .Values.worker.replicaCount) -}}
{{- $pod := printf "%s-%d" $service $i -}}
{{- $nodes = append $nodes (printf "%s=%s.%s.%s.svc:8090" $pod $pod $service $.Release.Namespace) -}}
{{- end -}}
{{- join "," $nodes -}}
{{- end -}}

{{/*
Fail early on combinations that cannot work.
*/}}
{{- define "calcside.validate" -}}
{{- $driver := .Values.database.driver -}}
{{- if not (has $driver (list "sqlite" "postgres")) -}}
{{- fail "database.driver must be sqlite or postgres" -}}
{{- end -}}
{{- if eq $driver "sqlite" -}}
{{- if gt (int .Values.replicaCount) 1 -}}
{{- fail "SQLite supports one API replica; set replicaCount=1 or use database.driver=postgres" -}}
{{- end -}}
{{- if not (hasPrefix "/" .Values.database.sqlite.path) -}}
{{- fail "database.sqlite.path must be an absolute path inside the data volume" -}}
{{- end -}}
{{- else -}}
{{- if and (not .Values.database.postgres.dsn) (not .Values.database.postgres.existingSecret) -}}
{{- fail "database.postgres.dsn or database.postgres.existingSecret is required when database.driver=postgres" -}}
{{- end -}}
{{- end -}}
{{- if and .Values.worker.enabled (lt (int .Values.worker.replicaCount) 1) -}}
{{- fail "worker.replicaCount must be at least 1 when worker.enabled is true" -}}
{{- end -}}
{{- if not .Values.config.consoleOrigin -}}
{{- fail "config.consoleOrigin is required" -}}
{{- end -}}
{{- if and .Values.config.adminUsername (not .Values.secrets.existingSecret) (not .Values.secrets.adminPassword) -}}
{{- fail "config.adminUsername requires secrets.adminPassword (or secrets.existingSecret)" -}}
{{- end -}}
{{- if and (not .Values.config.adminUsername) .Values.secrets.adminPassword (not .Values.secrets.existingSecret) -}}
{{- fail "secrets.adminPassword requires config.adminUsername" -}}
{{- end -}}
{{- if and .Values.config.googleClientID (not .Values.secrets.existingSecret) (not .Values.secrets.googleClientSecret) -}}
{{- fail "config.googleClientID requires secrets.googleClientSecret (or secrets.existingSecret)" -}}
{{- end -}}
{{- if and .Values.ingress.enabled (not .Values.ingress.host) -}}
{{- fail "ingress.host is required when ingress.enabled is true" -}}
{{- end -}}
{{- if and .Values.ingress.preview.enabled (not (hasPrefix "*." .Values.ingress.preview.host)) -}}
{{- fail "ingress.preview.host must be a wildcard host such as *.preview.example.com" -}}
{{- end -}}
{{- end -}}
