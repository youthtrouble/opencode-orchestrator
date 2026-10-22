#!/usr/bin/env bash
# The deploy workflow's plan step: what Cloudflare runs now, and whether TARGET_SHA needs deploying.
#
# Every deploy records its commit as the Worker version's message (`wrangler deploy --message <sha>`), so the
# version live now names the last deployed commit. From it this script decides:
#   - skip: a push (TRIGGER=workflow_run) that changed nothing under remote/worker or remote/protocol since
#     that commit deploys nothing, and neither does one whose commit the live one already contains (two Remote
#     runs that finish out of order must not deploy the older commit over the newer). A manual run
#     (workflow_dispatch) always deploys, to redeploy or roll back.
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
scripts_dir=$(dirname "${BASH_SOURCE[0]}")

output() { echo "$1=$2" >>"$GITHUB_OUTPUT"; }

# The commit a version was deployed from, if its message is one this repository has.
deployed_commit() {
  local message
  # Called in a command substitution, where bash drops `set -e`, so a failed lookup is returned explicitly.
  # Read as "names no commit" it would deploy without the migration check or the older-commit guard.
  message=$(wrangler versions view "$1" --json -c "$config" | jq -r '.annotations["workers/message"] // ""') || return 1
  if [[ $message =~ ^[0-9a-f]{40}$ ]] && git -C "$repo_root" cat-file -e "$message^{commit}" 2>/dev/null; then
    echo "$message"
  fi
}

previous_version=$("$scripts_dir/live-version.sh")
deployed_sha=""
if [[ -n $previous_version ]]; then deployed_sha=$(deployed_commit "$previous_version"); fi
output previous_version "$previous_version"
output deployed_sha "$deployed_sha"

if [[ -z $deployed_sha ]]; then
  echo "::warning::The live version names no commit of this repository; deploying $TARGET_SHA without the migration check."
  output skip false
  exit 0
fi

if [[ $TRIGGER == workflow_run ]] && git -C "$repo_root" merge-base --is-ancestor "$TARGET_SHA" "$deployed_sha"; then
  echo "::notice::$TARGET_SHA is already part of the live $deployed_sha; not deploying an older commit over it."
  output skip true
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
