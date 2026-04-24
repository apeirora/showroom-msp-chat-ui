{{- define "chat-ui-msp-app.rewriteKubeconfig" -}}
{{- $admin := .Values.kcpKubeconfig.adminContent -}}
{{- $ws := .Values.kcpKubeconfig.providerWorkspace -}}
{{- $target := printf "%s/clusters/%s" .Values.kcpKubeconfig.inClusterServerUrl $ws -}}
{{- regexReplaceAll "server: https://[^[:space:]]+(/clusters/[A-Za-z0-9:_-]+)?" $admin (printf "server: %s" $target) -}}
{{- end -}}

{{- define "chat-ui-msp-app.syncAgentNamespace" -}}
{{- $ns := (index .Values "chat-ui-sync-agent" "publishedResources" "namespace") -}}
{{- if $ns -}}{{ $ns }}{{- else -}}{{ .Release.Namespace }}{{- end -}}
{{- end -}}
