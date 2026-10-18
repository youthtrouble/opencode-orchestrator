#!/bin/sh
# Builds orch and runs the parity harness against the JS runtime in this checkout.
#   parity/run.sh               every group
#   parity/run.sh --only json   one group (TestParity/<group>); --only json,usage for several
#   ORCH_JS=/path/to/orchestrator parity/run.sh   compare against another JS tree (default: this checkout's)
set -eu

module_dir=$(cd "$(dirname "$0")/.." && pwd)
repo_dir=$(dirname "$module_dir")

group=""
while [ $# -gt 0 ]; do
  case "$1" in
    --only)
      [ $# -ge 2 ] || { echo "usage: parity/run.sh [--only GROUP]" >&2; exit 2; }
      group=$2
      shift 2
      ;;
    *)
      echo "usage: parity/run.sh [--only GROUP]" >&2
      exit 2
      ;;
  esac
done

cd "$module_dir"
CGO_ENABLED=0 go build -trimpath -o bin/orch ./cmd/orch

ORCH_BIN="$module_dir/bin/orch"
ORCH_JS="${ORCH_JS:-$repo_dir/orchestrator}"
export ORCH_BIN ORCH_JS

pattern='^TestParity$'
if [ -n "$group" ]; then
  pattern="^TestParity\$/^($(echo "$group" | tr , '|'))\$"
fi
exec go test -tags parity -count=1 -v ./parity -run "$pattern"
