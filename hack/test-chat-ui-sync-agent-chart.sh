#!/usr/bin/env bash
set -euo pipefail
chart_dir="${1:-charts/chat-ui-sync-agent}"
result_dir="$(mktemp -d)"
trap 'rm -rf "$result_dir"' EXIT

helm template chat-ui-sync-agent "$chart_dir" \
  --namespace chat-ui --set syncAgentOperator.enabled=false > "$result_dir/default.yaml"
if ! grep -Fq 'namespace: "{{ .ClusterName }}"' "$result_dir/default.yaml"; then
  echo 'Default customer namespace changed' >&2
  exit 1
fi
cat > "$result_dir/isolated-values.yaml" <<'VALUES'
syncAgentOperator:
  enabled: false
publishedResources:
  naming:
    namespace: '{{ .ClusterName }}-chat-ui'
VALUES
helm template chat-ui-sync-agent "$chart_dir" \
  --namespace chat-ui -f "$result_dir/isolated-values.yaml" > "$result_dir/isolated.yaml"
if ! grep -Fq 'namespace: "{{ .ClusterName }}-chat-ui"' "$result_dir/isolated.yaml"; then
  echo 'Configured customer namespace missing' >&2
  exit 1
fi
# The control namespace and credential relation keep their existing contracts.
for output in "$result_dir/default.yaml" "$result_dir/isolated.yaml"; do
  grep -Fq 'namespace: api-syncagent' "$output"
  grep -Fq 'path: spec.credentialsSecretRef.name' "$output"
  grep -Fq 'origin: kcp' "$output"
done
