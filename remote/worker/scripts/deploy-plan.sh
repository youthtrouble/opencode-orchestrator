#!/usr/bin/env bash
# The deploy workflow's plan step: what Cloudflare runs now, and whether TARGET_SHA needs deploying.
#
# Every deploy records its commit as the Worker version's message (`wrangler deploy --message <sha>`), so the
# version live now names the last deployed commit. From it this script decides:
#   - skip: a push (TRIGGER=workflow_run) that changed nothing under remote/worker or remote/protocol since
#     that commit deploys nothing; a manual run (workflow_dispatch) always deploys, to redeploy or roll back.
#   - refuse: a migration file that exists at both commits but differs. D1 records applied migrations by
#     name, so an edited one would never run; migrations must only ever be added.
# When the live version names no known commit (the first CI deploy, a deploy by hand, a gradual rollout)
# it deploys, and the migration check is skipped with a warning.
#
# Run from remote/worker with wrangler on PATH and wrangler.deploy.jsonc rendered. Writes `skip`,
# `previous_version` and `deployed_sha` to $GITHUB_OUTPUT.
set -euo pipefail
: "${TARGET_SHA:?}" "${TRIGGER:?}" "${GITHUB_OUTPUT:?}"

config=wrangler.deploy.jsonc
repo_root=$(git rev-parse --show-toplevel)

output() { echo "$1=$2" >>"$GITHUB_OUTPUT"; }

# The version serving all traffic now, or nothing for a Worker never deployed or mid gradual rollout.
current_version() {
  local status errors
  errors=$(mktemp)
  if ! status=$(wrangler deployments status --json -c "$config" 2>"$errors"); then
    if grep -q "has no deployments" "$errors"; then return 0; fi
    cat "$errors" >&2
    return 1
  fi
  jq -r 'if (.versions | length) == 1 and .versions[0].percentage == 100 then .versions[0].version_id else "" end' <<<"$status"
}

# The commit a version was deployed from, if its message is one this repository has.
deployed_commit() {
  local message
  message=$(wrangler versions view "$1" --json -c "$config" | jq -r '.annotations["workers/message"] // ""')
  if [[ $message =~ ^[0-9a-f]{40}$ ]] && git -C "$repo_root" cat-file -e "$message^{commit}" 2>/dev/null; then
    echo "$message"
  fi
}

previous_version=$(current_version)
deployed_sha=""
if [[ -n $previous_version ]]; then deployed_sha=$(deployed_commit "$previous_version"); fi
output previous_version "$previous_version"
output deployed_sha "$deployed_sha"

if [[ -z $deployed_sha ]]; then
  echo "::warning::The live version names no commit of this repository; deploying $TARGET_SHA without the migration check."
  output skip false
  exit 0
fi

edited=$(git -C "$repo_root" diff --no-renames --diff-filter=M --name-only "$deployed_sha" "$TARGET_SHA" -- remote/worker/migrations)
if [[ -n $edited ]]; then
  echo "::error::Migrations were edited since $deployed_sha, not added (add a new numbered file instead):"
  echo "$edited"
  exit 1
fi

changed=$(git -C "$repo_root" diff --name-only "$deployed_sha" "$TARGET_SHA" -- remote/worker remote/protocol)
if [[ $TRIGGER == workflow_run && -z $changed ]]; then
  echo "::notice::Nothing under remote/worker or remote/protocol changed since the live $deployed_sha; not deploying."
  output skip true
else
  output skip false
fi
