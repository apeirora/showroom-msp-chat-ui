{{- define "chat-ui-ui.labels" -}}
app.kubernetes.io/name: chat-ui-ui
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/managed-by: Helm
app.kubernetes.io/part-of: chat-ui
app.kubernetes.io/version: {{ .Chart.AppVersion }}
{{- end }}

