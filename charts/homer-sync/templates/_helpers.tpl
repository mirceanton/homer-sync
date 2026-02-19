{{/*
Expand the name of the chart.
*/}}
{{- define "homer-sync.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
The ServiceAccount name to use.
*/}}
{{- define "homer-sync.serviceAccountName" -}}
{{- .Values.serviceAccountName | default "homer-sync" }}
{{- end }}
