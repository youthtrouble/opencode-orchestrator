#!/usr/bin/env bash
# Prints the Worker version serving all traffic now, or nothing for a Worker never deployed or mid gradual
# rollout. The deploy workflow's plan records it before a deploy; its rollback reads it again after a failed one.
#
# Run from remote/worker with wrangler on PATH and wrangler.deploy.jsonc rendered.
set -euo pipefail

errors=$(mktemp)
trap 'rm -f "$errors"' EXIT
if ! status=$(wrangler deployments status --json -c wrangler.deploy.jsonc 2>"$errors"); then
  if grep -q "has no deployments" "$errors"; then exit 0; fi
  cat "$errors" >&2
  exit 1
fi
jq -r 'if (.versions | length) == 1 and .versions[0].percentage == 100 then .versions[0].version_id else "" end' <<<"$status"
