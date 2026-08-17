#!/usr/bin/env bash

set -euo pipefail

chart_dir="${1:-charts/chat-ui-ui}"
repo_root="$(cd "$(dirname "$0")/.." && pwd)"
tmpdir="$(mktemp -d)"
trap 'rm -rf "$tmpdir"' EXIT

assert_contains() {
  local file="$1"
  local needle="$2"

  if ! grep -Fq -- "$needle" "$file"; then
    echo "expected to find '$needle' in $file" >&2
    return 1
  fi
}

assert_not_contains() {
  local file="$1"
  local needle="$2"

  if grep -Fq -- "$needle" "$file"; then
    echo "did not expect to find '$needle' in $file" >&2
    return 1
  fi
}

public_host_render="$tmpdir/public-host.yaml"
helm template chat-ui-ui "$chart_dir" \
  --namespace chat-ui \
  --set publicHost=chat-ui.example.com \
  --set tls.secretName=chat-ui-tls \
  > "$public_host_render"
assert_contains "$public_host_render" 'host: "chat-ui.example.com"'
assert_contains "$public_host_render" '- "chat-ui.example.com"'
assert_not_contains "$public_host_render" 'host: "localhost"'
assert_contains "$public_host_render" 'path: "/.well-known/open-resource-discovery"'
assert_contains "$public_host_render" 'path: "/ord/"'
assert_contains "$public_host_render" 'path: "/ui-extensions/"'
assert_contains "$public_host_render" 'mountPath: /usr/share/nginx/html/ord/documents/chat-ui.json'
assert_contains "$public_host_render" 'add_header Access-Control-Allow-Origin "*" always;'
assert_contains "$public_host_render" 'types { }'
assert_contains "$public_host_render" 'default_type "application/json;charset=UTF-8";'
assert_contains "$public_host_render" 'add_header Cache-Control "public, max-age=300" always;'
assert_contains "$public_host_render" 'mountPath: /usr/share/nginx/html/ui-extensions/ord/index.html'
assert_contains "$public_host_render" 'platform-mesh.provider-details.navigate.v1'

default_render="$tmpdir/default.yaml"
helm template chat-ui-ui "$chart_dir" \
  --namespace chat-ui \
  > "$default_render"
assert_contains "$default_render" 'host: "localhost"'

metadata_render="$tmpdir/provider-metadata.yaml"
helm template chat-ui-pm "$repo_root/charts/chat-ui-pm-integration" \
  --set publicHost=chat-ui.example.com \
  --set publicScheme=https \
  > "$metadata_render"
assert_contains "$metadata_render" 'displayName: ORD'
assert_contains "$metadata_render" 'configUrl: "https://chat-ui.example.com/.well-known/open-resource-discovery"'
assert_contains "$metadata_render" 'detailViewExtensions:'
assert_contains "$metadata_render" 'url: "https://chat-ui.example.com/ui-extensions/ord/"'
