#!/usr/bin/env bash
set -euo pipefail
chart_dir="${1:-charts/chat-ui-sync-agent}"
result_dir="$(mktemp -d)"
trap 'rm -rf "$result_dir"' EXIT
helm template chat-ui-sync-agent "$chart_dir" \
  --namespace chat-ui --set syncAgentOperator.enabled=false > "$result_dir/default.yaml"
python3 - "$result_dir/default.yaml" "$chart_dir/values.yaml" "$result_dir/custom-values.yaml" <<'PY'
import sys
from pathlib import Path
import yaml
rendered = [obj for obj in yaml.safe_load_all(Path(sys.argv[1]).read_text()) if obj]
values = yaml.safe_load(Path(sys.argv[2]).read_text())
resource = next(obj for obj in rendered if obj['kind'] == 'PublishedResource')
pattern = values['publishedResources']['naming']['namespace']
assert resource['spec']['naming']['namespace'] == pattern
assert '.Object.metadata.namespace' in pattern and 'sha3short' in pattern
assert pattern.startswith('chat-')
assert resource['metadata']['namespace'] == 'api-syncagent'
assert resource['spec']['related'][0]['object']['reference']['path'] == 'spec.credentialsSecretRef.name'
assert resource['spec']['related'][0]['origin'] == 'kcp'
Path(sys.argv[3]).write_text(yaml.safe_dump({'syncAgentOperator': {'enabled': False},
    'publishedResources': {'naming': {'namespace': pattern.replace('chat-', 'web-', 1)}}}))
PY
helm template chat-ui-sync-agent "$chart_dir" --namespace chat-ui \
  -f "$result_dir/custom-values.yaml" > "$result_dir/custom.yaml"
python3 - "$result_dir/custom.yaml" <<'PY'
import sys
import yaml
with open(sys.argv[1]) as stream:
    resource = next(obj for obj in yaml.safe_load_all(stream) if obj and obj['kind'] == 'PublishedResource')
assert resource['spec']['naming']['namespace'].startswith('web-')
PY
