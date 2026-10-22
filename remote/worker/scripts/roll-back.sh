#!/usr/bin/env bash
# The deploy workflow's rollback step: after a failed run, put PREVIOUS_VERSION (the plan's) back if the deploy of
# TARGET_SHA made a new version live.
#
# DEPLOY_OUTCOME is the deploy step's outcome:
#   - success: a later step (reading the live version, the smoke check) failed with the new version live.
#   - failure: wrangler itself failed. `wrangler deploy` makes the new version live before it configures routes,
#     custom domains and triggers, so a failure there exits nonzero with the new code serving. The live version
#     is read again, and only a change from PREVIOUS_VERSION is rolled back.
#
# Run from remote/worker with wrangler on PATH and wrangler.deploy.jsonc rendered.
set -euo pipefail
: "${TARGET_SHA:?}" "${DEPLOY_OUTCOME:?}"
previous_version=${PREVIOUS_VERSION:-}
scripts_dir=$(dirname "${BASH_SOURCE[0]}")

case $DEPLOY_OUTCOME in
  success) cause="a step after the deploy of $TARGET_SHA failed" ;;
  failure)
    if ! live=$("$scripts_dir/live-version.sh"); then
      echo "::error::The deploy of $TARGET_SHA failed and the live version could not be read; check it and roll back by hand (remote/README.md)"
      exit 1
    fi
    if [[ $live == "$previous_version" ]]; then
      echo "::notice::The deploy of $TARGET_SHA failed before a new version went live; nothing to roll back."
      exit 0
    fi
    cause="the deploy of $TARGET_SHA failed after its version went live" ;;
  *)
    echo "::error::DEPLOY_OUTCOME must be success or failure, not '$DEPLOY_OUTCOME'"
    exit 1 ;;
esac

if [[ -z $previous_version ]]; then
  echo "::error::Not rolled back: $cause, and there is no single previous version to roll back to; roll back by hand (remote/README.md)"
  exit 1
fi
# The message is at most 120 characters (wrangler rollback).
wrangler rollback "$previous_version" -c wrangler.deploy.jsonc -y -m "rolled back: deploy of $TARGET_SHA failed"
echo "::error::Rolled back to version $previous_version: $cause. D1 migrations, if any ran, stay applied."
