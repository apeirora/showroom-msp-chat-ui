#!/usr/bin/env bash

set -euo pipefail

chart_dir="${1:-charts/chat-ui-ui}"
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

default_render="$tmpdir/default.yaml"
helm template chat-ui-ui "$chart_dir" \
  --namespace chat-ui \
  > "$default_render"
assert_contains "$default_render" 'host: "localhost"'
